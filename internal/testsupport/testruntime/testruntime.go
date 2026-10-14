package testruntime

// 测试假底座的共享核（ADR-0068 后架构评审第三轮候选 5）：仅测试消费的
// 叶子包。收编两处逐字同构的假实现——engine 域 fakeRuntime（驱动环 hermetic
// 测试）与 apitest FakeRuntime（RPC 全栈测试）各自的 RuntimeNetworkMaintenance
// 假底座与 Remove hang 注入缝此前成对生长（removeBlock/removeEntered 对偶，
// commit ebdad88 双侧手贴实证）。
//
// 边界（有意不收）：观测通道两套语义（engine fake 的 obsCh 由测试手动泵、
// apitest 的 Report* API 泵 obs）是分层测试哲学非重复；exec/relay/
// streamlogs/admin 假底座是 apitest 专属消费面。本包只收同构核，差异面留
// 各层 adapter——方法名在两侧保持原样，两层测试文件零改动。

import (
	"context"
	"sort"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Wait 供被测方在撞门后等待放行（或 ctx 取消）：blocked=true 时阻塞至
// release（门关闭）或 ctx 完成。与 Pass 配合的惯用法：
//
//	if g.Pass() {
//	    return g.Wait(ctx) // 门在：等放行或取消
//	}
func (g *BlockGate) Wait(ctx context.Context) error {
	g.mu.Lock()
	block := g.block
	g.mu.Unlock()
	select {
	case <-block:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CarrierKey 是假底座的载体网络命名（测试内稳定即可——真公式是 Provider
// 私有，引擎不解析）。
func CarrierKey(ns capability.NamespaceRef, network string) string {
	return ns.Team + "/" + ns.Project + "/" + network
}

// NetCarrier 是假载体网络状态。
type NetCarrier struct {
	attachable  bool
	managed     bool
	attachments map[string]capability.NetworkAttachment
}

// Maintenance 是 RuntimeNetworkMaintenance 的假底座：载体网假状态 + 六原语
// 调用流水（inspect/detach/remove/ensure/attach 序断言面）。hook 非空时在
// 每个原语入口（锁外）调用（engine 侧维护串行化测试的卡点注入）；removeErr
// 非空时 RemoveNetwork 报错（engine 侧失败回滚路径注入）。apitest 侧两面
// 不用（零值语义）。
type Maintenance struct {
	mu          sync.Mutex
	netCarriers map[string]*NetCarrier
	ops         []string
	hook        func(op string)
	removeErr   error
}

// Seed 预置载体网在场面（legacy 非 attachable 形态复现）。
func (m *Maintenance) Seed(ns capability.NamespaceRef, network string, attachable bool, attachments ...capability.NetworkAttachment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.netCarriers == nil {
		m.netCarriers = map[string]*NetCarrier{}
	}
	byCarrier := map[string]capability.NetworkAttachment{}
	for _, a := range attachments {
		byCarrier[a.Carrier] = a
	}
	m.netCarriers[CarrierKey(ns, network)] = &NetCarrier{
		attachable: attachable, managed: true, attachments: byCarrier,
	}
}

// Ops 返回维护原语调用流水快照。
func (m *Maintenance) Ops() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.ops))
	copy(out, m.ops)
	return out
}

// State 返回载体网的平台中立快照（附件断言面；不存在即 ok=false）。
func (m *Maintenance) State(ns capability.NamespaceRef, network string) (capability.NetworkCarrierState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.netCarriers[CarrierKey(ns, network)]
	if !ok {
		return capability.NetworkCarrierState{}, false
	}
	state := capability.NetworkCarrierState{Exists: true, Attachable: c.attachable, Managed: c.managed}
	for _, a := range c.attachments {
		state.Attachments = append(state.Attachments, a)
	}
	return state, true
}

// Attachable 报告载体网是否 attachable（engine 侧重建序判定的断言面）。
func (m *Maintenance) Attachable(ns capability.NamespaceRef, network string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.netCarriers[CarrierKey(ns, network)].attachable
}

// SetRemoveErr 注入 RemoveNetwork 错误（nil 清除）。
func (m *Maintenance) SetRemoveErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeErr = err
}

