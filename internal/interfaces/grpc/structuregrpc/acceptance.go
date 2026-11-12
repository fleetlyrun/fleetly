package structuregrpc

import (
	"context"
	"errors"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// authorizeProjectID 校验 Project 行归属 Team（沿 Anchor 链）。
func (d Deps) authorizeProjectID(ctx context.Context, projectID string) error {
	team, err := d.Anchor.TeamOf(ctx, anchor.KindProject, projectID)
	if err != nil {
		return grpcapi.MapAnchorError(err)
	}
	return grpcapi.TeamAuthorized(ctx, team)
}

// authorizeAppRow 校验 App 行归属 Team（沿 Project 链）。
func (d Deps) authorizeAppRow(ctx context.Context, a *app.App) error {
	team, err := d.Anchor.TeamOfProjectID(ctx, a.ProjectID)
	if err != nil {
		return grpcapi.MapAnchorError(err)
	}
	return grpcapi.TeamAuthorized(ctx, team)
}

// authorizeAppIDOnly 校验 App 行归属（不返回行）。
func (d Deps) authorizeAppIDOnly(ctx context.Context, appID string) error {
	team, err := d.Anchor.TeamOf(ctx, anchor.KindAnyApp, appID)
	if err != nil {
		return grpcapi.MapAnchorError(err)
	}
	return grpcapi.TeamAuthorized(ctx, team)
}

// authorizeTeamForProject 校验 Project 行归属 Team。
func (d Deps) authorizeTeamForProject(ctx context.Context, p *project.Project) error {
	return grpcapi.TeamAuthorized(ctx, p.TeamID)
}

// rejectActiveApps 是活跃 App 拒绝判定的核。
func (d Deps) rejectActiveApps(ctx context.Context, apps app.Repo, projectID string) error {
	return grpcapi.RejectActiveApps(ctx, apps, projectID)
}

// parentProjectAlive 是 Project 存活受理检查。
func parentProjectAlive(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		return grpcapi.RequireActiveProject(ctx, st, projectID)
	}
}

// teamExists 校验 Team 行存在。
func teamExists(id string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Teams.Get(ctx, id)
		return err
	}
}

// configQuota 是 per-Project 配置名数配额。
func configQuota(projectID, name string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		existing, err := st.Configs.Latest(ctx, projectID, name)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		if existing != nil {
			return nil
		}
		latest, err := st.Configs.LatestByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if len(latest) >= maxConfigsPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d configs (limit %d)", projectID, len(latest), maxConfigsPerProject)
		}
		return nil
	}
}

// sharedVariableQuota 是 per-Project 共享变量数配额。
func sharedVariableQuota(projectID, name string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		existing, err := st.SharedVariables.GetByName(ctx, projectID, name)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		if existing != nil {
			return nil
		}
		rows, err := st.SharedVariables.ListActive(ctx, projectID)
		if err != nil {
			return err
		}
		if len(rows) >= maxSharedVariablesPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d shared variables (limit %d)", projectID, len(rows), maxSharedVariablesPerProject)
		}
		return nil
	}
}

// appVariableQuota 是 per-App 变量数配额。
func appVariableQuota(appID, name string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		existing, err := st.AppVariables.GetByName(ctx, appID, name)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		if existing != nil {
			return nil
		}
		rows, err := st.AppVariables.ListActive(ctx, appID)
		if err != nil {
			return err
		}
		if len(rows) >= maxAppVariablesPerApp {
			return apperr.New("E_QUOTA_EXCEEDED",
				"app %s already holds %d variables (limit %d)", appID, len(rows), maxAppVariablesPerApp)
		}
		return nil
	}
}

// appQuota 是 per-Project App 数量配额。
func appQuota(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.Apps.CountByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n >= maxAppsPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d apps (limit %d)", projectID, n, maxAppsPerProject)
		}
		return nil
	}
}

// noActiveApps 是 DeleteProject 的删除守卫。
func noActiveApps(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		return grpcapi.RejectActiveApps(ctx, st.Apps, projectID)
	}
}

