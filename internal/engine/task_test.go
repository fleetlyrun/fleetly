package engine

// Task/Run 驱动测试（F1.5/F1.6，ADR-0012/0025）：one-shot 生命周期、resident
// 池补足、Owner Lease 排空与复活、TTL janitor、属主吊销拉式排空、删除收口。
// 手动驱动形态（taskStep 直调 + 观测经 handleObservation 直注）。

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/role"
	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/task"
	"github.com/fleetlyrun/fleetly/internal/state/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
)

const (
	tTaskProject = "01JD0PROJ00000000000000000"
)

// createTaskRow 落一条 Task 行（spec 内 task.id 以行 ID 填充后冻结）。
func createTaskRow(t *testing.T, e *Engine, id, name, form string, desired int64, ttl int64, group string) *task.Task {
	t.Helper()
	spec := fmt.Sprintf(`{"schema_version":1,"task":{"id":"%s","project":"%s"},`+
		`"process":{"name":"run","image":"busybox:1.37","command":["/worker"]},`+
		`"ttl_seconds":%d,"desired_concurrency":%d,"form":"%s"`, id, tTaskProject, ttl, desired, form)
	if group != "" {
		spec += fmt.Sprintf(`,"network_group":"%s"`, group)
	}
	row := &task.Task{
		ID: id, ProjectID: tTaskProject, Name: name, Form: form,
		State: task.StateActive, Spec: []byte(spec + "}"),
		DesiredConcurrency: desired, DNSName: TaskDNSName(id),
	}
	require.NoError(t, e.tasks.Create(context.Background(), e.db.Runner(), row))
	return row
}

func getTaskRow(t *testing.T, e *Engine, id string) *task.Task {
	t.Helper()
	row, err := e.tasks.Get(context.Background(), e.db.Runner(), id)
	require.NoError(t, err)
	return row
}

func getRunRow(t *testing.T, e *Engine, id string) *run.Run {
	t.Helper()
	row, err := e.runs.Get(context.Background(), e.db.Runner(), id)
	require.NoError(t, err)
	return row
}

func taskRuns(t *testing.T, e *Engine, taskID string) []run.Run {
	t.Helper()
	rows, err := e.runs.ListByTaskStates(context.Background(), e.db.Runner(), taskID,
		[]run.State{run.StatePending, run.StateRunning, run.StateStopping, run.StateStopped, run.StateFailed})
	require.NoError(t, err)
	return rows
}

// observe 注入一条 Run 观测（经 handleObservation 走真实裁决路由）。
func observe(t *testing.T, e *Engine, workloadID string, st capability.WorkloadState, exitCode *int) {
	t.Helper()
	e.handleObservation(context.Background(), capability.WorkloadEvent{
		WorkloadID: workloadID, Generation: capability.Generation(1),
		State: st, ExitCode: exitCode,
	})
}

// TestTaskOneShotLifecycle：one-shot 全链——补足 1 Run → Ensure（never +
// 双级 DNS + 网络组）→ running → completed（exit 0）→ Task 镜像 completed →
// 空集收口残留载体。
func TestTaskOneShotLifecycle(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000000"
	createTaskRow(t, e, taskID, "migrate", task.FormOneShot, 1, 3600, "dispatcher")

	e.taskStep(ctx)

	runs := taskRuns(t, e, taskID)
	require.Len(t, runs, 1, "one-shot replenishes exactly one run")
	assert.Equal(t, run.StatePending, runs[0].State)
	assert.Equal(t, RunDNSName(runs[0].ID), runs[0].DNSName)

	calls := rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tTaskProject, Task: taskID}, last.NS)
	require.Len(t, last.Spec, 1)
	w := last.Spec["run"]
	assert.Equal(t, runs[0].ID, w.ID)
	assert.Equal(t, capability.RestartNever, w.Restart)
	assert.Equal(t, int64(1), w.Replicas)
	assert.Equal(t, []string{"taskgrp-dispatcher"}, w.Networks)
	assert.Len(t, w.Addressing, 2)

	// 观测裁决：running。
	observe(t, e, runs[0].ID, capability.WorkloadRunning, nil)
	assert.Equal(t, run.StateRunning, getRunRow(t, e, runs[0].ID).State)

	// 自然完成（exit 0）：stopped/completed + Task 镜像。
	zero := 0
	observe(t, e, runs[0].ID, capability.WorkloadCompleted, &zero)
	got := getRunRow(t, e, runs[0].ID)
	assert.Equal(t, run.StateStopped, got.State)
	assert.Equal(t, run.ReasonCompleted, got.StopReason)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 0, *got.ExitCode)

	e.taskStep(ctx) // 终态镜像 + 空集收口
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, taskID).State)
	// 事件链咬合：run.created → run.running → run.stopped；task.completed。
	assert.Equal(t, []string{"run.created", "run.running", "run.stopped"},
		eventNames(t, e, runs[0].ID))
	assert.Contains(t, eventNames(t, e, taskID), "task.completed")
	// 观测缓存回收（P1-7）。
	_, cached := e.runObservation(runs[0].ID)
	assert.False(t, cached, "terminal run observation must be recycled")
}

