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
// 路由可通——messageloop 形态；F3.6 增 browse 双路由——ForwardAuth
// 门禁渲染）。
func TestDynamicConfigGolden(t *testing.T) {
	p, err := New("http://fleetlyd:9082/proxy/config", "ops@example.com", "")
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
		// browse 会话双路由（ADR-0051 决策 5）：entry 免门禁 + 工具路由带
		// ForwardAuth。
		{Host: "browse-01jd.test", Path: "/v1/browse/entry", Process: "pgweb", Port: 8080,
			Protocol: capability.ProtocolHTTP, TLS: "none", BackendAddr: "fleetlyd-host:9081"},
		{Host: "browse-01jd.test", Process: "pgweb", Port: 8080,
			Protocol: capability.ProtocolHTTP, TLS: "none", BackendAddr: "10.0.0.6:8080",
			Auth: &capability.RouteAuth{Address: "http://fleetlyd-host:9081/v1/browse/authorize"}},
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
	p, err := New("http://fleetlyd:9082/proxy/config", "", "")
	require.NoError(t, err)
	ws := p.ManagedWorkloads()
	require.Len(t, ws, 1)
	w := ws[0]
	assert.Equal(t, Image, w.Image)
	assert.Equal(t, []capability.PortPublish{
		{PublishedPort: 80, TargetPort: 80},
		{PublishedPort: 443, TargetPort: 443},
	}, w.Publish)
	assert.Equal(t, capability.NamespaceRef{Team: "fleetly", Project: "system", App: "proxy"}, p.ManagedNamespace())
	assert.Contains(t, joinCommand(w.Command), "--certificatesresolvers.le.acme.httpchallenge.entrypoint=web")
	assert.NotContains(t, joinCommand(w.Command), "X-Fleetly-Proxy-Token",
		"empty token must keep the command byte-identical to the no-auth form (upgrade zero-disturbance)")
}

// 共享令牌开关（ADR-0036 N2 兑现）：令牌非空 → 受管命令携带同名头；空 →
// 零新增参数（无认证现状逐位不变）。
func TestManagedWorkloadAuthTokenHeader(t *testing.T) {
	p, err := New("http://fleetlyd:9082/proxy/config", "", "s3cret-proxy-token")
	require.NoError(t, err)
	cmd := joinCommand(p.ManagedWorkloads()[0].Command)
	assert.Contains(t, cmd, "--providers.http.headers.X-Fleetly-Proxy-Token=s3cret-proxy-token")
}

func joinCommand(cmd []string) string {
	out := ""
	for _, c := range cmd {
		out += c + " "
	}
	return out
}

