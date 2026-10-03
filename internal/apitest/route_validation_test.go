package apitest_test

// Route 受理面校验与全局唯一（安全批 P0）：host/path 原样内插 traefik
// 规则（Host(`%s`)），反引号等元字符可注入路由；host 命名空间是平台级
// 资产——唯一约束从项目内升级为全局（00016 迁移），跨项目同 host+path
// 第二次 Create 拒绝（此前会让 Edge 同名 router 互覆/劫持）。

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestCreateRouteHostPathValidation：非法 host/path 形态在受理位拒绝。
func TestCreateRouteHostPathValidation(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	routes := edgev1.NewRoutesServiceClient(h.Conn)

	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "routeguard"})
	require.NoError(t, err)
	a, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: p.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	projectID, appID := p.GetProject().GetId(), a.GetApp().GetId()

	illegalHosts := []string{
		"",                               // 空
		"`evil.sslip.io`",                // 反引号注入（traefik 规则定界符）
		"un_der.sslip.io",                // 下划线
		"spa ce.sslip.io",                // 空白
		"https://evil.example",           // scheme 前缀
		"do..uble.example",               // 空标签
		"*evil.example",                  // 通配符不成标签
		strings.Repeat("ab.", 128) + "c", // 总长超 253
	}
	for _, host := range illegalHosts {
		_, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
			ProjectId: projectID, Host: host, AppId: appID, Process: "web", Port: 8080,
		})
		require.Error(t, err, "host %q must be rejected", host)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "host %q", host)
		assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT", "host %q", host)
	}

	illegalPaths := []string{
		"no-leading-slash",
		"/bad`tick",  // 反引号注入
		"/bad space", // 空白
		"/bad\\slash",
		"/" + strings.Repeat("a", 1025), // 超长
	}
	for _, path := range illegalPaths {
		_, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
			ProjectId: projectID, Host: "ok.sslip.io", Path: path, AppId: appID, Process: "web", Port: 8080,
		})
		require.Error(t, err, "path %q must be rejected", path)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "path %q", path)
	}

	// 合法形态照常受理（含通配前缀与 IP 形态 host；空 path）。
	for _, host := range []string{"*.apps.sslip.io", "10.124.0.3", "shop.127.0.0.1.sslip.io"} {
		_, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
			ProjectId: projectID, Host: host, AppId: appID, Process: "web", Port: 8080,
		})
		require.NoError(t, err, "host %q must be accepted", host)
	}
}

// TestRouteHostGloballyUnique：跨项目同 host+path 第二次 Create 拒绝
// （E_ALREADY_EXISTS；唯一约束 00016 已升级为全局——此前跨项目共存会让
// traefik 同名 router 互覆/劫持）。项目内撞名同形拒绝、软删让位复用不变。
func TestRouteHostGloballyUnique(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	routes := edgev1.NewRoutesServiceClient(h.Conn)

	const host = "shared.guard.test"
	var appIDs []string
	var projectIDs []string
	for _, name := range []string{"first", "second"} {
		p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: name})
		require.NoError(t, err)
		a, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: p.GetProject().GetId(), Name: "web"})
		require.NoError(t, err)
		projectIDs = append(projectIDs, p.GetProject().GetId())
		appIDs = append(appIDs, a.GetApp().GetId())
	}

	// 第一项目占用 host。
	_, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: projectIDs[0], Host: host, AppId: appIDs[0], Process: "web", Port: 8080,
	})
	require.NoError(t, err)

	// 跨项目同 host → E_ALREADY_EXISTS（安全批核心回归面）。
	_, err = routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: projectIDs[1], Host: host, AppId: appIDs[1], Process: "web", Port: 8080,
	})
	require.Error(t, err, "a second project claiming the same host must be rejected")
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	assert.Equal(t, "E_ALREADY_EXISTS", appErrCode(t, err))

	// 同项目内撞 host+path 同形拒绝；不同 path 可并存。
	_, err = routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: projectIDs[0], Host: host, AppId: appIDs[0], Process: "web", Port: 8081,
	})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	_, err = routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: projectIDs[0], Host: host, Path: "/api", AppId: appIDs[0], Process: "web", Port: 8080,
	})
	require.NoError(t, err, "a distinct path on the same host must coexist")

	// 软删让位：释放后跨项目可重新占用（partial index 的 tombstone 语义）。
	list, err := routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{ProjectId: projectIDs[0]})
	require.NoError(t, err)
	var routeID string
	for _, r := range list.GetRoutes() {
		if r.GetHost() == host && r.GetPath() == "" {
			routeID = r.GetId()
		}
	}
	require.NotEmpty(t, routeID)
	_, err = routes.DeleteRoute(ctx, &edgev1.DeleteRouteRequest{Id: routeID})
	require.NoError(t, err)
	_, err = routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: projectIDs[1], Host: host, AppId: appIDs[1], Process: "web", Port: 8080,
	})
	require.NoError(t, err, "a tombstoned route must release the host for re-claim")
}