// TestTaskOneShotFailedTerminal：exit 非 0 → failed/failed；Task 镜像 failed。
func TestTaskOneShotFailedTerminal(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000001"
	createTaskRow(t, e, taskID, "", task.FormOneShot, 1, 0, "")
	e.taskStep(ctx)
	runs := taskRuns(t, e, taskID)
	require.Len(t, runs, 1)

	one := 1
	observe(t, e, runs[0].ID, capability.WorkloadFailed, &one)
	got := getRunRow(t, e, runs[0].ID)
	assert.Equal(t, run.StateFailed, got.State)
	assert.Equal(t, run.ReasonFailed, got.StopReason)

	e.taskStep(ctx)
	assert.Equal(t, task.StateFailed, getTaskRow(t, e, taskID).State)
}

// TestTaskResidentPoolReplenish：desired=3 补足 3 Run；一 Run 失败 → 下一拍
// 补足回 3（池是韧性单元，单 Run never 不重启）。
func TestTaskResidentPoolReplenish(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000002"
	createTaskRow(t, e, taskID, "dispatcher", task.FormResident, 3, 0, "dispatcher")

	e.taskStep(ctx)
	assert.Len(t, taskRuns(t, e, taskID), 3, "pool replenishes to desired concurrency")

	// 一个 Run 失败（exit 1）。
	runs := taskRuns(t, e, taskID)
	one := 1
	observe(t, e, runs[0].ID, capability.WorkloadFailed, &one)
	assert.Equal(t, run.StateFailed, getRunRow(t, e, runs[0].ID).State)

	e.taskStep(ctx) // 补足：3 活槽位
	live := 0
	for _, r := range taskRuns(t, e, taskID) {
		if r.State.Active() {
			live++
		}
	}
	assert.Equal(t, 3, live, "failed slot is replenished")
	assert.Equal(t, task.StateActive, getTaskRow(t, e, taskID).State)
}

// TestTaskResidentPoolScaleDownDrains（staging 真机实证缺口，2026-10-02）：
// ScaleTask 缩容（desired 4→2）→ drive 排空过量（stopping/platform_drained，
// 停新保老）→ 存量收敛到 2。方向断言：被停的必须是最新两条（未预热），
// 存活的必须是最老两条（长者已预热——注释与 commit 8d397d4 的声明口径）。
func TestTaskResidentPoolScaleDownDrains(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000009"
	createTaskRow(t, e, taskID, "dispatcher", task.FormResident, 4, 0, "dispatcher")

	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, taskID), 4, "pool replenishes to 4")

	_, err := e.ScaleTask(ctx, taskID, 2)
	require.NoError(t, err)

	e.taskStep(ctx) // 过量排空：4 活 → 2 停 + 2 活
	all := taskRuns(t, e, taskID)
	var drainedIDs, liveIDs, allIDs []string
	for _, r := range all {
		allIDs = append(allIDs, r.ID)
		switch {
		case r.State == run.StateStopping:
			assert.Equal(t, run.ReasonPlatformDrained, r.StopReason, "scale-down drain stamps platform_drained")
			drainedIDs = append(drainedIDs, r.ID)
		case r.State.Active():
			liveIDs = append(liveIDs, r.ID)
		}
	}
	require.Len(t, drainedIDs, 2, "excess runs drain")
	require.Len(t, liveIDs, 2, "warm runs survive scale-down")
	assert.Equal(t, task.StateActive, getTaskRow(t, e, taskID).State, "scale-down keeps the pool active")

	// 方向（ULID 单调：ID 升序 = 铸造序，小 = 老）：被排空的是最新两条，
	// 存活的是最老两条（两侧排序后按 ID 比较，不依赖列表返回序）。
	sort.Strings(allIDs)
	sort.Strings(drainedIDs)
	sort.Strings(liveIDs)
	assert.Equal(t, []string{allIDs[2], allIDs[3]}, drainedIDs, "the two newest runs are drained")
	assert.Equal(t, []string{allIDs[0], allIDs[1]}, liveIDs, "the two oldest runs survive")
}