// SetHook 注入维护原语入口卡点（锁外调用；nil 清除）。
func (m *Maintenance) SetHook(fn func(op string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hook = fn
}

func (m *Maintenance) runHook(op string) {
	if hook := func() func(string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.hook
	}(); hook != nil {
		hook(op)
	}
}

// InspectNetwork 实现子面（附件按 Carrier 名排序——确定性执行序契约同
// 真源 swarm InspectNetwork）。
func (m *Maintenance) InspectNetwork(_ context.Context, ns capability.NamespaceRef, network string) (capability.NetworkCarrierState, error) {
	m.runHook("inspect " + CarrierKey(ns, network))
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.netCarriers[CarrierKey(ns, network)]
	if !ok {
		return capability.NetworkCarrierState{}, nil
	}
	state := capability.NetworkCarrierState{Exists: true, Attachable: c.attachable, Managed: c.managed}
	for _, a := range c.attachments {
		state.Attachments = append(state.Attachments, a)
	}
	sort.Slice(state.Attachments, func(i, j int) bool {
		return state.Attachments[i].Carrier < state.Attachments[j].Carrier
	})
	m.ops = append(m.ops, "inspect "+CarrierKey(ns, network))
	return state, nil
}

// DetachNetwork 实现子面。
func (m *Maintenance) DetachNetwork(_ context.Context, ns capability.NamespaceRef, network, carrier string) error {
	m.runHook("detach " + CarrierKey(ns, network) + " " + carrier)
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.netCarriers[CarrierKey(ns, network)]; c != nil {
		delete(c.attachments, carrier)
	}
	m.ops = append(m.ops, "detach "+CarrierKey(ns, network)+" "+carrier)
	return nil
}

// RemoveNetwork 实现子面（removeErr 注入即报错——失败回滚路径测试面）。
func (m *Maintenance) RemoveNetwork(_ context.Context, ns capability.NamespaceRef, network string) error {
	m.runHook("remove " + CarrierKey(ns, network))
	m.mu.Lock()
	err := m.removeErr
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.netCarriers, CarrierKey(ns, network))
	m.ops = append(m.ops, "remove "+CarrierKey(ns, network))
	return nil
}

// EnsureNetwork 实现子面（复建恒 attachable + managed）。
func (m *Maintenance) EnsureNetwork(_ context.Context, ns capability.NamespaceRef, network string) error {
	m.runHook("ensure " + CarrierKey(ns, network))
	m.mu.Lock()
	defer m.mu.Unlock()
	key := CarrierKey(ns, network)
	if m.netCarriers == nil {
		m.netCarriers = map[string]*NetCarrier{}
	}
	if _, exists := m.netCarriers[key]; !exists {
		m.netCarriers[key] = &NetCarrier{attachable: true, managed: true, attachments: map[string]capability.NetworkAttachment{}}
	}
	m.ops = append(m.ops, "ensure "+key)
	return nil
}

// AttachNetwork 实现子面（裸附件兜底——假底座无 Aliases 面，归属断言只
// 看在场性）。
func (m *Maintenance) AttachNetwork(_ context.Context, ns capability.NamespaceRef, network, carrier string) error {
	m.runHook("attach " + CarrierKey(ns, network) + " " + carrier)
	m.mu.Lock()
	defer m.mu.Unlock()
	key := CarrierKey(ns, network)
	if c := m.netCarriers[key]; c != nil {
		if c.attachments == nil {
			c.attachments = map[string]capability.NetworkAttachment{}
		}
		if _, ok := c.attachments[carrier]; !ok {
			c.attachments[carrier] = capability.NetworkAttachment{Carrier: carrier}
		}
	}
	m.ops = append(m.ops, "attach "+key+" "+carrier)
	return nil
}

// BlockGate 是 hang 注入缝（Remove/Ensure 卡死测试面）：Arm 布置阻塞门并
// 返回入口信号（非阻塞发一次——测试探知被测方已停在门内）与放行函数
// （关闭门：被测方的 ctx 感知等待立即返回）。零值就绪——未 Arm 时 Pass
// 恒 false（门不存在，照常往下走）。
type BlockGate struct {
	mu      sync.Mutex
	block   chan struct{}
	entered chan struct{}
}

// Arm 布置阻塞门（须在启动被测 goroutine 前调用——免锁读写与既有模式
// 一致）。entered 在被测方首次撞门时非阻塞发信号。
func (g *BlockGate) Arm() (entered <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.block = make(chan struct{})
	g.entered = make(chan struct{}, 1)
	block := g.block
	entry := g.entered
	return entry, func() { close(block) }
}

// Pass 报告调用点是否应阻塞：门已布置则记入口信号并返回 true（调用方随后
// Wait(ctx)）；未布置返回 false（照常往下走）。
func (g *BlockGate) Pass() (blocked bool) {
	g.mu.Lock()
	block := g.block
	entry := g.entered
	g.mu.Unlock()
	if block == nil {
		return false
	}
	select {
	case entry <- struct{}{}:
	default:
	}
	return true
}
