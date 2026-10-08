package k3s

// 载体卫生子面的 k3s 实现（capability.RuntimeHygiene，ADR-0053 决策 3）。
//
// SweepOrphanSecrets 判据（三重防误删，swarm 同款纪律）：
//   - 标签 labelManaged=true（fleetly 管辖判定——utility 材料 Secret 带
//     fleetly.utility 标签不带 managed，天然出局且已有 per-run 清理）；
//   - 不被任何现存载体 spec 引用（现役集 = 同 namespace 内全部
//     Deployment/DaemonSet 的 pod template + 独立 Pod 的 secret 引用面：
//     卷 secret/projected、envFrom、valueFrom、imagePullSecrets。只看
//     pod 列表不充分——缩容到零的 Deployment 仍引用着 secret，是现役）；
//   - 出生超过宽限窗（1h，swarm 同款：create→reference 竞态与滚动替换
//     窗的余量）。
//
// k3s 侧孤儿来源与 swarm 分叉：swarm secret 不可变、按值指纹版本化（值变
// = 新名新载体），k3s Secret 是 mutable 单名——孤儿主来源是域删除/Remove
// 的材料残留（收官批语义：Secret 是材料面不走域收敛），由本子面收口。
//
// SweepOrphanVolumes 诚实 no-op：k8s 无 docker 匿名卷对应物（镜像 VOLUME
// 遗产面不存在——emptyDir 随 pod 生命周期自动回收）；PVC 是显式数据面
//（Remove 不删、场景 3 显式数据处置），"孤儿 PVC"判据（平台 Volume 行
// 已删）超出 Provider 载体面视野——宁可漏扫不可误删。

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// orphanSecretGrace 是孤儿判定的出生宽限窗（create→reference 竞态与滚动
// 替换窗的安全余量；swarm orphanSecretGrace 同源语义）。
const orphanSecretGrace = time.Hour

// SweepOrphanSecrets 实现 capability.RuntimeHygiene（判据与限流契约见包
// 注释；删除按 namespace/名字典序——确定性，可测）。
func (p *Provider) SweepOrphanSecrets(ctx context.Context, maxDelete int) (int, error) {
	if maxDelete <= 0 {
		return 0, nil
	}
	referenced := map[string]bool{}
	collect := func(ns string, spec corev1.PodSpec) {
		for _, s := range spec.ImagePullSecrets {
			referenced[ns+"/"+s.Name] = true
		}
		for _, v := range spec.Volumes {
			if v.Secret != nil {
				referenced[ns+"/"+v.Secret.SecretName] = true
			}
			if v.Projected != nil {
				for _, src := range v.Projected.Sources {
					if src.Secret != nil {
						referenced[ns+"/"+src.Secret.Name] = true
					}
				}
			}
		}
		for _, c := range spec.Containers {
			for _, ef := range c.EnvFrom {
				if ef.SecretRef != nil {
					referenced[ns+"/"+ef.SecretRef.Name] = true
				}
			}
			for _, e := range c.Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
					referenced[ns+"/"+e.ValueFrom.SecretKeyRef.Name] = true
				}
			}
		}
	}
	deps, err := p.cli.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("k3s orphan sweep: deployment list: %w", err)
	}
	for i := range deps.Items {
		d := &deps.Items[i]
		collect(d.Namespace, d.Spec.Template.Spec)
	}
	dss, err := p.cli.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("k3s orphan sweep: daemonset list: %w", err)
	}
	for i := range dss.Items {
		ds := &dss.Items[i]
		collect(ds.Namespace, ds.Spec.Template.Spec)
	}
	pods, err := p.cli.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("k3s orphan sweep: pod list: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		collect(pod.Namespace, pod.Spec)
	}

	sel := labels.Set(map[string]string{labelManaged: "true"}).AsSelector()
	secrets, err := p.cli.CoreV1().Secrets("").List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return 0, fmt.Errorf("k3s orphan sweep: secret list: %w", err)
	}
	cutoff := time.Now().Add(-orphanSecretGrace)
	type orphanRef struct{ ns, name string }
	var orphans []orphanRef
	for i := range secrets.Items {
		sec := &secrets.Items[i]
		if referenced[sec.Namespace+"/"+sec.Name] {
			continue
		}
		if sec.CreationTimestamp.IsZero() || sec.CreationTimestamp.After(cutoff) {
			continue // 无出生事实或宽限窗内：一律不动
		}
		orphans = append(orphans, orphanRef{ns: sec.Namespace, name: sec.Name})
	}
	sort.Slice(orphans, func(i, j int) bool {
		if orphans[i].ns != orphans[j].ns {
			return orphans[i].ns < orphans[j].ns
		}
		return orphans[i].name < orphans[j].name
	})

	deleted := 0
	var lastErr error
	for _, o := range orphans {
		if deleted >= maxDelete {
			break
		}
		if err := p.cli.CoreV1().Secrets(o.ns).Delete(ctx, o.name, metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				deleted++ // 已不存在：幂等目标已达成
				continue
			}
			lastErr = err // 单体失败不中断（下一拍重扫续清）
			continue
		}
		deleted++
	}
	if lastErr != nil {
		return deleted, fmt.Errorf("k3s orphan sweep: secret remove: %w", lastErr)
	}
	return deleted, nil
}

