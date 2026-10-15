package user_test

// User 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create 同名冲突
// 归一 ErrAlreadyExists、Get/GetByName 双读径、Count/List（seq 稳定序）、
// 密码字段形态（00028_users_password：password_hash NOT NULL DEFAULT ''——
// 空 = 未设密诚实拒绝；SetPassword 覆写与缺行 NotFound）、Delete FK
// RESTRICT 归一 ErrConflict（ADR-0028：仍被 Token 引用不误导 E_INTERNAL）
// 与删后即净。user repo 无通用更新面——唯一更新口是 SetPassword，不硬造。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/user"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	userrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/user"
)

// fixture bcrypt 形态哈希（列存原文——哈希铸造在调用方，repo 只管落账，
// 不引入真 bcrypt 依赖）。
const fakeHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// seedTokenRef 落 team/role/token 三行夹具，使 tokens.user_id 外键指向
// userID（tokens.user_id REFERENCES users(id)——RESTRICT 触发面）。
func seedTokenRef(t *testing.T, ctx context.Context, db *persistence.DB, userID string) {
	t.Helper()
	_, err := db.Runner().ExecContext(ctx, `
		INSERT INTO teams (id, seq, name, created_at)
		VALUES ('01JD0TEAM000000000000000T1', 1, 'team-a', '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO roles (id, seq, team_id, name, builtin, scopes, created_at)
		VALUES ('01JD0ROLE000000000000000R1', 2, '01JD0TEAM000000000000000T1', 'member', 0, '[]', '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.Runner().ExecContext(ctx, `
		INSERT INTO tokens (id, seq, name, team_id, user_id, role_id, sha256, prefix, revoked, last_used_at, created_at)
		VALUES ('01JD0TOKN000000000000000T1', 3, 'ci', '01JD0TEAM000000000000000T1', ?, '01JD0ROLE000000000000000R1', 'deadbeef', 'fl-', 0, '', '2026-01-01T00:00:00Z')`, userID)
	require.NoError(t, err)
}

// TestUserCreateConflictAndReadFaces：落行 + 冲突归一 + 双读径 + 计数/列表。
func TestUserCreateConflictAndReadFaces(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	users := userrepo.New(clock)

	alice := &user.User{Name: "alice"}
	require.NoError(t, users.Create(ctx, db.Runner(), alice))
	assert.Equal(t, "alice-1", alice.ID, "default id joins name and seq")
	assert.Equal(t, int64(1), alice.Seq)
	assert.Equal(t, state.FormatTime(clock.Now()), alice.CreatedAt)
	assert.Empty(t, alice.PasswordHash, "fresh user starts without a password credential (00028 default '')")

	// 同名冲突归一 ErrAlreadyExists（name 唯一列把守）。
	err := users.Create(ctx, db.Runner(), &user.User{Name: "alice"})
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists, "duplicate name must normalize to ErrAlreadyExists")

	bob := &user.User{Name: "bob"}
	require.NoError(t, users.Create(ctx, db.Runner(), bob))

	// Get/GetByName；缺行 NotFound（Login 自证入口的诚实拒绝前置）。
	got, err := users.Get(ctx, db.Runner(), alice.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Name)
	byName, err := users.GetByName(ctx, db.Runner(), "bob")
	require.NoError(t, err)
	assert.Equal(t, bob.ID, byName.ID)
	_, err = users.Get(ctx, db.Runner(), "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = users.GetByName(ctx, db.Runner(), "carol")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// Count（Bootstrap Token 的"无用户"判定轴）与 List（seq 稳定序）。
	n, err := users.Count(ctx, db.Runner())
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	all, err := users.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "alice", all[0].Name)
	assert.Equal(t, "bob", all[1].Name)
}

// TestUserPasswordHashShape：password_hash 列（00028）——创建缺省空串、
// SetPassword 落哈希原文（bcrypt 由调用方铸）、覆写可见、缺行 NotFound。
func TestUserPasswordHashShape(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	users := userrepo.New(clock)

	u := &user.User{Name: "alice"}
	require.NoError(t, users.Create(ctx, db.Runner(), u))

	// 未设密形态：空串（密码登录诚实拒绝的判定面）。
	got, err := users.GetByName(ctx, db.Runner(), "alice")
	require.NoError(t, err)
	assert.Empty(t, got.PasswordHash)

	// 设置 → GetByName 回读（Login 自证物单源）。
	require.NoError(t, users.SetPassword(ctx, db.Runner(), u.ID, fakeHash))
	got, err = users.GetByName(ctx, db.Runner(), "alice")
	require.NoError(t, err)
	assert.Equal(t, fakeHash, got.PasswordHash)

	// 重置覆写。
	require.NoError(t, users.SetPassword(ctx, db.Runner(), u.ID, "$2a$10$rotated"))
	got, err = users.Get(ctx, db.Runner(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, "$2a$10$rotated", got.PasswordHash)

	// 缺行 NotFound（RowsAffected==0 归一）。
	err = users.SetPassword(ctx, db.Runner(), "missing-id", fakeHash)
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// TestUserDeleteFKRestrictNormalize：仍被 Token 引用 → FK RESTRICT 归一
// ErrConflict（ADR-0028）；引用解除后删行即净；缺行 NotFound。
func TestUserDeleteFKRestrictNormalize(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	users := userrepo.New(clock)

	u := &user.User{Name: "alice"}
	require.NoError(t, users.Create(ctx, db.Runner(), u))
	seedTokenRef(t, ctx, db, u.ID)

	// FK RESTRICT → 归一 ErrConflict，文案指向 tokens（不误导 E_INTERNAL）。
	err := users.Delete(ctx, db.Runner(), u.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrConflict)
	assert.Contains(t, err.Error(), "tokens", "conflict text must point at the referencing tokens")

	// 引用解除（吊销/删除名下 Token）后删除即净。
	_, err = db.Runner().ExecContext(ctx, `DELETE FROM tokens WHERE id = '01JD0TOKN000000000000000T1'`)
	require.NoError(t, err)
	require.NoError(t, users.Delete(ctx, db.Runner(), u.ID))
	_, err = users.Get(ctx, db.Runner(), u.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	n, err := users.Count(ctx, db.Runner())
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// 缺行 NotFound。
	err = users.Delete(ctx, db.Runner(), u.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
}
