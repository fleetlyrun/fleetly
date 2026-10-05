package engine

// P1-5 根修回归（docs/reviews/2026-10-05-n2-review.md；staging runbook
// 记录·五 #2）：受管 reconciler 的前置观测面（gen 播种 InspectWorkloads /
// Proxy 挂网的 networks+projects list）返回错误时按"跳拍 + 告警"保守化——
// 观测错误不得当作"载体不存在/失配"触发 spec 对照或 gen 冷启重置（I/O
// 风暴窗五受管域连滚、traefik 路由中断级假滚的事故机制）。

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

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

// TestManagedReconcilerObservationFailureSkipsTick 钉事故主机制（gen 续接
// 的播种 Inspect 失败回退 = 假定无锚冷启 gen=1 → 与载体持久标签差值 =
// 假滚动）：风暴窗内零 Ensure / 零 gen 推进 / 告警恰一次（不逐拍刷屏）；
// 观测恢复后下一拍播种续接收敛——沿用载体现行 gen（7232da4 重启零滚
// 语义在观测风暴窗内保持成立）。
func TestManagedReconcilerObservationFailureSkipsTick(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	ctx := context.Background()

	// 首任引擎把载体推到 gen=2（一次 spec 变更）——模拟重启前的现役载体
	//（fleetly.generation 标签持久在 spec，计数器随进程消亡）。
	e1 := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	e1.managedStep(ctx)
	proxy.ws = []capability.Workload{{ID: "fleetly-proxy-fake", Process: "proxy", Image: "fake/proxy:2", Replicas: 1}}
	e1.managedStep(ctx)
	require.NotEmpty(t, rt.calls())
	carrierGen := rt.calls()[len(rt.calls())-1].Gen
	require.EqualValues(t, 2, carrierGen, "fixture: the carrier must hold generation 2 before the restart")

	// 重启（新 Engine、同一载体假底座）落在观测风暴窗内：InspectWorkloads
	// 持续失败 → 每拍跳过（不 Ensure、不推进 gen、不判 drift）。
	h := &captureHandler{}
	rt.inspectFail = true
	e2 := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Logger: slog.New(h)}, Options{})
	before := len(rt.calls())
	for i := 0; i < 3; i++ {
		e2.managedStep(ctx)
	}
	assert.Equal(t, before, len(rt.calls()),
		"observation failure must not reach Ensure (zero managed rolls; no cold-start gen either)")
	warns := h.warns("observation failed; skipping domain")
	require.Len(t, warns, 1, "the warn must fire once per failure streak (no per-tick spam)")
	assert.Equal(t, "fleetly/system/proxy", recordAttr(warns[0], "namespace"))
	assert.Equal(t, "inspect workloads", recordAttr(warns[0], "observation"))
	assert.NotEmpty(t, recordAttr(warns[0], "err"), "the warn must carry the observation error")

	// 观测恢复：下一拍播种续接——沿用载体现行 gen（不冷启 1、不假 +1），
	// 随后稳态短路（恰好一次收敛）。
	rt.inspectFail = false
	e2.managedStep(ctx)
	calls := rt.calls()
	require.Len(t, calls, before+1, "the recovered tick must converge exactly once")
	assert.EqualValues(t, carrierGen, calls[len(calls)-1].Gen,
		"the recovered tick must adopt the carrier generation (zero label delta, zero roll)")
	e2.managedStep(ctx)
	assert.Len(t, rt.calls(), before+1, "steady state must short-circuit after the recovered tick")
}

// TestManagedProxyNetworkListFailureSkipsDomainTick 钉事故次机制（Proxy 挂网
// 面的 list 失败折成"无网络"下发 = 挖掉全部项目网引用 = 指纹一变一还
// 两轮假滚）：list 失败只跳挂网域（Proxy），非挂网域（registry/zot 形态）
// 照常收敛；恢复后 Proxy 带全量网引用正常收敛。
func TestManagedProxyNetworkListFailureSkipsDomainTick(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	reg := newFakeRegistry()
	reg.managed = true
	h := &captureHandler{}
	e := New(Deps{DB: db, Runtime: rt, Proxy: &fakeProxy{}, Registry: reg, Logger: slog.New(h)}, Options{})
	ctx := context.Background()
	// 在场一个项目网（挂网引用面非空——失败折成空集就是假滚的输入）。
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, e.networks.Create(ctx, db.Runner(), &networkrepo.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))

	// 风暴窗：ctx 已取消 → networks.List 失败 → Proxy 跳拍；registry 不
	// 挂网、不消费 refs，照常收敛。
	stormCtx, cancel := context.WithCancel(context.Background())
	cancel()
	e.managedStep(stormCtx)
	e.managedStep(stormCtx)
	for _, c := range rt.calls() {
		assert.NotEqual(t, "proxy", c.NS.App,
			"the proxy domain must skip its tick while the network list fails (no shrunken-spec Ensure)")
	}
	warns := h.warns("observation failed; skipping domain")
	require.Len(t, warns, 1, "the warn must fire once per failure streak")
	assert.Equal(t, "fleetly/system/proxy", recordAttr(warns[0], "namespace"))
	assert.Equal(t, "list project networks", recordAttr(warns[0], "observation"))

	// 恢复：Proxy 下一拍带全量网引用恰好收敛一次，随后两域稳态短路。
	e.managedStep(ctx)
	var proxyCalls []ensureCall
	for _, c := range rt.calls() {
		if c.NS.App == "proxy" {
			proxyCalls = append(proxyCalls, c)
		}
	}
	require.Len(t, proxyCalls, 1, "the recovered tick must converge the proxy domain exactly once")
	require.Len(t, proxyCalls[0].Spec["proxy"].NetworkRefs, 1,
		"the recovered spec must carry the full network ref set (not the storm-window empty set)")
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID},
		proxyCalls[0].Spec["proxy"].NetworkRefs[0].Namespace)
	e.managedStep(ctx)
	assert.Len(t, rt.calls(), 2, "steady state must short-circuit both domains")
}
