package k3s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ADR-0064 内容锚定等价测试群（k3s 面；swarm 侧 2026-10-11 事故同族
// 根因的 k8s 形态回归锚）：账本双键（A=modulo-gen / B=再剥副本数）+
// 写代钉住，免服务端内容比对。

// 跨代未变载体：A 键命中 → 零写（对象 ResourceVersion 不动）、对象与
// pod 模板代戳保留旧值、报告回传 wire-gen。
func TestEnsureContentEquivalenceSkipsUnchangedAcrossGen(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	w := capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27"}

	_, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	d, err := cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "1", d.Labels[labelGeneration])
	rv := d.ResourceVersion

	// 拍 2（gen 2，内容零变化）：A 键命中 → 零写、代戳保留、报告 wire-gen=1。
	report, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	d, _ = cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	assert.Equal(t, rv, d.ResourceVersion, "unchanged workload must not be rewritten across generations")
	assert.Equal(t, "1", d.Labels[labelGeneration], "gen stamp stays with the running carrier")
	assert.Equal(t, "1", d.Spec.Template.Labels[labelGeneration], "pod template gen stays frozen at creation gen")
	assert.Equal(t, capability.Generation(1), report.WireGen(w, 2), "report carries the wire gen for observation attribution")
	assert.Empty(t, report.Changed, "content-addressed skip must not report a change (ADR-0082 §2)")
	assert.Empty(t, report.Removed)
	assert.False(t, report.HasMaterialChange())
}

// replicas-only：B 键命中 → 写代钉 wire-gen——pod 模板字节不动（k8s 语义
// 下模板未变 = 存量 pod 零替换，仅 Deployment 副本数推进）。
func TestEnsureReplicasOnlyScalesPreservingTemplate(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	w := capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27", Replicas: 1}

	_, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)

	scaled := w
	scaled.Replicas = 3
	report, err := p.Ensure(ctx, appNS(), []capability.Workload{scaled}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	d, err := cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, d.Spec.Replicas)
	assert.EqualValues(t, 3, *d.Spec.Replicas, "replicas-only change must scale the Deployment")
	assert.Equal(t, "1", d.Spec.Template.Labels[labelGeneration],
		"scale-only must keep the pod template byte-stable (no pod replacement)")
	assert.Equal(t, capability.Generation(1), report.WireGen(scaled, 2))
	assert.Equal(t, []string{scaled.ID}, report.Changed, "replicas-only is a material change (the object is written)")
}

// 真内容变更：写代 = 调用 gen——对象与 pod 模板代戳随内容推进，报告
// 缺席该载体（wire-gen = 调用 gen）。
func TestEnsureContentChangeRollsWithNewGen(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	w := capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27"}

	_, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)

	changed := w
	changed.Image = "nginx:1.29"
	report, err := p.Ensure(ctx, appNS(), []capability.Workload{changed}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	d, err := cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "2", d.Labels[labelGeneration], "content change advances the gen stamp")
	assert.Equal(t, "2", d.Spec.Template.Labels[labelGeneration])
	assert.Equal(t, capability.Generation(2), report.WireGen(changed, 2), "rolled workload reports the call gen")
	assert.Equal(t, []string{changed.ID}, report.Changed, "a content change is a material change")
}

// 冷账本重放（daemon 重启形态）：账本空 → 读现存对象代戳钉写代——内容
// 未变时模板字节不动（服务端归一后 no-op，重启零滚动）；issuedGen 落
// 回 wire-gen。
func TestEnsureColdLedgerReplayPinsServerGen(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	w := capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27"}

	_, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)

	// 冷进程（重启形态）：账本全空，服务端对象 gen=1。
	cold := &Provider{cli: cli, apiServer: p.apiServer}
	report, err := cold.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(7), capability.Materials{})
	require.NoError(t, err)
	d, err := cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "1", d.Spec.Template.Labels[labelGeneration],
		"cold replay of unchanged content must pin the server gen (no rollout after daemon restart)")
	assert.Equal(t, capability.Generation(1), report.WireGen(w, 7))
	// B5：冷账本重放不计 Changed——该形态严格等于重启前已下发内容的幂等
	// 重放，计入会让每次重启产生 N 条假变更审计（ADR-0082 §2 第 3 条）。
	assert.Empty(t, report.Changed, "cold-ledger replay must not report a change")
	assert.False(t, report.HasMaterialChange())
	g, ok := cold.issuedGenOf(w.ID)
	require.True(t, ok)
	assert.Equal(t, uint64(1), g, "issuedGen ledger records the wire gen")
}

