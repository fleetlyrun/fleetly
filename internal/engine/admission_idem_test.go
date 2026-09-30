package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// A1 回归（N0 修复批）：固定幂等键的 Agent 语义 = 活跃期去重、终态后同键
// 复用必须可成功。逐终态钉死（succeeded/cancelled），防止 partial index 或
// admission 查询任一侧把终态行误算入活跃窗口。
func TestIdempotencyKeyReusableAfterTerminalState(t *testing.T) {
	submit := func(t *testing.T, e *Engine, rev string) *deployment.Deployment {
		t.Helper()
		d, err := e.Submit(context.Background(), SubmitRequest{
			AppID: tAppID, RevisionID: rev, IdempotencyKey: "agent-fixed-key",
		})
		require.NoError(t, err)
		return d
	}

	t.Run("succeeded", func(t *testing.T) {
		e, _, clock := newTestEngine(t)
		rev := freezeSpec(t, e, 1, tImageSpec)

		first := submit(t, e, rev)
		// 活跃期同键重放 → 去重返回既有。
		assert.Equal(t, first.ID, submit(t, e, rev).ID)

		// 驱动到 succeeded 终态。
		e.step(context.Background())
		e.handleObservation(context.Background(), workloadEventRunning(tAppID+"-web", 1))
		e.step(context.Background())
		clock.Advance(61 * time.Second)
		e.step(context.Background())
		require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, first.ID).State)

		// 终态后同键复用：必须受理为新部署。
		second := submit(t, e, rev)
		assert.NotEqual(t, first.ID, second.ID, "terminal deployment must not be returned as dedup result")
		assert.Equal(t, deployment.StateQueued, second.State)
	})

	t.Run("cancelled", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		rev := freezeSpec(t, e, 1, tImageSpec)

		first := submit(t, e, rev)
		_, err := e.Cancel(context.Background(), first.ID)
		require.NoError(t, err)

		second := submit(t, e, rev)
		assert.NotEqual(t, first.ID, second.ID, "terminal deployment must not be returned as dedup result")
		assert.Equal(t, deployment.StateQueued, second.State)
	})
}
