package apitest_test

// E_INTERNAL 出口纪律三面回归（P0-3）：gRPC 面 message 无 cause 复合文本、
// trailer metadata 携带 x-fleetly-error-id、信封 context 同步落 id；REST 面
// 复用同一单点（经 gRPC 段转发的错误幂等复用 id）且响应头同键承载；非
// E_INTERNAL 零影响。触发面（P0-7.2 起换装）：ListSharedVariables + DROP
// TABLE——受管仓面缺席族已分流 E_REGISTRY_UNAVAILABLE（registry_
// unavailable_test.go），E_INTERNAL 的真实形态是存储层故障（SQL 错误复合
// 文本经 handler 进 message，正是出口纪律要拦下的形态）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// errorIDHexRe 与 apperr.NewErrorID 的 128 位十六进制形态对齐。
var errorIDHexRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// dropSharedVariables 制造存储层内部故障（表缺席 → SQL 失败 → mapStateError
// 兜 E_INTERNAL，消息含 SQL cause 文本）。
func dropSharedVariables(t *testing.T, h *apitest.Harness) {
	t.Helper()
	if _, err := h.DB.Runner().ExecContext(context.Background(), "DROP TABLE shared_variables"); err != nil {
		t.Fatalf("drop shared_variables: %v", err)
	}
}

// TestInternalErrorExitGRPCFace：gRPC 面 E_INTERNAL 全链（bufconn 真装配
// 拦截器链）——message 脱敏、error_id 入信封 context、trailer 同键承载；
// 非 E_INTERNAL（E_ALREADY_EXISTS）message 原样、无 error_id。
func TestInternalErrorExitGRPCFace(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx,
		&structurev1.CreateProjectRequest{Name: "e-internal-exit"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	// E_INTERNAL：存储层故障（表缺席）→ handler 兜 E_INTERNAL（消息含 cause 文本）。
	dropSharedVariables(t, h)
	_, err = structurev1.NewSharedVariablesServiceClient(h.Conn).ListSharedVariables(ctx,
		&structurev1.ListSharedVariablesRequest{ProjectId: projectID})
	require.Error(t, err)
	st := status.Convert(err)
	assert.Equal(t, status.Code(err).String(), "Internal")
	assert.Equal(t, "E_INTERNAL: internal server error", st.Message(),
		"gRPC face must carry the redacted message, never the cause text")
	assert.NotContains(t, st.Message(), "shared_variables", "SQL cause text must not leak")

	env, ok := apperr.FromError(err)
	require.True(t, ok, "envelope detail must survive the wire")
	assert.Equal(t, "E_INTERNAL", env.Code())
	require.Regexp(t, errorIDHexRe, env.ErrorID(), "envelope context must carry the minted error_id")
	assert.NotEmpty(t, env.Suggestion())

	// trailer 承载：同一 id 经 x-fleetly-error-id 上线（非 proto 消费方可直读）。
	var trailer metadata.MD
	err = h.Conn.Invoke(ctx, "/fleetly.structure.v1.SharedVariablesService/ListSharedVariables",
		&structurev1.ListSharedVariablesRequest{ProjectId: projectID},
		&structurev1.ListSharedVariablesResponse{}, grpc.Trailer(&trailer))
	require.Error(t, err)
	trailerEnv, ok := apperr.FromError(err)
	require.True(t, ok)
	got := trailer.Get(apperr.ErrorIDHeader)
	require.Len(t, got, 1, "trailer must carry exactly one x-fleetly-error-id entry")
	assert.Equal(t, trailerEnv.ErrorID(), got[0], "trailer id must match the envelope id of the same call")
	assert.Regexp(t, errorIDHexRe, got[0])

	// 非 E_INTERNAL 零影响：唯一约束命中的 message 本就面向用户，原样上线。
	_, err = structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx,
		&structurev1.CreateProjectRequest{Name: "e-internal-exit"})
	require.Error(t, err)
	assert.Contains(t, status.Convert(err).Message(), "already exists")
	dupeEnv, ok := apperr.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_ALREADY_EXISTS", dupeEnv.Code())
	assert.Empty(t, dupeEnv.ErrorID(), "error_id is minted at the E_INTERNAL exit only")
	var dupeTrailer metadata.MD
	require.Error(t, h.Conn.Invoke(ctx, "/fleetly.structure.v1.ProjectsService/CreateProject",
		&structurev1.CreateProjectRequest{Name: "e-internal-exit"},
		&structurev1.CreateProjectResponse{}, grpc.Trailer(&dupeTrailer)))
	assert.Empty(t, dupeTrailer.Get(apperr.ErrorIDHeader), "non-E_INTERNAL exits carry no error-id trailer")
}

// TestInternalErrorExitRESTFace：REST 面（gateway 经 gRPC 段转发）复用同一
// 单点——gRPC 出口已收口的错误到 gateway 幂等放行，error_id 同源；响应头
// x-fleetly-error-id 与信封 context 同值；cause 文本不上线。
func TestInternalErrorExitRESTFace(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx,
		&structurev1.CreateProjectRequest{Name: "e-internal-rest"})
	require.NoError(t, err)
	dropSharedVariables(t, h)

	handler, err := assembly.NewGatewayHandler(nil, h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/shared-variables?project_id="+proj.GetProject().GetId(), nil)
	req.Header.Set("Authorization", "Bearer "+h.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "shared_variables", "REST body must not leak the cause text")

	var envelope struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Context map[string]string `json:"context"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "E_INTERNAL", envelope.Code)
	assert.Equal(t, apperr.RedactedInternalMessage, envelope.Message)
	require.Regexp(t, errorIDHexRe, envelope.Context["error_id"])

	// 响应头与信封同键同值（与 gRPC trailer 形态统一）。
	require.Equal(t, envelope.Context["error_id"], rec.Header().Get(apperr.ErrorIDHeader))
}