// TestDrainingTaskReStopsOrphanRuns（P1 修复 2026-10-02）：强停后人为复活
// 一条 Run（漏停行事实形态：停止单条失败只记日志/旧代码不持锁与补足竞态
// 漏停新铸 Run）→ 下一拍排空补停（复用行上排空起因）→ 观测收口 → Task
// 收敛 drained，不再永久卡 draining。
func TestDrainingTaskReStopsOrphanRuns(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK0000000000000000D"
	createTaskRow(t, e, taskID, "pool", task.FormResident, 2, 0, "")
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, taskID), 2)
	runs := taskRuns(t, e, taskID) // 新→旧
	older, newer := runs[1].ID, runs[0].ID

	// 强停：两条 Run 均 stopping/stopped_by_user。
	_, err := e.StopTask(ctx, taskID, true)
	require.NoError(t, err)
	require.Equal(t, task.StateDraining, getTaskRow(t, e, taskID).State)

	// 人为复活一条（state 回 running、deadline 清零回活跃态；行上残留的
	// stop_reason 保持——正是竞态漏停行的事实形态）。
	_, err = e.db.Runner().ExecContext(ctx,
		`UPDATE runs SET state = 'running', deadline = '' WHERE id = ?`, newer)
	require.NoError(t, err)
	require.Equal(t, run.StateRunning, getRunRow(t, e, newer).State)

	e.taskStep(ctx) // 排空补停：stopping 兄弟 = 宽限排空已发起 → 补停复活 Run
	got := getRunRow(t, e, newer)
	assert.Equal(t, run.StateStopping, got.State, "the orphan run is re-stopped by the draining task")
	assert.Equal(t, run.ReasonStoppedByUser, got.StopReason, "re-stop reuses the drain's stamped reason")

	// 观测收口：全终态 → Task 收敛 drained。
	for _, id := range []string{older, newer} {
		observe(t, e, id, capability.WorkloadStopped, nil)
	}
	e.taskStep(ctx)
	assert.Equal(t, task.StateDrained, getTaskRow(t, e, taskID).State, "the draining task converges")
}

// TestStopTaskSoftKeepsNaturalCompletion：force=false 的自然收口语义不被
// 排空补停越权——无 stopping 行 = 宽限排空未发起（属主吊销跑完 TTL 模式
// 同一豁免面），存量 Run 续跑到完成/TTL。
func TestStopTaskSoftKeepsNaturalCompletion(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK0000000000000000E"
	createTaskRow(t, e, taskID, "pool", task.FormResident, 1, 0, "")
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, taskID), 1)
	runID := taskRuns(t, e, taskID)[0].ID
	observe(t, e, runID, capability.WorkloadRunning, nil)

	_, err := e.StopTask(ctx, taskID, false)
	require.NoError(t, err)
	e.taskStep(ctx)

	assert.Equal(t, task.StateDraining, getTaskRow(t, e, taskID).State)
	got := getRunRow(t, e, runID)
	assert.Equal(t, run.StateRunning, got.State, "soft stop leaves the run to natural completion")
	assert.Empty(t, got.StopReason)
}

