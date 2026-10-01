package task_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

func newTask(id, name string) *task.Task {
	return &task.Task{
		ID: id, ProjectID: "01JD0PRJ000000000000000000", Name: name,
		Form: task.FormOneShot, State: task.StateActive,
		Spec:               []byte(`{"schemaVersion":1}`),
		DesiredConcurrency: 1,
		DNSName:            "task-" + id,
	}
}

func TestTaskLifecycleTransit(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	tasks := task.New(clock)

	tk := newTask("01JD0TASK00000000000000000", "migrate")
	require.NoError(t, tasks.Create(ctx, db.Runner(), tk))

	// one-shot 终态：active → completed（终态补 finished_at）。
	clock.Advance(time.Second)
	require.NoError(t, tasks.Transit(ctx, db.Runner(), tk.ID,
		[]task.State{task.StateActive}, task.StateCompleted, nil))
	got, err := tasks.Get(ctx, db.Runner(), tk.ID)
	require.NoError(t, err)
	assert.Equal(t, task.StateCompleted, got.State)
	assert.True(t, got.State.Terminal())
	assert.Equal(t, "2026-01-01T00:00:01Z", got.FinishedAt)

	// 终态不再迁移。
	err = tasks.Transit(ctx, db.Runner(), tk.ID,
		[]task.State{task.StateActive}, task.StateDraining, nil)
	assert.ErrorIs(t, err, state.ErrConflict)

	// resident 排空链：active → draining → drained；lease_deadline 原地更新。
	r2 := newTask("01JD0TASK00000000000000001", "dispatcher")
	r2.Form = task.FormResident
	r2.DesiredConcurrency = 4
	require.NoError(t, tasks.Create(ctx, db.Runner(), r2))
	require.NoError(t, tasks.Transit(ctx, db.Runner(), r2.ID,
		[]task.State{task.StateActive}, task.StateDraining, nil))
	require.NoError(t, tasks.UpdateLease(ctx, db.Runner(), r2.ID, "2026-01-01T00:05:00Z"))
	require.NoError(t, tasks.Transit(ctx, db.Runner(), r2.ID,
		[]task.State{task.StateDraining}, task.StateDrained, nil))
	got, err = tasks.Get(ctx, db.Runner(), r2.ID)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-01T00:05:00Z", got.LeaseDeadline)
	assert.True(t, got.State.Terminal())
}

func TestTaskNameUniquePerProject(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	tasks := task.New(clock)

	require.NoError(t, tasks.Create(ctx, db.Runner(), newTask("01JD0TASK00000000000000000", "dispatcher")))
	err := tasks.Create(ctx, db.Runner(), newTask("01JD0TASK00000000000000001", "dispatcher"))
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// tombstone 释放名位（deleted 行不占名）。
	require.NoError(t, tasks.Transit(ctx, db.Runner(), "01JD0TASK00000000000000000",
		[]task.State{task.StateActive}, task.StateDeleted, nil))
	require.NoError(t, tasks.Create(ctx, db.Runner(), newTask("01JD0TASK00000000000000002", "dispatcher")))

	// 同名不同项目合法。
	other := newTask("01JD0TASK00000000000000003", "dispatcher")
	other.ProjectID = "01JD0PRJ000000000000000001"
	require.NoError(t, tasks.Create(ctx, db.Runner(), other))
}

func TestTaskListAndOwnerScan(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	tasks := task.New(clock)

	// ULID 递减构造（新→旧断言用）：后建的 id 更大。
	ids := []string{"01JD0TASK00000000000000000", "01JD0TASK00000000000000001", "01JD0TASK00000000000000002"}
	for i, id := range ids {
		tk := newTask(id, "")
		if i == 1 {
			tk.OwnerTokenID = "01JD0TOK0000000000000000000"
		}
		require.NoError(t, tasks.Create(ctx, db.Runner(), tk))
	}

	// 新→旧 + after 游标 + limit。
	page, err := tasks.ListByProject(ctx, db.Runner(), "01JD0PRJ000000000000000000", "", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{ids[2], ids[1]}, []string{page[0].ID, page[1].ID})
	next, err := tasks.ListByProject(ctx, db.Runner(), "01JD0PRJ000000000000000000", page[1].ID, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{ids[0]}, []string{next[0].ID})

	// 属主扫描面（吊销排空拉式，P1-8）。
	owned, err := tasks.ListByOwner(ctx, db.Runner(), "01JD0TOK0000000000000000000")
	require.NoError(t, err)
	assert.Len(t, owned, 1)
	assert.Equal(t, ids[1], owned[0].ID)

	// 驱动拾取：终态行不入。
	require.NoError(t, tasks.Transit(ctx, db.Runner(), ids[0],
		[]task.State{task.StateActive}, task.StateDeleted, nil))
	driving, err := tasks.ListDriving(ctx, db.Runner())
	require.NoError(t, err)
	assert.Len(t, driving, 2)
}

