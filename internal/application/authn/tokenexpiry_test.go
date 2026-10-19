package authn

// Login 会话 Token 时效（S18-B / R14-M1 另一半）的 resolve 单测：
// expires_at 非空且已过期 → E_UNAUTHENTICATED（带重登建议，reason=expired）；
// 未过期/恰好到点/永不过期（''）三态的判定边界；过期判定用注入时钟
//（不读 wall clock），且不缓存结果（逐请求查表判定）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	rolerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/role"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	teamrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/token"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// seedExpiryWorld 落最小身份世界（team + role），返回执法器、假时钟、
// Token repo 与 DB（时间行走 / 行改写用）。无用户/无属主 = 不走 creator
// 收窄，判定面只剩时效。
func seedExpiryWorld(t *testing.T) (*Authenticator, *statetest.FakeClock, token.Repo, *persistence.DB) {
	t.Helper()
	db, clock := statetest.New(t)
	a := NewAuthenticator(db, testAuthnRepos(db.Clock(), db.Runner()), nil, testVocab, quietLogger())
	ctx := context.Background()
	require.NoError(t, teamrepo.New(clock, db.Runner()).Create(ctx, &team.Team{ID: "t1", Name: "crew"}))
	require.NoError(t, rolerepo.New(clock, db.Runner()).Create(ctx, &role.Role{
		ID: "r1", TeamID: "t1", Name: "reader", Scopes: []string{"projects:read"},
	}))
	return a, clock, tokenrepo.New(clock, db.Runner()), db
}

// seedExpiryToken 按既定 expiresAt（” = 永不过期）铸一条密码会话 Token，
// 返回明文凭证。
func seedExpiryToken(t *testing.T, repo token.Repo, expiresAt string) string {
	t.Helper()
	material, err := identity.NewToken()
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), &token.Token{
		Name: "password session", TeamID: "t1", RoleID: "r1",
		SHA256: material.SHA256, Prefix: material.Prefix, ExpiresAt: expiresAt,
	}))
	return material.Secret
}

func TestResolveTokenExpiry(t *testing.T) {
	t.Run("unexpired session token passes", func(t *testing.T) {
		a, clock, tokens, _ := seedExpiryWorld(t)
		secret := seedExpiryToken(t, tokens, clockRFC3339(clock.Now().Add(24*time.Hour)))
		id, err := resolveWith(t, a, secret)
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, "t1", id.TeamID)
	})

	t.Run("expired session token is rejected", func(t *testing.T) {
		a, clock, tokens, _ := seedExpiryWorld(t)
		secret := seedExpiryToken(t, tokens, clockRFC3339(clock.Now().Add(-time.Second)))
		id, err := resolveWith(t, a, secret)
		require.Error(t, err)
		assert.Nil(t, id)
		var e *apperr.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, "E_UNAUTHENTICATED", e.Code())
		assert.Contains(t, e.Message(), "expired")
		assert.Contains(t, e.Message(), "sign in again")
		assert.Equal(t, "expired", reasonOf(t, err))
		assert.Contains(t, apperrstatus.Envelope(e, "").GetSuggestion(), "Sign in again")
	})

	t.Run("expiry at the current instant counts as expired", func(t *testing.T) {
		a, clock, tokens, _ := seedExpiryWorld(t)
		secret := seedExpiryToken(t, tokens, clockRFC3339(clock.Now()))
		_, err := resolveWith(t, a, secret)
		require.Error(t, err)
		var e *apperr.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, "E_UNAUTHENTICATED", e.Code())
	})

	t.Run("empty expiry never expires even after time advances", func(t *testing.T) {
		a, clock, tokens, _ := seedExpiryWorld(t)
		secret := seedExpiryToken(t, tokens, "")
		id, err := resolveWith(t, a, secret)
		require.NoError(t, err)
		require.NotNil(t, id)
		clock.Advance(10 * 365 * 24 * time.Hour)
		id, err = resolveWith(t, a, secret)
		require.NoError(t, err)
		require.NotNil(t, id)
	})

	t.Run("expired result is not cached across requests", func(t *testing.T) {
		// 先失败（过期），再把行时效清空（等价于换发/修正会话）：下一次
		// 请求必须重新查表判定并放行——拦截器不缓存过期结果。
		a, clock, tokens, db := seedExpiryWorld(t)
		secret := seedExpiryToken(t, tokens, clockRFC3339(clock.Now().Add(-time.Second)))
		_, err := resolveWith(t, a, secret)
		require.Error(t, err)
		_, err = db.Runner().ExecContext(context.Background(),
			`UPDATE tokens SET expires_at = '' WHERE name = 'password session'`)
		require.NoError(t, err)
		id, err := resolveWith(t, a, secret)
		require.NoError(t, err)
		require.NotNil(t, id)
	})
}

// clockRFC3339 用固定秒精度渲染（state.FormatTime 同口径——库内文本形态）。
func clockRFC3339(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}
