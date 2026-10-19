package apitest_test

// S2 安全边界验收（R08-H1 / R08-H2 / R14-M2 / R14-M4）：
//   - H1 反转：非 default Team 的 builtin-admin（真实密码登录所得会话）对
//     平台面（EnrollNode/DrainNode/CordonNode/UncordonNode/RestartComponent/
//     RefreshTemplates/TriggerPlatformBackup/ListPlatformBackups）全部 403，
//     且零副作用（Runtime 未受理、未落审计）；平台 owner 同面可用。
//   - H2 反转：跨队 SetUserPassword/DeleteUser → 403；平台管理员身份账号仅
//     平台管理员可管（同队 admin 亦拒）；同队成员改密/删除成功；平台 owner
//     跨队身份写可用。
//   - 角色上限：非平台管理员不得授予 builtin-owner、不得创建声明
//     platform:admin / `*` / 超出自身权限的 Role；成员角色与 ≤ 自身权限的
//     自定义角色可授。
//   - R14-M4：非 default 队 admin 省略 team_id 的 CreateUser/CreateRole/
//     CreateInvitation（与 CreateToken）落在本队。
//
// 身份链全程走产品面（CreateUser/Login），不直插 token 表造特权 Token。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// loginSession 以密码自证铸会话 Token（产品面 Login）并回读 WhoAmI。
func loginSession(t *testing.T, h *apitest.Harness, name, password string) (context.Context, *identityv1.WhoAmIResponse) {
	t.Helper()
	users := identityv1.NewUsersServiceClient(h.Conn)
	resp, err := users.Login(context.Background(), &identityv1.LoginRequest{Name: name, Password: password})
	require.NoError(t, err)
	ctx := sdk.WithToken(context.Background(), resp.GetSecret())
	who, err := users.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	require.NoError(t, err)
	return ctx, who
}

// createUserAs 以 h.Ctx()（bootstrap owner）建用户并返回用户 ID。
func createUserAs(t *testing.T, h *apitest.Harness, name, teamID, roleID, password string) string {
	t.Helper()
	resp, err := identityv1.NewUsersServiceClient(h.Conn).CreateUser(h.Ctx(), &identityv1.CreateUserRequest{
		Name: name, TeamId: teamID, RoleId: roleID, Password: password,
	})
	require.NoError(t, err)
	return resp.GetUser().GetId()
}

// teamAdminSession 造一个非 default Team 的 builtin-admin 会话（owner 建队 +
// 建用户，用户经 PUBLIC Login 自证）。
func teamAdminSession(t *testing.T, h *apitest.Harness, teamName, userName, password string) (teamID string, ctx context.Context) {
	t.Helper()
	team, err := identityv1.NewTeamsServiceClient(h.Conn).CreateTeam(h.Ctx(), &identityv1.CreateTeamRequest{Name: teamName})
	require.NoError(t, err)
	teamID = team.GetTeam().GetId()
	createUserAs(t, h, userName, teamID, identity.RoleAdminID, password)
	ctx, who := loginSession(t, h, userName, password)
	require.Equal(t, teamID, who.GetTeamId())
	require.Equal(t, identity.RoleAdminID, who.GetRoleId())
	return teamID, ctx
}

