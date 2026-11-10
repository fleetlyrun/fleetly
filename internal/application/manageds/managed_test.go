package manageds

// 受管域应用模块白盒（ADR-0004；P5 wave-4 随域拆包自 engine 包迁入，断言面
// 零漂移）：受管 reconciler（系统域 ns/Generation 幂等/材料短路/失败重
// 试/步带界）、per-域 Generation 续接（F2.5 重启零扰动）、观测失败跳拍
//（P1-5 根修回归）、Placement 钉住、Proxy 挂全部活跃项目网（B1）、双
// Provider 材料（附录 B.1）。
//
// 夹具（browses 同款）：hermetic 应用模块（真 SQLite 台账 repo + Runtime 假面
// + ensure cache 协议端口本地实现）；gensFor 端口持本地 map（核 ensureMu 配对
// 裁定的等价白盒面——惰性建同形）；resolver 端口（activeProjectNetworks/
// activeProjectIDs/controlPlaneNode 的核公式）持等价窄面隔离副本（通则⑥
// ——留核侧 managed_test/managed_proxy_test/rollback_test/registry_project_
// test 持跨域序列与 fakeProxy 隔离副本）；reconcileNodes/publishRoutes 端口
// 消费计数（编排面留核的核侧序列由 engine 测试承载）。

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	"github.com/fleetlyrun/fleetly/internal/testsupport/testruntime"
)

// 测试常量（engine 包同源夹具 ID——断言面零漂移）。
const (
	tProjectID   = "01JD0PROJ00000000000000000"
	tControlNode = "01JD0NODE00000000000000000"
)

// fakeProxy 是 Proxy 端口假底座（受管形态 + 可覆写 workload 集）。engine
// 侧 managed_test.go 持同源隔离副本（通则⑥）。
type fakeProxy struct {
	ws []capability.Workload // 非空时覆盖默认受管 workload 集
}

func (f *fakeProxy) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-proxy", Capability: capability.KindProxy, Managed: true}
}
func (f *fakeProxy) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeProxy) PublishRoutes(context.Context, []capability.Route) error { return nil }
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

// fakeRegistryAddr 是受管仓库地址锚（引用形态断言共用）。
const fakeRegistryAddr = "reg.test:5000"

// fakeRegistry 是 Registry 端口假底座（managed 开启时同时实现 Managed+
// MaterialsSource——reconcileManaged 双 Provider 与材料透传的测试面，形态
// 对齐 zot Provider）。engine 侧 fakeregistry_test.go 持同源隔离副本
// （通则⑥）。
type fakeRegistry struct {
	addr      string
	cred      capability.RegistryCredential
	managed   bool
	materials capability.Materials
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		addr: fakeRegistryAddr,
		cred: capability.RegistryCredential{Server: fakeRegistryAddr, Username: "fleetly", Secret: "test-secret"},
	}
}

func (f *fakeRegistry) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-registry", Capability: capability.KindRegistry, Version: "test", Managed: f.managed}
}

func (f *fakeRegistry) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeRegistry) Endpoint(context.Context) (capability.RegistryEndpoint, error) {
	return capability.RegistryEndpoint{Addr: f.addr, Cred: f.cred}, nil
}

// ManagedNamespace 实现 Managed 扩展接口（fleetly/system/registry——与 zot 同域）。
func (f *fakeRegistry) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"}
}

// ManagedWorkloads 实现 Managed 扩展接口（单 workload 声明）。
func (f *fakeRegistry) ManagedWorkloads() []capability.Workload {
	return []capability.Workload{{
		ID: "fleetly-registry-fake", Process: "zot", Image: "reg.test/zot:vtest", Replicas: 1,
	}}
}

// ManagedMaterials 实现 MaterialsSource 扩展接口。
func (f *fakeRegistry) ManagedMaterials() capability.Materials { return f.materials }

var (
	_ capability.Registry        = (*fakeRegistry)(nil)
	_ capability.Managed         = (*fakeRegistry)(nil)
	_ capability.MaterialsSource = (*fakeRegistry)(nil)
)