// TestStopTaskConcurrentWithDrive：强停与驱动环并发（锁纪律回归形态）：
// 并发拍结束后观测收口，Task 必须收敛 drained 且无活跃 Run 残留——旧代
// 码 StopTask 不持 Task 互斥，强停可与补足竞态漏停新铸 Run（悬挂形态；
// 锁纪律 + 排空补停双保险下的等价收敛验证）。
func TestStopTaskConcurrentWithDrive(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK0000000000000000F"
	createTaskRow(t, e, taskID, "pool", task.FormResident, 2, 0, "")
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, taskID), 2)

	driving := make(chan struct{})
	go func() {
		defer close(driving)
		for i := 0; i < 20; i++ {
			e.taskStep(ctx)
		}
	}()
	_, err := e.StopTask(ctx, taskID, true)
	require.NoError(t, err)
	<-driving

	for _, r := range taskRuns(t, e, taskID) {
		observe(t, e, r.ID, capability.WorkloadStopped, nil)
	}
	for i := 0; i < 5 && getTaskRow(t, e, taskID).State != task.StateDrained; i++ {
		e.taskStep(ctx)
	}
	assert.Equal(t, task.StateDrained, getTaskRow(t, e, taskID).State)
	for _, r := range taskRuns(t, e, taskID) {
		assert.True(t, r.State.Terminal(), "no active run may outlive a forced stop: %s %s", r.ID, r.State)
	}
}

// TestTaskLeaseExpiryDrainAndRevive（F1.6）：lease 超宽限 → 排空（runs
// stopping/lease_expired）→ drained → RenewTask 复活 → 补足恢复。
func TestTaskLeaseExpiryDrainAndRevive(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000003"
	createTaskRow(t, e, taskID, "pool", task.FormResident, 2, 0, "")

	e.taskStep(ctx)
	assert.Len(t, taskRuns(t, e, taskID), 2)

	// 续期一次：deadline = now + interval。
	renewed, err := e.RenewTask(ctx, taskID)
	require.NoError(t, err)
	assert.Contains(t, eventNames(t, e, taskID), "lease.renewed")
	require.NotEmpty(t, renewed.LeaseDeadline)

	// 时钟推过 deadline + 宽限 → 排空（deadline = 续期时刻 + interval）。
	clock.Advance(e.opts.TaskLeaseInterval + e.opts.TaskLeaseGrace + time.Second)

	e.taskStep(ctx)
	assert.Equal(t, task.StateDraining, getTaskRow(t, e, taskID).State)
	for _, r := range taskRuns(t, e, taskID) {
		assert.Equal(t, run.StateStopping, r.State)
		assert.Equal(t, run.ReasonLeaseExpired, r.StopReason)
	}
	assert.Contains(t, eventNames(t, e, taskID), "lease.expired")
	assert.Contains(t, eventNames(t, e, taskID), "task.draining")

	// 观测 stopped → 全终态 → drained。
	for _, r := range taskRuns(t, e, taskID) {
		observe(t, e, r.ID, capability.WorkloadStopped, nil)
	}
	e.taskStep(ctx)
	assert.Equal(t, task.StateDrained, getTaskRow(t, e, taskID).State)
	assert.Contains(t, eventNames(t, e, taskID), "task.drained")

	// 复活（"排空并补足"的补足半边）：RenewTask → active + 补足。
	_, err = e.RenewTask(ctx, taskID)
	require.NoError(t, err)
	e.taskStep(ctx)
	assert.Equal(t, task.StateActive, getTaskRow(t, e, taskID).State)
	assert.Contains(t, eventNames(t, e, taskID), "task.active")
	live := 0
	for _, r := range taskRuns(t, e, taskID) {
		if r.State.Active() {
			live++
		}
	}
	assert.Equal(t, 2, live, "pool replenishes after revival")
}

