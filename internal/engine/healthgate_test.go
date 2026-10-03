package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// A2 回归（N0 修复批）：placement 落空（约束不可满足等价形态 = 观测停在
// pending@当前代）时，部署不得假 succeeded——L1 门必须保持关闭直至超时
// 失败（然后自动回滚路径接手；首次部署无基线则 failed 终态）。
func TestPendingObservationsNeverPassL1(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx) // → releasing（Ensure gen=1 + L1 deadline）
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)

	// 载体停在 pending（placement 不可满足）：L1 不得放行。
	pending := capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: capability.Generation(1),
		State: capability.WorkloadPending, Message: "pending",
	}
	for i := 0; i < 3; i++ {
		e.handleObservation(ctx, pending)
		e.step(ctx)
		require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State,
			"pending observations must not pass the L1 health gate")
	}

	// L1 超时 → failed（不得 succeeded/observing）。
	clock.Advance(e.opts.ReleaseTimeout + time.Second)
	e.step(ctx)
	final := getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateFailed, final.State,
		"unsatisfiable placement must fail the deployment, not succeed it (error: %s)", final.Error)
	require.Contains(t, final.Error, "health gate L1 timed out")
}

// N1 C17：releasing 等待期物化签名短路——等待拍跳过重物化（Ensure 调用
// 不再逐拍增长），观测门/超时门/终态迁移语义不变；强制重放节拍到 → 恰好
// 一次重物化（同 gen 幂等重放，自愈窗口有界）。
func TestReleaseWaitMaterializeShortCircuit(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx) // → releasing（materialize #1 + L1 deadline）
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)
	require.Len(t, rt.calls(), 1)

	// 等待期拍（无观测）：签名未变 → 跳过重物化，状态保持 releasing。
	e.step(ctx)
	e.step(ctx)
	require.Len(t, rt.calls(), 1, "wait-period ticks must not re-materialize (C17)")
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)

	// 观测 running 到达（落在短路拍）：L1 门照常放行——收敛终态不变。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", d.Generation))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d.ID).State,
		"the health gate must still pass on a short-circuited tick")
	require.Len(t, rt.calls(), 1, "the L1 pass must not need a fresh Ensure")

	// L3：时钟过窗 → succeeded。
	clock.Advance(e.opts.ObserveWindow + time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)
}

// N1 C17：强制重放节拍兜底——签名未变但节拍到，等待期恰好一次重物化
// （同 gen 幂等重放；签名外输入漂移——Secret 轮换/peer 批准/卷钉住——
// 的收敛通道）。
func TestReleaseWaitMaterializeReplayWindow(t *testing.T) {
	e, rt, clock := newTestEngineOpts(t, Options{ReconcileReplayInterval: 20 * time.Millisecond})
	ctx := context.Background()
	rev := freezeSpec(t, e, 2, tImageSpec)

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx) // → releasing（materialize #1 + L1 deadline）
	require.Len(t, rt.calls(), 1)

	e.step(ctx) // 窗内：短路
	require.Len(t, rt.calls(), 1, "within the replay window the wait tick must skip")

	clock.Advance(time.Second) // 节拍到：强制重放
	e.step(ctx)
	require.Len(t, rt.calls(), 2, "the expired replay window must force exactly one re-materialization")
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)
	require.Equal(t, rt.calls()[1].Gen, rt.calls()[0].Gen, "the forced replay stays on the same generation")

	e.step(ctx) // 重放后备忘已刷新：继续短路
	require.Len(t, rt.calls(), 2)
}
