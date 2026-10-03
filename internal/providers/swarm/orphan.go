package swarm

// 孤儿 Secret 载体清扫（收尾批 E29-1，capability.RuntimeHygiene 子面）。
//
// 缺陷背景：Secret 载体按值指纹版本化（值变 = 新名新载体，swarm secret
// 不可变），旧版本残留此前的策略是"只登记"；zot 逐重启新盐时期（E28 前）
// 每拍 Ensure 都铸新 htpasswd 载体，staging 积压 1300+ 孤儿。
//
// 孤儿判定（标签优先 + 非现役集比对 + 出生窗宽限，三重防误删）：
//   - 标签 labelManaged=true（fleetly 管辖判定，服务列表同一标记真源）；
//   - 名字命中 Secret 载体前缀（belt-and-braces：标签是必要非充分锚）；
//   - 不被任何现存受管服务的 spec 引用（现役集 = ServiceList 的
//     ContainerSpec.Secrets 引用名集——平台侧现役真源是 Ensure 下发，
//     载体被引用即是现役的既成事实）；
//   - 出生超过宽限窗（ CreatedAt 兜住 create→reference 的同拍窗口与滚动
//     替换窗：滚动期服务 spec 已指向新载体，但旧载体仍被在途旧任务挂载，
//     回滚重建任务也需要它——宽限窗内一律不动）。无出生事实不判孤儿。
//
// 限流=maxDelete 单次上限（调用方 janitor 10 分钟节拍传预算），删除按名
// 字典序（确定性，可测）；单体删除失败不中断整轮（记尾错上抛，下一拍
// 重扫幂等续清）；NotFound 视作已清（幂等目标已达成）。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// orphanSecretGrace 是孤儿判定的出生宽限窗（create→reference 竞态与滚动
// 替换窗的安全余量，远大于两者的真实时长量级）。
const orphanSecretGrace = time.Hour

// SweepOrphanSecrets 实现 capability.RuntimeHygiene（孤儿判定与限流契约
// 见包注释；平台侧不需要下发现役集——现存服务的引用关系即现役既成事实）。
func (p *Provider) SweepOrphanSecrets(ctx context.Context, maxDelete int) (int, error) {
	if maxDelete <= 0 {
		return 0, nil
	}
	svcs, err := p.cli.ServiceList(ctx, client.ServiceListOptions{
		Filters: client.Filters{}.Add("label", labelManaged+"=true"),
	})
	if err != nil {
		return 0, fmt.Errorf("swarm orphan sweep: service list: %w", err)
	}
	referenced := map[string]bool{}
	for i := range svcs.Items {
		cs := svcs.Items[i].Spec.TaskTemplate.ContainerSpec
		if cs == nil {
			continue
		}
		for _, ref := range cs.Secrets {
			if ref != nil {
				referenced[ref.SecretName] = true
			}
		}
	}

	res, err := p.cli.SecretList(ctx, client.SecretListOptions{
		Filters: client.Filters{}.Add("label", labelManaged+"=true"),
	})
	if err != nil {
		return 0, fmt.Errorf("swarm orphan sweep: secret list: %w", err)
	}
	cutoff := time.Now().Add(-orphanSecretGrace)
	var orphans []swarmSecretRef
	for i := range res.Items {
		sec := res.Items[i]
		name := sec.Spec.Name
		if !strings.HasPrefix(name, secretCarrierPrefix) || referenced[name] {
			continue
		}
		if sec.CreatedAt.IsZero() || sec.CreatedAt.After(cutoff) {
			continue // 无出生事实或宽限窗内：一律不动
		}
		orphans = append(orphans, swarmSecretRef{id: sec.ID, name: name})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].name < orphans[j].name })

	deleted := 0
	var lastErr error
	for _, o := range orphans {
		if deleted >= maxDelete {
			break
		}
		if _, err := p.cli.SecretRemove(ctx, o.id, client.SecretRemoveOptions{}); err != nil {
			if isNotFound(err) {
				deleted++ // 已不存在：幂等目标已达成
				continue
			}
			lastErr = err // 单体失败不中断（下一拍重扫续清）
			continue
		}
		deleted++
	}
	if lastErr != nil {
		return deleted, fmt.Errorf("swarm orphan sweep: secret remove: %w", lastErr)
	}
	return deleted, nil
}

// swarmSecretRef 是待清扫孤儿载体的引用面（id + 名双持：删除走 ID，
// 排序与日志走名）。
type swarmSecretRef struct {
	id   string
	name string
}
