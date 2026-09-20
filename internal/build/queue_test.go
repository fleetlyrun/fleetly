package build

// 队列并发上限测试（T2.8 验收「构建队列并发 ≤2」）：注入假执行器（阻塞
// 信号量直到测试放行），并发统计在途执行数——峰值不得超过并发上限；排队
// 行保持 queued（排队可见）；终态失败不影响调度循环。

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// blockingExecutor 是并发测试的假执行器：Execute 记录在途数、阻塞在
// release 上直到测试放行或 ctx 取消。
type blockingExecutor struct {
	store     *state.Store
	inFlight  atomic.Int64
	maxSeen   atomic.Int64
	executed  atomic.Int64
	release   chan struct{}
	startedMu sync.Mutex
	started   map[string]chan struct{}
}

func newBlockingExecutor(st *state.Store, release chan struct{}) *blockingExecutor {
	return &blockingExecutor{
		store:   st,
		release: release,
		started: map[string]chan struct{}{},
	}
}

func (e *blockingExecutor) signalStarted(id string) chan struct{} {
	e.startedMu.Lock()
	defer e.startedMu.Unlock()
	ch, ok := e.started[id]
	if !ok {
		ch = make(chan struct{}, 1)
		e.started[id] = ch
	}
	return ch
}

func (e *blockingExecutor) waitStarted(id string) chan struct{} { return e.signalStarted(id) }

func (e *blockingExecutor) Execute(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error) {
	cur := e.inFlight.Add(1)
	for {
		max := e.maxSeen.Load()
		if cur <= max || e.maxSeen.CompareAndSwap(max, cur) {
			break
		}
	}
	e.executed.Add(1)
	e.waitStarted(rec.ID) <- struct{}{}
	select {
	case <-e.release:
	case <-ctx.Done():
	}
	e.inFlight.Add(-1)
	rec.Status = state.BuildSucceeded
	rec.ImageDigest = "sha256:test"
	if err := e.store.FinishBuildSucceeded(ctx, rec.ID, "ref", "sha256:test", "", ""); err != nil {
		return rec, err
	}
	return rec, nil
}

