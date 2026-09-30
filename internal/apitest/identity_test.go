package apitest_test

// Identity 服务面 e2e（F0.5/F0.7）：whoami、双用户双角色、token 全套、
// 审计行 actor/source 断言、自定义 Role 词表校验。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/identity"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestIdentityServicesFlow(t *testing.T) {
	h := apitest.New(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	users := identityv1.NewUsersServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)
	roles := identityv1.NewRolesServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)

	// WhoAmI：bootstrap 是 owner（无属主用户，actor 落 token 形态）。
	who, err := users.WhoAmI(owner, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)
	assert.Equal(t, identity.BootstrapTokenName, who.GetTokenName())
	assert.Equal(t, "owner", who.GetRoleName())
	assert.Equal(t, []string{"*"}, who.GetScopes())

	// 双用户双角色（F0.5 验收）：admin 用户 + member 用户。
	admin, err := users.CreateUser(owner, &identityv1.CreateUserRequest{Name: "alice", RoleId: identity.RoleAdminID})
	require.NoError(t, err)
	dev, err := users.CreateUser(owner, &identityv1.CreateUserRequest{Name: "bob", RoleId: identity.RoleMemberID})
	require.NoError(t, err)

	// 给两个用户各铸一枚 Token（挂其角色）。
	adminTok, err := tokens.CreateToken(owner, &identityv1.CreateTokenRequest{
		Name: "alice-cli", RoleId: identity.RoleAdminID, UserId: admin.GetUser().GetId(),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, adminTok.GetSecret(), "secret must surface exactly once")
	devTok, err := tokens.CreateToken(owner, &identityv1.CreateTokenRequest{
		Name: "bob-cli", RoleId: identity.RoleMemberID, UserId: dev.GetUser().GetId(),
	})
	require.NoError(t, err)

	adminCtx := sdk.WithToken(context.Background(), adminTok.GetSecret())
	devCtx := sdk.WithToken(context.Background(), devTok.GetSecret())

	// 有属主用户的 whoami：user 形态 actor。
	whoDev, err := users.WhoAmI(devCtx, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)
	assert.Equal(t, "bob", whoDev.GetUserName())
	assert.Contains(t, whoDev.GetScopes(), "deployments:write")

	// member 不能读 token 面（identity 资源不在 member 面）。
	_, err = tokens.ListTokens(devCtx, &identityv1.ListTokensRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// admin 可以；且列表里 bootstrap 与名下 token 可见（明文永不回显）。
	list, err := tokens.ListTokens(adminCtx, &identityv1.ListTokensRequest{})
	require.NoError(t, err)
	names := map[string]bool{}
	for _, tok := range list.GetTokens() {
		names[tok.GetName()] = true
		assert.NotEmpty(t, tok.GetPrefix())
	}
	assert.True(t, names["bootstrap"])
	assert.True(t, names["bob-cli"])

	// member 铸 token 越权（tokens:write 不在面内）。
	_, err = tokens.CreateToken(devCtx, &identityv1.CreateTokenRequest{Name: "escalate", RoleId: identity.RoleOwnerID})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "member must not mint tokens")

	// 自定义 Role：词表外 scope 拒；合法 scope 建成。
	_, err = roles.CreateRole(owner, &identityv1.CreateRoleRequest{Name: "bad", Scopes: []string{"nope:read"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the scope vocabulary")
	deployer, err := roles.CreateRole(owner, &identityv1.CreateRoleRequest{Name: "deployer", Scopes: []string{"deployments:write"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"deployments:write"}, deployer.GetRole().GetScopes())

	// 吊销 dev token：下一个调用即 401（服务面路径复验 F0.6 验收）。
	revoked, err := tokens.RevokeToken(owner, &identityv1.RevokeTokenRequest{Id: devTok.GetToken().GetId()})
	require.NoError(t, err)
	assert.True(t, revoked.GetToken().GetRevoked())
	_, err = users.WhoAmI(devCtx, &identityv1.WhoAmIRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// 审计面（F0.7）：全部写路径带 actor 与 source；过滤可用。
	entries, err := auditq.ListAudit(owner, &identityv1.ListAuditRequest{Action: "token."})
	require.NoError(t, err)
	actions := map[string]string{}
	for _, e := range entries.GetEntries() {
		actions[e.GetAction()] = e.GetActor()
	}
	assert.Equal(t, "token:bootstrap", actions["token.create"])
	assert.Equal(t, "token:bootstrap", actions["token.revoke"])

	// 用户铸的 token 审计行以用户名计。
	userTokActions, err := auditq.ListAudit(owner, &identityv1.ListAuditRequest{Actor: "user:bob"})
	require.NoError(t, err)
	assert.Empty(t, userTokActions.GetEntries(), "bob performed no writes himself")

	// source 过滤：经 gRPC 直调的行全是 api。
	srcEntries, err := auditq.ListAudit(owner, &identityv1.ListAuditRequest{Source: "api", Limit: 1000})
	require.NoError(t, err)
	require.NotEmpty(t, srcEntries.GetEntries())
	for _, e := range srcEntries.GetEntries() {
		assert.Equal(t, "api", e.GetSource())
	}
}
