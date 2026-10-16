package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// API 公网发布双路由形态（ADR-0079 决策 2）：主路由 Host-only h2c 整面 +
// 登录路由 Host&&PathPrefix（gRPC method 路径）带 rateLimit（规则更长
// 优先级更高——console 登录路由先例）。
func TestAPICapabilityRoutes(t *testing.T) {
	db, _ := statetest.New(t)
	e := New(Deps{DB: db, Repos: testRepos(db), Logger: discardLogger()}, Options{Data: DataOptions{Api: ApiConfig{
		Host:    "api.dev.fleetly.run",
		GRPCURL: "http://10.124.0.3:9080",
	}}})
	routes := e.apiCapabilityRoutes()
	require.Len(t, routes, 2)

	main, login := routes[0], routes[1]
	assert.Equal(t, "api.dev.fleetly.run", main.Host)
	assert.Empty(t, main.Path, "主路由 Host-only——gRPC 全部 method 整面承接")
	assert.Equal(t, capability.ProtocolH2C, main.Protocol, "后端协议 h2c——fleetlyd gRPC 是明文 HTTP/2")
	assert.Equal(t, "10.124.0.3:9080", main.BackendAddr, "后端 = grpc_url 的 host:port")
	assert.Nil(t, main.RateLimit, "主路由不限速（CLI 单命令即可多次 RPC，整面限速会误伤正常使用）")
	assert.Zero(t, main.Target, "路由指向控制面自身，非租户载体")

	assert.Equal(t, "api.dev.fleetly.run", login.Host)
	assert.Equal(t, APILoginMethodPath, login.Path)
	assert.Equal(t, main.BackendAddr, login.BackendAddr, "同后端——同一 gRPC 监听")
	require.NotNil(t, login.RateLimit)
	assert.Equal(t, consoleLoginRateAverage, login.RateLimit.Average, "与 console REST 登录限速同值（ADR-0061 决策 3）")
	assert.Equal(t, consoleLoginRateBurst, login.RateLimit.Burst)
}

// 零值/半配置 = 面停用（不发布任何路由，现状）；grpc_url 畸形 = 诚实
// 降级零发布（错误日志留痕，不 panic 不半截发布）。
func TestAPICapabilityRoutesDisabledOrMalformed(t *testing.T) {
	db, _ := statetest.New(t)
	for name, opts := range map[string]Options{
		"零值":           {},
		"有 host 无 url": {Data: DataOptions{Api: ApiConfig{Host: "api.dev.fleetly.run"}}},
		"有 url 无 host": {Data: DataOptions{Api: ApiConfig{GRPCURL: "http://10.124.0.3:9080"}}},
		"url 无 host 段": {Data: DataOptions{Api: ApiConfig{Host: "a.test", GRPCURL: "not-a-url"}}},
		"url 仅 scheme": {Data: DataOptions{Api: ApiConfig{Host: "a.test", GRPCURL: "http://"}}},
	} {
		e := New(Deps{DB: db, Repos: testRepos(db), Logger: discardLogger()}, opts)
		assert.Empty(t, e.apiCapabilityRoutes(), "%s：必须零发布", name)
	}
}

// publishRoutes 合并面：API 双路由随全量发布进入 Proxy（与 route 行、
// browse/console 同渠道；进程生命周期内恒定，签名不覆盖——重启即重发）。
func TestPublishRoutesMergesAPIRoutes(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Repos: testRepos(db), Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{
		Data: DataOptions{
			Api: ApiConfig{Host: "api.dev.fleetly.run", GRPCURL: "http://127.0.0.1:9080"},
		},
	})
	ctx := context.Background()

	e.publishRoutes(ctx)
	require.NotEmpty(t, proxy.published)
	var paths []string
	for _, r := range proxy.published {
		if r.Host == "api.dev.fleetly.run" {
			paths = append(paths, r.Path)
		}
	}
	require.Len(t, paths, 2, "主路由（Path 空）与登录路由必须同批发布")
	assert.Contains(t, paths, "", "主路由 Host-only")
	assert.Contains(t, paths, APILoginMethodPath)

	// 再拍：行集与 browse 会话集未变 → 签名短路（API 路由进程内恒定，
	// 不参与签名——重启才可能变更）。
	count := len(proxy.published)
	e.publishRoutes(ctx)
	assert.Len(t, proxy.published, count, "签名未变不得重发布")
}