// SweepOrphanVolumes 实现 capability.RuntimeHygiene 卷面：诚实 no-op
// （判据论证见包注释——k8s 无匿名卷遗产，PVC 是显式数据面）。
func (p *Provider) SweepOrphanVolumes(ctx context.Context, maxDelete int) (int, error) {
	return 0, nil
}

// SweepOrphanPeerGrants 实现 capability.RuntimeHygiene 的 peer grant 面
// （ADR-0055 决策 3，ADR-0054 决策 1 残留留口的兑现）：清扫声明方已消亡的
// 接收方残留 grant。残留面：grant 落接收方 ns、由声明方 Ensure 持有——
// 声明方 App 删除（Remove 只拆本 ns 非 grant netpol）或项目删除（ns 拆除）
// 后该 (owner, domain) 再无 Ensure 拍，grant 永残留为指向空集的 no-op。
//
// 判据三信号全满足才删（宁可漏扫不可误删，与 Secret/卷清扫同纪律）：
//   - 声明方 ns 无活 pod 持有该 grant 选择器 key（pod 对象任意相位均计
//     ——CrashLoop/终止中也在场，滚动窗安全）；
//   - 声明方 ns 无该 key 的成员 policy——声明方意图的 Provider 可见锚：
//     成员 policy 与 grant 同拍同源创建（policy 先于 grant）、仅由声明方
//     Remove/Ensure 收敛删除，在场即意图可能仍活（如声明方载体缩容到零、
//     app 域无周期 Ensure 拍），保留；
//   - 声明方 ns 不存在时两者平凡成立（项目删除形态；ns 名 = 项目 ID 公式，
//     ULID 永不复用，无同名复活面）。
//
// 无出生宽限窗：出生竞态由成员 policy 信号覆盖（结构判据非 eventual）。
// 成员 policy 核查刻意放在删除前最后一步（TOCTOU 收窄）——与并发声明方
// Ensure 的微秒窗残余是诚实边界：删除方向恒为"更早拒绝"（安全侧），自愈
// 面 = 声明方下一次部署重铸 grant。
func (p *Provider) SweepOrphanPeerGrants(ctx context.Context, maxDelete int) (int, error) {
	if maxDelete <= 0 {
		return 0, nil
	}
	grants, err := p.cli.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{LabelSelector: labelPeerOwner})
	if err != nil {
		return 0, fmt.Errorf("k3s orphan grant sweep: list: %w", err)
	}
	type orphanGrant struct{ ns, name, ownerNS, key string }
	var orphans []orphanGrant
	liveKeys := map[string]map[string]bool{} // 声明方 ns → 活成员键集（拍内缓存）
	for i := range grants.Items {
		g := &grants.Items[i]
		key := selectorKey(g.Spec.PodSelector.MatchLabels)
		if key == "" {
			continue // 未知选择器形状不参与清扫（保守跳过，与收敛删除同口径）
		}
		ownerNS := namespacePrefix + "-" + g.Labels[labelPeerOwner]
		keys, ok := liveKeys[ownerNS]
		if !ok {
			keys, err = p.liveMembershipKeys(ctx, ownerNS) // ns 缺失 = 空 pod 集，平凡成立
			if err != nil {
				return 0, fmt.Errorf("k3s orphan grant sweep: %w", err)
			}
			liveKeys[ownerNS] = keys
		}
		if keys[key] {
			continue // 声明方仍有活 pod 持 key
		}
		orphans = append(orphans, orphanGrant{ns: g.Namespace, name: g.Name, ownerNS: ownerNS, key: key})
	}
	sort.Slice(orphans, func(i, j int) bool {
		if orphans[i].ns != orphans[j].ns {
			return orphans[i].ns < orphans[j].ns
		}
		return orphans[i].name < orphans[j].name
	})

	deleted := 0
	var lastErr error
	for _, o := range orphans {
		if deleted >= maxDelete {
			break
		}
		// 删除前最后核查（TOCTOU 收窄）：成员 policy 在场 = 声明方意图仍活，保留。
		_, err := p.cli.NetworkingV1().NetworkPolicies(o.ownerNS).Get(ctx, netIsolationPolicyName(o.key), metav1.GetOptions{})
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			lastErr = err
			continue
		}
		if err := p.cli.NetworkingV1().NetworkPolicies(o.ns).Delete(ctx, o.name, metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				deleted++ // 已不存在：幂等目标已达成
				continue
			}
			lastErr = err // 单体失败不中断（下一拍重扫续清）
			continue
		}
		deleted++
	}
	if lastErr != nil {
		return deleted, fmt.Errorf("k3s orphan grant sweep: remove: %w", lastErr)
	}
	return deleted, nil
}
