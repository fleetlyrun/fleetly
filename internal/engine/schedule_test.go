package engine

// Schedule 驱动测试（F1.7，ADR-0018）：到期拍铸 one-shot Task（F1.5 机制
// 复用）、错过窗口补跑一拍、重叠 skip、手动触发与重叠拒绝、tombstone 停拍。
// 手动驱动形态（scheduleStep/taskStep 直调 + 观测经 handleObservation 直注）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/schedule"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// createScheduleRow 落一条 Schedule 行（模板 spec 的 task ref 留空——fire
// 时铸新 Task；next_fire_at 由调用方显式给）。
func createScheduleRow(t *testing.T, e *Engine, id, name, cronExpr, tz, nextFireAt string) *schedule.Schedule {
	t.Helper()
	spec := `{"schema_version":1,"process":{"name":"run","image":"busybox:1.37","command":["/backup"]},` +
		`"ttl_seconds":3600,"desired_concurrency":1,"form":"one-shot"}`
	row := &schedule.Schedule{
		ID: id, ProjectID: tTaskProject, Name: name, State: schedule.StateActive,
		CronExpr: cronExpr, Timezone: tz, Spec: []byte(spec), NextFireAt: nextFireAt,
	}
	require.NoError(t, e.schedules.Create(context.Background(), e.db.Runner(), row))
	return row
}

func getScheduleRow(t *testing.T, e *Engine, id string) *schedule.Schedule {
	t.Helper()
	row, err := e.schedules.Get(context.Background(), e.db.Runner(), id)
	require.NoError(t, err)
	return row
}

// TestScheduleFireSpawnsTask：到期拍 → 铸 one-shot Task（name 空/系统属主/
// 模板字段继承）+ Fire CAS 推进 + schedule.fired；taskStep 补足 Run——
// F1.5 全链复用。
func TestScheduleFireSpawnsTask(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000000"
	// cron 每日 00:00 UTC；fake now = 2026-01-01T00:00:00Z 即刻到期。
	createScheduleRow(t, e, sid, "nightly-backup", "0 0 * * *", "UTC", "2026-01-01T00:00:00Z")

	e.scheduleStep(ctx)

	s := getScheduleRow(t, e, sid)
	assert.NotEmpty(t, s.LastTaskID, "fire must record the spawned task")
	assert.Equal(t, "2026-01-02T00:00:00Z", s.NextFireAt, "next fire advances one occurrence")

	spawned := getTaskRow(t, e, s.LastTaskID)
	assert.Equal(t, task.FormOneShot, spawned.Form)
	assert.Equal(t, task.StateActive, spawned.State)
	assert.Empty(t, spawned.Name, "fired tasks leave the project name space free")
	assert.Empty(t, spawned.OwnerTokenID, "fired tasks are system-owned (no lease)")
	assert.Equal(t, TaskDNSName(spawned.ID), spawned.DNSName)
	// 模板字段继承：TTL 与命令在冻结体里。
	ts := loadSpawnedSpec(t, e, spawned)
	assert.EqualValues(t, 3600, ts.GetTtlSeconds())
	assert.Equal(t, []string{"/backup"}, ts.GetProcess().GetCommand())
	assert.Equal(t, spawned.ID, ts.GetTask().GetId(), "fire stamps the task ref into the frozen template")

	assert.Contains(t, eventNames(t, e, sid), "schedule.fired")
	assert.Contains(t, eventNames(t, e, spawned.ID), "task.created")

	// F1.5 机制复用：补足 Run → 观测收口。
	e.taskStep(ctx)
	runs := taskRuns(t, e, spawned.ID)
	require.Len(t, runs, 1)
	zero := 0
	observe(t, e, runs[0].ID, capability.WorkloadCompleted, &zero)
	e.taskStep(ctx)
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, spawned.ID).State)
}

// TestScheduleNotDueNoFire：未到期不拍（行与事件都不动）。
func TestScheduleNotDueNoFire(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000001"
	createScheduleRow(t, e, sid, "", "0 0 * * *", "UTC", "2026-01-02T00:00:00Z")

	e.scheduleStep(ctx)

	s := getScheduleRow(t, e, sid)
	assert.Empty(t, s.LastTaskID)
	assert.Equal(t, "2026-01-02T00:00:00Z", s.NextFireAt)
	assert.Empty(t, eventNames(t, e, sid))
}

