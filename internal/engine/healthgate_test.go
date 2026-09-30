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
