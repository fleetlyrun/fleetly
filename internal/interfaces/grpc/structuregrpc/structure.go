package structuregrpc

import (
	"context"
	"errors"
	"fmt"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	configrepo "github.com/fleetlyrun/fleetly/internal/domain/structure/config"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"log/slog"
	"time"
)

// Anchor 是归属解析图的窄消费接口。
type Anchor interface {
	TeamOfProjectID(ctx context.Context, projectID string) (string, error)
	TeamOf(ctx context.Context, kind anchor.Kind, id string) (string, error)
}

// System 是受管/路由唤醒的窄消费接口。
type System interface {
	KickManagedLoop()
	PublishRoutesNow()
}

// Deploy 是 App 拆除面的窄消费接口。
type Deploy interface {
	TeardownApp(ctx context.Context, appID string, confirm *api.ConfirmInput) error
}

// Nodes 是网络运行面的窄消费接口。
type Nodes interface {
	RebuildNetwork(ctx context.Context, projectID, name string) (api.NetworkRebuildResult, error)
	RequestPeerIsolation(networkID, peerProjectID string)
}

// Deps 是结构服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	Projects            project.Repo
	Apps                app.Repo
	Secrets             secret.Repo
	Configs             configrepo.Repo
	SharedVariables     sharedvariable.Repo
	AppVariables        appvariable.Repo
	Volumes             volume.Repo
	Networks            network.Repo
	NetworkPeers        networkpeer.Repo
	Revisions           revision.Repo
	Deployments         deployment.Repo
	Databases           database.Repo
	Runtime             capability.Runtime
	Anchor              Anchor
	System              System
	Nodes               Nodes
	Cipher              *material.Cipher
	Commit              func(ctx context.Context, fact grpcapi.WriteFact) error
	MintConfirm         func(payload governance.ConfirmTicketPayload) (string, time.Duration, error)
	PeekConfirm         func(ticket string) (governance.ConfirmTicketPayload, governance.ConfirmPeekStatus)
	DiscardConfirm      func(ticket string) bool
	Deploy              Deploy
	Log                 *slog.Logger
	CascadeDatabases    func(ctx context.Context, projectID string) error
	CascadePubSubs      func(ctx context.Context, projectID string) error
	CascadeBucketStores func(ctx context.Context, projectID string) error
}

// ProjectsService 满足 structurev1.ProjectsServiceServer。
type ProjectsService struct {
	structurev1.UnimplementedProjectsServiceServer
	deps Deps
}

// AppsService 满足 structurev1.AppsServiceServer。
type AppsService struct {
	structurev1.UnimplementedAppsServiceServer
	deps Deps
}

// SecretsService 满足 structurev1.SecretsServiceServer。
type SecretsService struct {
	structurev1.UnimplementedSecretsServiceServer
	deps Deps
}

// ConfigsService 满足 structurev1.ConfigsServiceServer。
type ConfigsService struct {
	structurev1.UnimplementedConfigsServiceServer
	deps Deps
}

// SharedVariablesService 满足 structurev1.SharedVariablesServiceServer。
type SharedVariablesService struct {
	structurev1.UnimplementedSharedVariablesServiceServer
	deps Deps
}

// AppVariablesService 满足 structurev1.AppVariablesServiceServer。
type AppVariablesService struct {
	structurev1.UnimplementedAppVariablesServiceServer
	deps Deps
}

// VolumesService 满足 structurev1.VolumesServiceServer。
type VolumesService struct {
	structurev1.UnimplementedVolumesServiceServer
	deps Deps
}

// NetworksService 满足 structurev1.NetworksServiceServer。
type NetworksService struct {
	structurev1.UnimplementedNetworksServiceServer
	deps Deps
}

// NewApps 构造 Apps 服务（模板复合编排内层直调用）。
func NewApps(deps Deps) *AppsService { return &AppsService{deps: deps} }

// NewSecrets 构造 Secrets 服务（模板复合编排内层直调用）。
func NewSecrets(deps Deps) *SecretsService { return &SecretsService{deps: deps} }

// NewNetworks 构造 Networks 服务（模板复合编排内层直调用）。
func NewNetworks(deps Deps) *NetworksService { return &NetworksService{deps: deps} }

// Register 注册结构面八个服务。
func Register(srv *grpc.Server, deps Deps) {
	structurev1.RegisterProjectsServiceServer(srv, &ProjectsService{deps: deps})
	structurev1.RegisterAppsServiceServer(srv, &AppsService{deps: deps})
	structurev1.RegisterSecretsServiceServer(srv, &SecretsService{deps: deps})
	structurev1.RegisterConfigsServiceServer(srv, &ConfigsService{deps: deps})
	structurev1.RegisterSharedVariablesServiceServer(srv, &SharedVariablesService{deps: deps})
	structurev1.RegisterAppVariablesServiceServer(srv, &AppVariablesService{deps: deps})
	structurev1.RegisterVolumesServiceServer(srv, &VolumesService{deps: deps})
	structurev1.RegisterNetworksServiceServer(srv, &NetworksService{deps: deps})
}

