package engine

// firstBootJobs 部署链接线测试（F1.13，ADR-0030）：串行 converge、失败
// 回滚（jobs 永不重跑）、等待超时强停、重启重放零重铸、取消/抢占收口、
// 网络解析（缺省全项目网/声明翻译/未批准 fail-closed）、from_build digest、
// 配额有界重试。手动驱动形态（step/taskStep 直调 + 观测直注）。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// tFakeDigest 是 from_build 解析断言的构建产物 digest 锚。
const tFakeDigest = "sha256:aa11223344556677889900112233445566778899001122334455667788990011"

// jobsSpec 合成带 firstBootJobs 的镜像直投 spec（n 保证跨调用唯一）。
func jobsSpec(n int, jobsJSON string) string {
	return fmt.Sprintf(`{"schema_version":1,"app":{"id":"%s","project":"%s"},`+
		`"source":{"image":{"ref":"nginx:1.27"}},`+
		`"processes":[{"name":"web","image":"nginx:1.27","replicas":1}],`+
		`"first_boot_jobs":[%s]}`,
		tAppID, tProjectID, jobsJSON)
}

const tMigrateJob = `{"name":"migrate","ttl":"600s","process":{"image":"busybox:1.37","command":["/migrate"]}}`

// createProjectNetwork 落一条项目网络行（缺省挂靠断言的锚）。
func createProjectNetwork(t *testing.T, e *Engine, id, name string) {
	t.Helper()
	require.NoError(t, networkrepo.New(e.clock).Create(context.Background(), e.db.Runner(),
		&networkrepo.Network{ID: id, ProjectID: tProjectID, Name: name}))
}

// mintedJobTask 读取部署当前锚定的 job Task 行。
func mintedJobTask(t *testing.T, e *Engine, d *deployment.Deployment) *task.Task {
	t.Helper()
	_, taskID, ok := d.FirstBootAnchor()
	require.True(t, ok, "deployment should be anchored on a first boot task (cursor=%q)", d.FirstBoot)
	row, err := e.tasks.Get(context.Background(), e.db.Runner(), taskID)
	require.NoError(t, err)
	return row
}

// completeJobTask 把锚定 job 推到成功终态（Run completed → Task 镜像）。
func completeJobTask(t *testing.T, e *Engine, d *deployment.Deployment) {
	t.Helper()
	ctx := context.Background()
	row := mintedJobTask(t, e, d)
	e.taskStep(ctx) // 补足 Run
	runs := taskRuns(t, e, row.ID)
	require.Len(t, runs, 1)
	zero := 0
	observe(t, e, runs[0].ID, capability.WorkloadCompleted, &zero)
	e.taskStep(ctx) // 终态镜像
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, row.ID).State)
}

