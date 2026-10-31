package engine

// B2a W2 诊断 facts 装配器测试（zeabur 借鉴设计 §2.4）：观测导出面 →
// Facts 映射（RestartCount 事件账本派生/Process 窄导出/近窗事件合并）、
// 告警态行读面（firing+pending——W1b 持久化列）、None 三态透出（读不了 ≠
// 不存在：事件/告警面读失败 → None + UnreadableFaces；组件健康面 B2b 前
// 恒 None）、廉价先行筛命中矩阵。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// failingOutboxRepo 只覆写 ListByAggregate（注入事件面读故障；其余方法走
// 嵌入接口）——Events None 腿的夹具。
type failingOutboxRepo struct {
	outbox.Repo
	err error
}

func (f failingOutboxRepo) ListByAggregate(context.Context, string, string, string, int64, int) ([]outbox.Event, error) {
	return nil, f.err
}

// failingAlertRuleRepo 只覆写 ListByApp（注入告警面读故障）——Alerts None
// 腿的夹具。
type failingAlertRuleRepo struct {
	alertrule.Repo
	err error
}

func (f failingAlertRuleRepo) ListByApp(context.Context, string) ([]alertrule.Rule, error) {
	return nil, f.err
}

// seedCrashLoop 部署 spec 并注入 n 次容器终态（新实例逐次——终态事实逐次
// 入账，计数才能纯派生），返回 App/Workload 锚。
func seedCrashLoop(t *testing.T, e *Engine, n int) {
	t.Helper()
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx) // Ensure gen=1：归属 + ensuredSpec（process 面）落观测缓存
	exit := 1
	for i := 0; i < n; i++ {
		e.handleObservation(ctx, capability.WorkloadEvent{
			WorkloadID: tAppID + "-web", Generation: 1, State: capability.WorkloadFailed,
			ExitCode: &exit, Reason: "task: non-zero exit (1)", Instance: "task-" + string(rune('a'+i)),
		})
	}
}

// TestDiagnoseFactsAssemblesObservationsEventsAlerts：三件咬合全链——观测
// 导出面快照 + 事件账本派生 + 告警行读面 → Facts 映射（RestartCount/
// Process/LastExitAt/近窗事件/告警态 firing+pending）；组件健康面 B2b 前
// 恒 None。
func TestDiagnoseFactsAssemblesObservationsEventsAlerts(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	seedCrashLoop(t, e, 3)

	// 告警行读面：一条 firing + 一条 pending（W1b 持久化列）+ 一条 ok。
	now := state.FormatTime(clock.Now())
	require.NoError(t, e.alertRules.Create(ctx, &alertrule.Rule{ID: "r1", AppID: tAppID, Metric: alertrule.MetricCPUPercent, Threshold: 90, Enabled: true}))
	require.NoError(t, e.alertRules.Transition(ctx, "r1", alertrule.StateFiring, now))
	require.NoError(t, e.alertRules.Create(ctx, &alertrule.Rule{ID: "r2", AppID: tAppID, Metric: alertrule.MetricMemoryWorkingSet, Threshold: 1, Enabled: true}))
	require.NoError(t, e.alertRules.BeginPending(ctx, "r2", now))
	require.NoError(t, e.alertRules.Create(ctx, &alertrule.Rule{ID: "r3", AppID: tAppID, Metric: alertrule.MetricCPUPercent, Threshold: 2, Enabled: true}))

	facts := e.DiagnoseFacts(ctx, tAppID)

	// 观测面：Some、活状态在面、RestartCount 账本派生、Process 窄导出。
	obs, ok := facts.Observations.Value()
	require.True(t, ok, "observations face is readable (cache read)")
	require.Len(t, obs, 1)
	assert.Equal(t, tAppID+"-web", obs[0].WorkloadID)
	assert.Equal(t, "web", obs[0].Process, "process rides the WorkloadProcess narrow export")
	assert.Equal(t, capability.WorkloadFailed, obs[0].State)
	require.NotNil(t, obs[0].ExitCode)
	assert.Equal(t, 1, *obs[0].ExitCode)
	assert.Equal(t, 3, obs[0].RestartCount, "restart count derives purely from the event ledger")
	assert.False(t, obs[0].LastExitAt.IsZero(), "last exit time derives from ledger facts")

	// 事件面：Some、近窗 container_exited 三行在面（Seq 升序、载荷原文；
	// 播种触发的 crash_loop 沿 marker 也是合法近窗事件行——聚合轴同源）。
	events, ok := facts.Events.Value()
	require.True(t, ok)
	exitRows := 0
	for i, ev := range events {
		if ev.Code == eventWorkloadContainerExit {
			exitRows++
		}
		assert.Contains(t, ev.Detail, `"workload_id":"`+tAppID+"-web")
		if i > 0 {
			assert.Greater(t, ev.Seq, events[i-1].Seq, "events merge in seq order")
		}
	}
	assert.Equal(t, 3, exitRows)
	assert.Contains(t, codesOf(events), eventCrashLoopEntered, "the transition marker rides the same aggregate axis")

	// 告警面：firing/pending/ok 三态如实投影（诊断吃 pending 态）。
	alerts, ok := facts.Alerts.Value()
	require.True(t, ok)
	states := map[string]string{}
	for _, a := range alerts {
		states[a.Rule] = a.State
	}
	assert.Equal(t, map[string]string{"r1": "firing", "r2": "pending", "r3": "ok"}, states)

	// 组件健康面：B2b 前恒 None——unreadable 透出「诊断未覆盖」。
	assert.True(t, facts.Components.IsNone())
	assert.Equal(t, []string{diagnosis.FaceComponents}, facts.UnreadableFaces())
}

