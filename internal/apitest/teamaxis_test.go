package apitest_test

// Team 轴接实批（ADR-0028）验收：
//   - 同名项目跨 Team 并存、同 Team 撞名 409、归属 Team 不存在 404；
//   - Q-16：CreateUser/CreateToken 的 role/team 归属不一致 409（内置角色
//     是平台级模板，任意 Team 可授）；
//   - user repo FK 归一：名下仍有 Token 的用户删除 → E_CONFLICT（不再
//     E_INTERNAL）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/identity"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// 项目名唯一性口径 (team_id, name)：跨 Team 并存、同 Team 撒名 409、
// 不存在的 Team 404。
func TestProjectNameUniquenessPerTeam(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	teams := identityv1.NewTeamsServiceClient(h.Conn)

	other, err := teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: "platform"})
	require.NoError(t, err)

	first, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	assert.Equal(t, identity.DefaultTeamID, first.GetProject().GetTeamId())

	// 跨 Team 同名并存（ADR-0028 验收锚）。
	same, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{
		Name: "shop", TeamId: other.GetTeam().GetId(),
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.GetProject().GetId(), same.GetProject().GetId())

	// 同 Team 撞名拒。
	_, err = projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	// 归属 Team 不存在：受理位拒绝（Team 轴接实——不落悬空归属）。
	_, err = projects.CreateProject(ctx, &structurev1.CreateProjectRequest{
		Name: "ghost", TeamId: "01JD0GHOST0000000000000000A",
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// Q-16：role 与 team 归属一致（自定义 Role 跨 Team 引用 409；内置角色
// 平台级模板任意 Team 可授）。
func TestRoleTeamConsistencyEnforced(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	users := identityv1.NewUsersServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	roles := identityv1.NewRolesServiceClient(h.Conn)

	other, err := teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: "platform"})
	require.NoError(t, err)
	otherTeam := other.GetTeam().GetId()

	// default Team 的自定义角色。
	foreign, err := roles.CreateRole(ctx, &identityv1.CreateRoleRequest{Name: "deployer", Scopes: []string{"deployments:write"}})
	require.NoError(t, err)

	// CreateUser：跨 Team 引用拒（E_CONFLICT；REST 面 409）。
	_, err = users.CreateUser(ctx, &identityv1.CreateUserRequest{
		Name: "mallory", TeamId: otherTeam, RoleId: foreign.GetRole().GetId(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "role/team mismatch is a conflict per ADR-0028 Q-16")

	// CreateToken：同形拒。
	_, err = tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "cross-team", TeamId: otherTeam, RoleId: foreign.GetRole().GetId(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	// 内置角色是平台级模板（team_id 空）：任意 Team 可授——不是漏洞面。
	inTeam, err := users.CreateUser(ctx, &identityv1.CreateUserRequest{
		Name: "carol", TeamId: otherTeam, RoleId: identity.RoleMemberID,
	})
	require.NoError(t, err)
	tok, err := tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "carol-cli", TeamId: otherTeam, RoleId: identity.RoleMemberID, UserId: inTeam.GetUser().GetId(),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, tok.GetSecret())

	// 名下仍有 Token 的用户删除 → E_CONFLICT（FK RESTRICT 归一，不再
	// E_INTERNAL；ADR-0028 验收锚）。
	_, err = users.DeleteUser(ctx, &identityv1.DeleteUserRequest{Id: inTeam.GetUser().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "FK violation must map to E_CONFLICT, not E_INTERNAL")
}