// TestPlatformAdminGateReversesH1：R08-H1 复现链反转为 403（非 default 队
// builtin-admin 对平台面零触达），平台 owner 同面保持可用。
func TestPlatformAdminGateReversesH1(t *testing.T) {
	h := apitest.New(t)
	owner := h.Ctx()
	_, adminCtx := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")

	nodes := runtimev1.NewNodesServiceClient(h.Conn)
	system := systemv1.NewSystemServiceClient(h.Conn)
	platform := systemv1.NewPlatformServiceClient(h.Conn)
	templates := deliveryv1.NewTemplatesServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)

	// ---- 攻击面：全部 E_FORBIDDEN（复现场景逐个反转） ----
	for name, call := range map[string]func() error{
		"EnrollNode":         func() error { _, e := nodes.EnrollNode(adminCtx, &runtimev1.EnrollNodeRequest{}); return e },
		"EnrollNode(rotate)": func() error { _, e := nodes.EnrollNode(adminCtx, &runtimev1.EnrollNodeRequest{Rotate: true}); return e },
		"DrainNode": func() error {
			_, e := nodes.DrainNode(adminCtx, &runtimev1.DrainNodeRequest{NodeId: "node-1"})
			return e
		},
		"CordonNode": func() error {
			_, e := nodes.CordonNode(adminCtx, &runtimev1.CordonNodeRequest{NodeId: "node-1"})
			return e
		},
		"UncordonNode": func() error {
			_, e := nodes.UncordonNode(adminCtx, &runtimev1.UncordonNodeRequest{NodeId: "node-1"})
			return e
		},
		"RestartComponent": func() error {
			_, e := system.RestartComponent(adminCtx, &systemv1.RestartComponentRequest{Name: "traefik"})
			return e
		},
		"RefreshTemplates": func() error {
			_, e := templates.RefreshTemplates(adminCtx, &deliveryv1.RefreshTemplatesRequest{})
			return e
		},
		"TriggerPlatformBackup": func() error {
			_, e := platform.TriggerPlatformBackup(adminCtx, &systemv1.TriggerPlatformBackupRequest{})
			return e
		},
		"ListPlatformBackups": func() error {
			_, e := platform.ListPlatformBackups(adminCtx, &systemv1.ListPlatformBackupsRequest{})
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, call(), codes.PermissionDenied, "E_FORBIDDEN")
		})
	}

	// 零副作用：Runtime 未受理任何材料/节点动作，审计未落 node.* 行。
	assert.Empty(t, h.Runtime.EnrollCalls(), "denied EnrollNode must not reach the runtime")
	assert.Empty(t, h.Runtime.AdminCalls(), "denied node verbs must not reach the runtime")
	entries, err := auditq.ListAudit(owner, &identityv1.ListAuditRequest{Action: "node.", Limit: 1000})
	require.NoError(t, err)
	assert.Empty(t, entries.GetEntries(), "denied platform faces must not write audit rows")

	// ---- 平台 owner：同面可用（门的正例） ----
	kit, err := nodes.EnrollNode(owner, &runtimev1.EnrollNodeRequest{Rotate: true})
	require.NoError(t, err)
	assert.Contains(t, kit.GetJoinCommand(), "docker swarm join")
	assert.Equal(t, []bool{true}, h.Runtime.EnrollCalls())

	_, err = nodes.DrainNode(owner, &runtimev1.DrainNodeRequest{NodeId: "node-1"})
	require.NoError(t, err)
	_, err = nodes.CordonNode(owner, &runtimev1.CordonNodeRequest{NodeId: "node-1"})
	require.NoError(t, err)
	_, err = nodes.UncordonNode(owner, &runtimev1.UncordonNodeRequest{NodeId: "node-1"})
	require.NoError(t, err)
	assert.Len(t, h.Runtime.AdminCalls(), 3)

	// 引擎路由到业务错误（不是 403）即证明门已放行。
	_, err = system.RestartComponent(owner, &systemv1.RestartComponentRequest{Name: "traefik"})
	wantCode(t, err, codes.NotFound, "E_NOT_FOUND")

	// 未配置 catalog URL 的精确拒绝（门后业务分支），不是 403。
	_, err = templates.RefreshTemplates(owner, &deliveryv1.RefreshTemplatesRequest{})
	wantCode(t, err, codes.InvalidArgument, "E_INVALID_ARGUMENT")

	// 未配置平台备份的精确失败（门后引擎分支），不是 403。
	_, err = platform.TriggerPlatformBackup(owner, &systemv1.TriggerPlatformBackupRequest{})
	wantCode(t, err, codes.FailedPrecondition, "E_PLATFORM_BACKUP_FAILED")
	_, err = platform.ListPlatformBackups(owner, &systemv1.ListPlatformBackupsRequest{})
	wantCode(t, err, codes.FailedPrecondition, "E_PLATFORM_BACKUP_FAILED")
}

