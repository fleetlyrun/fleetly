package deployment_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func newDeployment(id, key string) *deployment.Deployment {
	return &deployment.Deployment{
		ID: id, AppID: "01JD0APP000000000000000000",
		ToRevision:     "01JD0REV000000000000000000",
		State:          deployment.StateQueued,
		IdempotencyKey: key,
	}
}

func TestDeploymentTransitCAS(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	deployments := deployment.New(clock)

	d := newDeployment("01JD0DEPLOY0000000000000000", "")
	require.NoError(t, deployments.Create(ctx, db.Runner(), d))

	// 合法迁移：queued → preparing → releasing。
	require.NoError(t, deployments.Transit(ctx, db.Runner(), d.ID,
		[]deployment.State{deployment.StateQueued}, deployment.StatePreparing, nil))
	require.NoError(t, deployments.Transit(ctx, db.Runner(), d.ID,
		[]deployment.State{deployment.StatePreparing, deployment.StateBuilding}, deployment.StateReleasing, nil))

	// 非法迁移（前置不符）→ ErrConflict；行保持原状态。
	err := deployments.Transit(ctx, db.Runner(), d.ID,
		[]deployment.State{deployment.StateQueued}, deployment.StateObserving, nil)
	assert.ErrorIs(t, err, state.ErrConflict)
	got, err := deployments.Get(ctx, db.Runner(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, deployment.StateReleasing, got.State)

	// 终态补 finished_at + mut 落 error。
	clock.Advance(30 * time.Second)
	require.NoError(t, deployments.Transit(ctx, db.Runner(), d.ID,
		[]deployment.State{deployment.StateReleasing, deployment.StateObserving}, deployment.StateFailed,
		func(m *deployment.Deployment) { m.Error = "image pull failed" }))
	got, err = deployments.Get(ctx, db.Runner(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, "image pull failed", got.Error)
	assert.Equal(t, "2026-01-01T00:00:30Z", got.FinishedAt)
	assert.True(t, got.State.Terminal())
}

func TestAdmissionDedup(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	deployments := deployment.New(clock)

	d := newDeployment("01JD0DEPLOY0000000000000000", "deploy-42")
	require.NoError(t, deployments.Create(ctx, db.Runner(), d))

	// 活跃期同幂等键并发入队 → 唯一索引拒绝（admission 事务内判定）。
	err := deployments.Create(ctx, db.Runner(), newDeployment("01JD0DEPLOY0000000000000001", "deploy-42"))
	assert.ErrorIs(t, err, state.ErrConflict)

	// 终态释放幂等键：完成后同键可再入队。
	require.NoError(t, deployments.Transit(ctx, db.Runner(), d.ID,
		[]deployment.State{deployment.StateQueued}, deployment.StateSuperseded,
		func(m *deployment.Deployment) { m.SupersededBy = "01JD0DEPLOY0000000000000002" }))
	require.NoError(t, deployments.Create(ctx, db.Runner(), newDeployment("01JD0DEPLOY0000000000000002", "deploy-42")))

	found, err := deployments.FindActiveByIdempotencyKey(ctx, db.Runner(), "deploy-42")
	require.NoError(t, err)
	assert.Equal(t, "01JD0DEPLOY0000000000000002", found.ID)

	active, err := deployments.ActiveByApp(ctx, db.Runner(), d.AppID)
	require.NoError(t, err)
	assert.Len(t, active, 1)
}

func TestBuildTransit(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	builds := build.New(clock)

	b := &build.Build{ID: "01JD0BUILD00000000000000000", AppID: "01JD0APP000000000000000000", State: build.StateQueued}
	require.NoError(t, builds.Create(ctx, db.Runner(), b))

	require.NoError(t, builds.Transit(ctx, db.Runner(), b.ID,
		[]build.State{build.StateQueued}, build.StateBuilding, nil))
	require.NoError(t, builds.Transit(ctx, db.Runner(), b.ID,
		[]build.State{build.StateBuilding}, build.StateSucceeded,
		func(m *build.Build) { m.Digest = "sha256:abc" }))

	got, err := builds.Get(ctx, db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, "sha256:abc", got.Digest)
	assert.True(t, got.State.Terminal())
	assert.NotEmpty(t, got.FinishedAt)

	err = builds.Transit(ctx, db.Runner(), b.ID,
		[]build.State{build.StateBuilding}, build.StateFailed, nil)
	assert.ErrorIs(t, err, state.ErrConflict, "terminal state must not transit again")
}
