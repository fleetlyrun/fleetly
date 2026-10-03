package fleetlygrpc

// 数据面 per-Team 行级授权（ADR-0035）：资源归属 Team（经 Project 行解析）
// 必须等于调用方 Team；builtin-owner（平台管理员，scope `*`）豁免。本文件
// 是判定的唯一真源——受理位/读写分面的约定见 ADR，全部 Get/Delete/List/
// 动词/写面受理的调用点在 handlers；越权形态恒 E_FORBIDDEN（存在但非本队），
// 行不存在保持既有 E_NOT_FOUND（404 不掩蔽为 403）。
//
// 与 scope 门禁（authn 拦截器）正交：scope 答"能不能用这个面"，本判定答
// "能不能碰这行"。`*` scope 不是行级豁免——自定义角色能力面全通行级仍
// 限本队（ADR-0035 决策 2）。

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/anchor"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
)

// callerTeam 解析调用方 Team 轴：owner=true 表示平台管理员（default Team
// 的 builtin-owner——bootstrap 引导 Token 即此档；其他 Team 的 owner 是该队
// 管理员，不跨队——networkpeer 安全批测试钉死"他队 owner 不得豁免"）。
// 数据面全部 SERVER 档，匿名到不了这里；防御性返回 E_UNAUTHENTICATED
// （fail-closed）。
func callerTeam(ctx context.Context) (teamID string, owner bool, err error) {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return "", false, apperr.New("E_UNAUTHENTICATED", "present a valid token to access team resources")
	}
	platformAdmin := id.RoleID == identity.RoleOwnerID && id.TeamID == identity.DefaultTeamID
	return id.TeamID, platformAdmin, nil
}

