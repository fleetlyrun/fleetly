package k3s

// 网络成员资格入站隔离（ADR-0054 决策 1）：swarm 的"附件即互通、不附件即
// 隔离"翻译为 k8s 的成员资格 label + per-network 入站 policy。每网一条
// policy 的并集恰好等价"共享至少一网"（载体挂 {X,Y} 被 policy_X 与 policy_Y
// 同时选中，放行集并集即共享集）——组级聚合，非 per-Task/per-Run。
//
// 诚实边界（Notes/ADR 声明）：hostPort 发布载体豁免（节点级可达语义）；
// 受管域（system ns）不设隔离 policy；项目域→系统域 pod 直连维持全通；
// 出站方向维持"仅 egress:none 受限"（跨域发起由目标侧入站收口）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

const (
	// nsNameLabel 是 k8s 命名空间的标准名标记（namespaceSelector 的锚——
	// v1.21+ 自动打在一切 ns 上）。
	nsNameLabel = "kubernetes.io/metadata.name"
	// netisolatePrefix 是成员 policy 命名前缀（名段 = 成员资格复合名）。
	netisolatePrefix = "fleetly-netisolate-"
	// peerGrantPrefix 是 peer grant policy 命名前缀（名段 = 声明方+引用的哈希）。
	peerGrantPrefix = "fleetly-peer-"
	// labelPeerOwner 标记 grant policy 的声明方（挂靠方 Ensure 的 stale 清理
	// 锚——只清自己名下，其他声明方的 grant 不触碰）。
	labelPeerOwner = "fleetly.peer.owner"
)

// sha256Sum8 返回输入的 SHA-256 前 8 个十六进制字符（policy 名的消歧段）。
func sha256Sum8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// toNetIsolationPolicy 构造单网成员 policy：podSelector 选挂该网载体；
// 入站放行 = 同 Namespace 同网成员 + fleetly-system（受管 Proxy 触达
// 后端——swarm"Proxy 附件全部项目网"的等价宽放，系统域全是平台载体）+
// 引用衍生网（peerNS 非空时）的接收方 Namespace 同网成员（双向互通的挂靠
// 方半边）。
func toNetIsolationPolicy(key, peerNS string) *networkingv1.NetworkPolicy {
	ingress := []networkingv1.NetworkPolicyIngressRule{{
		From: []networkingv1.NetworkPolicyPeer{
			{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{key: "true"}}},
			{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nsNameLabel: systemNamespace}}},
		},
	}}
	if peerNS != "" {
		ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
			From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nsNameLabel: peerNS}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{key: "true"}},
			}},
		})
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:   netIsolationPolicyName(key),
			Labels: map[string]string{labelManaged: "true"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{key: "true"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     ingress,
		},
	}
}

// toNetNonePolicy 是零附件载体的 deny-all 入站（无放行规则——Proxy 也触达
// 不了，swarm 零附件不可达的逐位对齐；DNS 应答是状态化回程不受影响）。
func toNetNonePolicy() *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:   netIsolationPolicyName(labelNetNone),
			Labels: map[string]string{labelManaged: "true"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{labelNetNone: "true"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
}

// toPeerGrantPolicy 构造接收方 ns 的 grant（跨 Project 互放行的接收方半边）：
// 选中接收方该网成员，放行挂靠方 ns 携引用 label 的载体。policy 由声明方
// （唯一知情方）Ensure 持有写入；撤销经 engine isolate 剥离引用后随期望集
// 收敛删除（ADR-0013 附录 A.4 隔离生效时点 = 该次 Ensure 完成）。
func toPeerGrantPolicy(ownerProject, key string) *networkingv1.NetworkPolicy {
	ownerNS := namespacePrefix + "-" + sanitizeNamePart(ownerProject)
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: peerGrantName(ownerProject, key),
			Labels: map[string]string{
				labelManaged:   "true",
				labelPeerOwner: sanitizeNamePart(ownerProject),
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{key: "true"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nsNameLabel: ownerNS}},
					PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{key: "true"}},
				}},
			}},
		},
	}
}

// peerGrantName 是 grant policy 的确定性对象名（声明方 + 引用 key 哈希）。
func peerGrantName(ownerProject, key string) string {
	return peerGrantPrefix + sha256Sum8(ownerProject+"\x00"+key)
}

// netIsolationPolicyName 是成员 policy 的对象名（key 去前缀即 DNS 安全名段）。
func netIsolationPolicyName(key string) string {
	return netisolatePrefix + strings.TrimPrefix(key, netLabelPrefix)
}

// putNetpol 是 netpol 的 create-only 落盘（AlreadyExists 即幂等跳过——与
// egress policy 同口径；期望集的收敛删除承载 stale 面）。
func (p *Provider) putNetpol(ctx context.Context, nsName string, pol *networkingv1.NetworkPolicy) error {
	_, err := p.cli.NetworkingV1().NetworkPolicies(nsName).Create(ctx, pol, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure net isolation policy %s: %w", pol.Name, err)
	}
	return nil
}