// TestScheduleMissedWindowCatchUpOnce（ADR-0018 附录 A）：停机跨过多个窗口
// 后恢复 → 补跑一拍（不多追），next 从当前时刻续算。
func TestScheduleMissedWindowCatchUpOnce(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000002"
	// cron 每分钟；next_fire_at = 00:00:00Z，恢复时已是 00:05:30Z（错过 5 拍）。
	createScheduleRow(t, e, sid, "", "* * * * *", "UTC", "2026-01-01T00:00:00Z")
	clock.Advance(5*time.Minute + 30*time.Second)

	e.scheduleStep(ctx)

	s := getScheduleRow(t, e, sid)
	assert.NotEmpty(t, s.LastTaskID, "missed window fires exactly one catch-up")
	assert.Equal(t, "2026-01-01T00:06:00Z", s.NextFireAt, "cadence resumes from now, no backlog replay")

	// 全库只有这一条 Task（补跑一次，不是五次）。
	tasks, err := e.tasks.ListDriving(ctx, e.db.Runner())
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

// TestScheduleOverlapSkips：上一拍 Run 未终态 → 到期拍跳过（事件 +
// 推进）；Run 终态后下一拍恢复。
func TestScheduleOverlapSkips(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000003"
	createScheduleRow(t, e, sid, "", "0 * * * *", "UTC", "2026-01-01T00:00:00Z")

	e.scheduleStep(ctx) // 第一拍
	first := getScheduleRow(t, e, sid).LastTaskID
	require.NotEmpty(t, first)
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, first), 1)

	// 下一拍到期，上一拍 Run 仍活 → skip。
	clock.Advance(time.Hour)
	e.scheduleStep(ctx)

	s := getScheduleRow(t, e, sid)
	assert.Equal(t, first, s.LastTaskID, "overlapped fire must not spawn")
	assert.Equal(t, "2026-01-01T02:00:00Z", s.NextFireAt)
	assert.Contains(t, eventNames(t, e, sid), "schedule.skipped")

	// Run 终态 → 下一拍恢复触发。
	zero := 0
	for _, r := range taskRuns(t, e, first) {
		observe(t, e, r.ID, capability.WorkloadCompleted, &zero)
	}
	e.taskStep(ctx) // Task 镜像 completed
	clock.Advance(time.Hour)
	e.scheduleStep(ctx)

	s = getScheduleRow(t, e, sid)
	assert.NotEqual(t, first, s.LastTaskID, "next occurrence fires after the previous run finished")
	assert.Contains(t, eventNames(t, e, s.LastTaskID), "task.created")
}

// TestTriggerScheduleManual：手动触发立即铸 Task；next_fire_at 不动（cron
// 节奏不被打乱）。
func TestTriggerScheduleManual(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000004"
	createScheduleRow(t, e, sid, "oncall", "0 0 * * *", "UTC", "2026-01-02T00:00:00Z")

	s, err := e.TriggerSchedule(ctx, sid)
	require.NoError(t, err)
	assert.NotEmpty(t, s.LastTaskID)
	assert.Equal(t, "2026-01-02T00:00:00Z", s.NextFireAt, "manual fire must not shift the cron cadence")
	assert.Contains(t, eventNames(t, e, sid), "schedule.fired")

	// 手动拍的 Task 进驱动链。
	e.taskStep(ctx)
	assert.Len(t, taskRuns(t, e, s.LastTaskID), 1)
}

// TestTriggerScheduleOverlapRejected：上一拍 Run 在途时手动触发 → 诚实拒绝。
func TestTriggerScheduleOverlapRejected(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000005"
	createScheduleRow(t, e, sid, "", "0 0 * * *", "UTC", "2026-01-01T00:00:00Z")

	e.scheduleStep(ctx)
	e.taskStep(ctx) // Run 已落（pending 即活槽位）

	_, err := e.TriggerSchedule(ctx, sid)
	assert.ErrorIs(t, err, ErrScheduleOverlapping)
}