var (
	_ structurev1.ProjectsServiceServer        = (*ProjectsService)(nil)
	_ structurev1.AppsServiceServer            = (*AppsService)(nil)
	_ structurev1.SecretsServiceServer         = (*SecretsService)(nil)
	_ structurev1.ConfigsServiceServer         = (*ConfigsService)(nil)
	_ structurev1.SharedVariablesServiceServer = (*SharedVariablesService)(nil)
	_ structurev1.AppVariablesServiceServer    = (*AppVariablesService)(nil)
	_ structurev1.VolumesServiceServer         = (*VolumesService)(nil)
	_ structurev1.NetworksServiceServer        = (*NetworksService)(nil)
)

// ---- Projects ----

func (svc *ProjectsService) CreateProject(ctx context.Context, req *structurev1.CreateProjectRequest) (*structurev1.CreateProjectResponse, error) {
	// name 的 required/dns 标签形态检查已退役为 CreateProjectRequest 的
	// protovalidate CEL 注解（P4-1 批 1）：validate 拦截器在 handler 之前
	// 执法，文案与手写时代逐字节一致（单源见 entityid.NamePatternSource）。
	// Team 轴（ADR-0035 决策 5）：目标 Team 缺省 = 调用方 Team；显式他队
	// 目标仅平台 owner 可（行级落子面）。
	teamID, err := grpcapi.ResolveTargetTeam(ctx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	p := &project.Project{Name: req.GetName(), TeamID: teamID}
	// default 网络随项目出生（F-C，2026-10-03 staging 实证）：compose 引用
	// `networks: [default]` 而表行缺失时会静默半物化——swarm 侧 overlay 由
	// workload Ensure 建了，networks 表（受管 Proxy 挂靠真源）却无行，traefik
	// 永不挂靠该网 → 路由 502。出生即建行，引用面与挂靠面同源；overlay
	// 本身仍随首个 workload 物化（表行不建网，无空跑）。
	net := &network.Network{ProjectID: p.ID, Name: "default"}
	// Team 轴接实（ADR-0028）：归属 Team 必须存在——Project 落在不存在
	// 的 Team 上会让域解析（engine projectTeam）悬空。
	// 资源轴 Team 在手直接铸（P0-2）：project.created 的聚合即项目本体、
	// default 网络与项目同队——projectID 锚要等 write 段才铸出，不能走
	// 解析锚路径（factOf）。
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{teamExists(p.TeamID)},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.Projects.Create(ctx, p); err != nil {
				return err
			}
			// default 网络归属事务内铸出的项目 id（ADR-0070）。
			net.ProjectID = p.ID
			return st.Networks.Create(ctx, net)
		},
		Facts: []func() writes.FactRow{
			func() writes.FactRow {
				return writes.FactRow{
					Name: eventProjectCreated, Aggregate: "project", ID: p.ID,
					Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: p.ID}),
					TeamID:  teamID,
					Action:  "project.create", Resource: "project/" + p.ID, AfterFP: p.Name,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			},
			func() writes.FactRow {
				// 双真偏离（仅流面）：default 网络随项目出生，取证由首行的
				// project.create 审计单行承载（A 化前亦无独立审计行——行集
				// 合零漂移）。
				return writes.FactRow{
					Name: eventNetworkCreated, Aggregate: "network", ID: net.Name,
					Payload:    grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: net.Name, ProjectID: p.ID}),
					TeamID:     teamID,
					StreamOnly: true,
				}
			},
		},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "project")
	}
	// per-Project registry 材料即时滚动（ADR-0036 N2 兑现节 2）：新 Project
	// 的 zot 用户不等下一受管节拍——首构建可与节拍竞速（staging 实录推送
	// 401 一次失败）。
	svc.deps.System.KickManagedLoop()
	return &structurev1.CreateProjectResponse{Project: projectMsg(p)}, nil
}

func (svc *ProjectsService) GetProject(ctx context.Context, req *structurev1.GetProjectRequest) (*structurev1.GetProjectResponse, error) {
	p, err := svc.deps.Projects.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "project")
	}
	if err := svc.deps.authorizeTeamForProject(ctx, p); err != nil {
		return nil, err
	}
	return &structurev1.GetProjectResponse{Project: projectMsg(p)}, nil
}

// ResolveProject 是 name 寻址的服务端单源（ADR-0092）：调用方 team 作用
// 域内按名解析（授权内嵌——跨队同名行不可见即 404，不泄露存在性）。
func (svc *ProjectsService) ResolveProject(ctx context.Context, req *structurev1.ResolveProjectRequest) (*structurev1.ResolveProjectResponse, error) {
	p, err := grpcapi.ResolveProjectRef(ctx, svc.deps.Projects, req.GetName())
	if err != nil {
		return nil, err
	}
	return &structurev1.ResolveProjectResponse{Project: projectMsg(p)}, nil
}

// ListProjects 非 owner 按 Team 过滤（ADR-0035 List 面：过滤而非逐行拒绝）；
// owner（平台管理员）全量。两种形态同带分页（ADR-0026 after_* + limit）。
func (svc *ProjectsService) ListProjects(ctx context.Context, req *structurev1.ListProjectsRequest) (*structurev1.ListProjectsResponse, error) {
	teamID, owner, err := grpcapi.CallerTeam(ctx)
	if err != nil {
		return nil, err
	}
	limit := grpcapi.ListLimit(req.GetLimit())
	var list []project.Project
	if owner {
		list, err = svc.deps.Projects.ListPage(ctx, req.GetAfterProjectSeq(), limit)
	} else {
		list, err = svc.deps.Projects.ListByTeamPage(ctx, teamID, req.GetAfterProjectSeq(), limit)
	}
	if err != nil {
		return nil, grpcapi.MapStateError(err, "project")
	}
	out := &structurev1.ListProjectsResponse{}
	for i := range list {
		out.Projects = append(out.Projects, projectMsg(&list[i]))
	}
	return out, nil
}