// P9 发布前预检：生成器不变量——一切合法 Route 语料（含白名单跳过后
// 的混合集）的生成物必须通过预检；预检红面=生成器 bug 或漏网输入。
// traefik 无校验面（真机核对 2026-10-04），控制面 schema 级自校验是
// 裁决降级轨道的全部落地形态。
func TestPrecheckAcceptsGeneratedConfigs(t *testing.T) {
	corpus := [][]capability.Route{
		{},
		{{Host: "a.127.0.0.1.sslip.io", Process: "web", Port: 80,
			Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.1:80"}},
		{{Host: "a.127.0.0.1.sslip.io", Path: "/v1", Process: "web", Port: 80,
			Protocol: capability.ProtocolH2C, BackendAddr: "10.0.0.1:80"}},
		{{Host: "a.127.0.0.1.sslip.io", Process: "db", Port: 5432,
			Protocol: capability.ProtocolTCP, TLS: "auto", BackendAddr: "10.0.0.2:5432"}},
		// 混合集：非法行被白名单纵深面跳过，剩余生成物仍须过预检。
		{
			{Host: "ok.127.0.0.1.sslip.io", Process: "web", Port: 80,
				Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.1:80"},
			{Host: "evil`.127.0.0.1.sslip.io", Process: "web", Port: 80,
				Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.2:80"},
		},
		// routeKey 撞名对（Q-10 语料）双路由共存形态。
		{
			{Host: "a.b", Path: "/c", Process: "web", Port: 80,
				Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.1:80"},
			{Host: "a.b-c", Process: "api", Port: 8080,
				Protocol: capability.ProtocolH2C, BackendAddr: "10.0.0.2:8080"},
		},
	}
	for i, routes := range corpus {
		cfg, err := buildDynamicConfig(routes)
		require.NoError(t, err, "corpus %d", i)
		assert.NoError(t, validateDynamicConfig(cfg), "corpus %d: the generated snapshot must pass the precheck", i)
	}
	// 空集快照（emptyDynamicConfig 形态）同样过预检。
	assert.NoError(t, validateDynamicConfig(emptyDynamicConfig()))
}

// P9 预检红面四类：非 JSON / 未知字段（形态漂移）/ 规则越语法 / 引用
// 悬空 / URL 形态错——各给精确原因。
func TestPrecheckRejectsCorruptedSnapshots(t *testing.T) {
	err := validateDynamicConfig([]byte("not-json"))
	assert.ErrorContains(t, err, "not valid dynamic config")

	err = validateDynamicConfig([]byte(`{"http":{"routers":{"r1":{"rule":"Host(` + "`a`" + `)","service":"s1","entryPoints":["web"]}},"services":{"s1":{"loadBalancer":{"servers":[{"url":"http://10.0.0.1:80"}]}}}}}`))
	assert.ErrorContains(t, err, "not valid dynamic config", "unknown fields (the generator never emits entryPoints) are shape drift and must fail strictly")

	err = validateDynamicConfig([]byte(`{"http":{"routers":{"r1":{"rule":"HostRegexp(` + "`{a:.+}`" + `)","service":"s1"}},"services":{"s1":{"loadBalancer":{"servers":[{"url":"http://10.0.0.1:80"}]}}}}}`))
	assert.ErrorContains(t, err, "outside the generated grammar", "a rule the generator never emits is a leak surface")

	err = validateDynamicConfig([]byte(`{"http":{"routers":{"r1":{"rule":"Host(` + "`a`" + `)","service":"missing"}},"services":{"s1":{"loadBalancer":{"servers":[{"url":"http://10.0.0.1:80"}]}}}}}`))
	assert.ErrorContains(t, err, "references missing service missing")

	err = validateDynamicConfig([]byte(`{"tcp":{"routers":{"r1":{"rule":"HostSNI(` + "`a`" + `)","service":"s1"}},"services":{"s1":{"loadBalancer":{"servers":[{"url":"http://10.0.0.1:5432"}]}}}}}`))
	assert.ErrorContains(t, err, "invalid url", "tcp services only accept tcp:// backends")
}

// P9 语义闭环：预检红 → PublishRoutes 拒绝且快照不换（控制面
// last-known-good；等价于 traefik 侧拒载但无 5s poll 窗口与静默面）。
// 预检失败需注入生成器缺陷——经 test 包内直接调 validateDynamicConfig
// 与快照保持断言承载（PublishRoutes 的拒绝路径由 UnresolvedBackend 案
// 覆盖同一出口）。
func TestPrecheckFailureKeepsLastGoodSnapshot(t *testing.T) {
	p, err := New("http://fleetlyd:9082/proxy/config", "", "")
	require.NoError(t, err)
	good := []capability.Route{{Host: "good.127.0.0.1.sslip.io", Process: "web", Port: 80,
		Protocol: capability.ProtocolHTTP, BackendAddr: "10.0.0.1:80"}}
	require.NoError(t, p.PublishRoutes(context.Background(), good))
	before := string(p.ConfigSnapshot())

	// 生成器缺陷注入：同包直改快照走不进 PublishRoutes，故以"预检红料
	// 走 PublishRoutes 同一出口"的最近形态——未解析后端在 build 段即拒
	//（拒绝出口同一函数，语义等价：错误返回、快照不动）。
	err = p.PublishRoutes(context.Background(), []capability.Route{{Host: "bad.127.0.0.1.sslip.io", Process: "web", Port: 80}})
	assert.Error(t, err)
	assert.Equal(t, before, string(p.ConfigSnapshot()), "a rejected publication must leave the served snapshot untouched")
}