// TestTaskTTLLifecycle（ADR-0018 墙钟）：TTL deadline 过 → stopping/
// ttl_expired；停止兜底 deadline 过 → stopped（观测缺位不悬挂）。
func TestTaskTTLLifecycle(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000004"
	createTaskRow(t, e, taskID, "", task.FormOneShot, 1, 60, "")
	e.taskStep(ctx)
	runs := taskRuns(t, e, taskID)
	require.Len(t, runs, 1)

	// 时钟推过 TTL deadline（60s）。
	clock.Advance(61 * time.Second)
	e.taskStep(ctx)
	got := getRunRow(t, e, runs[0].ID)
	assert.Equal(t, run.StateStopping, got.State)
	assert.Equal(t, run.ReasonTTLExpired, got.StopReason)

	// 观测确认停止。
	observe(t, e, runs[0].ID, capability.WorkloadStopped, nil)
	got = getRunRow(t, e, runs[0].ID)
	assert.Equal(t, run.StateStopped, got.State)
	assert.Equal(t, run.ReasonTTLExpired, got.StopReason)

	// 停止兜底路径（观测缺位）：推过停止兜底 deadline → stopped → Task
	// 镜像 completed。修复后语义：stopping 也是 one-shot 占位——全链只有
	// 这一条 Run，不补第二 Run（旧行为在 stopping 后立即补足，job 被多跑
	// 一次 + 僵尸 Run 悬挂）。
	clock.Advance(2 * time.Minute)
	e.taskStep(ctx)
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, taskID).State)
	assert.Len(t, taskRuns(t, e, taskID), 1, "one-shot never grows a second run")
}

// TestTaskOneShotStoppingNoReplenish（修复钉死）：one-shot Run 停止收口中
// 也是占位——TTL janitor 迁 stopping 后同拍不补第二 Run；停止兜底收口
// stopped → Task 镜像 completed，无 driving 残留。
func TestTaskOneShotStoppingNoReplenish(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK0000000000000000B"
	createTaskRow(t, e, taskID, "", task.FormOneShot, 1, 60, "")
	e.taskStep(ctx)
	runs := taskRuns(t, e, taskID)
	require.Len(t, runs, 1)

	// TTL 到期 → janitor stopping（停止收口兜底 deadline 落行）。
	clock.Advance(61 * time.Second)
	e.taskStep(ctx)
	require.Equal(t, run.StateStopping, getRunRow(t, e, runs[0].ID).State)
	require.Len(t, taskRuns(t, e, taskID), 1,
		"stopping occupies the one-shot slot: no second run may be minted")

	// 停止兜底（观测缺位）：推过收口截止 → stopped → 同拍镜像 completed。
	clock.Advance(2*e.opts.TaskStopGrace + 11*time.Second)
	e.taskStep(ctx)
	got := getRunRow(t, e, runs[0].ID)
	assert.Equal(t, run.StateStopped, got.State)
	assert.Equal(t, run.ReasonTTLExpired, got.StopReason)
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, taskID).State)
	driving, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), taskID, taskDrivingStates)
	require.NoError(t, err)
	assert.Empty(t, driving, "no driving runs may outlive a terminal one-shot task")
	assert.Len(t, taskRuns(t, e, taskID), 1, "exactly one run ever existed")
}

// TestTaskZombieRunSweep：pre-fix 存量僵尸（终态 Task + driving Run 行）→
// taskStep 收口 stopped/platform_drained（幂等 CAS；Task 行不动）。Run 行
// 是 Schedule 重叠判定的真源——僵尸收口即解挂永久 skip。
func TestTaskZombieRunSweep(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK0000000000000000C"
	createTaskRow(t, e, taskID, "", task.FormOneShot, 1, 3600, "")
	// 直落存量形态：Task 已终态、Run 停在 driving（绕过补足链）。
	require.NoError(t, e.tasks.Transit(ctx, e.db.Runner(), taskID,
		[]task.State{task.StateActive}, task.StateCompleted, nil))
	runID := "01JD0RUN00000000000000000001"
	require.NoError(t, e.runs.Create(ctx, e.db.Runner(), &run.Run{
		ID: runID, TaskID: taskID, ProjectID: tTaskProject,
		State: run.StateStopping, StopReason: run.ReasonStoppedByUser,
		WorkloadID: runID, DNSName: RunDNSName(runID),
	}))

	e.taskStep(ctx)

	got := getRunRow(t, e, runID)
	assert.Equal(t, run.StateStopped, got.State, "the zombie run is closed")
	assert.Equal(t, run.ReasonPlatformDrained, got.StopReason, "zombie runs close as platform_drained")
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, taskID).State,
		"the terminal task row is untouched")
}

