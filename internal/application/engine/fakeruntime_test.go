package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/testruntime"
)

// fakeRuntime 是 hermetic 测试假底座（架构 §11：真 SQLite + 假底座 + 假
// 时钟）：记录 Ensure 调用、可编程失败、观测事件由测试手动注入。网络维护
// 假底座与 Remove hang 注入缝是共享核（internal/testruntime，候选 5 收编
// ——此前与 apitest FakeRuntime 成对生长）。
type fakeRuntime struct {
	mu sync.Mutex

	ensures  []ensureCall
	failNext bool // 下一次 Ensure 失败（一次性注入；消费后自动清除）
	// wireGens 非空时 Ensure 回执逐载体回传（ADR-0064 归因回路注入面）。
	wireGens    map[string]capability.Generation
	inspectFail bool // InspectWorkloads 持续失败（P1-5 观测风暴注入缝；清除即恢复）
	removed     []capability.NamespaceRef
	ensureGate  testruntime.BlockGate // Arm 后 Ensure 阻塞直至放行或 ctx 取消（hang 注入）

	// removeGate 非空时 Remove 阻塞（B15-1 hang 注入；apitest
	// FakeRuntime.ArmRemoveBlock 同款语义，共享核 BlockGate）。
	removeGate testruntime.BlockGate

	obsCh chan capability.WorkloadEvent

	endpoints map[string][]capability.Endpoint // ns → 后端地址（Route 解析面）
	addrCalls []capability.NamespaceRef        // Addresses 调用记录（C16b 短路断言面）

	clusterOverride bool                   // 显式启用编程视图（空视图=节点全离开）
	clusterView     capability.ClusterView // 可编程集群快照（节点对账面）

	tamper map[string]tamperEntry // workloadID → 人工改载体注入（场景 7）

	health capability.HealthReport

	// maint 是载体网络假底座共享核（RuntimeNetworkMaintenance，ADR-0046）：
	// 在场面/调用流水/卡点/Remove 错误注入全在核内；六原语方法体委托。
	maint testruntime.Maintenance
}

// setMaintHook 注入维护原语入口卡点（串行化测试：锁外调用，卡点阻塞
// 不持假底座互斥）。
func (f *fakeRuntime) setMaintHook(fn func(op string)) {
	f.maint.SetHook(fn)
}

// seedNetworkCarrier 预置载体网形态（legacy 测试腿：attachable=false +
// 附着载体集——归属裁决的通过/拒绝面）。
func (f *fakeRuntime) seedNetworkCarrier(ns capability.NamespaceRef, network string, attachable bool, attachments ...capability.NetworkAttachment) {
	f.maint.Seed(ns, network, attachable, attachments...)
}

// maintFlow 返回维护原语调用流水快照。
func (f *fakeRuntime) maintFlow() []string {
	return f.maint.Ops()
}

// netAttachable 报告载体网当前的 attachable 形态（不存在即 false）。
func (f *fakeRuntime) netAttachable(ns capability.NamespaceRef, network string) bool {
	return f.maint.Attachable(ns, network)
}

// InspectNetwork 实现 RuntimeNetworkMaintenance 子面（委托共享核）。
func (f *fakeRuntime) InspectNetwork(ctx context.Context, ns capability.NamespaceRef, network string) (capability.NetworkCarrierState, error) {
	return f.maint.InspectNetwork(ctx, ns, network)
}

// DetachNetwork 实现 RuntimeNetworkMaintenance 子面（委托共享核）。
func (f *fakeRuntime) DetachNetwork(ctx context.Context, ns capability.NamespaceRef, network, carrier string) error {
	return f.maint.DetachNetwork(ctx, ns, network, carrier)
}

// RemoveNetwork 实现 RuntimeNetworkMaintenance 子面（委托共享核；
// setNetRemoveErr 注入错误——失败回滚路径的测试面）。
func (f *fakeRuntime) RemoveNetwork(ctx context.Context, ns capability.NamespaceRef, network string) error {
	return f.maint.RemoveNetwork(ctx, ns, network)
}

