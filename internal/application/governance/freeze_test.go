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
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/freeze"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	buildrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	databaserepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	freezerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/freeze"
	githubapprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/githubapp"
	hookrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/hook"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	networkpeerrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/networkpeer"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	routerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/route"
	runrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/run"
	schedulerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/schedule"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	taskrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/task"
	volumerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// testGuard 构造带真 repo 的执法器（hermetic 夹具可引 persistence——生产
// 构造面在组合根，P1b C 批；freeze repo 与 anchor 归属解析图全量真件）。
func testGuard(db state.DB, clock state.Clock) *FreezeGuard {
	return NewFreezeGuard(db, freezerepo.New(clock, db.Runner()), anchor.New(anchor.Repos{
		Projects: projectrepo.New(clock, db.Runner()), Apps: apprepo.New(clock, db.Runner()), Tasks: taskrepo.New(clock, db.Runner()),
		Schedules: schedulerepo.New(clock, db.Runner()), Networks: networkrepo.New(clock, db.Runner()), Peers: networkpeerrepo.New(clock, db.Runner()),
		Hooks: hookrepo.New(clock, db.Runner()), Routes: routerepo.New(clock, db.Runner()), Databases: databaserepo.New(clock, db.Runner()),
		Volumes: volumerepo.New(clock, db.Runner()), Runs: runrepo.New(clock, db.Runner()), Deployments: deploymentrepo.New(clock, db.Runner()),
		Builds: buildrepo.New(clock, db.Runner()), GitHubApps: githubapprepo.New(clock, db.Runner()),
	}), slog.New(slog.DiscardHandler))
}

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
	ae, ok := apperrstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_CHANGE_FROZEN", ae.Code())
	assert.Contains(t, ae.Message(), reason)
}

const tTeamA = "01JD0TEAMA0000000000000000"

func TestFreezeGuardTeamAxis(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()

	// 夹具：Team A（project/app/task/network/peer）与 Team B（project）。
	projects, apps, tasks := projectrepo.New(clock, db.Runner()), apprepo.New(clock, db.Runner()), taskrepo.New(clock, db.Runner())
	networks, peers, freezes := networkrepo.New(clock, db.Runner()), networkpeerrepo.New(clock, db.Runner()), freezerepo.New(clock, db.Runner())
	const (
		pidA    = "01JD0PROJA0000000000000000"
		pidB    = "01JD0PROJB0000000000000000"
		aidA    = "01JD0APPA00000000000000000"
		tidA    = "01JD0TASKA0000000000000000"
		nidA    = "01JD0NETWA0000000000000000"
		peerA   = "01JD0PEERA0000000000000000"
		reasonA = "upgrade window"
	)
	require.NoError(t, projects.Create(ctx, &project.Project{ID: pidA, TeamID: tTeamA, Name: "alpha"}))
	require.NoError(t, projects.Create(ctx, &project.Project{ID: pidB, TeamID: "01JD0TEAMB0000000000000000", Name: "beta"}))
	require.NoError(t, apps.Create(ctx, &app.App{ID: aidA, ProjectID: pidA, Name: "web"}))
	require.NoError(t, tasks.Create(ctx, &task.Task{
		ID: tidA, ProjectID: pidA, Form: task.FormResident, State: task.StateActive,
		Spec: []byte(`{}`), DesiredConcurrency: 1, DNSName: "x",
	}))
	require.NoError(t, networks.Create(ctx, &network.Network{ID: nidA, ProjectID: pidA, Name: "net"}))
	require.NoError(t, peers.Create(ctx, &networkpeer.Peer{
		ID: peerA, NetworkID: nidA, PeerProjectID: pidB, State: networkpeer.StatePending,
	}))

	g := testGuard(db, clock)
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
	require.NoError(t, freezes.Create(ctx, &freeze.Freeze{ID: "01JDQFRZA00000000000000000", TeamID: tTeamA, Reason: reasonA}))
	wantFrozen(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidA, Name: "x"}), reasonA)
	wantFrozen(t, call(t, g, createProj, &structurev1.CreateProjectRequest{Name: "x", TeamId: tTeamA}), reasonA)
	wantFrozen(t, call(t, g, scaleTask, &automationv1.ScaleTaskRequest{Id: tidA}), reasonA)
	wantFrozen(t, call(t, g, approvePr, &structurev1.ApproveNetworkPeerRequest{Id: peerA}), reasonA)
	require.NoError(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidB, Name: "x"}))
	require.NoError(t, call(t, g, stopTask, &automationv1.StopTaskRequest{Id: tidA}), "stop family is exempt during a freeze")

	// 全局冻结行与 Team 行并存：B 域也拒（全局命中）。
	require.NoError(t, freezes.Create(ctx, &freeze.Freeze{ID: "01JDQFRZG00000000000000000", TeamID: "", Reason: "platform maintenance"}))
	wantFrozen(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidB, Name: "x"}), "platform maintenance")

	// lift 全局行后：B 恢复、A 仍冻（Team 行还在）。
	require.NoError(t, freezes.Lift(ctx, "01JDQFRZG00000000000000000"))
	require.NoError(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidB, Name: "x"}))
	wantFrozen(t, call(t, g, createApp, &structurev1.CreateAppRequest{ProjectId: pidA, Name: "x"}), reasonA)
}

func TestFreezeGuardPassThroughOnMissingRows(t *testing.T) {
	db, clock := statetest.New(t)
	g := testGuard(db, clock)

	// 行不存在（task/app/peer 未知）：放行交受理位拒绝；非封禁面动词放行。
	require.NoError(t, call(t, g, "/fleetly.automation.v1.TasksService/ScaleTask",
		&automationv1.ScaleTaskRequest{Id: "01JD0MIA00000000000000000"}))
	require.NoError(t, call(t, g, "/fleetly.automation.v1.TasksService/GetTask",
		&automationv1.GetTaskRequest{Id: "01JD0MIA00000000000000000"}))
	// 空寻址字段放行（受理位给 E_INVALID_ARGUMENT）。
	require.NoError(t, call(t, g, "/fleetly.structure.v1.AppsService/CreateApp",
		&structurev1.CreateAppRequest{ProjectId: "", Name: "x"}))
}