// teamAuthorized 是纯判定（行级授权唯一比较点）：资源归属 Team 与调用方
// Team 等值比对；owner 豁免。
func teamAuthorized(ctx context.Context, resourceTeam string) error {
	teamID, owner, err := callerTeam(ctx)
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

// authorizeTeamForProject 校验 Project 行的归属 Team。tombstone 行保持可
// 授权（ADR-0035）：GetProject 按设计可读已删行（"tombstone 是事实不是
// 秘密"），归属判定对已删行同样成立；活跃性拒绝由各写面事务内的
// requireActiveProject 承载（不在此分叉读语义）。
func (s *Services) authorizeTeamForProject(ctx context.Context, p *project.Project) error {
	return teamAuthorized(ctx, p.TeamID)
}

// mapAnchorError 把 anchor 解析错误映射为 API 信封：链上哪跳行缺失就报
// 哪跳的 404（anchor.NotFoundError.Resource 是文案单词真源，与分散各
// handler 的 mapStateError 逐字一致）；其余错误走 mapStateError 缺省
// （存储故障 E_INTERNAL）。
func mapAnchorError(err error) error {
	var nf *anchor.NotFoundError
	if errors.As(err, &nf) {
		return mapStateError(err, nf.Resource)
	}
	return mapStateError(err, "resource")
}

// authorizeProjectID 校验 project_id 的归属（行不存在 → E_NOT_FOUND）。
// 全部"请求携带 project_id"的受理位共用（List 面授权、写面前置）。
func (s *Services) authorizeProjectID(ctx context.Context, projectID string) error {
	team, err := s.Anchor.TeamOf(ctx, s.DB.Runner(), anchor.KindProject, projectID)
	if err != nil {
		return mapAnchorError(err)
	}
	return teamAuthorized(ctx, team)
}

// authorizeAppRow 校验 App 行归属（经 Project 终点腿；行已载的调用点共用，
// 避免双读）。
func (s *Services) authorizeAppRow(ctx context.Context, a *app.App) error {
	team, err := s.Anchor.TeamOfProjectID(ctx, s.DB.Runner(), a.ProjectID)
	if err != nil {
		return mapAnchorError(err)
	}
	return teamAuthorized(ctx, team)
}

// authorizeAppID 载 App 行并校验归属（返回行供调用点复用——Deploy/Rollback
// 等本就要 App 行；生命周期动词走活跃档 Get）。
func (s *Services) authorizeAppID(ctx context.Context, appID string) (*app.App, error) {
	a, err := s.Apps.Get(ctx, s.DB.Runner(), appID)
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	if err := s.authorizeAppRow(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// ---- 按全局 ID 直取面的授权族（ADR-0035 决策 4：行不存在保持 404，
// 存在但非本队 → E_FORBIDDEN；归属链经 anchor 解析图单源——链上每跳
// 缺失由 anchor.NotFoundError 携带资源名，本族不再各自载行） ----

// authorizeTaskID 校验 Task 归属（生命周期动词 Scale/Stop/Delete/Renew
// 与 GetTask 共用——engine 动词在 handler 前置授权，engine 内部机制
// 不看 Team）。
func (s *Services) authorizeTaskID(ctx context.Context, taskID string) error {
	return s.authorizeAnchored(ctx, anchor.KindTask, taskID)
}

// authorizeRunID 校验 Run 归属（Get/Stop/Wait 共用）。
func (s *Services) authorizeRunID(ctx context.Context, runID string) error {
	return s.authorizeAnchored(ctx, anchor.KindRun, runID)
}

// authorizeScheduleID 校验 Schedule 归属（Get/Delete/Trigger 共用）。
func (s *Services) authorizeScheduleID(ctx context.Context, scheduleID string) error {
	return s.authorizeAnchored(ctx, anchor.KindSchedule, scheduleID)
}

// authorizeDeploymentID 校验 Deployment 归属（Get/Cancel/Wait 共用；App
// 锚走 Any 档——部署历史在 App 删除后仍可授权）。
func (s *Services) authorizeDeploymentID(ctx context.Context, deploymentID string) error {
	return s.authorizeAnchored(ctx, anchor.KindDeployment, deploymentID)
}

// authorizeBuildID 校验 Build 归属（StreamLogs/Wait 共用；App 锚走 Any 档）。
func (s *Services) authorizeBuildID(ctx context.Context, buildID string) error {
	return s.authorizeAnchored(ctx, anchor.KindBuild, buildID)
}

// authorizeRouteID 校验 Route 归属（Delete 共用）。
func (s *Services) authorizeRouteID(ctx context.Context, routeID string) error {
	return s.authorizeAnchored(ctx, anchor.KindRoute, routeID)
}

// authorizeAppIDOnly 校验 App 行归属（内部锚点复用：已持 App ID 不需要
// 行返回的场合）。行读含 tombstone（Any 档，ADR-0035）：ListDeployments/
// ListRevisions/ListBuilds 等审计型读面在 App 删除后仍须过授权（归属是
// 已删行上的事实）；活跃性拒绝由各动词自身的活跃行 Get/engine 受理承载。
func (s *Services) authorizeAppIDOnly(ctx context.Context, appID string) error {
	return s.authorizeAnchored(ctx, anchor.KindAnyApp, appID)
}

// authorizeAnchored 是按全局 ID 直取面的共用底座：anchor 全链解析归属
// Team → 行级比对（错误分层见 mapAnchorError）。
func (s *Services) authorizeAnchored(ctx context.Context, kind anchor.Kind, id string) error {
	team, err := s.Anchor.TeamOf(ctx, s.DB.Runner(), kind, id)
	if err != nil {
		return mapAnchorError(err)
	}
	return teamAuthorized(ctx, team)
}

// authorizeTokenRow 校验 Token 行归属（identity 面 Get/Revoke；Token 行
// 自带 TeamID，免二跳）。
func (s *Services) authorizeTokenRow(ctx context.Context, t *tokenrepo.Token) error {
	teamID, owner, err := callerTeam(ctx)
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

// teamProjectFilter 返回 List 面的过滤集：owner → (nil, true)=不过滤；
// 其余调用方 → 本 Team 全部活跃 Project ID 的集合（行级过滤在内存比对，
// Project 规模小微，无 N+1 查询——一次 ListByTeam）。
func (s *Services) teamProjectFilter(ctx context.Context) (map[string]bool, bool, error) {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return nil, false, err
	}
	if owner {
		return nil, true, nil
	}
	projects, err := s.Projects.ListByTeam(ctx, s.DB.Runner(), teamID)
	if err != nil {
		return nil, false, mapStateError(err, "project")
	}
	ids := make(map[string]bool, len(projects))
	for i := range projects {
		ids[projects[i].ID] = true
	}
	return ids, false, nil
}

// errTeamTargetMismatch 是"创建型请求显式指定他队目标"的拒绝形态（identity
// 面：CreateToken/CreateUser/CreateRole/CreateInvitation/CreateProject 与
// freeze 的 team_id 目标校验）。与 teamAuthorized 的行级形态分开——目标是
// 调用方声明的字段而非已存在的行。
func errTeamTargetMismatch(targetTeam, callerTeamID string) error {
	return apperr.New("E_FORBIDDEN",
		"target team %s does not match the token's team %s; tokens operate within their own team", targetTeam, callerTeamID).
		WithSuggestion("Omit team_id to target the token's own team, or use a platform owner token.")
}

// resolveTargetTeam 归一 identity 面创建请求的目标 Team：缺省 = 调用方 Team
// （ADR-0035 决策 5；此前缺省 default Team——对 default 调用方等价）；显式
// 目标 ≠ 调用方 Team 且非 owner → 拒绝。
func resolveTargetTeam(ctx context.Context, requested string) (string, error) {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return "", err
	}
	target := requested
	if target == "" {
		target = teamID
	}
	if target == "" {
		target = identity.DefaultTeamID // 匿名/引导面防御（数据面 SERVER 档不可达）
	}
	if !owner && target != teamID {
		return "", errTeamTargetMismatch(target, teamID)
	}
	return target, nil
}
