package build

// 队列并发上限测试（T2.8 验收「构建队列并发 ≤2」）：注入假执行器（阻塞
// 信号量直到测试放行），并发统计在途执行数——峰值不得超过并发上限；排队
// 行保持 queued（排队可见）；终态失败不影响调度循环。

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
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
	queue := NewQueue(st, exec, 2, 50*time.Millisecond, slog.New(slog.NewTextHandler(&nilWriter{}, nil)))
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
		t.Fatalf("queued rows = %d, want 3（并发上限内的行不得提前认领）", len(queued))
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
	queue := NewQueue(st, exec, 2, time.Hour /*tick 永不触发——全靠 wake*/, slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

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
			t.Fatalf("build still %s after wake enqueue（wake 信号失效？）", row.Status)
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
	queue := NewQueue(st, exec, 2, 20*time.Millisecond, slog.New(slog.NewTextHandler(&nilWriter{}, nil)))

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