// DeleteProject 软删 Project（tombstone）。活跃 App 守卫（Q-15）：项目下
// 有未 tombstone 的 App 即拒删（E_CONFLICT，提示先删 App——DeleteApp 自带
// 活跃部署收口与路由撤除，ADR-0023 ③）。边界：Project 级材料
// （Secret/Config/Volume/Network）不随 Project 删除——各自生命周期独立
// （操作者按需先删材料再删 Project，本守卫不级联、不代删）。
//
// 数据库级联（2026-10-05 评审批台账 #4，ADR-0029 追记）：项目下的活跃
// Database 随删除按单库删除同款收口序级联（TeardownDatabase 先行 →
// tombstone + database.deleted + 审计，逐库一事务），全部收口后项目才
// tombstone；卷与凭证 Secret 仍保留（备份保留义）。App 与 Database 的
// 处置分立是有意裁决：App 有部署状态机与路由撤除面，级联会静默拆走带
// 路由的活服务（用户可感知流量中断）；Database 是项目私有寻址的内部
// 依赖（db-<id> 只在项目网内可达），不删即孤儿——事故实录的痛点在库
// 不在 App（runbook 记录·五 #3）。
//
// 删除后窗口：本守卫只保证"删除瞬间无活跃 App 与活跃 Database"；删除
// 落账后 CreateApp/CreateDatabase 面不得再向已删项目挂子资源——由各自
// 事务内的 requireActiveProject 对偶守卫承载（两守卫同处各自事务、单写
// 者串行，任一交错下"项目存活 ⇔ 子资源可建"；批 0 复核：此前 CreateApp
// 无校验、apps.project_id 无 FK，该窗口实际敞开，注释宣称的"窗口闭合"
// 不成立，已随对偶守卫落地闭合）。
func (svc *ProjectsService) DeleteProject(ctx context.Context, req *structurev1.DeleteProjectRequest) (*structurev1.DeleteProjectResponse, error) {
	// 行级授权（ADR-0035）：载行比对归属 Team（不存在 → 既有 404 形态）。
	if err := svc.deps.authorizeProjectID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	// 无锁预检（DeleteApp 同款形态，拒绝零副作用）：有活 App 在此拒绝，
	// 不进入库级联拆载体；事务内 noActiveApps 复查仍是终局守卫。
	if err := svc.deps.rejectActiveApps(ctx, svc.deps.Apps, req.GetId()); err != nil {
		return nil, grpcapi.MapStateError(err, "project")
	}
	// 数据库级联（ADR-0029 追记 2026-10-05）：逐库同款收口序；任一库
	// 失败即整体诚实失败，已收口库保持 tombstone（重试收敛）。
	if err := svc.deps.CascadeDatabases(ctx, req.GetId()); err != nil {
		return nil, err
	}
	// pubsub 级联（B4b，ADR-0096：database 级联同款序——项目删除逐 pubsub
	// 收口）。
	if err := svc.deps.CascadePubSubs(ctx, req.GetId()); err != nil {
		return nil, err
	}
	// bucket store 级联（B5，ADR-0096：pubsub 级联同款序）。
	if err := svc.deps.CascadeBucketStores(ctx, req.GetId()); err != nil {
		return nil, err
	}
	// 聚合即项目本体：自身充当 Team 解析锚（P0-2，factOf）。事件归属进
	// 事务前解析（R08-M8：解析失败整单拒绝）。
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, req.GetId(), func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventProjectDeleted, Aggregate: "project", ID: req.GetId(),
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: req.GetId()}),
			TeamID:  team,
			Action:  "project.delete", Resource: "project/" + req.GetId(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		// 删除守卫与 tombstone 同事务：并发建 App 的窗口由两侧守卫对偶
		// 闭合（本侧拒绝"删除时仍有 App"；CreateApp 侧受理检查拒绝"删除
		// 后挂 App"；单写者事务串行，见函数注释）。并发建库窗口由
		// noActiveDatabases 复查闭合——级联的事务内终局，命中即拒（重试
		// 把新库纳入级联）。
		Checks: []grpcapi.Check{noActiveApps(req.GetId()), noActiveDatabases(req.GetId()), noActivePubSubs(req.GetId()), noActiveBucketStores(req.GetId())},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Projects.SoftDelete(ctx, req.GetId())
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "project")
	}
	// 同 CreateProject：htpasswd 摘行（真撤销面）随活跃集即时再生。
	svc.deps.System.KickManagedLoop()
	return &structurev1.DeleteProjectResponse{}, nil
}

// ---- Apps ----

