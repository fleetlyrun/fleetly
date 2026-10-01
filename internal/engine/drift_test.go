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

// 防假 drift 回归（N0.1 P1-2 实证收口）：tag 与 digest 引用部署后的 spec
// 对照拍不得产 workload.drift_detected。复审假设"docker 把 tag 钉版为
// repo:tag@sha256 存入 spec → 逐字比对恒失配"经双腿实证推翻：
//   - dind（docker:29）：`docker service create` CLI 确会尝试钉版（不可达
//     时告警并存原样 tag）——钉版是 CLI 客户端行为；
//   - staging 生产（fleetly 经 moby API 直传 spec）：三个现役 tag 载体
//     spec.Image 全为原样 tag；6 次部署 + 全天 30s 扫描零 drift 事件。
//
// 本测试钉死"API 路径无钉版 → 逐字比对不产假 drift"；若未来观测到钉版
// 形态（obs = want@sha256:…），须带实证重开 compareSpecs 对照口径。
func TestNoFalseDriftOnTagDeploySpecCompare(t *testing.T) {
	for _, tc := range []struct {
		name  string
		image string
	}{
		{"tag reference", "nginx:1.27"},
		{"digest reference", "nginx@sha256:0ea7efa44f5c0f2b1fd4b1c39c4d1c66da6b1d6d3e1f6c8c8e6d8ad5e9f2a7b1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, rt, clock := newTestEngine(t)
			specJSON := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
				`"source":{"image":{"ref":"` + tc.image + `"}},"processes":[{"name":"web","image":"` + tc.image + `","replicas":1}]}`
			rev := freezeSpec(t, e, 1, specJSON)
			driftDeployToSucceeded(t, e, rt, clock, rev)

			// 连拍两轮：首拍与去抖拍都不得出 drift（假 drift 的形态恰是
			// "首拍一条、签名去抖后沉默"——两轮都零才钉死）。
			e.driftScan(context.Background())
			e.driftScan(context.Background())
			for _, name := range eventNames(t, e, "") {
				require.NotEqual(t, "workload.drift_detected", name,
					"spec compare must not raise drift for an unmodified carrier (image %s)", tc.image)
			}

			// 对照面：真失配仍要报（防回归不等于放松执法）。
			rt.mu.Lock()
			rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.28"}}
			rt.mu.Unlock()
			e.driftScan(context.Background())
			assert.Equal(t, []string{"workload.drift_detected"}, eventNames(t, e, tAppID+"-web"),
				"a real image mismatch must still raise drift")
		})
	}
}
