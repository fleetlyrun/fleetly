package apitest_test

// 邀请流验收（F0.5）：一次性（二兑拒）、时窗（过期拒）、绑 team+role
//（兑换后即按该角色获权）、匿名可兑换（PUBLIC 档——邀请 Token 自身是
// 凭证）、错类凭证拒（防拿平台 Token 兑换）。

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

func TestInvitationFlow(t *testing.T) {
	h := apitest.New(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	invites := identityv1.NewInvitationsServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)

	inv, err := invites.CreateInvitation(owner, &identityv1.CreateInvitationRequest{
		RoleId: identity.RoleMemberID, TeamId: identity.DefaultTeamID,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, inv.GetSecret())
	assert.NotEmpty(t, inv.GetInvitation().GetExpiresAt())
	assert.Equal(t, "token:bootstrap", inv.GetInvitation().GetCreatedBy())

	// 匿名兑换（PUBLIC 档；无任何 Bearer）。
	anon := context.Background()
	accepted, err := invites.AcceptInvitation(anon, &identityv1.AcceptInvitationRequest{
		Secret: inv.GetSecret(), UserName: "carol",
	})
	require.NoError(t, err)
	assert.Equal(t, "carol", accepted.GetUser().GetName())

	// 兑换后按绑定角色获权：铸 carol 的 token，member 面生效。
	//（铸 token 需 owner；carol 的授权来自 membership。）
	tok, err := tokens.CreateToken(owner, &identityv1.CreateTokenRequest{
		Name: "carol-cli", RoleId: identity.RoleMemberID, UserId: accepted.GetUser().GetId(),
	})
	require.NoError(t, err)
	carol := sdk.WithToken(anon, tok.GetSecret())
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	_, err = projects.ListProjects(carol, &structurev1.ListProjectsRequest{})
	require.NoError(t, err, "member reads projects")
	_, err = projects.CreateProject(carol, &structurev1.CreateProjectRequest{Name: "x"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "member cannot create projects")

	// 单次使用：同 secret 二兑拒。
	_, err = invites.AcceptInvitation(anon, &identityv1.AcceptInvitationRequest{
		Secret: inv.GetSecret(), UserName: "dave",
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// 错类凭证：平台 Token 不能当邀请兑换。
	_, err = invites.AcceptInvitation(anon, &identityv1.AcceptInvitationRequest{
		Secret: h.Token, UserName: "eve",
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// 名字已占用：兑换失败（事务回滚，邀请未被消费）。
	inv2, err := invites.CreateInvitation(owner, &identityv1.CreateInvitationRequest{
		RoleId: identity.RoleMemberID,
	})
	require.NoError(t, err)
	_, err = invites.AcceptInvitation(anon, &identityv1.AcceptInvitationRequest{
		Secret: inv2.GetSecret(), UserName: "carol",
	})
	require.Error(t, err)
	_, err = invites.AcceptInvitation(anon, &identityv1.AcceptInvitationRequest{
		Secret: inv2.GetSecret(), UserName: "frank",
	})
	require.NoError(t, err, "failed redeem must not consume the invitation")
}

func TestInvitationExpiry(t *testing.T) {
	h := apitest.New(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	invites := identityv1.NewInvitationsServiceClient(h.Conn)

	// 最短时窗（1s）+ 假时钟推过。
	inv, err := invites.CreateInvitation(owner, &identityv1.CreateInvitationRequest{
		RoleId: identity.RoleMemberID, Ttl: "1s",
	})
	require.NoError(t, err)
	h.Clock.Advance(2e9)
	_, err = invites.AcceptInvitation(context.Background(), &identityv1.AcceptInvitationRequest{
		Secret: inv.GetSecret(), UserName: "late",
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "E_INVALID_INVITATION")

	// TTL 校验：非法形态与超上限拒。
	_, err = invites.CreateInvitation(owner, &identityv1.CreateInvitationRequest{RoleId: identity.RoleMemberID, Ttl: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive duration")
	_, err = invites.CreateInvitation(owner, &identityv1.CreateInvitationRequest{RoleId: identity.RoleMemberID, Ttl: "200h"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not exceed")
}
