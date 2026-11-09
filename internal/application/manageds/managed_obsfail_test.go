package manageds

// P1-5 根修回归（docs/reviews/2026-10-05-n2-review.md；staging runbook
// 记录·五 #2）：受管 reconciler 的前置观测面（gen 播种 InspectWorkloads /
// Proxy 挂网的 networks+projects list）返回错误时按"跳拍 + 告警"保守化——
// 观测错误不得当作"载体不存在/失配"触发 spec 对照或 gen 冷启重置（I/O
// 风暴窗五受管域连滚、traefik 路由中断级假滚的事故机制）。P5 wave-4
// 随域拆包自 engine 包迁入，断言面零漂移；per-域 Generation 续接语义
//（TestManagedGenSeed*）同批随域。

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
)

// TestManagedReconcilerObservationFailureSkipsTick 钉事故主机制（gen 续接
// 的播种 Inspect 失败回退 = 假定无锚冷启 gen=1 → 与载体持久标签差值 =
// 假滚动）：风暴窗内零 Ensure / 零 gen 推进 / 告警恰一次（不逐拍刷屏）；
// 观测恢复后下一拍播种续接收敛——沿用载体现行 gen（7232da4 重启零滚
// 语义在观测风暴窗内保持成立）。
func TestManagedReconcilerObservationFailureSkipsTick(t *testing.T) {
	proxy := &fakeProxy{}
	h := &captureHandler{}
	// 首任引擎把载体推到 gen=2（一次 spec 变更）——模拟重启前的现役载体
	//（fleetly.generation 标签持久在 spec，计数器随进程消亡）。重启 = 新
	// 应用模块实例（fresh gens 表）、同一载体假底座，落在观测风暴窗内。
	e1 := newFixture(t, nil, proxy, nil, nil, defaultStepTimeout)
	ctx := context.Background()
	e1.m.Step(ctx)
	proxy.ws = []capability.Workload{{ID: "fleetly-proxy-fake", Process: "proxy", Image: "fake/proxy:2", Replicas: 1}}
	e1.m.Step(ctx)
	require.NotEmpty(t, e1.rt.calls())
	carrierGen := e1.rt.calls()[len(e1.rt.calls())-1].Gen
	require.EqualValues(t, 2, carrierGen, "fixture: the carrier must hold generation 2 before the restart")

	// 重启（新应用模块、同一载体假底座）落在观测风暴窗内：InspectWorkloads
	// 持续失败 → 每拍跳过（不 Ensure、不推进 gen、不判 drift）。
	// 同一载体假底座（跨实例共享：载体现行 spec 是重启前事实）；新应用模块
	// 实例 = fresh gens 表（计数器随进程消亡的模拟）。
	e1.rt.inspectFail = true
	e2 := newFixture(t, e1.rt, proxy, nil, slog.New(h), defaultStepTimeout)
	before := len(e2.rt.calls())
	for i := 0; i < 3; i++ {
		e2.m.Step(ctx)
	}
	assert.Equal(t, before, len(e2.rt.calls()),
		"observation failure must not reach Ensure (zero managed rolls; no cold-start gen either)")
	warns := h.warns("observation failed; skipping domain")
	require.Len(t, warns, 1, "the warn must fire once per failure streak (no per-tick spam)")
	assert.Equal(t, "fleetly/system/proxy", recordAttr(warns[0], "namespace"))
	assert.Equal(t, "inspect workloads", recordAttr(warns[0], "observation"))
	assert.NotEmpty(t, recordAttr(warns[0], "err"), "the warn must carry the observation error")

	// 观测恢复：下一拍播种续接——沿用载体现行 gen（不冷启 1、不假 +1），
	// 随后稳态短路（恰好一次收敛）。
	e2.rt.inspectFail = false
	e2.m.Step(ctx)
	calls := e2.rt.calls()
	require.Len(t, calls, before+1, "the recovered tick must converge exactly once")
	assert.EqualValues(t, carrierGen, calls[len(calls)-1].Gen,
		"the recovered tick must adopt the carrier generation (zero label delta, zero roll)")
	e2.m.Step(ctx)
	assert.Len(t, e2.rt.calls(), before+1, "steady state must short-circuit after the recovered tick")
}