// setNetRemoveErr 注入 RemoveNetwork 错误（nil 清除）。
func (f *fakeRuntime) setNetRemoveErr(err error) {
	f.maint.SetRemoveErr(err)
}

// EnsureNetwork 实现 RuntimeNetworkMaintenance 子面（复建恒 attachable；
// 委托共享核）。
func (f *fakeRuntime) EnsureNetwork(ctx context.Context, ns capability.NamespaceRef, network string) error {
	return f.maint.EnsureNetwork(ctx, ns, network)
}

// AttachNetwork 实现 RuntimeNetworkMaintenance 子面（委托共享核）。
func (f *fakeRuntime) AttachNetwork(ctx context.Context, ns capability.NamespaceRef, network, carrier string) error {
	return f.maint.AttachNetwork(ctx, ns, network, carrier)
}

type ensureCall struct {
	NS   capability.NamespaceRef
	Gen  capability.Generation
	Spec map[string]capability.Workload // process → workload（同进程末见胜——单代形态；双代窗断言用 ByID）
	ByID map[string]capability.Workload // workloadID → workload（双代窗两代同 process 并存的完整记录）
	// MaterialsOf 是逐载体材料覆写快照（ADR-0048 双代窗旧代零扰动断言面；
	// nil 值 = 沿用调用级 Materials）。
	MaterialsOf map[string]*capability.Materials
	Materials   capability.Materials
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		obsCh:     make(chan capability.WorkloadEvent, 64),
		endpoints: map[string][]capability.Endpoint{},
	}
}

func (f *fakeRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake", Capability: capability.KindRuntime, Version: "test"}
}

func (f *fakeRuntime) Health(context.Context) capability.HealthReport { return f.health }

func (f *fakeRuntime) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, materials capability.Materials) (capability.EnsureReport, error) {
	if f.ensureGate.Pass() {
		if err := f.ensureGate.Wait(ctx); err != nil {
			return capability.EnsureReport{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	spec := make(map[string]capability.Workload, len(ws))
	byID := make(map[string]capability.Workload, len(ws))
	mats := make(map[string]*capability.Materials, len(ws))
	for _, w := range ws {
		spec[w.Process] = w
		byID[w.ID] = w
		mats[w.ID] = w.Materials
	}
	f.ensures = append(f.ensures, ensureCall{
		NS: ns, Gen: gen, Materials: materials,
		Spec: spec, ByID: byID, MaterialsOf: mats,
	})
	if f.failNext {
		f.failNext = false
		return capability.EnsureReport{}, fmt.Errorf("injected ensure failure")
	}
	// wireGens 注入（ADR-0064）：非空时作为 EnsureReport 逐载体回执返回——
	// 归因回路测试面（swarm 侧跳过/scale 分支的等价假体）。
	if len(f.wireGens) > 0 {
		return capability.EnsureReport{Gens: f.wireGens}, nil
	}
	return capability.EnsureReport{}, nil
}

func (f *fakeRuntime) Remove(ctx context.Context, ns capability.NamespaceRef) error {
	if f.removeGate.Pass() {
		if err := f.removeGate.Wait(ctx); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ns)
	return nil
}

func (f *fakeRuntime) Watch(context.Context) (<-chan capability.WorkloadEvent, error) {
	return f.obsCh, nil
}

// Addresses 返回隔离域可达地址：可编程端点表（endpoints[ns]）优先——
// 测试直设 VIP 的既有断言面；缺席时按期望集推导——存活载体 = 该域最近
// 一次 Ensure 的集合（真源 swarm 的域内收敛：Ensure 移除非期望集载体；
// InspectWorkloads 同款"最近一次"语义），期望集按平台 Workload ID 圈定
// 成员（双代窗两代同 process 的消歧断言面，ADR-0048 决策 1.4）。
func (f *fakeRuntime) Addresses(_ context.Context, ns capability.NamespaceRef, expected []capability.Workload) ([]capability.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addrCalls = append(f.addrCalls, ns)
	if eps, ok := f.endpoints[ns.String()]; ok {
		return eps, nil
	}
	var live *ensureCall
	for i := range f.ensures {
		if f.ensures[i].NS.String() == ns.String() {
			live = &f.ensures[i]
		}
	}
	if live == nil {
		return nil, nil
	}
	var eps []capability.Endpoint
	for _, w := range expected {
		if _, ok := live.ByID[w.ID]; !ok {
			continue // 期望集圈定成员，但只有最近 Ensure 存活集产端点
		}
		for _, port := range w.Ports {
			eps = append(eps, capability.Endpoint{
				Addr:    "vip-" + w.ID,
				Process: w.Process,
				Port:    port.Port,
			})
		}
	}
	return eps, nil
}

// addrCallsSnapshot 返回 Addresses 调用快照（publishRoutes 短路断言面）。
func (f *fakeRuntime) addrCallsSnapshot() []capability.NamespaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.NamespaceRef, len(f.addrCalls))
	copy(out, f.addrCalls)
	return out
}

func (f *fakeRuntime) DescribeCluster(context.Context) (capability.ClusterView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clusterOverride {
		return f.clusterView, nil // 空视图也是显式结果（节点全离开）
	}
	return capability.ClusterView{Nodes: []capability.NodeView{{
		NodeID: "01JD0NODE00000000000000000", CarrierID: "swarmmanager", Role: "manager", Available: true,
	}}}, nil
}

func (f *fakeRuntime) Enrollment(context.Context, bool, capability.EnrollmentOptions) (capability.EnrollKit, error) {
	return capability.EnrollKit{Command: "fake-join"}, nil
}

// calls 返回 Ensure 调用快照。
func (f *fakeRuntime) calls() []ensureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ensureCall, len(f.ensures))
	copy(out, f.ensures)
	return out
}