// reconcileNetIsolation 收敛域上的成员资格隔离（Ensure 每拍）：
//   - 期望集（本拍 ws 的附件集）逐网 create-only 落成员 policy；零附件载体
//     在场则落 deny-all；
//   - 跨域引用（NetworkRefs——已批准，engine strict/isolate 既有裁决）在
//     接收方 ns 落 grant（声明方身份标记），接收方 ns 缺席则顺手建（空 ns
//     无害，对方下次 Ensure 同样会建）；
//   - 收敛删除：本 ns 内 managed netpol 不在期望集（egress deny 名保白名单；
//     他方 grant 不触碰）；接收方 ns 内自己名下 grant 的 stale。
//
// 受管域（ns.Project 空 = system ns）整体跳过：受管载体要么发布宿主端口
// （豁免面）要么只被 host 流量触达（不过 netpol 链）。
func (p *Provider) reconcileNetIsolation(ctx context.Context, ns capability.NamespaceRef, nsName string, ws []capability.Workload) error {
	if ns.Project == "" {
		return nil // 受管域不设隔离 policy（ADR-0054 决策 1 诚实边界）
	}
	type memberEntry struct{ key, peerNS string }
	members := map[string]memberEntry{}
	type refEntry struct{ recvNS, key string }
	var refs []refEntry
	needNone := false
	for _, w := range ws {
		if len(w.Publish) > 0 {
			continue // hostPort 发布载体豁免
		}
		if len(w.Networks) == 0 && len(w.NetworkRefs) == 0 {
			needNone = true
			continue
		}
		for _, n := range w.Networks {
			key := netLabelKey(ns.Project, n)
			members[key] = memberEntry{key: key}
		}
		for _, r := range w.NetworkRefs {
			key := netLabelKey(r.Namespace.Project, r.Name)
			members[key] = memberEntry{key: key, peerNS: namespaceName(r.Namespace)}
			refs = append(refs, refEntry{recvNS: namespaceName(r.Namespace), key: key})
		}
	}

	want := map[string]bool{toEgressNetpol().Name: true}
	if needNone {
		want[toNetNonePolicy().Name] = true
		if err := p.putNetpol(ctx, nsName, toNetNonePolicy()); err != nil {
			return err
		}
	}
	for _, m := range members {
		want[toNetIsolationPolicy(m.key, m.peerNS).Name] = true
		if err := p.putNetpol(ctx, nsName, toNetIsolationPolicy(m.key, m.peerNS)); err != nil {
			return err
		}
	}

	// 本 ns 收敛：managed netpol 里不在期望集的删除（他方 grant 除外——
	// 挂靠方各管各的）。
	sel := labels.Set(map[string]string{labelManaged: "true"}).String()
	pols, err := p.cli.NetworkingV1().NetworkPolicies(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return fmt.Errorf("reconcile net isolation: list policies: %w", err)
	}
	own := sanitizeNamePart(ns.Project)
	for i := range pols.Items {
		pol := &pols.Items[i]
		if want[pol.Name] {
			continue
		}
		if owner := pol.Labels[labelPeerOwner]; owner != "" && owner != own {
			continue // 其他声明方的 grant，不是本域收敛面
		}
		if err := p.cli.NetworkingV1().NetworkPolicies(nsName).Delete(ctx, pol.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("reconcile net isolation: remove stale %s: %w", pol.Name, err)
		}
	}

	// 引用半边：接收方 ns 落 grant + 声明方名下 stale 清理（集群级单次
	// list——引用全部剥离后不再走访接收方 ns，per-ns 清理会漏收）。
	wantGrants := map[string]bool{}
	for _, r := range refs {
		if err := p.ensureNamespace(ctx, r.recvNS); err != nil {
			return fmt.Errorf("reconcile net isolation: %w", err)
		}
		if err := p.putNetpol(ctx, r.recvNS, toPeerGrantPolicy(ns.Project, r.key)); err != nil {
			return err
		}
		wantGrants[peerGrantName(ns.Project, r.key)] = true
	}
	ownerSel := labels.Set(map[string]string{labelPeerOwner: own}).String()
	grants, gerr := p.cli.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{LabelSelector: ownerSel})
	if gerr != nil {
		return fmt.Errorf("reconcile net isolation: list grants: %w", gerr)
	}
	for i := range grants.Items {
		g := &grants.Items[i]
		if wantGrants[g.Name] {
			continue
		}
		if err := p.cli.NetworkingV1().NetworkPolicies(g.Namespace).Delete(ctx, g.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("reconcile net isolation: remove stale grant %s: %w", g.Name, err)
		}
	}
	return nil
}
