package apitest_test

// Builder strategy intake 测试（F1.14，ADR-0032 验收锚）：upload+builder
// 全链（FakeBuilder 捕获路由名与 strategy 载荷）、builder 面互斥执法
//（image/compose 形态携带即拒、旗标与 builder 错配拒、railpack 缺版本拒）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestDeployUploadBuilderStrategies：三 strategy 的 upload 部署全链——
// Revision 冻结 strategy 声明，构建输入按路由名携带 strategy 载荷。
func TestDeployUploadBuilderStrategies(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "builder-strategies"})
	require.NoError(t, err)
	projID := proj.GetProject().GetId()

	up, err := uploadTar(owner, builds, projID, buildFixtureTar(), 8)
	require.NoError(t, err)

	cases := []struct {
		name   string
		req    func(appID string) *deliveryv1.DeployRequest
		verify func(t *testing.T, callIdx int)
	}{
		{
			name: "railpack carries the pin end to end",
			req: func(appID string) *deliveryv1.DeployRequest {
				return &deliveryv1.DeployRequest{AppId: appID, UploadId: up.GetId(), Builder: "railpack", RailpackVersion: "0.39.0"}
			},
			verify: func(t *testing.T, _ int) {
				calls := h.Builder.Calls()
				last := calls[len(calls)-1]
				assert.Equal(t, "railpack", last.Builder)
				require.NotNil(t, last.Railpack)
				assert.Equal(t, "0.39.0", last.Railpack.PinnedVersion)
			},
		},
		{
			name: "static carries the output dir",
			req: func(appID string) *deliveryv1.DeployRequest {
				return &deliveryv1.DeployRequest{AppId: appID, UploadId: up.GetId(), Builder: "static", OutputDir: "dist"}
			},
			verify: func(t *testing.T, _ int) {
				calls := h.Builder.Calls()
				last := calls[len(calls)-1]
				assert.Equal(t, "static", last.Builder)
				require.NotNil(t, last.Static)
				assert.Equal(t, "dist", last.Static.OutputDir)
			},
		},
		{
			name: "dockerfile stays the default track",
			req: func(appID string) *deliveryv1.DeployRequest {
				return &deliveryv1.DeployRequest{AppId: appID, UploadId: up.GetId()}
			},
			verify: func(t *testing.T, _ int) {
				calls := h.Builder.Calls()
				last := calls[len(calls)-1]
				assert.Equal(t, "dockerfile", last.Builder)
				assert.Nil(t, last.Railpack)
				assert.Nil(t, last.Static)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 每 case 独立 App：观测代数锚定首个部署（连续部署同 App 需要
			// 推进的 generation 观测，与本测焦点无关）。
			a, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projID, Name: "web-" + tc.name[:4]})
			require.NoError(t, err)
			appID := a.GetApp().GetId()
			dep, err := deployments.Deploy(owner, tc.req(appID))
			require.NoError(t, err)
			depID := dep.GetDeployment().GetId()
			succeeded := false
			for i := 0; i < 30 && !succeeded; i++ {
				h.Drive(owner)
				h.Runtime.ReportRunning(appID+"-web", 1)
				h.Clock.Advance(120 * time.Second)
				h.Drive(owner)
				got, gerr := deployments.GetDeployment(owner, &deliveryv1.GetDeploymentRequest{Id: depID})
				if gerr == nil && got.GetDeployment().GetState() == "succeeded" {
					succeeded = true
				}
			}
			if !succeeded {
				got, _ := deployments.GetDeployment(owner, &deliveryv1.GetDeploymentRequest{Id: depID})
				t.Logf("final state: %s error: %s", got.GetDeployment().GetState(), got.GetDeployment().GetError())
			}
			require.True(t, succeeded, "builder-strategy deploy must converge to succeeded")
			tc.verify(t, len(h.Builder.Calls()))
		})
	}
}

// TestDeployBuilderFlagEnforcement：builder 面互斥与配对执法。
func TestDeployBuilderFlagEnforcement(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "builder-flags"})
	require.NoError(t, err)
	projID := proj.GetProject().GetId()
	app, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projID, Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()
	up, err := uploadTar(owner, builds, projID, buildFixtureTar(), 8)
	require.NoError(t, err)

	invalid := codes.InvalidArgument
	for name, req := range map[string]*deliveryv1.DeployRequest{
		"builder flags on image deploy":     {AppId: appID, Image: "nginx:1.27", Builder: "railpack"},
		"builder flags on compose deploy":   {AppId: appID, ComposeYaml: "services: {web: {image: nginx:1.27}}", Builder: "static"},
		"railpack without version":          {AppId: appID, UploadId: up.GetId(), Builder: "railpack"},
		"railpack_version without railpack": {AppId: appID, UploadId: up.GetId(), RailpackVersion: "0.39.0"},
		"output_dir without static":         {AppId: appID, UploadId: up.GetId(), OutputDir: "dist"},
		"dockerfile with railpack builder":  {AppId: appID, UploadId: up.GetId(), Builder: "railpack", RailpackVersion: "0.39.0", Dockerfile: "Dockerfile"},
		"unknown builder":                   {AppId: appID, UploadId: up.GetId(), Builder: "makeimg"},
		"bad pin form":                      {AppId: appID, UploadId: up.GetId(), Builder: "railpack", RailpackVersion: "latest"},
	} {
		_, err := deployments.Deploy(owner, req)
		require.Error(t, err, name)
		assert.Equal(t, invalid, status.Code(err), "%s: %v", name, err)
	}
}