// TestFirstBootSerialConverge：两 job 串行——job0 铸造（游标/deadline/
// 事件/task 行形态）→ 成功 → job1 才铸造 → 全成 → carrier 相位 → succeeded。
// 事件序列含两条 deployment.first_boot_job；重复拍不重铸。
func TestFirstBootSerialConverge(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	createProjectNetwork(t, e, "01JD0NET00000000000000000", "default")
	spec := jobsSpec(1, tMigrateJob+`,`+
		`{"name":"seed","ttl":"300s","process":{"image":"busybox:1.37","command":["/seed"]}}`)
	revID := freezeSpec(t, e, 1, spec)

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)

	e.step(ctx) // queued → preparing → releasing → 铸 job0 → 等待
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	idx, taskID, ok := d.FirstBootAnchor()
	require.True(t, ok)
	assert.Equal(t, 0, idx)
	assert.NotEmpty(t, d.ObserveDeadline, "wait deadline is persisted with the cursor")
	assert.Equal(t, taskID, FirstBootTaskID(d), "API exposure mirrors the cursor")

	row := getTaskRow(t, e, taskID)
	assert.Equal(t, task.FormOneShot, row.Form)
	assert.Equal(t, "", row.Name, "deploy-time jobs are unnamed system-owned tasks")
	assert.Equal(t, task.StateActive, row.State)
	assert.Contains(t, string(row.Spec), `"ttl_seconds":"600"`)
	assert.Contains(t, string(row.Spec), `"image":"busybox:1.37"`)
	assert.Contains(t, string(row.Spec), `"default"`, "default attach = all active project networks")
	assert.Contains(t, eventNames(t, e, taskID), "task.created")
	assert.Equal(t, []string{"deployment.first_boot_job"},
		filterPrefix(eventNames(t, e, d.ID), "deployment.first_boot_job"))

	// 重复拍不重铸（重启重放的零重铸面：游标持锚）。
	e.step(ctx)
	assert.Equal(t, 1, taskCount(t, e), "re-entering release must not re-mint the anchored job")

	// job0 成功 → 下一拍才铸 job1（串行）。
	completeJobTask(t, e, d)
	// ProjectTask 加宽面：job Run 载体渲染 process.networks（平台网络名
	// 原样——ADR-0030 决策 7）。
	jobEnsure := false
	for _, c := range rt.calls() {
		if c.NS.Task != taskID {
			continue
		}
		w, ok := c.Spec["run"]
		if !ok {
			continue // 终态后的空集收口拍（移除残留载体）无 run Workload
		}
		jobEnsure = true
		assert.Equal(t, []string{"default"}, w.Networks,
			"the job run carrier attaches the project network set")
		assert.Equal(t, capability.RestartNever, w.Restart)
	}
	assert.True(t, jobEnsure, "the job task must have been ensured to the runtime")
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	_, taskID2, ok := d.FirstBootAnchor()
	require.True(t, ok)
	assert.NotEqual(t, taskID, taskID2, "second job gets its own task")
	assert.Equal(t, 2, taskCount(t, e))

	completeJobTask(t, e, d)
	e.step(ctx) // done → materialize → L1 deadline
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	assert.Equal(t, deployment.FirstBootDone, d.FirstBoot)
	assert.Equal(t, "", FirstBootTaskID(d), "no anchor once jobs are done")

	// carrier 相位：running → observing → succeeded。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateSucceeded, d.State)

	assert.Equal(t, []string{"deployment.first_boot_job", "deployment.first_boot_job"},
		filterPrefix(eventNames(t, e, d.ID), "deployment.first_boot_job"))
	calls := rt.calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, "nginx:1.27", calls[len(calls)-1].Spec["web"].Image,
		"carriers materialize only after all jobs complete")
}

// TestFirstBootJobFailureRollsBack：job Run 失败 → deployment.failed（error
// 点名 job）→ 自动回滚 Replay from_revision——**jobs 不重跑**（无第二次铸造）。
func TestFirstBootJobFailureRollsBack(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()

	// 成功基线（无 job）。
	base := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	first, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: base})
	require.NoError(t, err)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, first.ID).State)

	// 带失败 job 的 R2。
	r2 := freezeSpec(t, e, 2, jobsSpec(2, tMigrateJob))
	second, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
	require.NoError(t, err)
	e.step(ctx) // 铸 job0 → 等待
	d := getDeployment(t, e, second.ID)
	require.Equal(t, deployment.StateReleasing, d.State)

	// job 失败（exit 1）→ Task 镜像 failed。
	row := mintedJobTask(t, e, d)
	e.taskStep(ctx)
	runs := taskRuns(t, e, row.ID)
	require.Len(t, runs, 1)
	one := 1
	observe(t, e, runs[0].ID, capability.WorkloadFailed, &one)
	e.taskStep(ctx)
	assert.Equal(t, task.StateFailed, getTaskRow(t, e, row.ID).State)

	e.step(ctx) // failed → 自动回滚 → Replay（Ensure from_revision, gen 3）
	d = getDeployment(t, e, second.ID)
	require.Equal(t, deployment.StateRollingBack, d.State, "auto rollback in flight (job failure: %s)", d.Error)

	// 回放就绪 → observing → succeeded（jobs 不重跑）。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 3))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, second.ID).State)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	d = getDeployment(t, e, second.ID)
	require.Equal(t, deployment.StateSucceeded, d.State, "rollback replays the baseline to success")
	assert.Contains(t, d.Error, `"migrate"`, "failure text names the job")
	assert.Contains(t, d.Error, "failed (exit code 1)")
	assert.Equal(t, base, d.ToRevision, "terminal fact points at the replayed revision")
	assert.Equal(t, 1, taskCount(t, e), "rollback never re-runs first boot jobs")
	calls := rt.calls()
	assert.Equal(t, "nginx:1.26", calls[len(calls)-1].Spec["web"].Image, "replay re-ensures from_revision")
}

