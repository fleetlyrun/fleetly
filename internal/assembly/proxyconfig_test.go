package assembly

// Proxy 拉取端点共享令牌（ADR-0036 N2 兑现）：空令牌 = 无认证现状（逐位
// 不变）；非空 = X-Fleetly-Proxy-Token 头常量时间比对，缺失/错值 401，
// 正确值 200 出快照。hermetic：handler 直测，无监听无网络。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/config"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	teamrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/team"
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

// TestEnsureProxyConfigExposureStartupGate（R09-M8/R14-M6 → S14-c9）：启动
// 期团队数探测决定受管端点认证门——单租户现状放行（升级零扰动），多租户
// 无 token 且非回环监听 fail-fast，置 token 后放行。
func TestEnsureProxyConfigExposureStartupGate(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	teams := teamrepo.New(clock, db.Runner())
	require.NoError(t, teams.Create(ctx, &team.Team{ID: identity.DefaultTeamID, Name: "default"}))
	cfg := config.WithDefaults(&config.AppConfig{})

	require.NoError(t, ensureProxyConfigExposure(ctx, db, cfg),
		"single-team deployments keep the current default bind (ADR-0036, upgrade zero-disturbance)")

	require.NoError(t, teams.Create(ctx, &team.Team{ID: "01JD0TEAMB0000000000000000", Name: "acme"}))
	err := ensureProxyConfigExposure(ctx, db, cfg)
	require.Error(t, err, "more than one team + unauthenticated wildcard bind must fail fast")
	assert.Contains(t, err.Error(), "server.proxy_config.auth_token")

	// 置 token → 多租户放行。
	cfg = config.WithDefaults(&config.AppConfig{Server: &config.Server{ProxyConfig: &config.ProxyConfig{AuthToken: "proxy-secret"}}})
	require.NoError(t, ensureProxyConfigExposure(ctx, db, cfg))
}

// fakeProxyProvider 是 Proxy 端口最小假件（装配面只消费 Describe/Health 与
// FactoryParams 捕获）。
type fakeProxyProvider struct{}

func (fakeProxyProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "assembly-test-proxy", Capability: capability.KindProxy}
}
func (fakeProxyProvider) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (fakeProxyProvider) PublishRoutes(context.Context, []capability.Route) error { return nil }
func (fakeProxyProvider) IssueCertificate(context.Context, capability.CertificateRequest, capability.ChallengeWriter) (capability.CertificateStatus, error) {
	return capability.CertificateStatus{}, nil
}

// TestProxyProviderLegacyEnvFallbackMatrix（提案 2026-11-05 C1 拼写矩阵锚 /
// ADR-0036 载体收拢节 3）：proxy 四键与 lynx 自动映射拼写不同形
// （FLEETLY_SERVER_PROXY_CONFIG_* ≠ FLEETLY_PROXY_*），config 层吸收不到
// install.sh 物化的旧拼写值——装配侧手写兜底。三态钉死：config 显式值胜 /
// 仅 env 在场走兜底 / 双空透传空（分层禁例下 assembly 测试不 import
// traefik，四键经捕获假工厂验证，TestRuntimeProviderSelection 同款形态）。
func TestProxyProviderLegacyEnvFallbackMatrix(t *testing.T) {
	var captured capability.ProxyParams
	capability.RegisterFactory(capability.KindProxy, "assembly-test-proxy", func(_ context.Context, p capability.FactoryParams) (capability.Provider, error) {
		captured = p.Proxy
		return fakeProxyProvider{}, nil
	})
	app := &metricsFaceApp{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// 态一：config 显式值 + env 旧拼写双设 → config 值优先（逐位不变的
	// 解析序：config > 旧拼写 env > 缺省）。
	t.Setenv("FLEETLY_PROXY_AUTH_TOKEN", "env-token")
	t.Setenv("FLEETLY_PROXY_CONFIG_ENDPOINT", "http://env-host:9082/proxy/config")
	t.Setenv("FLEETLY_PROXY_ACME_EMAIL", "env-ops@fleetly.run")
	t.Setenv("FLEETLY_PROXY_ACME_CA_SERVER", "https://env-ca.example.com/directory")
	cfg := config.WithDefaults(&config.AppConfig{Server: &config.Server{ProxyConfig: &config.ProxyConfig{
		AuthToken:      "cfg-token",
		ConfigEndpoint: "http://cfg-host:9082/proxy/config",
		AcmeEmail:      "cfg-ops@fleetly.run",
		AcmeCaServer:   "https://cfg-ca.example.com/directory",
	}}})
	proxy, cleanup, err := NewProxyProvider(app, cfg)
	require.NoError(t, err)
	require.NotNil(t, proxy)
	require.NotNil(t, cleanup)
	cleanup()
	assert.Equal(t, "cfg-token", captured.AuthToken)
	assert.Equal(t, "http://cfg-host:9082/proxy/config", captured.ConfigEndpoint)
	assert.Equal(t, "cfg-ops@fleetly.run", captured.AcmeEmail)
	assert.Equal(t, "https://cfg-ca.example.com/directory", captured.AcmeCAServer)

	// 态二：config 未设、仅 env 旧拼写 → 兜底值生效（install.sh 物化形态，
	// 升级零扰动锚）。
	cfg = config.WithDefaults(&config.AppConfig{})
	proxy2, cleanup2, err2 := NewProxyProvider(app, cfg)
	require.NoError(t, err2)
	require.NotNil(t, proxy2)
	require.NotNil(t, cleanup2)
	cleanup2()
	assert.Equal(t, "env-token", captured.AuthToken)
	assert.Equal(t, "http://env-host:9082/proxy/config", captured.ConfigEndpoint)
	assert.Equal(t, "env-ops@fleetly.run", captured.AcmeEmail)
	assert.Equal(t, "https://env-ca.example.com/directory", captured.AcmeCAServer)

	// 态三：双空 → 空串透传（缺省 = 旧行为，装配不发明回退值）。
	t.Setenv("FLEETLY_PROXY_AUTH_TOKEN", "")
	t.Setenv("FLEETLY_PROXY_CONFIG_ENDPOINT", "")
	t.Setenv("FLEETLY_PROXY_ACME_EMAIL", "")
	t.Setenv("FLEETLY_PROXY_ACME_CA_SERVER", "")
	proxy3, cleanup3, err3 := NewProxyProvider(app, cfg)
	require.NoError(t, err3)
	require.NotNil(t, proxy3)
	require.NotNil(t, cleanup3)
	cleanup3()
	assert.Empty(t, captured.AuthToken)
	assert.Empty(t, captured.ConfigEndpoint)
	assert.Empty(t, captured.AcmeEmail)
	assert.Empty(t, captured.AcmeCAServer)
}
