package apitest_test

// Browse 会话契约测试（F3.6，ADR-0051）：受理（回显字段/事件/审计/收敛
// 投影）、面停用拒绝、未就绪拒绝、quota、写档与无执法方言的动态
// databases:write 门、Launcher Ticket 单用途（entry 烧票 302 + 二次 401）、
// ForwardAuth cookie 校验链（authorize 200/401）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// browseHarness 装配带 BrowseConfig 的夹具。
func browseHarness(t *testing.T) *apitest.Harness {
	t.Helper()
	return apitest.NewManualOpts(t, func(o *engine.Options) {
		o.Browse = engine.BrowseConfig{
			HostSuffix: "browse.test", GatewayURL: "http://127.0.0.1:9081", TLSMode: "none",
		}
	})
}

// browseFixture 建 running 状态的库，返回 (harness, ctx, dbID)。
func browseFixture(t *testing.T, engineName string) (*apitest.Harness, context.Context, string) {
	t.Helper()
	h := browseHarness(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx,
		&structurev1.CreateProjectRequest{Name: "browse-shop"})
	require.NoError(t, err)
	db, err := structurev1.NewDatabasesServiceClient(h.Conn).CreateDatabase(ctx,
		&structurev1.CreateDatabaseRequest{ProjectId: proj.GetProject().GetId(), Engine: engineName, Name: "shop"})
	require.NoError(t, err)
	// 状态推到 running（受理前置）。
	h.Drive(ctx)
	h.Runtime.ReportRunning(db.GetDatabase().GetId(), capability.Generation(1))
	h.Drive(ctx)
	return h, ctx, db.GetDatabase().GetId()
}

func TestBrowseDisabledWithoutConfig(t *testing.T) {
	h := apitest.NewManual(t) // 无 BrowseConfig：面停用
	ctx := sdk.WithToken(context.Background(), h.Token)
	_, _, dbID := browseFixtureOn(t, h, "postgres")
	_, err := structurev1.NewDatabasesServiceClient(h.Conn).BrowseDatabase(ctx,
		&structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "E_BROWSE_DISABLED")
}

// browseFixtureOn 是 browseFixture 的在册夹具变体（面停用测试复用）。
func browseFixtureOn(t *testing.T, h *apitest.Harness, engineName string) (*apitest.Harness, context.Context, string) {
	t.Helper()
	ctx := sdk.WithToken(context.Background(), h.Token)
	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx,
		&structurev1.CreateProjectRequest{Name: "browse-flat"})
	require.NoError(t, err)
	db, err := structurev1.NewDatabasesServiceClient(h.Conn).CreateDatabase(ctx,
		&structurev1.CreateDatabaseRequest{ProjectId: proj.GetProject().GetId(), Engine: engineName, Name: "shop"})
	require.NoError(t, err)
	h.Drive(ctx)
	h.Runtime.ReportRunning(db.GetDatabase().GetId(), capability.Generation(1))
	h.Drive(ctx)
	return h, ctx, db.GetDatabase().GetId()
}

func TestBrowseDatabaseAcceptance(t *testing.T) {
	h, ctx, dbID := browseFixture(t, "postgres")
	databases := structurev1.NewDatabasesServiceClient(h.Conn)

	resp, err := databases.BrowseDatabase(ctx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetSessionId())
	assert.Equal(t, "pgweb", resp.GetBrowser())
	assert.True(t, resp.GetReadOnly())
	assert.Equal(t, structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_SESSION, resp.GetEnforcement())
	assert.Equal(t, int32(120), resp.GetExpiresIn())
	assert.Contains(t, resp.GetUrl(), "http://browse-")
	assert.Contains(t, resp.GetUrl(), ".browse.test/v1/browse/entry?session="+resp.GetSessionId()+"&ticket="+resp.GetTicket())

	// 事件 + 审计（受理四件一拍的观测面）。
	names := peerEventNames(t, ctx, h, "database", dbID)
	assert.Contains(t, names, "database.browser_opened")

	// 收敛：browse 载体投影到第五轴 ns（pgweb 钉版镜像 + 只读 URL）。
	h.Drive(ctx)
	found := false
	for _, c := range h.Runtime.Calls() {
		if c.NS.Browse == resp.GetSessionId() {
			found = true
			require.Len(t, c.Spec, 1)
			for _, w := range c.Spec {
				assert.Equal(t, "pgweb", w.Process)
				assert.Contains(t, w.Image, "sosedoff/pgweb:0.17.0@sha256:")
			}
		}
	}
	assert.True(t, found, "browse workload must be ensured under the browse namespace")
}

