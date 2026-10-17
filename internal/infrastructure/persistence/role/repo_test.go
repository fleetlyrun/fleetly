package role_test

// Role 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create 落行铸
// 造面（ID = name+seq、scopes JSON 数组读回、同 Team 同名归一
// ErrAlreadyExists / 跨 Team 同名合法——UNIQUE(team_id, name)）、
// UpsertBuiltin 种子幂等（首播铸 seq 落 builtin 行；二次刷新只换 scopes，
// 不炸不改名不耗 seq——ADR-0070）、Get 双态、List 内置在前 ID 序、Delete
// 三守卫（缺行 NotFound、内置拒删 ErrConflict、仍被 membership 引用 FK
// RESTRICT 归一 ErrConflict）。roles 无外键，引用面夹具落原始行。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	rolerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/role"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

const (
	teamX = "01JD0TEAM000000000000000TX"
	teamY = "01JD0TEAM000000000000000TY"
)

// seedTeam 落一行 Team 夹具（自定义角色的归属轴）。
func seedTeam(t *testing.T, ctx context.Context, db *persistence.DB, id, name string, seq int64) {
	t.Helper()
	_, err := db.Runner().ExecContext(ctx, `
		INSERT INTO teams (id, seq, name, created_at) VALUES (?, ?, ?, '2026-01-01T00:00:00Z')`,
		id, seq, name)
	require.NoError(t, err)
}

