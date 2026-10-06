package apitest_test

// UploadSource REST 面冒烟（F3.5，ADR-0019 附录 A 留白收口）：POST
// /v1/uploads?project_id=<id>（raw body tar 流）——与生产 NewGatewayServer
// 同一清单与错误信封（assembly.NewGatewayHandler）。断言面：上传/内容寻址
// 去重/ListUploads 注解面不被同路径原生挂法遮蔽/未授权 401/冻结拒绝/
// 超限 413 诚实信封。

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/upload"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestRESTUploadSourceLifecycle(t *testing.T) {
	h := apitest.New(t)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil)
	require.NoError(t, err)

	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "rest-upload"})
	require.NoError(t, err)
	projID := proj.GetProject().GetId()

	post := func(body string, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
			"/v1/uploads?project_id="+projID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-tar")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 未授权：401 统一错误信封。
	rec := post("x", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code"`)

	// 上传成功：内容寻址引用（protojson snake_case；deduplicated=false 是
	// 零值、protojson 省略——以字段缺席为断言面）。
	body := string(buildFixtureTar())
	rec = post(body, h.Token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"digest"`)
	assert.NotContains(t, rec.Body.String(), `"deduplicated"`)

	// 同字节重传：deduplicated=true（内容寻址天然幂等）。
	rec = post(body, h.Token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"deduplicated":true`)

	// ListUploads 注解面不被同路径原生挂法遮蔽（GET 回落 gateway）。
	get := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/v1/uploads?project_id="+projID, nil)
	get.Header.Set("Authorization", "Bearer "+h.Token)
	grec := httptest.NewRecorder()
	handler.ServeHTTP(grec, get)
	require.Equal(t, http.StatusOK, grec.Code, grec.Body.String())
	assert.Contains(t, grec.Body.String(), `"uploads"`)

	// 缺 project_id：协议层最小拒绝体。
	bad := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/uploads", strings.NewReader("x"))
	bad.Header.Set("Authorization", "Bearer "+h.Token)
	brec := httptest.NewRecorder()
	handler.ServeHTTP(brec, bad)
	assert.Equal(t, http.StatusBadRequest, brec.Code)
}

func TestRESTUploadSourceEnforcement(t *testing.T) {
	// 超限：413 诚实信封（E_UPLOAD_TOO_LARGE，非不透明形态——Q-11 教训）。
	h := apitest.New(t)
	h.Services.UploadStore = upload.NewStore(h.DataRoot, 64, 1<<20)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil)
	require.NoError(t, err)

	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "rest-upload-lim"})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/v1/uploads?project_id="+proj.GetProject().GetId(), strings.NewReader(strings.Repeat("x", 65)))
	req.Header.Set("Authorization", "Bearer "+h.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "E_UPLOAD_TOO_LARGE")

	// 冻结：E_CHANGE_FROZEN 经统一错误信封（流式拦截器首帧执法）。
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	gov := systemv1.NewGovernanceServiceClient(h.Conn)
	team, err := teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: "rest-upload-frozen"})
	require.NoError(t, err)
	fproj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "frozen", TeamId: team.GetTeam().GetId()})
	require.NoError(t, err)
	_, err = gov.SetChangeFreeze(ctx, &systemv1.SetChangeFreezeRequest{TeamId: team.GetTeam().GetId(), Reason: "rest freeze"})
	require.NoError(t, err)

	freq := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/v1/uploads?project_id="+fproj.GetProject().GetId(), strings.NewReader("x"))
	freq.Header.Set("Authorization", "Bearer "+h.Token)
	frec := httptest.NewRecorder()
	handler.ServeHTTP(frec, freq)
	assert.NotEqual(t, http.StatusOK, frec.Code)
	assert.Contains(t, frec.Body.String(), "E_CHANGE_FROZEN")
}