// TestTaskStopForce：StopTask force → runs stopping/stopped_by_user；
// 全终态 → drained。
func TestTaskStopForce(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000005"
	createTaskRow(t, e, taskID, "", task.FormResident, 2, 0, "")
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, taskID), 2)

	_, err := e.StopTask(ctx, taskID, true)
	require.NoError(t, err)
	for _, r := range taskRuns(t, e, taskID) {
		assert.Equal(t, run.StateStopping, r.State)
		assert.Equal(t, run.ReasonStoppedByUser, r.StopReason)
	}

	for _, r := range taskRuns(t, e, taskID) {
		observe(t, e, r.ID, capability.WorkloadStopped, nil)
	}
	e.taskStep(ctx)
	assert.Equal(t, task.StateDrained, getTaskRow(t, e, taskID).State)
}

// TestOwnerRevokedDrainsTasks（P1-8 拉式口径）：属主 Token 吊销 → task 环
// 周期扫描排空（owner_revoked）。
func TestOwnerRevokedDrainsTasks(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	// FK 地基：team + role（engine 测试库不跑账号批种子）。
	require.NoError(t, team.New(e.db.Clock()).Create(ctx, e.db.Runner(), &team.Team{ID: "default", Name: "default"}))
	require.NoError(t, role.New(e.db.Clock()).Create(ctx, e.db.Runner(), &role.Role{
		ID: "builtin-member", Name: "member", Builtin: true, Scopes: []string{"tasks:read"},
	}))
	tokenID := "01JD0TOK0000000000000000000"
	require.NoError(t, tokenrepo.New(e.db.Clock()).Create(ctx, e.db.Runner(), &tokenrepo.Token{
		ID: tokenID, TeamID: "default", Name: "worker", RoleID: "builtin-member", SHA256: "aa", Prefix: "flt_x",
	}))

	taskID := "01JD0TASK00000000000000006"
	createTaskRow(t, e, taskID, "pool", task.FormResident, 2, 0, "")
	// 属主引用上锚（吊销扫描的锚——createTaskRow 不带属主面）。
	_, execErr := e.db.Runner().ExecContext(ctx, `UPDATE tasks SET owner_token_id = ? WHERE id = ?`, tokenID, taskID)
	require.NoError(t, execErr)
	require.NoError(t, e.tasks.UpdateLease(ctx, e.db.Runner(), taskID,
		state.FormatTime(e.clock.Now().Add(time.Hour)))) // lease 未过期：排空只因吊销

	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, taskID), 2)

	// 吊销属主 Token。
	_, err := e.tokens.Revoke(ctx, e.db.Runner(), tokenID)
	require.NoError(t, err)

	e.taskStep(ctx)
	tk := getTaskRow(t, e, taskID)
	assert.Equal(t, task.StateDraining, tk.State)
	for _, r := range taskRuns(t, e, taskID) {
		assert.Equal(t, run.ReasonOwnerRevoked, r.StopReason)
	}
}

// TestTaskScale：resident 期望并发原地调整 + task.updated 事件。
func TestTaskScale(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000007"
	createTaskRow(t, e, taskID, "", task.FormResident, 1, 0, "")

	scaled, err := e.ScaleTask(ctx, taskID, 4)
	require.NoError(t, err)
	assert.Equal(t, int64(4), scaled.DesiredConcurrency)
	assert.Contains(t, eventNames(t, e, taskID), "task.updated")

	e.taskStep(ctx)
	assert.Len(t, taskRuns(t, e, taskID), 4)
}

