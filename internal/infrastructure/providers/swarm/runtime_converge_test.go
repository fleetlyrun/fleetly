package swarm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// TestEnsureConvergesExternalScaleDown：平台外改动载体（`docker service scale
// <name>=0`）后，下一拍 Ensure 必须把副本拉回——controller wins（ADR-0082
// 后果段：「人工改载体……最迟 60s+一拍内收敛」）。
//
// 回归面（2026-11-24，干净 dind 最小复现；进度表 Z1.4 记录更正⑫）：no-op
// 断路器此前只比对「期望 canonical == 最近一次下发的 canonical」，期望未变
// 即把整条 update 判定链短路——外部缩零后收敛环每拍 Ensure 都「成功」返回
// 而载体恒 0/1，引擎照发 workload.converged。断路器自此同时比对服务对象
// 版本：期望相同**且**服务自上次确认后无人写过，才是 no-op。
func TestEnsureConvergesExternalScaleDown(t *testing.T) {
	d := newFakeDaemon()
	p := &Provider{cli: d.newClient(t)}
	ctx := context.Background()
	name := ensureCarrierName()
	updateKey := "POST /services/srv-" + name + "/update"
	w := ensureWorkload()
	w.Replicas = 1

	run := func() capability.EnsureReport {
		report, err := p.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(1), capability.Materials{})
		require.NoError(t, err)
		return report
	}

	// 拍 1：create；拍 2：稳态——服务版本未变，断路器短路，零 update。
	run()
	steady := run()
	assert.Equal(t, 0, d.count(updateKey), "an untouched carrier must stay quiet")
	assert.Empty(t, steady.Changed)

	// 平台外缩零（docker service scale <name>=0：改副本数并推进服务版本）。
	d.scaleExternal(t, name, 0)

	// 拍 3：必须拉回副本——走 scale-only（保留在服任务模板），计入变更集。
	converged := run()
	assert.Equal(t, 1, d.count(updateKey), "an externally scaled-down carrier must be re-issued on the very next beat")
	assert.EqualValues(t, 1, d.replicasOf(t, name), "the carrier is back at the desired replica count")
	svc, _ := d.byRef(name)
	assert.Equal(t, "1", svc.Spec.TaskTemplate.ContainerSpec.Labels[labelGeneration],
		"the convergence write is a scale-only update (no task-template change)")
	assert.Equal(t, []string{w.ID}, converged.Changed, "the convergence write is a real write (the engine's converge audit rides on it)")

	// 拍 4：回到稳态——在新版本号上短路，零新增 update。
	after := run()
	assert.Equal(t, 1, d.count(updateKey))
	assert.Empty(t, after.Changed)
}

// TestEnsureConvergesExternalScaleDownColdLedger：同一场景在冷账本（引擎重启
// 形态）下本就成立——等价闸看到副本数不同即落到 scale-only。钉住它是为了
// 说明缺陷只在账本温热时出现，修复不改变冷账本路径的语义。
func TestEnsureConvergesExternalScaleDownColdLedger(t *testing.T) {
	d := newFakeDaemon()
	p := &Provider{cli: d.newClient(t)}
	ctx := context.Background()
	name := ensureCarrierName()
	updateKey := "POST /services/srv-" + name + "/update"
	w := ensureWorkload()
	w.Replicas = 1

	_, err := p.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	d.scaleExternal(t, name, 0)

	cold := &Provider{cli: d.newClient(t)}
	report, err := cold.Ensure(ctx, ensureNS, []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	assert.Equal(t, 1, d.count(updateKey))
	assert.EqualValues(t, 1, d.replicasOf(t, name))
	assert.Equal(t, []string{w.ID}, report.Changed)
}