// TestFirstBootWaitTimeoutForceStops：等待窗到期未终态 → failDeployment +
// 锚定 Task 被强停（防超窗迁移 job 继续写库与回放竞态）。
func TestFirstBootWaitTimeoutForceStops(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, jobsSpec(3, tMigrateJob))
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // 铸 job（ttl 600s + grace 2m）
	d = getDeployment(t, e, d.ID)
	row := mintedJobTask(t, e, d)
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, row.ID), 1, "run is pending (never completes)")

	clock.Advance(600*time.Second + e.opts.FirstBootWaitGrace + time.Second)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateFailed, d.State)
	assert.Contains(t, d.Error, `"migrate"`)
	assert.Contains(t, d.Error, "wait window")
	assert.Equal(t, task.StateDraining, getTaskRow(t, e, row.ID).State,
		"the past-deadline job task is force-stopped")
}

// TestFirstBootTTLExpiryFails：job 超过自身 ttl 被 janitor 停止 → 终态对
// (stopped, ttl_expired) = 部署失败（非 completed 镜像）。
func TestFirstBootTTLExpiryFails(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, jobsSpec(4, tMigrateJob))
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	row := mintedJobTask(t, e, d)
	e.taskStep(ctx)
	runs := taskRuns(t, e, row.ID)
	require.Len(t, runs, 1)

	// TTL 到期（Run deadline 落库 = 创建时刻 + 600s）→ janitor stopping →
	// 停止兜底收口 stopped/ttl_expired → Task 镜像。
	clock.Advance(601 * time.Second)
	e.taskStep(ctx)
	clock.Advance(2*e.opts.TaskStopGrace + 11*time.Second)
	e.taskStep(ctx)
	assert.Equal(t, task.StateCompleted, getTaskRow(t, e, row.ID).State,
		"ttl expiry mirrors to completed on the task row")

	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateFailed, d.State)
	assert.Contains(t, d.Error, "exceeded its ttl")
}

// TestFirstBootRestartReplayZeroRemint：铸造后同库重启（进程内状态丢弃）
// → 零重铸、等待恢复；完成后 carrier 相位照常推进。
func TestFirstBootRestartReplayZeroRemint(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, jobsSpec(5, tMigrateJob))
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	_, taskID, ok := d.FirstBootAnchor()
	require.True(t, ok)

	e2 := restartEngine(t, e)
	e2.step(ctx) // 重启后重入：游标持锚，不重铸
	d = getDeployment(t, e2, d.ID)
	_, taskIDAfter, ok := d.FirstBootAnchor()
	require.True(t, ok)
	assert.Equal(t, taskID, taskIDAfter, "the anchored task survives the restart")
	assert.Equal(t, 1, taskCount(t, e2))

	// 重启后完成 job（观测缓存已丢，Run 推进仍正常——行是真源）。
	completeJobTask(t, e2, d)
	e2.step(ctx)
	e2.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e2.step(ctx)
	clock.Advance(61 * time.Second)
	e2.step(ctx)
	d = getDeployment(t, e2, d.ID)
	require.Equal(t, deployment.StateSucceeded, d.State)
}

