package assembly

// Edge 拉取端点共享令牌（ADR-0036 N2 兑现）：空令牌 = 无认证现状（逐位
// 不变）；非空 = X-Fleetly-Edge-Token 头常量时间比对，缺失/错值 401，
// 正确值 200 出快照。hermetic：handler 直测，无监听无网络。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConfigSource 是 ConfigSource 的测试假（快照恒定）。
type fakeConfigSource struct{ snap []byte }

func (f fakeConfigSource) ConfigSnapshot() []byte { return f.snap }

func edgeConfigRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/edge/config", nil)
	if token != "" {
		req.Header.Set(edgeAuthTokenHeader, token)
	}
	return req
}

func TestEdgeConfigHandlerNoAuth(t *testing.T) {
	h := newEdgeConfigMux(fakeConfigSource{snap: []byte(`{"http":{"routers":{}}}`)}, "")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, edgeConfigRequest(t, ""))
	require.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, `{"http":{"routers":{}}}`, resp.Body.String())
}

func TestEdgeConfigHandlerEmptySnapshotNeverBareEmpty(t *testing.T) {
	h := newEdgeConfigMux(fakeConfigSource{}, "")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, edgeConfigRequest(t, ""))
	require.Equal(t, http.StatusOK, resp.Code)
	assert.NotEqual(t, "{}", resp.Body.String(), "endpoint must never serve a bare empty object (config wipe invariant)")
}

func TestEdgeConfigHandlerTokenEnforced(t *testing.T) {
	snap := []byte(`{"http":{"routers":{"r":{}}}}`)
	h := newEdgeConfigMux(fakeConfigSource{snap: snap}, "edge-secret")

	// 缺失 / 错值 → 401（体不携带任何配置字节）。
	for name, token := range map[string]string{"missing": "", "wrong": "not-the-token"} {
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, edgeConfigRequest(t, token))
		assert.Equal(t, http.StatusUnauthorized, resp.Code, name)
		assert.NotContains(t, resp.Body.String(), "routers", name)
	}

	// 正确值 → 200 + 快照。
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, edgeConfigRequest(t, "edge-secret"))
	require.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, string(snap), resp.Body.String())
}