// TestRoleCreateScopedNameConflict：落行 + 同 Team 同名拒绝 + 跨 Team 同名
// 合法 + 读径与列表序。
func TestRoleCreateScopedNameConflict(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	roles := rolerepo.New(clock, db.Runner())
	seedTeam(t, ctx, db, teamX, "team-x", 1)
	seedTeam(t, ctx, db, teamY, "team-y", 2)

	// 首见落行：ID 由 name+seq 铸，scopes 以 JSON 数组原样读回。
	r1 := &role.Role{TeamID: teamX, Name: "deployer", Scopes: []string{"deployments:write", "builds:read"}}
	require.NoError(t, roles.Create(ctx, r1))
	assert.Equal(t, "deployer-1", r1.ID, "default id joins name and seq")
	assert.Equal(t, int64(1), r1.Seq)
	assert.False(t, r1.Builtin, "custom roles are not builtin")
	assert.Equal(t, state.FormatTime(clock.Now()), r1.CreatedAt)

	// 同 Team 同名 → ErrAlreadyExists（文案带角色名锚）。
	err := roles.Create(ctx, &role.Role{TeamID: teamX, Name: "deployer", Scopes: []string{"apps:read"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"a duplicate name within the same team must normalize to ErrAlreadyExists")
	assert.Contains(t, err.Error(), "deployer", "conflict text must carry the role name")

	// 跨 Team 同名合法（唯一轴是 team+name）。
	r2 := &role.Role{TeamID: teamY, Name: "deployer", Scopes: []string{"routes:write"}}
	require.NoError(t, roles.Create(ctx, r2))

	// Get：命中（scopes 读回）与缺行 NotFound。
	got, err := roles.Get(ctx, r1.ID)
	require.NoError(t, err)
	assert.Equal(t, teamX, got.TeamID)
	assert.Equal(t, []string{"deployments:write", "builds:read"}, got.Scopes)
	_, err = roles.Get(ctx, "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// TestRoleUpsertBuiltinIdempotent：种子首播与二次刷新（词表演进面）。
func TestRoleUpsertBuiltinIdempotent(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	roles := rolerepo.New(clock, db.Runner())

	// 首播落行：固定 slug ID、team_id 空（平台级模板）、builtin=1、seq 铸造。
	first := &role.Role{ID: identity.RoleOwnerID, Name: "owner", Scopes: []string{"*:read"}}
	require.NoError(t, roles.UpsertBuiltin(ctx, first))
	assert.Equal(t, int64(1), first.Seq, "first plant mints a seq")
	assert.Equal(t, state.FormatTime(clock.Now()), first.CreatedAt)
	planted, err := roles.Get(ctx, identity.RoleOwnerID)
	require.NoError(t, err)
	assert.Empty(t, planted.TeamID, "builtin roles carry no team")
	assert.True(t, planted.Builtin)
	plantCreatedAt := planted.CreatedAt

	// 二次刷新：换 scopes 再 upsert——scopes 同步（词表演进启动即刷），
	// 名字与创建时刻不动，行数不变。
	clock.Advance(30e9)
	refreshed := &role.Role{ID: identity.RoleOwnerID, Name: "owner", Scopes: []string{"*:admin"}}
	require.NoError(t, roles.UpsertBuiltin(ctx, refreshed))
	got, err := roles.Get(ctx, identity.RoleOwnerID)
	require.NoError(t, err)
	assert.Equal(t, []string{"*:admin"}, got.Scopes, "upsert refreshes scopes from the code-side word list")
	assert.Equal(t, "owner", got.Name, "a second upsert must not rename the role")
	assert.Equal(t, int64(1), got.Seq, "an idempotent refresh must not mint a new seq")
	assert.Equal(t, plantCreatedAt, got.CreatedAt, "a refresh must not touch created_at")

	// 刷新不耗 seq 号：下一个自定义行接着铸造（ADR-0070 单源序）。
	custom := &role.Role{TeamID: teamX, Name: "viewer", Scopes: []string{"apps:read"}}
	require.NoError(t, roles.Create(ctx, custom))
	assert.Equal(t, int64(2), custom.Seq, "the refresh path must not consume a seq")

	// List：内置在前、ID 序稳定。
	all, err := roles.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.True(t, all[0].Builtin, "builtin roles sort first")
	assert.Equal(t, "viewer-2", all[1].ID)
}

// TestRoleDeleteGuards：删面三守卫 + 引用面。
func TestRoleDeleteGuards(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	roles := rolerepo.New(clock, db.Runner())
	seedTeam(t, ctx, db, teamX, "team-x", 1)

	// 夹具：user + membership 引用自定义角色（FK RESTRICT 触发面）。
	const userID = "01JD0USER000000000000000U1"
	_, err := db.Runner().ExecContext(ctx, `
		INSERT INTO users (id, seq, name, created_at) VALUES (?, 1, 'alice', '2026-01-01T00:00:00Z')`, userID)
	require.NoError(t, err)

	builtin := &role.Role{ID: identity.RoleAdminID, Name: "admin", Scopes: []string{"apps:admin"}}
	require.NoError(t, roles.UpsertBuiltin(ctx, builtin))
	referenced := &role.Role{TeamID: teamX, Name: "bound", Scopes: []string{"apps:read"}}
	require.NoError(t, roles.Create(ctx, referenced))
	free := &role.Role{TeamID: teamX, Name: "loose", Scopes: []string{"builds:read"}}
	require.NoError(t, roles.Create(ctx, free))

	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO memberships (id, seq, user_id, team_id, role_id, created_at)
		VALUES ('01JD0MEMB000000000000000M1', 9, ?, ?, ?, '2026-01-01T00:00:00Z')`,
		userID, teamX, referenced.ID)
	require.NoError(t, err)

	// 缺行 → ErrNotFound（Get 先行把守）。
	err = roles.Delete(ctx, "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 内置角色拒删（代码单一源）→ ErrConflict。
	err = roles.Delete(ctx, identity.RoleAdminID)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrConflict, "builtin roles refuse deletion")
	assert.Contains(t, err.Error(), "admin", "refusal text must carry the role name")

	// 仍被 membership 引用 → FK RESTRICT 归一 ErrConflict（先解绑提示面）。
	err = roles.Delete(ctx, referenced.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrConflict,
		"a referenced role must surface ErrConflict, not a raw FK error")

	// 无引用自定义角色删行即净。
	require.NoError(t, roles.Delete(ctx, free.ID))
	_, err = roles.Get(ctx, free.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	all, err := roles.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2, "only the free role disappears")
}