// TestFirstBootCancelAbandonsJob：取消 jobs 等待中的部署 → 锚定 Task 被
// 强停（draining；审计携带操作者）。
func TestFirstBootCancelAbandonsJob(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, jobsSpec(6, tMigrateJob))
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	row := mintedJobTask(t, e, d)
	e.taskStep(ctx)
	require.Len(t, taskRuns(t, e, row.ID), 1)

	cancelled, err := e.Cancel(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, deployment.StateCancelled, cancelled.State)
	assert.Equal(t, task.StateDraining, getTaskRow(t, e, row.ID).State)
}

// TestFirstBootSupersedeAbandonsJob：显式 supersede 抢占 jobs 等待中的
// 部署 → 锚定 Task 强停。
func TestFirstBootSupersedeAbandonsJob(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, jobsSpec(7, tMigrateJob))
	first, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	d := getDeployment(t, e, first.ID)
	row := mintedJobTask(t, e, d)

	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))
	_, err = e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2, Supersede: true})
	require.NoError(t, err)
	assert.Equal(t, deployment.StateSuperseded, getDeployment(t, e, first.ID).State)
	assert.Equal(t, task.StateDraining, getTaskRow(t, e, row.ID).State,
		"superseding a job-waiting deployment force-stops the anchored job")
}

// TestFirstBootNetworksDeclared：声明的挂靠网铸时解析（taskGroup: 翻译 /
// 裸名直用；跨 Project 未批准在 Submit 受理预检即拒——CheckPeerRefs 覆盖
// job 引用）。
func TestFirstBootNetworksDeclared(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	createProjectNetwork(t, e, "01JD0NET00000000000000001", "default")
	spec := jobsSpec(8, `{"name":"migrate","ttl":"300s","process":`+
		`{"image":"busybox:1.37","command":["/migrate"],"networks":["workers","taskGroup:dispatcher"]}}`)
	revID := freezeSpec(t, e, 1, spec)
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	row := mintedJobTask(t, e, d)
	assert.Contains(t, string(row.Spec), `"workers"`)
	assert.Contains(t, string(row.Spec), `"taskgrp-dispatcher"`,
		"taskGroup refs are translated to platform network names at mint")

	// 未批准跨 Project 引用：Submit 受理预检（CheckPeerRefs 现覆盖 job 面）。
	e2, _, _ := newTestEngine(t)
	bad := jobsSpec(9, `{"name":"migrate","ttl":"300s","process":`+
		`{"image":"busybox:1.37","command":["/migrate"],"networks":["project:01JD9PROJ99999999999999999/main"]}}`)
	badRev := freezeSpec(t, e2, 1, bad)
	_, err = e2.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: badRev})
	require.ErrorIs(t, err, ErrCrossProjectRefNotApproved)
}

