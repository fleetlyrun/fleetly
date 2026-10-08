package k3s

// 网络成员资格入站隔离单测（ADR-0054 决策 1）：成员资格 label 翻译四形态、
// per-network 成员 policy（活 pod label 派生收敛——policy 是项目级共享
// 资源，同项目多域 Ensure 互不误删）、零附件 deny-all、peer grant 双侧
// 形态与撤销收敛、受管域跳过、hostPort 发布豁免、Service 域感收敛（同族
// 跨域误删 bug 的姊妹面）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// seedMemberPod 播一个携带成员资格 label 的活 pod（收敛真源：policy 的
// 存续判据是"本 ns 有 managed pod 持有该 key"）。
func seedMemberPod(t *testing.T, p *Provider, nsName, name string, keys ...string) {
	t.Helper()
	labels := map[string]string{labelManaged: "true"}
	for _, k := range keys {
		labels[k] = "true"
	}
	_, err := p.cli.CoreV1().Pods(nsName).Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

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
	defKey := netLabelKey("shop", "default")
	isoKey := netLabelKey("shop", "isolated")
	seedMemberPod(t, p, "fleetly-shop", "pod-web", defKey)
	seedMemberPod(t, p, "fleetly-shop", "pod-worker", isoKey)
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
	refKey := netLabelKey("partner", "default")
	seedMemberPod(t, p, "fleetly-shop", "pod-consumer", refKey)
	ws = []capability.Workload{{ID: "w1", Process: "api", NetworkRefs: []capability.NetworkRef{ref}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	pol, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(refKey), metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, pol.Spec.Ingress, 2)
	assert.Equal(t, map[string]string{nsNameLabel: "fleetly-partner"}, pol.Spec.Ingress[1].From[0].NamespaceSelector.MatchLabels)
	assert.Equal(t, map[string]string{refKey: "true"}, pol.Spec.Ingress[1].From[0].PodSelector.MatchLabels)
}

// TestReconcileNetIsolationFreshDeployKeepsPolicy（e2e 第二轮实锤回归锚）：
// 全新部署的 Ensure 拍——载体尚未创建（liveKeys 空），本拍期望键即意图，
// 成员 policy 不得同拍自噬（一次部署成的项目再无 Ensure 补建，隔离面会
// 永久缺失）。
func TestReconcileNetIsolationFreshDeployKeepsPolicy(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ws := []capability.Workload{{ID: "w1", Process: "api", Networks: []string{"default"}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, appNS(), "fleetly-shop", ws))
	_, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(netLabelKey("shop", "default")), metav1.GetOptions{})
	assert.NoError(t, err, "fresh-deploy ensure must keep the member policy (batch key is intent; carriers are created after this pass)")
}

// TestReconcileNetIsolationStaleRemoval（活 label 派生收敛）：选择器 key 无
// 活 pod 持有的 policy 收敛删除；有活 pod 持有的保留；egress deny 不归本面
// （恒保留由 reconcileEgressNetpol 管）；非 managed 的他人 policy 不触碰；
// 他方 grant 不触碰。
func TestReconcileNetIsolationStaleRemoval(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()
	deadKey := netLabelKey("shop", "deadnet")
	liveKey := netLabelKey("shop", "default")
	seedMemberPod(t, p, "fleetly-shop", "pod-web", liveKey)
	ws := []capability.Workload{{ID: "w1", Process: "api", Networks: []string{"default"}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	// 预置：egress deny、他人非 managed policy、他方 grant、无活 pod 的死网
	// 成员 policy。
	_, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, toEgressNetpol(), metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "foreign-policy"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	foreignGrant := toPeerGrantPolicy("otherproj", "web", netLabelKey("shop", "default"))
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, foreignGrant, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, toNetIsolationPolicy(deadKey, ""), metav1.CreateOptions{})
	require.NoError(t, err)

	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(deadKey), metav1.GetOptions{})
	assert.True(t, errNotFound(err), "policy with no live member pod must be converged away")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(liveKey), metav1.GetOptions{})
	assert.NoError(t, err, "policy with a live member pod must be retained")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, toEgressNetpol().Name, metav1.GetOptions{})
	assert.NoError(t, err, "egress deny policy must survive convergence")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, "foreign-policy", metav1.GetOptions{})
	assert.NoError(t, err, "non-managed policies must not be touched")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, foreignGrant.Name, metav1.GetOptions{})
	assert.NoError(t, err, "grants owned by other projects must not be touched")
}