func (svc *AppsService) CreateApp(ctx context.Context, req *structurev1.CreateAppRequest) (*structurev1.CreateAppResponse, error) {
	// name 的 dns 标签形态检查（validName）与 project_id/name 必填组合检查
	// 已退役为 CreateAppRequest 的 protovalidate CEL 注解（P4-1 批 1 Run
	// B1）：validate 拦截器在 handler 之前执法，组合必填拆 per-field 文案；
	// 正则字面单源见 entityid.NamePatternSource（descriptor 对齐测试执法）。
	// 行级授权（ADR-0035）：创建在他队 Project 下 = 越权落子，受理前置拒
	//（Project 的 Team 归属不可变，无 TOCTOU 面）。
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	a := &app.App{ProjectID: req.GetProjectId(), Name: req.GetName()}
	// 父资源存活校验（批 0 复核）：apps.project_id 无 FK，不校验则对
	// 不存在/已删 project 建 App 直接成功，活 App 落已删项目、路由照发
	// ——DeleteProject 守卫的"窗口闭合"承诺以此对偶守卫成立。数量配额
	//（ADR-0017 附录 A.1）与存活校验同住受理位（事务内读）。事件归属
	// 进事务前解析（R08-M8：解析失败整单拒绝）。
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, a.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventAppCreated, Aggregate: "app", ID: a.ID,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: a.ID, ProjectID: a.ProjectID}),
			TeamID:  team,
			Action:  "app.create", Resource: "app/" + a.ID, AfterFP: a.Name,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{
			parentProjectAlive(req.GetProjectId()),
			appQuota(req.GetProjectId()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Apps.Create(ctx, a)
		},
		// 行 id 事务内铸出（ADR-0070）：闭包求值在聚合写后，p.ID/a.ID
		// 就位自然可见。
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	return &structurev1.CreateAppResponse{App: appMsg(a)}, nil
}

func (svc *AppsService) GetApp(ctx context.Context, req *structurev1.GetAppRequest) (*structurev1.GetAppResponse, error) {
	a, err := svc.deps.Apps.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	if err := svc.deps.authorizeAppRow(ctx, a); err != nil {
		return nil, err
	}
	return &structurev1.GetAppResponse{App: appMsg(a)}, nil
}

// ResolveApp 按 (project id|name, app name) 解析（ADR-0092 name 边界解析
// 单源；授权随 project 解析内嵌）。
func (svc *AppsService) ResolveApp(ctx context.Context, req *structurev1.ResolveAppRequest) (*structurev1.ResolveAppResponse, error) {
	p, err := grpcapi.ResolveProjectRef(ctx, svc.deps.Projects, req.GetProject())
	if err != nil {
		return nil, err
	}
	a, err := svc.deps.Apps.GetByName(ctx, p.ID, req.GetName())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	return &structurev1.ResolveAppResponse{App: appMsg(a)}, nil
}

// GetAppSpec 回读 App 当前冻结 Spec（最新 Revision；只读面——写路径仅
// Deploy）。Variables 编辑面 / Used-by 反查 / Volume 挂载反查的数据源
// （IA v3 二期②；spec.proto 唯一运行时边界的只读回读）。未部署过 =
// E_NOT_FOUND（诚实：无冻结即无 Spec）。
func (svc *AppsService) GetAppSpec(ctx context.Context, req *structurev1.GetAppSpecRequest) (*structurev1.GetAppSpecResponse, error) {
	a, err := svc.deps.Apps.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	if err := svc.deps.authorizeAppRow(ctx, a); err != nil {
		return nil, err
	}
	rev, err := svc.deps.Revisions.Latest(ctx, a.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "spec")
	}
	var spec specv1.AppSpec
	if err := protojson.Unmarshal(rev.Spec, &spec); err != nil {
		return nil, apperr.New("E_INTERNAL", "app spec: frozen revision is not valid protojson (%v)", err)
	}
	// Revision 锚（ADR-0071 决策 1）：最新冻结的诚实锚——消费方据此与
	// "当前生效"（GetAppWorkloads 缺省寻址）分野。
	return &structurev1.GetAppSpecResponse{
		Spec: &spec, RevisionSeq: rev.Seq, RevisionDigest: rev.Digest,
	}, nil
}

