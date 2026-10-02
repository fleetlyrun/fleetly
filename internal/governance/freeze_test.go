package governance

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/freeze"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// call 经拦截器跑一次 handler（fullMethod 决定执法面）。
func call(t *testing.T, g *FreezeGuard, method string, req proto.Message) error {
	t.Helper()
	called := false
	_, err := g.Unary()(context.Background(), req, &grpc.UnaryServerInfo{FullMethod: method},
		func(context.Context, any) (any, error) { called = true; return nil, nil })
	if err == nil {
		require.True(t, called, "handler must run when not frozen")
	}
	return err
}

// wantFrozen 断言拒绝是 E_CHANGE_FROZEN 且信封可见 reason。
func wantFrozen(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_CHANGE_FROZEN", ae.Code())
	assert.Contains(t, ae.Message(), reason)
}

const tTeamA = "01JD0TEAMA0000000000000000"

func TestFreezeGuardTeamAxis(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	run := db.Runner()

	// 夹具：Team A（project/app/task/network/peer）与 Team B（project）。
	projects, apps, tasks := project.New(clock), app.New(clock), task.New(clock)
	networks, peers, freezes := networkrepo.New(clock), networkpeer.New(clock), freeze.New(clock)
	const (
		pidA    = "01JD0PROJA0000000000000000"
		pidB    = "01JD0PROJB0000000000000000"
		aidA    = "01JD0APPA00000000000000000"
		tidA    = "01JD0TASKA0000000000000000"
		nidA    = "01JD0NETWA0000000000000000"
		peerA   = "01JD0PEERA0000000000000000"
		reasonA = "upgrade window"
	)
	require.NoError(t, projects.Create(ctx, run, &project.Project{ID: pidA, TeamID: tTeamA, Name: "alpha"}))
	require.NoError(t, projects.Create(ctx, run, &project.Project{ID: pidB, TeamID: "01JD0TEAMB0000000000000000", Name: "beta"}))
	require.NoError(t, apps.Create(ctx, run, &app.App{ID: aidA, ProjectID: pidA, Name: "web"}))
	require.NoError(t, tasks.Create(ctx, run, &task.Task{
		ID: tidA, ProjectID: pidA, Form: task.FormResident, State: task.StateActive,
		Spec: []byte(`{}`), DesiredConcurrency: 1, DNSName: "x",
	}))
	require.NoError(t, networks.Create(ctx, run, &networkrepo.Network{ID: nidA, ProjectID: pidA, Name: "net"}))
	require.NoError(t, peers.Create(ctx, run, &networkpeer.Peer{
		ID: peerA, NetworkID: nidA, PeerProjectID: pidB, State: networkpeer.StatePending,
	}))

	g := NewFreezeGuard(db, slog.New(slog.DiscardHandler))
	const (
		createApp  = "/fleetly.structure.v1.AppsService/CreateApp"
		createProj = "/fleetly.structure.v1.ProjectsService/CreateProject"
		scaleTask  = "/fleetly.automation.v1.TasksService/ScaleTask"
		approvePr  = "/fleetly.structure.v1.NetworksService/ApproveNetworkPeer"
		stopTask   = "/fleetly.automation.v1.TasksService/StopTask"
	)

	// 冻结前：A/B 都放行；寻址失败（未知 project）放行（受理位拒绝）。
	require.NoError(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidA, Name: "x"}))
	require.NoError(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: "01JD0MIA00000000000000000", Name: "x"}))

	// Team A 冻结：A 域动词拒（reason 可见），B 域放行，豁免面放行。
	require.NoError(t, freezes.Create(ctx, run, &freeze.Freeze{ID: "01JDQFRZA00000000000000000", TeamID: tTeamA, Reason: reasonA}))
	wantFrozen(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidA, Name: "x"}), reasonA)
	wantFrozen(t, call(t, g, createProj, &structurev1.CreateProjectRequest{Name: "x", TeamId: tTeamA}), reasonA)
	wantFrozen(t, call(t, g, scaleTask, &automationv1.ScaleTaskRequest{Id: tidA}), reasonA)
	wantFrozen(t, call(t, g, approvePr, &structurev1.ApproveNetworkPeerRequest{Id: peerA}), reasonA)
	require.NoError(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidB, Name: "x"}))
	require.NoError(t, call(t, g, stopTask, &automationv1.StopTaskRequest{Id: tidA}), "stop family is exempt during a freeze")

	// 全局冻结行与 Team 行并存：B 域也拒（全局命中）。
	require.NoError(t, freezes.Create(ctx, run, &freeze.Freeze{ID: "01JDQFRZG00000000000000000", TeamID: "", Reason: "platform maintenance"}))
	wantFrozen(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidB, Name: "x"}), "platform maintenance")

	// lift 全局行后：B 恢复、A 仍冻（Team 行还在）。
	require.NoError(t, freezes.Lift(ctx, run, "01JDQFRZG00000000000000000"))
	require.NoError(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidB, Name: "x"}))
	wantFrozen(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidA, Name: "x"}), reasonA)
}

func TestFreezeGuardPassThroughOnMissingRows(t *testing.T) {
	db, _ := statetest.New(t)
	g := NewFreezeGuard(db, slog.New(slog.DiscardHandler))

	// 行不存在（task/app/peer 未知）：放行交受理位拒绝；非封禁面动词放行。
	require.NoError(t, call(t, g, "/fleetly.automation.v1.TasksService/ScaleTask",
		&automationv1.ScaleTaskRequest{Id: "01JD0MIA00000000000000000"}))
	require.NoError(t, call(t, g, "/fleetly.automation.v1.TasksService/GetTask",
		&automationv1.GetTaskRequest{Id: "01JD0MIA00000000000000000"}))
	// 空寻址字段放行（受理位给 E_INVALID_ARGUMENT）。
	require.NoError(t, call(t, g, "/fleetly.structure.v1.AppsService/CreateApp",
		&structurev1.CreateAppRequest{ProjectId: "", Name: "x"}))
}
