package apitest_test

// Token 实时收窄全链验收（ADR-0038 / P6 T1 锚）：
//   - creator 降权（membership 角色低于 Token 声明角色）→ Token 立即失去
//     对应面（WhoAmI 面收窄 + 写面 403）；
//   - creator 被移出 Team → Token 全失效（403 带原因）+ 审计行；
//   - 铸造位收口：给无 membership 的用户铸属主 Token 在受理位拒绝。

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

// TestTokenNarrowsToCreatorAuthority：admin 角色声明 × member 角色 creator
// → 有效面 = member（阶梯收窄），写面按收窄后执法。
func TestTokenNarrowsToCreatorAuthority(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	users := identityv1.NewUsersServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)

	// creator：member 档（CreateUser 同事务建 membership）。
	bob, err := users.CreateUser(ctx, &identityv1.CreateUserRequest{
		Name: "bob", RoleId: identity.RoleMemberID,
	})
	require.NoError(t, err)

	// Token：admin 档声明（高于 creator 的 member 档）。
	minted, err := tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "bob-deploy", RoleId: identity.RoleAdminID, UserId: bob.GetUser().GetId(),
	})
	require.NoError(t, err)

	// ①WhoAmI：scope 面已收窄到 creator 的 member 档（admin 声明被压住）。
	bobCtx := sdk.WithToken(context.Background(), minted.GetSecret())
	who, err := users.WhoAmI(bobCtx, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)
	assert.NotContains(t, who.GetScopes(), "users:admin", "admin-declared token must not outlive its member creator")
	assert.Contains(t, who.GetScopes(), "secrets:write", "member-level capability survives narrowing")

	// ②写面按收窄后执法：member 无 projects:write → 建项目 403。
	_, err = structurev1.NewProjectsServiceClient(h.Conn).CreateProject(bobCtx,
		&structurev1.CreateProjectRequest{Name: "nope"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "E_FORBIDDEN")
}

// TestTokenDiesWithCreatorMembership：creator 被移出 Token 所在 Team →
// 下一次请求 403 带原因（P6 裁决：可判定"找管理员"）+ 审计行落库。
func TestTokenDiesWithCreatorMembership(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	users := identityv1.NewUsersServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)

	bob, err := users.CreateUser(ctx, &identityv1.CreateUserRequest{
		Name: "carol", RoleId: identity.RoleAdminID,
	})
	require.NoError(t, err)
	minted, err := tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "carol-cli", RoleId: identity.RoleAdminID, UserId: bob.GetUser().GetId(),
	})
	require.NoError(t, err)

	bobCtx := sdk.WithToken(context.Background(), minted.GetSecret())
	// 同角色同档：membership 在位时全通（SERVER 读面）。
	_, err = structurev1.NewProjectsServiceClient(h.Conn).ListProjects(bobCtx,
		&structurev1.ListProjectsRequest{})
	require.NoError(t, err, "same-role token must pass while creator membership holds")

	// 移出 Team（membership 行直删——v1 API 面尚无移除 RPC，状态面等价）。
	_, err = h.DB.Runner().ExecContext(context.Background(),
		`DELETE FROM memberships WHERE user_id = ?`, bob.GetUser().GetId())
	require.NoError(t, err)

	// PUBLIC 面（WhoAmI）按既有语义把死 Token 放行为匿名；收窄 403 带原因
	// 在 SERVER 面浮出——Agent 可判定"找管理员"而非"重新认证"。
	_, err = structurev1.NewProjectsServiceClient(h.Conn).ListProjects(bobCtx,
		&structurev1.ListProjectsRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "creator is no longer a member")

	// 审计行（节流窗内恰一条）。
	auditList, err := identityv1.NewAuditQueryServiceClient(h.Conn).ListAudit(ctx,
		&identityv1.ListAuditRequest{Limit: 50})
	require.NoError(t, err)
	found := 0
	for _, row := range auditList.GetEntries() {
		if row.GetAction() == "token.narrowed_denied" && row.GetResource() == "token/"+minted.GetToken().GetId() {
			found++
		}
	}
	assert.Equal(t, 1, found, "narrowing denial must leave exactly one audit row within the throttle window")
}

// TestCreateTokenRequiresCreatorMembership：铸造位收口——无 membership 的
// 用户铸属主 Token 在受理位拒绝（实时收窄语义下铸出即死）。
func TestCreateTokenRequiresCreatorMembership(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	users := identityv1.NewUsersServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)

	// dave 只在 team "other" 有 membership；给 default 铸属主 Token 应拒。
	other, err := identityv1.NewTeamsServiceClient(h.Conn).CreateTeam(ctx,
		&identityv1.CreateTeamRequest{Name: "other"})
	require.NoError(t, err)
	dave, err := users.CreateUser(ctx, &identityv1.CreateUserRequest{
		Name: "dave", RoleId: identity.RoleMemberID, TeamId: other.GetTeam().GetId(),
	})
	require.NoError(t, err)

	_, err = tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "dave-x", RoleId: identity.RoleMemberID,
		UserId: dave.GetUser().GetId(), TeamId: identity.DefaultTeamID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no membership in team")
}
