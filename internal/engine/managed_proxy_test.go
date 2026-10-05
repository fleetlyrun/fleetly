package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// B1 回归（N0 修复批）：受管 Proxy 挂全部活跃 Project 网络——引用形态
// （NamespaceRef+平台名，engine 不拼载体名）；网络集变化推进受管 gen
// （一次性收敛，不逐 tick 滚动）。Team 轴（ADR-0028）：引用 Namespace 的
// Team 从 Project 行实取。
func TestManagedProxyAttachesProjectNetworks(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))

	e.managedStep(ctx)
	genBefore := rt.calls()[len(rt.calls())-1].Gen

	// 建两个 Project 网络 → 受管 workload 的引用集挂上。
	require.NoError(t, e.networks.Create(ctx, db.Runner(), &networkrepo.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	require.NoError(t, e.networks.Create(ctx, db.Runner(), &networkrepo.Network{
		ID: "01JD0NET000000000000000002", ProjectID: tProjectID, Name: "internal", EgressNone: true,
	}))
	e.managedStep(ctx)

	calls := rt.calls()
	last := calls[len(calls)-1]
	assert.Greater(t, last.Gen, genBefore, "network set change must advance the managed generation once")
	w := last.Spec["proxy"]
	require.Len(t, w.NetworkRefs, 2)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID}, w.NetworkRefs[0].Namespace)
	assert.Equal(t, "default", w.NetworkRefs[0].Name)
	assert.Equal(t, "internal", w.NetworkRefs[1].Name)
	assert.Empty(t, w.Networks, "cross-domain attachments must not be spliced into same-domain Networks")

	// 稳定拍：引用集未变 → gen 不再推进（不逐 tick 滚动）。
	e.managedStep(ctx)
	assert.Equal(t, last.Gen, rt.calls()[len(rt.calls())-1].Gen)
}

// 受管 proxy 纳入稳态看门狗（N0.1 P2-10）：expected 随受管 Ensure 登记——
// proxy 载体在当前受管 gen 停止 → workload.stopped（平台自身可用性失明
// 不可接受）。
func TestManagedProxyCoveredBySteadyStateWatchdog(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	ctx := context.Background()

	e.managedStep(ctx) // 受管 Ensure + 归属/期望登记
	gen := rt.calls()[len(rt.calls())-1].Gen

	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: "fleetly-proxy-fake", Generation: gen,
		State: capability.WorkloadStopped, Message: "proxy down",
	})
	e.driftScan(ctx)
	assert.Equal(t, []string{"workload.stopped"}, eventNames(t, e, "fleetly-proxy-fake"),
		"a stopped managed proxy carrier must raise workload.stopped")
}
