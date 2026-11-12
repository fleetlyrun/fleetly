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

	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// callerTeam 是 grpcapi.CallerTeam 的既名委托。
func callerTeam(ctx context.Context) (teamID string, owner bool, err error) {
	return grpcapi.CallerTeam(ctx)
}

// requireScope 是 grpcapi.RequireScope 的既名委托。
func (s *Services) requireScope(ctx context.Context, resource string, op string) error {
	return grpcapi.RequireScope(ctx, resource, op)
}

// teamAuthorized 是 grpcapi.TeamAuthorized 的既名委托。
func teamAuthorized(ctx context.Context, resourceTeam string) error {
	return grpcapi.TeamAuthorized(ctx, resourceTeam)
}

// RequirePlatformAdmin 是 grpcapi.RequirePlatformAdmin 的跨包既名委托。
func RequirePlatformAdmin(ctx context.Context) error { return grpcapi.RequirePlatformAdmin(ctx) }

// AuthorizeTeamForProject 是 authorizeTeamForProject 的导出形态（logsgrpc
// 等拆分包的消费面）。
func (s *Services) AuthorizeTeamForProject(ctx context.Context, p *project.Project) error {
	return s.authorizeTeamForProject(ctx, p)
}

// authorizeTeamForProject 校验 Project 行的归属 Team。tombstone 行保持可
// 授权（ADR-0035）：GetProject 按设计可读已删行（"tombstone 是事实不是
// 秘密"），归属判定对已删行同样成立；活跃性拒绝由各写面事务内的
// requireActiveProject 承载（不在此分叉读语义）。
func (s *Services) authorizeTeamForProject(ctx context.Context, p *project.Project) error {
	return teamAuthorized(ctx, p.TeamID)
}

// mapAnchorError 是 grpcapi.MapAnchorError 的既名委托。
func mapAnchorError(err error) error { return grpcapi.MapAnchorError(err) }

// AuthorizeAppID 是 authorizeAppID 的导出形态（diagnosisgrpc 等拆分包的
// 消费面；行级授权判定仍单源在本文件）。
func (s *Services) AuthorizeAppID(ctx context.Context, appID string) (*app.App, error) {
	return s.authorizeAppID(ctx, appID)
}

// AuthorizeProjectID 是 authorizeProjectID 的导出形态（拆分包消费面）。
func (s *Services) AuthorizeProjectID(ctx context.Context, projectID string) error {
	return s.authorizeProjectID(ctx, projectID)
}

// authorizeProjectID 校验 project_id 的归属（行不存在 → E_NOT_FOUND）。
// 全部"请求携带 project_id"的受理位共用（List 面授权、写面前置）。
func (s *Services) authorizeProjectID(ctx context.Context, projectID string) error {
	team, err := s.Anchor.TeamOf(ctx, anchor.KindProject, projectID)
	if err != nil {
		return mapAnchorError(err)
	}
	return teamAuthorized(ctx, team)
}

// authorizeAppRow 校验 App 行归属（经 Project 终点腿；行已载的调用点共用，
// 避免双读）。
func (s *Services) authorizeAppRow(ctx context.Context, a *app.App) error {
	team, err := s.Anchor.TeamOfProjectID(ctx, a.ProjectID)
	if err != nil {
		return mapAnchorError(err)
	}
	return teamAuthorized(ctx, team)
}

// authorizeAppID 载 App 行并校验归属（返回行供调用点复用——Deploy/Rollback
// 等本就要 App 行；生命周期动词走活跃档 Get）。
func (s *Services) authorizeAppID(ctx context.Context, appID string) (*app.App, error) {
	a, err := s.Apps.Get(ctx, appID)
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	if err := s.authorizeAppRow(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// resolveProjectRef 把 project 引用（id|name）解析为已授权行（ADR-0092：
// name 在边界解析，id 在内核——26 位 ULID 形直取；其余按 name 在调用方
// team 作用域解析，未命中回退 id 直取兜底快照恢复的旧形 id；平台管理员的
// name 解析同样锚定其身份 team——跨 team 同名不猜测，fail-closed）。
func (s *Services) resolveProjectRef(ctx context.Context, ref string) (*project.Project, error) {
	if entityid.LooksMinted(ref) {
		p, err := s.Projects.Get(ctx, ref)
		if err != nil {
			return nil, mapStateError(err, "project")
		}
		if err := s.authorizeTeamForProject(ctx, p); err != nil {
			return nil, err
		}
		return p, nil
	}
	teamID, _, err := callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.Projects.GetByNameInTeam(ctx, teamID, ref)
	if err == nil {
		if err := s.authorizeTeamForProject(ctx, p); err != nil {
			return nil, err
		}
		return p, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return nil, mapStateError(err, "project")
	}
	// 旧形 id（快照恢复共存面）：name 未命中再按 id 直取一次。
	p, err = s.Projects.Get(ctx, ref)
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	if err := s.authorizeTeamForProject(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ---- 按全局 ID 直取面的授权族（ADR-0035 决策 4：行不存在保持 404，
// 存在但非本队 → E_FORBIDDEN；归属链经 anchor 解析图单源——链上每跳
// 缺失由 anchor.NotFoundError 携带资源名，本族不再各自载行） ----

// AuthorizeTaskID 是 authorizeTaskID 的导出形态（automationgrpc 消费面）。
func (s *Services) AuthorizeTaskID(ctx context.Context, taskID string) error {
	return s.authorizeTaskID(ctx, taskID)
}

// AuthorizeRunID 是 authorizeRunID 的导出形态（automationgrpc 消费面）。
func (s *Services) AuthorizeRunID(ctx context.Context, runID string) error {
	return s.authorizeRunID(ctx, runID)
}

// AuthorizeScheduleID 是 authorizeScheduleID 的导出形态（automationgrpc 消费面）。
func (s *Services) AuthorizeScheduleID(ctx context.Context, scheduleID string) error {
	return s.authorizeScheduleID(ctx, scheduleID)
}

// AuthorizeDeploymentID 是 authorizeDeploymentID 的导出形态（delivery 拆分消费面）。
func (s *Services) AuthorizeDeploymentID(ctx context.Context, deploymentID string) error {
	return s.authorizeDeploymentID(ctx, deploymentID)
}

// AuthorizeBuildID 是 authorizeBuildID 的导出形态（delivery 拆分消费面）。
func (s *Services) AuthorizeBuildID(ctx context.Context, buildID string) error {
	return s.authorizeBuildID(ctx, buildID)
}

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

// AuthorizeAppIDOnly 是 authorizeAppIDOnly 的导出形态（deliverygrpc 消费面）。
func (s *Services) AuthorizeAppIDOnly(ctx context.Context, appID string) error {
	return s.authorizeAppIDOnly(ctx, appID)
}

// authorizeAnchored 是按全局 ID 直取面的共用底座：anchor 全链解析归属
// Team → 行级比对（错误分层见 mapAnchorError）。
func (s *Services) authorizeAnchored(ctx context.Context, kind anchor.Kind, id string) error {
	team, err := s.Anchor.TeamOf(ctx, kind, id)
	if err != nil {
		return mapAnchorError(err)
	}
	return teamAuthorized(ctx, team)
}

// authorizeTokenRow 校验 Token 行归属（identity 面 Get/Revoke；Token 行
// 自带 TeamID，免二跳）。

// AuthorizeRouteID 是 authorizeRouteID 的导出形态（routesgrpc 消费面）。
func (s *Services) AuthorizeRouteID(ctx context.Context, routeID string) error {
	return s.authorizeRouteID(ctx, routeID)
}
