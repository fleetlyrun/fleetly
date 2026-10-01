package apitest_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
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
	routes := edgev1.NewRoutesServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "teardown"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	// 引用该 App 的路由（撤路由面的前置）。
	route, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: proj.GetProject().GetId(), Host: "gone.teardown.test",
		AppId: appID, Process: "web", Port: 8000,
	})
	require.NoError(t, err)

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

	// 引用路由随删：列表不再可见。
	routeList, err := routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	for _, r := range routeList.GetRoutes() {
		require.NotEqual(t, route.GetRoute().GetId(), r.GetId(), "routes referencing the app must be withdrawn on delete")
	}

	// tombstone：列表无此 App；同项目同名可重建（新 ULID，ID 永不复用）。
	listed, err := apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	for _, a := range listed.GetApps() {
		require.NotEqual(t, appID, a.GetId(), "deleted app must not be listed")
	}
	// 读面统一口径（N0.1 P1-1）：GetApp 已删 → 404（与 List/Delete 同形）。
	_, err = apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
	// 删后 deploy 拒绝：不重建载体。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")

	again, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	require.NotEqual(t, appID, again.GetApp().GetId(), "a new app gets a new id (ids are never reused)")

	// 旧 ID 再删 → E_NOT_FOUND。
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
}

// 并发删+部署竞态回归（N0.1 P1-1）：预检通过到 tombstone 落账之间受理
// 部署的 TOCTOU 已由最终事务内 ActiveByApp 复查收口（与 Submit 的存活
// 判定互为对偶；单连接事务串行）。任一交错下不变式成立：tombstone 与
// 活跃部署不共存，且不出 E_INTERNAL。
func TestDeleteDeployRaceInvariant(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "race"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	const rounds = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var deployErrs, deleteErrs []error
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
			mu.Lock()
			deployErrs = append(deployErrs, err)
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
			mu.Lock()
			deleteErrs = append(deleteErrs, err)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range deployErrs {
		if err == nil {
			continue
		}
		require.NotContains(t, err.Error(), "E_INTERNAL", "deploy must fail cleanly (not-found/queue-full), never internal")
	}
	for _, err := range deleteErrs {
		if err == nil {
			continue
		}
		require.Contains(t, err.Error(), "E_CONFLICT", "delete must only fail on active deployments")
	}

	// 不变式：删成（GetApp 404）⇒ 该 App 不可能再有活跃部署。
	_, getErr := apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	if getErr == nil {
		return // 删除被拒（活跃部署在）：App 保持可操作，不变式无涉。
	}
	require.Contains(t, getErr.Error(), "E_NOT_FOUND")
	list, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	terminal := map[string]bool{
		"succeeded": true, "failed": true, "cancelled": true, "superseded": true, "rolled-back": true,
	}
	for _, d := range list.GetDeployments() {
		require.True(t, terminal[d.GetState()],
			"tombstoned app must hold no active deployment (state=%s)", d.GetState())
	}
}