// TestScaleTaskQuota（ADR-0017 附录 A.1）：并发总量配额在写事务内按净增量
// 执法——超限拒绝、等值/缩量放行；终态 Task 不占位。
func TestScaleTaskQuota(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	a := createTaskRow(t, e, "01JD0TASK0000000000000000A", "pool-a", task.FormResident, 150, 0, "")
	b := createTaskRow(t, e, "01JD0TASK0000000000000000B", "pool-b", task.FormResident, 50, 0, "")

	_, err := e.ScaleTask(ctx, a.ID, 151) // sum 200 + 净增 1 → 超限
	assert.ErrorIs(t, err, ErrTaskQuota)

	got, err := e.ScaleTask(ctx, a.ID, 150) // 等值改写零净增 → 放行
	require.NoError(t, err)
	assert.EqualValues(t, 150, got.DesiredConcurrency)

	got, err = e.ScaleTask(ctx, a.ID, 100) // 缩量放行
	require.NoError(t, err)
	assert.EqualValues(t, 100, got.DesiredConcurrency)

	// 终态 Task 的 desired 不进统计：b completed 后总量回到 100，可扩满 200。
	require.NoError(t, e.tasks.Transit(ctx, e.db.Runner(), b.ID,
		[]task.State{task.StateActive}, task.StateCompleted, nil))
	got, err = e.ScaleTask(ctx, a.ID, 200)
	require.NoError(t, err)
	assert.EqualValues(t, 200, got.DesiredConcurrency)
}

// TestTaskDeleteTeardown：DeleteTask 拆载体 + Run 终态化 + tombstone。
func TestTaskDeleteTeardown(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000008"
	createTaskRow(t, e, taskID, "", task.FormOneShot, 1, 0, "")
	e.taskStep(ctx)
	runs := taskRuns(t, e, taskID)
	require.Len(t, runs, 1)

	require.NoError(t, e.DeleteTask(ctx, taskID))
	assert.Equal(t, task.StateDeleted, getTaskRow(t, e, taskID).State)
	assert.Equal(t, run.StateStopped, getRunRow(t, e, runs[0].ID).State)
	assert.Equal(t, run.ReasonStoppedByUser, getRunRow(t, e, runs[0].ID).StopReason)
	assert.Contains(t, eventNames(t, e, taskID), "task.deleted")

	removed := rt.removedSnapshot()
	require.Len(t, removed, 1)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tTaskProject, Task: taskID}, removed[0])
	// 幂等。
	require.NoError(t, e.DeleteTask(ctx, taskID))
}

// TestStopRunSingle：单 Run 停止（stopped_by_user）；Task 下一拍补位。
func TestStopRunSingle(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK00000000000000009"
	createTaskRow(t, e, taskID, "", task.FormResident, 1, 0, "")
	e.taskStep(ctx)
	runs := taskRuns(t, e, taskID)
	require.Len(t, runs, 1)

	stopped, err := e.StopRun(ctx, runs[0].ID)
	require.NoError(t, err)
	assert.Equal(t, run.StateStopping, stopped.State)
	assert.Equal(t, run.ReasonStoppedByUser, stopped.StopReason)

	observe(t, e, runs[0].ID, capability.WorkloadStopped, nil)
	e.taskStep(ctx) // 补足 1
	live := 0
	for _, r := range taskRuns(t, e, taskID) {
		if r.State.Active() {
			live++
		}
	}
	assert.Equal(t, 1, live)
}

// TestEnsureTaskWorkloadsSignatureSkip：期望集签名未变 → 跳过 Ensure
// （swarm API 压力面）；周期到期 → 强制重放。
func TestEnsureTaskWorkloadsSignatureSkip(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK0000000000000000A"
	createTaskRow(t, e, taskID, "", task.FormResident, 1, 0, "")

	e.taskStep(ctx)
	n1 := len(rt.calls())
	require.Positive(t, n1)

	e.taskStep(ctx) // 签名未变 → 跳过
	assert.Equal(t, n1, len(rt.calls()), "unchanged desired set must skip Ensure")

	clock.Advance(e.opts.TaskReconcileInterval + time.Second)
	e.taskStep(ctx) // 周期强制重放
	assert.Equal(t, n1+1, len(rt.calls()), "reconcile interval must force re-ensure")
}

// TestProjectTaskSpecFields：投影细节断言（StopGrace 注入）。
func TestProjectTaskSpecFields(t *testing.T) {
	spec := &specv1.TaskSpec{
		SchemaVersion: 1,
		Task:          &specv1.TaskRef{Id: "01JTASK", Project: tTaskProject},
		Process:       &specv1.ProcessSpec{Name: "run", ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"}},
	}
	w, _, err := ProjectTask(spec, "default", "01JRUN", true)
	require.NoError(t, err)
	assert.Equal(t, "01JRUN", w.ID)
	assert.Equal(t, capability.RestartNever, w.Restart)
}