// fakeRuntime 是应用模块消费面（Ensure/InspectWorkloads/DescribeCluster）的
// 假底座（engine 侧 fakeruntime_test.go 的等价窄面隔离副本，通则⑥——
// InspectWorkloads 持「该域最近一次 Ensure」语义，P1-5 播种续接测试咬出
// 的真源形态）。
type fakeRuntime struct {
	capability.Runtime
	mu          sync.Mutex
	ensures     []ensureCall
	failNext    bool
	inspectFail bool
	clusterOv   bool
	clusterVw   capability.ClusterView
	ensureGate  testruntime.BlockGate
}

// ensureCall 是一次 Ensure 的记录面。
type ensureCall struct {
	NS        capability.NamespaceRef
	Gen       capability.Generation
	Spec      map[string]capability.Workload
	Materials capability.Materials
}

func (f *fakeRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-runtime", Capability: capability.KindRuntime, Version: "test"}
}

func (f *fakeRuntime) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeRuntime) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (capability.EnsureReport, error) {
	if f.ensureGate.Pass() {
		if err := f.ensureGate.Wait(ctx); err != nil {
			return capability.EnsureReport{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	spec := make(map[string]capability.Workload, len(ws))
	for _, w := range ws {
		spec[w.Process] = w
	}
	f.ensures = append(f.ensures, ensureCall{NS: ns, Gen: gen, Spec: spec, Materials: m})
	if f.failNext {
		f.failNext = false
		return capability.EnsureReport{}, fmt.Errorf("injected ensure failure")
	}
	return capability.EnsureReport{}, nil
}

func (f *fakeRuntime) DescribeCluster(context.Context) (capability.ClusterView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clusterOv {
		return f.clusterVw, nil
	}
	return capability.ClusterView{Nodes: []capability.NodeView{{
		NodeID: tControlNode, CarrierID: "swarmmanager", Role: "manager", Available: true,
	}}}, nil
}

// InspectWorkloads 实现 RuntimeInspector 扩展接口：观测 = 该域最近一次 Ensure
// 的 spec（与真 Provider 的 ServiceList 快照一致——载体持有的是最新 spec，
// 不是下发历史的首笔）。inspectFail 置位时返回错误（P1-5 观测失败缝）。
func (f *fakeRuntime) InspectWorkloads(_ context.Context, ns capability.NamespaceRef) ([]capability.WorkloadObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectFail {
		return nil, fmt.Errorf("injected inspect failure (ns %s)", ns.String())
	}
	var obs []capability.WorkloadObservation
	for _, c := range f.ensures {
		if c.NS.String() != ns.String() {
			continue
		}
		obs = nil
		for _, w := range c.Spec {
			obs = append(obs, capability.WorkloadObservation{
				WorkloadID: w.ID, Generation: c.Gen, Image: w.Image, Command: w.Command,
				Replicas: w.Replicas, State: capability.WorkloadRunning,
			})
		}
	}
	return obs, nil
}

func (f *fakeRuntime) calls() []ensureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ensureCall, len(f.ensures))
	copy(out, f.ensures)
	return out
}

var _ capability.RuntimeInspector = (*fakeRuntime)(nil)

// memoBox 是 ensure cache 协议端口的本地实现（与核 ensureMu 族等价的锁语义——
// 白盒夹具，browses 同款）。
type memoBox struct {
	mu sync.Mutex
}

func (m *memoBox) fresh(memo map[string]contract.Memo, key, sig string, _ time.Time) (contract.Memo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := memo[key]
	if !ok || rec.Sig != sig {
		return contract.Memo{}, false
	}
	return rec, true
}

func (m *memoBox) remember(memo map[string]contract.Memo, key string, rec contract.Memo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	memo[key] = rec
}

func (m *memoBox) forget(memo map[string]contract.Memo, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(memo, key)
}

// captureHandler 是收集全部日志记录的 slog 假底座（warn 断言面）。
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// warns 返回消息含子串的 warn 级记录（观测失败跳拍告警的计数/属性面）。
func (h *captureHandler) warns(substr string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, substr) {
			out = append(out, r)
		}
	}
	return out
}

// recordAttr 取记录上指定键的属性值（缺省空串）。
func recordAttr(r slog.Record, key string) string {
	val := ""
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value.String()
		}
		return true
	})
	return val
}

