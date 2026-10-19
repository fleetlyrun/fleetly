package apitest_test

// S18-B（R14-M1 另一半）：Login 会话 Token 时效端到端——Login 铸造的
// 会话 Token 带 expires_at（now + auth.login_token_ttl），到期后 authn
// 拦截器逐请求拒绝（API 401 + 重登建议）；TTL=0 与会话外的 tokens create
// 保持永不过期（升级零扰动）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestLoginSessionTokenExpires 钉会话 Token 的完整时效链：未到期可用，
// 到期后 WhoAmI 与业务 RPC 都 401（逐请求判定，不缓存）。
func TestLoginSessionTokenExpires(t *testing.T) {
	h := apitest.New(t)
	h.Services.LoginTokenTTL = time.Hour
	createUserAs(t, h, "alice", identity.DefaultTeamID, identity.RoleMemberID, "alice-pass")
	ctx, _ := loginSession(t, h, "alice", "alice-pass")

	users := identityv1.NewUsersServiceClient(h.Conn)
	projects := structurev1.NewProjectsServiceClient(h.Conn)

	// 未到期：会话可用。
	h.Clock.Advance(59 * time.Minute)
	who, err := users.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)
	assert.Equal(t, "alice", who.GetUserName())
	_, err = projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
	require.NoError(t, err)

	// 到期后：认证面与业务面都 401。WhoAmI 是 PUBLIC 档（脏凭证按匿名
	// 放行——status 不被阻塞），业务 RPC 直接回带原因的拒绝文案。
	h.Clock.Advance(2 * time.Minute)
	_, err = users.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")

	_, err = projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")
	assert.Contains(t, err.Error(), "expired")
	assert.Contains(t, err.Error(), "sign in again")

	// 重新登录铸新会话：立即可用（过期只针对旧会话行）。
	_, fresh := loginSession(t, h, "alice", "alice-pass")
	require.NotEqual(t, who.GetTokenId(), fresh.GetTokenId())
}

// TestLoginSessionTokenTTLZeroNeverExpires 钉 TTL=0 逃生门：会话 Token
// 不落 expires_at，时间推进不失效。
func TestLoginSessionTokenTTLZeroNeverExpires(t *testing.T) {
	h := apitest.New(t) // Services.LoginTokenTTL 零值 = 永不过期
	createUserAs(t, h, "bob", identity.DefaultTeamID, identity.RoleMemberID, "bob-pass")
	ctx, _ := loginSession(t, h, "bob", "bob-pass")

	h.Clock.Advance(10 * 365 * 24 * time.Hour)
	who, err := identityv1.NewUsersServiceClient(h.Conn).WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)
	assert.Equal(t, "bob", who.GetUserName())
}

// TestCreateTokenNeverExpires 钉 Session 外的铸造点不受 TTL 影响：
// tokens create 的 Agent/CI Token 仍永不过期（登录 TTL 只作用于 Login）。
func TestCreateTokenNeverExpires(t *testing.T) {
	h := apitest.New(t)
	h.Services.LoginTokenTTL = time.Hour
	minted, err := identityv1.NewTokensServiceClient(h.Conn).CreateToken(h.Ctx(),
		&identityv1.CreateTokenRequest{Name: "ci", RoleId: identity.RoleMemberID})
	require.NoError(t, err)

	ciCtx := sdk.WithToken(context.Background(), minted.GetSecret())
	users := identityv1.NewUsersServiceClient(h.Conn)
	_, err = users.WhoAmI(ciCtx, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)

	h.Clock.Advance(10 * 365 * 24 * time.Hour)
	who, err := users.WhoAmI(ciCtx, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)
	assert.Equal(t, "ci", who.GetTokenName())
}
