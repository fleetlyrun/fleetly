package grpcapi

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// CallerTeam 解析调用方 Team 轴：owner=true 表示平台管理员（default Team
// 的 builtin-owner——bootstrap 引导 Token 即此档；其他 Team 的 owner 是该队
// 管理员，不跨队——networkpeer 安全批测试钉死"他队 owner 不得豁免"）。
// 数据面全部 SERVER 档，匿名到不了这里；防御性返回 E_UNAUTHENTICATED
// （fail-closed）。
func CallerTeam(ctx context.Context) (teamID string, owner bool, err error) {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return "", false, apperr.New("E_UNAUTHENTICATED", "present a valid token to access team resources")
	}
	return id.TeamID, CallerIsPlatformAdmin(id), nil
}

// CallerIsPlatformAdmin 报告身份是否平台管理员：default Team 的
// builtin-owner（ADR-0035 决策 2）。平台面门（H1）与身份写面归属门（H2）
// 共用的唯一判定点——别处不得再复制 role/team 双条件。
func CallerIsPlatformAdmin(id *authn.Identity) bool {
	return id != nil && id.RoleID == identity.RoleOwnerID && id.TeamID == identity.DefaultTeamID
}

// TeamAuthorized 是纯判定（行级授权唯一比较点）：资源归属 Team 与调用方
// Team 等值比对；owner 豁免。
func TeamAuthorized(ctx context.Context, resourceTeam string) error {
	teamID, owner, err := CallerTeam(ctx)
	if err != nil {
		return err
	}
	if owner {
		return nil
	}
	if teamID != resourceTeam {
		return apperr.New("E_FORBIDDEN",
			"this resource belongs to team %s; the token belongs to team %s", resourceTeam, teamID).
			WithSuggestion("Use a token of the team that owns the resource (WhoAmI shows the token's team).")
	}
	return nil
}

// RequirePlatformAdmin 是平台管理员门的共享形态：平台面/身份面 handler 必须
// 在任何副作用与审计之前调用（403 先于 404/501/业务校验，拒绝零副作用）。
// 平台管理员 = default Team 的 builtin-owner——bootstrap 引导 Token 与 init
// 产出的首个用户即此档。
func RequirePlatformAdmin(ctx context.Context) error {
	_, platformAdmin, err := CallerTeam(ctx)
	if err != nil {
		return err
	}
	if !platformAdmin {
		return apperr.New("E_FORBIDDEN",
			"this operation manages platform-scoped material; a platform-admin token (default team owner) is required").
			WithSuggestion("Sign in as the platform owner (the builtin-owner of the default team, minted by 'fleetly init') and retry.")
	}
	return nil
}

// RequireScope 是复合 handler 的内层动作门（R08-M3）：复合编排在进程内直调
// 其他服务方法（或触发部署链），authn 拦截器只在 RPC 边界按外层注解执法
// 一次——内层动词的能力面必须在这里显式复核（HasScope 与内层 RPC 的
// api_key_scope 同源；平台 owner 的 `*` 天然通过，无身份特判）。
func RequireScope(ctx context.Context, resource string, op string) error {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return apperr.New("E_UNAUTHENTICATED", "present a valid token to use the %s capability", resource)
	}
	if id.HasScope(resource, op) {
		return nil
	}
	need := resource + ":" + op
	return apperr.New("E_FORBIDDEN",
		"token %q lacks required scope %s for this composite operation", id.TokenName, need).
		WithContext("token", id.TokenName).
		WithContext("required_scope", need).
		WithSuggestion("Grant the token's role the inner scopes this operation performs (" + need + "), or use a token whose role carries them.")
}
