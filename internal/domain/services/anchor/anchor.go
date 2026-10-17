// Package anchor 是「聚合行 → 归属 Project → Team」的解析图单源（架构
// 评审第二轮候选 3，2026-10-03）：Change Freeze 的治理 Team 轴
// （ADR-0017 附录 A.3）与数据面行级授权（ADR-0035）此前各持一份行为
// 拷贝（freeze.go resolveTeam switch × authz.go authorize 族），每个新
// 资源两处各长一枝。本包拥有图的事实——X 锚到 Project 的链怎么走；
// 两消费面各自的策略留在消费面：冻结把 NotFound 放行交受理位拒绝、
// 授权把 NotFound 报 E_NOT_FOUND（404 不掩蔽为 403）；App 的 tombstone
// 读档分双档（生命周期动词走活跃档、审计型读面走 Any 档）。
// repo 族经 Repos 注入（P1b 批三 A-2 全量聚合上移后构造收进组合根，
// 本包零持久化依赖）。
package anchor

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/githubapp"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// Kind 是归属解析链的判别（freeze 冻结表与行级授权共用的值域）。
type Kind int

const (
	KindTeam       Kind = iota // ref 即 team_id（CreateProject：零跳直通）
	KindProject                // project_id → Project 行
	KindApp                    // app_id → App（活跃档）→ Project
	KindAnyApp                 // app_id → App（tombstone 可见档）→ Project
	KindTask                   // task id → Task → Project
	KindSchedule               // schedule id → Schedule → Project
	KindNetwork                // network_id → Network → Project
	KindPeer                   // peer id → Peer → Network → Project（二跳）
	KindRoute                  // route id → Route → Project
	KindDatabase               // database id → Database → Project（ADR-0029）
	KindVolume                 // volume id → Volume → Project（DeleteVolume 冻结锚）
	KindRun                    // run id → Run → Project
	KindDeployment             // deployment id → Deployment → App（Any 档）→ Project
	KindBuild                  // build id → Build → App（Any 档）→ Project
	KindHookToken              // hook token 明文 → hash → Hook → App（活跃档）→ Project
	KindGitHubApp              // github app id → 行 → Project（Global 行 project_id 空 → NotFound，冻结放行）
)

// NotFoundError 是解析链任一跳的行缺失：Resource 是该跳的资源名（API 面
// E_NOT_FOUND 文案的单词真源——链上哪跳缺就报哪跳，与分散各 handler 的
// mapStateError 逐字一致）；errors.Is(err, state.ErrNotFound) 成立，冻结
// 面据此放行、授权面据此报 404。
type NotFoundError struct {
	Resource string
	ID       string
	cause    error
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %s not found", e.Resource, e.ID)
}

func (e *NotFoundError) Unwrap() error { return e.cause }

// notFound 铸某跳的行缺失（cause 恒 state.ErrNotFound 等值哨兵）。
func notFound(resource, id string) error {
	return &NotFoundError{Resource: resource, ID: id, cause: state.ErrNotFound}
}

// Repos 是解析图的 repo 依赖集（组合根构造注入——图消费聚合读面，不
// 自持持久化）。
type Repos struct {
	Projects    project.Repo
	Apps        app.Repo
	Tasks       task.Repo
	Schedules   schedule.Repo
	Networks    network.Repo
	Peers       networkpeer.Repo
	Hooks       hook.Repo
	Routes      route.Repo
	Databases   database.Repo
	Volumes     volume.Repo
	Runs        run.Repo
	Deployments deployment.Repo
	Builds      build.Repo
	GitHubApps  githubapp.Repo
}

// Anchor 是解析器（无状态，并发安全；repo 族经 Repos 注入）。
type Anchor struct {
	projects    project.Repo
	apps        app.Repo
	tasks       task.Repo
	schedules   schedule.Repo
	networks    network.Repo
	peers       networkpeer.Repo
	hooks       hook.Repo
	routes      route.Repo
	databases   database.Repo
	volumes     volume.Repo
	runs        run.Repo
	deployments deployment.Repo
	builds      build.Repo
	ghApps      githubapp.Repo
}

// New 构造解析器（repo 族由组合根构造注入）。
func New(repos Repos) *Anchor {
	return &Anchor{
		projects:    repos.Projects,
		apps:        repos.Apps,
		tasks:       repos.Tasks,
		schedules:   repos.Schedules,
		networks:    repos.Networks,
		peers:       repos.Peers,
		hooks:       repos.Hooks,
		routes:      repos.Routes,
		databases:   repos.Databases,
		volumes:     repos.Volumes,
		runs:        repos.Runs,
		deployments: repos.Deployments,
		builds:      repos.Builds,
		ghApps:      repos.GitHubApps,
	}
}

// resolver 是一跳到 projectID 的链（KindTeam 除外——直通 team）。
type resolver func(a *Anchor, ctx context.Context, ref string) (string, error)

