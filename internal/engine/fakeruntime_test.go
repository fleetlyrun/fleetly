package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// fakeRuntime 是 hermetic 测试假底座（架构 §11：真 SQLite + 假底座 + 假
// 时钟）：记录 Ensure 调用、可编程失败、观测事件由测试手动注入。
type fakeRuntime struct {
	mu sync.Mutex

	ensures     []ensureCall
	failNext    bool // 下一次 Ensure 失败（一次性注入；消费后自动清除）
	inspectFail bool // InspectWorkloads 持续失败（P1-5 观测风暴注入缝；清除即恢复）
	removed     []capability.NamespaceRef
	blockPoint  chan struct{} // 非空时 Ensure 阻塞直至关闭或 ctx 取消（hang 注入）
	// ensured 非空时每次 Ensure 入口非阻塞发信号（测试同步：探知某次
	// Ensure 已进入并停在 blockPoint）。
	ensureEntered chan struct{}

	// removeBlock 非空时 Remove 阻塞直至关闭或 ctx 取消（B15-1 hang 注入；
	// Ensure 的 blockPoint 同款形态，apitest FakeRuntime.ArmRemoveBlock 对偶）。
	removeBlock chan struct{}
	// removeEntered 非空时 Remove 入口非阻塞发信号（探知已停在 removeBlock）。
	removeEntered chan struct{}

	obsCh chan capability.WorkloadEvent

	endpoints map[string][]capability.Endpoint // ns → 后端地址（Route 解析面）
	addrCalls []capability.NamespaceRef        // Addresses 调用记录（C16b 短路断言面）

	clusterOverride bool                   // 显式启用编程视图（空视图=节点全离开）
	clusterView     capability.ClusterView // 可编程集群快照（节点对账面）

	tamper map[string]tamperEntry // workloadID → 人工改载体注入（场景 7）

	health capability.HealthReport

	// 载体网络假状态（RuntimeNetworkMaintenance 假底座，ADR-0046）：
	// netCarriers 在场面（seed 复现 legacy 非 attachable 形态）+ 维护
	// 原语调用流水（重建序断言面）。maintHookFn 非空时在每个维护原语
	// 入口（锁外）调用（串行化测试的卡点注入）。
	netCarriers  map[string]*fakeNetCarrier
	maintOps     []string
	maintHookFn  func(op string)
	netRemoveErr error
}

// fakeNetCarrier 是假载体网络状态。
type fakeNetCarrier struct {
	attachable  bool
	managed     bool
	attachments map[string]capability.NetworkAttachment
}

// fakeNetKey 是假底座的载体网络命名（测试内稳定即可——真公式是 Provider
// 私有，引擎不解析）。
func fakeNetKey(ns capability.NamespaceRef, network string) string {
	return ns.Team + "/" + ns.Project + "/" + network
}

// setMaintHook 注入维护原语入口卡点（串行化测试：锁外调用，卡点阻塞
// 不持假底座互斥）。
func (f *fakeRuntime) setMaintHook(fn func(op string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maintHookFn = fn
}

// maintHook 取当前卡点（无则 nil）。
func (f *fakeRuntime) maintHook() func(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maintHookFn
}

// seedNetworkCarrier 预置载体网形态（legacy 测试腿：attachable=false +
// 附着载体集——归属裁决的通过/拒绝面）。
func (f *fakeRuntime) seedNetworkCarrier(ns capability.NamespaceRef, network string, attachable bool, attachments ...capability.NetworkAttachment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.netCarriers == nil {
		f.netCarriers = map[string]*fakeNetCarrier{}
	}
	m := map[string]capability.NetworkAttachment{}
	for _, a := range attachments {
		m[a.Carrier] = a
	}
	f.netCarriers[fakeNetKey(ns, network)] = &fakeNetCarrier{attachable: attachable, managed: true, attachments: m}
}

// maintFlow 返回维护原语调用流水快照。
func (f *fakeRuntime) maintFlow() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.maintOps))
	copy(out, f.maintOps)
	return out
}

// netAttachable 报告载体网当前的 attachable 形态（不存在即 false）。
func (f *fakeRuntime) netAttachable(ns capability.NamespaceRef, network string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.netCarriers[fakeNetKey(ns, network)].attachable
}