func (svc *AppsService) ListApps(ctx context.Context, req *structurev1.ListAppsRequest) (*structurev1.ListAppsResponse, error) {
	// project_id 必填检查已退役为 ListAppsRequest 的 protovalidate CEL 注解
	//（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	// 行级授权（ADR-0035 List 面）：project 锚先授权再查询。
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.deps.Apps.ListByProjectPage(ctx,
		req.GetProjectId(), req.GetAfterAppSeq(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	out := &structurev1.ListAppsResponse{}
	for i := range list {
		out.Apps = append(out.Apps, appMsg(&list[i]))
	}
	return out, nil
}

// DeleteApp 收口删除（ADR-0023）：驱动权在位拒绝（E_CONFLICT，先 cancel/
// 等终态；含 failed 待自动回滚窗，R02-M4）→ engine 收口拆载体 → tombstone +
// 撤路由 + 审计一事务落账。副作用不可与审计同事务：先收口后落账，收口失败
// 即整体失败（App 保持可操作）。
//
// 拒删不变式"App 存活 ⇒ 路由不得消失"由三道复查承载（ADR-0023 修订）：
//  1. 无锁预检：常规拒绝发生在零副作用阶段（驱动权谓词）；
//  2. TeardownApp 锁内预检：与 Submit 共享 appMu，拆载体前所见即受理
//     终局——预检与收口之间受理的部署在副作用前拒绝；
//  3. tombstone 事务内复查：与 App tombstone、撤路由、审计同生共死——
//     收口后落账前受理的驱动权行使整单回滚（无 tombstone、无路由删除），
//     已拆载体由在途部署的 Ensure/回滚重放自愈，teardown-aborted 事件 +
//     审计留痕（不静默）。
func (svc *AppsService) DeleteApp(ctx context.Context, req *structurev1.DeleteAppRequest) (*structurev1.DeleteAppResponse, error) {
	// id 必填检查已退役为 DeleteAppRequest 的 protovalidate CEL 注解
	//（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	appRow, err := svc.deps.Apps.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	if err := svc.deps.authorizeAppRow(ctx, appRow); err != nil {
		return nil, err
	}
	// 驱动权预检（R02-M4）：与 converger 同口径（DrivingByApp = 活跃态 +
	// failed 待自动回滚窗）。ActiveByApp 不含 failed 待回滚行——用它放行
	// 会让驱动器在收口后回滚重放并重建载体（无主孤儿）。
	driving, err := svc.deps.Deployments.DrivingByApp(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "deployment")
	}
	if len(driving) > 0 {
		return nil, apperr.New("E_CONFLICT",
			"app %s has %d active deployment(s); cancel them or wait for a terminal state before deleting", req.GetId(), len(driving))
	}
	// B1 LedgerOnly 审计行的票面锚在此 peek（必须在 TeardownApp 烧票之前
	// ——烧后 peek 只见 tombstone）；peek 不删，权威验票/烧票在 engine 锁内。
	confirmFact := svc.teardownConfirmFact(ctx, req)
	if err := svc.deps.Deploy.TeardownApp(ctx, req.GetId(), deleteConfirmInput(req)); err != nil {
		// 并发双删：对手已落 tombstone（Get 活跃行口径）→ 同形 404；
		// 锁内预检命中活跃部署 → E_CONFLICT（零副作用，App 保持可操作）；
		// B1 确认哨兵 → 三码（票检在 TeardownApp 的 appMu 内、拆载体之前
		// ——零副作用拒绝）；其余收口失败保持 E_INTERNAL（App 未落账，
		// 可重试删除）。
		switch {
		case errors.Is(err, state.ErrNotFound):
			return nil, apperr.New("E_NOT_FOUND", "app %s not found", req.GetId())
		case errors.Is(err, api.ErrActiveDeployment):
			return nil, apperr.New("E_CONFLICT",
				"app %s has active deployment(s); cancel them or wait for a terminal state before deleting", req.GetId()).WithCause(err)
		case errors.Is(err, api.ErrConfirmStale):
			return nil, apperr.New("E_CONFIRMATION_STALE",
				"the confirmation for this teardown no longer matches the current state").
				WithCause(err).
				WithSuggestion("The teardown baseline moved (hook configuration changed) or the ticket expired. Run the teardown preview again and confirm the fresh ticket.")
		case errors.Is(err, api.ErrConfirmMismatch):
			return nil, apperr.New("E_CONFIRMATION_MISMATCH",
				"the confirmation ticket does not match this teardown").
				WithCause(err).
				WithSuggestion("The ticket binds the previewed app and its operator. Mint a fresh preview ticket for this app, or pass --yes.")
		}
		return nil, apperr.New("E_INTERNAL", "app teardown failed").WithCause(err)
	}

	// tombstone 事务：复查、App tombstone、撤路由、事件、审计同生共死。
	// aborted 非零 ⇔ 复查命中活跃部署（整单回滚的哨兵，见下方残余面）。
	var aborted int
	// 事件归属进事务前解析（R08-M8：解析失败整单拒绝）。
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, appRow.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventAppDeleted, Aggregate: "app", ID: req.GetId(),
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: req.GetId(), ProjectID: appRow.ProjectID}),
			TeamID:  team,
			Action:  "app.delete", Resource: "app/" + req.GetId(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	// B1 LedgerOnly 审计行：拆除确认的取证形态（proof=redeemed/skipped）
	// 与 app.delete 同事务落库——「确认过」「跳过」与拦截器的「无凭证
	// 拒绝」三形态取证面可辨（设计稿 §1.2 审计 A8）。
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		// 驱动权复查（R02-M4）：收口后、落账前受理的部署（含 failed 待
		// 自动回滚窗）在此拒绝——整单回滚（无 tombstone、无路由删除；与
		// Submit 的存活判定互为对偶，单连接事务串行下窗口闭合）。
		Checks: []grpcapi.Check{func(ctx context.Context, st transact.Stores) error {
			driving, err := st.Deployments.DrivingByApp(ctx, req.GetId())
			if err != nil {
				return err
			}
			if len(driving) > 0 {
				aborted = len(driving)
				return errDeleteAborted
			}
			return nil
		}},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.Apps.SoftDelete(ctx, req.GetId()); err != nil {
				return err
			}
			// git hook 行与 tombstone 同逝（级联）：配置面对已删 App 即刻
			// 404，与接收面的活跃行拒绝同语义（无孤儿子女）。
			if err := st.Hooks.DeleteByApp(ctx, req.GetId()); err != nil {
				return err
			}
			// 撤路由与 tombstone 同事务（ADR-0023 修订）：复查通过前路由
			// 不得消失——拒绝路径不碰路由（manageds.Step 的周期发布因此
			// 只可能见到"App 与路由同逝"的一致状态）。
			routes, err := st.Routes.List(ctx)
			if err != nil {
				return err
			}
			for _, rt := range routes {
				if rt.AppID != req.GetId() {
					continue
				}
				if err := st.Routes.SoftDelete(ctx, rt.ID); err != nil {
					return err
				}
			}
			return nil
		},
		Facts: teardownConfirmFacts(confirmFact, fact),
	})
	if err != nil {
		if aborted > 0 {
			// 残余面（ADR-0023 修订）：载体已拆而活跃部署在——不静默：
			// teardown-aborted 事件 + 审计留痕后按 E_CONFLICT 拒绝；在途
			// 部署的 Ensure/回滚重放自愈重建载体，路由未被触碰。留痕失败
			// 即整体失败（不做静默的 E_CONFLICT）。
			if rerr := svc.recordTeardownAbort(ctx, req.GetId(), appRow.ProjectID, aborted); rerr != nil {
				return nil, rerr
			}
			return nil, apperr.New("E_CONFLICT",
				"app %s has %d active deployment(s); cancel them or wait for a terminal state before deleting", req.GetId(), aborted)
		}
		return nil, grpcapi.MapStateError(err, "app")
	}
	svc.deps.System.PublishRoutesNow() // Proxy 全量发布即时触发（撤流收口）
	return &structurev1.DeleteAppResponse{}, nil
}

