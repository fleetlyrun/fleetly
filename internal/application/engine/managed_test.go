package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// Route 发布面测试（routepublish.go 留内核）。受管 reconciler 的域内行为
// 面（Generation 幂等/材料短路/观测失败跳拍/Placement 钉住/gen 续接）已随
// P5 wave-4 拆包迁入 manageds 应用模块包白盒（通则⑥——断言面零漂移）；本
// 文件持 fakeProxy 假面（publishRoutes 真源在核的测试底座）与发布面。

// fakeProxy 是 Proxy 端口假底座（受管形态 + 配置快照面）。manageds 应用模块
// 包白盒持同源隔离副本（通则⑥）。
type fakeProxy struct {
	published []capability.Route
	fail      bool
	ws        []capability.Workload // 非空时覆盖默认受管 workload 集
}

func (f *fakeProxy) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-proxy", Capability: capability.KindProxy, Managed: true}
}
func (f *fakeProxy) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeProxy) PublishRoutes(_ context.Context, routes []capability.Route) error {
	if f.fail {
		return assert.AnError
	}
	f.published = routes
	return nil
}
func (f *fakeProxy) IssueCertificate(context.Context, capability.CertificateRequest, capability.ChallengeWriter) (capability.CertificateStatus, error) {
	return capability.CertificateStatus{}, nil
}
func (f *fakeProxy) ManagedWorkloads() []capability.Workload {
	if len(f.ws) > 0 {
		return f.ws
	}
	return []capability.Workload{{ID: "fleetly-proxy-fake", Process: "proxy", Image: "fake/proxy:1", Replicas: 1}}
}
func (f *fakeProxy) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "proxy"}
}

var (
	_ capability.Proxy   = (*fakeProxy)(nil)
	_ capability.Managed = (*fakeProxy)(nil)
)

// Route 发布：后端地址经 Runtime.Addresses 解析填充（Addresses 假底座
// 返回 VIP），未解析的 Route 跳过不阻断全量发布。
func TestRoutePublishResolvesBackends(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	// Team 轴接实（ADR-0028）：后端域解析从 Project 行实取团队。
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))

	// web process 的后端地址（VIP 形态）。
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()

	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolH2C, TLSMode: "auto",
	}))
	// 后端解析不到（process 无地址）：跳过。
	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000002", ProjectID: tProjectID,
		Host: "ghost.127.0.0.1.sslip.io", AppID: tAppID, Process: "ghost", Port: 9999,
		Protocol: capability.ProtocolHTTP, TLSMode: "none",
	}))

	e.publishRoutes(ctx)
	require.Len(t, proxy.published, 1, "unresolvable route is skipped, the rest still publish")
	got := proxy.published[0]
	assert.Equal(t, "shop.127.0.0.1.sslip.io", got.Host)
	assert.NotEmpty(t, got.BackendAddr, "backend address must be resolved via Runtime.Addresses")
	assert.Equal(t, capability.ProtocolH2C, got.Protocol)
}

// N1 C16b：行集指纹未变 → 跳过 per-Route Addresses 解析与发布（稳态零
// 解析压力）；行集变化 → 恰好一次全量重发布。
func TestPublishRoutesShortCircuitsWhenUnchanged(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()
	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))

	e.publishRoutes(ctx)
	require.Len(t, proxy.published, 1)
	first := len(rt.addrCallsSnapshot())

	e.publishRoutes(ctx) // 行集未变：短路
	assert.Len(t, rt.addrCallsSnapshot(), first, "unchanged route set must not re-resolve backends")
	e.publishRoutes(ctx)
	assert.Len(t, rt.addrCallsSnapshot(), first, "the short circuit must hold across ticks")

	// 行集变化（新增一行）→ 恰好一次全量重发布。
	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000002", ProjectID: tProjectID,
		Host: "api.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "none",
	}))
	e.publishRoutes(ctx)
	assert.Greater(t, len(rt.addrCallsSnapshot()), first, "route-set change must re-resolve")
	require.Len(t, proxy.published, 2, "the new route must join the full publish")
}