// TestIdentityWriteOwnershipReversesH2：R08-H2 复现链反转为 403——跨队改密/
// 删号零副作用，平台管理员身份账号仅平台管理员可管；同队管理与平台 owner
// 跨队管理保持可用。
func TestIdentityWriteOwnershipReversesH2(t *testing.T) {
	h := apitest.New(t)
	owner := h.Ctx()
	acmeTeam, adminCtx := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")

	users := identityv1.NewUsersServiceClient(h.Conn)
	// 默认队普通成员（跨队攻击目标；未设密）。
	plainUser := createUserAs(t, h, "plain-user", identity.DefaultTeamID, identity.RoleMemberID, "")
	// 默认队平台 owner 用户（"平台管理员身份"目标；未设密）。
	rootOwner := createUserAs(t, h, "root-owner", identity.DefaultTeamID, identity.RoleOwnerID, "")
	// 默认队 admin（同队但非平台管理员：对平台 owner 账号亦不得动手）。
	createUserAs(t, h, "default-admin", identity.DefaultTeamID, identity.RoleAdminID, "default-admin-pass")
	defaultAdminCtx, who := loginSession(t, h, "default-admin", "default-admin-pass")
	require.Equal(t, identity.RoleAdminID, who.GetRoleId())
	// acme 队成员：一个用于同队改密，一个用于同队删号（删号目标不建 Token）。
	acmeDev := createUserAs(t, h, "acme-dev", acmeTeam, identity.RoleMemberID, "acme-dev-old")
	acmeGone := createUserAs(t, h, "acme-gone", acmeTeam, identity.RoleMemberID, "")

	// ---- H2 攻击链：非 default 队 admin 重置默认队账号 → 403 ----
	for name, call := range map[string]func() error{
		"SetUserPassword(default member)": func() error {
			_, e := users.SetUserPassword(adminCtx, &identityv1.SetUserPasswordRequest{
				UserId: plainUser, Password: "hijacked-pass",
			})
			return e
		},
		"DeleteUser(default member)": func() error {
			_, e := users.DeleteUser(adminCtx, &identityv1.DeleteUserRequest{Id: plainUser})
			return e
		},
		"SetUserPassword(default platform owner)": func() error {
			_, e := users.SetUserPassword(adminCtx, &identityv1.SetUserPasswordRequest{
				UserId: rootOwner, Password: "hijacked-pass",
			})
			return e
		},
		"DeleteUser(default platform owner)": func() error {
			_, e := users.DeleteUser(adminCtx, &identityv1.DeleteUserRequest{Id: rootOwner})
			return e
		},
		"SetUserPassword(platform owner, same team)": func() error {
			_, e := users.SetUserPassword(defaultAdminCtx, &identityv1.SetUserPasswordRequest{
				UserId: rootOwner, Password: "hijacked-pass",
			})
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, call(), codes.PermissionDenied, "E_FORBIDDEN")
		})
	}
	// 零副作用：密码未被改写（受害账号仍无密码会话），用户行仍在。
	targets := identityv1.NewUsersServiceClient(h.Conn)
	for _, id := range []string{plainUser, rootOwner} {
		u, err := targets.GetUser(owner, &identityv1.GetUserRequest{Id: id})
		require.NoError(t, err)
		assert.Equal(t, id, u.GetUser().GetId())
	}
	_, err := users.Login(context.Background(), &identityv1.LoginRequest{Name: "plain-user", Password: "hijacked-pass"})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")
	_, err = users.Login(context.Background(), &identityv1.LoginRequest{Name: "root-owner", Password: "hijacked-pass"})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")

	// ---- 合法动线：同队 admin 管本队成员 ----
	_, err = users.SetUserPassword(adminCtx, &identityv1.SetUserPasswordRequest{
		UserId: acmeDev, Password: "acme-dev-new",
	})
	require.NoError(t, err)
	_, devWho := loginSession(t, h, "acme-dev", "acme-dev-new")
	assert.Equal(t, acmeTeam, devWho.GetTeamId(), "password reset must land in the caller's team")
	_, err = identityv1.NewTokensServiceClient(h.Conn).RevokeToken(owner, &identityv1.RevokeTokenRequest{Id: devWho.GetTokenId()})
	require.NoError(t, err)
	_, err = users.DeleteUser(adminCtx, &identityv1.DeleteUserRequest{Id: acmeGone})
	require.NoError(t, err)
	_, err = users.GetUser(owner, &identityv1.GetUserRequest{Id: acmeGone})
	wantCode(t, err, codes.NotFound, "E_NOT_FOUND")

	// ---- 平台 owner：跨队身份写与平台管理员账号管理可用 ----
	_, err = users.SetUserPassword(owner, &identityv1.SetUserPasswordRequest{
		UserId: rootOwner, Password: "platform-owner-pass",
	})
	require.NoError(t, err)
	_, rootWho := loginSession(t, h, "root-owner", "platform-owner-pass")
	assert.Equal(t, identity.DefaultTeamID, rootWho.GetTeamId())
	assert.Equal(t, identity.RoleOwnerID, rootWho.GetRoleId())
	_, err = users.SetUserPassword(owner, &identityv1.SetUserPasswordRequest{
		UserId: acmeDev, Password: "acme-dev-owner-set",
	})
	require.NoError(t, err)
	_, err = users.DeleteUser(owner, &identityv1.DeleteUserRequest{Id: plainUser})
	require.NoError(t, err)
}

