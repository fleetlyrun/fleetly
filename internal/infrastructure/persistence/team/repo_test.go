package team_test

// Team 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create 落行铸
// 造面（ID = name+seq、created_at 取假时钟）、同名冲突归一
// ErrAlreadyExists（name 唯一列把守，文案带名字锚）、Get 双态、List seq
// 稳定序、Delete 三态（删行即净；缺行 ErrNotFound；仍被 membership 引用
// FK RESTRICT 归一 ErrConflict——先解绑提示面）。引用面夹具落原始行。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	teamrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/team"
)

// TestTeamLifecycleFaces：落行 → 同名拒绝 → 双读径 → 列表 → 删面三态。
func TestTeamLifecycleFaces(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	teams := teamrepo.New(clock)

	// 首见落行：ID 由 name+seq 铸，created_at 假时钟。
	t1 := &team.Team{Name: "acme"}
	require.NoError(t, teams.Create(ctx, db.Runner(), t1))
	assert.Equal(t, "acme-1", t1.ID, "default id joins name and seq")
	assert.Equal(t, int64(1), t1.Seq)
	assert.Equal(t, state.FormatTime(clock.Now()), t1.CreatedAt)

	// 同名冲突归一 ErrAlreadyExists。
	err := teams.Create(ctx, db.Runner(), &team.Team{Name: "acme"})
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"duplicate team name must normalize to ErrAlreadyExists")
	assert.Contains(t, err.Error(), "acme", "conflict text must carry the team name")

	// 第二行供列表序面。
	t2 := &team.Team{Name: "platform"}
	require.NoError(t, teams.Create(ctx, db.Runner(), t2))

	// Get：命中与缺行 NotFound。
	got, err := teams.Get(ctx, db.Runner(), t1.ID)
	require.NoError(t, err)
	assert.Equal(t, "acme", got.Name)
	assert.Equal(t, int64(1), got.Seq)
	assert.Equal(t, t1.CreatedAt, got.CreatedAt)
	_, err = teams.Get(ctx, db.Runner(), "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// List：seq 稳定序全量。
	list, err := teams.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "acme", list[0].Name)
	assert.Equal(t, "platform", list[1].Name)

	// Delete：缺行 ErrNotFound；自由行删行即净。
	err = teams.Delete(ctx, db.Runner(), "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)
	require.NoError(t, teams.Delete(ctx, db.Runner(), t2.ID))
	list, err = teams.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, list, 1)

	// 仍被 membership 引用的 Team 拒删（FK RESTRICT 归一 ErrConflict）。
	const (
		userID = "01JD0USER000000000000000U1"
		roleID = "01JD0ROLE000000000000000R1"
	)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO users (id, seq, name, created_at) VALUES (?, 1, 'alice', '2026-01-01T00:00:00Z')`, userID)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO roles (id, seq, team_id, name, builtin, scopes, created_at)
		VALUES (?, 1, ?, 'member', 0, '[]', '2026-01-01T00:00:00Z')`, roleID, t1.ID)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO memberships (id, seq, user_id, team_id, role_id, created_at)
		VALUES ('01JD0MEMB000000000000000M1', 9, ?, ?, ?, '2026-01-01T00:00:00Z')`,
		userID, t1.ID, roleID)
	require.NoError(t, err)
	err = teams.Delete(ctx, db.Runner(), t1.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrConflict,
		"a referenced team must surface ErrConflict, not a raw FK error")
	assert.Contains(t, err.Error(), t1.ID, "refusal text must carry the team id")
}
