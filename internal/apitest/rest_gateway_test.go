package apitest_test

// REST gateway 冒烟（P1-13/A-9 回归）：assembly 挂载清单补齐 identity 六
// 服务与 Hooks 配置面后，注解面经 REST 可达（此前 identity 整面 404）。
// 走 assembly.NewGatewayHandler——与生产 NewGatewayServer 同一清单与错误
// 信封；静态对账在守卫 A（internal/guards），本文件是行为面。

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/identity"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestRESTGatewayServesIdentityAnnotationSurface：identity 只读端点经 REST
// 不再 404——whoami（Bearer 凭证）与 users 列表（owner 全权）双确认，
// protojson snake_case 输出口径与 CLI --json 同源。
func TestRESTGatewayServesIdentityAnnotationSurface(t *testing.T) {
	h := apitest.New(t)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, fleetlygrpc.NewEventStreamSource(h.Services))
	require.NoError(t, err)

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+h.Token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// whoami：P1-13 修复前 identity 服务未挂载，此请求 404。
	rec := get("/v1/whoami")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"token_name":"bootstrap"`)
	assert.Contains(t, rec.Body.String(), `"role_name":"owner"`)

	// users 只读面同源可达（漏挂时同 404）；先经 gRPC 面造一个用户，
	// 列表非空以钉 protojson 字段名。
	users := identityv1.NewUsersServiceClient(h.Conn)
	_, err = users.CreateUser(sdk.WithToken(context.Background(), h.Token), &identityv1.CreateUserRequest{Name: "rest-smoke", RoleId: identity.RoleMemberID})
	require.NoError(t, err)
	rec = get("/v1/users")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"users"`)
	assert.Contains(t, rec.Body.String(), "rest-smoke")

	// 未带凭证：401 且走统一错误信封（非 grpc-gateway 默认错误体）。
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/users", nil)
	bare := httptest.NewRecorder()
	handler.ServeHTTP(bare, req)
	assert.Equal(t, http.StatusUnauthorized, bare.Code)
	assert.Contains(t, bare.Body.String(), `"code"`)
}

// TestRESTGatewayServesSelfDescription：能力自描述面（F1.4）经 REST 可达
// （PUBLIC——零文档发现面，无凭证即可发现契约）；explain 未知名走统一
// 错误信封 E_NOT_FOUND。
func TestRESTGatewayServesSelfDescription(t *testing.T) {
	h := apitest.New(t)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/system/schema", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// 全量文档含 spec 与事件两类条目（夹具链接全部贡献方）。
	body := rec.Body.String()
	assert.Contains(t, body, `"name":"app"`)
	assert.Contains(t, body, `"name":"deployment.succeeded"`)
	assert.Contains(t, body, `"schema_json"`)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/system/explain?resource=deployment.succeeded", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"kind":"event"`)
	// schema_json 内嵌为转义字符串，断言不带引号的字段名即可。
	assert.Contains(t, rec.Body.String(), "deployment_id")

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/system/explain?resource=no.such", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "E_NOT_FOUND")
}
