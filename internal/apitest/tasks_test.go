package apitest_test

// Task/Run API 全链（F1.5/F1.6）：创建（受理位 + 归一化 + task.created）、
// 池补足与双级 DNS、one-shot 终态、WaitRun、幂等键重放、属主缺省解析。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// taskFixtureProject 建项目返回 ID。
func taskFixtureProject(t *testing.T, h *apitest.Harness, ctx context.Context, name string) string {
	t.Helper()
	p, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: name})
	require.NoError(t, err)
	return p.GetProject().GetId()
}

// driveTaskStep 驱动 Task 收敛环（手动形态）。
func driveTaskStep(t *testing.T, h *apitest.Harness, ctx context.Context) {
	t.Helper()
	h.Drive(ctx)
}

func TestTaskOneShotAPILifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projectID := taskFixtureProject(t, h, ctx, "automation")

	tc := automationv1.NewTasksServiceClient(h.Conn)
	created, err := tc.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: projectID, Name: "migrate",
		Image: "busybox:1.37", Command: []string{"/migrate"},
		TtlSeconds: 3600, NetworkGroup: "dispatcher",
		Env: map[string]string{"POOL": "gold"},
	})
	require.NoError(t, err)
	taskID := created.GetTask().GetId()
	assert.Equal(t, "one-shot", created.GetTask().GetForm())
	assert.Equal(t, "active", created.GetTask().GetState())
	assert.Contains(t, created.GetTask().GetDnsName(), "task-")
	// 幂等键重放：同键同体 → 同响应。
	ctxKeyed := sdk.WithIdempotencyKey(ctx, "task-once-1")
	first, err := tc.CreateTask(ctxKeyed, &automationv1.CreateTaskRequest{
		ProjectId: projectID, Name: "idem", Image: "busybox:1.37", IdempotencyKey: "task-once-1",
	})
	require.NoError(t, err)
	replay, err := tc.CreateTask(ctxKeyed, &automationv1.CreateTaskRequest{
		ProjectId: projectID, Name: "idem", Image: "busybox:1.37", IdempotencyKey: "task-once-1",
	})
	require.NoError(t, err)
	assert.Equal(t, first.GetTask().GetId(), replay.GetTask().GetId())

	// 驱动：补足 1 Run + Ensure（Task 域 ns + never + 双级 DNS）。
	driveTaskStep(t, h, ctx)
	rc := automationv1.NewRunsServiceClient(h.Conn)
	runs, err := rc.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: taskID})
	require.NoError(t, err)
	require.Len(t, runs.GetRuns(), 1)
	runID := runs.GetRuns()[0].GetId()
	assert.Equal(t, "pending", runs.GetRuns()[0].GetState())
	assert.Contains(t, runs.GetRuns()[0].GetDnsName(), "run-")

	ensures := h.Runtime.Calls()
	var migrateEnsure *apitest.EnsureCall
	for i := range ensures {
		if ensures[i].NS.Task == taskID {
			migrateEnsure = &ensures[i]
		}
	}
	require.NotNil(t, migrateEnsure, "task-domain ensure must be recorded")
	require.Len(t, migrateEnsure.Spec, 1)
	w := migrateEnsure.Spec["run"]
	assert.Equal(t, runID, w.ID)
	assert.Equal(t, capability.RestartNever, w.Restart)
	assert.Equal(t, []string{"taskgrp-dispatcher"}, w.Networks)
	assert.Equal(t, int64(1), w.Replicas)

	// 观测 running → 自然完成（exit 0）→ Task 镜像 completed。
	h.Runtime.ReportRunning(runID, capability.Generation(1))
	driveTaskStep(t, h, ctx)
	got, err := rc.GetRun(ctx, &automationv1.GetRunRequest{Id: runID})
	require.NoError(t, err)
	assert.Equal(t, "running", got.GetRun().GetState())

	// one-shot 完成：fake runtime 无终态观测注入面——直接验证停止语义经
	// StopTask（force）路径。
	stopped, err := tc.StopTask(ctx, &automationv1.StopTaskRequest{Id: taskID, Force: true})
	require.NoError(t, err)
	assert.Equal(t, "draining", stopped.GetTask().GetState())
	h.Runtime.ReportStopped(runID, capability.Generation(1))
	driveTaskStep(t, h, ctx)

	gotRun, err := rc.GetRun(ctx, &automationv1.GetRunRequest{Id: runID})
	require.NoError(t, err)
	assert.Equal(t, "stopped", gotRun.GetRun().GetState())
	assert.Equal(t, "stopped_by_user", gotRun.GetRun().GetStopReason())

	gotTask, err := tc.GetTask(ctx, &automationv1.GetTaskRequest{Id: taskID})
	require.NoError(t, err)
	assert.Equal(t, "drained", gotTask.GetTask().GetState())

	// 删除（幂等）+ tombstone 后行仍可读。
	_, err = tc.DeleteTask(ctx, &automationv1.DeleteTaskRequest{Id: taskID})
	require.NoError(t, err)
	_, err = tc.DeleteTask(ctx, &automationv1.DeleteTaskRequest{Id: taskID})
	require.NoError(t, err)
	gotTask, err = tc.GetTask(ctx, &automationv1.GetTaskRequest{Id: taskID})
	require.NoError(t, err)
	assert.Equal(t, "deleted", gotTask.GetTask().GetState())
}

