package api

// 资源归属解析与改派编排端口（v0.3 W2-S3，rbac-teams §3.4/§4.2 D-W0-9 解
// 析规则 + D-W0-4 二修）：
//
//   - project 引用 = 裸名或限定形 `team/prj`：限定形恒可解析；裸名仅在解
//     析域（用户 = 可见项目集；机具令牌/平台管理员 = 全库）内唯一时可用，
//     多命中 → E_PROJECT_AMBIGUOUS（错误 context 列候选 `team/prj`）；
//   - 用户 principal 缺省 project = 个人队默认项目 `default`；机具令牌
//     （UserID 空）缺省无——必须显式携带，否则 400 带指引；
//   - MoveApp / MoveDatabase 改派 = 写归属 + 换名重部署（编排经端口委托
//     engine/database/ingress——api 定义端口、不感知实现类型）。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// DefaultProjectSlug 是注册默认项目 slug（state.DefaultProjectSlug 同源——
// 本常量保留为 api 面字面引用点；两者由 projects_test 钉同值）。
const DefaultProjectSlug = "default"

// resolveProjectRef 解析 project 引用为项目行。ref 空 = 缺省解析（见上）。
// 不存在 → 404 退化信封；多命中 → E_PROJECT_AMBIGUOUS。
func resolveProjectRef(ctx context.Context, st *state.Store, ref string) (state.Project, error) {
	p, ok := PrincipalFromContext(ctx)
	userSide := ok && p.UserID != ""
	ref = strings.TrimSpace(ref)

	// 缺省解析：用户 = 个人队 default 项目；机具令牌无缺省（必须显式）。
	if ref == "" {
		if !userSide {
			return state.Project{}, statusInvalidArgument(
				"machine tokens have no default project: pass project \"team/project\" (or \"project\" when unambiguous) on deploy/database-create requests")
		}
		return defaultProjectForUser(ctx, st, p.UserID)
	}

	// 限定形 team/prj：恒可解析（D-W0-9）。
	if teamSlug, prjSlug, ok := strings.Cut(ref, "/"); ok {
		return resolveQualifiedProject(ctx, st, teamSlug, prjSlug)
	}

	// 裸名：解析域内唯一才可用。解析域 = 用户可见项目集（成员团队归属）/
	// 全库（机具令牌与平台管理员）。ID 形态优先直查（D-W0-9：管理面/脚本
	// 用 ID，免疫同名歧义）——查无再按 slug 解析。
	if proj, err := st.GetProject(ctx, ref); err == nil {
		return proj, nil
	}
	var projects []state.Project
	var err error
	if userSide && !isPlatformAdminUser(ctx, st) {
		projects, err = visibleProjectsForUser(ctx, st, p.UserID)
	} else {
		projects, err = st.ListProjects(ctx)
	}
	if err != nil {
		return state.Project{}, err
	}
	var hits []state.Project
	for _, proj := range projects {
		if proj.Slug == ref {
			hits = append(hits, proj)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return state.Project{}, notFound(fmt.Sprintf("project %q not found in your visible projects (pass \"team/project\" to resolve across teams)", ref))
	default:
		candidates := make([]string, 0, len(hits))
		for _, proj := range hits {
			slug, serr := qualifiedProjectSlug(ctx, st, proj)
			if serr != nil {
				return state.Project{}, serr
			}
			candidates = append(candidates, slug)
		}
		sort.Strings(candidates)
		return state.Project{}, apperr.New("E_PROJECT_AMBIGUOUS",
			"project %q matches %d visible projects; qualify the reference as team/project",
			ref, len(hits)).
			WithContext("candidates", strings.Join(candidates, ","))
	}
}

// defaultProjectForUser 解析用户的个人队默认项目（state.ResolveUserDefault-
// Project 单点；注册时落位的 owner 队下 slug=default 项目）。无个人队/缺省
// 项目 = 请求侧显式携带 project 的可行动指引。
func defaultProjectForUser(ctx context.Context, st *state.Store, userID string) (state.Project, error) {
	proj, err := st.ResolveUserDefaultProject(ctx, userID)
	if err != nil {
		if errors.Is(err, state.ErrDefaultProjectUnresolved) {
			return state.Project{}, statusInvalidArgument(
				"no default project resolvable for your account: pass project \"team/project\" explicitly")
		}
		return state.Project{}, err
	}
	return proj, nil
}

// visibleProjectsForUser 返回用户可见项目集（成员团队的全部项目——可见性
// 零级联，D-W0-2）。
func visibleProjectsForUser(ctx context.Context, st *state.Store, userID string) ([]state.Project, error) {
	memberships, err := st.ListUserMemberships(ctx, userID)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, m := range memberships {
		allowed[m.TeamID] = true
	}
	all, err := st.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]state.Project, 0, len(all))
	for _, proj := range all {
		if allowed[proj.TeamID] {
			out = append(out, proj)
		}
	}
	return out, nil
}

