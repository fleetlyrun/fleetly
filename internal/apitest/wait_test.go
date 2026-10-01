package apitest_test

// 等待原语全链测试（F1.3，架构 §7）：事件流过滤实现的等待——首帧快照、
// 状态迁移帧、终态帧收流；终态行即入即收；不存在行 E_NOT_FOUND。

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// waitFixtureApp 建项目 + App，返回 app ID。
func waitFixtureApp(t *testing.T, h *apitest.Harness, ctx context.Context) string {
	t.Helper()
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "wait"})
	require.NoError(t, err)
	a, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: p.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	return a.GetApp().GetId()
}

// waitCodeOf 提取 apperr 信封码。
func waitCodeOf(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok)
	if e, ok := apperr.FromGRPCStatus(st); ok {
		return e.Code()
	}
	return "grpc:" + st.Code().String()
}

// deployToSucceeded 走完一条镜像直投部署（手动驱动），返回 deployment ID。
func deployToSucceeded(t *testing.T, h *apitest.Harness, appID string) string {
	t.Helper()
	ctx := context.Background()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	d, err := dc.Deploy(sdk.WithToken(ctx, h.Token), &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})
	require.NoError(t, err)
	for i := 0; i < 40; i++ {
		h.Runtime.ReportRunning(appID+"-web", capability.Generation(d.GetDeployment().GetGeneration()))
		h.Drive(ctx)
		h.Clock.Advance(120 * time.Second)
		h.Drive(ctx)
		got, err := dc.GetDeployment(sdk.WithToken(ctx, h.Token), &deliveryv1.GetDeploymentRequest{Id: d.GetDeployment().GetId()})
		require.NoError(t, err)
		if got.GetDeployment().GetState() == "succeeded" {
			return d.GetDeployment().GetId()
		}
	}
	t.Fatal("deployment did not converge to succeeded")
	return ""
}

func TestWaitDeployment(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	appID := waitFixtureApp(t, h, ctx)

	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	dep, err := dc.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27", ProcessName: "web"})
	require.NoError(t, err)
	depID := dep.GetDeployment().GetId()

	// 开流后驱动状态机：帧只走观察到的状态迁移，终态帧后流关闭。
	stream, err := dc.WaitDeployment(ctx, &deliveryv1.WaitDeploymentRequest{DeploymentId: depID})
	require.NoError(t, err)
	var states []string
	done := make(chan error, 1)
	go func() {
		for {
			frame, err := stream.Recv()
			if err != nil {
				done <- err
				return
			}
			states = append(states, frame.GetDeployment().GetState())
		}
	}()

	driveCtx := context.Background()
	converged := false
	for i := 0; i < 40 && !converged; i++ {
		h.Runtime.ReportRunning(appID+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
		h.Drive(driveCtx)
		h.Clock.Advance(120 * time.Second)
		h.Drive(driveCtx)
		got, err := dc.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		require.NoError(t, err)
		converged = got.GetDeployment().GetState() == "succeeded"
	}
	require.True(t, converged, "deployment must converge for the wait stream to close")

	// 终态已落行：流的轮询拍（250ms）拾取事件后发终态帧并关流。
	select {
	case err := <-done:
		require.ErrorIs(t, err, io.EOF, "the stream must close after the terminal frame")
		require.NotEmpty(t, states)
		assert.Equal(t, "succeeded", states[len(states)-1], "the last frame is the terminal state")
		assert.NotContains(t, states[:len(states)-1], "succeeded", "no frames after terminal")
	case <-time.After(5 * time.Second):
		t.Fatal("wait stream did not close on the terminal state")
	}
}

// 终态行即入即收：单帧 succeeded 后关流（等待不依赖事件史）。
func TestWaitDeploymentTerminalAtEntry(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	appID := waitFixtureApp(t, h, ctx)

	depID := deployToSucceeded(t, h, appID)
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	stream, err := dc.WaitDeployment(ctx, &deliveryv1.WaitDeploymentRequest{DeploymentId: depID})
	require.NoError(t, err)
	frame, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, "succeeded", frame.GetDeployment().GetState())
	_, err = stream.Recv()
	assert.ErrorIs(t, err, io.EOF, "a terminal row closes the stream after one frame")
}

// 不存在的行 → E_NOT_FOUND。
func TestWaitDeploymentNotFound(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	stream, err := dc.WaitDeployment(ctx, &deliveryv1.WaitDeploymentRequest{DeploymentId: "01JD0MISSING00000000000000X"})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, "E_NOT_FOUND", waitCodeOf(t, err))
}
