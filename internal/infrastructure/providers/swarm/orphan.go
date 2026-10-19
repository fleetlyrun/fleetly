package swarm

// 孤儿载体清扫（收尾批 E29-1 + R06-M7，capability.RuntimeHygiene 子面）。
//
// 缺陷背景：Secret 载体按值指纹版本化（值变 = 新名新载体，swarm secret
// 不可变），旧版本残留此前的策略是"只登记"；zot 逐重启新盐时期（E28 前）
// 每拍 Ensure 都铸新 htpasswd 载体，staging 积压 1300+ 孤儿。Config 载体
// 用同一版本化命名公式（fleetly-sec-<name>-<fp8>），每次 Config 内容变更
// 净增一枚且旧版本永不回收——同族泄漏，本清扫面一并收口（R06-M7）。
//
// 孤儿判定（标签优先 + 非现役集比对 + 出生窗宽限，三重防误删）：
//   - 标签 labelManaged=true（fleetly 管辖判定，服务列表同一标记真源）；
//   - 名字命中载体前缀（belt-and-braces：标签是必要非充分锚）；
//   - 不被任何现存受管服务的 spec 引用（现役集 = ServiceList 的
//     ContainerSpec.Secrets ∪ ContainerSpec.Configs 引用名集——平台侧现役
//     真源是 Ensure 下发，载体被引用即是现役的既成事实）；
//   - 出生超过宽限窗（Secret 1h / Config 7d 两个常量；CreatedAt 兜住
//     create→reference 的同拍窗口与滚动替换窗：滚动期服务 spec 已指向新
//     载体，但旧载体仍被在途旧任务挂载，回滚重建任务也需要它——宽限窗内
//     一律不动）。无出生事实不判孤儿。
//
// 限流=maxDelete 单次上限，两类载体共用一个预算（janitor 10 分钟节拍传
// 预算）；删除按 (名字, 载体类别) 字典序（确定性，可测）；单体删除失败不
// 中断整轮（记尾错上抛，下一拍重扫幂等续清）；NotFound 视作已清（幂等目标
// 已达成）。

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// orphanSecretGrace 是孤儿判定的出生宽限窗（create→reference 竞态与滚动
// 替换窗的安全余量，远大于两者的真实时长量级）。
const orphanSecretGrace = time.Hour

// configCarrierGrace 是 Config 载体的出生宽限窗——刻意比 Secret 宽（R06-M7
// 的三重防误删里前两重已足以识别管辖与载体形态，现役引用集是主判据；宽限
// 只是兜"已落盘但服务 spec 还没写引用"的窗口。Config 版本迭代节奏慢于
// Secret（值变更才新增版本），漏扫的代价远小于误删，故取 7 天）。
const configCarrierGrace = 7 * 24 * time.Hour

// configCarrierPrefix 是 Config 载体的命名前缀：与 secretCarrierName 同公式
// （ensureConfigs 复用版本化命名；R06-M7 判据面显式成文）。
const configCarrierPrefix = secretCarrierPrefix

// 载体类别标签（清扫日志归因与排序平局消歧）。
const (
	carrierKindSecret = "secret"
	carrierKindConfig = "config"
)

