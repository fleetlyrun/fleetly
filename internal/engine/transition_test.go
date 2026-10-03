package engine

// 四件一脊柱测试（transition.go，2026-10-03 收口批）：抑制规则、审计
// 强制（D4）、原子性、用户动词审计面——全部经类型化前门与 commitWrite
// 真实 seam 驱动（statetest 真库 + eventcode 注册表真实拒绝路径）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// auditActions 取审计动作列表（按资源前缀过滤；空前缀 = 全部）。
func auditActions(t *testing.T, e *Engine, resourcePrefix string) []string {
	t.Helper()
	rows, err := e.audits.List(context.Background(), e.db.Runner(), 500)
	require.NoError(t, err)
	var actions []string
	for _, r := range rows {
		if resourcePrefix == "" || strings.HasPrefix(r.Resource, resourcePrefix) {
			actions = append(actions, r.Action)
		}
	}
	return actions
}

// TestTaskTransitFourAuditsAndEmits：真实迁移四件一齐全——task.<state>
// 事件 + task.transit 审计（收口前该拷贝注释承诺审计、代码未落，D4 补齐）。
func TestTaskTransitFourAuditsAndEmits(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	tid := "01JD0TASK00000000000000T01"
	createTaskRow(t, e, tid, "", task.FormOneShot, 1, 0, "")
	row := getTaskRow(t, e, tid)

	require.NoError(t, e.transitTaskFour(ctx, row,
		[]task.State{task.StateActive}, task.StateDraining, nil))

	assert.Contains(t, eventNames(t, e, tid), "task.draining")
	assert.Contains(t, auditActions(t, e, "task/"+tid), "task.transit",
		"D4：一切状态迁移落审计")
}

// TestTransitionInPlaceSuppressesEventButAudits：原地迁移（active→active
// 仅改伴生字段）不发事件——事件是状态迁移的既成事实；审计照落。
func TestTransitionInPlaceSuppressesEventButAudits(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	tid := "01JD0TASK00000000000000T02"
	createTaskRow(t, e, tid, "pool", task.FormResident, 2, 0, "")
	row := getTaskRow(t, e, tid)

	require.NoError(t, e.transitTaskFour(ctx, row,
		[]task.State{task.StateActive}, task.StateActive,
		func(m *task.Task) { m.DesiredConcurrency = 3 }))

	assert.Empty(t, eventNames(t, e, tid), "原地迁移不发事件")
	assert.Contains(t, auditActions(t, e, "task/"+tid), "task.transit", "审计照落")
	assert.Equal(t, int64(3), getTaskRow(t, e, tid).DesiredConcurrency)
}

// TestScaleTaskKeepsUpdateEventAndAudits：task.updated 是"变化"事件而非
// 状态事件——原地迁移照发（updateEvent 面）；task.update 用户动词审计。
func TestScaleTaskKeepsUpdateEventAndAudits(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	tid := "01JD0TASK00000000000000T03"
	createTaskRow(t, e, tid, "pool", task.FormResident, 2, 0, "")

	row, err := e.ScaleTask(ctx, tid, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(3), row.DesiredConcurrency)

	assert.Contains(t, eventNames(t, e, tid), "task.updated")
	assert.Contains(t, auditActions(t, e, "task/"+tid), "task.update")
}

// TestStopTaskSingleDrainingEventPerMigration：排空迁移发一条 task.draining；
// 已 draining 的重复 Stop 无迁移不发事件（收口前该路径无迁移也重复发）。
func TestStopTaskSingleDrainingEventPerMigration(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	tid := "01JD0TASK00000000000000T04"
	createTaskRow(t, e, tid, "pool", task.FormResident, 2, 0, "")

	_, err := e.StopTask(ctx, tid, false)
	require.NoError(t, err)
	assert.Equal(t, 1, countEvent(eventNames(t, e, tid), "task.draining"))
	assert.Contains(t, auditActions(t, e, "task/"+tid), "task.stop")

	_, err = e.StopTask(ctx, tid, false) // 已 draining：无迁移
	require.NoError(t, err)
	assert.Equal(t, 1, countEvent(eventNames(t, e, tid), "task.draining"),
		"状态未变不是迁移——不重复发事件")
}

// TestRenewTaskRevivalAuditsAndEmits：复活迁移四件一（task.active 事件 +
// task.renew 审计）+ lease.renewed 事实事件。
func TestRenewTaskRevivalAuditsAndEmits(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	tid := "01JD0TASK00000000000000T05"
	createTaskRow(t, e, tid, "pool", task.FormResident, 2, 0, "")
	row := getTaskRow(t, e, tid)
	require.NoError(t, e.transitTaskFour(ctx, row,
		[]task.State{task.StateActive}, task.StateDraining, nil))
	require.NoError(t, e.transitTaskFour(ctx, row,
		[]task.State{task.StateDraining}, task.StateDrained, nil))

	fresh, err := e.RenewTask(ctx, tid)
	require.NoError(t, err)
	assert.Equal(t, task.StateActive, fresh.State)

	names := eventNames(t, e, tid)
	assert.Contains(t, names, "task.active")
	assert.Contains(t, names, "lease.renewed")
	assert.Contains(t, auditActions(t, e, "task/"+tid), "task.renew")
}

// TestDeleteTaskStopsRunsWithEvents：存量 Run 终态化走四件一——run.stopped
// 事件随迁移发出（收口前 DeleteTask 静默 CAS，事件订阅面看不见）。
func TestDeleteTaskStopsRunsWithEvents(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	tid := "01JD0TASK00000000000000T06"
	createTaskRow(t, e, tid, "migrate", task.FormOneShot, 1, 3600, "")
	e.taskStep(ctx) // 补足 1 条 pending Run
	runs := taskRuns(t, e, tid)
	require.Len(t, runs, 1)

	require.NoError(t, e.DeleteTask(ctx, tid))

	assert.Contains(t, eventNames(t, e, runs[0].ID), "run.stopped",
		"删除收口的存量 Run 终态化也是状态迁移")
	acts := auditActions(t, e, "")
	assert.Contains(t, acts, "task.delete")
	assert.Contains(t, acts, "run.transit")
}

// TestCommitWriteRollsBackOnEventFailure：事件段失败（未注册事件名被
// eventcode 注册表拒绝）→ 整单回滚，行不落。
func TestCommitWriteRollsBackOnEventFailure(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	tid := "01JD0TASK00000000000000T07"
	row := &task.Task{
		ID: tid, ProjectID: tProjectID, Form: task.FormOneShot,
		State: task.StateActive, Spec: []byte("{}"), DesiredConcurrency: 1,
		DNSName: TaskDNSName(tid),
	}
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.commitWrite(ctx, tx, writeFact{
			write: func(ctx context.Context, tx *sql.Tx) error { return e.tasks.Create(ctx, tx, row) },
			events: []func() eventFact{func() eventFact {
				return eventFact{name: "task.not-registered", aggregate: "task", id: tid}
			}},
		})
	})
	require.Error(t, err, "未注册事件名被注册表拒绝")
	_, gerr := e.tasks.Get(ctx, e.db.Runner(), tid)
	assert.Error(t, gerr, "事件失败整单回滚——行不落")
}

func countEvent(names []string, want string) int {
	n := 0
	for _, v := range names {
		if v == want {
			n++
		}
	}
	return n
}