// TestManagedProxyNetworkListFailureSkipsDomainTick 钉事故次机制（Proxy 挂网
// 面的 list 失败折成"无网络"下发 = 挖掉全部项目网引用 = 指纹一变一还
// 两轮假滚）：list 失败只跳挂网域（Proxy），非挂网域（registry/zot 形态）
// 照常收敛；恢复后 Proxy 带全量网引用正常收敛。
func TestManagedProxyNetworkListFailureSkipsDomainTick(t *testing.T) {
	reg := newFakeRegistry()
	reg.managed = true
	h := &captureHandler{}
	fx := newFixture(t, nil, &fakeProxy{}, reg, slog.New(h), defaultStepTimeout)
	fx.seedProject(t)
	require.NoError(t, networkrepo.New(fx.clock, fx.run).Create(context.Background(), &network.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))

	// 风暴窗：ctx 已取消 → networks.List 失败 → Proxy 跳拍；registry 不
	// 挂网、不消费 refs，照常收敛。
	stormCtx, cancel := context.WithCancel(context.Background())
	cancel()
	fx.m.Step(stormCtx)
	fx.m.Step(stormCtx)
	for _, c := range fx.rt.calls() {
		assert.NotEqual(t, "proxy", c.NS.App,
			"the proxy domain must skip its tick while the network list fails (no shrunken-spec Ensure)")
	}
	warns := h.warns("observation failed; skipping domain")
	require.Len(t, warns, 1, "the warn must fire once per failure streak")
	assert.Equal(t, "fleetly/system/proxy", recordAttr(warns[0], "namespace"))
	assert.Equal(t, "list project networks", recordAttr(warns[0], "observation"))

	// 恢复：Proxy 下一拍带全量网引用恰好收敛一次，随后两域稳态短路。
	fx.m.Step(context.Background())
	var proxyCalls []ensureCall
	for _, c := range fx.rt.calls() {
		if c.NS.App == "proxy" {
			proxyCalls = append(proxyCalls, c)
		}
	}
	require.Len(t, proxyCalls, 1, "the recovered tick must converge the proxy domain exactly once")
	require.Len(t, proxyCalls[0].Spec["proxy"].NetworkRefs, 1,
		"the recovered spec must carry the full network ref set (not the storm-window empty set)")
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID},
		proxyCalls[0].Spec["proxy"].NetworkRefs[0].Namespace)
	fx.m.Step(context.Background())
	assert.Len(t, fx.rt.calls(), 2, "steady state must short-circuit both domains")
}

// defaultStepTimeout 是夹具的步带界缺省（与 engine 侧 Options 缺省同量级）。
const defaultStepTimeout = 30 * time.Second

// TestManagedGenSeedContinuity 钉续接语义：播种 → 首见同指纹沿用（不 +1）；
// 指纹变化 +1；无播种冷启 1。
func TestManagedGenSeedContinuity(t *testing.T) {
	st := &ManagedGen{}
	st.seedFromRuntime(7)
	assert.Equal(t, uint64(7), st.next("fp-a"), "first-seen fingerprint adopts the seeded generation (no fake bump)")
	assert.Equal(t, uint64(7), st.next("fp-a"), "same fingerprint reuses")
	assert.Equal(t, uint64(8), st.next("fp-b"), "changed fingerprint advances")
	assert.Equal(t, uint64(9), st.next("fp-c"), "each change advances once")

	cold := &ManagedGen{}
	assert.Equal(t, uint64(1), cold.next("fp-x"), "cold start (no seed) begins at 1")
	assert.Equal(t, uint64(1), cold.next("fp-x"))
	assert.Equal(t, uint64(2), cold.next("fp-y"))
}

// TestManagedGenSeedMonotonic 钉播种的保守面：只升不降（观测乱序/陈旧
// 不回退计数器）。
func TestManagedGenSeedMonotonic(t *testing.T) {
	st := &ManagedGen{}
	st.seedFromRuntime(5)
	st.seedFromRuntime(3) // 陈旧观测
	assert.Equal(t, uint64(5), st.next("fp"), "stale observations never lower the counter")
}

var _ = atomic.Bool{} // 保持 import（seeded/adopted 字段的类型面）