func TestBrowseDatabaseNotReady(t *testing.T) {
	h := browseHarness(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx,
		&structurev1.CreateProjectRequest{Name: "browse-pending"})
	require.NoError(t, err)
	db, err := structurev1.NewDatabasesServiceClient(h.Conn).CreateDatabase(ctx,
		&structurev1.CreateDatabaseRequest{ProjectId: proj.GetProject().GetId(), Engine: "postgres", Name: "shop"})
	require.NoError(t, err)
	// 不 Drive：状态恒 pending。
	_, err = structurev1.NewDatabasesServiceClient(h.Conn).BrowseDatabase(ctx,
		&structurev1.BrowseDatabaseRequest{DatabaseId: db.GetDatabase().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "E_DATABASE_NOT_READY")
}

// readScopedToken 铸只含 databases:read 的 Token（member 用户 + 自定义
// 角色；动态提权门的测试面）。
func readScopedToken(t *testing.T, h *apitest.Harness, ctx context.Context) context.Context {
	t.Helper()
	roles := identityv1.NewRolesServiceClient(h.Conn)
	role, err := roles.CreateRole(ctx, &identityv1.CreateRoleRequest{Name: "browser", Scopes: []string{"databases:read"}})
	require.NoError(t, err)
	users := identityv1.NewUsersServiceClient(h.Conn)
	bob, err := users.CreateUser(ctx, &identityv1.CreateUserRequest{Name: "bob", RoleId: role.GetRole().GetId()})
	require.NoError(t, err)
	minted, err := identityv1.NewTokensServiceClient(h.Conn).CreateToken(ctx,
		&identityv1.CreateTokenRequest{Name: "bob-browse", RoleId: role.GetRole().GetId(), UserId: bob.GetUser().GetId()})
	require.NoError(t, err)
	return sdk.WithToken(context.Background(), minted.GetSecret())
}

func TestBrowseWriteScopeGate(t *testing.T) {
	h, ctx, dbID := browseFixture(t, "postgres")
	databases := structurev1.NewDatabasesServiceClient(h.Conn)
	readCtx := readScopedToken(t, h, ctx)

	// 只读档：read scope 足够（PG 服务端执法）。
	_, err := databases.BrowseDatabase(readCtx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
	require.NoError(t, err)

	// 写档（read_write=true）：动态要求 databases:write。
	_, err = databases.BrowseDatabase(readCtx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID, ReadWrite: true})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "E_FORBIDDEN")

	// owner 全权档写档可开。
	_, err = databases.BrowseDatabase(ctx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID, ReadWrite: true})
	require.NoError(t, err)
}

func TestBrowseMysqlRequiresWriteForReadOnly(t *testing.T) {
	h, ctx, dbID := browseFixture(t, "mysql")
	databases := structurev1.NewDatabasesServiceClient(h.Conn)
	readCtx := readScopedToken(t, h, ctx)

	// mysql（Adminer 无只读方言）：只读浏览也要求 write（诚实收窄）。
	_, err := databases.BrowseDatabase(readCtx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	// owner 档可开；回显 enforcement=none。
	resp, err := databases.BrowseDatabase(ctx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
	require.NoError(t, err)
	assert.Equal(t, "adminer", resp.GetBrowser())
	assert.Equal(t, structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_NONE, resp.GetEnforcement())
}

func TestBrowseQuota(t *testing.T) {
	h, ctx, dbID := browseFixture(t, "postgres")
	databases := structurev1.NewDatabasesServiceClient(h.Conn)
	for i := 0; i < engine.BrowseMaxSessionsPerTeam; i++ {
		_, err := databases.BrowseDatabase(ctx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
		require.NoError(t, err, "session %d", i)
	}
	_, err := databases.BrowseDatabase(ctx, &structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
}

// TestBrowseEntryTicketAndCookie：entry 烧票（302 + Set-Cookie）→ 二次
// 401（单用途）→ authorize 无 cookie 401 / 有效 cookie 200 / 坏值 401。
func TestBrowseEntryTicketAndCookie(t *testing.T) {
	h, ctx, dbID := browseFixture(t, "postgres")
	resp, err := structurev1.NewDatabasesServiceClient(h.Conn).BrowseDatabase(ctx,
		&structurev1.BrowseDatabaseRequest{DatabaseId: dbID})
	require.NoError(t, err)

	handler, err := assembly.NewGatewayHandler(nil, h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)

	// 缺参/坏票 → 401。
	for _, q := range []string{"", "session=" + resp.GetSessionId(), "session=" + resp.GetSessionId() + "&ticket=bogus"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/v1/browse/entry?"+q, nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "query %q", q)
	}

	// 有效票 → 302 + host-only cookie。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET",
		"/v1/browse/entry?session="+resp.GetSessionId()+"&ticket="+resp.GetTicket(), nil))
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))
	cookies := rec.Result().Cookies() //nolint:bodyclose // httptest 响应体零资源
	require.Len(t, cookies, 1)
	ck := cookies[0]
	assert.Equal(t, "flt_browse", ck.Name)
	assert.True(t, ck.HttpOnly)
	assert.NotEmpty(t, ck.Value)

	// 同票二次 → 401（单用途烧票）。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET",
		"/v1/browse/entry?session="+resp.GetSessionId()+"&ticket="+resp.GetTicket(), nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// authorize（ForwardAuth 语义——traefik 透传原始 Cookie 头）。
	authReq := func(cookie string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/browse/authorize", nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		handler.ServeHTTP(rec, req)
		return rec
	}
	assert.Equal(t, http.StatusUnauthorized, authReq("").Code)
	assert.Equal(t, http.StatusOK, authReq("flt_browse="+ck.Value).Code)
	assert.Equal(t, http.StatusUnauthorized, authReq("flt_browse="+resp.GetSessionId()+".wrongvalue").Code)

	// 错方法 → 405（get_only）。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "POST", "/v1/browse/entry", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