// recordBox 是观测窄接口（system 轴）的捕获盒：归属/期望登记参数捕获面
// （核绑定闭包铸造 systemOwner——应用模块侧按 (process, gen) 轴断言等价）。
type recordBox struct {
	mu       sync.Mutex
	expected map[string]uint64 // process → gen
}

func newRecordBox() *recordBox { return &recordBox{expected: map[string]uint64{}} }

func (r *recordBox) recordOwners(_ uint64, ws []capability.Workload) {}

func (r *recordBox) recordExpected(process string, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expected[process] = gen
}

// fixture 是 manageds 应用模块白盒夹具的组装面。
type fixture struct {
	m      *Manageds
	rt     *fakeRuntime
	obs    *observationBox
	stores transact.Stores
	clock  state.Clock
	run    state.Runner
}

// newFixture 组装应用模块夹具：真 SQLite 台账 repo + 假 Runtime + 全端口本地
// 实现（ensure cache/gensFor/facts/观测窄接口——核公式等价隔离副本，通则⑥）。
// rt 非空时复用给定载体假底座（重启续接测试的跨实例共享面——载体现行
// spec 是重启前事实）；stepTimeout 是步带界端口的硬上限
// （TestManagedStepBounded 的 50ms 注入面；缺省 30s）。
func newFixture(t *testing.T, rt *fakeRuntime, proxy capability.Proxy, registry capability.Registry, logger *slog.Logger, stepTimeout time.Duration) *fixture {
	t.Helper()
	db, clock := statetest.New(t)
	run := db.Runner()
	if rt == nil {
		rt = &fakeRuntime{}
	}
	stores := transact.Stores{
		Projects: projectrepo.New(clock, run), Networks: networkrepo.New(clock, run),
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	memo := &memoBox{}
	gens := map[string]*ManagedGen{}
	rec := newRecordBox()
	obs := newObservationBox()
	fx := &fixture{rt: rt, obs: obs, stores: stores, clock: clock, run: run}
	fx.m = New(Deps{
		Proxy:    proxy,
		Registry: registry,
		Runtime:  rt,
		Log:      logger,
		Clock:    clock,
		LockMaintenance: func() func() {
			return func() {}
		},
		BoundedStep: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, stepTimeout)
		},
		EnsureFresh:    memo.fresh,
		EnsureRemember: memo.remember,
		EnsureForget:   memo.forget,
		ActiveProjectNetworks: func(ctx context.Context) ([]capability.NetworkRef, error) {
			rows, err := stores.Networks.List(ctx)
			if err != nil {
				return nil, err
			}
			projects, err := stores.Projects.List(ctx)
			if err != nil {
				return nil, err
			}
			teams := map[string]string{}
			for _, p := range projects {
				teams[p.ID] = p.TeamID
			}
			var refs []capability.NetworkRef
			for _, n := range rows {
				team, ok := teams[n.ProjectID]
				if !ok {
					continue
				}
				refs = append(refs, capability.NetworkRef{
					Namespace: capability.NamespaceRef{Team: team, Project: n.ProjectID},
					Name:      n.Name,
				})
			}
			return refs, nil
		},
		ActiveProjectIDs: func(ctx context.Context) ([]string, error) {
			projects, err := stores.Projects.List(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(projects))
			for i := range projects {
				ids = append(ids, projects[i].ID)
			}
			sort.Strings(ids)
			return ids, nil
		},
		ControlPlaneNode: func(ctx context.Context) (string, error) {
			view, err := rt.DescribeCluster(ctx)
			if err != nil {
				return "", err
			}
			for _, n := range view.Nodes {
				if n.Available && n.NodeID != "" && n.Role == "manager" {
					return n.NodeID, nil
				}
			}
			return "", fmt.Errorf("no available manager node for restore pre-seeding")
		},
		GensFor: func(nsKey string) *ManagedGen {
			if st, ok := gens[nsKey]; ok {
				return st
			}
			st := &ManagedGen{}
			gens[nsKey] = st
			return st
		},
		ReconcileNodes:  func(context.Context) {},
		PublishRoutes:   func(context.Context) {},
		MetricsProvider: func() capability.Metrics { return nil },
		RecordOwners:    rec.recordOwners,
		RecordExpected:  rec.recordExpected,
		ObservationOf:   obs.observationOf,
		EnsureMemo:      make(map[string]contract.Memo),
	})
	return fx
}

