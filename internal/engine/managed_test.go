package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
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
	require.Len(t, calls, 5)
	for _, c := range calls {
		assert.Equal(t, calls[0].Gen, c.Gen, "unchanged managed spec must reuse the same generation every tick")
	}

	// spec 变化（受管 workload 镜像升级）→ 恰好推进一次，随后稳定。
	edge.ws = []capability.Workload{{ID: "fleetly-edge-fake", Process: "edge", Image: "fake/edge:2", Replicas: 1}}
	for i := 0; i < 3; i++ {
		e.managedStep(ctx)
	}
	calls = rt.calls()
	require.Len(t, calls, 8)
	assert.Greater(t, calls[5].Gen, calls[4].Gen, "spec change must advance the generation")
	assert.Equal(t, calls[5].Gen, calls[6].Gen, "new spec must stabilize on the new generation")
	assert.Equal(t, calls[5].Gen, calls[7].Gen)
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
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	edge := &fakeEdge{}
	e := New(Deps{DB: db, Runtime: rt, Edge: edge, Logger: discardLogger()}, Options{})
	ctx := context.Background()

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