// N1 C16b：PublishRoutesNow 即时触发绕过短路（API 写路径契约）——后端
// 地址漂移不在行集指纹内，靠强制通道即时收敛（Dead backend 撤流同通道）。
func TestPublishRoutesForcedBypassResolvesFreshBackends(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()
	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))
	e.publishRoutes(ctx)
	require.Len(t, proxy.published, 1)
	require.Equal(t, "10.0.0.2:8080", proxy.published[0].BackendAddr)

	// 后端地址漂移（VIP 变化）：行集未变 → 短路保持（滞后有界）。
	rt.mu.Lock()
	rt.endpoints["default/"+tProjectID+"/"+tAppID] = []capability.Endpoint{{Addr: "10.0.0.9", Process: "web", Port: 8080}}
	rt.mu.Unlock()
	e.publishRoutes(ctx)
	require.Len(t, rt.addrCallsSnapshot(), 1, "unchanged route set short-circuits")
	assert.Equal(t, "10.0.0.2:8080", proxy.published[0].BackendAddr, "stale target persists until forced")

	// PublishRoutesNow → 绕过短路 → 新地址即时进发布集。
	e.PublishRoutesNow()
	e.publishRoutes(ctx)
	assert.Greater(t, len(rt.addrCallsSnapshot()), 1, "the forced publish must re-resolve backends")
	require.Len(t, proxy.published, 1)
	assert.Equal(t, "10.0.0.9:8080", proxy.published[0].BackendAddr, "forced publish converges address drift")
}

// N1 C16b：强制重放节拍兜底环外变更——Dead backend（Addresses 解析不到）
// 的撤流降级在节拍窗口内收敛（即时通道之外的自愈面，滞后有界终态同）。
func TestPublishRoutesReplayWindowDropsDeadBackend(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Proxy: proxy, Logger: discardLogger()},
		Options{Ring: RingOptions{ReconcileReplayInterval: 30 * time.Second}})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()
	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))
	e.publishRoutes(ctx)
	require.Len(t, proxy.published, 1)

	// 后端消失（载体已拆）：行集未变 → 短路保持（配置滞后有界）。
	rt.mu.Lock()
	delete(rt.endpoints, "default/"+tProjectID+"/"+tAppID)
	rt.mu.Unlock()
	e.publishRoutes(ctx)
	require.Len(t, proxy.published, 1, "the skipped tick keeps the last good config")

	// 节拍到 → 强制全量重发布 → Dead backend 撤流（诚实降级语义不变）。
	clock.Advance(31 * time.Second)
	e.publishRoutes(ctx)
	assert.Empty(t, proxy.published, "the replay window must converge the dead-backend withdrawal")
}

// H7 发布就绪门（引擎侧）：routeReady=false 时 publishRoutes 不触达 Proxy
// （连后端解析都不做——冷缓存不得产半量快照）；开门后恢复全量发布。
func TestPublishRoutesGatedUntilReady(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()
	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))

	e.routeReady.Store(false)
	e.publishRoutes(ctx)
	assert.Empty(t, proxy.published, "a closed route gate must not publish")
	assert.Empty(t, rt.addrCallsSnapshot(), "a closed route gate must not even resolve backends")

	e.routeReady.Store(true)
	e.publishRoutes(ctx)
	require.Len(t, proxy.published, 1, "the open gate publishes the full snapshot")
}

// H7 启动就绪门：Start 后基线重放在途期间 routeReady=false（发布被抑制）；
// 重放收口即开门（成功或失败都开——否则拉取端点永久 503）。
func TestStartOpensRouteGateAfterBaselineReplay(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	entered, release := rt.ensureGate.Arm()
	e.Start(context.Background())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("startup baseline replay never reached Ensure")
	}
	assert.False(t, e.routeReady.Load(),
		"the route gate must stay closed while the startup baseline replay is in flight")
	release()
	require.Eventually(t, e.routeReady.Load, 5*time.Second, 10*time.Millisecond,
		"the gate must open once the baseline replay returns")
}