// seedProject 落 Project 行（facts 端点的团队轴输入）。
func (fx *fixture) seedProject(t *testing.T) {
	t.Helper()
	require.NoError(t, projectrepo.New(fx.clock, fx.run).Create(context.Background(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
}

// 受管 reconciler：Ensure 到系统域 + Generation 推进 + 归属登记。
func TestManagedReconcilerEnsuresSystemNamespace(t *testing.T) {
	fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 30*time.Second)
	ctx := context.Background()

	fx.m.Step(ctx)
	fx.m.Step(ctx) // 幂等：再次收敛不产生状态回退

	calls := fx.rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, "fleetly", last.NS.Team)
	assert.Equal(t, "system", last.NS.Project)
	assert.Equal(t, "fleetly-proxy-fake", last.Spec["proxy"].ID)
	assert.Equal(t, "fake/proxy:1", last.Spec["proxy"].Image)
	assert.Greater(t, uint64(last.Gen), uint64(0))
}

// 受管 Generation 幂等语义（staging 真机 2026-09-30 教训：逐 tick +1 让
// 受管 traefik 滚动替换繁殖到 115 实例打穿内存）：spec 未变 → 多 tick 同
// gen（载体 no-op）；spec 变化 → gen 推进一次后在新值上稳定。
// N1 C16 追加：签名未变拍直接跳过 Ensure 调用（收敛终态不变，稳态零
// swarm API 压力），变化拍恰好一次下发。
func TestManagedGenerationStableAcrossTicks(t *testing.T) {
	proxy := &fakeProxy{}
	fx := newFixture(t, nil, proxy, nil, nil, 30*time.Second)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		fx.m.Step(ctx)
	}
	calls := fx.rt.calls()
	require.Len(t, calls, 1, "unchanged managed spec must short-circuit to a single Ensure (C16)")
	assert.Equal(t, capability.Generation(1), calls[0].Gen)

	// spec 变化（受管 workload 镜像升级）→ 恰好推进一次，随后稳定。
	proxy.ws = []capability.Workload{{ID: "fleetly-proxy-fake", Process: "proxy", Image: "fake/proxy:2", Replicas: 1}}
	for i := 0; i < 3; i++ {
		fx.m.Step(ctx)
	}
	calls = fx.rt.calls()
	require.Len(t, calls, 2, "spec change must produce exactly one new Ensure")
	assert.Greater(t, uint64(calls[1].Gen), uint64(calls[0].Gen), "spec change must advance the generation")
	assert.Equal(t, "fake/proxy:2", calls[1].Spec["proxy"].Image)
}

// N1 C16：受管 Ensure 签名覆盖材料面——MaterialsSource 材料变化（ws 不变）
// 也必须短路失效重下发（zot 配置/htpasswd 轮换的收敛通道）。
func TestManagedEnsureMaterialsChangeForcesEnsure(t *testing.T) {
	reg := newFakeRegistry()
	reg.managed = true
	reg.materials = capability.Materials{SecretFiles: map[string][]byte{"zot-config": []byte(`{"v":1}`)}}
	fx := newFixture(t, nil, &fakeProxy{}, reg, nil, 30*time.Second)
	ctx := context.Background()
	// 双 Provider（proxy+registry）：首轮各一次 Ensure。
	fx.m.Step(ctx)
	fx.m.Step(ctx)
	require.Len(t, fx.rt.calls(), 2, "stable inputs must short-circuit after the first tick")

	// 材料变化（zot 配置重渲染）→ 仅 registry 域短路失效，恰好一次重下发
	// 且带新材料。
	reg.materials = capability.Materials{SecretFiles: map[string][]byte{"zot-config": []byte(`{"v":2}`)}}
	fx.m.Step(ctx)
	calls := fx.rt.calls()
	require.Len(t, calls, 3, "materials change must invalidate only the registry domain's short-circuit")
	assert.Equal(t, []byte(`{"v":2}`), calls[2].Materials.SecretFiles["zot-config"])
	assert.Equal(t, "fleetly", calls[2].NS.Team)
	assert.Equal(t, "registry", calls[2].NS.App)
}