// InspectNetwork 实现 RuntimeNetworkMaintenance 子面。
func (f *fakeRuntime) InspectNetwork(_ context.Context, ns capability.NamespaceRef, network string) (capability.NetworkCarrierState, error) {
	if hook := f.maintHook(); hook != nil {
		hook("inspect " + fakeNetKey(ns, network))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.netCarriers[fakeNetKey(ns, network)]
	if !ok {
		return capability.NetworkCarrierState{}, nil
	}
	state := capability.NetworkCarrierState{Exists: true, Attachable: c.attachable, Managed: c.managed}
	for _, a := range c.attachments {
		state.Attachments = append(state.Attachments, a)
	}
	// 排序契约同真源（swarm InspectNetwork——确定性执行序）。
	sort.Slice(state.Attachments, func(i, j int) bool {
		return state.Attachments[i].Carrier < state.Attachments[j].Carrier
	})
	f.maintOps = append(f.maintOps, "inspect "+fakeNetKey(ns, network))
	return state, nil
}

// DetachNetwork 实现 RuntimeNetworkMaintenance 子面。
func (f *fakeRuntime) DetachNetwork(_ context.Context, ns capability.NamespaceRef, network, carrier string) error {
	if hook := f.maintHook(); hook != nil {
		hook("detach " + fakeNetKey(ns, network) + " " + carrier)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.netCarriers[fakeNetKey(ns, network)]; c != nil {
		delete(c.attachments, carrier)
	}
	f.maintOps = append(f.maintOps, "detach "+fakeNetKey(ns, network)+" "+carrier)
	return nil
}

// RemoveNetwork 实现 RuntimeNetworkMaintenance 子面（netRemoveErr 可注
// 入——失败回滚路径的测试面）。
func (f *fakeRuntime) RemoveNetwork(_ context.Context, ns capability.NamespaceRef, network string) error {
	if hook := f.maintHook(); hook != nil {
		hook("remove " + fakeNetKey(ns, network))
	}
	f.mu.Lock()
	err := f.netRemoveErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.netCarriers, fakeNetKey(ns, network))
	f.maintOps = append(f.maintOps, "remove "+fakeNetKey(ns, network))
	return nil
}

// setNetRemoveErr 注入 RemoveNetwork 错误（nil 清除）。
func (f *fakeRuntime) setNetRemoveErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netRemoveErr = err
}

// EnsureNetwork 实现 RuntimeNetworkMaintenance 子面（复建恒 attachable）。
func (f *fakeRuntime) EnsureNetwork(_ context.Context, ns capability.NamespaceRef, network string) error {
	if hook := f.maintHook(); hook != nil {
		hook("ensure " + fakeNetKey(ns, network))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeNetKey(ns, network)
	if f.netCarriers == nil {
		f.netCarriers = map[string]*fakeNetCarrier{}
	}
	if _, exists := f.netCarriers[key]; !exists {
		f.netCarriers[key] = &fakeNetCarrier{attachable: true, managed: true, attachments: map[string]capability.NetworkAttachment{}}
	}
	f.maintOps = append(f.maintOps, "ensure "+key)
	return nil
}

// AttachNetwork 实现 RuntimeNetworkMaintenance 子面。
func (f *fakeRuntime) AttachNetwork(_ context.Context, ns capability.NamespaceRef, network, carrier string) error {
	if hook := f.maintHook(); hook != nil {
		hook("attach " + fakeNetKey(ns, network) + " " + carrier)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeNetKey(ns, network)
	if c := f.netCarriers[key]; c != nil {
		if c.attachments == nil {
			c.attachments = map[string]capability.NetworkAttachment{}
		}
		if _, ok := c.attachments[carrier]; !ok {
			c.attachments[carrier] = capability.NetworkAttachment{Carrier: carrier}
		}
	}
	f.maintOps = append(f.maintOps, "attach "+key+" "+carrier)
	return nil
}

type ensureCall struct {
	NS        capability.NamespaceRef
	Gen       capability.Generation
	Spec      map[string]capability.Workload // process → workload
	Materials capability.Materials
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

func (f *fakeRuntime) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, materials capability.Materials) error {
	if f.ensureEntered != nil {
		select {
		case f.ensureEntered <- struct{}{}:
		default:
		}
	}
	if f.blockPoint != nil {
		select {
		case <-f.blockPoint:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures = append(f.ensures, ensureCall{
		NS: ns, Gen: gen, Materials: materials,
		Spec: func() map[string]capability.Workload {
			m := make(map[string]capability.Workload, len(ws))
			for _, w := range ws {
				m[w.Process] = w
			}
			return m
		}(),
	})
	if f.failNext {
		f.failNext = false
		return fmt.Errorf("injected ensure failure")
	}
	return nil
}

func (f *fakeRuntime) Remove(ctx context.Context, ns capability.NamespaceRef) error {
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

func (f *fakeRuntime) Watch(context.Context) (<-chan capability.WorkloadEvent, error) {
	return f.obsCh, nil
}

func (f *fakeRuntime) Addresses(_ context.Context, ns capability.NamespaceRef, _ []capability.Workload) ([]capability.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addrCalls = append(f.addrCalls, ns)
	return f.endpoints[ns.String()], nil
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

func (f *fakeRuntime) Enrollment(context.Context, bool) (capability.EnrollKit, error) {
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
			obs = append(obs, capability.WorkloadObservation{
				WorkloadID: w.ID, Generation: c.Gen, Image: w.Image, Command: w.Command,
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
		}
	}
	return obs, nil
}

// tamperEntry 是人工改载体的注入面（场景 7）。command 非 nil 即覆写
// （含覆写为空切片 = 清掉入口覆盖）。
type tamperEntry struct {
	image    string
	command  []string
	replicas int64
}

var _ capability.RuntimeInspector = (*fakeRuntime)(nil)