// TestFirstBootFromBuildDigest：from_build job 在铸时解析为完整 digest 引用
// （Revision 级单产物 by 进程名）；引用非 from_build 进程 = 精确失败。
func TestFirstBootFromBuildDigest(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.registry = newFakeRegistry()
	e.builders = map[string]capability.Builder{specir.BuilderDockerfile: &fakeBuilder{digest: tFakeDigest}} // driveBuilding 前置门：builder 在册（产物行已直落，不执行）
	ctx := context.Background()
	spec := fmt.Sprintf(`{"schema_version":1,"app":{"id":"%s","project":"%s"},`+
		`"source":{"git":{"repo":"https://git.test/x.git","ref":"main"}},`+
		`"build":{"builder":"dockerfile","dockerfile":"Dockerfile"},`+
		`"processes":[{"name":"web","from_build":"web","replicas":1}],`+
		`"first_boot_jobs":[{"name":"migrate","ttl":"300s","process":{"from_build":"web","command":["/migrate"]}}]}`,
		tAppID, tProjectID)
	revID := freezeSpec(t, e, 1, spec)

	// 构建产物行（releasing 进入条件 = build succeeded；building 落行后经
	// Transit 携 digest 收终态——Create 不落 digest 列，真实流同款路径）。
	b := &build.Build{
		ID: "01JD0B00000000000000000FB", AppID: tAppID,
		RevisionID: revID, State: build.StateBuilding,
	}
	require.NoError(t, e.builds.Create(ctx, e.db.Runner(), b))
	require.NoError(t, e.builds.Transit(ctx, e.db.Runner(), b.ID,
		[]build.State{build.StateBuilding}, build.StateSucceeded,
		func(m *build.Build) { m.Digest = tFakeDigest }))

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // building → releasing（build 已成）→ 铸 job
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State, "deployment error: %s", d.Error)
	row := mintedJobTask(t, e, d)
	assert.Contains(t, string(row.Spec),
		fmt.Sprintf(`"image":"%s"`, fakeRegistryAddr+"/"+strings.ToLower(tAppID)+"@"+tFakeDigest),
		"from_build resolves to the full digest reference at mint")
}

// TestFirstBootQuotaBoundedRetry：项目 Task 配额满 → 铸造受阻转有界等待
// （deadline 收口，不立即失败）；超窗才失败且理由点名配额。
func TestFirstBootQuotaBoundedRetry(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, jobsSpec(10, tMigrateJob))
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)

	// 灌满 per-Project Task 配额（100 活跃行；job 自身还要占一位）。
	for i := 0; i < MaxTasksPerProject; i++ {
		createTaskRow(t, e, fmt.Sprintf("01JD0FILL00000000000000%02d", i), "", task.FormOneShot, 1, 0, "")
	}

	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	assert.Equal(t, deployment.StateReleasing, d.State, "quota pressure waits, does not fail")
	assert.NotEmpty(t, d.ObserveDeadline, "the bounded wait window is persisted")

	e.step(ctx) // 再拍仍在等
	assert.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)

	clock.Advance(600*time.Second + e.opts.FirstBootWaitGrace + time.Second)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateFailed, d.State)
	assert.Contains(t, d.Error, "quota")
}

// TestRollbackDeploymentSkipsFirstBootJobs：显式 rollback 创建的**新**
// Deployment 直落 done 游标（ADR-0030 决策 5）——目标 Revision 声明
// firstBootJobs 也不重跑（迁移已应用，重跑反而破坏）；审计标注
// kind=rollback（Kind 不再是死参数）。
func TestRollbackDeploymentSkipsFirstBootJobs(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()

	// R1：带 job 的 spec 走完整链到 succeeded（job 执行一轮，合法）。
	r1 := freezeSpec(t, e, 1, jobsSpec(11, tMigrateJob))
	first, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r1})
	require.NoError(t, err)
	e.step(ctx) // 铸 job → 等待
	d := getDeployment(t, e, first.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	completeJobTask(t, e, d)
	e.step(ctx) // done → materialize → L1
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, first.ID).State)
	tasksAfterR1 := taskCount(t, e)

	// R2：第二 revision（无 job）部署成功。
	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))
	second, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
	require.NoError(t, err)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 2))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, second.ID).State)

	// 显式 rollback 回 R1（spec 带 jobs）。
	rb, err := e.Rollback(ctx, tAppID, r1)
	require.NoError(t, err)
	e.step(ctx) // queued → preparing → releasing →（游标 done）→ carrier
	d = getDeployment(t, e, rb.ID)
	require.Equal(t, deployment.StateReleasing, d.State, "deployment error: %s", d.Error)
	assert.Equal(t, deployment.FirstBootDone, d.FirstBoot,
		"the rollback deployment lands with the done cursor, never the empty one")

	// carrier 相位照常：running → observing → succeeded。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 3))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, rb.ID).State)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	d = getDeployment(t, e, rb.ID)
	require.Equal(t, deployment.StateSucceeded, d.State)

	// 零重铸：无 job Task 新增、无 first_boot_job 事件。
	assert.Equal(t, tasksAfterR1, taskCount(t, e), "rollback never re-mints first boot jobs")
	assert.Empty(t, filterPrefix(eventNames(t, e, rb.ID), "deployment.first_boot_job"),
		"no first boot job events on the rollback deployment")
}

