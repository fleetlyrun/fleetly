package engine

// 载体卫生清扫面测试（收尾批 E29）：终态收口次序门控（E29-2 根修）+ 终态
// Task 残留载体清扫 + 孤儿 Secret 载体清扫透传。手动驱动形态，假底座记录
// 断言（删除调用记录 + 现役/窗外不删）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// TestTaskOneShotTerminalWaitsForCarrierConvergence 钉 E29-2 收口次序：唯一
// Run 终态后空集 Ensure 失败 → Task 保持 active 留在驱动集（不镜像、不补
// 足——补足会铸造第二个 Run）；下一拍 Ensure 成功才镜像 completed。修复前
// 先镜像后 Ensure，失败窗口下 Task 已终态退出驱动集，run service 残留
// （swarm 0/1）无人重试。
func TestTaskOneShotTerminalWaitsForCarrierConvergence(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000002"
	createTaskRow(t, e, taskID, "", task.FormOneShot, 1, 0, "")
	e.taskStep(ctx)
	runs := taskRuns(t, e, taskID)
	require.Len(t, runs, 1)

	zero := 0
	observe(t, e, runs[0].ID, capability.WorkloadCompleted, &zero)

	rt.failNext = true
	e.taskStep(ctx) // 空集 Ensure 失败：Task 不得镜像
	assert.Equal(t, task.StateActive, getTaskRow(t, e, taskID).State,
		"failed carrier convergence must keep the task driving (retry next tick)")
	assert.Len(t, taskRuns(t, e, taskID), 1,
		"terminal-mirror wait must not replenish a second run")

	calls := rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Empty(t, last.Spec, "the convergence pass must ensure an empty desired set")
	assert.Equal(t, taskID, last.NS.Task)

	e.taskStep(ctx) // Ensure 成功：镜像落
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, taskID).State)
	assert.Len(t, taskRuns(t, e, taskID), 1, "mirror must not create runs")
}

// finishTaskForTest 把 Task 迁到指定终态（finished_at = 当前假钟时刻）。
func finishTaskForTest(t *testing.T, e *Engine, id string, to task.State) {
	t.Helper()
	require.NoError(t, e.tasks.Transit(context.Background(), e.db.Runner(), id,
		[]task.State{task.StateActive, task.StateDraining, task.StateCompleted, task.StateFailed, task.StateDrained},
		to, nil))
}

// TestSweepTerminalTaskCarriers 钉 E29-2 兜底面：窗内终态 Task 的隔离域被
// Remove（fake 记录断言）；窗外/deleted/项目行缺失不动。
func TestSweepTerminalTaskCarriers(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()

	// 旧收口（窗外）：先落，再推进时钟越过窗口。
	oldID := "01JD0TASK00000000000000010"
	createTaskRow(t, e, oldID, "", task.FormOneShot, 1, 0, "")
	finishTaskForTest(t, e, oldID, task.StateCompleted)
	clock.Advance(8 * 24 * time.Hour)

	// 新收口（窗内）：completed / failed / drained 各一；deleted 一（无残留
	// 面——DeleteTask 先收口后落账）；项目行缺失一（跳过不报错）。
	recentCompleted := "01JD0TASK00000000000000011"
	createTaskRow(t, e, recentCompleted, "", task.FormOneShot, 1, 0, "")
	finishTaskForTest(t, e, recentCompleted, task.StateCompleted)
	recentFailed := "01JD0TASK00000000000000012"
	createTaskRow(t, e, recentFailed, "", task.FormOneShot, 1, 0, "")
	finishTaskForTest(t, e, recentFailed, task.StateFailed)
	recentDrained := "01JD0TASK00000000000000013"
	createTaskRow(t, e, recentDrained, "idle", task.FormResident, 2, 0, "grp")
	require.NoError(t, e.tasks.Transit(ctx, e.db.Runner(), recentDrained,
		[]task.State{task.StateActive}, task.StateDraining, nil))
	finishTaskForTest(t, e, recentDrained, task.StateDrained)
	deletedRecent := "01JD0TASK00000000000000014"
	createTaskRow(t, e, deletedRecent, "", task.FormOneShot, 1, 0, "")
	finishTaskForTest(t, e, deletedRecent, task.StateDeleted)

	orphanProject := "01JD0TASK00000000000000015"
	require.NoError(t, e.tasks.Create(ctx, e.db.Runner(), &task.Task{
		ID: orphanProject, ProjectID: "01JPROJMISSING000000000000", Name: "ghost",
		Form: task.FormOneShot, State: task.StateActive,
		Spec: []byte(`{"schema_version":1,"task":{"id":"` + orphanProject + `"}`),
	}))
	finishTaskForTest(t, e, orphanProject, task.StateCompleted)

	n, err := e.SweepTerminalTaskCarriers(ctx, 7*24*time.Hour, 20)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "three in-window terminal tasks get their namespace removed")

	removed := rt.removedSnapshot()
	require.Len(t, removed, 3)
	want := map[string]bool{recentCompleted: true, recentFailed: true, recentDrained: true}
	for _, ns := range removed {
		assert.True(t, want[ns.Task], "unexpected removal for task %s", ns.Task)
		assert.Equal(t, "default", ns.Team)
		assert.Equal(t, tTaskProject, ns.Project)
	}
	for _, stale := range []string{oldID, deletedRecent, orphanProject} {
		for _, ns := range removed {
			assert.NotEqual(t, stale, ns.Task,
				"out-of-window/deleted/missing-project tasks must not be swept")
		}
	}
}

