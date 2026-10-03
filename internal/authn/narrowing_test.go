package authn

// 实时收窄单测（ADR-0038 / P6 T1）：有属主 Token 的有效授权 =
// min(声明, creator 当前 membership 角色)。全失 → 403 带原因 + 审计行
//（节流）；部分收窄 → scope 面原地收窄；无属主 Token（bootstrap/Agent）
// 不参与收窄。

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	membershiprepo "github.com/fleetlyrun/fleetly/internal/state/membership"
	rolerepo "github.com/fleetlyrun/fleetly/internal/state/role"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
	"github.com/fleetlyrun/fleetly/internal/state/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	"github.com/fleetlyrun/fleetly/internal/state/user"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seedNarrowingWorld 落最小身份世界：team + 声明角色(tokRole) + creator 角色
// (creatorRole) + user + membership + 有属主 Token；返回明文凭证。
func seedNarrowingWorld(t *testing.T, tokScopes, creatorScopes []string) (string, *Authenticator) {
	t.Helper()
	db, _ := statetest.New(t)
	a := NewAuthenticator(db, nil, testVocab, quietLogger())
	ctx := context.Background()
	run := db.Runner()

	tm := &team.Team{ID: "t1", Name: "crew"}
	require.NoError(t, team.New(db.Clock()).Create(ctx, run, tm))
	tokRole := &rolerepo.Role{ID: "r-tok", TeamID: "t1", Name: "tok-role", Scopes: tokScopes}
	creatorRole := &rolerepo.Role{ID: "r-creator", TeamID: "t1", Name: "creator-role", Scopes: creatorScopes}
	require.NoError(t, rolerepo.New(db.Clock()).Create(ctx, run, tokRole))
	require.NoError(t, rolerepo.New(db.Clock()).Create(ctx, run, creatorRole))
	u := &user.User{ID: "u1", Name: "alice"}
	require.NoError(t, user.New(db.Clock()).Create(ctx, run, u))
	m := &membershiprepo.Membership{ID: "m1", UserID: "u1", TeamID: "t1", RoleID: "r-creator"}
	require.NoError(t, membershiprepo.New(db.Clock()).Create(ctx, run, m))

	mat, err := identity.NewToken()
	require.NoError(t, err)
	tok := &tokenrepo.Token{
		ID: "tok1", Name: "alice-cli", TeamID: "t1", UserID: "u1", RoleID: "r-tok",
		SHA256: mat.SHA256, Prefix: mat.Prefix,
	}
	require.NoError(t, tokenrepo.New(db.Clock()).Create(ctx, run, tok))
	return mat.Secret, a
}

func resolveWith(t *testing.T, a *Authenticator, secret string) (*Identity, error) {
	t.Helper()
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+secret))
	return a.resolve(ctx)
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	return e.Envelope("").GetContext()["reason"]
}

func TestNarrowingLadderAndPassThrough(t *testing.T) {
	// 声明 apps:write+projects:read；creator 当前 apps:read+projects:read →
	// 有效 apps:read+projects:read（阶梯降档，projects 原样通过）。
	secret, a := seedNarrowingWorld(t,
		[]string{"apps:write", "projects:read"},
		[]string{"apps:read", "projects:read"})
	id, err := resolveWith(t, a, secret)
	require.NoError(t, err)
	assert.Equal(t, []string{"apps:read", "projects:read"}, id.ScopeStrings())
}

func TestNarrowingEmptyIntersectionIs403WithReasonAndAudit(t *testing.T) {
	secret, a := seedNarrowingWorld(t, []string{"deployments:write"}, []string{"projects:read"})
	_, err := resolveWith(t, a, secret)
	require.Error(t, err)
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "E_FORBIDDEN", e.Code())
	assert.Equal(t, "empty_scope_intersection", reasonOf(t, err))
	assert.Contains(t, e.Suggestion(), "ADR-0038")

	// 审计行落在库（action=token.narrowed_denied；Team 轴=Token 行 Team）。
	rows, rerr := audit.New(a.db.Clock()).ListFiltered(context.Background(), a.db.Runner(),
		audit.Filter{Resource: "token/tok1"})
	require.NoError(t, rerr)
	require.Len(t, rows, 1)
	assert.Equal(t, "token.narrowed_denied", rows[0].Action)
	assert.Equal(t, "empty_scope_intersection", rows[0].AfterFP)
	assert.Equal(t, "t1", rows[0].TeamID)

	// 节流：同 Token 60s 内重复拒绝不再落第二行。
	_, err = resolveWith(t, a, secret)
	require.Error(t, err)
	rows, rerr = audit.New(a.db.Clock()).ListFiltered(context.Background(), a.db.Runner(),
		audit.Filter{Resource: "token/tok1"})
	require.NoError(t, rerr)
	assert.Len(t, rows, 1)
}

func TestNarrowingCreatorRemovedFromTeam(t *testing.T) {
	secret, a := seedNarrowingWorld(t, []string{"apps:write"}, []string{"apps:admin"})
	// creator membership 被移除（移出 Team）→ Token 立即失去全部授权。
	require.NoError(t, a.memberships.DeleteByUser(context.Background(), a.db.Runner(), "u1", "t1"))
	_, err := resolveWith(t, a, secret)
	require.Error(t, err)
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "E_FORBIDDEN", e.Code())
	assert.Equal(t, "creator_not_in_team", reasonOf(t, err))
}

func TestNarrowingCreatorDeletedIsTerminal(t *testing.T) {
	secret, a := seedNarrowingWorld(t, []string{"apps:write"}, []string{"apps:admin"})
	// FK 把守下生产不可达（删用户须先吊销名下 Token）；制造该防御分支的
	// 现场：钉一条连接关 FK 删 user（池其余连接不受影响，行删除是持久的）。
	sqlDB, ok := a.db.Runner().(*sql.DB)
	require.True(t, ok, "runner must be *sql.DB in tests")
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), `DELETE FROM users WHERE id = 'u1'`)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	_, err = resolveWith(t, a, secret)
	require.Error(t, err)
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "E_FORBIDDEN", e.Code())
	assert.Equal(t, "creator_deleted", reasonOf(t, err))
}

func TestNarrowingUserlessTokenStaysDeclared(t *testing.T) {
	// 无属主 Token（bootstrap/Agent/CI 形态）不参与收窄——声明即有效。
	db, _ := statetest.New(t)
	a := NewAuthenticator(db, nil, testVocab, quietLogger())
	ctx := context.Background()
	run := db.Runner()
	tm := &team.Team{ID: "t1", Name: "crew"}
	require.NoError(t, team.New(db.Clock()).Create(ctx, run, tm))
	ro := &rolerepo.Role{ID: "r-tok", TeamID: "t1", Name: "tok-role", Scopes: []string{"apps:write"}}
	require.NoError(t, rolerepo.New(db.Clock()).Create(ctx, run, ro))
	mat, err := identity.NewToken()
	require.NoError(t, err)
	tok := &tokenrepo.Token{ID: "tok1", Name: "ci", TeamID: "t1", RoleID: "r-tok", SHA256: mat.SHA256, Prefix: mat.Prefix}
	require.NoError(t, tokenrepo.New(db.Clock()).Create(ctx, run, tok))

	id, err := resolveWith(t, a, mat.Secret)
	require.NoError(t, err)
	assert.Equal(t, []string{"apps:write"}, id.ScopeStrings())
}
