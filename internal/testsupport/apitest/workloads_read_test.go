package apitest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// deployAndWaitSucceeded 部署 spec_file 并推进到 succeeded（apitest 惯例
// 序列：releasing → L1 观测 → observing → 时钟推进 → succeeded）。返回
// 部署行（含 generation 锚）。
func deployAndWaitSucceeded(t *testing.T, h *apitest.Harness, ctx context.Context, deployments deliveryv1.DeploymentsServiceClient, appID, body string) *deliveryv1.Deployment {
	t.Helper()
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, SpecFile: body})
	require.NoError(t, err)
	id := dep.GetDeployment().GetId()
	require.Eventually(t, func() bool {
		return len(h.Runtime.Calls()) > 0
	}, 2e9, 1e7)
	h.Runtime.ReportRunning(appID+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
	require.Eventually(t, func() bool {
		got, gerr := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: id})
		return gerr == nil && got.GetDeployment().GetState() == "observing"
	}, 4e9, 1e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, gerr := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: id})
		return gerr == nil && got.GetDeployment().GetState() == "succeeded"
	}, 2e9, 1e7)
	return dep.GetDeployment()
}

// TestGetRevisionVerb（ADR-0071 冻结层）：按 (app, seq) 回读冻结全文——
// Revision 行锚（seq/digest/source_kind）与 AppSpec 本体齐备；未知 seq
// E_NOT_FOUND。冻结同步于 Deploy（部署异步推进不影响读面）。
func TestGetRevisionVerb(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "revget"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "svc"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":2}]}`
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, SpecFile: body})
	require.NoError(t, err)

	got, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.GetRevision().GetSeq())
	assert.NotEmpty(t, got.GetRevision().GetDigest())
	assert.Equal(t, deliveryv1.SourceKind_SOURCE_KIND_IMAGE, got.GetRevision().GetSourceKind())
	require.Len(t, got.GetSpec().GetProcesses(), 1)
	assert.Equal(t, "web", got.GetSpec().GetProcesses()[0].GetName())
	assert.Equal(t, int64(2), got.GetSpec().GetProcesses()[0].GetReplicas())

	// 未知 seq 精确 E_NOT_FOUND。
	_, err = revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: 99})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
}

// TestGetAppWorkloadsVerb（ADR-0071 投影层）：缺省寻址 = 当前生效；显式
// seq 历史重放；**渲染输出与 Runtime Ensure 收到的载体用户字段逐一致**
// （单源 by construction，此断言钉回归）；无成功部署的缺省寻址
// E_NOT_FOUND；GetAppSpec 锚（最新冻结 seq/digest）齐备。
func TestGetAppWorkloadsVerb(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "wlview"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "svc"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	// 冻结未部署：缺省寻址拒绝（无成功基线），显式 seq 也无从渲染。
	_, err = revisions.GetAppWorkloads(ctx, &deliveryv1.GetAppWorkloadsRequest{AppId: appID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")

	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":2,"command":["nginx","-g","daemon off;"],` +
		`"env":{"ROLE":"edge"},"ports":[{"port":8080,"protocol":"PROTOCOL_HTTP"}]}]}`
	deployAndWaitSucceeded(t, h, ctx, deployments, appID, body)

	ensured := h.Runtime.Calls()[len(h.Runtime.Calls())-1]
	dispatched := ensured.Spec["web"]

	view, err := revisions.GetAppWorkloads(ctx, &deliveryv1.GetAppWorkloadsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), view.GetRevision().GetSeq(), "default addressing must resolve the effective revision")
	require.Len(t, view.GetWorkloads(), 1)
	w := view.GetWorkloads()[0]
	// 与 Ensure 下发面的用户字段逐一致（渲染/部署单源回归锚）。
	assert.Equal(t, dispatched.ID, w.GetId())
	assert.Equal(t, dispatched.Image, w.GetImage())
	assert.Equal(t, dispatched.Command, w.GetCommand())
	assert.Equal(t, dispatched.Env, w.GetEnv())
	assert.Equal(t, dispatched.Replicas, w.GetReplicas())
	require.Len(t, w.GetPorts(), 1)
	assert.Equal(t, int32(8080), w.GetPorts()[0].GetPort())

	// 历史重放：replicas 变更后，显式 seq=1 仍渲染旧值。
	body2 := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":4}]}`
	deployAndWaitSucceeded(t, h, ctx, deployments, appID, body2)
	view, err = revisions.GetAppWorkloads(ctx, &deliveryv1.GetAppWorkloadsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Equal(t, int64(2), view.GetRevision().GetSeq())
	assert.Equal(t, int64(4), view.GetWorkloads()[0].GetReplicas())
	view, err = revisions.GetAppWorkloads(ctx, &deliveryv1.GetAppWorkloadsRequest{AppId: appID, Seq: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(2), view.GetWorkloads()[0].GetReplicas(), "explicit seq replays the historical revision")

	// GetAppSpec 锚（ADR-0071 决策 1）：最新冻结的 seq/digest 诚实锚。
	spec, err := apps.GetAppSpec(ctx, &structurev1.GetAppSpecRequest{Id: appID})
	require.NoError(t, err)
	assert.Equal(t, int64(2), spec.GetRevisionSeq())
	assert.NotEmpty(t, spec.GetRevisionDigest())
}

// TestGetAppWorkloadObservationsVerb（ADR-0071 观测层）：在服观测快照——
// WorkloadID/generation（真实 wire-gen 语义面）/image/state；与投影视图
// 互补（投影不带代戳，观测才有）。
func TestGetAppWorkloadObservationsVerb(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	workloads := runtimev1.NewWorkloadsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "wlobs"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "svc"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1}]}`
	dep := deployAndWaitSucceeded(t, h, ctx, deployments, appID, body)

	obs, err := workloads.GetAppWorkloadObservations(ctx, &runtimev1.GetAppWorkloadObservationsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, obs.GetObservations(), 1)
	o := obs.GetObservations()[0]
	assert.Equal(t, appID+"-web", o.GetWorkloadId())
	assert.Equal(t, dep.GetGeneration(), o.GetGeneration())
	assert.Equal(t, "nginx:1.27", o.GetImage())
	assert.Equal(t, "running", o.GetState())

	// 未知 App 精确 E_NOT_FOUND。
	_, err = workloads.GetAppWorkloadObservations(ctx, &runtimev1.GetAppWorkloadObservationsRequest{AppId: "01JD0APP0000000000000MISS"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
}
