package run_test

// Run 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create 落行与
// ID/seq 铸造、ListByTask 新→旧 + after 游标 + limit 钳位（<=0 或 >200 回
// 落 50）、ListByProject 项目隔离、Transit CAS 状态机四态（pending →
// running → stopping → stopped|failed）：前置不符 ErrConflict 文案含
// want-one-of、Running 回填 StartedAt、终态回填 FinishedAt、mut 落
// stop_reason/exit_code/deadline、终态不可覆写（Terminal 后再 Transit 即
// Conflict）、缺行 ErrNotFound；ListDrivingOfTerminalTasks 僵尸收口与
// task.TerminalStateStrings 词表口径咬合（对照 domain 契约，不硬抄词表）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainrun "github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	runrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/run"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

const (
	projX = "01JD0PROJ0000000000000000X"
	projY = "01JD0PROJ0000000000000000Y"
)

// insertTask 落一行 Task 夹具（runs 行无外键约束，但僵尸收口子查询扫
// tasks 表——真实 task 行才能驱动 ListDrivingOfTerminalTasks 的 IN 词表）。
func insertTask(t *testing.T, ctx context.Context, db *persistence.DB, id string, seq int64, st task.State) {
	t.Helper()
	_, err := db.Runner().ExecContext(ctx, `
		INSERT INTO tasks (id, seq, project_id, name, form, state, spec, created_at, updated_at)
		VALUES (?, ?, ?, '', 'one-shot', ?, x'7b7d', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, seq, projX, string(st))
	require.NoError(t, err)
}

// TestRunCreateAndListByTask：Create 铸造面 + ListByTask 游标/钳位/隔离 +
// ListByProject 冗余 project_id 轴。
func TestRunCreateAndListByTask(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	runs := runrepo.New(clock)

	// 同 Task 落三行（ID 缺省由 task-r<seq> 派生）+ 异项目一行。
	mk := func(taskID, projectID string) *domainrun.Run {
		m := &domainrun.Run{TaskID: taskID, ProjectID: projectID, State: domainrun.StatePending}
		require.NoError(t, runs.Create(ctx, db.Runner(), m))
		return m
	}
	r1 := mk("task-a", projX)
	r2 := mk("task-a", projX)
	r3 := mk("task-a", projX)
	mk("task-b", projY)

	assert.Equal(t, "task-a-r1", r1.ID)
	assert.Equal(t, domainrun.StatePending, r1.State)
	assert.Empty(t, r1.StartedAt)
	assert.Empty(t, r1.FinishedAt)
	assert.Nil(t, r1.ExitCode)
	assert.Equal(t, state.FormatTime(clock.Now()), r1.CreatedAt)
	assert.Equal(t, r1.CreatedAt, r1.UpdatedAt)

	// 新→旧：seq DESC。
	page, err := runs.ListByTask(ctx, db.Runner(), "task-a", 0, 50)
	require.NoError(t, err)
	require.Len(t, page, 3)
	assert.Equal(t, r3.ID, page[0].ID)
	assert.Equal(t, r1.ID, page[2].ID)

	// after 游标：只回 seq 严格小于游标的行。
	page, err = runs.ListByTask(ctx, db.Runner(), "task-a", r2.Seq, 50)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, r1.ID, page[0].ID)

	// 游标越过最旧行 → 空页。
	page, err = runs.ListByTask(ctx, db.Runner(), "task-a", 1, 50)
	require.NoError(t, err)
	assert.Empty(t, page)

	// limit 钳位：<=0 或 >200 回落缺省 50（三行全回，小夹具不截断）。
	for _, limit := range []int{0, -7, 201} {
		page, err = runs.ListByTask(ctx, db.Runner(), "task-a", 0, limit)
		require.NoError(t, err)
		assert.Len(t, page, 3, "limit %d clamps into range and returns all rows", limit)
	}

	// ListByProject：runs 行自带冗余 project_id，项目隔离不二跳。
	page, err = runs.ListByProject(ctx, db.Runner(), projY, 0, 50)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "task-b-r4", page[0].ID)
}

// TestRunTransitCASStateMachine：ADR-0012 四态链 + CAS 前置/终态口径。
func TestRunTransitCASStateMachine(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	runs := runrepo.New(clock)

	m := &domainrun.Run{TaskID: "task-a", ProjectID: projX, State: domainrun.StatePending}
	require.NoError(t, runs.Create(ctx, db.Runner(), m))

	// 缺行 → ErrNotFound。
	err := runs.Transit(ctx, db.Runner(), "missing-id", domainrun.ActiveStates, domainrun.StateRunning, nil)
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 前置不符 → ErrConflict，文案含 want one of（CAS 报错的消费面锚）。
	err = runs.Transit(ctx, db.Runner(), m.ID, []domainrun.State{domainrun.StateStopping}, domainrun.StateRunning, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrConflict)
	assert.Contains(t, err.Error(), "want one of")
	assert.Contains(t, err.Error(), "stopping", "conflict text must carry the from-state set")
	assert.Contains(t, err.Error(), "pending", "conflict text must carry the current state")

	// pending → running：StartedAt 回填（UpdatedAt 同时刻），FinishedAt 仍空。
	clock.Advance(10e9)
	require.NoError(t, runs.Transit(ctx, db.Runner(), m.ID, domainrun.ActiveStates, domainrun.StateRunning, nil))
	got, err := runs.Get(ctx, db.Runner(), m.ID)
	require.NoError(t, err)
	assert.Equal(t, domainrun.StateRunning, got.State)
	assert.Equal(t, state.FormatTime(clock.Now()), got.StartedAt)
	assert.Empty(t, got.FinishedAt)

	// running → stopping：mut 落停止起因 + 收口兜底 deadline（ADR-0018 覆写面）。
	clock.Advance(10e9)
	cutoff := "2026-01-01T00:05:00Z"
	require.NoError(t, runs.Transit(ctx, db.Runner(), m.ID, []domainrun.State{domainrun.StateRunning},
		domainrun.StateStopping, func(cur *domainrun.Run) {
			cur.StopReason = domainrun.ReasonStoppedByUser
			cur.Deadline = cutoff
		}))
	got, err = runs.Get(ctx, db.Runner(), m.ID)
	require.NoError(t, err)
	assert.Equal(t, domainrun.StateStopping, got.State)
	assert.Equal(t, domainrun.ReasonStoppedByUser, got.StopReason)
	assert.Equal(t, cutoff, got.Deadline)

	// stopping → stopped：终态回填 FinishedAt，mut 落退出码（NULL → 值的扫描面）。
	clock.Advance(10e9)
	require.NoError(t, runs.Transit(ctx, db.Runner(), m.ID, []domainrun.State{domainrun.StateStopping},
		domainrun.StateStopped, func(cur *domainrun.Run) {
			code := 143
			cur.ExitCode = &code
		}))
	got, err = runs.Get(ctx, db.Runner(), m.ID)
	require.NoError(t, err)
	assert.Equal(t, domainrun.StateStopped, got.State)
	assert.Equal(t, state.FormatTime(clock.Now()), got.FinishedAt)
	assert.Equal(t, got.FinishedAt, got.UpdatedAt)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 143, *got.ExitCode)
	finishedAt := got.FinishedAt

	// 终态不可覆写：Terminal 后再 Transit 即 Conflict，行面冻结。
	err = runs.Transit(ctx, db.Runner(), m.ID, domainrun.ActiveStates, domainrun.StateFailed, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrConflict, "a terminal run must refuse further transitions")
	got, err = runs.Get(ctx, db.Runner(), m.ID)
	require.NoError(t, err)
	assert.Equal(t, domainrun.StateStopped, got.State)
	assert.Equal(t, finishedAt, got.FinishedAt, "terminal finished_at must not be overwritten")

	// 第二条腿：ActiveStates 直达 stopping（过量排空形态）→ failed 终态
	// （失败起因）也回填 FinishedAt；exit_code 缺省保持 nil（未观测）。
	n := &domainrun.Run{TaskID: "task-a", ProjectID: projX, State: domainrun.StatePending}
	require.NoError(t, runs.Create(ctx, db.Runner(), n))
	require.NoError(t, runs.Transit(ctx, db.Runner(), n.ID, domainrun.ActiveStates, domainrun.StateStopping,
		func(cur *domainrun.Run) { cur.StopReason = domainrun.ReasonPlatformDrained }))
	require.NoError(t, runs.Transit(ctx, db.Runner(), n.ID, []domainrun.State{domainrun.StateStopping},
		domainrun.StateFailed, func(cur *domainrun.Run) { cur.StopReason = domainrun.ReasonFailed }))
	got, err = runs.Get(ctx, db.Runner(), n.ID)
	require.NoError(t, err)
	assert.Equal(t, domainrun.StateFailed, got.State)
	assert.Equal(t, domainrun.ReasonFailed, got.StopReason)
	assert.NotEmpty(t, got.FinishedAt)
	assert.Empty(t, got.StartedAt, "pending→stopping direct path never observed running")
	assert.Nil(t, got.ExitCode)
}

// TestRunDrivingReapMatchesTaskTerminalWords：僵尸收口词表与 domain 契约
// 咬合——TerminalStateStrings 的 SQL IN 词表恰覆盖 task 全部终态、不含任
// 何非终态；六个状态各落一行 driving Run，收口面只捞终态 Task 名下的四行。
func TestRunDrivingReapMatchesTaskTerminalWords(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	runs := runrepo.New(clock)

	// 全六态 Task 各一行 + 名下一行 pending Run。
	allStates := []task.State{
		task.StateActive, task.StateDraining, // 非终态
		task.StateCompleted, task.StateFailed, task.StateDrained, task.StateDeleted, // 终态
	}
	for i, st := range allStates {
		id := "task-" + string(st)
		insertTask(t, ctx, db, id, int64(i+1), st)
		require.NoError(t, runs.Create(ctx, db.Runner(), &domainrun.Run{
			TaskID: id, ProjectID: projX, State: domainrun.StatePending,
		}))
	}

	// 词表对照 domain 契约：TerminalStateStrings 与 TerminalStates/Terminal()
	// 三面一致——词表腐化在此显形。
	words := task.TerminalStateStrings()
	require.Len(t, words, 4)
	for _, st := range allStates {
		inWords := false
		for _, w := range words {
			if w == string(st) {
				inWords = true
				break
			}
		}
		assert.Equal(t, st.Terminal(), inWords, "state %q: SQL word list membership must match Terminal()", st)
	}

	// driving 集 = pending/running/stopping 全量六行（词表无关）。
	driving, err := runs.ListDriving(ctx, db.Runner())
	require.NoError(t, err)
	assert.Len(t, driving, 6)

	// 收口面只捞终态 Task 名下 driving 行。
	reap, err := runs.ListDrivingOfTerminalTasks(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, reap, 4)
	for _, m := range reap {
		st := task.State("unknown")
		for _, cand := range allStates {
			if m.TaskID == "task-"+string(cand) {
				st = cand
				break
			}
		}
		assert.True(t, st.Terminal(), "reaped run %s belongs to terminal task %s", m.ID, m.TaskID)
	}
}
