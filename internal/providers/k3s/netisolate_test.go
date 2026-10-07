package k3s

// 网络成员资格入站隔离单测（ADR-0054 决策 1）：成员资格 label 翻译四形态、
// per-network 成员 policy 收敛（组级聚合）、零附件 deny-all、peer grant
// 双侧形态与撤销收敛、受管域跳过、hostPort 发布豁免。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestNetMembershipLabels：附件集翻译四形态——同域网、跨域引用、零附件
// none 锚、Publish 豁免；同域与跨域对同一 (project, name) 推导同一 key
// （跨 ns 放行规则两侧对齐的前提）。
func TestNetMembershipLabels(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}

	// 同域附件：每网一 label，key 含项目消歧哈希。
	w := capability.Workload{ID: "w1", Process: "api", Networks: []string{"default", "taskgrp-pipe"}}
	labels := netMembershipLabels(ns, w)
	assert.Len(t, labels, 2)
	assert.Equal(t, "true", labels[netLabelKey("shop", "default")])
	assert.Equal(t, "true", labels[netLabelKey("shop", "taskgrp-pipe")])

	// 跨域引用：key 按引用自身的 (project, name) 推导——与接收方自己的网
	// 同公式（两侧同 key 是互放行规则的锚）。
	ref := capability.NetworkRef{
		Namespace: capability.NamespaceRef{Team: "t2", Project: "partner"},
		Name:      "default",
	}
	w = capability.Workload{ID: "w1", Process: "api", NetworkRefs: []capability.NetworkRef{ref}}
	labels = netMembershipLabels(ns, w)
	assert.Len(t, labels, 1)
	assert.Equal(t, netLabelKey("partner", "default"), netLabelKey(ref.Namespace.Project, ref.Name))
	_, hasOwn := labels[netLabelKey("shop", "default")]
	assert.False(t, hasOwn, "ref-derived key must not collide with same-name own network")

	// 零附件：none 锚。
	labels = netMembershipLabels(ns, capability.Workload{ID: "w2", Process: "x"})
	assert.Equal(t, map[string]string{labelNetNone: "true"}, labels)

	// Publish 豁免：发布载体不带成员资格 label（hostPort = 节点级可达语义）。
	w = capability.Workload{ID: "w3", Process: "proxy",
		Networks: []string{"default"},
		Publish:  []capability.PortPublish{{Mode: capability.PublishModeHost, PublishedPort: 80, TargetPort: 8080}}}
	assert.Empty(t, netMembershipLabels(ns, w))

	// label key 上限（63）内。
	assert.LessOrEqual(t, len(netLabelKey("01ARZ3NDEKTSV4RRFFQ69G5FAV", "a-very-long-network-name-exceeding-the-label-budget")), 63)
}

// TestReconcileNetIsolationPerNetwork：每网一条成员 policy（组级聚合——非
// per-carrier）；放行集 = 同网成员 + fleetly-system；引用衍生网多一条接收方
// ns 规则。
func TestReconcileNetIsolationPerNetwork(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()
	ws := []capability.Workload{
		{ID: "w1", Process: "api", Networks: []string{"default"}},
		{ID: "w2", Process: "worker", Networks: []string{"default", "isolated"}},
	}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	pols, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	// 两条成员 policy（default + isolated），无 net.none（无零附件载体）。
	require.Len(t, pols.Items, 2)
	byName := map[string]*networkingv1.NetworkPolicy{}
	for i := range pols.Items {
		byName[pols.Items[i].Name] = &pols.Items[i]
	}
	defKey := netLabelKey("shop", "default")
	defPol, ok := byName[netIsolationPolicyName(defKey)]
	require.True(t, ok, "member policy for 'default' must exist")
	assert.Equal(t, map[string]string{defKey: "true"}, defPol.Spec.PodSelector.MatchLabels)
	require.Len(t, defPol.Spec.Ingress, 1)
	require.Len(t, defPol.Spec.Ingress[0].From, 2)
	assert.Equal(t, map[string]string{defKey: "true"}, defPol.Spec.Ingress[0].From[0].PodSelector.MatchLabels,
		"same-namespace members must be admitted")
	assert.Equal(t, map[string]string{nsNameLabel: systemNamespace}, defPol.Spec.Ingress[0].From[1].NamespaceSelector.MatchLabels,
		"system namespace must be admitted (managed proxy reachability)")
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, defPol.Spec.PolicyTypes)
	assert.Equal(t, "true", defPol.Labels[labelManaged])

	// 引用衍生网：多一条接收方 ns 同网成员规则。
	ref := capability.NetworkRef{Namespace: capability.NamespaceRef{Project: "partner"}, Name: "default"}
	ws = []capability.Workload{{ID: "w1", Process: "api", NetworkRefs: []capability.NetworkRef{ref}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	refKey := netLabelKey("partner", "default")
	pol, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(refKey), metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, pol.Spec.Ingress, 2)
	assert.Equal(t, map[string]string{nsNameLabel: "fleetly-partner"}, pol.Spec.Ingress[1].From[0].NamespaceSelector.MatchLabels)
	assert.Equal(t, map[string]string{refKey: "true"}, pol.Spec.Ingress[1].From[0].PodSelector.MatchLabels)
}

