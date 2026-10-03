package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// fakeEdge 是 Edge 端口假底座（受管形态 + 配置快照面）。
type fakeEdge struct {
	published []capability.Route
	fail      bool
	ws        []capability.Workload // 非空时覆盖默认受管 workload 集
}

func (f *fakeEdge) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-edge", Capability: capability.KindEdge, Managed: true}
}
func (f *fakeEdge) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeEdge) PublishRoutes(_ context.Context, routes []capability.Route) error {
	if f.fail {
		return assert.AnError
	}
	f.published = routes
	return nil
}
func (f *fakeEdge) IssueCertificate(context.Context, capability.CertificateRequest, capability.ChallengeWriter) (capability.CertificateStatus, error) {
	return capability.CertificateStatus{}, nil
}
func (f *fakeEdge) ManagedWorkloads() []capability.Workload {
	if len(f.ws) > 0 {
		return f.ws
	}
	return []capability.Workload{{ID: "fleetly-edge-fake", Process: "edge", Image: "fake/edge:1", Replicas: 1}}
}
func (f *fakeEdge) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "edge"}
}

var (
	_ capability.Edge    = (*fakeEdge)(nil)
	_ capability.Managed = (*fakeEdge)(nil)
)

// 受管 reconciler：Ensure 到系统域 + Generation 推进 + 归属登记。
func TestManagedReconcilerEnsuresSystemNamespace(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	edge := &fakeEdge{}
	e := New(Deps{DB: db, Runtime: rt, Edge: edge, Logger: discardLogger()}, Options{})
	ctx := context.Background()

	e.managedStep(ctx)
	e.managedStep(ctx) // 幂等：再次收敛不产生状态回退

	calls := rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, "fleetly", last.NS.Team)
	assert.Equal(t, "system", last.NS.Project)
	assert.Equal(t, "fleetly-edge-fake", last.Spec["edge"].ID)
	assert.Equal(t, "fake/edge:1", last.Spec["edge"].Image)
	assert.Greater(t, uint64(last.Gen), uint64(0))
}

// 受管 Generation 幂等语义（staging 真机 2026-09-30 教训：逐 tick +1 让
// 受管 traefik 滚动替换繁殖到 115 实例打穿内存）：spec 未变 → 多 tick 同
// gen（载体 no-op）；spec 变化 → gen 推进一次后在新值上稳定。
// N1 C16 追加：签名未变拍直接跳过 Ensure 调用（收敛终态不变，稳态零
// swarm API 压力），变化拍恰好一次下发。
func TestManagedGenerationStableAcrossTicks(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	edge := &fakeEdge{}
	e := New(Deps{DB: db, Runtime: rt, Edge: edge, Logger: discardLogger()}, Options{})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		e.managedStep(ctx)
	}
	calls := rt.calls()
	require.Len(t, calls, 1, "unchanged managed spec must short-circuit to a single Ensure (C16)")
	assert.Equal(t, capability.Generation(1), calls[0].Gen)

	// spec 变化（受管 workload 镜像升级）→ 恰好推进一次，随后稳定。
	edge.ws = []capability.Workload{{ID: "fleetly-edge-fake", Process: "edge", Image: "fake/edge:2", Replicas: 1}}
	for i := 0; i < 3; i++ {
		e.managedStep(ctx)
	}
	calls = rt.calls()
	require.Len(t, calls, 2, "spec change must produce exactly one new Ensure")
	assert.Greater(t, uint64(calls[1].Gen), uint64(calls[0].Gen), "spec change must advance the generation")
	assert.Equal(t, "fake/edge:2", calls[1].Spec["edge"].Image)
}

