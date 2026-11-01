package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// 受管 Proxy 挂网收敛的域内行为面（挂全部活跃 Project 网络、网络集变化
// 推进受管 gen 一次）已随 P5 wave-4 拆包迁入 manageds 卫星包白盒（通则⑥
// ——断言面零漂移）；本文件留跨域稳态看门狗面（manageds.Step 登记 →
// 观测 → driftScan 报警的核侧序列）。

// 受管 proxy 纳入稳态看门狗（N0.1 P2-10）：expected 随受管 Ensure 登记——
// proxy 载体在当前受管 gen 停止 → workload.stopped（平台自身可用性失明
// 不可接受）。
func TestManagedProxyCoveredBySteadyStateWatchdog(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	ctx := context.Background()

	e.manageds.Step(ctx) // 受管 Ensure + 归属/期望登记
	gen := rt.calls()[len(rt.calls())-1].Gen

	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: "fleetly-proxy-fake", Generation: gen,
		State: capability.WorkloadStopped, Message: "proxy down",
	})
	e.driftScan(ctx)
	assert.Equal(t, []string{"workload.stopped"}, eventNames(t, e, "fleetly-proxy-fake"),
		"a stopped managed proxy carrier must raise workload.stopped")
}
