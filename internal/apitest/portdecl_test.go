package apitest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestDeployImagePortDeclaration（F3.5）：直投/上传形态的端口声明——
// 声明端口 = Route-facing：进程携带 ports 且挂靠项目 default 网（Proxy
// 可达性——static Route 404 的可达性半边）。互斥执法：compose 自带
// ports 声明面，port/protocol 携带即拒；protocol 无 port 拒；值域外拒。
func TestDeployImagePortDeclaration(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "port-decl"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)

	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), Image: "nginx:1.27", Port: 8080, Protocol: "h2c",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(h.Runtime.Calls()) > 0
	}, 2e9, 1e7)
	calls := h.Runtime.Calls()
	w := calls[0].Spec["web"]
	require.Len(t, w.Ports, 1)
	assert.Equal(t, int32(8080), w.Ports[0].Port)
	assert.Equal(t, capability.ProtocolH2C, w.Ports[0].Protocol)
	assert.Equal(t, []string{"default"}, w.Networks, "a declared port implies project default network attachment")

	// L1 观测 → L3 观察窗过钟 → succeeded（apitest_test.go 同款推进序）。
	h.Runtime.ReportRunning(app.GetApp().GetId()+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "observing"
	}, 3e9, 1e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "succeeded"
	}, 3e9, 1e7)

	// 互斥与值域执法。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), ComposeYaml: "services:\n  web:\n    image: nginx:1.27\n", Port: 8080,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port and protocol are for image or upload deploys")

	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), Image: "nginx:1.27", Protocol: "h2c",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protocol is only meaningful together with a declared port")

	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), Image: "nginx:1.27", Port: 8080, Protocol: "grpc",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `protocol "grpc" must be http, h2c or tcp`)

	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), Image: "nginx:1.27", Port: 70000,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

// TestDeployUploadPortDeclaration：上传形态同款端口声明（static builder
// 是 canonical 消费者——caddy 8080 伺服面）。FakeBuilder 链路与 upload
// 生命周期测试同款；断言焦点是 ports/networks 落进期望集。
func TestDeployUploadPortDeclaration(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "port-decl-up"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "static"})
	require.NoError(t, err)

	up, err := uploadTar(ctx, builds, proj.GetProject().GetId(), buildFixtureTar(), 8)
	require.NoError(t, err)

	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), UploadId: up.GetId(), Builder: "static", Port: 8080,
	})
	require.NoError(t, err)

	succeeded := false
	for i := 0; i < 30 && !succeeded; i++ {
		h.Drive(ctx)
		h.Runtime.ReportRunning(app.GetApp().GetId()+"-web", 1)
		h.Clock.Advance(120 * time.Second)
		h.Drive(ctx)
		got, gerr := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		if gerr == nil && got.GetDeployment().GetState() == "succeeded" {
			succeeded = true
		}
	}
	require.True(t, succeeded, "static upload deploy with a port declaration must converge")

	calls := h.Runtime.Calls()
	w := calls[len(calls)-1].Spec["web"]
	require.Len(t, w.Ports, 1)
	assert.Equal(t, int32(8080), w.Ports[0].Port)
	assert.Equal(t, capability.ProtocolHTTP, w.Ports[0].Protocol, "protocol defaults to http")
	assert.Equal(t, []string{"default"}, w.Networks)
}