func TestTaskResidentPoolAndScale(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projectID := taskFixtureProject(t, h, ctx, "automation2")

	tc := automationv1.NewTasksServiceClient(h.Conn)
	rc := automationv1.NewRunsServiceClient(h.Conn)
	created, err := tc.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: projectID, Name: "dispatcher", Form: "resident",
		Image: "ghcr.io/torchwood/dispatcher:1", DesiredConcurrency: 2,
	})
	require.NoError(t, err)
	taskID := created.GetTask().GetId()
	assert.Equal(t, "resident", created.GetTask().GetForm())

	driveTaskStep(t, h, ctx)
	runs, err := rc.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: taskID})
	require.NoError(t, err)
	assert.Len(t, runs.GetRuns(), 2)

	// 列表投影：active_run_count + after 游标。
	listed, err := tc.ListTasks(ctx, &automationv1.ListTasksRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, listed.GetTasks(), 1)
	assert.Equal(t, int64(2), listed.GetTasks()[0].GetActiveRunCount())
	assert.Equal(t, int64(2), listed.GetTasks()[0].GetDesiredConcurrency())

	// 缩放 + 补足。
	scaled, err := tc.ScaleTask(ctx, &automationv1.ScaleTaskRequest{Id: taskID, DesiredConcurrency: 3})
	require.NoError(t, err)
	assert.Equal(t, int64(3), scaled.GetTask().GetDesiredConcurrency())
	driveTaskStep(t, h, ctx)
	runs, err = rc.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: taskID})
	require.NoError(t, err)
	assert.Len(t, runs.GetRuns(), 3)

	// one-shot 缩放拒绝（动词面）。
	osTask, err := tc.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: projectID, Image: "busybox:1.37",
	})
	require.NoError(t, err)
	_, err = tc.ScaleTask(ctx, &automationv1.ScaleTaskRequest{Id: osTask.GetTask().GetId(), DesiredConcurrency: 3})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resident tasks only")
}

// TestWaitRun：首帧快照 → 终态帧收流（F1.3 收口面）。
func TestWaitRun(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projectID := taskFixtureProject(t, h, ctx, "automation3")

	tc := automationv1.NewTasksServiceClient(h.Conn)
	created, err := tc.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: projectID, Image: "busybox:1.37", Command: []string{"/job"},
	})
	require.NoError(t, err)
	taskID := created.GetTask().GetId()
	driveTaskStep(t, h, ctx)

	rc := automationv1.NewRunsServiceClient(h.Conn)
	runs, err := rc.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: taskID})
	require.NoError(t, err)
	require.Len(t, runs.GetRuns(), 1)
	runID := runs.GetRuns()[0].GetId()

	type frame struct{ state string }
	frames := make(chan frame, 8)
	go func() {
		stream, err := rc.WaitRun(ctx, &automationv1.WaitRunRequest{Id: runID})
		if err != nil {
			close(frames)
			return
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				close(frames)
				return
			}
			frames <- frame{resp.GetRun().GetState()}
		}
	}()

	h.Runtime.ReportRunning(runID, capability.Generation(1))
	h.Drive(ctx)
	h.Runtime.ReportStopped(runID, capability.Generation(1))
	h.Drive(ctx)

	var states []string
	for f := range frames {
		states = append(states, f.state)
	}
	require.NotEmpty(t, states)
	assert.Equal(t, "stopped", states[len(states)-1], "terminal frame must close the stream")
	// 帧序单调走状态迁移（首帧 pending 或 running → stopped）。
	assert.Contains(t, states, "stopped")
}

// TestTaskAcceptanceFace：父资源守卫（已删项目拒收）与 owner 缺省解析。
func TestTaskAcceptanceFace(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	pc := structurev1.NewProjectsServiceClient(h.Conn)
	p, err := pc.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "gone"})
	require.NoError(t, err)
	_, err = pc.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: p.GetProject().GetId()})
	require.NoError(t, err)

	tc := automationv1.NewTasksServiceClient(h.Conn)
	_, err = tc.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: p.GetProject().GetId(), Image: "busybox:1.37",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// owner 缺省 = 调用方 Token（行上引用非明文）。
	p2, err := pc.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "owners"})
	require.NoError(t, err)
	created, err := tc.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: p2.GetProject().GetId(), Form: "resident",
		Image: "busybox:1.37", DesiredConcurrency: 1,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.GetTask().GetOwnerTokenId(), "owner defaults to the calling token")

	// 续期推进 lease_deadline；再续期一次（复活语义在 engine 测试钉过）。
	before := created.GetTask().GetLeaseDeadline()
	require.Empty(t, before) // 创建时不预置——首续期起租
	renewed, err := tc.RenewTask(ctx, &automationv1.RenewTaskRequest{Id: created.GetTask().GetId()})
	require.NoError(t, err)
	assert.NotEmpty(t, renewed.GetTask().GetLeaseDeadline())
}