// noActiveDatabases 是 DeleteProject 的级联复查守卫。
func noActiveDatabases(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.Databases.CountByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n > 0 {
			return apperr.New("E_CONFLICT",
				"project %s still holds %d database(s) created while the delete was cascading; retry the project delete to cascade them", projectID, n)
		}
		return nil
	}
}

// noActivePubSubs 是 DeleteProject 的 pubsub 级联复查守卫。
func noActivePubSubs(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.PubSubs.CountByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n > 0 {
			return apperr.New("E_CONFLICT",
				"project %s still holds %d pubsub(s) created while the delete was cascading; retry the project delete to cascade them", projectID, n)
		}
		return nil
	}
}

// noActiveBucketStores 是 DeleteProject 的 bucket store 级联复查守卫。
func noActiveBucketStores(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.BucketStores.CountByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n > 0 {
			return apperr.New("E_CONFLICT",
				"project %s still holds %d bucket store(s) created while the delete was cascading; retry the project delete to cascade them", projectID, n)
		}
		return nil
	}
}

// 结构面事件名（usage 反扫的字面量锚点；C5 补齐——结构写操作此前只有
// 审计无事件）。
const (
	eventProjectCreated = "project.created"
	eventProjectDeleted = "project.deleted"
	eventAppCreated     = "app.created"
	eventAppDeleted     = "app.deleted"
	// teardown-aborted（ADR-0023 修订）：收口拆载体后、落账前受理的活跃
	// 部署使删除被拒——载体已拆这一残余面不留静默（在途部署重放自愈）。
	eventAppTeardownAborted = "app.teardown_aborted"
	eventSecretUpdated      = "secret.updated"
	eventSecretDeleted      = "secret.deleted"
	eventConfigUpdated      = "config.updated"
	// SharedVariable 面（F2.9，ADR-0043）：与 secret.* 同款三链（事件 +
	// 审计 + golden）；aggregate=variable（事件名 <聚合>.<事实> 惯例）。
	eventVariableUpdated = "variable.updated"
	eventVariableDeleted = "variable.deleted"
	eventAppVarUpdated   = "app_variable.updated"
	eventAppVarDeleted   = "app_variable.deleted"
	eventVolumeCreated   = "volume.created"
	eventVolumeDeleted   = "volume.deleted"
	eventNetworkCreated  = "network.created"
	// 跨 Project peer 声明面（F1.8，ADR-0013 附录 A.1）：三拍事件，
	// aggregate=network（挂接收方网络 ID——事件流沿网络聚合面订阅）。
	eventNetworkPeerDeclared = "network.peer_declared"
	eventNetworkPeerApproved = "network.peer_approved"
	eventNetworkPeerRevoked  = "network.peer_revoked"
	// 网络重建（ADR-0046，N2 评审批 P1-4）：执行成功才落事件——失败/拒绝
	// 无事件（重试风暴自放大防护，冻结拒绝同口径）。
	eventNetworkRebuilt = "network.rebuilt"
)

// Config 配额（F0.17 执法面，B2 落地）：per-Project 配置数上限。N0 小团队
// 口径的保守缺省；配置面接入 config.proto 后可覆盖。（单值大小上限已退役
// 为 PutConfigRequest 的 protovalidate CEL 注解——P4-1 批 1 Run B1。）
const (
	maxConfigsPerProject = 100
	// SharedVariable 配额（F2.9，ADR-0043 决策 5）：Configs 先例同口径；
	// 值面按 env 值量级收一档（env 是进程环境面不是文件面）。（单值大小
	// 上限同退役为变量请求注解。）
	maxSharedVariablesPerProject = 100
	maxAppVariablesPerApp        = 100
	// maxAppsPerProject 是 per-Project 活跃 App 数上限（ADR-0017 附录 A.1，
	// F1.9）：Workload 的 App 来源面。Task 轴两枚配额住 engine（域拥有者，
	// ScaleTask/到期拍共用）。
	maxAppsPerProject = 50
)
