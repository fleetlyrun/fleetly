package fleetlygrpc

// authorizeUserAdmin 的登录队对齐单测（H2 部分闭合残留的收口锚）：队内
// admin 的管理能力边界 = 目标的登录队（首个 membership，ListByUser seq 稳
// 定序——Login 铸 Token 的 memberships[0] 同源同序）。「本队挂名、登录队
// 在他队」的多队用户重置密码后经 PUBLIC Login 即得他队身份，历史形态（任
// 一 membership 在本队即放行）对它是跨队读洞。多队行当前无产品面创建路径
//（membership 仅随 CreateUser/AcceptInvitation 单次产生），故在判定函数
// 层单测钉住——多队语义整体待权限模型 ADR。

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
)

// stubMembershipRows 是只支撑 ListByUser 的 repo 桩（判定函数唯一消费面；
// 行序即 seq 稳定序——测试按 Login 解析语义显式布置）。
type stubMembershipRows struct{ rows []membership.Membership }

func (s *stubMembershipRows) Create(context.Context, *membership.Membership) error {
	return errors.New("not implemented")
}
func (s *stubMembershipRows) GetByUser(context.Context, string, string) (*membership.Membership, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMembershipRows) DeleteByUser(context.Context, string, string) error {
	return errors.New("not implemented")
}
func (s *stubMembershipRows) DeleteAllForUser(context.Context, string) (int64, error) {
	return 0, errors.New("not implemented")
}
func (s *stubMembershipRows) ListByUser(context.Context, string) ([]membership.Membership, error) {
	return s.rows, nil
}
func (s *stubMembershipRows) ListByTeam(context.Context, string) ([]membership.Membership, error) {
	return nil, errors.New("not implemented")
}

const (
	targetUserID = "01TGT"
	teamAcme     = "01ACME"
	teamOther    = "01OTHER"
)

func memRow(teamID string) membership.Membership {
	return membership.Membership{UserID: targetUserID, TeamID: teamID, RoleID: identity.RoleMemberID}
}

func adminCtx(teamID string) context.Context {
	return authn.WithIdentity(context.Background(), &authn.Identity{
		TokenID: "01TOKEN", UserID: "01CALLER", TeamID: teamID, RoleID: identity.RoleAdminID,
	})
}

func TestUserAdminLoginTeamAlignment(t *testing.T) {
	s := &Services{}
	run := func(t *testing.T, ctx context.Context, rows []membership.Membership) error {
		t.Helper()
		return s.authorizeUserAdmin(ctx, &stubMembershipRows{rows: rows}, targetUserID)
	}

	t.Run("login team in another team denies (cross-team read hole closed)", func(t *testing.T) {
		// 本队挂名、登录队在他队：历史形态在此放行，重置密码即取得他队身份。
		err := run(t, adminCtx(teamAcme), []membership.Membership{memRow(teamOther), memRow(teamAcme)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "E_FORBIDDEN")
	})

	t.Run("login team is caller team allows (multi-membership)", func(t *testing.T) {
		assert.NoError(t, run(t, adminCtx(teamAcme), []membership.Membership{memRow(teamAcme), memRow(teamOther)}))
	})

	t.Run("single-team member unchanged", func(t *testing.T) {
		assert.NoError(t, run(t, adminCtx(teamAcme), []membership.Membership{memRow(teamAcme)}))
	})

	t.Run("member role caller denies (role gate intact)", func(t *testing.T) {
		ctx := authn.WithIdentity(context.Background(), &authn.Identity{
			TokenID: "01TOKEN", UserID: "01CALLER", TeamID: teamAcme, RoleID: identity.RoleMemberID,
		})
		require.Error(t, run(t, ctx, []membership.Membership{memRow(teamAcme)}))
	})

	t.Run("platform administrator target denies for team admin", func(t *testing.T) {
		// 第 2 步先于第 4 步：default Team owner 身份目标，同队（登录队）
		// admin 亦不得动手。
		rows := []membership.Membership{{UserID: targetUserID, TeamID: identity.DefaultTeamID, RoleID: identity.RoleOwnerID}}
		require.Error(t, run(t, adminCtx(identity.DefaultTeamID), rows))
	})

	t.Run("platform admin caller allows", func(t *testing.T) {
		ctx := authn.WithIdentity(context.Background(), &authn.Identity{
			TokenID: "01TOKEN", UserID: "01OWNER", TeamID: identity.DefaultTeamID, RoleID: identity.RoleOwnerID,
		})
		assert.NoError(t, run(t, ctx, []membership.Membership{memRow(teamOther)}))
	})

	t.Run("self service allows regardless of login team", func(t *testing.T) {
		ctx := authn.WithIdentity(context.Background(), &authn.Identity{
			TokenID: "01TOKEN", UserID: targetUserID, TeamID: teamAcme, RoleID: identity.RoleMemberID,
		})
		assert.NoError(t, run(t, ctx, []membership.Membership{memRow(teamOther)}))
	})

	t.Run("target without memberships denies", func(t *testing.T) {
		require.Error(t, run(t, adminCtx(teamAcme), nil))
	})
}
