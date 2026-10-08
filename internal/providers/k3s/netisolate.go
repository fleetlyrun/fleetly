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
	"reflect"
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
	// labelPeerOwner 标记 grant policy 的声明方（挂靠方 Ensure 的收敛锚——
	// 只清自己名下，其他声明方的 grant 不触碰）。
	labelPeerOwner = "fleetly.peer.owner"
	// labelPeerDomain 标记 grant 的声明方域（app 轴值——per-domain 键控，
	// 同项目多 App 的 grant 互不误删）。
	labelPeerDomain = "fleetly.peer.domain"
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
// 方半边）。备份/恢复链不在此放行面：utility pod 走 hostNetwork（节点本机
// 流量过 per-pod FW 链的 src-type LOCAL 无条件放行规则），可达性不依赖
// CNI 对新 pod 成员 label 的 ipset 准入传播——秒级一次性载体赌不起传播
// 时序（staging k3s 实证 kube-router 传播分钟级，ADR-0055）。
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
// 选中接收方该网成员，放行挂靠方 ns 携引用 label 的载体。grant 由声明方
// （唯一知情方）Ensure 持有写入、**按声明方域（app 轴）键控**：收敛是纯意
// 图集（本拍 refs 不含即删——grant 只放行携 key 的载体，删除只会更早拒
// 绝，安全方向），撤销的隔离 Ensure 当拍即清双侧（ADR-0013 附录 A.4 隔离
// 生效时点 = 该次 Ensure 完成），不等旧 pod 终止、不依赖后续 Ensure。peer
// 引用只出自 App 域——per-app 键控使同项目多 App 互不误删。
func toPeerGrantPolicy(ownerProject, domain, key string) *networkingv1.NetworkPolicy {
	ownerNS := namespacePrefix + "-" + sanitizeNamePart(ownerProject)
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: peerGrantName(ownerProject, domain, key),
			Labels: map[string]string{
				labelManaged:    "true",
				labelPeerOwner:  sanitizeNamePart(ownerProject),
				labelPeerDomain: sanitizeNamePart(domain),
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

// peerGrantName 是 grant policy 的确定性对象名（声明方项目 + 声明方域 +
// 引用 key 哈希——per-domain 键控，同项目多 App 各自持有互不碰撞）。
func peerGrantName(ownerProject, domain, key string) string {
	return peerGrantPrefix + sha256Sum8(ownerProject+"\x00"+domain+"\x00"+key)
}

// domainAxisValue 返回 NamespaceRef 的域主体轴值（grant 的声明方域键——
// App 域 = app ID；Task/Database/Browse 域不携带 peer 引用，值仅作键用）。
func domainAxisValue(ns capability.NamespaceRef) string {
	switch {
	case ns.App != "":
		return ns.App
	case ns.Task != "":
		return ns.Task
	case ns.Database != "":
		return ns.Database
	case ns.Browse != "":
		return ns.Browse
	}
	return "system"
}

// netIsolationPolicyName 是成员 policy 的对象名（key 去前缀即 DNS 安全名段）。
func netIsolationPolicyName(key string) string {
	return netisolatePrefix + strings.TrimPrefix(key, netLabelPrefix)
}

// putNetpol 是 netpol 的 create-or-update 落盘：相等（名称 + 语义 spec +
// managed 标记逐位一致）零写；形状漂移（平台升级改放行集——如 ADR-0055
// 的 utility 放行）即更新收敛——create-only 会让存量 policy 永锁旧形态
//（staging 真机实证：新 FROM 规则不落地）。stale 面仍由期望集收敛删除
// 承载。
func (p *Provider) putNetpol(ctx context.Context, nsName string, pol *networkingv1.NetworkPolicy) error {
	existing, err := p.cli.NetworkingV1().NetworkPolicies(nsName).Get(ctx, pol.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, cerr := p.cli.NetworkingV1().NetworkPolicies(nsName).Create(ctx, pol, metav1.CreateOptions{})
		if cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return fmt.Errorf("ensure net isolation policy %s: %w", pol.Name, cerr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("ensure net isolation policy %s: %w", pol.Name, err)
	}
	if reflect.DeepEqual(existing.Spec, pol.Spec) && existing.Labels[labelManaged] == pol.Labels[labelManaged] {
		return nil // 幂等重放零写
	}
	_, err = p.cli.NetworkingV1().NetworkPolicies(nsName).Update(ctx, pol, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("ensure net isolation policy %s: %w", pol.Name, err)
	}
	return nil
}

// reconcileNetIsolation 收敛域上的成员资格隔离（Ensure 每拍）：
//   - 期望面（create-only）：本拍 ws 的附件集逐网落成员 policy（引用衍生网带
//     接收方 ns 放行规则）；零附件载体在场则落 deny-all；跨域引用在接收方
//     ns 落 grant（声明方身份标记；接收方 ns 缺席则顺手建——空 ns 无害）。
//   - 收敛删除（活 pod label ∪ 本拍期望键）：成员 policy 与 grant 是**项目级
//     共享资源**——同项目多域（App/Task/Database/Browse）各自 Ensure，按
//     本拍期望集单边删除会让一域收敛掉别域成员的 policy（task drill 咬出
//     的实锤）；而仅按活 pod 删除会在全新部署的同一拍自噬（载体在
//     reconcileNetIsolation 之后才创建，liveKeys 恒空——policy 建完即删，
//     一次部署成的项目再无 Ensure 补建，隔离面永久缺失；e2e 跨 ns 探针
//     咬出的第二轮实锤）。判据取并集：policy 选择器 key 既无活 pod 持有
//     又不在本拍期望键集 → 删——跨域安全（他域成员 pod 在场即保留）、滚动
//     窗口安全（替换中的旧 pod 仍持有 label）、新部署安全（批内键即意图）、
//     重启安全（无内存态）。
//
// 受管域（ns.Project 空 = system ns）整体跳过：受管载体要么发布宿主端口
// （豁免面）要么只被 host 流量触达（不过 netpol 链）。
func (p *Provider) reconcileNetIsolation(ctx context.Context, ns capability.NamespaceRef, nsName string, ws []capability.Workload) error {
	if ns.Project == "" {
		return nil // 受管域不设隔离 policy（ADR-0054 决策 1 诚实边界）
	}
	intent := map[string]bool{} // 本拍期望键集（载体在路上——删除判据的意图半边）
	domain := domainAxisValue(ns)
	wantGrants := map[string]bool{}
	for _, w := range ws {
		if len(w.Publish) > 0 {
			continue // hostPort 发布载体豁免
		}
		if len(w.Networks) == 0 && len(w.NetworkRefs) == 0 {
			intent[labelNetNone] = true
			if err := p.putNetpol(ctx, nsName, toNetNonePolicy()); err != nil {
				return err
			}
			continue
		}
		for _, n := range w.Networks {
			key := netLabelKey(ns.Project, n)
			intent[key] = true
			if err := p.putNetpol(ctx, nsName, toNetIsolationPolicy(key, "")); err != nil {
				return err
			}
		}
		for _, r := range w.NetworkRefs {
			key := netLabelKey(r.Namespace.Project, r.Name)
			recvNS := namespaceName(r.Namespace)
			intent[key] = true
			if err := p.putNetpol(ctx, nsName, toNetIsolationPolicy(key, recvNS)); err != nil {
				return err
			}
			// 接收方半边：grant（声明方域键控——纯意图集收敛，撤销当拍即清）。
			if err := p.ensureNamespace(ctx, recvNS); err != nil {
				return fmt.Errorf("reconcile net isolation: %w", err)
			}
			if err := p.putNetpol(ctx, recvNS, toPeerGrantPolicy(ns.Project, domain, key)); err != nil {
				return err
			}
			wantGrants[peerGrantName(ns.Project, domain, key)] = true
		}
	}

	// 收敛删除：活成员资格键集（本 ns managed pod 的并集）∪ 本拍期望键集。
	liveKeys, err := p.liveMembershipKeys(ctx, nsName)
	if err != nil {
		return err
	}
	keep := func(key string) bool { return key == "" || intent[key] || liveKeys[key] }
	sel := labels.Set(map[string]string{labelManaged: "true"}).String()
	pols, err := p.cli.NetworkingV1().NetworkPolicies(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return fmt.Errorf("reconcile net isolation: list policies: %w", err)
	}
	for i := range pols.Items {
		pol := &pols.Items[i]
		if pol.Name == toEgressNetpol().Name {
			continue // egress deny 的收敛归 reconcileEgressNetpol
		}
		if pol.Labels[labelPeerOwner] != "" {
			continue // 本 ns 收到的他方 grant：挂靠方收敛面（其项目消亡即残留 no-op，诚实边界）
		}
		if keep(selectorKey(pol.Spec.PodSelector.MatchLabels)) {
			continue
		}
		if err := p.cli.NetworkingV1().NetworkPolicies(nsName).Delete(ctx, pol.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("reconcile net isolation: remove stale %s: %w", pol.Name, err)
		}
	}

	// 挂靠方名下 grant 的收敛（集群级 owner 扫——grant 落在接收方 ns，本 ns
	// 的 Ensure 不走访）：**纯意图集 + 声明方域**——本域 grant 不在本拍期望
	// 集即删（撤销当拍清）；他域（同项目其他 App）与他方项目不触碰。
	own := sanitizeNamePart(ns.Project)
	ownDomain := sanitizeNamePart(domain)
	ownerSel := labels.Set(map[string]string{labelPeerOwner: own}).String()
	grants, gerr := p.cli.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{LabelSelector: ownerSel})
	if gerr != nil {
		return fmt.Errorf("reconcile net isolation: list grants: %w", gerr)
	}
	for i := range grants.Items {
		g := &grants.Items[i]
		if g.Labels[labelPeerDomain] != ownDomain {
			continue // 同项目他域（其他 App）的 grant：各域各管
		}
		if wantGrants[g.Name] {
			continue
		}
		if err := p.cli.NetworkingV1().NetworkPolicies(g.Namespace).Delete(ctx, g.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("reconcile net isolation: remove stale grant %s: %w", g.Name, err)
		}
	}
	return nil
}

// liveMembershipKeys 返回 namespace 内全部 managed pod 持有的成员资格键集
// （含 Pending/ terminating——滚动替换窗口的安全锚：旧 pod 在位即 policy
// 在位）。零附件锚（fleetly.net.none）同集收录。
func (p *Provider) liveMembershipKeys(ctx context.Context, nsName string) (map[string]bool, error) {
	sel := labels.Set(map[string]string{labelManaged: "true"}).String()
	pods, err := p.cli.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("reconcile net isolation: list pods: %w", err)
	}
	live := map[string]bool{}
	for i := range pods.Items {
		for k := range pods.Items[i].Labels {
			if strings.HasPrefix(k, netLabelPrefix) || k == labelNetNone {
				live[k] = true
			}
		}
	}
	return live, nil
}

// selectorKey 取单键选择器的键（成员/零附件/grant policy 的选择器都是
// 单键形态；非单键的未知形状不参与收敛删除——保守跳过）。
func selectorKey(match map[string]string) string {
	if len(match) != 1 {
		return ""
	}
	for k := range match {
		return k
	}
	return ""
}
