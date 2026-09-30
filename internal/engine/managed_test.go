package engine

import (
	"context"
	"testing"

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