// TestDiagnoseFactsNoneFacesOnUnreadableSources：读不了 ≠ 不存在——事件面
// 读故障 → Events None（且观测行 RestartCount 无从派生）；告警面读故障 →
// Alerts None；两者都进 UnreadableFaces，观测面保持 Some。
func TestDiagnoseFactsNoneFacesOnUnreadableSources(t *testing.T) {
	boom := errors.New("injected source failure")
	e, _, _ := newTestEngineMutated(t, Options{}, discardLogger(), func(s *transact.Stores) {
		s.Outbox = failingOutboxRepo{Repo: s.Outbox, err: boom}
		s.AlertRules = failingAlertRuleRepo{Repo: s.AlertRules, err: boom}
	})
	ctx := context.Background()
	seedCrashLoop(t, e, 3)

	facts := e.DiagnoseFacts(ctx, tAppID)

	obs, ok := facts.Observations.Value()
	require.True(t, ok, "the observation face stays readable (in-memory export)")
	require.Len(t, obs, 1)
	assert.Equal(t, 0, obs[0].RestartCount, "restart count is underivable while the event face is unreadable (not guessed)")

	assert.True(t, facts.Events.IsNone(), "event face read failure must surface as None, not an error or an empty set")
	assert.True(t, facts.Alerts.IsNone(), "alert face read failure must surface as None")
	assert.ElementsMatch(t,
		[]string{diagnosis.FaceEvents, diagnosis.FaceAlerts, diagnosis.FaceComponents},
		facts.UnreadableFaces(), "unreadable faces are surfaced for honest display")
}

// TestDiagnoseAppCheapHitMatrix：先行筛命中矩阵——失败态全集命中（failed/
// degraded/orphaned/stopped——v1 规则输入态超集），健康态（running/pending/
// completed）与零观测不命中。
func TestDiagnoseAppCheapHitMatrix(t *testing.T) {
	e, _, _ := newTestEngine(t)
	// 域内播种两 App 各一载体。
	e.obs.recordSpecs(appOwner(tAppID), "dep-1", 1, []capability.Workload{{ID: tAppID + "-web", Process: "web", Generation: 1}})
	e.obs.recordSpecs(appOwner(tApp2ID), "dep-2", 1, []capability.Workload{{ID: tApp2ID + "-web", Process: "web", Generation: 1}})
	for _, st := range []capability.WorkloadState{
		capability.WorkloadFailed, capability.WorkloadDegraded, capability.WorkloadOrphaned, capability.WorkloadStopped,
	} {
		e.obs.observe(capability.WorkloadEvent{WorkloadID: tAppID + "-web", Generation: 1, State: st})
		assert.True(t, e.DiagnoseAppCheapHit(tAppID), "state %s must hit the cheap pre-filter", st)
	}
	for _, st := range []capability.WorkloadState{
		capability.WorkloadRunning, capability.WorkloadPending, capability.WorkloadCompleted,
	} {
		e.obs.observe(capability.WorkloadEvent{WorkloadID: tApp2ID + "-web", Generation: 1, State: st})
		assert.False(t, e.DiagnoseAppCheapHit(tApp2ID), "state %s must not hit the cheap pre-filter", st)
	}
	// 零观测 App（冷窗/无载体）：不命中——深诊断必空，预算不花。
	assert.False(t, e.DiagnoseAppCheapHit("01JDNOOBS00000000000000000"))
}

// TestDiagnoseFactsWindowHonored：近窗纪律——窗外终态行不入计数与事件面
// （与 DeriveTerminalCounts 同窗；时钟推进后旧事实出窗）。
func TestDiagnoseFactsWindowHonored(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	seedCrashLoop(t, e, 3)

	// 窗内：计数 3（+ 播种触发的 entered 沿 marker 同窗在面）。
	facts := e.DiagnoseFacts(ctx, tAppID)
	obs, _ := facts.Observations.Value()
	assert.Equal(t, 3, obs[0].RestartCount)
	events, _ := facts.Events.Value()
	assert.Equal(t, 3, countCode(events, eventWorkloadContainerExit))

	// 出窗（DiagnoseWindow + 余量）：计数归零、事件面空集（可读且确无）。
	clock.Advance(DiagnoseWindow + time.Minute)
	facts = e.DiagnoseFacts(ctx, tAppID)
	obs, _ = facts.Observations.Value()
	assert.Equal(t, 0, obs[0].RestartCount, "terminal facts age out of the near window")
	events, _ = facts.Events.Value()
	assert.Empty(t, events)
}

// codesOf 取事件名序列（断言便捷面）。
func codesOf(events []diagnosis.EventFact) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Code)
	}
	return out
}

// countCode 数某事件名的行数。
func countCode(events []diagnosis.EventFact, code string) int {
	n := 0
	for _, ev := range events {
		if ev.Code == code {
			n++
		}
	}
	return n
}