// deleteConfirmInput 把 DeleteAppRequest 的 confirmation 铸为 engine 入参
// （nil = 未携带——confirm 拦截器已拦 E_CONFIRMATION_REQUIRED）。
func deleteConfirmInput(req *structurev1.DeleteAppRequest) *api.ConfirmInput {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	return &api.ConfirmInput{Ticket: conf.GetTicket(), Skip: conf.GetSkip()}
}

// teardownConfirmFact 铸拆除确认的 LedgerOnly 审计行（票面锚经 peek 取，
// 不删；调用时点必须在 TeardownApp 烧票之前——烧后 peek 只见 tombstone。
// 票不可 peek 时〔伪造腿会被 engine 拒，此处防御〕只落 proof 形态）。
func (svc *AppsService) teardownConfirmFact(ctx context.Context, req *structurev1.DeleteAppRequest) *writes.FactRow {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	fr := writes.FactRow{
		Aggregate: "app", ID: req.GetId(),
		Action: "confirm.redeem", Resource: "app/" + req.GetId(),
		Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		// LedgerOnly：取证面单行（app.delete 的流面事实由 fact 承载）。
		LedgerOnly: true,
	}
	if conf.GetSkip() {
		fr.Action = "confirm.skip"
		fr.AfterFP = "proof=skipped"
		return &fr
	}
	if payload, status := svc.deps.PeekConfirm(conf.GetTicket()); status == governance.ConfirmPeekHit {
		b := payload.Base
		fr.AfterFP = fmt.Sprintf("proof=redeemed; verb=%s; preview_digest=%s; request_digest=%s; hook_row=%s",
			payload.Verb, payload.PreviewDigest, payload.RequestDigest, b.HookRowVersion)
	}
	return &fr
}

// teardownConfirmFacts 组装 tombstone 事务的事实集（confirm 行在场时居前，
// app.delete 殿后——确认事实与删除事实同事务）。
func teardownConfirmFacts(confirmFact *writes.FactRow, fact func() writes.FactRow) []func() writes.FactRow {
	if confirmFact == nil {
		return []func() writes.FactRow{fact}
	}
	cf := *confirmFact
	return []func() writes.FactRow{func() writes.FactRow { return cf }, fact}
}

// errDeleteAborted 是 tombstone 事务复查命中活跃部署的回滚哨兵（事务内
// 错误只触发回滚；对外形态由 aborted 分支的 E_CONFLICT 承载）。
var errDeleteAborted = errors.New("app delete: teardown aborted by a deployment admitted mid-delete")

// recordTeardownAbort 落 teardown-aborted 事件 + 审计（同事务）。资源轴
// 改走正常归属解析（R08-M8 第二个实例：调用点已持有 projectID，隶属可
// 解析——不再留 ” 平台级可见，把受害 Team 的 project_id 广播给全体 Team）。
func (svc *AppsService) recordTeardownAbort(ctx context.Context, id, projectID string, active int) error {
	fact, ferr := grpcapi.FactOf(ctx, svc.deps.Projects, projectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventAppTeardownAborted, Aggregate: "app", ID: id,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: id, ProjectID: projectID, ActiveDeployments: active}),
			TeamID:  team,
			Action:  "app.teardown_abort", Resource: "app/" + id,
			AfterFP: fmt.Sprintf("active_deployments=%d", active),
			Source:  audit.SourceSystem,
		}
	})
	if ferr != nil {
		return ferr
	}
	err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return apperr.New("E_INTERNAL", "app teardown abort could not be recorded").WithCause(err)
	}
	return nil
}

// ---- Secrets（值永不回显；写路径审计全落） ----

