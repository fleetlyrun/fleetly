package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// Console 公网发布双路由形态（ADR-0061 决策 2）：主路由 Host-only 整面 +
// 登录路由 Host&&PathPrefix 带 rateLimit（规则更长优先级更高——browse
// entry 先例）。
func TestConsoleCapabilityRoutes(t *testing.T) {
	db, _ := statetest.New(t)
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Logger: discardLogger()}, Options{Data: DataOptions{Console: ConsoleConfig{
		Host:       "console.dev.fleetly.run",
		GatewayURL: "http://10.124.0.3:9081",
	}}})
	routes := e.consoleCapabilityRoutes()
	require.Len(t, routes, 2)

	main, login := routes[0], routes[1]
	assert.Equal(t, "console.dev.fleetly.run", main.Host)
	assert.Empty(t, main.Path, "主路由 Host-only——SPA 深链与任意 /v1 路径整面承接")
	assert.Equal(t, capability.ProtocolHTTP, main.Protocol)
	assert.Equal(t, "10.124.0.3:9081", main.BackendAddr, "后端 = gateway_url 的 host:port")
	assert.Nil(t, main.RateLimit, "主路由不限速（SPA 静态面与轮询工作台不设窗）")
	assert.Zero(t, main.Target, "路由指向控制面自身，非租户载体")

	assert.Equal(t, "console.dev.fleetly.run", login.Host)
	assert.Equal(t, ConsoleLoginPath, login.Path)
	assert.Equal(t, main.BackendAddr, login.BackendAddr, "同后端——同端口同源服务")
	require.NotNil(t, login.RateLimit)
	assert.Equal(t, consoleLoginRateAverage, login.RateLimit.Average)
	assert.Equal(t, consoleLoginRateBurst, login.RateLimit.Burst)
}

// 零值/半配置 = 面停用（不发布任何路由，现状）；gateway_url 畸形 = 诚实
// 降级零发布（错误日志留痕，不 panic 不半截发布）。
func TestConsoleCapabilityRoutesDisabledOrMalformed(t *testing.T) {
	db, _ := statetest.New(t)
	for name, opts := range map[string]Options{
		"零值":           {},
		"有 host 无 url": {Data: DataOptions{Console: ConsoleConfig{Host: "console.dev.fleetly.run"}}},
		"有 url 无 host": {Data: DataOptions{Console: ConsoleConfig{GatewayURL: "http://10.124.0.3:9081"}}},
		"url 无 host 段": {Data: DataOptions{Console: ConsoleConfig{Host: "c.test", GatewayURL: "not-a-url"}}},
		"url 仅 scheme": {Data: DataOptions{Console: ConsoleConfig{Host: "c.test", GatewayURL: "http://"}}},
	} {
		e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Logger: discardLogger()}, opts)
		assert.Empty(t, e.consoleCapabilityRoutes(), "%s：必须零发布", name)
	}
}

// publishRoutes 合并面：Console 双路由随全量发布进入 Proxy（与 route 行、
// browse 会话路由同渠道；进程生命周期内恒定，签名不覆盖——重启即重发）。
func TestPublishRoutesMergesConsoleRoutes(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{
		Data: DataOptions{
			Console: ConsoleConfig{Host: "console.dev.fleetly.run", GatewayURL: "http://127.0.0.1:9081"},
		},
	})
	ctx := context.Background()

	e.publishRoutes(ctx)
	require.NotEmpty(t, proxy.published)
	var hosts []string
	for _, r := range proxy.published {
		if r.Host == "console.dev.fleetly.run" {
			hosts = append(hosts, r.Path)
		}
	}
	require.Len(t, hosts, 2, "主路由（Path 空）与登录路由必须同批发布")
	assert.Contains(t, hosts, "", "主路由 Host-only")
	assert.Contains(t, hosts, ConsoleLoginPath)

	// 再拍：行集与 browse 会话集未变 → 签名短路（Console 路由进程内恒定，
	// 不参与签名——重启才可能变更）。
	count := len(proxy.published)
	e.publishRoutes(ctx)
	assert.Len(t, proxy.published, count, "签名未变不得重发布")
}