// TestRoleGrantCeilingAndTeamDefault：角色授予特权上限（builtin-owner /
// platform:admin / `*` / 超自身权限全部 403，零副作用）与 R14-M4（非 default
// 队 admin 省略 team_id 的创建请求落本队）。
func TestRoleGrantCeilingAndTeamDefault(t *testing.T) {
	h := apitest.New(t)
	owner := h.Ctx()
	acme, adminCtx := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")

	users := identityv1.NewUsersServiceClient(h.Conn)
	roles := identityv1.NewRolesServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)
	invitations := identityv1.NewInvitationsServiceClient(h.Conn)

	// ---- 上限负例：builtin-owner 与平台面 scope 一律 403 ----
	for name, call := range map[string]func() error{
		"CreateToken(owner role)": func() error {
			_, e := tokens.CreateToken(adminCtx, &identityv1.CreateTokenRequest{
				Name: "escalate", RoleId: identity.RoleOwnerID,
			})
			return e
		},
		"CreateUser(owner role)": func() error {
			_, e := users.CreateUser(adminCtx, &identityv1.CreateUserRequest{
				Name: "evil-owner", RoleId: identity.RoleOwnerID,
			})
			return e
		},
		"CreateInvitation(owner role)": func() error {
			_, e := invitations.CreateInvitation(adminCtx, &identityv1.CreateInvitationRequest{
				RoleId: identity.RoleOwnerID,
			})
			return e
		},
		"CreateRole(platform)": func() error {
			_, e := roles.CreateRole(adminCtx, &identityv1.CreateRoleRequest{
				Name: "platform-writer", Scopes: []string{"platform:admin"},
			})
			return e
		},
		"CreateRole(wildcard)": func() error {
			_, e := roles.CreateRole(adminCtx, &identityv1.CreateRoleRequest{
				Name: "root-wildcard", Scopes: []string{"*"},
			})
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, call(), codes.PermissionDenied, "E_FORBIDDEN")
		})
	}
	// 零副作用：越权 Role/User 未落行。
	roleList, err := roles.ListRoles(owner, &identityv1.ListRolesRequest{})
	require.NoError(t, err)
	for _, ro := range roleList.GetRoles() {
		assert.NotEqual(t, "platform-writer", ro.GetName(), "denied CreateRole must not persist")
		assert.NotEqual(t, "root-wildcard", ro.GetName(), "denied CreateRole must not persist")
	}
	userList, err := users.ListUsers(owner, &identityv1.ListUsersRequest{})
	require.NoError(t, err)
	for _, u := range userList.GetUsers() {
		assert.NotEqual(t, "evil-owner", u.GetName(), "denied CreateUser must not persist")
	}

	// ---- R14-M4：省略 team_id 落在调用方 Team ----
	custom, err := roles.CreateRole(adminCtx, &identityv1.CreateRoleRequest{
		Name: "acme-deployer", Scopes: []string{"deployments:write", "projects:read"},
	})
	require.NoError(t, err)
	assert.Equal(t, acme, custom.GetRole().GetTeamId(), "omitted team_id must resolve to the caller's team")

	_, err = users.CreateUser(adminCtx, &identityv1.CreateUserRequest{
		Name: "acme-member", RoleId: identity.RoleMemberID, Password: "acme-member-pass",
	})
	require.NoError(t, err)
	_, memberWho := loginSession(t, h, "acme-member", "acme-member-pass")
	assert.Equal(t, acme, memberWho.GetTeamId(), "CreateUser without team_id must land in the caller's team")
	assert.Equal(t, identity.RoleMemberID, memberWho.GetRoleId())
	assert.Equal(t, "acme-member", memberWho.GetUserName())
	inv, err := invitations.CreateInvitation(adminCtx, &identityv1.CreateInvitationRequest{
		RoleId: identity.RoleMemberID,
	})
	require.NoError(t, err)
	assert.Equal(t, acme, inv.GetInvitation().GetTeamId(), "CreateInvitation without team_id must land in the caller's team")

	minted, err := tokens.CreateToken(adminCtx, &identityv1.CreateTokenRequest{
		Name: "acme-ci", RoleId: identity.RoleMemberID,
	})
	require.NoError(t, err)
	assert.Equal(t, acme, minted.GetToken().GetTeamId(), "CreateToken without team_id must land in the caller's team")
}
