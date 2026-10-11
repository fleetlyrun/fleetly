package swarm

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/swarm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// ADR-0064 内容锚定等价三义测试群（staging 2026-10-11 事故回归锚：单
// 副本调整曾触发全服务替换——gen 戳进任务模板使跨代等价恒失配，六服务
// 齐滚 → DNS 断档 → 健康门双超时）。

// 跨代未变载体：期望内容不变、仅部署代次推进 → 零 update、服务保留旧
// 代戳、报告回传 wire-gen。两条跳过路径都钉：账本短路（同 Provider 拍
// 2）与等价回读（冷账本 Provider 首拍——重启形态）。
func TestEnsureContentEquivalenceSkipsUnchangedAcrossGen(t *testing.T) {
	d := newFakeDaemon()
	p := &Provider{cli: d.newClient(t)}
	ctx := context.Background()
	name := ensureCarrierName()
	w := ensureWorkload()
	updateKey := "POST /services/srv-" + name + "/update"

	// 拍 1（gen 1）：create 落 gen=1 戳。
	_, err := p.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	svc, ok := d.byRef(name)
	require.True(t, ok)
	require.Equal(t, "1", svc.Spec.Labels[labelGeneration])

	// 拍 2（gen 2，内容零变化）：账本短路路径——零 update、代戳保留。
	report, err := p.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 0, d.count(updateKey), "unchanged workload must not be updated across generations")
	svc, _ = d.byRef(name)
	assert.Equal(t, "1", svc.Spec.Labels[labelGeneration], "gen stamp stays with the running carrier")
	assert.Equal(t, capability.Generation(1), report.WireGen(w, 2), "report carries the wire gen for observation attribution")

	// 拍 3（冷账本——daemon 重启形态）：等价回读路径同语义。
	cold := &Provider{cli: d.newClient(t)}
	report, err = cold.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(3), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 0, d.count(updateKey))
	assert.Equal(t, capability.Generation(1), report.WireGen(w, 3))
}

// replicas-only：内容等价（modulo 代戳与副本数）仅副本数不同 → 恰一次
// scale-only update——任务模板与服务级标签原样回发（模板未变 = swarm
// 不替换存量任务），报告 wire-gen = 现存代戳。
func TestEnsureReplicasOnlyScalesPreservingTemplate(t *testing.T) {
	d := newFakeDaemon()
	p := &Provider{cli: d.newClient(t)}
	ctx := context.Background()
	name := ensureCarrierName()
	w := ensureWorkload()
	w.Replicas = 1
	updateKey := "POST /services/srv-" + name + "/update"

	_, err := p.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)

	// 拍 2（gen 2、副本 1→2）：scale-only。
	scaled := w
	scaled.Replicas = 2
	report, err := p.Ensure(ctx, ensureNS, []capability.Workload{scaled}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 1, d.count(updateKey), "replicas-only change updates exactly once")
	svc, _ := d.byRef(name)
	require.NotNil(t, svc.Spec.Mode.Replicated)
	require.NotNil(t, svc.Spec.Mode.Replicated.Replicas)
	assert.EqualValues(t, 2, *svc.Spec.Mode.Replicated.Replicas)
	assert.Equal(t, "1", svc.Spec.TaskTemplate.ContainerSpec.Labels[labelGeneration],
		"scale-only must preserve the in-service task template (no task replacement)")
	assert.Equal(t, "1", svc.Spec.Labels[labelGeneration], "service-level gen anchor moves with the template, not the deployment")
	assert.Equal(t, capability.Generation(1), report.WireGen(scaled, 2))

	// 拍 3（同期望重放）：账本短路零新 update。
	_, err = p.Ensure(ctx, ensureNS, []capability.Workload{scaled}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 1, d.count(updateKey))
}

// 真内容变更：滚动替换照常、新代戳随变更落地、报告缺席该载体（wire-gen
// = 调用 gen）——代戳与内容同移的另一半。
func TestEnsureContentChangeRollsWithNewGen(t *testing.T) {
	d := newFakeDaemon()
	p := &Provider{cli: d.newClient(t)}
	ctx := context.Background()
	name := ensureCarrierName()
	w := ensureWorkload()
	updateKey := "POST /services/srv-" + name + "/update"

	_, err := p.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)

	changed := w
	changed.Image = "nginx:1.29"
	report, err := p.Ensure(ctx, ensureNS, []capability.Workload{changed}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 1, d.count(updateKey))
	svc, _ := d.byRef(name)
	assert.Equal(t, "2", svc.Spec.Labels[labelGeneration], "content change advances the gen stamp")
	assert.Equal(t, "2", svc.Spec.TaskTemplate.ContainerSpec.Labels[labelGeneration])
	assert.Equal(t, capability.Generation(2), report.WireGen(changed, 2), "rolled workload reports the call gen")
}

// 滚动停摆 resume（ADR-0064 决策 5）：期望内容等价 + UpdateStatus=paused
// （平台自己的滚动更新停摆）→ ForceUpdate 自增重发恰一次；时间窗护栏内
// 不再重复。staging 两次致害（N/1 收案、2026-10-11 双门超时案）的收敛
// 机制回归锚。
func TestEnsureResumesStalledRolloutOnce(t *testing.T) {
	d := newFakeDaemon()
	p := &Provider{cli: d.newClient(t)}
	ctx := context.Background()
	name := ensureCarrierName()
	w := ensureWorkload()
	updateKey := "POST /services/srv-" + name + "/update"

	_, err := p.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	changed := w
	changed.Image = "nginx:1.29"
	_, err = p.Ensure(ctx, ensureNS, []capability.Workload{changed}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	require.Equal(t, 1, d.count(updateKey))

	// 模拟停摆：滚动被服务端接受（spec 已是 gen2）但任务失败进入 paused。
	d.mu.Lock()
	svc := d.store[name]
	svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStatePaused, Message: "update paused due to failure or early termination of task"}
	d.store[name] = svc
	d.mu.Unlock()

	// 拍 3：断路器命中（期望==账本）但停摆在 → resume 恰一次（ForceUpdate
	// 自增的真 update），报告按本拍 gen 归因（模板已随 resume 换新戳）。
	report, err := p.Ensure(ctx, ensureNS, []capability.Workload{changed}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 2, d.count(updateKey), "stalled rollout must be resumed exactly once")
	d.mu.Lock()
	svc = d.store[name]
	fu := svc.Spec.TaskTemplate.ForceUpdate
	d.mu.Unlock()
	assert.Equal(t, uint64(1), fu, "resume bumps ForceUpdate (docker --force semantics)")
	assert.Equal(t, capability.Generation(2), report.WireGen(changed, 2))

	// 拍 4：fake 不清 UpdateStatus（停摆持续形态）→ 窗内护栏不再 resume，
	// 回到跳过语义（wire-gen = 现存服务代戳）。
	report, err = p.Ensure(ctx, ensureNS, []capability.Workload{changed}, capability.Generation(2), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 2, d.count(updateKey), "resume must not flap inside the guard window")
	assert.Equal(t, capability.Generation(2), report.WireGen(changed, 2))
}
