package identitygrpc

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// authorizeUserAdmin 判定调用方能否管理目标用户（SetUserPassword/DeleteUser
// 的归属门）：
//  1. 平台管理员（default Team 的 builtin-owner）→ 放行；
//  2. 目标用户持有平台管理员身份（default Team 的 builtin-owner）→ 仅平台
//     管理员可操作（同队 admin 也不行——该账号可登录成平台 owner）；
//  3. 调用者本人 → 放行（自助改密/自销；自助面不引入旧密码自证）；
//  4. 调用者角色 ∈ {builtin-owner, builtin-admin} 且目标的**登录队**（首个
//     membership——ListByUser 的 seq 稳定序正是 Login 铸 Token 的解析源，
//     identity.go memberships[0] 同源同序）是调用方 Team → 放行；
//     其余一律 E_FORBIDDEN。
//
// 第 2 步先于第 3/4 步：平台管理员身份的判定不因"同队"或"本人"降级。
// 第 4 步按登录队对齐而非「任一 membership 在本队」：队内管理员若能重置
// 「本队挂名、登录队在他队」的多队用户密码，PUBLIC Login 按 memberships[0]
// 铸 Token 即等价取得他队身份（跨队 member 读）——队内管理的能力边界必须
// 与 Login 的会话语义严格对齐。多队用户的产品面路径与 Login 队选择语义整
// 体待权限模型 ADR。
func (d Deps) authorizeUserAdmin(ctx context.Context, memberships membership.Repo, targetUserID string) error {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return apperr.New("E_UNAUTHENTICATED", "present a valid token to manage user accounts")
	}
	if grpcapi.CallerIsPlatformAdmin(id) {
		return nil
	}
	rows, err := memberships.ListByUser(ctx, targetUserID)
	if err != nil {
		return grpcapi.MapStateError(err, "membership")
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
	// 登录队对齐（首个 membership，seq 稳定序）：任一 membership 在本队即放
	// 行的历史形态对多队用户是跨队读洞——会话语义由 memberships[0] 决定，
	// 队内管理的能力边界不得宽于它。
	if len(rows) > 0 && rows[0].TeamID == id.TeamID {
		return nil
	}
	return apperr.New("E_FORBIDDEN",
		"user %s does not log into the caller's team %s; team administrators may only manage accounts whose login team (first membership) is their own", targetUserID, id.TeamID).
		WithSuggestion("Use a token of the user's login team, or ask the platform owner.")
}

// authorizeUserRead 判定调用方能否读目标用户（R14-M2 读面残留；S2 只修了
// 写面）：
//  1. 平台管理员（default Team 的 builtin-owner）→ 任意用户；
//  2. 调用者本人 → 放行（WhoAmI 之外的 GetUser 自助读）；
//  3. 目标用户在调用方 Team 持有任一 membership（任意角色）→ 放行；
//  4. 其余一律 E_FORBIDDEN——含目标不存在：读面不泄露跨队用户是否存在，
//     跨队与缺行统一 403（与写面 authorizeUserAdmin 的角色门区分：读面
//     允许同队 member，写面仍限 owner/admin）。
func (d Deps) authorizeUserRead(ctx context.Context, targetUserID string) error {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return apperr.New("E_UNAUTHENTICATED", "present a valid token to read user accounts")
	}
	if grpcapi.CallerIsPlatformAdmin(id) {
		return nil
	}
	if id.UserID != "" && targetUserID == id.UserID {
		return nil
	}
	rows, err := d.Memberships.ListByUser(ctx, targetUserID)
	if err != nil {
		return grpcapi.MapStateError(err, "membership")
	}
	for i := range rows {
		if rows[i].TeamID == id.TeamID {
			return nil
		}
	}
	return apperr.New("E_FORBIDDEN",
		"user %s holds no membership in the caller's team %s", targetUserID, id.TeamID).
		WithSuggestion("Use a token of the team that holds the user, or ask the platform owner.")
}

