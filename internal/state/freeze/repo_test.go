package freeze_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/freeze"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func newFreeze(id, teamID, reason string) *freeze.Freeze {
	return &freeze.Freeze{ID: id, TeamID: teamID, Reason: reason, CreatedBy: "user:root"}
}

func TestFreezeLifecycle(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repo := freeze.New(clock)

	f := newFreeze("01JDQFRZ00000000000000000A", "01JD0TEAM00000000000000000", "upgrade window")
	require.NoError(t, repo.Create(ctx, db.Runner(), f))
	assert.True(t, f.Active())
	assert.NotEmpty(t, f.CreatedAt)

	// 同 scope 二次 Set → 唯一索引拒。
	err := repo.Create(ctx, db.Runner(), newFreeze("01JDQFRZ00000000000000000B", f.TeamID, "again"))
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// Team 命中：自身行与全局行都返回，Team 特定行在前。
	g := newFreeze("01JDQFRZ00000000000000000C", "", "global maintenance")
	require.NoError(t, repo.Create(ctx, db.Runner(), g))
	hit, err := repo.ActiveForTeam(ctx, db.Runner(), f.TeamID)
	require.NoError(t, err)
	require.Len(t, hit, 2)
	assert.Equal(t, f.ID, hit[0].ID, "team-specific freeze outranks the global row")

	// 无关 Team 只见全局行。
	hit, err = repo.ActiveForTeam(ctx, db.Runner(), "01JD0TEAM00000000000000009")
	require.NoError(t, err)
	require.Len(t, hit, 1)
	assert.Equal(t, g.ID, hit[0].ID)

	// Lift：活跃行落 lifted_at；再 lift 幂等成功；行不存在 404。
	clock.Advance(time.Minute)
	require.NoError(t, repo.Lift(ctx, db.Runner(), f.ID))
	got, err := repo.Get(ctx, db.Runner(), f.ID)
	require.NoError(t, err)
	assert.False(t, got.Active())
	assert.Equal(t, "2026-01-01T00:01:00Z", got.LiftedAt)
	require.NoError(t, repo.Lift(ctx, db.Runner(), f.ID))
	assert.ErrorIs(t, repo.Lift(ctx, db.Runner(), "01JDQFRZ0000000000000000ZZ"), state.ErrNotFound)

	// lift 后同 scope 可再 Set（新审批环——networkpeer 撤销同款口径）。
	require.NoError(t, repo.Create(ctx, db.Runner(), newFreeze("01JDQFRZ00000000000000000D", f.TeamID, "round two")))

	// List 新→旧 + after 游标。
	all, err := repo.List(ctx, db.Runner(), "", 0)
	require.NoError(t, err)
	require.Len(t, all, 3)
	page, err := repo.List(ctx, db.Runner(), all[1].ID, 0)
	require.NoError(t, err)
	assert.Len(t, page, 1)
}