func TestRunStateMachine(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	runs := run.New(clock)

	m := &run.Run{
		ID: "01JD0RUN0000000000000000000", TaskID: "01JD0TASK00000000000000000",
		ProjectID: "01JD0PRJ000000000000000000", State: run.StatePending,
		WorkloadID: "01JD0RUN0000000000000000000", DNSName: "run-01jd0run0000000000000000000",
		Deadline:   "2026-01-01T01:00:00Z",
	}
	require.NoError(t, runs.Create(ctx, db.Runner(), m))

	// pending → running：started_at 补齐。
	clock.Advance(time.Second)
	require.NoError(t, runs.Transit(ctx, db.Runner(), m.ID,
		[]run.State{run.StatePending}, run.StateRunning, nil))
	got, err := runs.Get(ctx, db.Runner(), m.ID)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-01T00:00:01Z", got.StartedAt)
	assert.Nil(t, got.ExitCode)

	// running → stopping（起因即写入）→ stopped（终态携带 + 退出码）。
	require.NoError(t, runs.Transit(ctx, db.Runner(), m.ID,
		[]run.State{run.StateRunning}, run.StateStopping,
		func(r *run.Run) { r.StopReason = run.ReasonTTLExpired; r.Deadline = "2026-01-01T00:01:00Z" }))
	code := 0
	require.NoError(t, runs.Transit(ctx, db.Runner(), m.ID,
		[]run.State{run.StateStopping}, run.StateStopped,
		func(r *run.Run) { r.ExitCode = &code }))
	got, err = runs.Get(ctx, db.Runner(), m.ID)
	require.NoError(t, err)
	assert.Equal(t, run.StateStopped, got.State)
	assert.Equal(t, run.ReasonTTLExpired, got.StopReason)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 0, *got.ExitCode)
	assert.True(t, got.State.Terminal())
	assert.NotEmpty(t, got.FinishedAt)

	// 非法迁移与终态封锁。
	err = runs.Transit(ctx, db.Runner(), m.ID,
		[]run.State{run.StatePending, run.StateRunning}, run.StateStopping, nil)
	assert.ErrorIs(t, err, state.ErrConflict)

	// 自然完成直达：running → stopped/completed（不经 stopping）。
	m2 := &run.Run{ID: "01JD0RUN0000000000000000001", TaskID: "01JD0TASK00000000000000000",
		ProjectID: "01JD0PRJ000000000000000000", State: run.StateRunning}
	require.NoError(t, runs.Create(ctx, db.Runner(), m2))
	require.NoError(t, runs.Transit(ctx, db.Runner(), m2.ID,
		[]run.State{run.StateRunning}, run.StateStopped,
		func(r *run.Run) { r.StopReason = run.ReasonCompleted; r.ExitCode = &code }))
}

func TestRunListByTaskAndStates(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	runs := run.New(clock)

	taskID := "01JD0TASK00000000000000000"
	states := []run.State{run.StatePending, run.StateRunning, run.StateStopping, run.StateStopped, run.StateFailed}
	for i, s := range states {
		m := &run.Run{
			ID:        "01JD0RUN000000000000000000" + string(rune('0'+i)),
			TaskID:    taskID, ProjectID: "01JD0PRJ000000000000000000", State: s,
		}
		require.NoError(t, runs.Create(ctx, db.Runner(), m))
	}

	// 新→旧 + after 游标。
	page, err := runs.ListByTask(ctx, db.Runner(), taskID, "", 3)
	require.NoError(t, err)
	assert.Len(t, page, 3)
	assert.Equal(t, "01JD0RUN0000000000000000004", page[0].ID)
	next, err := runs.ListByTask(ctx, db.Runner(), taskID, page[2].ID, 3)
	require.NoError(t, err)
	assert.Len(t, next, 2)

	// 活槽位计数（补足判定面）。
	live, err := runs.ListByTaskStates(ctx, db.Runner(), taskID,
		[]run.State{run.StatePending, run.StateRunning})
	require.NoError(t, err)
	assert.Len(t, live, 2)

	// 驱动拾取：pending/running/stopping。
	driving, err := runs.ListDriving(ctx, db.Runner())
	require.NoError(t, err)
	assert.Len(t, driving, 3)
}