// N1 C16：Ensure 失败不落签名——下一拍重试（失败重放语义不变）。
func TestManagedEnsureFailureRetriesNextTick(t *testing.T) {
	fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 30*time.Second)
	fx.rt.failNext = true
	ctx := context.Background()

	fx.m.Step(ctx) // Ensure 失败
	fx.m.Step(ctx) // 失败拍不落签名：重试成功
	fx.m.Step(ctx) // 成功后签名生效：短路
	calls := fx.rt.calls()
	require.Len(t, calls, 2, "failed ensure must retry next tick, then short-circuit")
}

// 受管收敛步带界（staging 实证 2026-09-30：docker daemon 重启窗口的 API
// hang 把无界单写者循环永久卡死，静默直至进程重启）：Runtime Ensure 挂起
// 时 Step 必须在步预算内返回，下一拍重试。
func TestManagedStepBoundedWhenRuntimeHangs(t *testing.T) {
	fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 50*time.Millisecond)
	fx.rt.ensureGate.Arm() // 永不放行 = Ensure 永久挂起
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		fx.m.Step(ctx)
		close(done)
	}()
	select {
	case <-done:
		// 带界返回：收敛步被 deadline 切断，循环存活（下一拍重试）。
	case <-time.After(2 * time.Second):
		t.Fatal("Step must be bounded when the runtime hangs")
	}
}

// 受管域 Placement 钉住测试（F2.3 收口 F1.15 挂账）：受管 zot/proxy 带本地
// 卷无钉住——spec 变更滚动替换可把 task 漂到无卷节点 preparing 打转。
// 带卷受管 Workload 钉控制面节点（首个可用 manager），卷外 Workload 保持
// 无约束（调度自由）；锚不可解析 = 本拍整组不下发（下拍重试——漏钉住
// 比部署失败更糟，applyVolumePinning 的 Q-8 同款取舍）。
func TestManagedVolumeWorkloadPinnedToControlPlane(t *testing.T) {
	ctx := context.Background()

	t.Run("volume-bearing workload pins the manager, volumeless stays free", func(t *testing.T) {
		proxy := &fakeProxy{ws: []capability.Workload{
			{ID: "fleetly-vol", Process: "vol", Image: "fake/vol:1", Replicas: 1,
				Volumes: []capability.VolumeMount{{VolumeID: "fleetly-data", Target: "/data"}}},
			{ID: "fleetly-novol", Process: "novol", Image: "fake/novol:1", Replicas: 1},
		}}
		fx := newFixture(t, nil, proxy, nil, nil, 30*time.Second)
		fx.m.Step(ctx)

		last := fx.rt.calls()[len(fx.rt.calls())-1]
		assert.Equal(t, []string{tControlNode}, last.Spec["vol"].Placement.NodeIDs,
			"a volume-bearing managed workload must pin to the control-plane node")
		assert.Empty(t, last.Spec["novol"].Placement.NodeIDs,
			"a volumeless managed workload keeps scheduling freedom")
	})

	t.Run("unresolvable control-plane anchor skips the whole pass", func(t *testing.T) {
		proxy := &fakeProxy{ws: []capability.Workload{
			{ID: "fleetly-vol", Process: "vol", Image: "fake/vol:1", Replicas: 1,
				Volumes: []capability.VolumeMount{{VolumeID: "fleetly-data", Target: "/data"}}},
		}}
		fx := newFixture(t, nil, proxy, nil, nil, 30*time.Second)
		fx.rt.clusterOv = true // 可编程视图（空 = 无可用 manager）
		fx.m.Step(ctx)
		assert.Empty(t, fx.rt.calls(), "no ensure may go out without the pinning anchor")

		// 锚恢复：下拍正常下发（带钉住）。
		fx.rt.clusterVw = capability.ClusterView{Nodes: []capability.NodeView{{
			NodeID: tControlNode, CarrierID: "swarmmanager", Role: "manager", Available: true,
		}}}
		fx.m.Step(ctx)
		require.NotEmpty(t, fx.rt.calls())
		last := fx.rt.calls()[len(fx.rt.calls())-1]
		assert.Equal(t, []string{tControlNode}, last.Spec["vol"].Placement.NodeIDs)
	})
}

