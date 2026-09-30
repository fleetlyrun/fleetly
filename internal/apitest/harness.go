// Package apitest 是进程内 API 集成夹具（架构 §11 e2e 分层：真装配
// engine+SQLite+API 面 + bufconn 传输 + 假 Provider 底座——hermetic 与
// dind e2e 之间的中间层；CLI golden 复用本夹具跑真 RPC）。
package apitest

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// Harness 是进程内全链夹具。
type Harness struct {
	DB      *state.DB
	Engine  *engine.Engine
	Runtime *FakeRuntime
	Clock   *statetest.FakeClock
	Conn    *grpc.ClientConn // bufconn 连接（类型化客户端的底座）

	listener *bufconn.Listener
	server   *grpc.Server
}

// New 装配夹具：真 SQLite（temp）+ 真 engine（假 Runtime）+ 真 gRPC 服务
// + bufconn。Edge/Builder/Cipher 不装配（对应面停用是诚实态）。
func New(t testing.TB) *Harness {
	t.Helper()
	ctx := context.Background()
	db, clock := statetest.New(t)
	rt := &FakeRuntime{obs: make(chan capability.WorkloadEvent, 64)}
	log := slog.New(slog.DiscardHandler)
	eng := engine.New(engine.Deps{DB: db, Runtime: rt, Logger: log}, engine.Options{})
	eng.Start(ctx)
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	services := fleetlygrpc.NewServices(db, eng, nil, rt, log)
	srv := grpc.NewServer()
	fleetlygrpc.RegisterAll(srv, services)
	listener := bufconn.Listen(64 * 1024)
	go func() { _ = srv.Serve(listener) }() //nolint:errcheck // 测试收尾统一关闭
	t.Cleanup(srv.Stop)

	return &Harness{
		DB: db, Engine: eng, Runtime: rt, Clock: clock,
		Conn:     dialBufconn(t, listener),
		listener: listener, server: srv,
	}
}

// dialBufconn 建立 bufconn gRPC 连接。
func dialBufconn(t testing.TB, l *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return l.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("apitest: bufconn dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// FakeRuntime 是假 Runtime 底座（Ensure 记录、观测事件由测试注入）。
type FakeRuntime struct {
	mu      sync.Mutex
	ensures []EnsureCall
	obs     chan capability.WorkloadEvent
}

// EnsureCall 是一次 Ensure 记录。
type EnsureCall struct {
	NS   capability.NamespaceRef
	Gen  capability.Generation
	Spec map[string]capability.Workload
}

func (f *FakeRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "apitest", Capability: capability.KindRuntime, Version: "test"}
}

func (f *FakeRuntime) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *FakeRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, _ capability.Materials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	spec := make(map[string]capability.Workload, len(ws))
	for _, w := range ws {
		spec[w.Process] = w
	}
	f.ensures = append(f.ensures, EnsureCall{NS: ns, Gen: gen, Spec: spec})
	return nil
}

func (f *FakeRuntime) Remove(context.Context, capability.NamespaceRef) error { return nil }

func (f *FakeRuntime) Watch(context.Context) (<-chan capability.WorkloadEvent, error) {
	return f.obs, nil
}

func (f *FakeRuntime) Addresses(context.Context, capability.NamespaceRef) ([]capability.Endpoint, error) {
	return nil, nil
}

func (f *FakeRuntime) DescribeCluster(context.Context) (capability.ClusterView, error) {
	return capability.ClusterView{Nodes: []capability.NodeView{{
		NodeID: "01JD0NODE00000000000000000", CarrierID: "apimanager", Role: "manager", Available: true,
	}}}, nil
}

func (f *FakeRuntime) Enrollment(context.Context) (capability.EnrollKit, error) {
	return capability.EnrollKit{Command: "docker swarm join --token TESTTOKEN 127.0.0.1:2377"}, nil
}

// Calls 返回 Ensure 记录快照。
func (f *FakeRuntime) Calls() []EnsureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]EnsureCall, len(f.ensures))
	copy(out, f.ensures)
	return out
}

// ReportRunning 注入 running 观测。
func (f *FakeRuntime) ReportRunning(workloadID string, gen capability.Generation) {
	f.obs <- capability.WorkloadEvent{WorkloadID: workloadID, Generation: gen, State: capability.WorkloadRunning}
}

var _ capability.Runtime = (*FakeRuntime)(nil)