// TestReconcileNetIsolationCrossDomainSafe（e2e 回归锚）：policy 是项目级共享
// 资源——task 域的 Ensure（只带 taskgrp 附件）不得收敛掉 app 域的 default
// 成员 policy（app pod 持有该 key 即在役）。
func TestReconcileNetIsolationCrossDomainSafe(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	appNS := appNS()
	taskNS := capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01T"}
	defKey := netLabelKey("shop", "default")
	grpKey := netLabelKey("shop", "taskgrp-pipe")
	seedMemberPod(t, p, "fleetly-shop", "pod-web", defKey)
	seedMemberPod(t, p, "fleetly-shop", "pod-run", grpKey)

	// app 域 Ensure：default + taskgrp 两条成员 policy。
	appWS := []capability.Workload{
		{ID: "w1", Process: "api", Networks: []string{"default"}},
		{ID: "w2", Process: "bridge", Networks: []string{"taskgrp-pipe"}},
	}
	require.NoError(t, p.reconcileNetIsolation(ctx, appNS, "fleetly-shop", appWS))
	// task 域 Ensure：只带 taskgrp。
	taskWS := []capability.Workload{{ID: "01R", Process: "run", Networks: []string{"taskgrp-pipe"}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, taskNS, "fleetly-shop", taskWS))

	_, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(defKey), metav1.GetOptions{})
	assert.NoError(t, err, "task-domain ensure must not remove the app domain's member policy (live pod holds the key)")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(grpKey), metav1.GetOptions{})
	assert.NoError(t, err)
}

// TestReconcileNetIsolationNone：零附件载体在场 → deny-all policy（无放行
// 规则）；全部载体有附件 → 撤除（活 label 派生：无 net.none pod 即删）。
func TestReconcileNetIsolationNone(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()
	ws := []capability.Workload{
		{ID: "w1", Process: "api", Networks: []string{"default"}},
		{ID: "w2", Process: "bare"}, // 零附件
	}
	seedMemberPod(t, p, "fleetly-shop", "pod-bare", labelNetNone)
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	pol, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, toNetNonePolicy().Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{labelNetNone: "true"}, pol.Spec.PodSelector.MatchLabels)
	assert.Empty(t, pol.Spec.Ingress, "zero-attachment carriers must admit no traffic")

	// 零附件载体消失（pod 删除 + 期望集无零附件）→ deny-all 收敛撤除。
	require.NoError(t, cli.CoreV1().Pods("fleetly-shop").Delete(ctx, "pod-bare", metav1.DeleteOptions{}))
	ws = ws[:1]
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, toNetNonePolicy().Name, metav1.GetOptions{})
	assert.True(t, errNotFound(err), "deny-all must be removed when no zero-attachment carriers remain")
}

// TestReconcilePeerGrants：引用在场 → 挂靠方 ns 成员 policy + 接收方 ns
// grant（声明方身份标记 + 双向互放行）；引用消失且挂靠方 ns 无 pod 持有
// 引用 key（isolate 剥离滚动完成后）→ 两侧收敛删除（ADR-0013 附录 A.4）。
func TestReconcilePeerGrants(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()
	ref := capability.NetworkRef{Namespace: capability.NamespaceRef{Project: "partner"}, Name: "default"}
	refKey := netLabelKey("partner", "default")
	grantName := peerGrantName("shop", "web", refKey)
	ws := []capability.Workload{{ID: "w1", Process: "api", NetworkRefs: []capability.NetworkRef{ref}}}
	seedMemberPod(t, p, "fleetly-shop", "pod-consumer", refKey)
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	grant, err := cli.NetworkingV1().NetworkPolicies("fleetly-partner").Get(ctx, grantName, metav1.GetOptions{})
	require.NoError(t, err, "peer grant must land in the receiver namespace")
	assert.Equal(t, "shop", grant.Labels[labelPeerOwner])
	assert.Equal(t, map[string]string{refKey: "true"}, grant.Spec.PodSelector.MatchLabels)
	require.Len(t, grant.Spec.Ingress, 1)
	require.Len(t, grant.Spec.Ingress[0].From, 1)
	assert.Equal(t, map[string]string{nsNameLabel: "fleetly-shop"}, grant.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels,
		"grant must admit the declaring project's namespace")

	// 撤销形态（isolate 剥离 → 本拍期望集不含引用）：grant 当拍即清（纯
	// 意图集收敛——旧 pod 仍在终止中也不等待；删除只会更早拒绝）；引用衍生
	// 成员 policy 在旧 pod 终止窗内保留（活 label 半边），滚动完成后收敛删除。
	ws = []capability.Workload{{ID: "w1", Process: "api", Networks: []string{"default"}}}
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-partner").Get(ctx, grantName, metav1.GetOptions{})
	assert.True(t, errNotFound(err), "grant must be converged away in the same ensure once the ref is stripped from intent")
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(refKey), metav1.GetOptions{})
	assert.NoError(t, err, "ref-derived member policy must survive the rolling window (terminating pod still holds the key)")
	require.NoError(t, cli.CoreV1().Pods("fleetly-shop").Delete(ctx, "pod-consumer", metav1.DeleteOptions{}))
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	_, err = cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(refKey), metav1.GetOptions{})
	assert.True(t, errNotFound(err), "ref-derived member policy must be converged away after the last carrier holding the key is gone")
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

