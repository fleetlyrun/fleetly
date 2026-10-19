package apitest_test

// 身份目录读面归属（R14-M2 读面残留，S13）：ListUsers 非平台管理员只见
// 与本队有 membership 的用户；GetUser 平台管理员任意、本人、同队任一角色
// 可读，跨队/不存在统一 403（不泄露用户是否存在）。全程走 CreateUser/
// Login 产品面。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// userNames 投影 ListUsers 的用户名集。
func userNames(resp *identityv1.ListUsersResponse) map[string]bool {
	out := map[string]bool{}
	for _, u := range resp.GetUsers() {
		out[u.GetName()] = true
	}
	return out
}

func TestIdentityReadRowAuthz(t *testing.T) {
	h := apitest.New(t)
	owner := h.Ctx()
	users := identityv1.NewUsersServiceClient(h.Conn)

	// acme 队（admin 自证会话）+ 本队普通用户（同队可读对象）。内置
	// member 角色按设计不含 users:read（管理面读权也不给，S13 不动 scope
	// 门禁），故"同队非管理员可读"用只授 users:read 的自定义角色验证——
	// 读面按 membership 放行、写面仍限 owner/admin。
	acmeTeam, adminCtx := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")
	acmePeerID := createUserAs(t, h, "acme-peer", acmeTeam, identity.RoleMemberID, "")

	readerRole, err := identityv1.NewRolesServiceClient(h.Conn).CreateRole(adminCtx, &identityv1.CreateRoleRequest{
		Name: "acme-reader", Scopes: []string{"users:read"},
	})
	require.NoError(t, err)
	readerID := createUserAs(t, h, "acme-reader", acmeTeam, readerRole.GetRole().GetId(), "acme-reader-pass")
	readerCtx, readerWho := loginSession(t, h, "acme-reader", "acme-reader-pass")
	require.Equal(t, readerID, readerWho.GetUserId())

	// 内置 member：scope 门禁不变（users:read 不含）→ 读面仍 403。
	createUserAs(t, h, "acme-member", acmeTeam, identity.RoleMemberID, "acme-member-pass")
	memberCtx, _ := loginSession(t, h, "acme-member", "acme-member-pass")

	// default 队 admin（跨队读面的负例调用方 + 本队过滤的正例）。
	defaultMemberID := createUserAs(t, h, "default-member", identity.DefaultTeamID, identity.RoleMemberID, "")
	createUserAs(t, h, "default-admin", identity.DefaultTeamID, identity.RoleAdminID, "default-admin-pass")
	defaultAdminCtx, defaultWho := loginSession(t, h, "default-admin", "default-admin-pass")
	require.Equal(t, identity.DefaultTeamID, defaultWho.GetTeamId())

	// ---- ListUsers：非 owner 只回本队 ----
	ownerList, err := users.ListUsers(owner, &identityv1.ListUsersRequest{})
	require.NoError(t, err)
	all := userNames(ownerList)
	assert.True(t, all["acme-admin"] && all["acme-member"] && all["acme-reader"] && all["default-member"],
		"platform owner sees every team")

	adminList, err := users.ListUsers(adminCtx, &identityv1.ListUsersRequest{})
	require.NoError(t, err)
	acmeNames := userNames(adminList)
	assert.True(t, acmeNames["acme-admin"] && acmeNames["acme-member"] && acmeNames["acme-reader"],
		"team admin sees own team members")
	assert.False(t, acmeNames["default-member"], "team admin must not enumerate other-team users")

	readerList, err := users.ListUsers(readerCtx, &identityv1.ListUsersRequest{})
	require.NoError(t, err)
	assert.Equal(t, acmeNames, userNames(readerList),
		"same-team read-only role sees the same team directory as its admin")

	defaultList, err := users.ListUsers(defaultAdminCtx, &identityv1.ListUsersRequest{})
	require.NoError(t, err)
	defaultNames := userNames(defaultList)
	assert.True(t, defaultNames["default-admin"] && defaultNames["default-member"], "default admin sees own team members")
	assert.False(t, defaultNames["acme-admin"] || defaultNames["acme-member"], "default-team caller must not see acme users")

	// scope 门禁不变：内置 member 没有 users:read，读面在拦截器即 403
	//（本批只加行级 Team 过滤，不扩 member 的目录读权）。
	_, err = users.ListUsers(memberCtx, &identityv1.ListUsersRequest{})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")

	// ---- GetUser：本人 / 同队任一角色 / owner 可用，跨队与缺行统一 403 ----
	self, err := users.GetUser(readerCtx, &identityv1.GetUserRequest{Id: readerWho.GetUserId()})
	require.NoError(t, err)
	assert.Equal(t, "acme-reader", self.GetUser().GetName())

	sameTeam, err := users.GetUser(readerCtx, &identityv1.GetUserRequest{Id: acmePeerID})
	require.NoError(t, err)
	assert.Equal(t, "acme-peer", sameTeam.GetUser().GetName())

	adminRow, err := users.GetUser(adminCtx, &identityv1.GetUserRequest{Id: acmePeerID})
	require.NoError(t, err)
	assert.Equal(t, "acme-peer", adminRow.GetUser().GetName())

	// 读面放行同队非管理员，但写面仍限 owner/admin：同一个人改密被拒。
	_, err = users.SetUserPassword(readerCtx, &identityv1.SetUserPasswordRequest{
		UserId: acmePeerID, Password: "hijacked-pass",
	})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")

	_, err = users.GetUser(adminCtx, &identityv1.GetUserRequest{Id: defaultMemberID})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")
	_, err = users.GetUser(defaultAdminCtx, &identityv1.GetUserRequest{Id: readerID})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")
	// 跨队与不存在同形（403），不泄露用户是否存在。
	_, err = users.GetUser(adminCtx, &identityv1.GetUserRequest{Id: "01M01M01M01M01M01M01M01M01M"})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")

	// 平台管理员：任意用户可读；缺行保持既有 404。
	anyUser, err := users.GetUser(owner, &identityv1.GetUserRequest{Id: acmePeerID})
	require.NoError(t, err)
	assert.Equal(t, "acme-peer", anyUser.GetUser().GetName())
	_, err = users.GetUser(owner, &identityv1.GetUserRequest{Id: "01M01M01M01M01M01M01M01M01M"})
	wantCode(t, err, codes.NotFound, "E_NOT_FOUND")
}
