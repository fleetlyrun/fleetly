package fleetlygrpc

// 身份写面授权（S2 安全边界 / R08-H2）：用户管理归属门 + 角色授予特权
// 上限。数据面行级授权（authz.go）答"能不能碰这行"；本文件答"能不能碰
// 这个账号/这项授权"——identity 面的目标是用户与角色本身，无 Project
// 归属链可解析。
//
// 两条判定都在受理检查（acceptanceCheck）内运行：repo 由事务注入
// （transact.Stores），判定与写同事务、先于写——拒绝零副作用。SQLite
// 连接池收口为 1（persistence.Open），事务内不得回读库级 repo（会等
// 自己），这是 repo 必须作为参数传入的原因。

import (
	"context"

	"github.com/lynx-go/grpcapi/authz"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// authorizeUserAdmin 判定调用方能否管理目标用户（SetUserPassword/DeleteUser
// 的归属门）：
//  1. 平台管理员（default Team 的 builtin-owner）→ 放行；
//  2. 目标用户持有平台管理员身份（default Team 的 builtin-owner）→ 仅平台
//     管理员可操作（同队 admin 也不行——该账号可登录成平台 owner）；
//  3. 调用者本人 → 放行（自助改密/自销；自助面不引入旧密码自证）；
//  4. 调用者角色 ∈ {builtin-owner, builtin-admin} 且目标用户在其 Team 持有
//     membership → 放行（队内管理员管本队成员）；
//     其余一律 E_FORBIDDEN。
//
// 第 2 步先于第 3/4 步：平台管理员身份的判定不因"同队"或"本人"降级；
// 多队用户的会话 Team 由 Login 首个 membership 决定（现行语义）。
func (s *Services) authorizeUserAdmin(ctx context.Context, memberships membership.Repo, targetUserID string) error {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return apperr.New("E_UNAUTHENTICATED", "present a valid token to manage user accounts")
	}
	if callerIsPlatformAdmin(id) {
		return nil
	}
	rows, err := memberships.ListByUser(ctx, targetUserID)
	if err != nil {
		return mapStateError(err, "membership")
	}
	for i := range rows {
		if rows[i].TeamID == identity.DefaultTeamID && rows[i].RoleID == identity.RoleOwnerID {
			return apperr.New("E_FORBIDDEN",
				"user %s is a platform administrator; only a platform administrator may manage this account", targetUserID).
				WithSuggestion("Ask the platform owner (the builtin-owner of the default team) to perform this operation.")
		}
	}
	if id.UserID != "" && targetUserID == id.UserID {
		return nil
	}
	if id.RoleID != identity.RoleOwnerID && id.RoleID != identity.RoleAdminID {
		return apperr.New("E_FORBIDDEN",
			"managing user %s requires a builtin-owner or builtin-admin role in the user's team", targetUserID).
			WithSuggestion("Ask an owner or admin of the user's team to perform this operation.")
	}
	for i := range rows {
		if rows[i].TeamID == id.TeamID {
			return nil
		}
	}
	return apperr.New("E_FORBIDDEN",
		"user %s holds no membership in the caller's team %s; tokens operate within their own team", targetUserID, id.TeamID).
		WithSuggestion("Use a token of the team that holds the user, or ask the platform owner.")
}

// authorizeRoleGrant 判定调用方能否授予 roleID（角色授予特权上限）：
//   - 平台管理员 → 放行（builtin-owner 亦只在此分支放行）；
//   - 其余调用者：逐条比对 Role.Scopes 是否被自身 scope 覆盖（覆盖语义
//     复用 authn.Identity.HasScope——admin ⇒ write ⇒ read 的蕴含已由
//     identity.DotForm 展开物化）；
//   - `*`（owner 通配）与 platform 资源 scope 在任何非平台分支一律拒绝
//     （前者等价平台 owner，后者是平台面资源——与 handler 平台门同口径；
//     内建 admin 模板虽携带 platform:admin，非平台管理员不得再转授）。
func (s *Services) authorizeRoleGrant(ctx context.Context, roles role.Repo, roleID string) error {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return apperr.New("E_UNAUTHENTICATED", "present a valid token to grant roles")
	}
	if callerIsPlatformAdmin(id) {
		return nil
	}
	if roleID == identity.RoleOwnerID {
		return apperr.New("E_FORBIDDEN",
			"role %s is the platform-owner role; only a platform administrator may grant it", roleID).
			WithSuggestion("Ask the platform owner to grant this role, or grant a role within your own authority.")
	}
	ro, err := roles.Get(ctx, roleID)
	if err != nil {
		return err
	}
	scopes, err := identity.ParseScopes(ro.Scopes, s.ScopeVocabulary)
	if err != nil {
		// 内置角色由代码单一源刷新、自定义角色创建时已过词表——此处恒
		// 不可达；仍诚实失败（fail-closed）。
		return apperr.New("E_INTERNAL", "role %s carries scopes outside the scope vocabulary", roleID).WithCause(err)
	}
	return authorizeScopeGrant(id, scopes)
}

// authorizeScopeGrant 校验调用者覆盖待授予的每条 scope（角色上限的共用
// 判定核：既有角色经 authorizeRoleGrant 读回 scopes，CreateRole 直接传入
// 请求解析结果）。
func authorizeScopeGrant(id *authn.Identity, scopes []identity.Scope) error {
	for _, sc := range scopes {
		switch sc.Resource {
		case "*":
			return apperr.New("E_FORBIDDEN",
				"scope %s is the platform-owner wildcard; only a platform administrator may grant it", sc.String()).
				WithSuggestion("Grant concrete resource:action scopes within your own authority instead.")
		case "platform":
			return apperr.New("E_FORBIDDEN",
				"scope %s is platform-scoped; only a platform administrator may grant it", sc.String()).
				WithSuggestion("Ask the platform owner to grant platform-scoped roles, or drop this scope.")
		}
		if !id.HasScope(sc.Resource, authz.ScopeOp(sc.Action)) {
			return apperr.New("E_FORBIDDEN",
				"scope %s exceeds the caller's own authority; a role grant cannot exceed what the caller holds", sc.String()).
				WithSuggestion("Grant only scopes your own token carries (WhoAmI lists them), or ask the platform owner.")
		}
	}
	return nil
}

// userAdmin 是 SetUserPassword/DeleteUser 的受理检查形态（事务内运行：
// membership 读与写同事务，拒绝零副作用）。
func (s *Services) userAdmin(targetUserID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		return s.authorizeUserAdmin(ctx, st.Memberships, targetUserID)
	}
}

// roleGrant 是 CreateUser/CreateToken/CreateInvitation 的受理检查形态
// （事务内读 Role——授权上限判定先于写）。
func (s *Services) roleGrant(roleID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		return s.authorizeRoleGrant(ctx, st.Roles, roleID)
	}
}

// scopeGrant 是 CreateRole 的受理检查形态（请求 scopes 的逐条上限判定；
// 先于建 Role）。
func (s *Services) scopeGrant(scopes []identity.Scope) acceptanceCheck {
	return func(ctx context.Context, _ transact.Stores) error {
		id, ok := authn.FromContext(ctx)
		if !ok {
			return apperr.New("E_UNAUTHENTICATED", "present a valid token to create roles")
		}
		if callerIsPlatformAdmin(id) {
			return nil
		}
		return authorizeScopeGrant(id, scopes)
	}
}