// TestEnsureServiceConvergenceIsDomainScoped（e2e 回归锚·姊妹面）：Service
// 收敛带域主体轴——task 域的 Ensure 不得删 app 域的 Service；无轴遗留
// （补轴前形态）在宽列中清除；他域不触碰。
func TestEnsureServiceConvergenceIsDomainScoped(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	appNS := appNS()
	// app 域落 Service。
	ws := []capability.Workload{{
		ID: "w1", Process: "api", Image: "nginx:1.27",
		Ports:      []capability.WorkloadPort{{Port: 80}},
		Addressing: []capability.Address{{Name: "api.web"}},
	}}
	require.NoError(t, p.Ensure(ctx, appNS, ws, 1, capability.Materials{}))
	_, err := cli.CoreV1().Services("fleetly-shop").Get(ctx, "api-web", metav1.GetOptions{})
	require.NoError(t, err)

	// 同项目他域（task）的 Service + 无轴遗留（补轴前形态）。
	taskNS := capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01T"}
	for name, svcLabels := range map[string]map[string]string{
		"task-01t": serviceLabels(taskNS),
		"srv-legacy": {
			labelManaged: "true",
			labelTeam:    "acme",
			labelProject: "shop",
		},
	} {
		_, err := cli.CoreV1().Services("fleetly-shop").Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: svcLabels},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// task 域 Ensure（携带 task 轴 Service）：app 的 Service 保留（他域），
	// 无轴遗留清除（升级收敛面）。
	taskWS := []capability.Workload{{
		ID: "01R", Process: "run", Image: "busybox:1.37", Restart: capability.RestartNever,
		Addressing: []capability.Address{{Name: "task-01T"}},
	}}
	require.NoError(t, p.Ensure(ctx, taskNS, taskWS, 1, capability.Materials{}))
	for name, want := range map[string]bool{
		"api-web":    true,  // app 域（他域视角：不删）
		"task-01t":   true,  // 本域期望集
		"srv-legacy": false, // 无轴遗留 → 清除
	} {
		_, err := cli.CoreV1().Services("fleetly-shop").Get(ctx, name, metav1.GetOptions{})
		if want {
			assert.NoError(t, err, "service %s must survive task-domain ensure", name)
		} else {
			assert.True(t, errNotFound(err), "legacy axis-less service %s must be cleaned", name)
		}
	}
}

// TestNetpolShapeDriftConverges（ADR-0055 实录锚）：存量 policy 形状漂移
// （平台升级改放行集）经 Ensure 收敛更新——create-only 会把存量锁死在旧
// 形态；相等幂等零写。备份链的可达性不走 policy 放行面（utility pod 走
// hostNetwork，见 utility.go——CNI 对新 pod 的 ipset 准入传播赌不起）。
func TestNetpolShapeDriftConverges(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	defKey := netLabelKey("shop", "default")
	ns := appNS()

	// 首拍：多一个多余 peer 的漂移形态落盘（模拟升级前/人工改动存量）。
	legacy := toNetIsolationPolicy(defKey, "")
	legacy.Spec.Ingress[0].From = append(legacy.Spec.Ingress[0].From,
		networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"stale": "true"}}})
	_, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Create(ctx, legacy, metav1.CreateOptions{})
	require.NoError(t, err)

	ws := []capability.Workload{{ID: "w1", Process: "api", Networks: []string{"default"}}}
	seedMemberPod(t, p, "fleetly-shop", "pod-web", defKey)
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))

	got, err := cli.NetworkingV1().NetworkPolicies("fleetly-shop").Get(ctx, netIsolationPolicyName(defKey), metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, got.Spec.Ingress[0].From, 2, "shape drift must converge to the desired allow set")

	// 相等幂等：再拍零写（Update 计数不增）。
	updates := 0
	cli.PrependReactor("update", "networkpolicies", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})
	require.NoError(t, p.reconcileNetIsolation(ctx, ns, "fleetly-shop", ws))
	assert.Zero(t, updates, "converged policy must not be rewritten on replay")
}