func (svc *SecretsService) PutSecret(ctx context.Context, req *structurev1.PutSecretRequest) (*structurev1.PutSecretResponse, error) {
	// project_id/name 必填组合与 name 字符集白名单检查（spec.ValidSecretName，
	// 含 ".." 拒）已退役为 PutSecretRequest 的 protovalidate CEL 注解
	//（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法，组合必填拆
	// per-field 文案；正则字面单源见 spec.SecretNamePattern（spec 包
	// descriptor 对齐测试执法）。保留前缀与字符集的拒绝次序随之外移到边界。
	if svc.deps.Cipher == nil {
		// 未配主密钥是显式关态（P4-3 批 3）：cipher 面缺席精确 503，自
		// E_SECRET_UNAVAILABLE 双语义拆出，文案逐字节保留。
		return nil, apperr.New("E_MASTER_KEY_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}
	// 行级授权（ADR-0035）：写面受理前置（Project Team 归属不可变，无 TOCTOU）。
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 数据库凭证保留前缀（ADR-0029 决策 6）：值必须恒为完整连接 URL
	//（单真源），用户覆写会破坏凭证三面。
	if api.IsDatabaseCredentialSecret(req.GetName()) {
		return nil, apperr.New("E_CONFLICT",
			"secret name %q is reserved for platform-managed database credentials; database secrets are minted by 'fleetly databases create'",
			req.GetName())
	}
	// pubsub 凭证保留前缀（ADR-0096 决策 4）：值恒为连接材料方言串。
	if api.IsPubSubCredentialSecret(req.GetName()) {
		return nil, apperr.New("E_CONFLICT",
			"secret name %q is reserved for platform-managed pubsub credentials; pubsub secrets are minted by 'fleetly pubsubs create'",
			req.GetName())
	}
	// bucket store 凭证保留前缀（B5，ADR-0096 决策 4）：值恒为 k=v 连接材料
	// 方言串。
	if api.IsBucketStoreCredentialSecret(req.GetName()) {
		return nil, apperr.New("E_CONFLICT",
			"secret name %q is reserved for platform-managed bucket store credentials; bucket store secrets are minted by 'fleetly buckets create'",
			req.GetName())
	}
	sealed, err := svc.deps.Cipher.Produce([]byte(req.GetValue()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "secret")
	}
	row := &secret.Secret{
		ProjectID: req.GetProjectId(), Name: req.GetName(),
		Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
	}
	// 父资源存活校验（批 0 复核，同族面）：Project 级材料不得落在
	// 不存在/已删的 Project 下。事件归属进事务前解析（R08-M8）。
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventSecretUpdated, Aggregate: "secret", ID: row.Name,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "secret.put", Resource: "secret/" + row.Name, AfterFP: row.Fingerprint,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{parentProjectAlive(req.GetProjectId())},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Secrets.Upsert(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "secret")
	}
	return &structurev1.PutSecretResponse{Secret: secretMsg(*row)}, nil
}

func (svc *SecretsService) ListSecrets(ctx context.Context, req *structurev1.ListSecretsRequest) (*structurev1.ListSecretsResponse, error) {
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.deps.Secrets.ListFingerprints(ctx,
		req.GetProjectId(), req.GetAfterName(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "secret")
	}
	out := &structurev1.ListSecretsResponse{}
	for _, row := range list {
		out.Secrets = append(out.Secrets, secretMsg(row))
	}
	return out, nil
}

func (svc *SecretsService) DeleteSecret(ctx context.Context, req *structurev1.DeleteSecretRequest) (*structurev1.DeleteSecretResponse, error) {
	// 行级授权（ADR-0035）：此前 (project_id,name) 直删零校验——知道
	// project id 即可删别队 secret。
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, req.GetProjectId(), func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventSecretDeleted, Aggregate: "secret", ID: req.GetName(),
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: req.GetName(), ProjectID: req.GetProjectId()}),
			TeamID:  team,
			Action:  "secret.delete", Resource: "secret/" + req.GetName(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Secrets.SoftDelete(ctx, req.GetProjectId(), req.GetName())
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "secret")
	}
	return &structurev1.DeleteSecretResponse{}, nil
}

func (svc *ConfigsService) PutConfig(ctx context.Context, req *structurev1.PutConfigRequest) (*structurev1.PutConfigResponse, error) {
	// project_id/name 必填组合与 content 单值上限检查（256KiB）已退役为
	// PutConfigRequest 的 protovalidate CEL 注解（P4-1 批 1 Run B1）：
	// validate 拦截器在 handler 之前执法，组合必填拆 per-field 文案。
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 数量配额按"Project 内配置名数"计（新名才占新位；同名 put 是新版本）
	// ——受理位检查（事务内读，ADR-0024：与写同事务，无先读后写 TOCTOU）。
	row := &configrepo.Config{ProjectID: req.GetProjectId(), Name: req.GetName(), Content: []byte(req.GetContent())}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventConfigUpdated, Aggregate: "config", ID: row.Name,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "config.put", Resource: "config/" + row.Name, AfterFP: material.Fingerprint(row.Content),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{
			parentProjectAlive(req.GetProjectId()),
			configQuota(req.GetProjectId(), req.GetName()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Configs.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "config")
	}
	return &structurev1.PutConfigResponse{Config: configMsg(*row, false)}, nil
}