// newQueueTestStore 建独立状态库（TempDir，不落仓库根）。
func newQueueTestStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func enqueueTestBuild(t *testing.T, q *Queue, appID, service string) state.BuildRecord {
	t.Helper()
	rec, err := q.Enqueue(context.Background(), state.BuildRecord{
		AppID:   appID,
		Service: service,
		Driver:  state.DriverRailpack,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return rec
}

// TestQueueConcurrencyCap 并发 2 时 5 个构建的在途峰值不得超过 2。
func TestQueueConcurrencyCap(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-cap")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	release := make(chan struct{})
	exec := newBlockingExecutor(st, release)
	queue := NewQueue(st, exec, 2, 50*time.Millisecond, 0 /*超时取缺省*/, slog.New(slog.NewTextHandler(&nilWriter{}, nil)))
	if queue.Concurrency() != 2 {
		t.Fatalf("queue concurrency = %d, want 2", queue.Concurrency())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = queue.Run(ctx) }()

	var recs []state.BuildRecord
	for i := 0; i < 5; i++ {
		recs = append(recs, enqueueTestBuild(t, queue, app.ID, "svc"))
	}

	// 等到前两个真正开跑（信号量容量 = 2）。
	deadline := time.After(10 * time.Second)
	for exec.executed.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d executions started, want 2", exec.executed.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// 槽位满后，剩余行必须保持 queued（排队可见）。
	time.Sleep(200 * time.Millisecond)
	queued, err := st.NextQueuedBuilds(context.Background(), 10)
	if err != nil {
		t.Fatalf("scan queued: %v", err)
	}
	if len(queued) != 3 {
		t.Fatalf("queued rows = %d, want 3 (rows beyond the concurrency cap must not be claimed early)", len(queued))
	}
	if peak := exec.maxSeen.Load(); peak > 2 {
		t.Fatalf("in-flight peak = %d, want <= 2", peak)
	}

	// 放行：全部执行完。
	close(release)
	deadline = time.After(10 * time.Second)
	for {
		done := 0
		for _, rec := range recs {
			row, err := st.GetBuild(context.Background(), rec.ID)
			if err != nil {
				t.Fatalf("get build: %v", err)
			}
			if row.Status == state.BuildSucceeded {
				done++
			}
		}
		if done == len(recs) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d/%d builds finished", done, len(recs))
		case <-time.After(20 * time.Millisecond):
		}
	}
	if peak := exec.maxSeen.Load(); peak > 2 {
		t.Fatalf("in-flight peak = %d, want <= 2", peak)
	}
	cancel()
}

// TestQueueWakesOnEnqueue 同进程入队立即触发扫描（不必等 tick）：执行器
// 无阻塞时构建快速到达终态。
func TestQueueWakesOnEnqueue(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-wake")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	release := make(chan struct{})
	close(release) // Execute 立即放行
	exec := newBlockingExecutor(st, release)
	queue := NewQueue(st, exec, 2, time.Hour /*tick 永不触发——全靠 wake*/, 0 /*超时取缺省*/, slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = queue.Run(ctx) }()

	rec := enqueueTestBuild(t, queue, app.ID, "web")
	deadline := time.After(5 * time.Second)
	for {
		row, err := st.GetBuild(context.Background(), rec.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if row.Status == state.BuildSucceeded {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("build still %s after wake-on-enqueue (wake signal broken?)", row.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestQueueExecuteFailureKeepsScheduling 执行失败（含 panic 防御外的一般
// 错误）不影响后续调度。
func TestQueueExecuteFailureKeepsScheduling(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-fail")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	var calls atomic.Int64
	exec := failingExecutor{store: st, calls: &calls, until: 1}
	queue := NewQueue(st, exec, 2, 20*time.Millisecond, 0 /*超时取缺省*/, slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = queue.Run(ctx) }()

	first := enqueueTestBuild(t, queue, app.ID, "a")
	second := enqueueTestBuild(t, queue, app.ID, "b")

	deadline := time.After(10 * time.Second)
	for {
		rowA, _ := st.GetBuild(context.Background(), first.ID)
		rowB, _ := st.GetBuild(context.Background(), second.ID)
		if rowA.Status == state.BuildFailed && rowB.Status == state.BuildSucceeded {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("first=%s second=%s, want failed then succeeded", rowA.Status, rowB.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// failingExecutor 前 until 次调用返回错误（终态由执行器自行落库——真实
// Builder 契约：失败也收敛终态）。
type failingExecutor struct {
	store *state.Store
	calls *atomic.Int64
	until int64
}

func (e failingExecutor) Execute(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error) {
	n := e.calls.Add(1)
	if n <= e.until {
		_ = e.store.FinishBuildFailed(ctx, rec.ID, "E_BUILD_FAILED")
		return rec, errors.New("boom")
	}
	_ = e.store.FinishBuildSucceeded(ctx, rec.ID, "ref", "sha256:ok", "", "")
	return rec, nil
}

// nilWriter 是测试日志黑洞。
type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

// waitBuildStatus 轮询等待构建行到达期望状态（超时 fatal 带上下文说明）。
func waitBuildStatus(t *testing.T, st *state.Store, id string, want state.BuildStatus) state.BuildRecord {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		row, err := st.GetBuild(context.Background(), id)
		if err != nil {
			t.Fatalf("get build %s: %v", id, err)
		}
		if row.Status == want {
			return row
		}
		select {
		case <-deadline:
			t.Fatalf("build %s still %s, want %s", id, row.Status, want)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// assertAuditReason 断言指定构建行的 build.finish（result=error）审计 diff
// 携带期望归因文案（builds 表只存注册表错误码，兜底/复位路径的归因落点在
// 审计）。
func assertAuditReason(t *testing.T, st *state.Store, buildID, wantContains string) {
	t.Helper()
	audits, err := st.RecentAudits(context.Background(), 50)
	if err != nil {
		t.Fatalf("read audits: %v", err)
	}
	for _, a := range audits {
		if a.Target == "build:"+buildID && a.Action == "build.finish" && a.Result == "error" {
			if !strings.Contains(a.DiffSummary, wantContains) {
				t.Fatalf("audit diff = %s, want contains %q", a.DiffSummary, wantContains)
			}
			if a.ErrorCode != "E_BUILD_FAILED" {
				t.Fatalf("audit error_code = %s, want E_BUILD_FAILED", a.ErrorCode)
			}
			return
		}
	}
	t.Fatalf("no build.finish(error) audit for %s (reset/fallback did not write an audit?)", buildID)
}

// TestQueueStartupResetsInterruptedBuilds （MG-A3 crashpoint：重启恢复）
// 预置遗留 building 行 + 正常 queued 行后启动队列：building 行复位为 failed
// （E_BUILD_FAILED + finished_at + 审计中断归因），queued 行照常认领执行、
// 不受复位误伤。
func TestQueueStartupResetsInterruptedBuilds(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-reset")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	// 遗留 building 行：上一进程认领后崩溃/关停（无人收敛终态）。
	interrupted, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID: app.ID, Service: "web", Driver: state.DriverRailpack,
	})
	if err != nil {
		t.Fatalf("create interrupted: %v", err)
	}
	if err := st.ClaimBuild(context.Background(), interrupted.ID); err != nil {
		t.Fatalf("claim interrupted: %v", err)
	}
	// 正常 queued 行：重启后应被队列照常认领。
	pending, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID: app.ID, Service: "worker", Driver: state.DriverRailpack,
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}

	release := make(chan struct{})
	exec := newBlockingExecutor(st, release)
	queue := NewQueue(st, exec, 2, 20*time.Millisecond, 0, /*超时取缺省*/
		slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = queue.Run(ctx) }()

	// 遗留 building 行 → failed 终态（无人接手的行永不重跑，启动复位收敛）。
	row := waitBuildStatus(t, st, interrupted.ID, state.BuildFailed)
	if row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("reset error_code = %s, want E_BUILD_FAILED", row.ErrorCode)
	}
	if row.FinishedAt.IsZero() {
		t.Fatal("reset row must stamp finished_at")
	}
	assertAuditReason(t, st, interrupted.ID, "build interrupted")

	// queued 行不受复位影响：照常认领为 building（执行器阻塞中）。
	waitBuildStatus(t, st, pending.ID, state.BuildBuilding)

	// 收尾放行：正常路径收敛 succeeded（复位不干扰在途执行）。
	close(release)
	waitBuildStatus(t, st, pending.ID, state.BuildSucceeded)
}

// TestConvergeClaimedUnreadable M2-6：认领后行读取失败路径的兜底收敛语义
// ——滞留 building 行收敛 failed（E_BUILD_FAILED + finished_at + 审计归因
// 「认领后读取失败」），FailStrandedBuild 的行级 CAS 幂等（行已终态后二
// 次调用不误伤、不改写 finished_at）。触发面（GetBuild 瞬时错误）由
// drainOnce 的错误分支接入；*state.Store 为具体类型无法注入单次失败，
// 本测试钉死收敛语义本身。
func TestConvergeClaimedUnreadable(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-unreadable")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	rec := enqueueTestBuild(t, NewQueue(st, nil, 1, time.Hour, 0,
		slog.New(slog.NewTextHandler(&nilWriter{}, nil))), app.ID, "web")
	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	q := NewQueue(st, nil, 1, time.Hour, 0, slog.New(slog.NewTextHandler(&nilWriter{}, nil)))
	q.convergeClaimedUnreadable(context.Background(), rec.ID)
	row := waitBuildStatus(t, st, rec.ID, state.BuildFailed)
	if row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("error_code = %s, want E_BUILD_FAILED", row.ErrorCode)
	}
	if row.FinishedAt.IsZero() {
		t.Fatal("converged row must stamp finished_at")
	}
	assertAuditReason(t, st, rec.ID, "row read failed after build claim")

	// CAS 幂等：终态行不误伤（finished_at 不被二次收敛改写）。
	q.convergeClaimedUnreadable(context.Background(), rec.ID)
	again, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if again.Status != state.BuildFailed || !again.FinishedAt.Equal(row.FinishedAt) {
		t.Fatalf("second convergence clobbered a terminal row: status=%s finished_at=%v (want unchanged)", again.Status, again.FinishedAt)
	}
}

// panicThenOkExecutor 首次 Execute panic（单条恶意构建注入面），后续正常
// 收敛 succeeded——验证调度存活。
type panicThenOkExecutor struct {
	store *state.Store
	calls atomic.Int64
}

func (e *panicThenOkExecutor) Execute(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error) {
	if e.calls.Add(1) == 1 {
		panic("injected panic: malicious build input")
	}
	if err := e.store.FinishBuildSucceeded(ctx, rec.ID, "ref", "sha256:ok", "", ""); err != nil {
		return rec, err
	}
	return e.store.GetBuild(ctx, rec.ID)
}

// TestQueuePanickingBuildDoesNotKillScheduler M2-7（MG-1 同族：外部输入驱动
// 的执行路径必须有 panic 边界）：执行器 panic 的构建单条收敛 failed，调度
// 循环存活，后续构建照常执行。
func TestQueuePanickingBuildDoesNotKillScheduler(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-panic")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	exec := &panicThenOkExecutor{store: st}
	queue := NewQueue(st, exec, 2, 20*time.Millisecond, 0, /*超时取缺省*/
		slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = queue.Run(ctx) }()

	// 首条：panic → 兜底收敛 failed（不滞留 building、不打崩进程）。
	first := enqueueTestBuild(t, queue, app.ID, "web")
	row := waitBuildStatus(t, st, first.ID, state.BuildFailed)
	if row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("panic row error_code = %s, want E_BUILD_FAILED", row.ErrorCode)
	}
	assertAuditReason(t, st, first.ID, "build executor exited abnormally")

	// 次条：调度存活，照常执行收敛 succeeded。
	second := enqueueTestBuild(t, queue, app.ID, "worker")
	waitBuildStatus(t, st, second.ID, state.BuildSucceeded)
}

// TestQueueWakeFiresAfterSlotRelease M2-10：满槽时完成一条，排队行必须在
// poll 周期内被认领——补位信号来自「槽位释放之后」的 Wake，而非入队/启动
// 时的无效唤醒（tick 设 1h：永不触发，补位只能走释放后 Wake；回归形态下
// 第二条要等 tick 即超时失败）。
func TestQueueWakeFiresAfterSlotRelease(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-wake-slot")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	release := make(chan struct{})
	exec := newBlockingExecutor(st, release)
	// 并发 1 + tick 1h：唯一补位路径 = 完成后的「释放 + Wake」defer。
	queue := NewQueue(st, exec, 1, time.Hour, 0, /*超时取缺省*/
		slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = queue.Run(ctx) }()

	first := enqueueTestBuild(t, queue, app.ID, "web")
	select {
	case <-exec.waitStarted(first.ID):
	case <-time.After(5 * time.Second):
		t.Fatal("first build never started")
	}
	// 满槽期入队第二条：Enqueue 的 Wake 是无效信号（drainOnce 撞满信号量即
	// 返回），必须保持 queued。
	second := enqueueTestBuild(t, queue, app.ID, "worker")
	time.Sleep(200 * time.Millisecond)
	if row, err := st.GetBuild(context.Background(), second.ID); err != nil || row.Status != state.BuildQueued {
		t.Fatalf("second = %v/%v, want queued (must not be claimed while the slot is full)", row.Status, err)
	}

	// 放行首条：完成 → 槽位释放 → Wake → 第二条在 tick（1h）之前被认领。
	close(release)
	select {
	case <-exec.waitStarted(second.ID):
	case <-time.After(5 * time.Second):
		t.Fatal("second build was not claimed after slot release (wake fired at the wrong time: waiting for tick — M2-10)")
	}
	waitBuildStatus(t, st, first.ID, state.BuildSucceeded)
	waitBuildStatus(t, st, second.ID, state.BuildSucceeded)
}

// hungThenOkExecutor 前 hung 次执行永不完成（阻塞到 ctx 取消后直接返回、
// 不落终态——模拟挂起的 solve 被超时取消且执行器异常路径未收敛）；后续
// 调用立即收敛 succeeded（验证超时后并发槽已释放）。
type hungThenOkExecutor struct {
	store *state.Store
	hung  atomic.Int64
	calls atomic.Int64
}

func (e *hungThenOkExecutor) Execute(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error) {
	if e.calls.Add(1) <= e.hung.Load() {
		<-ctx.Done()
		return rec, ctx.Err()
	}
	if err := e.store.FinishBuildSucceeded(ctx, rec.ID, "ref", "sha256:ok", "", ""); err != nil {
		return rec, err
	}
	return e.store.GetBuild(ctx, rec.ID)
}

// TestQueueBuildTimeoutConvergesFailed （MG-A3 crashpoint：执行超时）极小
// 超时预算（1s）+ 永不完成的执行器：超时 → 队列兜底 failed 终态
// （E_BUILD_FAILED + finished_at + 审计超时归因），并发槽随即释放（并发 1
// 下第二条构建正常执行）。
func TestQueueBuildTimeoutConvergesFailed(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "queue-timeout")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	exec := &hungThenOkExecutor{store: st}
	exec.hung.Store(1)
	queue := NewQueue(st, exec, 1, 20*time.Millisecond, time.Second, /*极小超时预算*/
		slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = queue.Run(ctx) }()

	// 挂起构建超时 → 兜底 failed 终态。
	first := enqueueTestBuild(t, queue, app.ID, "web")
	row := waitBuildStatus(t, st, first.ID, state.BuildFailed)
	if row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("timeout error_code = %s, want E_BUILD_FAILED", row.ErrorCode)
	}
	if row.FinishedAt.IsZero() {
		t.Fatal("timeout row must stamp finished_at")
	}
	assertAuditReason(t, st, first.ID, "build timed out")

	// 并发 1：槽位必须已释放——第二条构建正常收敛 succeeded。
	second := enqueueTestBuild(t, queue, app.ID, "web")
	waitBuildStatus(t, st, second.ID, state.BuildSucceeded)
}
