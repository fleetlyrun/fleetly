package engine

// B2a W1b facts 地基测试（zeabur 借鉴设计 §2.4）：容器终态事实入账
// （幂等沿事件面：同 workload 同代同终态实例的重复观测不重复发射）、
// WorkloadEvent 快照导出面（快照与缓存一致性）、终态计数纯派生（计数
// 完全由事件切片计算，无独立计数器状态）。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// eventsByName 取某事件名的账本行（载荷断言面）。
func eventsByName(t *testing.T, e *Engine, name string) []eventWithPayload {
	t.Helper()
	rows, err := e.repos.Outbox.ListAfter(context.Background(), 0, 1000, "")
	require.NoError(t, err)
	var out []eventWithPayload
	for _, r := range rows {
		if r.Name == name {
			out = append(out, eventWithPayload{Aggregate: r.Aggregate, AggregateID: r.AggregateID, Payload: r.Payload})
		}
	}
	return out
}

type eventWithPayload struct {
	Aggregate   string
	AggregateID string
	Payload     []byte
}

// TestContainerExitedFactEntersLedger：App 域载体的异常终态观测 →
// workload.container_exited 入账（ExitCode/Reason/workload id/process 全
// 面在载荷；workload 聚合归档与 drift/stopped 同形态）。
func TestContainerExitedFactEntersLedger(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx) // Ensure gen=1：归属 + ensuredSpec（process 面）落观测缓存

	exit := 137
	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: 1, State: capability.WorkloadFailed,
		ExitCode: &exit, Reason: "task: non-zero exit (137)", Instance: "task-1",
	})

	rows := eventsByName(t, e, eventWorkloadContainerExit)
	require.Len(t, rows, 1)
	assert.Equal(t, "workload", rows[0].Aggregate, "filed under the workload aggregate like drift/stopped")
	assert.Equal(t, tAppID+"-web", rows[0].AggregateID)
	var p containerExitedEventPayload
	require.NoError(t, json.Unmarshal(rows[0].Payload, &p))
	assert.Equal(t, tAppID+"-web", p.WorkloadID)
	assert.Equal(t, tAppID, p.AppID)
	assert.Equal(t, "web", p.Process)
	assert.Equal(t, "failed", p.State)
	require.NotNil(t, p.ExitCode)
	assert.Equal(t, 137, *p.ExitCode)
	assert.Equal(t, "task: non-zero exit (137)", p.Reason)
	assert.Equal(t, "task-1", p.Instance)
}

// TestContainerExitedDebouncePerInstance：幂等纪律沿事件面——去抖签名含
// 实例身份，同实例的重复轮询不重复发射；重启循环的新 task（新实例）是
// 新事实照常入账（逐次入账，计数才能纯派生）；正常完成（退出码 0）与
// 非平台管辖载体不入账。
func TestContainerExitedDebouncePerInstance(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)

	exit := 1
	term := func(instance string) capability.WorkloadEvent {
		return capability.WorkloadEvent{
			WorkloadID: tAppID + "-web", Generation: 1, State: capability.WorkloadFailed,
			ExitCode: &exit, Reason: "task: non-zero exit (1)", Instance: instance,
		}
	}
	e.handleObservation(ctx, term("task-a")) // 崩溃 1：入账
	e.handleObservation(ctx, term("task-a")) // 同实例重复轮询：不重发
	assert.Len(t, eventsByName(t, e, eventWorkloadContainerExit), 1,
		"repeated polls of the same terminal instance must not re-emit")

	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1)) // 回 running 清签名
	e.handleObservation(ctx, term("task-b"))                         // 新 task：新事实
	assert.Len(t, eventsByName(t, e, eventWorkloadContainerExit), 2,
		"a restarted task exiting again is a new fact and must enter the ledger")

	// 正常完成（退出码 0）不是异常终态：不入账。
	done := 0
	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: 1, State: capability.WorkloadCompleted,
		ExitCode: &done, Instance: "task-c",
	})
	// 非平台管辖载体：观测缓存照登，事实不落（与 drift/stopped 同口径）。
	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: "stray-carrier", Generation: 1, State: capability.WorkloadFailed,
		ExitCode: &exit, Instance: "task-x",
	})
	assert.Len(t, eventsByName(t, e, eventWorkloadContainerExit), 2)
}