// resolvers 是 Kind → 解析链的方法表（图的事实单源；新资源 = 加一行）。
var resolvers = map[Kind]resolver{
	KindProject: func(a *Anchor, ctx context.Context, ref string) (string, error) {
		return ref, nil // project_id 直达终点腿
	},
	KindApp:        (*Anchor).appProject,
	KindAnyApp:     (*Anchor).anyAppProject,
	KindTask:       (*Anchor).taskProject,
	KindSchedule:   (*Anchor).scheduleProject,
	KindNetwork:    (*Anchor).networkProject,
	KindPeer:       (*Anchor).peerProject,
	KindRoute:      (*Anchor).routeProject,
	KindDatabase:   (*Anchor).databaseProject,
	KindVolume:     (*Anchor).volumeProject,
	KindRun:        (*Anchor).runProject,
	KindDeployment: (*Anchor).deploymentProject,
	KindBuild:      (*Anchor).buildProject,
	KindHookToken:  (*Anchor).hookTokenProject,
	KindGitHubApp:  (*Anchor).gitHubAppProject,
}

// TeamOf 解析 ref 的归属 Team（全链：Kind 链 → projectID → Project 行的
// TeamID）。链上任何一跳行缺失返回 *NotFoundError（errors.Is
// state.ErrNotFound 成立）；存储故障原样上抛（消费面各自 fail-closed）。
// 空 TeamID（Team 轴外的行，ADR-0028 后不可达防御）原样返回，消费面
// 自行处置。
func (a *Anchor) TeamOf(ctx context.Context, kind Kind, ref string) (string, error) {
	if kind == KindTeam {
		return ref, nil
	}
	resolve, ok := resolvers[kind]
	if !ok {
		return "", fmt.Errorf("anchor: no resolution chain registered for kind %d", kind)
	}
	projectID, err := resolve(a, ctx, ref)
	if err != nil {
		return "", err
	}
	return a.TeamOfProjectID(ctx, projectID)
}

// TeamOfProjectID 是终点腿：projectID → Project 行 → TeamID（已持
// projectID/挂靠行的消费面共用；projects.Get 对 tombstone 可见——
// "tombstone 是事实不是秘密"，ADR-0023/0035）。
func (a *Anchor) TeamOfProjectID(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", notFound("project", projectID)
	}
	p, err := a.projects.Get(ctx, projectID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("project", projectID)
		}
		return "", err
	}
	return p.TeamID, nil
}

// ---- 各 Kind 的解析链（链上每跳 NotFound 带该跳资源名） ----

func (a *Anchor) appProject(ctx context.Context, ref string) (string, error) {
	row, err := a.apps.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("app", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) anyAppProject(ctx context.Context, ref string) (string, error) {
	row, err := a.apps.GetAnyByID(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("app", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) taskProject(ctx context.Context, ref string) (string, error) {
	row, err := a.tasks.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("task", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) scheduleProject(ctx context.Context, ref string) (string, error) {
	row, err := a.schedules.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("schedule", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) networkProject(ctx context.Context, ref string) (string, error) {
	row, err := a.networks.GetByID(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("network", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) peerProject(ctx context.Context, ref string) (string, error) {
	p, err := a.peers.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("network peer", ref)
		}
		return "", err
	}
	return a.networkProject(ctx, p.NetworkID)
}

func (a *Anchor) routeProject(ctx context.Context, ref string) (string, error) {
	row, err := a.routes.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("route", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) databaseProject(ctx context.Context, ref string) (string, error) {
	row, err := a.databases.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("database", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) volumeProject(ctx context.Context, ref string) (string, error) {
	row, err := a.volumes.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("volume", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) runProject(ctx context.Context, ref string) (string, error) {
	row, err := a.runs.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("run", ref)
		}
		return "", err
	}
	return row.ProjectID, nil
}

func (a *Anchor) deploymentProject(ctx context.Context, ref string) (string, error) {
	d, err := a.deployments.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("deployment", ref)
		}
		return "", err
	}
	// 审计型读面口径（ADR-0035）：App 锚走 Any 档——App 删除后的部署/
	// 构建历史仍须可授权（归属是已删行上的事实）。
	return a.anyAppProject(ctx, d.AppID)
}

func (a *Anchor) buildProject(ctx context.Context, ref string) (string, error) {
	b, err := a.builds.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("build", ref)
		}
		return "", err
	}
	return a.anyAppProject(ctx, b.AppID)
}

func (a *Anchor) gitHubAppProject(ctx context.Context, ref string) (string, error) {
	row, err := a.ghApps.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("github app", ref)
		}
		return "", err
	}
	return a.TeamOfProjectID(ctx, row.ProjectID)
}

func (a *Anchor) hookTokenProject(ctx context.Context, ref string) (string, error) {
	if identity.TokenKind(ref) != "hook" {
		return "", notFound("hook", ref) // 非法凭证：接收面自会 401（freeze 放行口径不变）
	}
	h, err := a.hooks.GetByTokenSHA256(ctx, identity.HashToken(ref))
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", notFound("hook", ref)
		}
		return "", err
	}
	return a.appProject(ctx, h.AppID)
}