func (svc *ConfigsService) GetConfig(ctx context.Context, req *structurev1.GetConfigRequest) (*structurev1.GetConfigResponse, error) {
	// 行级授权（ADR-0035）：Config 回读 env 内容——跨租户读面的大头。
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	var (
		row *configrepo.Config
		err error
	)
	if req.GetVersion() > 0 {
		row, err = svc.deps.Configs.GetVersion(ctx, req.GetProjectId(), req.GetName(), req.GetVersion())
	} else {
		row, err = svc.deps.Configs.Latest(ctx, req.GetProjectId(), req.GetName())
	}
	if err != nil {
		return nil, grpcapi.MapStateError(err, "config")
	}
	return &structurev1.GetConfigResponse{Config: configMsg(*row, true)}, nil
}

func (svc *ConfigsService) ListConfigs(ctx context.Context, req *structurev1.ListConfigsRequest) (*structurev1.ListConfigsResponse, error) {
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 列表面只回 Project 内最新版（版本明细随版本面扩展）；分页只动行集
	//（每行仍是该 name 最新版，ADR-0026 after_* + limit）。
	list, err := svc.deps.Configs.LatestByProjectPage(ctx,
		req.GetProjectId(), req.GetAfterName(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "config")
	}
	out := &structurev1.ListConfigsResponse{}
	for _, row := range list {
		out.Configs = append(out.Configs, configMsg(row, false))
	}
	return out, nil
}

// projectMsg / appMsg / secretMsg / configMsg / sharedVariableMsg /
// appVariableMsg / volumeMsg / networkMsg 是结构行 → proto 投影。
func projectMsg(p *project.Project) *structurev1.Project {
	return &structurev1.Project{Id: p.ID, Seq: p.Seq, Name: p.Name, TeamId: p.TeamID, CreatedAt: p.CreatedAt, DeletedAt: p.DeletedAt}
}

func appMsg(a *app.App) *structurev1.App {
	return &structurev1.App{Id: a.ID, Seq: a.Seq, ProjectId: a.ProjectID, Name: a.Name, CreatedAt: a.CreatedAt}
}

func secretMsg(s secret.Secret) *structurev1.Secret {
	return &structurev1.Secret{Id: s.ID, ProjectId: s.ProjectID, Name: s.Name, Fingerprint: s.Fingerprint, UpdatedAt: s.UpdatedAt}
}

func configMsg(c configrepo.Config, withContent bool) *structurev1.Config {
	out := &structurev1.Config{Id: c.ID, ProjectId: c.ProjectID, Name: c.Name, Version: c.Version, CreatedAt: c.CreatedAt}
	if withContent {
		out.Content = string(c.Content)
	}
	return out
}

func sharedVariableMsg(v sharedvariable.SharedVariable) *structurev1.SharedVariable {
	return &structurev1.SharedVariable{Id: v.ID, ProjectId: v.ProjectID, Name: v.Name, Value: v.Value, UpdatedAt: v.UpdatedAt}
}

func appVariableMsg(v appvariable.AppVariable) *structurev1.AppVariable {
	return &structurev1.AppVariable{Id: v.ID, AppId: v.AppID, Name: v.Name, Value: v.Value, UpdatedAt: v.UpdatedAt}
}

func volumeMsg(v volume.Volume) *structurev1.Volume {
	return &structurev1.Volume{Id: v.ID, ProjectId: v.ProjectID, Name: v.Name, PinnedNodeId: v.PinnedNodeID, CreatedAt: v.CreatedAt}
}

func networkMsg(n network.Network) *structurev1.Network {
	return &structurev1.Network{Id: n.ID, ProjectId: n.ProjectID, Name: n.Name, EgressNone: n.EgressNone, CreatedAt: n.CreatedAt}
}

// 事件名与载荷导出面（schemareg 注册与跨包消费）。
const (
	EventProjectCreated      = eventProjectCreated
	EventProjectDeleted      = eventProjectDeleted
	EventAppCreated          = eventAppCreated
	EventAppDeleted          = eventAppDeleted
	EventAppTeardownAborted  = eventAppTeardownAborted
	EventSecretUpdated       = eventSecretUpdated
	EventSecretDeleted       = eventSecretDeleted
	EventConfigUpdated       = eventConfigUpdated
	EventVariableUpdated     = eventVariableUpdated
	EventVariableDeleted     = eventVariableDeleted
	EventAppVarUpdated       = eventAppVarUpdated
	EventAppVarDeleted       = eventAppVarDeleted
	EventVolumeCreated       = eventVolumeCreated
	EventVolumeDeleted       = eventVolumeDeleted
	EventVolumePurged        = eventVolumePurged
	EventNetworkCreated      = eventNetworkCreated
	EventNetworkPeerDeclared = eventNetworkPeerDeclared
	EventNetworkPeerApproved = eventNetworkPeerApproved
	EventNetworkPeerRevoked  = eventNetworkPeerRevoked
	EventNetworkRebuilt      = eventNetworkRebuilt
)

// NetworkPeerEventPayload 是跨 Project peer 事件的载荷。
type NetworkPeerEventPayload = networkPeerEventPayload

// NetworkRebuiltEventPayload 是 network.rebuilt 的载荷。
type NetworkRebuiltEventPayload = networkRebuiltEventPayload
