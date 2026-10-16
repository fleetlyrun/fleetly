package apitest_test

// Registry 面缺席族 503 回归（P0-7.2）：harness 假仓无 content 子面 →
// engine.ErrRegistryUnavailable → E_REGISTRY_UNAVAILABLE（gRPC
// Unavailable / REST 503）。配置级 off 态不再是 E_INTERNAL 500：message
// 本就面向用户（含 set registry.addr 处置提示），非 E_INTERNAL 不铸
// error_id（P0-3 出口纪律零影响面）。nil-registry 形态在 engine 单测
// （registrycontent_test.go）承载——夹具引擎恒装配假仓。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestRegistryUnavailableFamily(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()

	projectID := h.CreateProject(t, "reg-unavailable").GetId()

	// gRPC 面：Unavailable + 信封 E_REGISTRY_UNAVAILABLE + 配置级 message。
	_, err := deliveryv1.NewRegistryServiceClient(h.Conn).ListRegistryCatalog(ctx,
		&deliveryv1.ListRegistryCatalogRequest{ProjectId: projectID})
	require.Error(t, err)
	assert.Equal(t, "Unavailable", status.Code(err).String())
	env, ok := apperrstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_REGISTRY_UNAVAILABLE", env.Code())
	assert.Contains(t, status.Convert(err).Message(), "does not offer the content face",
		"config-level message is user-facing by design, not a leaked cause")
	assert.NotEmpty(t, env.Suggestion())
	assert.Empty(t, env.ErrorID(), "non-E_INTERNAL exits mint no error_id")

	// REST 面：503 + 同信封（经 gRPC 段转发）。
	handler, err := assembly.NewGatewayHandler(nil, h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/registry/catalog?project_id="+projectID, nil)
	req.Header.Set("Authorization", "Bearer "+h.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())

	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "E_REGISTRY_UNAVAILABLE", envelope.Code)
	assert.Contains(t, envelope.Message, "does not offer the content face")
}