// TestContainerExitedDebounceSamePodRestarts（修复批 F3）：k3s 形态的
// CrashLoopBackOff 观测（Degraded + LastTerminationState 退出码，Instance
// = pod 名跨重启稳定）逐次入账——去抖签名的重启序数段区分同 pod 的逐次
// 容器终止；同序数的重复轮询不重发（崩溃循环计数在 k3s 侧才能纯派生）。
func TestContainerExitedDebounceSamePodRestarts(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)

	exit := 1
	k3sTerm := func(restarts int32) capability.WorkloadEvent {
		return capability.WorkloadEvent{
			WorkloadID: tAppID + "-web", Generation: 1, State: capability.WorkloadDegraded,
			ExitCode: &exit, Reason: "Error", Instance: "web-5f9d8", RestartCount: restarts,
		}
	}
	// 第一次终止（backoff 回放）：入账。
	e.handleObservation(ctx, k3sTerm(1))
	e.handleObservation(ctx, k3sTerm(1)) // 同序数重复轮询：不重发
	assert.Len(t, eventsByName(t, e, eventWorkloadContainerExit), 1,
		"repeated polls of the same pod restart must not re-emit")

	// 同 pod 二次重启（RestartCount 递增）：新事实照常入账——签名区分度
	// 来自重启序数段（pod 名与退出码均未变）。
	e.handleObservation(ctx, k3sTerm(2))
	assert.Len(t, eventsByName(t, e, eventWorkloadContainerExit), 2,
		"the next restart of the same pod is a new terminal fact (sig restart segment)")

	// 签名直断言：同实例同退出码、序数异 → 签名异。
	assert.NotEqual(t, terminalExitSig(k3sTerm(1)), terminalExitSig(k3sTerm(2)))
}

// TestObservationSnapshotMatchesCache：导出面快照与 obs 缓存一致（App 域
// 归属圈定、活状态 State/ExitCode/Reason 在面、字典序稳定、副本不回写、
// 异 App 载体隔离）。
func TestObservationSnapshotMatchesCache(t *testing.T) {
	e, _, _ := newTestEngine(t)
	// 域内播种：App 域两载体 + 异 App 载体（隔离面）。
	e.obs.recordSpecs(appOwner(tAppID), "dep-1", 7, []capability.Workload{
		{ID: tAppID + "-web", Process: "web", Generation: 7},
		{ID: tAppID + "-worker", Process: "worker", Generation: 7},
	})
	e.obs.recordSpecs(appOwner(tApp2ID), "dep-2", 3, []capability.Workload{{ID: tApp2ID + "-web", Process: "web"}})

	exit := 137
	e.obs.observe(capability.WorkloadEvent{WorkloadID: tAppID + "-web", Generation: 7, State: capability.WorkloadRunning})
	e.obs.observe(capability.WorkloadEvent{
		WorkloadID: tAppID + "-worker", Generation: 7, State: capability.WorkloadFailed,
		ExitCode: &exit, Reason: "oom",
	})
	e.obs.observe(capability.WorkloadEvent{WorkloadID: tApp2ID + "-web", Generation: 3, State: capability.WorkloadRunning})

	ctx := context.Background()
	snap := e.ObservationSnapshot(ctx, tAppID)
	require.Len(t, snap, 2, "snapshot is scoped to the app's own carriers")
	assert.Equal(t, tAppID+"-web", snap[0].WorkloadID, "stable workload-id lexicographic order")
	assert.Equal(t, tAppID+"-worker", snap[1].WorkloadID)
	assert.Equal(t, capability.WorkloadRunning, snap[0].State)
	assert.Equal(t, capability.WorkloadFailed, snap[1].State, "live state (not the spec-facing constant)")
	require.NotNil(t, snap[1].ExitCode, "live exit code is on the exported face")
	assert.Equal(t, 137, *snap[1].ExitCode)
	assert.Equal(t, "oom", snap[1].Reason)

	// 快照与缓存一致：逐载体对照单点读。
	for _, ev := range snap {
		cached, ok := e.obs.observationOf(ev.WorkloadID)
		require.True(t, ok)
		assert.Equal(t, cached, ev)
	}

	// 副本语义：修改快照不回写缓存。
	snap[0].State = capability.WorkloadStopped
	again := e.ObservationSnapshot(ctx, tAppID)
	assert.Equal(t, capability.WorkloadRunning, again[0].State)
}

