// Package apitest 是进程内 API 集成夹具（架构 §11 e2e 分层：真装配
// engine+SQLite+API 面 + bufconn 传输 + 假 Provider 底座——hermetic 与
// dind e2e 之间的中间层；CLI golden 复用本夹具跑真 RPC）。
package apitest

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	grpcapiinterceptor "github.com/lynx-go/grpcapi/interceptor"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/api/systemgrpc"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/governance"
	"github.com/fleetlyrun/fleetly/internal/idem"
	"github.com/fleetlyrun/fleetly/internal/material"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// Harness 是进程内全链夹具。
type Harness struct {
	DB       *state.DB
	Engine   *engine.Engine
	Runtime  *FakeRuntime
	Builder  *FakeBuilder
	Registry *FakeRegistry
	Clock    *statertest.FakeClock
	Conn     *grpc.ClientConn      // bufconn 连接（类型化客户端的底座）
	Token    string                // Bootstrap Token 明文（owner 全权；CLI golden 夹具经 FLEETLY_TOKEN 注入）
	DataRoot string                // 数据根（构建上下文预置等夹具操作用）
	Services *fleetlygrpc.Services // 服务依赖集（REST/SSE 冒烟构造原生入口用）

	listener *bufconn.Listener
	server   *grpc.Server
}

// DialOpts 返回 SDK 拨号选项（CLI golden 夹具把 dialClient 接缝指向
// bufconn：fleetly.Dial("bufnet", h.DialOpts()...)）。
func (h *Harness) DialOpts() []sdk.Option {
	return []sdk.Option{sdk.WithDialOptions(
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return h.listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)}
}

// New 装配夹具（自动驱动形态：engine 真实节拍收敛）。
func New(t testing.TB) *Harness {
	return newHarness(t, true)
}

// NewManual 装配夹具（手动驱动形态：不启动收敛循环，测试经 Drive 控制
// 状态推进——golden 确定性）。
func NewManual(t testing.TB) *Harness {
	return newHarness(t, false)
}

// Drive 手动驱动一轮收敛。
func (h *Harness) Drive(ctx context.Context) {
	h.Engine.DriveOnce(ctx)
}

func newHarness(t testing.TB, autostart bool) *Harness {
	t.Helper()
	ctx := context.Background()
	db, clock := statertest.New(t)
	cipher, err := material.LoadCipher(t.TempDir())
	if err != nil {
		t.Fatalf("apitest: master key: %v", err)
	}
	rt := &FakeRuntime{obs: make(chan capability.WorkloadEvent, 64)}
	fb := &FakeBuilder{}
	freg := &FakeRegistry{}
	log := slog.New(slog.DiscardHandler)
	dataRoot := t.TempDir()
	// 假 Builder + 假 Registry + 数据根：git 触发链的 building→releasing
	// 推进底座（检出目录由测试预置跳过真实 clone；推送目标=假受管仓库）。
	// Builder 家族三名全在册同一 fake（ADR-0032 路由面——Calls() 捕获路由
	// 名与 strategy 载荷，全链用例按 spec 的 builder 分派）。
	eng := engine.New(engine.Deps{DB: db, Runtime: rt, Cipher: cipher,
		Builders: map[string]capability.Builder{
			specir.BuilderDockerfile: fb,
			specir.BuilderRailpack:   fb,
			specir.BuilderStatic:     fb,
		}, Registry: freg, Logger: log},
		engine.Options{DataRoot: dataRoot})
	if autostart {
		eng.Start(ctx)
		t.Cleanup(func() { _ = eng.Stop(ctx) })
	} else {
		eng.StartWatch(ctx) // 观测面照常消费（手动形态只省收敛节拍）
		t.Cleanup(func() { _ = eng.Stop(ctx) })
	}

	// 身份面与生产同源：首启流（种子 + bootstrap Token）→ 执法链
	//（policy + authn 拦截器）→ fail-closed 断言。夹具不经 lynx，但执法
	// 链必须与 NewGRPCServer 同构，否则测试面与生产面漂移。
	boot, err := authn.EnsureBootstrapToken(ctx, db, dataRoot, assembly.ScopeResources(), log)
	if err != nil {
		t.Fatalf("apitest: bootstrap: %v", err)
	}
	policySet, err := assembly.NewPolicySet()
	if err != nil {
		t.Fatalf("apitest: policy set: %v", err)
	}
	authenticator := authn.NewAuthenticator(db, policySet, assembly.ScopeResources(), log)
	enforcer := idem.NewEnforcer(db, log)
	limiter := governance.NewRateLimiter(db.Clock(), governance.DefaultRateWindow, governance.DefaultRateBudget)
	freeze := governance.NewFreezeGuard(db, log)
	unary, stream, err := assembly.NewInterceptors(policySet, authenticator, enforcer, limiter, freeze)
	if err != nil {
		t.Fatalf("apitest: interceptors: %v", err)
	}

	services := fleetlygrpc.NewServices(db, eng, cipher, rt, dataRoot, assembly.ScopeResources(), log)
	// 传输层选项与生产服务器同源（收包限额等，assembly.GRPCServerOptions
	// 单一真源）——夹具缺同款限额会让大请求面（如 webhook payload）的
	// 测试结果与生产漂移。
	srv := grpc.NewServer(append(assembly.GRPCServerOptions(),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	)...)
	fleetlygrpc.RegisterAll(srv, services)
	// SystemService 与生产 NewGRPCServer 同挂（此前夹具缺席——schema/
	// explain 自描述面需要全量注册贡献，夹具必须链到 assembly 同一面）。
	systemv1.RegisterSystemServiceServer(srv, systemgrpc.New(buildinfo.BuildInfo{Version: "0.1.0-test"}))
	if err := grpcapiinterceptor.AssertAllRegisteredHavePolicy(srv, policySet); err != nil {
		t.Fatalf("apitest: policy coverage: %v", err)
	}
	listener := bufconn.Listen(64 * 1024)
	go func() { _ = srv.Serve(listener) }() //nolint:errcheck // 测试收尾统一关闭
	t.Cleanup(srv.Stop)

	return &Harness{
		DB: db, Engine: eng, Runtime: rt, Builder: fb, Registry: freg, Clock: clock,
		Conn: dialBufconn(t, listener), Token: boot.Secret, DataRoot: dataRoot,
		Services: services,
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
	mu          sync.Mutex
	ensures     []EnsureCall
	removed     []capability.NamespaceRef // Remove 记录（ADR-0023 收口断言）
	enrollCalls []bool                    // Enrollment 调用记录（rotate 序列，C3）
	adminErr    error                     // RuntimeAdmin 动词注入错误（SetAdminErr 设置）
	adminOps    []AdminCall
	obs         chan capability.WorkloadEvent

	// removeBlock 非空时 Remove 阻塞直至关闭或 ctx 取消（teardown 竞态
	// 注入；与 engine 测试假底座的 Ensure blockPoint 同款）。仅经
	// ArmRemoveBlock 在启动 goroutine 前布置，免锁读写与该模式一致。
	removeBlock chan struct{}
	// removeEntered 非空时 Remove 入口非阻塞发信号（探知 teardown 已停
	// 在 removeBlock）。
	removeEntered chan struct{}
}

// AdminCall 是一次 RuntimeAdmin 动词记录。
type AdminCall struct {
	Verb   string // drain | cordon | uncordon
	NodeID string
}

// EnsureCall 是一次 Ensure 记录。
type EnsureCall struct {
	NS        capability.NamespaceRef
	Gen       capability.Generation
	Spec      map[string]capability.Workload
	Materials capability.Materials // secret 文件注入等分发材料（job 面断言用）
}

func (f *FakeRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "apitest", Capability: capability.KindRuntime, Version: "test"}
}

