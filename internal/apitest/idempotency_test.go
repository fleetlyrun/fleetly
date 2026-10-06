package apitest_test

// 幂等执法全链测试（ADR-0024 / F1.1）：拦截器经真执法链（authn → idem）
// 生效——gRPC 面重放/异体 409/双源一致性；REST 面头映射与 409 信封。
// hermetic 单测在 internal/idem；本文件钉"链上确实装了它"。

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// codeOf 提取 apperr 信封码。
func codeOf(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok)
	if e, ok := apperr.FromGRPCStatus(st); ok {
		return e.Code()
	}
	return "grpc:" + st.Code().String()
}

// gRPC 面：同键同体重放返回同一 Deployment（不重复受理）；异体 409；
// 双源（body 字段 vs 头）不一致 409。
func TestIdempotencyDeployReplayAndConflicts(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)

	// 夹具链：项目 + App（幂等键挂部署面）。
	pctx := structurev1.NewProjectsServiceClient(h.Conn)
	proj, err := pctx.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "idem-proj"})
	require.NoError(t, err)
	pc := structurev1.NewAppsServiceClient(h.Conn)
	app, err := pc.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "idem-app"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()
	req := &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.28", ProcessName: "web"}

	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	kctx := sdk.WithIdempotencyKey(ctx, "torchwood-run-1")
	d1, err := dc.Deploy(kctx, req)
	require.NoError(t, err)
	d2, err := dc.Deploy(kctx, req)
	require.NoError(t, err, "same key + same body replays the original response")
	assert.Equal(t, d1.GetDeployment().GetId(), d2.GetDeployment().GetId())

	// 落库事实：重放不产生第二条部署行。
	dl, err := dc.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Len(t, dl.GetDeployments(), 1, "replay must not admit a second deployment")

	// 同键异体：409 族错误码。
	other := &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.29", ProcessName: "web"}
	_, err = dc.Deploy(kctx, other)
	require.Error(t, err)
	assert.Equal(t, "E_IDEMPOTENCY_KEY_CONFLICT", codeOf(t, err))

	// 双源不一致：头 k2 + body k3 → 拒绝（未执行）。
	dual := &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.28", ProcessName: "web", IdempotencyKey: "body-anchor"}
	_, err = dc.Deploy(sdk.WithIdempotencyKey(ctx, "header-key"), dual)
	require.Error(t, err)
	assert.Equal(t, "E_IDEMPOTENCY_KEY_CONFLICT", codeOf(t, err))

	// 无键请求不受影响（可选承诺）。
	_, err = dc.Deploy(ctx, other)
	require.NoError(t, err)
}

// REST 面：Idempotency-Key 头经 gateway 映射进 metadata——重放同体、
// 异体 409 走统一错误信封。
func TestIdempotencyRESTHeader(t *testing.T) {
	h := apitest.New(t)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)

	post := func(key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/projects", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.Token)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	r1 := post("rest-k1", `{"name":"idem-rest"}`)
	require.Equal(t, http.StatusOK, r1.Code, r1.Body.String())
	r2 := post("rest-k1", `{"name":"idem-rest"}`)
	require.Equal(t, http.StatusOK, r2.Code, r2.Body.String())
	assert.Equal(t, r1.Body.String(), r2.Body.String(), "REST replay returns the stored response verbatim")

	r3 := post("rest-k1", `{"name":"different"}`)
	assert.Equal(t, http.StatusConflict, r3.Code)
	assert.Contains(t, r3.Body.String(), `"code":"E_IDEMPOTENCY_KEY_CONFLICT"`)
}
