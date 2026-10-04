package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// TestDriveTerminalRowIsQuiet（staging 实证 2026-10-03）：终态行撞进 driving
// 集（列举与驱动之间的并发竞态——行在 ListDriving 之后、drive 之前被并发
// 步骤收进终态）应当安静停驱，不得打 error——回归钉 01M40ZXP2 的
// "unexpected driving state succeeded" 单次噪音。非法状态值仍报错。
func TestDriveTerminalRowIsQuiet(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)

	// 驱动到 succeeded 终态（admission_idem_test 同款配方）。
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	row := getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateSucceeded, row.State)

	// 终态行直驱：安静停驱。
	next, err := e.driveOnce(ctx, row)
	assert.NoError(t, err, "a terminal row racing into the driving set must not be an error")
	assert.Nil(t, next)

	// 状态机外的非法值保持吵闹（宁可吵不可哑）。
	row.State = "nonsense"
	_, err = e.driveOnce(ctx, row)
	assert.Error(t, err)
}
