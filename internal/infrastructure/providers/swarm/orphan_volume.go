package swarm

// 匿名孤儿卷清扫（N2 评审 P2-4，capability.RuntimeHygiene 子面的卷面）。
//
// 缺陷背景：db 模板镜像的 VOLUME 遗产——镜像声明 VOLUME 而平台挂的是命名
// 卷时，daemon 在 VOLUME 路径自动铸匿名卷（64 位小写 hex 名、无 label），
// swarm 任务替换不回收旧容器的匿名卷，staging manager 已积 1680 枚。
//
// 法律依据（架构 §8 孤儿词条后半句）："孤儿=对不上账的载体只登记、永不
// 自动删"不适用本面——匿名卷是平台工作负载的自建副产物，由看门狗
//（retention janitor）收口，不属孤儿。判据必须把删除面收敛到该副产物：
//
// 判据（五重全满足才删，宁可漏扫不可误删）：
//   - 名字是 64 位小写 hex（docker 匿名卷命名规律；命名卷天然出局——
//     fleetly-vol-* 平台命名卷、compose/swarm 具名载体都不是该形态）；
//   - 悬空（daemon 侧 dangling=true 过滤 = 无任何容器引用）；
//   - 出生超过年龄窗（缺省 7d，"数据其实还有用"的保留窗；无出生事实
//     不判孤儿，与 Secret 清扫同款保守）；
//   - 无任何 label（用户/编排器命名或标记过的卷绝不碰）；
//   - 仅控制面节点（本 Provider 的 cli 即 manager 本机 daemon；docker 卷
//     是 node-local 面，跨节点清点超射程——worker 节点泄漏不在本扫内）。
//
// 限流=maxDelete 单次上限（调用方 janitor 10 分钟节拍传预算），删除按名
// 字典序（确定性，可测）；单体删除失败不中断整轮（记尾错上抛，下一拍
// 重扫幂等续清）；NotFound 视作已清（幂等目标已达成）。

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/moby/moby/client"
)

// orphanVolumeAge 是匿名孤儿卷判定的年龄窗（保留窗：想保住某枚匿名卷的
// 操作者在此窗内命名化或导出；窗外即平台副产物收口。7d 与终态 Task 载体
// 清扫窗同文化）。
const orphanVolumeAge = 7 * 24 * time.Hour

// SweepOrphanVolumes 实现 capability.RuntimeHygiene 卷面（判据与限流契约
// 见包注释；列表级 dangling 过滤交给 daemon 服务端，本侧只做形态收口）。
func (p *Provider) SweepOrphanVolumes(ctx context.Context, maxDelete int) (int, error) {
	if maxDelete <= 0 {
		return 0, nil
	}
	res, err := p.cli.VolumeList(ctx, client.VolumeListOptions{
		Filters: client.Filters{}.Add("dangling", "true"),
	})
	if err != nil {
		return 0, fmt.Errorf("swarm volume sweep: volume list: %w", err)
	}
	cutoff := time.Now().Add(-orphanVolumeAge)
	var orphans []string
	for i := range res.Items {
		v := &res.Items[i]
		if !isAnonymousVolumeName(v.Name) || len(v.Labels) > 0 || v.ClusterVolume != nil {
			continue // 非匿名形态 / 有标记面 / CSI 集群卷：一律不碰
		}
		birth, err := time.Parse(time.RFC3339Nano, v.CreatedAt)
		if err != nil || birth.After(cutoff) {
			continue // 无出生事实或窗内：一律不动
		}
		orphans = append(orphans, v.Name)
	}
	sort.Strings(orphans)

	deleted := 0
	var lastErr error
	for _, name := range orphans {
		if deleted >= maxDelete {
			break
		}
		if _, err := p.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
			if isNotFound(err) {
				deleted++ // 已不存在：幂等目标已达成
				continue
			}
			lastErr = err // 单体失败不中断（下一拍重扫续清；in-use 冲突 = 卷已获新引用，幸存即正确）
			continue
		}
		deleted++
	}
	if lastErr != nil {
		return deleted, fmt.Errorf("swarm volume sweep: volume remove: %w", lastErr)
	}
	return deleted, nil
}

// isAnonymousVolumeName 报告名字是否 docker 匿名卷形态（64 位小写 hex）。
func isAnonymousVolumeName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