// authorizeRoleGrant 判定调用方能否授予 roleID（角色授予特权上限）：
//   - 平台管理员 → 放行（builtin-owner 亦只在此分支放行）；
//   - 其余调用者：逐条比对 Role.Scopes 是否被自身 scope 覆盖（覆盖语义
//     复用 authn.Identity.HasScope——admin ⇒ write ⇒ read 的蕴含已由
//     identity.ScopeSet 展开物化）；
//   - `*`（owner 通配）与 platform 资源 scope 在任何非平台分支一律拒绝
//     （前者等价平台 owner，后者是平台面资源——与 handler 平台门同口径；
//     内建 admin 模板虽携带 platform:admin，非平台管理员不得再转授）。
func (d Deps) authorizeRoleGrant(ctx context.Context, roles role.Repo, roleID string) error {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return apperr.New("E_UNAUTHENTICATED", "present a valid token to grant roles")
	}
	if grpcapi.CallerIsPlatformAdmin(id) {
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
	scopes, err := identity.ParseScopes(ro.Scopes, d.ScopeVocabulary)
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
		if !id.HasScope(sc.Resource, sc.Action) {
			return apperr.New("E_FORBIDDEN",
				"scope %s exceeds the caller's own authority; a role grant cannot exceed what the caller holds", sc.String()).
				WithSuggestion("Grant only scopes your own token carries (WhoAmI lists them), or ask the platform owner.")
		}
	}
	return nil
}

// userAdmin 是 SetUserPassword/DeleteUser 的受理检查形态（事务内运行：
// membership 读与写同事务，拒绝零副作用）。
func (d Deps) userAdmin(targetUserID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		return d.authorizeUserAdmin(ctx, st.Memberships, targetUserID)
	}
}

// roleGrant 是 CreateUser/CreateToken/CreateInvitation 的受理检查形态
// （事务内读 Role——授权上限判定先于写）。
func (d Deps) roleGrant(roleID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		return d.authorizeRoleGrant(ctx, st.Roles, roleID)
	}
}

// scopeGrant 是 CreateRole 的受理检查形态（请求 scopes 的逐条上限判定；
// 先于建 Role）。
func (d Deps) scopeGrant(scopes []identity.Scope) grpcapi.Check {
	return func(ctx context.Context, _ transact.Stores) error {
		id, ok := authn.FromContext(ctx)
		if !ok {
			return apperr.New("E_UNAUTHENTICATED", "present a valid token to create roles")
		}
		if grpcapi.CallerIsPlatformAdmin(id) {
			return nil
		}
		return authorizeScopeGrant(id, scopes)
	}
}

// userInTeam 校验 user 在 team 持有 membership（token 创建的用户绑定面）。
func (d Deps) userInTeam(userID, teamID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Memberships.GetByUser(ctx, userID, teamID)
		if err != nil {
			return apperr.New("E_CONFLICT",
				"user %s holds no membership in team %s; a user-bound token is capped by its creator's current access (ADR-0038) and would carry no authority", userID, teamID)
		}
		return nil
	}
}

// teamExists 校验 Team 行存在。
func (d Deps) teamExists(id string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Teams.Get(ctx, id)
		return err
	}
}

// userExists 校验 User 行存在。
func (d Deps) userExists(id string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Users.Get(ctx, id)
		return err
	}
}

// roleInTeam 校验 Role 存在性与 Team 归属（内置角色平台级可任意授予）。
func (d Deps) roleInTeam(roleID, teamID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		ro, err := st.Roles.Get(ctx, roleID)
		if err != nil {
			return err
		}
		if ro.Builtin {
			return nil
		}
		if ro.TeamID != teamID {
			return apperr.New("E_CONFLICT",
				"role %s belongs to team %s; it cannot be granted in team %s", roleID, ro.TeamID, teamID)
		}
		return nil
	}
}

// authorizeTokenRow 校验 Token 行归属 Team（owner 豁免）。
func (d Deps) authorizeTokenRow(ctx context.Context, t *token.Token) error {
	teamID, owner, err := grpcapi.CallerTeam(ctx)
	if err != nil {
		return err
	}
	if owner || t.TeamID == teamID {
		return nil
	}
	return apperr.New("E_FORBIDDEN",
		"token %s belongs to team %s; the caller's team is %s", t.Name, t.TeamID, teamID).
		WithSuggestion("Use a platform owner token to manage tokens of other teams.")
}
