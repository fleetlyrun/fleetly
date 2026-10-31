package engine

// crash-loop 通知投递沿纪律测试（B2a W2，投递两类裁决的 crash-loop 类）：
// 迁移沿各一次——进入沿在第 3 次终态事实后一次（此后更多终态不重轰）、
// 退出沿在恢复观测后一次（此后稳态 running 零重轰）；沿记录 = 事件账本
// marker（重启安全——判定只读账本，无内存沿）；App 级其余 Finding 不投递
//（单发 OOM 终态零 marker）；真实复发（恢复后新循环达阈值）是新迁移沿。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// crashMarkers 取该 workload 聚合的沿 marker 名序列（升序）。
func crashMarkers(t *testing.T, e *Engine, workloadID string) []string {
	t.Helper()
	rows, err := e.repos.Outbox.ListByAggregate(context.Background(), "", "workload", workloadID, 0, 1000)
	require.NoError(t, err)
	var out []string
	for _, r := range rows {
		if r.Name == eventCrashLoopEntered || r.Name == eventCrashLoopCleared {
			out = append(out, r.Name)
		}
	}
	return out
}

// TestCrashLoopNotifyEntersOnce：进入沿一次——第 3 次终态（阈值达成）落
// entered marker；第 4/5 次终态（循环持续）不重轰。
func TestCrashLoopNotifyEntersOnce(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	seedCrashLoop(t, e, 2)
	wid := tAppID + "-web"
	require.Empty(t, crashMarkers(t, e, wid), "below the threshold there is no crash loop yet")

	exit := 1
	term := func(instance string) capability.WorkloadEvent {
		return capability.WorkloadEvent{
			WorkloadID: wid, Generation: 1, State: capability.WorkloadFailed,
			ExitCode: &exit, Reason: "task: non-zero exit (1)", Instance: instance,
		}
	}
	e.handleObservation(ctx, term("task-c")) // 第 3 次：阈值达成 → entered 一次
	assert.Equal(t, []string{eventCrashLoopEntered}, crashMarkers(t, e, wid))

	e.handleObservation(ctx, term("task-d")) // 循环持续：不重轰
	e.handleObservation(ctx, term("task-e"))
	assert.Equal(t, []string{eventCrashLoopEntered}, crashMarkers(t, e, wid),
		"a sustained loop must not re-notify (transition-edge discipline)")
}

// TestCrashLoopNotifyClearedOnceAndStaysQuiet：退出沿一次——恢复观测
// （failed→running）落 cleared；此后稳态 running 轮询零重轰；真实复发
// （恢复后新终态累积到阈值）重新进入沿一次。
func TestCrashLoopNotifyClearedOnceAndStaysQuiet(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	seedCrashLoop(t, e, 3)
	wid := tAppID + "-web"
	require.Equal(t, []string{eventCrashLoopEntered}, crashMarkers(t, e, wid))

	// 恢复：failed→running 迁移 → cleared 一次。
	e.handleObservation(ctx, workloadEventRunning(wid, 1))
	assert.Equal(t, []string{eventCrashLoopEntered, eventCrashLoopCleared}, crashMarkers(t, e, wid))

	// 稳态 running 轮询（prev 已 running）：零新沿。
	e.handleObservation(ctx, workloadEventRunning(wid, 1))
	e.handleObservation(ctx, workloadEventRunning(wid, 1))
	assert.Equal(t, []string{eventCrashLoopEntered, eventCrashLoopCleared}, crashMarkers(t, e, wid),
		"steady running polls must not re-notify (prev-state gate keeps them query-free)")

	// 真实复发：恢复后新终态逐次入账，计数重新达阈值 → entered 再一次
	//（新循环是新的迁移沿——沿纪律防重复推送，不防真实复发）。
	exit := 1
	term := func(instance string) capability.WorkloadEvent {
		return capability.WorkloadEvent{
			WorkloadID: wid, Generation: 1, State: capability.WorkloadFailed,
			ExitCode: &exit, Reason: "task: non-zero exit (1)", Instance: instance,
		}
	}
	e.handleObservation(ctx, term("task-f"))
	e.handleObservation(ctx, term("task-g"))
	e.handleObservation(ctx, term("task-h"))
	assert.Equal(t, []string{eventCrashLoopEntered, eventCrashLoopCleared, eventCrashLoopEntered},
		crashMarkers(t, e, wid), "a genuine relapse after recovery is a new transition edge")
}

// TestCrashLoopMarkerEdgesRideTheLedger：沿记录 = 账本 marker（重启安全）：
// 判定只读账本行——阈值达成后的又一终态观测（冷评估形态）不重轰：marker
// 已在账本，进程边界不改变沿状态。
func TestCrashLoopMarkerEdgesRideTheLedger(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	seedCrashLoop(t, e, 4) // 阈值后又一终态：entered 一次
	wid := tAppID + "-web"
	require.Equal(t, []string{eventCrashLoopEntered}, crashMarkers(t, e, wid))

	exit := 1
	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: wid, Generation: 1, State: capability.WorkloadFailed,
		ExitCode: &exit, Reason: "task: non-zero exit (1)", Instance: "task-z",
	})
	assert.Equal(t, []string{eventCrashLoopEntered}, crashMarkers(t, e, wid),
		"the edge record is the ledger marker; a cold re-evaluation does not re-notify")
}

// TestCrashLoopOtherFindingsDoNotDispatch：App 级其余 Finding 不投递——
// 单发 OOM 终态（137，计数 1 < 阈值）产出 OOM 诊断但零 crash-loop 沿
// marker（投递两类裁决：只有 crash-loop 类进通道）。
func TestCrashLoopOtherFindingsDoNotDispatch(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)

	exit := 137
	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: 1, State: capability.WorkloadFailed,
		ExitCode: &exit, Reason: "task: non-zero exit (137)", Instance: "task-oom",
	})
	// 诊断面：OOM 条目在（读时计算面正常产出）。
	findings := diagnosis.Evaluate(e.DiagnoseFacts(ctx, tAppID))
	require.NotEmpty(t, findings, "the OOM finding is produced by the read-time diagnosis face")
	assert.Equal(t, "F_WORKLOAD_OOM_WITHOUT_LIMIT", findings[0].ID())

	// 投递面：零沿 marker（无 crash-loop，无任何通知沿）。
	assert.Empty(t, crashMarkers(t, e, tAppID+"-web"),
		"non-crash-loop findings must not ride the notification channel (delivery two-kind ruling)")
}

// TestCrashLoopNotifyIgnoresNonAppDomains：非 App 域载体（受管域）不进
// 诊断评估点——零 marker、零账本终态行（与 container_exited 事实同口径）。
func TestCrashLoopNotifyIgnoresNonAppDomains(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	// 受管域载体直接经 recordOwners 播种归属（零 spec 面）。
	e.obs.recordOwners(3, []capability.Workload{{ID: "managed-proxy", Generation: 3}},
		func(w capability.Workload) workloadOwner { return systemOwner("proxy") })
	exit := 1
	for i := 0; i < 3; i++ {
		e.handleObservation(ctx, capability.WorkloadEvent{
			WorkloadID: "managed-proxy", Generation: 3, State: capability.WorkloadFailed,
			ExitCode: &exit, Instance: "task-m" + string(rune('a'+i)),
		})
	}
	assert.Empty(t, crashMarkers(t, e, "managed-proxy"),
		"managed-domain carriers are outside the App diagnosis/notification face")
}