func (f *FakeRuntime) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *FakeRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	spec := make(map[string]capability.Workload, len(ws))
	for _, w := range ws {
		spec[w.Process] = w
	}
	f.ensures = append(f.ensures, EnsureCall{NS: ns, Gen: gen, Spec: spec, Materials: m})
	return nil
}

func (f *FakeRuntime) Remove(ctx context.Context, ns capability.NamespaceRef) error {
	if f.removeEntered != nil {
		select {
		case f.removeEntered <- struct{}{}:
		default:
		}
	}
	if f.removeBlock != nil {
		select {
		case <-f.removeBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ns)
	return nil
}

// ArmRemoveBlock 使下一次 Remove 阻塞在注入点，返回入口信号与解除函数。
// DeleteApp 拒删竞态的确定性交错面：探知 teardown 已停在 Remove（appMu
// 仍被持有）后布置中间态，消除调度骰子（ADR-0023 修订回归）。
func (f *FakeRuntime) ArmRemoveBlock() (entered <-chan struct{}, release func()) {
	f.removeEntered = make(chan struct{})
	f.removeBlock = make(chan struct{})
	return f.removeEntered, func() { close(f.removeBlock) }
}

// Removed 返回 Remove 记录快照（ADR-0023 收口断言）。
func (f *FakeRuntime) Removed() []capability.NamespaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.NamespaceRef, len(f.removed))
	copy(out, f.removed)
	return out
}

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

func (f *FakeRuntime) Enrollment(_ context.Context, rotate bool) (capability.EnrollKit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enrollCalls = append(f.enrollCalls, rotate)
	return capability.EnrollKit{Command: "docker swarm join --token TESTTOKEN 127.0.0.1:2377"}, nil
}

// EnrollCalls 返回 Enrollment 调用记录（rotate 标志序列；C3 断言面）。
func (f *FakeRuntime) EnrollCalls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bool, len(f.enrollCalls))
	copy(out, f.enrollCalls)
	return out
}

// Calls 返回 Ensure 记录快照。
func (f *FakeRuntime) Calls() []EnsureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]EnsureCall, len(f.ensures))
	copy(out, f.ensures)
	return out
}

// SetAdminErr 注入 RuntimeAdmin 动词错误（测错误映射；调用前设置）。
func (f *FakeRuntime) SetAdminErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adminErr = err
}

