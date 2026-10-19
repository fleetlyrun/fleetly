package assembly

// Proxy 拉取端点共享令牌（ADR-0036 N2 兑现）：空令牌 = 无认证现状（逐位
// 不变）；非空 = X-Fleetly-Proxy-Token 头常量时间比对，缺失/错值 401，
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

// readyConfigSource 在 fakeConfigSource 上叠加可选 Ready 子面（H7 发布
// 就绪门的端点侧判定；不实现 Ready 的旧 Provider 走 fakeConfigSource
// 形态，语义不变）。
type readyConfigSource struct {
	fakeConfigSource
	ready bool
}

func (f readyConfigSource) Ready() bool { return f.ready }

func proxyConfigRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/proxy/config", nil)
	if token != "" {
		req.Header.Set(proxyAuthTokenHeader, token)
	}
	return req
}

func TestProxyConfigHandlerNoAuth(t *testing.T) {
	h := newProxyConfigMux(fakeConfigSource{snap: []byte(`{"http":{"routers":{}}}`)}, "")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, proxyConfigRequest(t, ""))
	require.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, `{"http":{"routers":{}}}`, resp.Body.String())
}

func TestProxyConfigHandlerEmptySnapshotNeverBareEmpty(t *testing.T) {
	h := newProxyConfigMux(fakeConfigSource{}, "")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, proxyConfigRequest(t, ""))
	require.Equal(t, http.StatusOK, resp.Code)
	assert.NotEqual(t, "{}", resp.Body.String(), "endpoint must never serve a bare empty object (config wipe invariant)")
}

func TestProxyConfigHandlerTokenEnforced(t *testing.T) {
	snap := []byte(`{"http":{"routers":{"r":{}}}}`)
	h := newProxyConfigMux(fakeConfigSource{snap: snap}, "proxy-secret")

	// 缺失 / 错值 → 401（体不携带任何配置字节）。
	for name, token := range map[string]string{"missing": "", "wrong": "not-the-token"} {
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, proxyConfigRequest(t, token))
		assert.Equal(t, http.StatusUnauthorized, resp.Code, name)
		assert.NotContains(t, resp.Body.String(), "routers", name)
	}

	// 正确值 → 200 + 快照。
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, proxyConfigRequest(t, "proxy-secret"))
	require.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, string(snap), resp.Body.String())
}

// H7：Ready 子面在场且未就绪 → 503 且不写快照（traefik 保 last-known-good，
// 不被空/半量配置覆盖）；就绪 → 200 出快照。认证先于就绪判定。
func TestProxyConfigHandlerReadinessGate(t *testing.T) {
	snap := []byte(`{"http":{"routers":{"r":{}}}}`)

	notReady := newProxyConfigMux(readyConfigSource{fakeConfigSource{snap: snap}, false}, "")
	resp := httptest.NewRecorder()
	notReady.ServeHTTP(resp, proxyConfigRequest(t, ""))
	require.Equal(t, http.StatusServiceUnavailable, resp.Code)
	assert.NotContains(t, resp.Body.String(), "routers", "503 must not leak any snapshot bytes")

	// 认证优先：错令牌仍是 401（不因未就绪改判 503）。
	authed := newProxyConfigMux(readyConfigSource{fakeConfigSource{snap: snap}, false}, "proxy-secret")
	resp = httptest.NewRecorder()
	authed.ServeHTTP(resp, proxyConfigRequest(t, "wrong"))
	assert.Equal(t, http.StatusUnauthorized, resp.Code)

	ready := newProxyConfigMux(readyConfigSource{fakeConfigSource{snap: snap}, true}, "")
	resp = httptest.NewRecorder()
	ready.ServeHTTP(resp, proxyConfigRequest(t, ""))
	require.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, string(snap), resp.Body.String())
}