// removedSnapshot 返回 Remove 调用快照。
func (f *fakeRuntime) removedSnapshot() []capability.NamespaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.NamespaceRef, len(f.removed))
	copy(out, f.removed)
	return out
}

// InspectWorkloads 实现 RuntimeInspector 子面（ADR-0022）：观测 = 该域
// 最近一次 Ensure 的 spec（与真 Provider 的 ServiceList 快照一致——载体
// 持有的是最新 spec，不是下发历史的首笔；P1-5 播种续接测试咬出旧实现
// 首笔匹配的失真）；tamper 非空时按 workloadID 覆写（人工改载体注入）。
// inspectFail 置位时返回错误（P1-5：docker API 停滞的观测失败缝）。
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
			// 载体观测的 gen = 逐载体生效锚（覆写优先，缺省调用 gen——
			// 真源 swarm 的 labelGeneration 同款，ADR-0048 双代窗）。
			gen := c.Gen
			if w.Generation != 0 {
				gen = capability.Generation(w.Generation)
			}
			obs = append(obs, capability.WorkloadObservation{
				WorkloadID: w.ID, Generation: gen, Image: w.Image, Command: w.Command,
				Replicas: w.Replicas, State: capability.WorkloadRunning,
			})
		}
	}
	for i := range obs {
		if t, ok := f.tamper[obs[i].WorkloadID]; ok {
			if t.image != "" {
				obs[i].Image = t.image
			}
			if t.command != nil {
				obs[i].Command = t.command
			}
			if t.replicas != 0 {
				obs[i].Replicas = t.replicas
			}
			if t.rollout != "" {
				obs[i].RolloutStalled = true
				obs[i].RolloutDetail = t.rollout
			}
		}
	}
	return obs, nil
}

// tamperEntry 是人工改载体的注入面（场景 7）。command 非 nil 即覆写
// （含覆写为空切片 = 清掉入口覆盖）。rollout 非空 = 滚动停摆注入
// （swarm UpdateStatus=paused 形态，runbook 记录·二十七）。
type tamperEntry struct {
	image    string
	command  []string
	replicas int64
	rollout  string
}

var _ capability.RuntimeInspector = (*fakeRuntime)(nil)
