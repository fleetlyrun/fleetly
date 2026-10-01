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

// RowsAffected 纵深防御（Q-5）：前置 Get 与 UPDATE 之间行状态被并发迁移
// → UPDATE 命中 0 行，Transit 必须归一 ErrConflict，不得静默当成功。
// 夹具用 mut 作同步点确定交错（单连接池下 Get 与 UPDATE 之间不持连接）。
func TestDeploymentTransitConcurrentStateReturnsConflict(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	deployments := deployment.New(clock)

	d := newDeployment("01JD0DEPLOY0000000000000000", "")
	require.NoError(t, deployments.Create(ctx, db.Runner(), d))

	inMut := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- deployments.Transit(ctx, db.Runner(), d.ID,
			[]deployment.State{deployment.StateQueued}, deployment.StatePreparing,
			func(m *deployment.Deployment) {
				close(inMut) // 前置 Get 已过，停在 UPDATE 前
				<-release
			})
	}()
	<-inMut
	// 并发迁移：行状态改走（Transit 的 UPDATE WHERE state='queued' 落 0 行）。
	_, err := db.Runner().ExecContext(ctx,
		`UPDATE deployments SET state = ? WHERE id = ?`, string(deployment.StateSuperseded), d.ID)
	require.NoError(t, err)
	close(release)

	assert.ErrorIs(t, <-done, state.ErrConflict)
	got, err := deployments.Get(ctx, db.Runner(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, deployment.StateSuperseded, got.State, "the concurrent writer's state stands")
}

func TestAdmissionDedup(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	deployments := deployment.New(clock)

	d := newDeployment("01JD0DEPLOY0000000000000000", "deploy-42")
	require.NoError(t, deployments.Create(ctx, db.Runner(), d))

	// 活跃期同幂等键并发入队 → 唯一索引拒绝（ErrAlreadyExists；admission
	// 事务内判定）。
	err := deployments.Create(ctx, db.Runner(), newDeployment("01JD0DEPLOY0000000000000001", "deploy-42"))
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

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