// AdminCalls 返回 RuntimeAdmin 动词记录快照。
func (f *FakeRuntime) AdminCalls() []AdminCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AdminCall, len(f.adminOps))
	copy(out, f.adminOps)
	return out
}

// recordAdmin 记录并返回注入错误（RuntimeAdmin 子面）。
func (f *FakeRuntime) recordAdmin(verb, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adminOps = append(f.adminOps, AdminCall{Verb: verb, NodeID: nodeID})
	return f.adminErr
}

// Drain 实现 RuntimeAdmin 子面。
func (f *FakeRuntime) Drain(_ context.Context, nodeID string) error {
	return f.recordAdmin("drain", nodeID)
}

// Cordon 实现 RuntimeAdmin 子面。
func (f *FakeRuntime) Cordon(_ context.Context, nodeID string) error {
	return f.recordAdmin("cordon", nodeID)
}

// Uncordon 实现 RuntimeAdmin 子面。
func (f *FakeRuntime) Uncordon(_ context.Context, nodeID string) error {
	return f.recordAdmin("uncordon", nodeID)
}

// ReportRunning 注入 running 观测。
func (f *FakeRuntime) ReportRunning(workloadID string, gen capability.Generation) {
	f.obs <- capability.WorkloadEvent{WorkloadID: workloadID, Generation: gen, State: capability.WorkloadRunning}
}

// ReportStopped 注入 stopped 观测（Run 排空收口的终态确认面，F1.5/F1.6）。
func (f *FakeRuntime) ReportStopped(workloadID string, gen capability.Generation) {
	f.obs <- capability.WorkloadEvent{WorkloadID: workloadID, Generation: gen, State: capability.WorkloadStopped}
}

// ReportCompleted 注入 one-shot 完成观测（exit 0，ADR-0025 决策 2）。
func (f *FakeRuntime) ReportCompleted(workloadID string, gen capability.Generation) {
	code := 0
	f.obs <- capability.WorkloadEvent{WorkloadID: workloadID, Generation: gen, State: capability.WorkloadCompleted, ExitCode: &code}
}

// ReportFailed 注入 one-shot 失败观测（exit 非 0）。
func (f *FakeRuntime) ReportFailed(workloadID string, gen capability.Generation, exitCode int) {
	f.obs <- capability.WorkloadEvent{WorkloadID: workloadID, Generation: gen, State: capability.WorkloadFailed, ExitCode: &exitCode}
}

// StreamLogs 实现 RuntimeLogs 子面（固定两帧——logs golden 的确定性底座；
// Follow 不实现：测试只用非 follow 形态）。
func (f *FakeRuntime) StreamLogs(_ context.Context, q capability.LogQuery, w capability.LogWriter) error {
	for i := 1; i <= 2; i++ {
		if err := w.WriteLog(context.Background(), capability.LogFrame{
			WorkloadID: q.Namespace.App + "-web",
			Container:  "web",
			Time:       time.Unix(1767225600, 0).UTC(),
			Line:       []byte(fmt.Sprintf("frame-%d", i)),
		}); err != nil {
			return err
		}
	}
	return nil
}

// FakeBuilder 是假构建底座（git 触发链的 building 推进用；digest 固定、
// 记录调用）。构建上下文由测试预置（跳过真实 clone）。
type FakeBuilder struct {
	mu    sync.Mutex
	calls []capability.BuildRequest
}

// FakeBuildDigest 是假构建产物 digest。
const FakeBuildDigest = "sha256:apitest-built"

func (f *FakeBuilder) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "apitest-builder", Capability: capability.KindBuilder, Version: "test"}
}

func (f *FakeBuilder) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *FakeBuilder) Build(_ context.Context, req capability.BuildRequest, _ capability.LogWriter) (capability.BuildResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	return capability.BuildResult{Digest: FakeBuildDigest}, nil
}

// Calls 返回构建调用记录快照。
func (f *FakeBuilder) Calls() []capability.BuildRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.BuildRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

var (
	_ capability.Runtime      = (*FakeRuntime)(nil)
	_ capability.RuntimeLogs  = (*FakeRuntime)(nil)
	_ capability.RuntimeAdmin = (*FakeRuntime)(nil)
	_ capability.Builder      = (*FakeBuilder)(nil)
	_ capability.Registry     = (*FakeRegistry)(nil)
)

// FakeRegistryAddr 是 apitest 假受管仓库地址（引用形态断言锚）。
const FakeRegistryAddr = "reg.apitest:5000"

// FakeRegistry 是假受管仓库底座（engine 构建链的推送目标；地址/凭证固定）。
type FakeRegistry struct{}

func (f *FakeRegistry) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "apitest-registry", Capability: capability.KindRegistry, Version: "test"}
}

func (f *FakeRegistry) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *FakeRegistry) Endpoint(context.Context) (capability.RegistryEndpoint, error) {
	return capability.RegistryEndpoint{
		Addr: FakeRegistryAddr,
		Cred: capability.RegistryCredential{Server: FakeRegistryAddr, Username: "fleetly", Secret: "apitest"},
	}, nil
}
