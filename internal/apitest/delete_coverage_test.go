package apitest_test

// Delete RPC 覆盖补齐（批 0 阶段 5 / 守卫 C-b）：DeleteApp/DeleteProject
// 已在 delete_app_test.go；本文件补 identity 三个 Delete 与 DeleteSecret/
// DeleteRoute——每个 RPC 落"活跃下级拒绝"或"显式删除生效"至少一形态，
// 缺口由 internal/guards 的覆盖反扫钉死（新 Delete RPC 无用例即 CI 红）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/identity"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestIdentityDeletesCovered：DeleteUser/DeleteTeam/DeleteRole 三面——
// 活跃下级（仍被 Token 行引用）FK 拒删（E_CONFLICT，Q-12 中性冲突文案），
// 无引用时删除生效（Get → E_NOT_FOUND）。
func TestIdentityDeletesCovered(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	users := identityv1.NewUsersServiceClient(h.Conn)
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	roles := identityv1.NewRolesServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)

	// ---- DeleteUser：名下 Token 行仍引用 → FK RESTRICT 拒删；无引用 →
	// 删除生效。注意：user repo 未做 team/role 式的 FK→ErrConflict 归一，
	// 拒绝面现以 E_INTERNAL 呈现（批 0 阶段 5 不改服务实现，遗留随下个
	// identity 批收口）——此处只钉"拒删"事实，不钉错误码。----
	dave, err := users.CreateUser(ctx, &identityv1.CreateUserRequest{Name: "dave", RoleId: identity.RoleMemberID})
	require.NoError(t, err)
	_, err = tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "dave-ci", RoleId: identity.RoleMemberID, UserId: dave.GetUser().GetId(),
	})
	require.NoError(t, err)
	_, err = users.DeleteUser(ctx, &identityv1.DeleteUserRequest{Id: dave.GetUser().GetId()})
	require.Error(t, err, "user with a live token row must not be deletable (FK RESTRICT)")

	carol, err := users.CreateUser(ctx, &identityv1.CreateUserRequest{Name: "carol", RoleId: identity.RoleMemberID})
	require.NoError(t, err)
	_, err = users.DeleteUser(ctx, &identityv1.DeleteUserRequest{Id: carol.GetUser().GetId()})
	require.NoError(t, err)
	_, err = users.GetUser(ctx, &identityv1.GetUserRequest{Id: carol.GetUser().GetId()})
	require.Error(t, err)
	assert.Equal(t, "E_NOT_FOUND", appErrCode(t, err))

	// ---- DeleteTeam：Team 内 Token 引用 → FK 拒；空 Team → 删除生效。----
	sandbox, err := teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: "sandbox"})
	require.NoError(t, err)
	_, err = tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "sandbox-ci", TeamId: sandbox.GetTeam().GetId(), RoleId: identity.RoleMemberID,
	})
	require.NoError(t, err)
	_, err = teams.DeleteTeam(ctx, &identityv1.DeleteTeamRequest{Id: sandbox.GetTeam().GetId()})
	require.Error(t, err, "team with a live token row must not be deletable")
	assert.Equal(t, "E_CONFLICT", appErrCode(t, err))

	empty, err := teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: "ephemeral"})
	require.NoError(t, err)
	_, err = teams.DeleteTeam(ctx, &identityv1.DeleteTeamRequest{Id: empty.GetTeam().GetId()})
	require.NoError(t, err)
	_, err = teams.GetTeam(ctx, &identityv1.GetTeamRequest{Id: empty.GetTeam().GetId()})
	require.Error(t, err)
	assert.Equal(t, "E_NOT_FOUND", appErrCode(t, err))

	// ---- DeleteRole：Token 挂靠的 Role → FK 拒；未挂靠自定义 Role → 删成。----
	claimed, err := roles.CreateRole(ctx, &identityv1.CreateRoleRequest{Name: "claimed", Scopes: []string{"deployments:read"}})
	require.NoError(t, err)
	_, err = tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "role-holder", RoleId: claimed.GetRole().GetId(),
	})
	require.NoError(t, err)
	_, err = roles.DeleteRole(ctx, &identityv1.DeleteRoleRequest{Id: claimed.GetRole().GetId()})
	require.Error(t, err, "role referenced by a token must not be deletable")
	assert.Equal(t, "E_CONFLICT", appErrCode(t, err))

	unused, err := roles.CreateRole(ctx, &identityv1.CreateRoleRequest{Name: "unused", Scopes: []string{"deployments:read"}})
	require.NoError(t, err)
	_, err = roles.DeleteRole(ctx, &identityv1.DeleteRoleRequest{Id: unused.GetRole().GetId()})
	require.NoError(t, err)
	_, err = roles.GetRole(ctx, &identityv1.GetRoleRequest{Id: unused.GetRole().GetId()})
	require.Error(t, err)
	assert.Equal(t, "E_NOT_FOUND", appErrCode(t, err))
}

// TestMaterialAndRouteDeletesCovered：DeleteSecret（软删后列表不可见）与
// DeleteRoute（撤下后列表不可见，Proxy 全量发布即时触发）各落显式删除
// 生效形态。
func TestMaterialAndRouteDeletesCovered(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	routes := proxyv1.NewRoutesServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "materials"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	// ---- DeleteSecret：put → delete → 指纹列表不再可见。----
	_, err = secrets.PutSecret(ctx, &structurev1.PutSecretRequest{ProjectId: projectID, Name: "conn-string", Value: "s3cr3t"})
	require.NoError(t, err)
	_, err = secrets.DeleteSecret(ctx, &structurev1.DeleteSecretRequest{ProjectId: projectID, Name: "conn-string"})
	require.NoError(t, err)
	list, err := secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	for _, s := range list.GetSecrets() {
		require.NotEqual(t, "conn-string", s.GetName(), "deleted secret must not be listed")
	}

	// ---- DeleteRoute：create → delete → 列表不再可见。----
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projectID, Name: "web"})
	require.NoError(t, err)
	route, err := routes.CreateRoute(ctx, &proxyv1.CreateRouteRequest{
		ProjectId: projectID, Host: "gone.materials.test",
		AppId: app.GetApp().GetId(), Process: "web", Port: 8000,
	})
	require.NoError(t, err)
	_, err = routes.DeleteRoute(ctx, &proxyv1.DeleteRouteRequest{Id: route.GetRoute().GetId()})
	require.NoError(t, err)
	routeList, err := routes.ListRoutes(ctx, &proxyv1.ListRoutesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.False(t, routeListed(t, routeList, route.GetRoute().GetId()),
		"deleted route must be withdrawn from the list")
}