// N1 C16：受管 Ensure 签名覆盖材料面——MaterialsSource 材料变化（ws 不变）
// 也必须短路失效重下发（zot 配置/htpasswd 轮换的收敛通道）。
func TestManagedEnsureMaterialsChangeForcesEnsure(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	reg := newFakeRegistry()
	reg.managed = true
	reg.materials = capability.Materials{SecretFiles: map[string][]byte{"zot-config": []byte(`{"v":1}`)}}
	e := New(Deps{DB: db, Runtime: rt, Edge: &fakeEdge{}, Registry: reg, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	// 双 Provider（edge+registry）：首轮各一次 Ensure。
	e.managedStep(ctx)
	e.managedStep(ctx)
	require.Len(t, rt.calls(), 2, "stable inputs must short-circuit after the first tick")

	// 材料变化（zot 配置重渲染）→ 仅 registry 域短路失效，恰好一次重下发
	// 且带新材料。
	reg.materials = capability.Materials{SecretFiles: map[string][]byte{"zot-config": []byte(`{"v":2}`)}}
	e.managedStep(ctx)
	calls := rt.calls()
	require.Len(t, calls, 3, "materials change must invalidate only the registry domain's short-circuit")
	assert.Equal(t, []byte(`{"v":2}`), calls[2].Materials.SecretFiles["zot-config"])
	assert.Equal(t, "fleetly", calls[2].NS.Team)
	assert.Equal(t, "registry", calls[2].NS.App)
}

// N1 C16：Ensure 失败不落签名——下一拍重试（失败重放语义不变）。
func TestManagedEnsureFailureRetriesNextTick(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	rt.failNext = true
	e := New(Deps{DB: db, Runtime: rt, Edge: &fakeEdge{}, Logger: discardLogger()}, Options{})
	ctx := context.Background()

	e.managedStep(ctx) // Ensure 失败
	e.managedStep(ctx) // 失败拍不落签名：重试成功
	e.managedStep(ctx) // 成功后签名生效：短路
	calls := rt.calls()
	require.Len(t, calls, 2, "failed ensure must retry next tick, then short-circuit")
}

// 受管收敛步带界（staging 实证 2026-09-30：docker daemon 重启窗口的 API
// hang 把无界单写者循环永久卡死，静默直至进程重启）：Runtime Ensure 挂起
// 时 managedStep 必须在 ManagedStepTimeout 内返回，下一拍重试。
func TestManagedStepBoundedWhenRuntimeHangs(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	rt.blockPoint = make(chan struct{}) // 永不关闭 = Ensure 永久挂起
	e := New(Deps{DB: db, Runtime: rt, Edge: &fakeEdge{}, Logger: discardLogger()},
		Options{ManagedStepTimeout: 50 * time.Millisecond})
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		e.managedStep(ctx)
		close(done)
	}()
	select {
	case <-done:
		// 带界返回：收敛步被 deadline 切断，循环存活（下一拍重试）。
	case <-time.After(2 * time.Second):
		t.Fatal("managedStep must be bounded when the runtime hangs")
	}
}

// Route 发布：后端地址经 Runtime.Addresses 解析填充（Addresses 假底座
// 返回 VIP），未解析的 Route 跳过不阻断全量发布。
func TestRoutePublishResolvesBackends(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	edge := &fakeEdge{}
	e := New(Deps{DB: db, Runtime: rt, Edge: edge, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	// Team 轴接实（ADR-0028）：后端域解析从 Project 行实取团队。
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))

	// web process 的后端地址（VIP 形态）。
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()

	require.NoError(t, e.routes.Create(ctx, db.Runner(), &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolH2C, TLSMode: "auto",
	}))
	// 后端解析不到（process 无地址）：跳过。
	require.NoError(t, e.routes.Create(ctx, db.Runner(), &route.Route{
		ID: "01JD0ROUTE00000000000000002", ProjectID: tProjectID,
		Host: "ghost.127.0.0.1.sslip.io", AppID: tAppID, Process: "ghost", Port: 9999,
		Protocol: capability.ProtocolHTTP, TLSMode: "none",
	}))

	e.publishRoutes(ctx)
	require.Len(t, edge.published, 1, "unresolvable route is skipped, the rest still publish")
	got := edge.published[0]
	assert.Equal(t, "shop.127.0.0.1.sslip.io", got.Host)
	assert.NotEmpty(t, got.BackendAddr, "backend address must be resolved via Runtime.Addresses")
	assert.Equal(t, capability.ProtocolH2C, got.Protocol)
}