// B1 回归（N0 修复批）：受管 Proxy 挂全部活跃 Project 网络——引用形态
// （NamespaceRef+平台名，engine 不拼载体名）；网络集变化推进受管 gen
// （一次性收敛，不逐 tick 滚动）。Team 轴（ADR-0028）：引用 Namespace 的
// Team 从 Project 行实取。
func TestManagedProxyAttachesProjectNetworks(t *testing.T) {
	fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 30*time.Second)
	fx.seedProject(t)
	ctx := context.Background()

	fx.m.Step(ctx)
	genBefore := fx.rt.calls()[len(fx.rt.calls())-1].Gen

	// 建两个 Project 网络 → 受管 workload 的引用集挂上。
	require.NoError(t, networkrepo.New(fx.clock, fx.run).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	require.NoError(t, networkrepo.New(fx.clock, fx.run).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000002", ProjectID: tProjectID, Name: "internal", EgressNone: true,
	}))
	fx.m.Step(ctx)

	calls := fx.rt.calls()
	last := calls[len(calls)-1]
	assert.Greater(t, last.Gen, genBefore, "network set change must advance the managed generation once")
	w := last.Spec["proxy"]
	require.Len(t, w.NetworkRefs, 2)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID}, w.NetworkRefs[0].Namespace)
	assert.Equal(t, "default", w.NetworkRefs[0].Name)
	assert.Equal(t, "internal", w.NetworkRefs[1].Name)
	assert.Empty(t, w.Networks, "cross-domain attachments must not be spliced into same-domain Networks")

	// 稳定拍：引用集未变 → gen 不再推进（不逐 tick 滚动）。
	fx.m.Step(ctx)
	assert.Equal(t, last.Gen, fx.rt.calls()[len(fx.rt.calls())-1].Gen)
}

// 附录 B.1：reconcileManaged 双 Provider——Proxy 挂活跃项目网、zot 域不挂；
// MaterialsSource 材料透传到受管 Ensure。
func TestReconcileManagedDualProviders(t *testing.T) {
	reg := newFakeRegistry()
	reg.managed = true
	reg.materials = capability.Materials{SecretFiles: map[string][]byte{"zot-config": []byte(`{"http":{}}`)}}
	fx := newFixture(t, nil, &fakeProxy{}, reg, nil, 30*time.Second)
	fx.seedProject(t)
	require.NoError(t, networkrepo.New(fx.clock, fx.run).Create(context.Background(), &network.Network{
		ID: "01JD0NET00000000000000000A", ProjectID: tProjectID, Name: "default",
	}))

	fx.m.Step(context.Background())

	proxyNS := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "proxy"}
	regNS := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"}
	var proxyCall, regCall *ensureCall
	calls := fx.rt.calls()
	for i := range calls {
		switch calls[i].NS.String() {
		case proxyNS.String():
			proxyCall = &calls[i]
		case regNS.String():
			regCall = &calls[i]
		}
	}
	require.NotNil(t, proxyCall, "proxy domain must be ensured")
	require.NotNil(t, regCall, "registry domain must be ensured")

	require.Len(t, proxyCall.Spec["proxy"].NetworkRefs, 1, "proxy must attach the active project network")
	assert.Empty(t, regCall.Spec["zot"].NetworkRefs, "registry must not attach project networks (B.1)")
	assert.Empty(t, regCall.Spec["zot"].Networks)

	assert.Empty(t, proxyCall.Materials.SecretFiles, "proxy declares no materials")
	assert.Equal(t, []byte(`{"http":{}}`), regCall.Materials.SecretFiles["zot-config"],
		"MaterialsSource payload must ride the managed Ensure")
	assert.Equal(t, proxyCall.Gen, regCall.Gen, "managed generation is shared across providers")
}