// TestDeriveTerminalCountsPureFromSlice：计数纯派生——崩溃循环计数完全
// 由事件切片计算（窗内过滤、终态事件过滤、主体缺席/未来行不计；乱序与
// 重复调用同结果、返回值突变不回染 = 无隐藏状态）。
func TestDeriveTerminalCountsPureFromSlice(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	mk := func(wid string, at time.Time) EventFact {
		return EventFact{Name: eventWorkloadContainerExit, WorkloadID: wid, OccurredAt: at}
	}
	events := []EventFact{
		mk("wl-a", now.Add(-1*time.Minute)),
		mk("wl-a", now.Add(-2*time.Minute)),
		mk("wl-a", now.Add(-3*time.Minute)),
		mk("wl-a", now.Add(-11*time.Minute)),                              // 窗外
		mk("wl-b", now.Add(-9*time.Minute)),                               // 第二载体
		{Name: eventWorkloadStopped, WorkloadID: "wl-a", OccurredAt: now}, // 非终态事实
		mk("", now),                      // 主体缺席
		mk("wl-c", now.Add(time.Minute)), // 未来行（时钟异常）
	}
	assert.Equal(t, map[string]int{"wl-a": 3, "wl-b": 1}, DeriveTerminalCounts(events, window, now),
		"crash-loop counts derive purely from the event slice")

	// 乱序同结果 + 重复调用同结果（纯函数，无隐藏状态）。
	shuffled := []EventFact{events[3], events[0], events[7], events[4], events[6], events[2], events[5], events[1]}
	assert.Equal(t, map[string]int{"wl-a": 3, "wl-b": 1}, DeriveTerminalCounts(shuffled, window, now))
	counts := DeriveTerminalCounts(events, window, now)
	counts["wl-a"] = 99 // 突变返回值不回染
	assert.Equal(t, 3, DeriveTerminalCounts(events, window, now)["wl-a"])
}

// TestTerminalCountsEndToEndFromLedger：三件咬合——发射点入账 →
// ListByAggregate 按聚合取近窗事件 → ContainerExitedFacts/DeriveTerminalCounts
// 派生崩溃循环计数（全链无独立计数器状态）。
func TestTerminalCountsEndToEndFromLedger(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)

	exit := 1
	term := func(instance string) capability.WorkloadEvent {
		return capability.WorkloadEvent{
			WorkloadID: tAppID + "-web", Generation: 1, State: capability.WorkloadFailed,
			ExitCode: &exit, Reason: "task: non-zero exit (1)", Instance: instance,
		}
	}
	for _, instance := range []string{"task-a", "task-b", "task-c"} {
		e.handleObservation(ctx, term(instance))
		e.handleObservation(ctx, term(instance)) // 重复轮询不入账
		clock.Advance(time.Minute)
	}

	// 诊断近窗查询形态：按 (team, aggregate, aggregate_id) 前向扫描。
	rows, err := e.repos.Outbox.ListByAggregate(ctx, "default", "workload", tAppID+"-web", 0, 100)
	require.NoError(t, err)
	// 三次终态事实入账（重复轮询不重发）；W2 起同聚合还承载 crash-loop
	// 通知沿 marker——按事件名计数（marker 不是终态事实）。
	exitRows := 0
	for _, r := range rows {
		if r.Name == eventWorkloadContainerExit {
			exitRows++
		}
	}
	require.Equal(t, 3, exitRows, "three crash facts survive, duplicate polls do not")
	counts := DeriveTerminalCounts(ContainerExitedFacts(rows), 10*time.Minute, clock.Now())
	assert.Equal(t, map[string]int{tAppID + "-web": 3}, counts,
		"the crash-loop count is derived from the ledger alone")
}