// TestSweepTerminalTaskCarriersDisabled 钉停用口径：window/limit 非正值 =
// 本拍 no-op（装配层策略旋钮）。
func TestSweepTerminalTaskCarriersDisabled(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	createTaskRow(t, e, "01JD0TASK00000000000000020", "", task.FormOneShot, 1, 0, "")
	finishTaskForTest(t, e, "01JD0TASK00000000000000020", task.StateCompleted)

	n, err := e.SweepTerminalTaskCarriers(context.Background(), 0, 20)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = e.SweepTerminalTaskCarriers(context.Background(), 7*24*time.Hour, 0)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, rt.removedSnapshot())
}

// hygieneRuntime 是带 RuntimeHygiene 子面的假底座（透传面断言；指针内嵌
// 避免复制含锁的 fakeRuntime 值）。
type hygieneRuntime struct {
	*fakeRuntime
	swept    int
	maxGiven int
}

func (h *hygieneRuntime) SweepOrphanSecrets(_ context.Context, maxDelete int) (int, error) {
	h.maxGiven = maxDelete
	h.swept++
	return 7, nil
}

// TestSweepOrphanSecretCarriersDelegates 钉透传面：实现 RuntimeHygiene 的
// Runtime 被调用且预算透传；未实现的 Runtime 静默跳过。
func TestSweepOrphanSecretCarriersDelegates(t *testing.T) {
	_, base, _ := newTestEngine(t)
	t.Run("runtime without the hygiene face skips silently", func(t *testing.T) {
		e := &Engine{runtime: base, log: discardLogger()}
		n, err := e.SweepOrphanSecretCarriers(context.Background(), 100)
		require.NoError(t, err)
		assert.Zero(t, n)
	})
	t.Run("runtime with the hygiene face passes the budget through", func(t *testing.T) {
		h := &hygieneRuntime{fakeRuntime: base}
		e := &Engine{runtime: h, log: discardLogger()}
		n, err := e.SweepOrphanSecretCarriers(context.Background(), 42)
		require.NoError(t, err)
		assert.Equal(t, 7, n)
		assert.Equal(t, 42, h.maxGiven)
	})
	t.Run("non-positive budget is a no-op", func(t *testing.T) {
		h := &hygieneRuntime{fakeRuntime: base}
		e := &Engine{runtime: h, log: discardLogger()}
		n, err := e.SweepOrphanSecretCarriers(context.Background(), 0)
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Zero(t, h.swept)
	})
}