// resolveQualifiedProject 解析限定形 team/prj（两段 slug 精确匹配）。
func resolveQualifiedProject(ctx context.Context, st *state.Store, teamSlug, prjSlug string) (state.Project, error) {
	team, err := st.GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		if errors.Is(err, state.ErrTeamNotFound) {
			return state.Project{}, notFound("team not found: " + teamSlug)
		}
		return state.Project{}, err
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		return state.Project{}, err
	}
	for _, proj := range projects {
		if proj.TeamID == team.ID && proj.Slug == prjSlug {
			return proj, nil
		}
	}
	return state.Project{}, notFound(fmt.Sprintf("project %q not found in team %q", prjSlug, teamSlug))
}

// qualifiedProjectSlug 组装项目的 `team/prj` 展示串（候选列表/审计投影）。
func qualifiedProjectSlug(ctx context.Context, st *state.Store, p state.Project) (string, error) {
	t, err := st.GetTeam(ctx, p.TeamID)
	if err != nil {
		return p.Slug, nil // 团队行缺失退化裸名（不阻塞候选列示）
	}
	return t.Slug + "/" + p.Slug, nil
}

// ── MoveApp / MoveDatabase 编排端口（实现 = runtime 装配）──────────────────

// MoveAwaitTimeout 是改派换名交换的等待预算（api 层缺省；覆盖部署管线
// planning+releasing 到任务 running 的常规路径）。
const MoveAwaitTimeout = 120 * time.Second

// AppMovePort 是 MoveApp 换名重部署的引擎编排端口（实现 = engine.Engine，
// internal/engine/move.go；接口在 api 定义——方向纪律同 GitDeployTriggers）。
type AppMovePort interface {
	// EnqueueMoveRedeploy 归属切换后沿正常发布管线入队重部署；无成功部署
	// 史返回 engine.ErrNoRedeploySource（app 从未发布 = 无对象随迁）。
	EnqueueMoveRedeploy(ctx context.Context, appID string) (string, error)
	// AwaitAppSwap 等待新命名上下文的长驻服务就位（预算内未就位返回错误
	// ——调用方据此放弃旧服务清扫，流量仍在旧名上）。
	AwaitAppSwap(ctx context.Context, appID string, timeout time.Duration) error
	// SweepMovedServices 摘除旧命名上下文的长驻服务（在途 cron job 豁免）。
	SweepMovedServices(ctx context.Context, oldQualified string) (int, error)
}

// DBMovePort 是 MoveDatabase 换名重部署的收敛编排端口（实现 =
// database.Manager）。
type DBMovePort interface {
	MoveDatabaseRedeploy(ctx context.Context, inst state.DatabaseInstance, oldTeamSlug, oldPrjSlug string) error
}

// IngressMovePort 是 MoveApp 摘旧网的收尾端口（实现 = ingress.Manager；
// best-effort 语义——错误由调用方降级日志，不阻塞改派应答）。
type IngressMovePort interface {
	DetachAppNetwork(ctx context.Context, team, prj, app string) error
}