// TestScheduleOverlapPolicyFires（ADR-0017 附录 A.4）：fire 策略下重叠时
// 照常拍（允许并行拍）；手动触发在重叠下恒诚实拒绝（旋钮只改到期拍）。
func TestScheduleOverlapPolicyFires(t *testing.T) {
	e, _, clock := newTestEngineOpts(t, Options{ScheduleOverlap: ScheduleOverlapFire})
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000008"
	createScheduleRow(t, e, sid, "", "0 * * * *", "UTC", "2026-01-01T00:00:00Z")

	e.scheduleStep(ctx) // 第一拍
	first := getScheduleRow(t, e, sid).LastTaskID
	require.NotEmpty(t, first)
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, first), 1)

	// 下一拍到期，上一拍 Run 仍活 → fire 策略照常拍（并行拍）。
	clock.Advance(time.Hour)
	e.scheduleStep(ctx)

	s := getScheduleRow(t, e, sid)
	assert.NotEqual(t, first, s.LastTaskID, "fire policy must spawn despite the overlap")
	assert.Equal(t, "2026-01-01T02:00:00Z", s.NextFireAt)
	assert.NotContains(t, eventNames(t, e, sid), "schedule.skipped")

	// 手动触发在重叠下恒拒绝（显式动作给显式反馈）——重叠真源是上一拍
	// Task 的未终态 Run，先驱动第二拍补足 Run 再断言。
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, s.LastTaskID), 1)
	_, err := e.TriggerSchedule(ctx, sid)
	assert.ErrorIs(t, err, ErrScheduleOverlapping)
}

// TestScheduleQuotaSkips（ADR-0017 附录 A.1）：项目配额满时到期拍 → skip
// （事件 + 节奏照常推进，不铸 Task）；手动拍诚实拒绝。
func TestScheduleQuotaSkips(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000007"
	createScheduleRow(t, e, sid, "", "0 * * * *", "UTC", "2026-01-01T00:00:00Z")
	// 同项目灌满并发总量（desired=200 的一条 resident）。
	createTaskRow(t, e, "01JD0TASK0000000000000000Q", "filler", task.FormResident, MaxTaskConcurrencyPerProject, 0, "")

	e.scheduleStep(ctx) // 到期拍 → 配额满 → skip

	s := getScheduleRow(t, e, sid)
	assert.Empty(t, s.LastTaskID, "quota-full fire must not spawn")
	assert.Equal(t, "2026-01-01T01:00:00Z", s.NextFireAt, "cadence advances on quota skip")
	assert.Contains(t, eventNames(t, e, sid), "schedule.skipped")

	// 手动拍 → 诚实拒绝（API 面 E_QUOTA_EXCEEDED）。
	_, err := e.TriggerSchedule(ctx, sid)
	assert.ErrorIs(t, err, ErrTaskQuota)
}

// TestParseScheduleOverlap：值域 fail-fast（空值回退 skip，非法值报错）。
func TestParseScheduleOverlap(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", ScheduleOverlapSkip, false},
		{"skip", ScheduleOverlapSkip, false},
		{"fire", ScheduleOverlapFire, false},
		{"queue", "", true},
		{"SKIP", "", true},
	} {
		got, err := ParseScheduleOverlap(tc.in)
		if tc.wantErr {
			assert.Error(t, err, "input %q", tc.in)
			continue
		}
		assert.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
}

// TestScheduleTerminalNoFire：tombstone 行不再拍（ListDriving 不拾取）；
// 已铸 Task 不受删除影响（跑完自然收口——DeleteSchedule 语义）。
func TestScheduleTerminalNoFire(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	sid := "01JD0SCHD00000000000000006"
	createScheduleRow(t, e, sid, "", "0 * * * *", "UTC", "2026-01-01T00:00:00Z")
	e.scheduleStep(ctx)
	spawned := getScheduleRow(t, e, sid).LastTaskID
	require.NotEmpty(t, spawned)

	require.NoError(t, e.schedules.Transit(ctx, e.db.Runner(), sid, schedule.StateActive, schedule.StateDeleted))
	clock.Advance(2 * time.Hour)
	e.scheduleStep(ctx)

	s := getScheduleRow(t, e, sid)
	assert.Equal(t, spawned, s.LastTaskID, "tombstoned schedule must not fire")
	assert.Equal(t, schedule.StateDeleted, s.State)

	// 已铸 Task 照常驱动（删除不停在途执行）。
	e.taskStep(ctx)
	assert.Len(t, taskRuns(t, e, spawned), 1)
}

// loadSpawnedSpec 反序列化铸出 Task 的冻结体。
func loadSpawnedSpec(t *testing.T, e *Engine, row *task.Task) *specv1.TaskSpec {
	t.Helper()
	spec, err := loadTaskSpec(row.Spec)
	require.NoError(t, err)
	return spec
}
