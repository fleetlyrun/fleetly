package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// ADR-0022 回归（N0 修复批 C1）。

// driftClock 是假时钟的最小消费面（Advance）。
type driftClock interface {
	Advance(time.Duration)
}

// driftDeployToSucceeded 走完整链到 succeeded（基线形态）。
func driftDeployToSucceeded(t *testing.T, e *Engine, rt *fakeRuntime, clock driftClock, rev string) *deployment.Deployment {
	t.Helper()
	d, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(context.Background())
	e.handleObservation(context.Background(), workloadEventRunning(tAppID+"-web", 1))
	e.step(context.Background())
	clock.Advance(61 * time.Second)
	e.step(context.Background())
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)
	return getDeployment(t, e, d.ID)
}

// 启动基线重放：重启后（新 Engine 实例、缓存清空）按 succeeded 基线重放
// Ensure——归属/期望缓存重建，drift 检测立即在场（无需下一次部署）。
func TestStartupBaselineReplayRebuildsDriftCache(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	// "重启"：新引擎实例（内存缓存清空），同库同 runtime。
	e2 := New(Deps{DB: e.db, Runtime: rt, Logger: e.log}, Options{})
	e2.rebuildBaselines(context.Background())

	calls := rt.calls()
	last := calls[len(calls)-1]
	assert.Equal(t, capability.Generation(1), last.Gen, "baseline replay re-ensures at the recorded generation")
	assert.Equal(t, "nginx:1.27", last.Spec["web"].Image)

	// 缓存重建后 drift 立即在场：旧 gen 观测 → drift 事件（无需新部署）。
	e2.handleObservation(context.Background(), capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: capability.Generation(0),
		State: capability.WorkloadStopped, Message: "manually updated",
	})
	assert.Equal(t, []string{"workload.drift_detected"}, eventNames(t, e2, tAppID+"-web"))
}

// 场景 7（领域模型）：人工改载体（镜像/副本，不动 fleetly 标记 = Generation
// 不变）→ 下一扫描拍必须出 drift 事件（gen-only 对照对此失明）。
func TestSpecDriftDetectedOnManualCarrierEdit(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	// 人工改镜像（docker service update --image 等价）。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	e.driftScan(context.Background())
	names := eventNames(t, e, tAppID+"-web")
	require.Equal(t, []string{"workload.drift_detected"}, names, "manual image swap must raise drift")

	// 去抖：同一失配不重复发。
	e.driftScan(context.Background())
	assert.Len(t, eventNames(t, e, tAppID+"-web"), 1)

	// 人工改副本数（scale 等价）→ 新签名 → 再发一条。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {replicas: 3}}
	rt.mu.Unlock()
	e.driftScan(context.Background())
	assert.Len(t, eventNames(t, e, tAppID+"-web"), 2, "a different mismatch is a new drift")

	// 回归期望 spec → 签名清（下次偏离可再发）。
	rt.mu.Lock()
	rt.tamper = nil
	rt.mu.Unlock()
	e.driftScan(context.Background())
	assert.Len(t, eventNames(t, e, tAppID+"-web"), 2)
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	e.driftScan(context.Background())
	assert.Len(t, eventNames(t, e, tAppID+"-web"), 3, "recovery clears the debounce signature")
}

// 稳态看门狗：succeeded 之后载体停止 → workload.stopped（只观测不迁移，
// 无 Deployment 状态变化）；观测恢复 → 签名清；在途部署期不发。
func TestSteadyStateWatchdogEmitsStopped(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	d := driftDeployToSucceeded(t, e, rt, clock, rev)

	// 稳态停止（当前 gen）→ workload.stopped。
	e.handleObservation(context.Background(), capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: capability.Generation(1),
		State: capability.WorkloadStopped, Message: "container exited",
	})
	e.driftScan(context.Background())
	assert.Equal(t, []string{"workload.stopped"}, eventNames(t, e, tAppID+"-web"))
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State,
		"steady-state stop must not touch the terminal deployment")

	// 去抖 + 恢复清签名。
	e.driftScan(context.Background())
	assert.Len(t, eventNames(t, e, tAppID+"-web"), 1)
	e.handleObservation(context.Background(), workloadEventRunning(tAppID+"-web", 1))
	e.handleObservation(context.Background(), capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: capability.Generation(1),
		State: capability.WorkloadStopped, Message: "container exited again",
	})
	e.driftScan(context.Background())
	assert.Len(t, eventNames(t, e, tAppID+"-web"), 2, "recovery then another stop emits again")
}
