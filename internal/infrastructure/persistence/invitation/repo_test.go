package invitation_test

// Invitation 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create
// 落行铸造面（ID = inv+seq、created_at 取假时钟、consumed_at 空串起手、
// sha256 + team/role 绑定）、GetBySHA256 摘要查径（accept 面查询键；缺行
// NotFound）、List seq 稳定序含已消费行（状态是可见事实）、MarkConsumed
// 一次性语义（条件更新钉 consumed_at；二次消费 ErrConflict——并发双
// accept 只有一个成功）。invitations 绑 teams/roles 外键，夹具落两行。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/invitation"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	invitationrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/invitation"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

const (
	teamID = "01JD0TEAM000000000000000T1"
	roleID = "01JD0ROLE000000000000000R1"
)

// seedTeamAndRole 落 team/role 两行夹具（invitations 双外键引用）。
func seedTeamAndRole(t *testing.T, ctx context.Context, db *persistence.DB) {
	t.Helper()
	_, err := db.Runner().ExecContext(ctx, `
		INSERT INTO teams (id, seq, name, created_at)
		VALUES (?, 1, 'team-a', '2026-01-01T00:00:00Z')`, teamID)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO roles (id, seq, team_id, name, builtin, scopes, created_at)
		VALUES (?, 2, ?, 'member', 0, '[]', '2026-01-01T00:00:00Z')`, roleID, teamID)
	require.NoError(t, err)
}

// TestInvitationLifecycleOneShotConsumption：落行 → 摘要查径 → 一次性消费。
func TestInvitationLifecycleOneShotConsumption(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	invitations := invitationrepo.New(clock)
	seedTeamAndRole(t, ctx, db)

	// 首见落行：ID 由 inv+seq 铸、consumed_at 空串起手、created_at 假时钟。
	inv := &invitation.Invitation{
		TokenSHA256: "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
		TeamID:      teamID, RoleID: roleID, CreatedBy: "01JD0USER000000000000000U1",
		ExpiresAt: "2026-01-01T01:00:00Z",
	}
	require.NoError(t, invitations.Create(ctx, db.Runner(), inv))
	assert.Equal(t, "inv-1", inv.ID, "default id joins the inv base and seq")
	assert.Equal(t, int64(1), inv.Seq)
	assert.Equal(t, state.FormatTime(clock.Now()), inv.CreatedAt)
	assert.Empty(t, inv.ConsumedAt, "fresh invitation is unconsumed")

	// GetBySHA256：摘要命中全行；未知摘要 NotFound。
	got, err := invitations.GetBySHA256(ctx, db.Runner(), inv.TokenSHA256)
	require.NoError(t, err)
	assert.Equal(t, inv.ID, got.ID)
	assert.Equal(t, teamID, got.TeamID)
	assert.Equal(t, roleID, got.RoleID)
	assert.Equal(t, "2026-01-01T01:00:00Z", got.ExpiresAt)
	_, err = invitations.GetBySHA256(ctx, db.Runner(), "deadbeef")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 第二条邀请（列表序面）。
	inv2 := &invitation.Invitation{
		TokenSHA256: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		TeamID:      teamID, RoleID: roleID,
		ExpiresAt: "2026-01-01T01:00:00Z",
	}
	require.NoError(t, invitations.Create(ctx, db.Runner(), inv2))

	// MarkConsumed：落单次使用位，consumed_at 钉假时钟。
	clock.Advance(30e9)
	require.NoError(t, invitations.MarkConsumed(ctx, db.Runner(), inv.ID))
	got, err = invitations.GetBySHA256(ctx, db.Runner(), inv.TokenSHA256)
	require.NoError(t, err)
	assert.Equal(t, state.FormatTime(clock.Now()), got.ConsumedAt,
		"consumption stamps the acceptance instant")

	// 一次性语义：已消费行再 MarkConsumed → ErrConflict（并发双 accept
	// 只有一个成功）。
	err = invitations.MarkConsumed(ctx, db.Runner(), inv.ID)
	assert.ErrorIs(t, err, state.ErrConflict,
		"a consumed invitation must refuse a second consumption")

	// List：seq 稳定序，含已消费与未消费行（状态是可见事实）。
	list, err := invitations.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, inv.ID, list[0].ID)
	assert.Equal(t, inv2.ID, list[1].ID)
	assert.NotEmpty(t, list[0].ConsumedAt)
	assert.Empty(t, list[1].ConsumedAt)
}
