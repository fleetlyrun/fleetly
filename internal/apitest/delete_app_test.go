package apitest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// ADR-0023 回归（N0 修复批 C2）：DeleteApp 收口语义——活跃部署拒绝
// （E_CONFLICT）、终态后删除成功、引用路由随删、tombstone 生效（同项目
// 同名可重建、旧 ID 不复用）。
func TestDeleteAppSemantics(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "teardown"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	// 活跃部署在 → E_CONFLICT（先 cancel/等终态的提示在 suggestion）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_CONFLICT")
	require.Contains(t, err.Error(), "active deployment")

	// 取消到终态 → 删除成功。
	list, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	_, err = deployments.CancelDeployment(ctx, &deliveryv1.CancelDeploymentRequest{Id: list.GetDeployments()[0].GetId()})
	require.NoError(t, err)

	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.NoError(t, err)

	// Runtime.Remove 已被触发（收口）。
	require.NotEmpty(t, h.Runtime.Removed(), "delete must tear down runtime carriers")

	// tombstone：列表无此 App；同项目同名可重建（新 ULID，ID 永不复用）。
	listed, err := apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	for _, a := range listed.GetApps() {
		require.NotEqual(t, appID, a.GetId(), "deleted app must not be listed")
	}
	again, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	require.NotEqual(t, appID, again.GetApp().GetId(), "a new app gets a new id (ids are never reused)")

	// 旧 ID 再删 → E_NOT_FOUND。
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
}
