package apitest_test

// API 面全链冒烟：真 engine + 真 API + bufconn（Deploy 归一化 → 冻结 →
// admission → 状态机驱动一条龙）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

func TestDeployEndToEnd(t *testing.T) {
	h := apitest.New(t)
	ctx := context.Background()
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)

	// 镜像直投部署。
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), Image: "nginx:1.27",
	})
	require.NoError(t, err)
	assert.Equal(t, "queued", dep.GetDeployment().GetState())

	// 引擎推进到 releasing（假 runtime Ensure 生效）。
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == string(deployment.StateReleasing)
	}, 2e9, 1e7)
	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, "nginx:1.27", calls[0].Spec["web"].Image)

	// L1 观测 → succeeded。
	h.Runtime.ReportRunning(app.GetApp().GetId()+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == string(deployment.StateObserving)
	}, 2e9, 1e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == string(deployment.StateSucceeded)
	}, 2e9, 1e7)
}

func TestComposeDeployNormalizes(t *testing.T) {
	h := apitest.New(t)
	ctx := context.Background()
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)

	proj, _ := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	app, _ := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "compose"})

	compose := "services:\n  web:\n    image: nginx:1.27\n    ports:\n      - \"8080\"\n    deploy:\n      replicas: 2\n"
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: app.GetApp().GetId(), ComposeYaml: compose})
	require.NoError(t, err)

	revs, err := revisions.ListRevisions(ctx, &deliveryv1.ListRevisionsRequest{AppId: app.GetApp().GetId()})
	require.NoError(t, err)
	require.Len(t, revs.GetRevisions(), 1)

	// 同内容重复部署：Revision 内容寻址复用。
	dep2, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: app.GetApp().GetId(), ComposeYaml: compose})
	require.NoError(t, err)
	revs2, _ := revisions.ListRevisions(ctx, &deliveryv1.ListRevisionsRequest{AppId: app.GetApp().GetId()})
	require.Len(t, revs2.GetRevisions(), 1, "identical spec reuses the frozen revision")
	assert.NotEqual(t, dep.GetDeployment().GetId(), dep2.GetDeployment().GetId(), "but deployments are distinct")
}

func TestComposeRejectedField(t *testing.T) {
	h := apitest.New(t)
	ctx := context.Background()
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, _ := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "p"})
	app, _ := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "a"})
	_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId:       app.GetApp().GetId(),
		ComposeYaml: "services:\n  web:\n    image: nginx\n    privileged: true\n",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported field")
}

func TestNodesAndEnroll(t *testing.T) {
	h := apitest.New(t)
	ctx := context.Background()
	nodes := runtimev1.NewNodesServiceClient(h.Conn)

	kit, err := nodes.EnrollNode(ctx, &runtimev1.EnrollNodeRequest{})
	require.NoError(t, err)
	assert.Contains(t, kit.GetJoinCommand(), "docker swarm join")

	// 节点对账在 managed 节拍落缓存（启动后一个 tick 内）。
	require.Eventually(t, func() bool {
		list, err := nodes.ListNodes(ctx, &runtimev1.ListNodesRequest{})
		return err == nil && len(list.GetNodes()) > 0
	}, 3e9, 2e7)
}