// SweepOrphanSecrets 实现 capability.RuntimeHygiene（孤儿判定与限流契约
// 见包注释）。方法名沿用既有契约面（capability.RuntimeHygiene 冻结），实际
// 覆盖 Secret 与 Config 两类载体——返回值是两类删除数之和。
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
	referencedSecrets := map[string]bool{}
	referencedConfigs := map[string]bool{}
	for i := range svcs.Items {
		cs := svcs.Items[i].Spec.TaskTemplate.ContainerSpec
		if cs == nil {
			continue
		}
		for _, ref := range cs.Secrets {
			if ref != nil {
				referencedSecrets[ref.SecretName] = true
			}
		}
		for _, ref := range cs.Configs {
			if ref != nil {
				referencedConfigs[ref.ConfigName] = true
			}
		}
	}
	now := time.Now()

	orphans, err := p.orphanSecretCarriers(ctx, referencedSecrets, now.Add(-orphanSecretGrace))
	if err != nil {
		return 0, err
	}
	configs, err := p.orphanConfigCarriers(ctx, referencedConfigs, now.Add(-configCarrierGrace))
	if err != nil {
		return 0, err
	}
	orphans = append(orphans, configs...)
	sort.SliceStable(orphans, func(i, j int) bool {
		if orphans[i].name != orphans[j].name {
			return orphans[i].name < orphans[j].name
		}
		return orphans[i].kind < orphans[j].kind
	})

	deleted, deletedConfigs := 0, 0
	var lastErr error
	var lastKind string
	for _, o := range orphans {
		if deleted >= maxDelete {
			break
		}
		var err error
		if o.kind == carrierKindConfig {
			_, err = p.cli.ConfigRemove(ctx, o.id, client.ConfigRemoveOptions{})
		} else {
			_, err = p.cli.SecretRemove(ctx, o.id, client.SecretRemoveOptions{})
		}
		if err != nil {
			if isNotFound(err) {
				deleted++ // 已不存在：幂等目标已达成
				continue
			}
			lastErr = err // 单体失败不中断（下一拍重扫续清）
			lastKind = o.kind
			continue
		}
		deleted++
		if o.kind == carrierKindConfig {
			deletedConfigs++
		}
	}
	if deletedConfigs > 0 {
		// 可观测面（R06-M7）：janitor 的聚合日志按 Secret 口径命名，config
		// 面单独留一行归因计数（不动 assembly 的日志面）。
		slog.Info("swarm orphan sweep: reaped orphan config carriers", "count", deletedConfigs)
	}
	if lastErr != nil {
		return deleted, fmt.Errorf("swarm orphan sweep: %s carrier remove: %w", lastKind, lastErr)
	}
	return deleted, nil
}

// orphanSecretCarriers 列出非现役且过宽限窗的受管 Secret 载体。
func (p *Provider) orphanSecretCarriers(ctx context.Context, referenced map[string]bool, cutoff time.Time) ([]carrierRef, error) {
	res, err := p.cli.SecretList(ctx, client.SecretListOptions{
		Filters: client.Filters{}.Add("label", labelManaged+"=true"),
	})
	if err != nil {
		return nil, fmt.Errorf("swarm orphan sweep: secret list: %w", err)
	}
	var orphans []carrierRef
	for i := range res.Items {
		sec := res.Items[i]
		name := sec.Spec.Name
		if !strings.HasPrefix(name, secretCarrierPrefix) || referenced[name] {
			continue
		}
		if sec.CreatedAt.IsZero() || sec.CreatedAt.After(cutoff) {
			continue // 无出生事实或宽限窗内：一律不动
		}
		orphans = append(orphans, carrierRef{kind: carrierKindSecret, id: sec.ID, name: name})
	}
	return orphans, nil
}

// orphanConfigCarriers 列出非现役且过宽限窗的受管 Config 载体（R06-M7：
// 内容寻址版本化的 config 每次值变更净增一枚，旧版本必须可回收）。
func (p *Provider) orphanConfigCarriers(ctx context.Context, referenced map[string]bool, cutoff time.Time) ([]carrierRef, error) {
	res, err := p.cli.ConfigList(ctx, client.ConfigListOptions{
		Filters: client.Filters{}.Add("label", labelManaged+"=true"),
	})
	if err != nil {
		return nil, fmt.Errorf("swarm orphan sweep: config list: %w", err)
	}
	var orphans []carrierRef
	for i := range res.Items {
		cfg := res.Items[i]
		name := cfg.Spec.Name
		if !strings.HasPrefix(name, configCarrierPrefix) || referenced[name] {
			continue
		}
		if cfg.CreatedAt.IsZero() || cfg.CreatedAt.After(cutoff) {
			continue // 无出生事实或宽限窗内：一律不动
		}
		orphans = append(orphans, carrierRef{kind: carrierKindConfig, id: cfg.ID, name: name})
	}
	return orphans, nil
}

// carrierRef 是待清扫孤儿载体的引用面（id + 名 + 类别：删除走 ID，
// 排序与日志走名/类别）。
type carrierRef struct {
	kind string
	id   string
	name string
}

// SweepOrphanPeerGrants 实现 capability.RuntimeHygiene 的 peer grant 面：
// 诚实 no-op。swarm 的 peer 互放行是网络附件语义（docker 无独立 grant
// 载体对象）——剥离由 engine 隔离环（ADR-0013 附录 A.4）原子承载，无
// 残留面；k3s netpol 形态的残留清扫（ADR-0055 决策 3）在 swarm 无对应物。
func (p *Provider) SweepOrphanPeerGrants(context.Context, int) (int, error) {
	return 0, nil
}