// ADR-0083 内容等价四用例（k3s 面；restart_seq 铸 pod 模板标签，账本
// canonical key 随标签入比照——swarm 侧同款四例的 k8s 对偶）：seq 变 →
// rollout / 同 seq → skip / 缺失 → create / 存量无标签 × 期望 0 → skip
// （升级首拍零滚动，bb5259d WAL 前车回归钉）。
func TestEnsureRestartSeqContentEquivalence(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	name := "fleetly-web-api"

	// 用例 1（seq 变 → rollout）：模板标签换值即模板变更，pod 模板代戳随
	// 内容推进、报告按调用 gen 归因。
	seq1 := capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27", RestartSeq: 1}
	_, err := p.Ensure(ctx, appNS(), []capability.Workload{seq1}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	seq2 := seq1
	seq2.RestartSeq = 2
	report, err := p.Ensure(ctx, appNS(), []capability.Workload{seq2}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	d, err := cli.AppsV1().Deployments(nsName).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "2", d.Spec.Template.Labels[labelRestartSeq], "pod template carries the new seq")
	assert.Equal(t, "2", d.Spec.Template.Labels[labelGeneration], "seq change is a content change: gen advances")
	assert.Equal(t, capability.Generation(2), report.WireGen(seq2, 2))

	// 用例 2（同 seq → skip）：A 键命中零写（ResourceVersion 不动），冷账
	// 本进程同样钉现存代戳。
	rv := d.ResourceVersion
	_, err = p.Ensure(ctx, appNS(), []capability.Workload{seq2}, capability.Generation(3), capability.Materials{})
	require.NoError(t, err)
	cold := &Provider{cli: cli, apiServer: p.apiServer}
	report, err = cold.Ensure(ctx, appNS(), []capability.Workload{seq2}, capability.Generation(4), capability.Materials{})
	require.NoError(t, err)
	d, err = cli.AppsV1().Deployments(nsName).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, rv, d.ResourceVersion, "same seq must not rewrite the object (hot and cold ledger)")
	assert.Equal(t, "2", d.Spec.Template.Labels[labelGeneration])
	assert.Equal(t, capability.Generation(2), report.WireGen(seq2, 4))

	// 用例 3（缺失 → create）：seq=0 新载体创建且不铸序数标签。
	fresh, freshCLI := newFakeProvider()
	seq0 := capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27"}
	_, err = fresh.Ensure(ctx, appNS(), []capability.Workload{seq0}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	d, err = freshCLI.AppsV1().Deployments(nsName).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, d.Spec.Template.Labels, labelRestartSeq, "seq=0 casts no label")
	assert.NotContains(t, d.Labels, labelRestartSeq)

	// 用例 4（存量无标签 × 期望 0 → skip，升级零滚）：用例 3 的 create
	// 产物即旧版平台形态（无序数标签）——升级后新二进制（冷账本）期望
	// seq=0 同样不铸，A 键命中零写、代戳保留。
	upgrade := &Provider{cli: freshCLI, apiServer: fresh.apiServer}
	report, err = upgrade.Ensure(ctx, appNS(), []capability.Workload{seq0}, capability.Generation(8), capability.Materials{})
	require.NoError(t, err)
	d, err = freshCLI.AppsV1().Deployments(nsName).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "1", d.Spec.Template.Labels[labelGeneration],
		"existing carrier without a seq label vs desired seq=0 must not roll (upgrade is zero-roll)")
	assert.Equal(t, capability.Generation(1), report.WireGen(seq0, 8))
}

// canonical 归一钉（ADR-0083 比对半边，k3s 面）：restart_seq 标签 "0" 值
// 与缺失在 modulo-gen canonical 里等价（stripObjectGenLabel 单点执法）。
func TestCanonicalModuloGenNormalizesZeroRestartSeqLabel(t *testing.T) {
	base := toDeployment(appNS(), capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27"}, 1, nil, nil, nil)
	zero := toDeployment(appNS(), capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27"}, 1, nil, nil, nil)
	zero.Labels[labelRestartSeq] = "0"
	zero.Spec.Template.Labels[labelRestartSeq] = "0"
	assert.Equal(t, canonicalJSONModuloGen(base), canonicalJSONModuloGen(zero),
		"a zero-valued restart_seq label must compare equal to its absence")
	one := toDeployment(appNS(), capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27", RestartSeq: 1}, 1, nil, nil, nil)
	assert.NotEqual(t, canonicalJSONModuloGen(base), canonicalJSONModuloGen(one))
}