// TestReconcileNetIsolationStaleRemoval：期望集外的 managed policy 收敛删除；
// egress deny 保白名单；非 managed 的他人 policy 不触碰；他方 grant 不触碰。
func TestReconcileNetIsolationStaleRemoval(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()
	ws := []capability.Workload{{ID: "w1", Process: "api", Networks: []string{"default"}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	// 预置：egress deny（保白名单）、他人非 managed policy、他方 grant。
	seedEgress := toEgressNetpol()
	_, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, seedEgress, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "foreign-policy"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	foreignGrant := toPeerGrantPolicy("otherproj", netLabelKey("shop", "default"))
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, foreignGrant, metav1.CreateOptions{})
	require.NoError(t, err)

	// 期望集收窄（default → isolated）：default 成员 policy 删除。
	ws = []capability.Workload{{ID: "w1", Process: "api", Networks: []string{"isolated"}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(netLabelKey("shop", "default")), metav1.GetOptions{})
	assert.True(t, errNotFound(err), "member policy of removed network must be converged away")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, toEgressNetpol().Name, metav1.GetOptions{})
	assert.NoError(t, err, "egress deny policy must survive convergence")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, "foreign-policy", metav1.GetOptions{})
	assert.NoError(t, err, "non-managed policies must not be touched")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, foreignGrant.Name, metav1.GetOptions{})
	assert.NoError(t, err, "grants owned by other projects must not be touched")
}

// TestReconcileNetIsolationNone：零附件载体在场 → deny-all policy（无放行
// 规则）；全部载体有附件 → 撤除。
func TestReconcileNetIsolationNone(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()
	ws := []capability.Workload{
		{ID: "w1", Process: "api", Networks: []string{"default"}},
		{ID: "w2", Process: "bare"}, // 零附件
	}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	pol, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, toNetNonePolicy().Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{labelNetNone: "true"}, pol.Spec.PodSelector.MatchLabels)
	assert.Empty(t, pol.Spec.Ingress, "zero-attachment carriers must admit no traffic")

	ws = ws[:1]
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, toNetNonePolicy().Name, metav1.GetOptions{})
	assert.True(t, errNotFound(err), "deny-all must be removed when no zero-attachment carriers remain")
}

// TestReconcilePeerGrants：引用在场 → 挂靠方 ns 成员 policy + 接收方 ns
// grant（声明方身份标记 + 双向互放行）；引用消失（isolate 剥离后）→ 两侧
// 收敛删除（ADR-0013 附录 A.4 隔离生效时点 = 该次 Ensure 完成）。
func TestReconcilePeerGrants(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()
	ref := capability.NetworkRef{Namespace: capability.NamespaceRef{Project: "partner"}, Name: "default"}
	ws := []capability.Workload{{ID: "w1", Process: "api", NetworkRefs: []capability.NetworkRef{ref}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	refKey := netLabelKey("partner", "default")
	grantName := peerGrantName("shop", refKey)
	grant, err := cli.NetworkingV1().NetworkPolicies("fleetly-partner").Get(ctx, grantName, metav1.GetOptions{})
	require.NoError(t, err, "peer grant must land in the receiver namespace")
	assert.Equal(t, "shop", grant.Labels[labelPeerOwner])
	assert.Equal(t, map[string]string{refKey: "true"}, grant.Spec.PodSelector.MatchLabels)
	require.Len(t, grant.Spec.Ingress, 1)
	require.Len(t, grant.Spec.Ingress[0].From, 1)
	assert.Equal(t, map[string]string{nsNameLabel: "fleetly-shop"}, grant.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels,
		"grant must admit the declaring project's namespace")

	// 撤销形态：引用剥离后的重 Ensure → 两侧 policy 收敛删除。
	ws = []capability.Workload{{ID: "w1", Process: "api", Networks: []string{"default"}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-partner").Get(ctx, grantName, metav1.GetOptions{})
	assert.True(t, errNotFound(err), "grant must be converged away when the ref is stripped")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(refKey), metav1.GetOptions{})
	assert.True(t, errNotFound(err), "ref-derived member policy must be converged away")
}

// TestReconcileNetIsolationSkipsSystemNs：受管域不设隔离 policy（发布豁免
// + host 流量不过 netpol 链——ADR-0054 诚实边界）。
func TestReconcileNetIsolationSkipsSystemNs(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	sysNS := capability.NamespaceRef{} // 无 Project = 受管域
	ws := []capability.Workload{
		{ID: "vl", Process: "victorialogs"}, // 零附件（不落 deny-all）
		{ID: "proxy", Process: "traefik", Networks: []string{"x"}, Publish: []capability.PortPublish{{Mode: capability.PublishModeHost}}},
	}
	require.NoError(t, p.reconcileNetIsolation(ctx, sysNS, systemNamespace, ws))
	pols, err := cli.NetworkingV1().NetworkPolicies(systemNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, pols.Items, "system namespace must carry no isolation policies")
}

// TestEnsureNetMembershipLabelsLand：workloadLabels 合入成员资格标记（载体
// 模板面——policy 的 podSelector 锚）。
func TestEnsureNetMembershipLabelsLand(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ws := []capability.Workload{{ID: "w1", Process: "api", Image: "nginx:1.27", Networks: []string{"default"}}}
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 1, capability.Materials{}))
	d, err := cli.AppsV1().Deployments("fleetly-shop").Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "true", d.Spec.Template.Labels[netLabelKey("shop", "default")],
		"pod template must carry the network membership label")
}
