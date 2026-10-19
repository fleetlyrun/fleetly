package token

// Token 聚合 hermetic 测试 + identity 五聚合组合流（user/team/role/
// membership/token 在同一事务的四件一拍形态演练——跨聚合测试落在本包：
// Token 是组合的终点，测试视角自组装链）。

import (
	"context"
	"errors"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/invitation"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/user"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	invitationrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/invitation"
	membershiprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/membership"
	rolerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/role"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	teamrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/team"
	userrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/user"
)

func TestTokenRoundTrip(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repos := newRepos(clock, db.Runner())

	teamRow := &team.Team{ID: identity.DefaultTeamID, Name: "default"}
	roleRow := &role.Role{ID: identity.RoleMemberID, Name: "member", Scopes: []string{"deployments:write"}}
	material, err := identity.NewToken()
	if err != nil {
		t.Fatalf("material: %v", err)
	}
	tok := &token.Token{
		ID: "01TOKEN0000000000000000000", Name: "ci", TeamID: teamRow.ID,
		RoleID: roleRow.ID, SHA256: material.SHA256, Prefix: material.Prefix,
		// 过期时刻随行往返（S18-B：Login 会话 Token 的时效锚；'' = 永不
		// 过期由 TestTokenExpiryOptional 覆盖）。
		ExpiresAt: "2026-01-02T00:00:00Z",
	}

	err = db.Tx(ctx, func(run state.Runner) error {
		if err := teamrepo.New(clock, run).Create(ctx, teamRow); err != nil {
			return err
		}
		if err := rolerepo.New(clock, run).Create(ctx, roleRow); err != nil {
			return err
		}
		return New(clock, run).Create(ctx, tok)
	})
	if err != nil {
		t.Fatalf("seed tx: %v", err)
	}

	// 摘要查询（authn 拦截器路径）。
	got, err := repos.tokens.GetBySHA256(ctx, material.SHA256)
	if err != nil {
		t.Fatalf("GetBySHA256: %v", err)
	}
	if got.Name != "ci" || got.Revoked {
		t.Fatalf("got = %+v", got)
	}
	if got.ExpiresAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("expires_at roundtrip = %q, want 2026-01-02T00:00:00Z", got.ExpiresAt)
	}

	// last_used_at 节流记录 + 时间推进可见。
	clock.Advance(61e9)
	if err := repos.tokens.TouchLastUsed(ctx, got.ID); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	got, err = repos.tokens.Get(ctx, got.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastUsedAt == "" {
		t.Fatal("last_used_at not recorded")
	}

	// 吊销：幂等、状态可见。
	revoked, err := repos.tokens.Revoke(ctx, got.ID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !revoked.Revoked {
		t.Fatal("revoke must flip revoked")
	}
	if again, err := repos.tokens.Revoke(ctx, got.ID); err != nil || !again.Revoked {
		t.Fatalf("re-revoke must be idempotent: %v", err)
	}
	alive, err := repos.tokens.HasAliveBootstrap(ctx)
	if err != nil {
		t.Fatalf("HasAliveBootstrap: %v", err)
	}
	if alive {
		t.Fatal("non-bootstrap token must not count as bootstrap")
	}
}

// TestTokenExpiryOptional 钉 ” = 永不过期的显式语义（bootstrap/CI Token
// 的存量形态；迁移默认与 repo 往返都不得编造过期时刻）。
func TestTokenExpiryOptional(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repos := newRepos(clock, db.Runner())

	teamRow := &team.Team{ID: identity.DefaultTeamID, Name: "default"}
	roleRow := &role.Role{ID: identity.RoleMemberID, Name: "member", Scopes: []string{"deployments:write"}}
	material, err := identity.NewToken()
	if err != nil {
		t.Fatalf("material: %v", err)
	}
	tok := &token.Token{
		Name: "ci", TeamID: teamRow.ID, RoleID: roleRow.ID,
		SHA256: material.SHA256, Prefix: material.Prefix,
	}
	err = db.Tx(ctx, func(run state.Runner) error {
		if err := teamrepo.New(clock, run).Create(ctx, teamRow); err != nil {
			return err
		}
		if err := rolerepo.New(clock, run).Create(ctx, roleRow); err != nil {
			return err
		}
		return New(clock, run).Create(ctx, tok)
	})
	if err != nil {
		t.Fatalf("seed tx: %v", err)
	}

	got, err := repos.tokens.GetBySHA256(ctx, material.SHA256)
	if err != nil {
		t.Fatalf("GetBySHA256: %v", err)
	}
	if got.ExpiresAt != "" {
		t.Fatalf("expires_at = %q, want empty (never expires)", got.ExpiresAt)
	}
	list, err := repos.tokens.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ExpiresAt != "" {
		t.Fatalf("List = %+v, want one row with empty expires_at", list)
	}
}

func TestIdentityFlow(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repos := newRepos(clock, db.Runner())

	err := db.Tx(ctx, func(run state.Runner) error {
		if err := teamrepo.New(clock, run).Create(ctx, &team.Team{ID: identity.DefaultTeamID, Name: "default"}); err != nil {
			return err
		}
		for _, b := range identity.BuiltinRoles([]string{"projects", "deployments", "users", "audit"}) {
			ro := &role.Role{ID: b.ID, Name: b.Name, Builtin: true, Scopes: identity.ScopeStrings(b.Scopes)}
			if err := rolerepo.New(clock, run).UpsertBuiltin(ctx, ro); err != nil {
				return err
			}
		}
		if err := userrepo.New(clock, run).Create(ctx, &user.User{ID: "01USER00000000000000000000", Name: "alice"}); err != nil {
			return err
		}
		return membershiprepo.New(clock, run).Create(ctx, &membership.Membership{
			ID: "01MEMB00000000000000000000", UserID: "01USER00000000000000000000",
			TeamID: identity.DefaultTeamID, RoleID: identity.RoleAdminID,
		})
	})
	if err != nil {
		t.Fatalf("flow tx: %v", err)
	}

	// UpsertBuiltin 幂等 + scopes 刷新（词表演进的单源同步）。
	newDef := identity.BuiltinRoles([]string{"projects", "deployments", "users", "audit", "builds"})
	err = db.Tx(ctx, func(run state.Runner) error {
		for _, b := range newDef {
			ro := &role.Role{ID: b.ID, Name: b.Name, Builtin: true, Scopes: identity.ScopeStrings(b.Scopes)}
			if err := rolerepo.New(clock, run).UpsertBuiltin(ctx, ro); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reseed tx: %v", err)
	}
	roles, err := repos.roles.List(ctx)
	if err != nil {
		t.Fatalf("roles list: %v", err)
	}
	if len(roles) != 3 {
		t.Fatalf("builtin roles = %d, want 3 (upsert must not duplicate)", len(roles))
	}
	var admin *role.Role
	for i := range roles {
		if roles[i].ID == identity.RoleAdminID {
			admin = &roles[i]
		}
	}
	if admin == nil {
		t.Fatal("admin role missing")
	}
	foundBuilds := false
	for _, s := range admin.Scopes {
		if s == "builds:admin" {
			foundBuilds = true
		}
	}
	if !foundBuilds {
		t.Fatalf("admin scopes not refreshed after vocabulary growth: %v", admin.Scopes)
	}

	// 同名用户冲突（唯一约束命中 → ErrAlreadyExists，与 CAS/FK 冲突分立）。
	err = repos.users.Create(ctx, &user.User{ID: "01USER00000000000000000001", Name: "alice"})
	if err == nil || !errors.Is(err, state.ErrAlreadyExists) {
		t.Fatalf("duplicate user name must be ErrAlreadyExists, got %v", err)
	}

	// 内置角色拒删；membership 引用的角色拒删。
	if err := repos.roles.Delete(ctx, identity.RoleAdminID); err == nil || !errors.Is(err, state.ErrConflict) {
		t.Fatalf("builtin delete must conflict, got %v", err)
	}
	custom := &role.Role{ID: "01ROLE00000000000000000000", TeamID: identity.DefaultTeamID, Name: "deployer", Scopes: []string{"deployments:write"}}
	if err := repos.roles.Create(ctx, custom); err != nil {
		t.Fatalf("custom role: %v", err)
	}
	err = db.Tx(ctx, func(run state.Runner) error {
		return membershiprepo.New(clock, run).Create(ctx, &membership.Membership{
			ID: "01MEMB00000000000000000001", UserID: "01USER00000000000000000000",
			TeamID: identity.DefaultTeamID, RoleID: custom.ID,
		})
	})
	if err == nil || !errors.Is(err, state.ErrAlreadyExists) {
		t.Fatalf("second membership in same team must be ErrAlreadyExists, got %v", err)
	}

	// 邀请：摘要查询、单次消费、二消费冲突。
	invMaterial, err := identity.NewInvitation()
	if err != nil {
		t.Fatalf("invitation material: %v", err)
	}
	inv := &invitation.Invitation{
		ID: "01INV000000000000000000000", TokenSHA256: invMaterial.SHA256,
		TeamID: identity.DefaultTeamID, RoleID: identity.RoleMemberID,
		ExpiresAt: "2026-01-02T00:00:00Z",
	}
	if err := repos.invitations.Create(ctx, inv); err != nil {
		t.Fatalf("invitation create: %v", err)
	}
	got, err := repos.invitations.GetBySHA256(ctx, invMaterial.SHA256)
	if err != nil {
		t.Fatalf("invitation lookup: %v", err)
	}
	if err := repos.invitations.MarkConsumed(ctx, got.ID); err != nil {
		t.Fatalf("MarkConsumed: %v", err)
	}
	if err := repos.invitations.MarkConsumed(ctx, got.ID); err == nil || !errors.Is(err, state.ErrConflict) {
		t.Fatalf("second consume must conflict, got %v", err)
	}
}

type identityRepos struct {
	users       user.Repo
	teams       team.Repo
	roles       role.Repo
	memberships membership.Repo
	tokens      token.Repo
	invitations invitation.Repo
}

func newRepos(clock state.Clock, run state.Runner) *identityRepos {
	return &identityRepos{
		users:       userrepo.New(clock, run),
		teams:       teamrepo.New(clock, run),
		roles:       rolerepo.New(clock, run),
		memberships: membershiprepo.New(clock, run),
		tokens:      New(clock, run),
		invitations: invitationrepo.New(clock, run),
	}
}
