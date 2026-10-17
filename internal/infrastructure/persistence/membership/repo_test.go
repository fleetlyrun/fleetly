package membership_test

// Membership 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create
// 落行铸造面（ID = membership+seq、created_at 取假时钟）、同 User 同 Team
// 重复授权归一 ErrAlreadyExists（UNIQUE(user_id, team_id)，文案带
// user/team 锚）、GetByUser 双态（User×Team 复合键查径）、ListByUser 用户
// 隔离 seq 稳定序、DeleteByUser 解绑（plain DELETE——缺行也是幂等成功）。
// memberships 三外键指向 users/teams/roles，夹具落原始行。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	membershiprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/membership"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

const (
	userA = "01JD0USER000000000000000UA"
	userB = "01JD0USER000000000000000UB"
	teamA = "01JD0TEAM000000000000000TA"
	teamB = "01JD0TEAM000000000000000TB"
	roleA = "01JD0ROLE000000000000000RA"
)

// TestMembershipGrantRevokeFaces：授权 → 重复拒绝 → 复合键查径 → 解绑。
func TestMembershipGrantRevokeFaces(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	memberships := membershiprepo.New(clock, db.Runner())

	// 夹具：两 user、两 team、一 role（role 挂 teamA）。
	_, err := db.Runner().ExecContext(ctx, `
		INSERT INTO users (id, seq, name, created_at) VALUES (?, 1, 'alice', '2026-01-01T00:00:00Z')`, userA)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO users (id, seq, name, created_at) VALUES (?, 2, 'bob', '2026-01-01T00:00:00Z')`, userB)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO teams (id, seq, name, created_at) VALUES (?, 3, 'team-a', '2026-01-01T00:00:00Z')`, teamA)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO teams (id, seq, name, created_at) VALUES (?, 4, 'team-b', '2026-01-01T00:00:00Z')`, teamB)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO roles (id, seq, team_id, name, builtin, scopes, created_at)
		VALUES (?, 5, ?, 'member', 0, '[]', '2026-01-01T00:00:00Z')`, roleA, teamA)
	require.NoError(t, err)

	// 首见授权：ID 由 membership+seq 铸，created_at 假时钟。
	m1 := &membership.Membership{UserID: userA, TeamID: teamA, RoleID: roleA}
	require.NoError(t, memberships.Create(ctx, m1))
	assert.Equal(t, "membership-1", m1.ID, "default id joins the base and seq")
	assert.Equal(t, int64(1), m1.Seq)
	assert.Equal(t, state.FormatTime(clock.Now()), m1.CreatedAt)

	// 第二行：同 user 异 team（唯一轴是 user+team，跨 team 各自一行）。
	m2 := &membership.Membership{UserID: userA, TeamID: teamB, RoleID: roleA}
	require.NoError(t, memberships.Create(ctx, m2))

	// 同 User 同 Team 重复授权 → ErrAlreadyExists（文案带双锚）。
	err = memberships.Create(ctx, &membership.Membership{UserID: userA, TeamID: teamA, RoleID: roleA})
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"a duplicate user+team grant must normalize to ErrAlreadyExists")
	assert.Contains(t, err.Error(), userA, "conflict text must carry the user id")
	assert.Contains(t, err.Error(), teamA, "conflict text must carry the team id")

	// GetByUser：复合键命中与缺行 NotFound。
	got, err := memberships.GetByUser(ctx, userA, teamA)
	require.NoError(t, err)
	assert.Equal(t, m1.ID, got.ID)
	assert.Equal(t, roleA, got.RoleID)
	_, err = memberships.GetByUser(ctx, userB, teamA)
	assert.ErrorIs(t, err, state.ErrNotFound)

	// ListByUser：用户隔离 + seq 稳定序（Login 铸 Token 的 team/role 解析源）。
	list, err := memberships.ListByUser(ctx, userA)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, m1.ID, list[0].ID)
	assert.Equal(t, m2.ID, list[1].ID)
	list, err = memberships.ListByUser(ctx, userB)
	require.NoError(t, err)
	assert.Empty(t, list)

	// DeleteByUser 解绑：行即净；再解绑/解绑缺行是幂等成功（plain DELETE）。
	require.NoError(t, memberships.DeleteByUser(ctx, userA, teamA))
	_, err = memberships.GetByUser(ctx, userA, teamA)
	assert.ErrorIs(t, err, state.ErrNotFound)
	list, err = memberships.ListByUser(ctx, userA)
	require.NoError(t, err)
	require.Len(t, list, 1, "only the unbound membership disappears")
	require.NoError(t, memberships.DeleteByUser(ctx, userA, teamA))
	require.NoError(t, memberships.DeleteByUser(ctx, "missing-user", teamA))
}
