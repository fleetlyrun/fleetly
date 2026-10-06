package apitest_test

// REST 错误面 HTTP 语义回归（批 0 复核）：E_CONFLICT 族曾按 gRPC code 机械
// 映射打成 400（FailedPrecondition），E_ALREADY_EXISTS 随 Q-12 退休后唯一
// 约束命中也退化成 400——REST/SDK 消费方按 409 写的重试逻辑被静默破坏。
// 修复后：唯一约束命中 → E_ALREADY_EXISTS（AlreadyExists→409）；状态冲突
// → E_CONFLICT，REST 面经 assembly errcodeToHTTP 显式映射 409。本文件走
// assembly.NewGatewayHandler（与生产同一错误出口）断行为面；映射表的完备
// 性守卫在 internal/assembly/errors_test.go。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// restGateway 装配 REST 面（与生产同清单同错误出口）并返回 POST/DELETE 助手。
func restGateway(t *testing.T, h *apitest.Harness) (post func(path, body string) *httptest.ResponseRecorder, del func(path string) *httptest.ResponseRecorder) {
	t.Helper()
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, nil)
	require.NoError(t, err)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+h.Token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	return func(path, body string) *httptest.ResponseRecorder { return do(http.MethodPost, path, body) },
		func(path string) *httptest.ResponseRecorder { return do(http.MethodDelete, path, "") }
}

// TestRESTUniqueViolationReturns409：同名重复创建（唯一索引违例）在 REST
// 面必须是 409 + E_ALREADY_EXISTS——批 0 Q-12 曾把该形态统一进 E_CONFLICT
// （FailedPrecondition→400），按 409 写的重试逻辑静默破坏；gRPC 面同步
// 断言 AlreadyExists（分类诚实，传输码与 HTTP 码各守其语义）。
func TestRESTUniqueViolationReturns409(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	post, _ := restGateway(t, h)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)

	// 同名 Project 再建：REST 409 + E_ALREADY_EXISTS（信封 code 落 body）。
	assert.Equal(t, http.StatusOK, post("/v1/projects", `{"name":"dupe-rest"}`).Code)
	rec := post("/v1/projects", `{"name":"dupe-rest"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_ALREADY_EXISTS"`)
	// gRPC 面：AlreadyExists（不再是 FailedPrecondition）。
	_, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "dupe-rest"})
	require.Error(t, err)
	require.Equal(t, codes.AlreadyExists, status.Code(err), "unique violation must surface gRPC AlreadyExists")

	// 同 Project 内同名 App 再建：同族同判（apps 唯一索引 (project_id,name)）。
	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "dupe-apps"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()
	appBody := `{"project_id":"` + projectID + `","name":"web"}`
	assert.Equal(t, http.StatusOK, post("/v1/apps", appBody).Code)
	rec = post("/v1/apps", appBody)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_ALREADY_EXISTS"`)
	_, err = apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projectID, Name: "web"})
	require.Error(t, err)
	require.Equal(t, codes.AlreadyExists, status.Code(err))
}

// TestRESTStateConflictReturns409：与资源当前状态冲突（DeleteApp 有活跃
// 部署）在 REST 面必须是 409 + E_CONFLICT——请求合法、当前状态不容，
// HTTP 语义即 409（此前机械映射 FailedPrecondition→400）；gRPC 面保持
// FailedPrecondition（gRPC 语义正确，REST 由错误码层显式纠偏）。
func TestRESTStateConflictReturns409(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	_, del := restGateway(t, h)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "conflict-rest"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	// 活跃部署在 → 删除拒绝（E_CONFLICT）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)
	rec := del("/v1/apps/" + appID)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_CONFLICT"`)

	// gRPC 面同步断言：FailedPrecondition（传输语义不变，409 由 REST 映射层承载）。
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