// N1 C16b：行集指纹未变 → 跳过 per-Route Addresses 解析与发布（稳态零
// 解析压力）；行集变化 → 恰好一次全量重发布。
func TestPublishRoutesShortCircuitsWhenUnchanged(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	edge := &fakeEdge{}
	e := New(Deps{DB: db, Runtime: rt, Edge: edge, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()
	require.NoError(t, e.routes.Create(ctx, db.Runner(), &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))

	e.publishRoutes(ctx)
	require.Len(t, edge.published, 1)
	first := len(rt.addrCallsSnapshot())

	e.publishRoutes(ctx) // 行集未变：短路
	assert.Len(t, rt.addrCallsSnapshot(), first, "unchanged route set must not re-resolve backends")
	e.publishRoutes(ctx)
	assert.Len(t, rt.addrCallsSnapshot(), first, "the short circuit must hold across ticks")

	// 行集变化（新增一行）→ 恰好一次全量重发布。
	require.NoError(t, e.routes.Create(ctx, db.Runner(), &route.Route{
		ID: "01JD0ROUTE00000000000000002", ProjectID: tProjectID,
		Host: "api.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "none",
	}))
	e.publishRoutes(ctx)
	assert.Greater(t, len(rt.addrCallsSnapshot()), first, "route-set change must re-resolve")
	require.Len(t, edge.published, 2, "the new route must join the full publish")
}

// N1 C16b：PublishRoutesNow 即时触发绕过短路（API 写路径契约）——后端
// 地址漂移不在行集指纹内，靠强制通道即时收敛（Dead backend 撤流同通道）。
func TestPublishRoutesForcedBypassResolvesFreshBackends(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	edge := &fakeEdge{}
	e := New(Deps{DB: db, Runtime: rt, Edge: edge, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()
	require.NoError(t, e.routes.Create(ctx, db.Runner(), &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))
	e.publishRoutes(ctx)
	require.Len(t, edge.published, 1)
	require.Equal(t, "10.0.0.2:8080", edge.published[0].BackendAddr)

	// 后端地址漂移（VIP 变化）：行集未变 → 短路保持（滞后有界）。
	rt.mu.Lock()
	rt.endpoints["default/"+tProjectID+"/"+tAppID] = []capability.Endpoint{{Addr: "10.0.0.9", Process: "web", Port: 8080}}
	rt.mu.Unlock()
	e.publishRoutes(ctx)
	require.Len(t, rt.addrCallsSnapshot(), 1, "unchanged route set short-circuits")
	assert.Equal(t, "10.0.0.2:8080", edge.published[0].BackendAddr, "stale target persists until forced")

	// PublishRoutesNow → 绕过短路 → 新地址即时进发布集。
	e.PublishRoutesNow()
	e.publishRoutes(ctx)
	assert.Greater(t, len(rt.addrCallsSnapshot()), 1, "the forced publish must re-resolve backends")
	require.Len(t, edge.published, 1)
	assert.Equal(t, "10.0.0.9:8080", edge.published[0].BackendAddr, "forced publish converges address drift")
}

// N1 C16b：强制重放节拍兜底环外变更——Dead backend（Addresses 解析不到）
// 的撤流降级在节拍窗口内收敛（即时通道之外的自愈面，滞后有界终态同）。
func TestPublishRoutesReplayWindowDropsDeadBackend(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	edge := &fakeEdge{}
	e := New(Deps{DB: db, Runtime: rt, Edge: edge, Logger: discardLogger()},
		Options{ReconcileReplayInterval: 30 * time.Second})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	rt.mu.Lock()
	rt.endpoints = map[string][]capability.Endpoint{
		"default/" + tProjectID + "/" + tAppID: {{Addr: "10.0.0.2", Process: "web", Port: 8080}},
	}
	rt.mu.Unlock()
	require.NoError(t, e.routes.Create(ctx, db.Runner(), &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))
	e.publishRoutes(ctx)
	require.Len(t, edge.published, 1)

	// 后端消失（载体已拆）：行集未变 → 短路保持（配置滞后有界）。
	rt.mu.Lock()
	delete(rt.endpoints, "default/"+tProjectID+"/"+tAppID)
	rt.mu.Unlock()
	e.publishRoutes(ctx)
	require.Len(t, edge.published, 1, "the skipped tick keeps the last good config")

	// 节拍到 → 强制全量重发布 → Dead backend 撤流（诚实降级语义不变）。
	clock.Advance(31 * time.Second)
	e.publishRoutes(ctx)
	assert.Empty(t, edge.published, "the replay window must converge the dead-backend withdrawal")
}
