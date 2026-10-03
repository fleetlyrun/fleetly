package traefik

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

var update = flag.Bool("update", false, "rewrite golden files")

// 动态配置翻译 golden（h2c/http/tcp + TLS 标注；F0.15 验收：h2c 后端
// 路由可通——messageloop 形态）。
func TestDynamicConfigGolden(t *testing.T) {
	p, err := New("http://fleetlyd:9082/edge/config", "ops@example.com")
	require.NoError(t, err)
	require.NoError(t, p.PublishRoutes(context.Background(), []capability.Route{
		{Host: "shop.127.0.0.1.sslip.io", Path: "/", Process: "web", Port: 8080,
			Protocol: capability.ProtocolHTTP, TLS: "auto", BackendAddr: "10.0.0.2:8080"},
		{Host: "api.127.0.0.1.sslip.io", Path: "/v1", Process: "gateway", Port: 8081,
			Protocol: capability.ProtocolH2C, TLS: "auto", BackendAddr: "10.0.0.3:8081"},
		{Host: "db.127.0.0.1.sslip.io", Process: "pg", Port: 5432,
			Protocol: capability.ProtocolTCP, TLS: "none", BackendAddr: "10.0.0.4:5432"},
		{Host: "plain.localhost", Process: "web", Port: 3000,
			Protocol: capability.ProtocolHTTP, TLS: "none", BackendAddr: "10.0.0.5:3000"},
	}))
	got := string(p.ConfigSnapshot())

	golden := filepath.Join("testdata", "dynamic-config.json")
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750))
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/providers/traefik -update")
	assert.Equal(t, string(want), got)
}

// 未解析后端的 Route 拒绝发布（控制面强制全量：宁拒发不发半截配置）。
func TestUnresolvedBackendRejected(t *testing.T) {
	_, err := buildDynamicConfig([]capability.Route{{Host: "x.sslip.io", Process: "web", Port: 80}})
	assert.ErrorContains(t, err, "no resolved backend address")
}

// 纵深防线（安全批 P0）：host/path 白名单外（反引号注入形态等）的路由
// 被跳过并留错误日志，合法路由照常发布——存量行绕过受理面校验的兜底。
func TestInvalidHostOrPathRouteSkipped(t *testing.T) {
	valid := capability.Route{Host: "ok.127.0.0.1.sslip.io", Path: "/api", Process: "web", Port: 80,
		Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.1:80"}
	cfg, err := buildDynamicConfig([]capability.Route{
		valid,
		{Host: "evil`.sslip.io", Process: "web", Port: 80, Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.2:80"}, // 反引号注入
		{Host: "under_score.sslip.io", Process: "web", Port: 80, Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.3:80"},
		{Host: "", Process: "web", Port: 80, Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.4:80"},
		{Host: "long.sslip.io", Path: "/bad`path", Process: "web", Port: 80,
			Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.5:80"}, // path 注入
	})
	require.NoError(t, err, "invalid routes must be skipped, not fail the whole publication")
	var parsed struct {
		HTTP struct {
			Routers map[string]json.RawMessage `json:"routers"`
		} `json:"http"`
	}
	require.NoError(t, json.Unmarshal(cfg, &parsed))
	require.Len(t, parsed.HTTP.Routers, 1, "only the valid route may be published")
	require.Contains(t, parsed.HTTP.Routers, routeKey(valid))
	// 发布串里不得出现任何用户供给的恶意形态（规则定界符反引号本身合法，
	// 注入面是"恶意串穿透进规则串"）。
	assert.NotContains(t, string(cfg), "evil`", "the backtick host must not reach the published config")
	assert.NotContains(t, string(cfg), "under_score", "the underscore host must not reach the published config")
	assert.NotContains(t, string(cfg), "bad`path", "the backtick path must not reach the published config")
}

// Q-10 回归：routeKey 必须单射——归一化清洗的碰撞对 ("a.b","/c") 与
// ("a.b-c","") 不得共用 router/service 键（同名 map 撞键 = 后路由静默
// 互覆前路由）。两路由共存于同一配置，四键（2 router + 2 service）互异。
func TestRouteKeyInjectiveOnNormalizedCollision(t *testing.T) {
	collide := []capability.Route{
		{Host: "a.b", Path: "/c", Process: "web", Port: 80,
			Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.1:80"},
		{Host: "a.b-c", Process: "api", Port: 8080,
			Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.2:8080"},
	}
	k1, k2 := routeKey(collide[0]), routeKey(collide[1])
	require.NotEqual(t, k1, k2, "the normalization-collision pair must produce distinct keys")

	cfg, err := buildDynamicConfig(collide)
	require.NoError(t, err)
	var parsed struct {
		HTTP struct {
			Routers  map[string]json.RawMessage `json:"routers"`
			Services map[string]json.RawMessage `json:"services"`
		} `json:"http"`
	}
	require.NoError(t, json.Unmarshal(cfg, &parsed))
	require.Len(t, parsed.HTTP.Routers, 2, "both routes must coexist as routers (collision would silently merge to one)")
	require.Len(t, parsed.HTTP.Services, 2, "both routes must coexist as services")
	require.Contains(t, parsed.HTTP.Routers, k1)
	require.Contains(t, parsed.HTTP.Routers, k2)

	// 键仍在 traefik 名字的安全字符集内（DNS label：字母数字与连字符）。
	for _, k := range []string{k1, k2} {
		for _, c := range k {
			require.True(t, (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-',
				"route key %q must stay DNS-safe", k)
		}
	}
}

// 受管形态声明完整性（80/443 发布 + ACME storage 卷）。
func TestManagedWorkloadDeclaration(t *testing.T) {
	p, err := New("http://fleetlyd:9082/edge/config", "")
	require.NoError(t, err)
	ws := p.ManagedWorkloads()
	require.Len(t, ws, 1)
	w := ws[0]
	assert.Equal(t, Image, w.Image)
	assert.Equal(t, []capability.PortPublish{
		{PublishedPort: 80, TargetPort: 80},
		{PublishedPort: 443, TargetPort: 443},
	}, w.Publish)
	assert.Equal(t, capability.NamespaceRef{Team: "fleetly", Project: "system", App: "edge"}, p.ManagedNamespace())
	assert.Contains(t, joinCommand(w.Command), "--certificatesresolvers.le.acme.httpchallenge.entrypoint=web")
}

func joinCommand(cmd []string) string {
	out := ""
	for _, c := range cmd {
		out += c + " "
	}
	return out
}
