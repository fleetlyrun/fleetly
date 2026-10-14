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
	g, ok := cold.issuedGenOf(w.ID)
	require.True(t, ok)
	assert.Equal(t, uint64(1), g, "issuedGen ledger records the wire gen")
}