// TestRollbackAuditAnnotatesKind：deployment.create 审计携带来源标注
// （AfterFP 后缀 "; kind=rollback"）；常规部署不带后缀。
func TestRollbackAuditAnnotatesKind(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 3, imageSpecFor("nginx:1.26"))

	plain, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	entries, err := e.audits.ListFiltered(ctx, e.db.Runner(),
		audit.Filter{ActionPrefix: "deployment.create", Resource: "deployment/" + plain.ID})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.NotContains(t, entries[0].AfterFP, "kind=", "plain deploys carry no kind annotation")

	rb, err := e.Rollback(ctx, tAppID, revID)
	require.NoError(t, err)
	entries, err = e.audits.ListFiltered(ctx, e.db.Runner(),
		audit.Filter{ActionPrefix: "deployment.create", Resource: "deployment/" + rb.ID})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].AfterFP, "kind=rollback",
		"the rollback submission is identifiable in the audit stream")
}

// TestFirstBootLateCompletionNotL1Timeout：job 完成观测晚于等待截止（控制
// 面停机跨窗/环卡滞形态）→ done 迁移清空的截止对 release 可见——进
// carrier 相位自设新 L1 deadline，不误判 "L1 timed out" 假回滚。
func TestFirstBootLateCompletionNotL1Timeout(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, jobsSpec(12, tMigrateJob))
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // 铸 job → 等待（deadline = ttl 600s + grace 2m）
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	row := mintedJobTask(t, e, d)
	e.taskStep(ctx)
	runs := taskRuns(t, e, row.ID)
	require.Len(t, runs, 1)

	// 停机跨窗：时钟推过 job 等待截止（完成观测迟到）。
	clock.Advance(600*time.Second + e.opts.FirstBootWaitGrace + time.Minute)

	// job 实际完成：completed 观测 → Run 终态（pending 直收，不查截止）→
	// Task 镜像——行终态先于 deploy 拍落库。
	zero := 0
	observe(t, e, runs[0].ID, capability.WorkloadCompleted, &zero)
	e.taskStep(ctx)
	require.Equal(t, task.StateCompleted, getTaskRow(t, e, row.ID).State)

	e.step(ctx) // done 迁移（截止清空对调用方可见）→ carrier 相位自设 L1
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State,
		"late job completion must not fail as L1 timeout (error: %s)", d.Error)
	assert.Equal(t, deployment.FirstBootDone, d.FirstBoot)
	assert.NotEmpty(t, d.ObserveDeadline, "carrier phase self-sets a fresh L1 deadline")
	l1 := parseDeadline(d.ObserveDeadline)
	require.NotNil(t, l1)
	assert.True(t, e.clock.Now().Before(*l1), "the fresh L1 deadline must be in the future")

	// carrier 相位照常走到 succeeded。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d.ID).State)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)
}

// taskCount 数项目 Task 行（零重铸断言的口径）。
func taskCount(t *testing.T, e *Engine) int {
	t.Helper()
	rows, err := e.tasks.ListByProject(context.Background(), e.db.Runner(), tProjectID, "", 1000)
	require.NoError(t, err)
	return len(rows)
}

// filterPrefix 过滤事件名前缀（first_boot 事件序列断言用）。
func filterPrefix(names []string, prefix string) []string {
	var out []string
	for _, n := range names {
		if len(n) >= len(prefix) && n[:len(prefix)] == prefix {
			out = append(out, n)
		}
	}
	return out
}
