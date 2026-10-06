package apitest_test

// Console 静态面与 REST API 面的共存行为锚（F2.6/ADR-0044 验收锚 2）：
// 经 assembly.NewGatewayHandler（与生产同清单）确认——根路径出 Console
// 入口、未知 /v1/* 仍是 gateway 404（不被 SPA fallback 吞掉）、既有
// REST 端点行为不变（错误信封口径）。

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
)

func TestGatewayServesConsoleAndKeepsV1Untouched(t *testing.T) {
	h := apitest.New(t)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, nil)
	require.NoError(t, err)

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+h.Token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 根路径与 SPA 深链都回 Console 入口（embed 静态面挂载生效）。
	for _, path := range []string{"/", "/deployments"} {
		rec := get(path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), path)
		assert.Contains(t, rec.Body.String(), "<title>fleetly console</title>", path)
	}

	// 未知 /v1/* 仍是 gateway 404（JSON 错误体），不被 SPA fallback 吞。
	rec := get("/v1/no-such-endpoint")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "Not Found")
	assert.NotContains(t, rec.Body.String(), "<html")

	// 既有 REST 端点照常（凭证面不受静态挂载影响）。
	rec = get("/v1/whoami")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"token_name":"bootstrap"`)
}
