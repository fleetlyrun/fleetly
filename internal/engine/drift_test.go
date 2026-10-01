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

// 基线重放竞态回归（N0.1 P1-3）：重放 Ensure 在途时受理新部署——App 级
// 互斥使 Submit 排队到重放收口之后，驱动器的 gen2 Ensure 是最后一次
// 下发。旧实现的窗口：busy 集是启动快照，重放期间受理的部署由驱动器
// Ensure 新 Generation，与重放的旧 Generation 对翻载体标签 → 健康部署
// 被 L1 误判失败并回滚。
func TestBaselineReplayRaceWithAdmission(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	before := len(rt.calls())
	rt.mu.Lock()
	rt.ensureEntered = make(chan struct{}, 8)
	rt.blockPoint = make(chan struct{})
	rt.mu.Unlock()

	replayDone := make(chan struct{})
	go func() {
		defer close(replayDone)
		e.rebuildBaselines(context.Background())
	}()
	<-rt.ensureEntered // 重放的 Ensure 已进入并停在 blockPoint

	// 重放在途时受理新部署：Submit 必须等在 App 级互斥上。
	rev2 := freezeSpec(t, e, 2, `{"schema_version":1,"app":{"id":"`+tAppID+`","project":"`+tProjectID+`"},`+
		`"source":{"image":{"ref":"nginx:1.28"}},"processes":[{"name":"web","image":"nginx:1.28","replicas":1}]}`)
	type submitResult struct {
		d   *deployment.Deployment
		err error
	}
	submitCh := make(chan submitResult, 1)
	go func() {
		d, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev2})
		submitCh <- submitResult{d: d, err: err}
	}()
	select {
	case <-submitCh:
		t.Fatal("submit must wait for the in-flight baseline replay (app-level mutex)")
	case <-time.After(50 * time.Millisecond):
	}

	// 放行重放：gen1 重放完成后 Submit 才落行，驱动器的 gen2 Ensure 必然
	// 是最后一次下发（对翻不可能）。
	close(rt.blockPoint)
	<-replayDone
	res := <-submitCh
	require.NoError(t, res.err)

	e.step(context.Background())
	e.handleObservation(context.Background(), workloadEventRunning(tAppID+"-web", 2))
	e.step(context.Background())
	clock.Advance(61 * time.Second)
	e.step(context.Background())
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, res.d.ID).State,
		"the deployment admitted mid-replay must converge, not be misjudged and rolled back")

	calls := rt.calls()
	require.Greater(t, len(calls), before)
	assert.Equal(t, capability.Generation(2), calls[len(calls)-1].Gen,
		"the driver's ensure must be the last dispatch (replay cannot flip the carrier back to the old generation)")
}

// 重放前复查（N0.1 P1-3 第二层）：快照后受理的活跃部署使该 App 的重放
// 跳过（Ensure 权交给驱动器，不额外下发）。
func TestBaselineReplayRecheckSkipsActiveApp(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	d1 := driftDeployToSucceeded(t, e, rt, clock, rev)
	_ = d1

	// 受理 gen2 但不驱动（活跃在场：queued 属活跃态）。
	rev2 := freezeSpec(t, e, 2, `{"schema_version":1,"app":{"id":"`+tAppID+`","project":"`+tProjectID+`"},`+
		`"source":{"image":{"ref":"nginx:1.28"}},"processes":[{"name":"web","image":"nginx:1.28","replicas":1}]}`)
	_, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)

	before := len(rt.calls())
	appRow, err := e.apps.Get(context.Background(), e.db.Runner(), tAppID)
	require.NoError(t, err)
	e.replayAppBaseline(context.Background(), appRow, d1)
	assert.Len(t, rt.calls(), before, "replay must skip an app with an active deployment")
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

// commandSpec 是带入口覆盖命令的 spec 形态（C-11：Command 对照面）。
const commandSpec = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
	`"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
	`{"name":"web","image":"nginx:1.27","replicas":1,"command":["sleep","3600"]}]}`

// C-11 回归：人工改载体入口命令（docker service update --command 等价）
// → workload.drift_detected 带明细（message 含期望/观测两侧命令）。
func TestSpecDriftDetectedOnManualCommandEdit(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, commandSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	// 人工改命令（不动 fleetly 标记 = Generation 不变）。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {command: []string{"sleep", "9999"}}}
	rt.mu.Unlock()
	e.driftScan(context.Background())

	require.Equal(t, []string{"workload.drift_detected"}, eventNames(t, e, tAppID+"-web"),
		"a manual command edit must raise drift")
	var payload string
	evs, err := e.outbox.ListAfter(context.Background(), e.db.Runner(), 0, 100)
	require.NoError(t, err)
	for _, ev := range evs {
		if ev.AggregateID == tAppID+"-web" {
			payload = string(ev.Payload)
		}
	}
	require.Contains(t, payload, "command", "the drift event must carry the command mismatch detail")
	require.Contains(t, payload, "sleep", "the mismatch detail must show the observed command")

	// 回归期望 → 签名清。
	rt.mu.Lock()
	rt.tamper = nil
	rt.mu.Unlock()
	e.driftScan(context.Background())
	assert.Len(t, eventNames(t, e, tAppID+"-web"), 1)
}

// C-11 对翻否定面：带 command 部署后的对照拍零 drift（观测 = 期望命令，
// nil/空切片等价；对翻否定断言沿用 TestNoFalseDriftOnTagDeploySpecCompare
// 形态——两轮都零才钉死）。
func TestNoFalseDriftOnCommandDeploySpecCompare(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, commandSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	e.driftScan(context.Background())
	e.driftScan(context.Background())
	for _, name := range eventNames(t, e, "") {
		require.NotEqual(t, "workload.drift_detected", name,
			"spec compare must not raise drift for an unmodified command carrier")
	}
}
