package fleetlygrpc

// Structure 上下文服务实现（Projects/Apps/Secrets/Configs/Volumes/Networks）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

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
	eventVolumeCreated   = "volume.created"
	eventNetworkCreated  = "network.created"
	// 跨 Project peer 声明面（F1.8，ADR-0013 附录 A.1）：三拍事件，
	// aggregate=network（挂接收方网络 ID——事件流沿网络聚合面订阅）。
	eventNetworkPeerDeclared = "network.peer_declared"
	eventNetworkPeerApproved = "network.peer_approved"
	eventNetworkPeerRevoked  = "network.peer_revoked"
)

// ---- Projects ----

type ProjectsService struct {
	structurev1.UnimplementedProjectsServiceServer
	s *Services
}

// structureEventPayload 是结构面事件的最小负载（字段只增；事件事实经
// acceptance.go 的 structureEvent 构造）。
type structureEventPayload struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	// ActiveDeployments 是 teardown-aborted 事件的诊断字段（拒删时收口后
	// 复查所见的活跃部署数；其余结构面事件恒缺省）。
	ActiveDeployments int `json:"active_deployments,omitempty"`
}

// requireActiveProject 在调用方事务内校验 Project 存活（ADR-0023 活跃行
// 口径：tombstone 后一律不存在）。全部"创建时引用父 Project"的写面共用
// （CreateApp/Route/Volume/Network/PutSecret/PutConfig）。与写操作同事务：
// 单写者串行下与 DeleteProject 的活跃 App 守卫构成对偶——任一交错下
// "项目存活 ⇔ 子资源可建"（批 0 复核：apps.project_id 无 FK，删除后建
// 子资源此前直接成功）。
func (s *Services) requireActiveProject(ctx context.Context, run state.Runner, id string) error {
	p, err := s.Projects.Get(ctx, run, id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return apperr.New("E_NOT_FOUND", "project %s not found", id)
		}
		return err
	}
	if p.Deleted() {
		return apperr.New("E_NOT_FOUND", "project %s not found", id)
	}
	return nil
}

// requireProjectApp 在调用方事务内校验 App 在指定 Project 内存活（Route
// 的父引用面：apps 活跃行口径 + 归属一致——跨项目悬空引用按"本项目无此
// App"拒绝）。
func (s *Services) requireProjectApp(ctx context.Context, run state.Runner, projectID, appID string) error {
	a, err := s.Apps.Get(ctx, run, appID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return apperr.New("E_NOT_FOUND", "app %s not found in project %s", appID, projectID)
		}
		return err
	}
	if a.ProjectID != projectID {
		return apperr.New("E_NOT_FOUND", "app %s not found in project %s", appID, projectID)
	}
	return nil
}

func (svc *ProjectsService) CreateProject(ctx context.Context, req *structurev1.CreateProjectRequest) (*structurev1.CreateProjectResponse, error) {
	if req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
	// Team 轴（ADR-0035 决策 5）：目标 Team 缺省 = 调用方 Team；显式他队
	// 目标仅平台 owner 可（行级落子面）。
	teamID, err := resolveTargetTeam(ctx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	p := &project.Project{ID: newID(), Name: req.GetName(), TeamID: teamID}
	// default 网络随项目出生（F-C，2026-10-03 staging 实证）：compose 引用
	// `networks: [default]` 而表行缺失时会静默半物化——swarm 侧 overlay 由
	// workload Ensure 建了，networks 表（受管 Edge 挂靠真源）却无行，traefik
	// 永不挂靠该网 → 路由 502。出生即建行，引用面与挂靠面同源；overlay
	// 本身仍随首个 workload 物化（表行不建网，无空跑）。
	net := &networkrepo.Network{ID: newID(), ProjectID: p.ID, Name: "default"}
	// Team 轴接实（ADR-0028）：归属 Team 必须存在——Project 落在不存在
	// 的 Team 上会让域解析（engine projectTeam）悬空。
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.teamExists(p.TeamID)},
		write: func(ctx context.Context, tx *sql.Tx) error {
			if err := svc.s.Projects.Create(ctx, tx, p); err != nil {
				return err
			}
			return svc.s.Networks.Create(ctx, tx, net)
		},
		events: []eventFact{
			structureEvent(eventProjectCreated, "project", p.ID, ""),
			structureEvent(eventNetworkCreated, "network", net.Name, net.ProjectID),
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "project.create",
			Resource: "project/" + p.ID, AfterFP: p.Name,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	// per-Project registry 材料即时滚动（ADR-0036 N2 兑现节 2）：新 Project
	// 的 zot 用户不等下一受管节拍——首构建可与节拍竞速（staging 实录推送
	// 401 一次失败）。
	svc.s.Engine.KickManagedLoop()
	return &structurev1.CreateProjectResponse{Project: projectMsg(p)}, nil
}

func (svc *ProjectsService) GetProject(ctx context.Context, req *structurev1.GetProjectRequest) (*structurev1.GetProjectResponse, error) {
	p, err := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	if err := svc.s.authorizeTeamForProject(ctx, p); err != nil {
		return nil, err
	}
	return &structurev1.GetProjectResponse{Project: projectMsg(p)}, nil
}

// ListProjects 非 owner 按 Team 过滤（ADR-0035 List 面：过滤而非逐行拒绝）；
// owner（平台管理员）全量。两种形态同带分页（ADR-0026 after_* + limit）。
func (svc *ProjectsService) ListProjects(ctx context.Context, req *structurev1.ListProjectsRequest) (*structurev1.ListProjectsResponse, error) {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	limit := listLimit(req.GetLimit())
	var list []project.Project
	if owner {
		list, err = svc.s.Projects.ListPage(ctx, svc.s.DB.Runner(), req.GetAfterProjectId(), limit)
	} else {
		list, err = svc.s.Projects.ListByTeamPage(ctx, svc.s.DB.Runner(), teamID, req.GetAfterProjectId(), limit)
	}
	if err != nil {
		return nil, mapStateError(err, "project")
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
// 删除后窗口：本守卫只保证"删除瞬间无活跃 App"；删除落账后 CreateApp 面
// 不得再向已删项目挂 App——由 CreateApp 事务内的 requireActiveProject 对偶
// 守卫承载（两守卫同处各自事务、单写者串行，任一交错下"项目存活 ⇔ App
// 可建"；批 0 复核：此前 CreateApp 无校验、apps.project_id 无 FK，该窗口
// 实际敞开，注释宣称的"窗口闭合"不成立，已随对偶守卫落地闭合）。
func (svc *ProjectsService) DeleteProject(ctx context.Context, req *structurev1.DeleteProjectRequest) (*structurev1.DeleteProjectResponse, error) {
	// 行级授权（ADR-0035）：载行比对归属 Team（不存在 → 既有 404 形态）。
	if err := svc.s.authorizeProjectID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	err := svc.s.commit(ctx, writeFact{
		// 删除守卫与 tombstone 同事务：并发建 App 的窗口由两侧守卫对偶
		// 闭合（本侧拒绝"删除时仍有 App"；CreateApp 侧受理检查拒绝"删除
		// 后挂 App"；单写者事务串行，见函数注释）。
		checks: []acceptanceCheck{svc.s.noActiveApps(req.GetId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Projects.SoftDelete(ctx, tx, req.GetId())
		},
		events: []eventFact{structureEvent(eventProjectDeleted, "project", req.GetId(), "")},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "project.delete",
			Resource: "project/" + req.GetId(),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	// 同 CreateProject：htpasswd 摘行（真撤销面）随活跃集即时再生。
	svc.s.Engine.KickManagedLoop()
	return &structurev1.DeleteProjectResponse{}, nil
}

// ---- Apps ----

type AppsService struct {
	structurev1.UnimplementedAppsServiceServer
	s *Services
}

func (svc *AppsService) CreateApp(ctx context.Context, req *structurev1.CreateAppRequest) (*structurev1.CreateAppResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	// 行级授权（ADR-0035）：创建在他队 Project 下 = 越权落子，受理前置拒
	//（Project 的 Team 归属不可变，无 TOCTOU 面）。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	a := &app.App{ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName()}
	// 父资源存活校验（批 0 复核）：apps.project_id 无 FK，不校验则对
	// 不存在/已删 project 建 App 直接成功，活 App 落已删项目、路由照发
	// ——DeleteProject 守卫的"窗口闭合"承诺以此对偶守卫成立。数量配额
	// （ADR-0017 附录 A.1）与存活校验同住受理位（事务内读）。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.appQuota(req.GetProjectId()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Apps.Create(ctx, tx, a)
		},
		events: []eventFact{structureEvent(eventAppCreated, "app", a.ID, a.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "app.create",
			Resource: "app/" + a.ID, AfterFP: a.Name,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	return &structurev1.CreateAppResponse{App: appMsg(a)}, nil
}

func (svc *AppsService) GetApp(ctx context.Context, req *structurev1.GetAppRequest) (*structurev1.GetAppResponse, error) {
	a, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	if err := svc.s.authorizeAppRow(ctx, a); err != nil {
		return nil, err
	}
	return &structurev1.GetAppResponse{App: appMsg(a)}, nil
}

func (svc *AppsService) ListApps(ctx context.Context, req *structurev1.ListAppsRequest) (*structurev1.ListAppsResponse, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	// 行级授权（ADR-0035 List 面）：project 锚先授权再查询。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Apps.ListByProjectPage(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterAppId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	out := &structurev1.ListAppsResponse{}
	for i := range list {
		out.Apps = append(out.Apps, appMsg(&list[i]))
	}
	return out, nil
}

// DeleteApp 收口删除（ADR-0023）：活跃部署拒绝（E_CONFLICT，先 cancel/等
// 终态）→ engine 收口拆载体 → tombstone + 撤路由 + 审计一事务落账。副作用
// 不可与审计同事务：先收口后落账，收口失败即整体失败（App 保持可操作）。
//
// 拒删不变式"App 存活 ⇒ 路由不得消失"由三道复查承载（ADR-0023 修订）：
//  1. 无锁预检：常规拒绝发生在零副作用阶段；
//  2. TeardownApp 锁内预检：与 Submit 共享 appMu，拆载体前所见即受理
//     终局——预检与收口之间受理的部署在副作用前拒绝；
//  3. tombstone 事务内复查：与 App tombstone、撤路由、审计同生共死——
//     收口后落账前受理的活跃部署使整单回滚（无 tombstone、无路由删除），
//     已拆载体由在途部署的 Ensure/回滚重放自愈，teardown-aborted 事件 +
//     审计留痕（不静默）。
func (svc *AppsService) DeleteApp(ctx context.Context, req *structurev1.DeleteAppRequest) (*structurev1.DeleteAppResponse, error) {
	if req.GetId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "id: must not be empty")
	}
	appRow, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	if err := svc.s.authorizeAppRow(ctx, appRow); err != nil {
		return nil, err
	}
	active, err := svc.s.Deployments.ActiveByApp(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	if len(active) > 0 {
		return nil, apperr.New("E_CONFLICT",
			"app %s has %d active deployment(s); cancel them or wait for a terminal state before deleting", req.GetId(), len(active))
	}
	if err := svc.s.Engine.TeardownApp(ctx, req.GetId()); err != nil {
		// 并发双删：对手已落 tombstone（Get 活跃行口径）→ 同形 404；
		// 锁内预检命中活跃部署 → E_CONFLICT（零副作用，App 保持可操作）；
		// 其余收口失败保持 E_INTERNAL（App 未落账，可重试删除）。
		switch {
		case errors.Is(err, state.ErrNotFound):
			return nil, apperr.New("E_NOT_FOUND", "app %s not found", req.GetId())
		case errors.Is(err, engine.ErrActiveDeployment):
			return nil, apperr.New("E_CONFLICT",
				"app %s has active deployment(s); cancel them or wait for a terminal state before deleting", req.GetId()).WithCause(err)
		}
		return nil, apperr.New("E_INTERNAL", "app teardown failed").WithCause(err)
	}

	// tombstone 事务：复查、App tombstone、撤路由、事件、审计同生共死。
	// aborted 非零 ⇔ 复查命中活跃部署（整单回滚的哨兵，见下方残余面）。
	var aborted int
	err = svc.s.commit(ctx, writeFact{
		// TOCTOU 复查：收口后、落账前受理的活跃部署在此拒绝——整单回滚
		//（无 tombstone、无路由删除；与 Submit 的存活判定互为对偶，单连接
		// 事务串行下窗口闭合）。
		checks: []acceptanceCheck{func(ctx context.Context, tx *sql.Tx) error {
			active, err := svc.s.Deployments.ActiveByApp(ctx, tx, req.GetId())
			if err != nil {
				return err
			}
			if len(active) > 0 {
				aborted = len(active)
				return errDeleteAborted
			}
			return nil
		}},
		write: func(ctx context.Context, tx *sql.Tx) error {
			if err := svc.s.Apps.SoftDelete(ctx, tx, req.GetId()); err != nil {
				return err
			}
			// 撤路由与 tombstone 同事务（ADR-0023 修订）：复查通过前路由
			// 不得消失——拒绝路径不碰路由（managedStep 的周期发布因此只
			// 可能见到"App 与路由同逝"的一致状态）。
			routes, err := svc.s.Routes.List(ctx, tx)
			if err != nil {
				return err
			}
			for _, rt := range routes {
				if rt.AppID != req.GetId() {
					continue
				}
				if err := svc.s.Routes.SoftDelete(ctx, tx, rt.ID); err != nil {
					return err
				}
			}
			return nil
		},
		events: []eventFact{structureEvent(eventAppDeleted, "app", req.GetId(), appRow.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "app.delete",
			Resource: "app/" + req.GetId(),
		}},
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
		return nil, mapStateError(err, "app")
	}
	svc.s.Engine.PublishRoutesNow() // Edge 全量发布即时触发（撤流收口）
	return &structurev1.DeleteAppResponse{}, nil
}

// errDeleteAborted 是 tombstone 事务复查命中活跃部署的回滚哨兵（事务内
// 错误只触发回滚；对外形态由 aborted 分支的 E_CONFLICT 承载）。
var errDeleteAborted = errors.New("app delete: teardown aborted by a deployment admitted mid-delete")

// recordTeardownAbort 落 teardown-aborted 事件 + 审计（同事务）。
func (svc *AppsService) recordTeardownAbort(ctx context.Context, id, projectID string, active int) error {
	payload, _ := json.Marshal(structureEventPayload{ID: id, ProjectID: projectID, ActiveDeployments: active}) //nolint:errcheck // 结构体字段恒可序列化
	err := svc.s.commit(ctx, writeFact{
		events: []eventFact{{name: eventAppTeardownAborted, aggregate: "app", id: id, payload: payload}},
		audits: []*audit.Entry{{
			ID: newID(), Source: audit.SourceSystem, Action: "app.teardown_abort",
			Resource: "app/" + id, AfterFP: fmt.Sprintf("active_deployments=%d", active),
		}},
	})
	if err != nil {
		return apperr.New("E_INTERNAL", "app teardown abort could not be recorded").WithCause(err)
	}
	return nil
}

// ---- Secrets（值永不回显；写路径审计全落） ----

type SecretsService struct {
	structurev1.UnimplementedSecretsServiceServer
	s *Services
}

func (svc *SecretsService) PutSecret(ctx context.Context, req *structurev1.PutSecretRequest) (*structurev1.PutSecretResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	if svc.s.Cipher == nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}
	// 行级授权（ADR-0035）：写面受理前置（Project Team 归属不可变，无 TOCTOU）。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 数据库凭证保留前缀（ADR-0029 决策 6）：值必须恒为完整连接 URL
	//（单真源），用户覆写会破坏凭证三面。
	if engine.IsDatabaseCredentialSecret(req.GetName()) {
		return nil, apperr.New("E_CONFLICT",
			"secret name %q is reserved for platform-managed database credentials; database secrets are minted by 'fleetly databases create'",
			req.GetName())
	}
	// 名字符集白名单（N1 收尾批 A3）：secret 名成为容器内 /run/secrets/<名>
	// 文件目标——受理面拒路径逃逸形态（/、\、空白、控制字符与 ".."）。
	if !spec.ValidSecretName(req.GetName()) {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"name: secret names must match %q, start with a letter or digit, and must not contain \"..\" (got %q; secret names become /run/secrets/<name> paths)",
			spec.SecretNamePattern, req.GetName())
	}
	sealed, err := svc.s.Cipher.Produce([]byte(req.GetValue()))
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	row := &secret.Secret{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(),
		Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
	}
	// 父资源存活校验（批 0 复核，同族面）：Project 级材料不得落在
	// 不存在/已删的 Project 下。
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Secrets.Upsert(ctx, tx, row)
		},
		events: []eventFact{structureEvent(eventSecretUpdated, "secret", row.Name, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "secret.put",
			Resource: "secret/" + row.Name, AfterFP: row.Fingerprint,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	return &structurev1.PutSecretResponse{Secret: secretMsg(*row)}, nil
}

func (svc *SecretsService) ListSecrets(ctx context.Context, req *structurev1.ListSecretsRequest) (*structurev1.ListSecretsResponse, error) {
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Secrets.ListFingerprints(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterName(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "secret")
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
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Secrets.SoftDelete(ctx, tx, req.GetProjectId(), req.GetName())
		},
		events: []eventFact{structureEvent(eventSecretDeleted, "secret", req.GetName(), req.GetProjectId())},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "secret.delete",
			Resource: "secret/" + req.GetName(),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	return &structurev1.DeleteSecretResponse{}, nil
}

// ---- Configs（版本化可回读） ----

type ConfigsService struct {
	structurev1.UnimplementedConfigsServiceServer
	s *Services
}

// Config 配额（F0.17 执法面，B2 落地）：per-Project 配置数上限与单值
// 大小上限。N0 小团队口径的保守缺省；配置面接入 config.proto 后可覆盖。
const (
	maxConfigsPerProject = 100
	maxConfigBytes       = 256 * 1024
	// SharedVariable 配额（F2.9，ADR-0043 决策 5）：Configs 先例同口径；
	// 值面按 env 值量级收一档（env 是进程环境面不是文件面）。
	maxSharedVariablesPerProject = 100
	maxVariableBytes             = 16 * 1024
	// maxAppsPerProject 是 per-Project 活跃 App 数上限（ADR-0017 附录 A.1，
	// F1.9）：Workload 的 App 来源面。Task 轴两枚配额住 engine（域拥有者，
	// ScaleTask/到期拍共用）。
	maxAppsPerProject = 50
)

func (svc *ConfigsService) PutConfig(ctx context.Context, req *structurev1.PutConfigRequest) (*structurev1.PutConfigResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	if len(req.GetContent()) > maxConfigBytes {
		return nil, apperr.New("E_INVALID_ARGUMENT", "content: exceeds the %d-byte per-config limit", maxConfigBytes)
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 数量配额按"Project 内配置名数"计（新名才占新位；同名 put 是新版本）
	// ——受理位检查（事务内读，ADR-0024：与写同事务，无先读后写 TOCTOU）。
	row := &configrepo.Config{ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(), Content: []byte(req.GetContent())}
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.configQuota(req.GetProjectId(), req.GetName()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Configs.Create(ctx, tx, row)
		},
		events: []eventFact{structureEvent(eventConfigUpdated, "config", row.Name, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "config.put",
			Resource: "config/" + row.Name, AfterFP: material.Fingerprint(row.Content),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "config")
	}
	return &structurev1.PutConfigResponse{Config: configMsg(*row, false)}, nil
}

func (svc *ConfigsService) GetConfig(ctx context.Context, req *structurev1.GetConfigRequest) (*structurev1.GetConfigResponse, error) {
	// 行级授权（ADR-0035）：Config 回读 env 内容——跨租户读面的大头。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	var (
		row *configrepo.Config
		err error
	)
	if req.GetVersion() > 0 {
		row, err = svc.s.Configs.GetVersion(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetName(), req.GetVersion())
	} else {
		row, err = svc.s.Configs.Latest(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetName())
	}
	if err != nil {
		return nil, mapStateError(err, "config")
	}
	return &structurev1.GetConfigResponse{Config: configMsg(*row, true)}, nil
}

func (svc *ConfigsService) ListConfigs(ctx context.Context, req *structurev1.ListConfigsRequest) (*structurev1.ListConfigsResponse, error) {
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 列表面只回 Project 内最新版（版本明细随版本面扩展）；分页只动行集
	//（每行仍是该 name 最新版，ADR-0026 after_* + limit）。
	list, err := svc.s.Configs.LatestByProjectPage(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterName(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "config")
	}
	out := &structurev1.ListConfigsResponse{}
	for _, row := range list {
		out.Configs = append(out.Configs, configMsg(row, false))
	}
	return out, nil
}

// ---- SharedVariables（Project 级共享变量，ADR-0043） ----

type SharedVariablesService struct {
	structurev1.UnimplementedSharedVariablesServiceServer
	s *Services
}

// PutSharedVariable 落/覆盖一条共享变量，响应携带受影响 App 提示（近似
// 口径，ADR-0043 决策 4——改共享变量不触发任何自动重部署，提示"哪些 App
// 重部署会取新值"）。
func (svc *SharedVariablesService) PutSharedVariable(ctx context.Context, req *structurev1.PutSharedVariableRequest) (*structurev1.PutSharedVariableResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	// 名 = env 键形态（ADR-0043 决策 2）：变量名直接成为容器 env 键。
	if !spec.ValidEnvName(req.GetName()) {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"name: variable names must match %q (variable names become environment keys; got %q)",
			spec.EnvNamePattern, req.GetName())
	}
	if len(req.GetValue()) > maxVariableBytes {
		return nil, apperr.New("E_INVALID_ARGUMENT", "value: exceeds the %d-byte per-variable limit", maxVariableBytes)
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	row := &sharedvariable.SharedVariable{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(), Value: req.GetValue(),
	}
	// 数量配额按"Project 内活跃名数"计（同名 put 是覆盖不占新位）——
	// 受理位检查（事务内读，ADR-0024 同款）。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.sharedVariableQuota(req.GetProjectId(), req.GetName()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.SharedVariables.Upsert(ctx, tx, row)
		},
		events: []eventFact{structureEvent(eventVariableUpdated, "variable", row.Name, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "variable.put",
			Resource: "variable/" + row.Name, AfterFP: row.Value,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	affected, err := svc.s.affectedApps(ctx, req.GetProjectId(), req.GetName(), req.GetValue())
	if err != nil {
		return nil, err
	}
	return &structurev1.PutSharedVariableResponse{Variable: sharedVariableMsg(*row), AffectedApps: affected}, nil
}

func (svc *SharedVariablesService) ListSharedVariables(ctx context.Context, req *structurev1.ListSharedVariablesRequest) (*structurev1.ListSharedVariablesResponse, error) {
	// 行级授权（ADR-0035）：值明文回显（非敏感契约）——跨租户读面。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.SharedVariables.ListPage(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterName(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	out := &structurev1.ListSharedVariablesResponse{}
	for _, row := range list {
		out.Variables = append(out.Variables, sharedVariableMsg(row))
	}
	return out, nil
}

func (svc *SharedVariablesService) DeleteSharedVariable(ctx context.Context, req *structurev1.DeleteSharedVariableRequest) (*structurev1.DeleteSharedVariableResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.SharedVariables.SoftDelete(ctx, tx, req.GetProjectId(), req.GetName())
		},
		events: []eventFact{structureEvent(eventVariableDeleted, "variable", req.GetName(), req.GetProjectId())},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "variable.delete",
			Resource: "variable/" + req.GetName(),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	// 删除后的受影响口径：键从共享层消失——最新冻结 env 仍带该键的 App
	// 重部署会失去它（absentValue 哨兵）。
	affected, err := svc.s.affectedApps(ctx, req.GetProjectId(), req.GetName(), absentValue)
	if err != nil {
		return nil, err
	}
	return &structurev1.DeleteSharedVariableResponse{AffectedApps: affected}, nil
}

// absentValue 是受影响口径的"键已从共享层消失"哨兵：任一进程 env 带该键
// 即受影响（冻结值与新状态恒不等）。
const absentValue = "\x00absent"

// sharedVariableQuota 是 per-Project 共享变量数配额检查（Configs 同款：
// 新名才占新位）。
func (s *Services) sharedVariableQuota(projectID, name string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		if _, err := s.SharedVariables.GetByName(ctx, tx, projectID, name); err == nil {
			return nil
		} else if !errors.Is(err, state.ErrNotFound) {
			return err
		}
		active, err := s.SharedVariables.ListActive(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if len(active) >= maxSharedVariablesPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d shared variables (limit %d)", projectID, len(active), maxSharedVariablesPerProject)
		}
		return nil
	}
}

// affectedApps 计算受影响 App 提示（ADR-0043 决策 4 的近似口径）：项目内
// 有 ≥1 Revision 的 App，其最新冻结 env 会因本次变更而不同（任一进程
// env[KEY] 与变更后状态不等；键缺席亦算——重冻结会补进）。App 层覆盖
// 同键的 App 会误报（冻结形态是扁平 map 无键来源账本）——提示是 DX 附注
// 不是行为承诺，误报代价 = 一次幂等重部署。
func (s *Services) affectedApps(ctx context.Context, projectID, name, newValue string) ([]string, error) {
	apps, err := s.Apps.ListByProject(ctx, s.DB.Runner(), projectID)
	if err != nil {
		return nil, err
	}
	var affected []string
	for _, a := range apps {
		rev, err := s.Revisions.Latest(ctx, s.DB.Runner(), a.ID)
		if errors.Is(err, state.ErrNotFound) {
			continue // 从未部署过的 App 首次部署自然取新值
		}
		if err != nil {
			return nil, err
		}
		if revisionUsesDifferentValue(rev.Spec, name, newValue) {
			affected = append(affected, a.ID)
		}
	}
	return affected, nil
}

// revisionUsesDifferentValue 报告冻结 spec 的任一进程 env[KEY] 与变更后
// 状态不同（absentValue 哨兵 = 键应消失：在场即不同）。反序列化失败按
// 不受影响处理——提示面不因冻结体解析问题放大故障。
func revisionUsesDifferentValue(blob []byte, name, newValue string) bool {
	s := &specv1.AppSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blob, s); err != nil {
		return false
	}
	differs := func(env map[string]string) bool {
		v, ok := env[name]
		if newValue == absentValue {
			return ok
		}
		return !ok || v != newValue
	}
	for _, p := range s.GetProcesses() {
		if differs(p.GetEnv()) {
			return true
		}
	}
	for _, j := range s.GetFirstBootJobs() {
		if differs(j.GetProcess().GetEnv()) {
			return true
		}
	}
	return false
}

// ---- Volumes ----

type VolumesService struct {
	structurev1.UnimplementedVolumesServiceServer
	s *Services
}

func (svc *VolumesService) CreateVolume(ctx context.Context, req *structurev1.CreateVolumeRequest) (*structurev1.CreateVolumeResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	row := &volume.Volume{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(),
		PinnedNodeID: req.GetPinnedNodeId(),
	}
	// 父资源存活校验（批 0 复核，同族面）：Project 级材料不得落在
	// 不存在/已删的 Project 下。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Volumes.Create(ctx, tx, row)
		},
		events: []eventFact{structureEvent(eventVolumeCreated, "volume", row.Name, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "volume.create",
			Resource: "volume/" + row.Name, AfterFP: row.PinnedNodeID,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "volume")
	}
	return &structurev1.CreateVolumeResponse{Volume: volumeMsg(*row)}, nil
}

func (svc *VolumesService) ListVolumes(ctx context.Context, req *structurev1.ListVolumesRequest) (*structurev1.ListVolumesResponse, error) {
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Volumes.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "volume")
	}
	out := &structurev1.ListVolumesResponse{}
	for _, row := range list {
		out.Volumes = append(out.Volumes, volumeMsg(row))
	}
	return out, nil
}

// ---- Networks ----

type NetworksService struct {
	structurev1.UnimplementedNetworksServiceServer
	s *Services
}

func (svc *NetworksService) CreateNetwork(ctx context.Context, req *structurev1.CreateNetworkRequest) (*structurev1.CreateNetworkResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	row := &networkrepo.Network{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(), EgressNone: req.GetEgressNone(),
	}
	// 父资源存活校验（批 0 复核，同族面）：Project 级材料不得落在
	// 不存在/已删的 Project 下。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Networks.Create(ctx, tx, row)
		},
		events: []eventFact{structureEvent(eventNetworkCreated, "network", row.Name, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "network.create",
			Resource: "network/" + row.Name, AfterFP: req.String(),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	return &structurev1.CreateNetworkResponse{Network: networkMsg(*row)}, nil
}

func (svc *NetworksService) ListNetworks(ctx context.Context, req *structurev1.ListNetworksRequest) (*structurev1.ListNetworksResponse, error) {
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Networks.ListByProjectPage(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterName(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	out := &structurev1.ListNetworksResponse{}
	for _, row := range list {
		out.Networks = append(out.Networks, networkMsg(row))
	}
	return out, nil
}

// ---- Network Peers（跨 Project 挂靠声明，ADR-0013 附录 A.1） ----

// networkPeerEventPayload 是 peer 三拍事件的载荷（字段只增）。
type networkPeerEventPayload struct {
	ID               string `json:"id"`
	NetworkID        string `json:"network_id"`
	NetworkName      string `json:"network_name"`
	NetworkProjectID string `json:"network_project_id"`
	PeerProjectID    string `json:"peer_project_id"`
}

// networkPeerEvent 构造 peer 事件事实（aggregate=network，id=网络行）。
func networkPeerEvent(name string, p *networkpeer.Peer, net *networkrepo.Network) eventFact {
	payload, _ := json.Marshal(networkPeerEventPayload{ //nolint:errcheck // 结构体字段恒可序列化
		ID: p.ID, NetworkID: p.NetworkID, NetworkName: net.Name,
		NetworkProjectID: net.ProjectID, PeerProjectID: p.PeerProjectID,
	})
	return eventFact{name: name, aggregate: "network", id: p.NetworkID, payload: payload}
}

// networkPeerAudits 是双方审计（ADR-0013：挂靠计入两侧）：网络侧 +
// 挂靠项目侧各一行，action 同名（ListAudit 的 action/resource 过滤面
// 两侧各自可答"谁挂在我的网上/我挂在哪里"）。
func networkPeerAudits(ctx context.Context, action string, p *networkpeer.Peer, net *networkrepo.Network) []*audit.Entry {
	fp := fmt.Sprintf("peer_project=%s network=%s state=%s", p.PeerProjectID, net.Name, p.State)
	mk := func(resource string) *audit.Entry {
		return &audit.Entry{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: action, Resource: resource, AfterFP: fp,
		}
	}
	return []*audit.Entry{
		mk("network/" + net.Name + "/peers/" + p.ID),
		mk("project/" + p.PeerProjectID + "/peers/" + p.ID),
	}
}

// loadPeerNetwork 读 peer 声明指向的网络行（活跃口径；A.1：批准面只对
// 活跃网络有意义——已删网络的挂靠无接收方）。
func (svc *NetworksService) loadPeerNetwork(ctx context.Context, networkID string) (*networkrepo.Network, error) {
	net, err := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), networkID)
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	return net, nil
}

// requirePeerReceiverTeam 校验调用方归属于接收方团队（ADR-0013：网络
// peer 的批准权在网络归属项目所属团队；ADR-0035 owner 豁免）。调用方身份
// 从 authn ctx 取（Token 的 Team 轴）；网络归属项目的 Team 实取自 project
// 行。不等即 E_FORBIDDEN——P1 修复前按行 ID 直批不比对归属，任意项目的
// 持有者可单向自助批准跨项目挂靠（越权网络接入）。declare 侧不动：挂靠方
// 本就任意项目发起（声明≠批准；归属校验在挂靠方自己的 Project 上）。
func (svc *NetworksService) requirePeerReceiverTeam(ctx context.Context, net *networkrepo.Network) error {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return err
	}
	if owner {
		return nil
	}
	proj, err := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), net.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if proj.Deleted() {
		return apperr.New("E_NOT_FOUND", "project %s not found", net.ProjectID)
	}
	if proj.TeamID != teamID {
		return apperr.New("E_FORBIDDEN",
			"only a token of team %s (owning the network's project) can approve this peer declaration", proj.TeamID).
			WithSuggestion("Ask the receiving project's team to approve the declaration, or revoke it.")
	}
	return nil
}

// authorizePeerSide 校验调用方属于声明任一侧的 Team（ADR-0035）：网络归属
// 方（receiver）或挂靠方（peer project）都可视/撤声明——声明是双边事实。
// 网络行已删时只按挂靠方判定（撤销语义本就覆盖"接收网络已逝"）。
func (svc *NetworksService) authorizePeerSide(ctx context.Context, p *networkpeer.Peer) error {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return err
	}
	if owner {
		return nil
	}
	if net, nerr := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), p.NetworkID); nerr == nil {
		if proj, perr := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), net.ProjectID); perr == nil && !proj.Deleted() && proj.TeamID == teamID {
			return nil
		}
	}
	if err := svc.s.authorizeProjectID(ctx, p.PeerProjectID); err == nil {
		return nil
	}
	return apperr.New("E_FORBIDDEN",
		"peer declaration %s belongs to another team; only a token of either side's team can view or revoke it", p.ID).
		WithSuggestion("Check the declaration id with the team that created it.")
}

// DeclareNetworkPeer 是挂靠方声明（pending 行）：peer 项目请求挂靠目标
// 网络。双向声明的第一拍；批准前引用不可投影（strict fail-closed）。
func (svc *NetworksService) DeclareNetworkPeer(ctx context.Context, req *structurev1.DeclareNetworkPeerRequest) (*structurev1.DeclareNetworkPeerResponse, error) {
	if req.GetNetworkId() == "" || req.GetPeerProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "network_id and peer_project_id: must not be empty")
	}
	net, err := svc.loadPeerNetwork(ctx, req.GetNetworkId())
	if err != nil {
		return nil, err
	}
	if net.ProjectID == req.GetPeerProjectId() {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"peer_project_id %s owns network %q; same-project attachment uses the project-local network name",
			req.GetPeerProjectId(), net.Name)
	}
	// 行级授权（ADR-0035）：挂靠方发起声明——Peer Project 必须归属调用方
	// Team（receiver 侧由 Approve 的 requirePeerReceiverTeam 把守）。
	if err := svc.s.authorizeProjectID(ctx, req.GetPeerProjectId()); err != nil {
		return nil, err
	}
	p := &networkpeer.Peer{ID: newID(), NetworkID: net.ID, PeerProjectID: req.GetPeerProjectId()}
	err = svc.s.commit(ctx, writeFact{
		// 双侧父资源存活（事务内）：网络归属方与挂靠方项目都必须在场。
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(net.ProjectID),
			svc.s.parentProjectAlive(req.GetPeerProjectId()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.NetworkPeers.Create(ctx, tx, p)
		},
		events: []eventFact{networkPeerEvent(eventNetworkPeerDeclared, p, net)},
		audits: networkPeerAudits(ctx, "network.peer_declare", p, net),
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	return &structurev1.DeclareNetworkPeerResponse{Peer: networkPeerMsg(p, net)}, nil
}

// ApproveNetworkPeer 是接收方批准（pending → approved）。已批准幂等返回；
// 已撤销的行不可复活（重新挂靠走新声明）。批准前校验调用方归属于接收方
// 团队（ADR-0013 语义执法，安全批 P1）——任意团队不得单向自助批准。
func (svc *NetworksService) ApproveNetworkPeer(ctx context.Context, req *structurev1.ApproveNetworkPeerRequest) (*structurev1.ApproveNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	net, err := svc.loadPeerNetwork(ctx, p.NetworkID)
	if err != nil {
		return nil, err
	}
	if err := svc.requirePeerReceiverTeam(ctx, net); err != nil {
		return nil, err
	}
	if p.State == networkpeer.StateApproved {
		return &structurev1.ApproveNetworkPeerResponse{Peer: networkPeerMsg(p, net)}, nil // 幂等
	}
	if p.State == networkpeer.StateRevoked {
		return nil, apperr.New("E_CONFLICT",
			"peer declaration %s was revoked; declare a new one to re-attach", p.ID)
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.NetworkPeers.Approve(ctx, tx, p.ID)
		},
		events: []eventFact{networkPeerEvent(eventNetworkPeerApproved, p, net)},
		audits: networkPeerAudits(ctx, "network.peer_approve", p, net),
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	fresh, gerr := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), p.ID)
	if gerr != nil {
		return nil, mapStateError(gerr, "network peer")
	}
	return &structurev1.ApproveNetworkPeerResponse{Peer: networkPeerMsg(fresh, net)}, nil
}

// RevokeNetworkPeer 撤销（任一侧；幂等）：即时隔离——撤销落账后引擎对
// 受影响 App isolate 重收敛剥离附件（断存量，A.4）。剥离失败不回滚撤销
// （受理面已生效），漂移扫描拍兜底重收敛。
func (svc *NetworksService) RevokeNetworkPeer(ctx context.Context, req *structurev1.RevokeNetworkPeerRequest) (*structurev1.RevokeNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	// 行级授权（ADR-0035）：撤销是任一侧的权利——双边归属判定。
	if err := svc.authorizePeerSide(ctx, p); err != nil {
		return nil, err
	}
	if p.State == networkpeer.StateRevoked {
		return &structurev1.RevokeNetworkPeerResponse{Peer: networkPeerMsg(p, nil)}, nil // 幂等
	}
	// 网络行可能已删（先删网络后撤声明的次序）：审计名缺失不阻断撤销——
	// 撤销的语义就是摘除挂靠，接收网络在不在都成立。
	net := &networkrepo.Network{ID: p.NetworkID}
	if active, nerr := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), p.NetworkID); nerr == nil {
		net = active
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.NetworkPeers.Revoke(ctx, tx, p.ID)
		},
		events: []eventFact{networkPeerEvent(eventNetworkPeerRevoked, p, net)},
		audits: networkPeerAudits(ctx, "network.peer_revoke", p, net),
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	// 即时隔离（A.4）：撤销已生效，剥离是收敛动作——失败记日志，漂移拍自愈。
	if serr := svc.s.Engine.IsolateNetworkPeer(ctx, p.NetworkID, p.PeerProjectID); serr != nil {
		if svc.s.Log != nil {
			svc.s.Log.Warn("network peer revoke: isolation reconverge deferred to the drift tick", "peer", p.ID, "err", serr)
		}
	}
	fresh, gerr := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), p.ID)
	if gerr != nil {
		return nil, mapStateError(gerr, "network peer")
	}
	return &structurev1.RevokeNetworkPeerResponse{Peer: networkPeerMsg(fresh, net)}, nil
}

func (svc *NetworksService) GetNetworkPeer(ctx context.Context, req *structurev1.GetNetworkPeerRequest) (*structurev1.GetNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	if err := svc.authorizePeerSide(ctx, p); err != nil {
		return nil, err
	}
	return &structurev1.GetNetworkPeerResponse{Peer: networkPeerMsg(p, svc.peerNetProjection(ctx, p))}, nil
}

// ListNetworkPeers 新→旧分页（ADR-0026 after_* + limit）。network_id /
// peer_project_id 过滤可选（接收方与挂靠方两侧视图同面）。行级过滤
// （ADR-0035）：非 owner 只见本 Team 任一侧在场的声明。
func (svc *NetworksService) ListNetworkPeers(ctx context.Context, req *structurev1.ListNetworkPeersRequest) (*structurev1.ListNetworkPeersResponse, error) {
	list, err := svc.s.NetworkPeers.List(ctx, svc.s.DB.Runner(),
		req.GetNetworkId(), req.GetPeerProjectId(), req.GetAfterPeerId(), int(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	out := &structurev1.ListNetworkPeersResponse{}
	for i := range list {
		p := &list[i]
		if !svc.peerVisibleToCaller(ctx, p) {
			continue
		}
		out.Peers = append(out.Peers, networkPeerMsg(p, svc.peerNetProjection(ctx, p)))
	}
	return out, nil
}

// peerVisibleToCaller 是 List 面的单行归属判定（owner 恒可见；网络已删的
// 行按挂靠方一侧判定）。判定失败按不可见处理——List 面不因解析故障放大
// 为整页错误（行级执法 fail-closed：未见即拒）。
func (svc *NetworksService) peerVisibleToCaller(ctx context.Context, p *networkpeer.Peer) bool {
	return svc.authorizePeerSide(ctx, p) == nil
}

// peerNetProjection 读声明指向的网络行（读投影便捷字段；行已删 → 空值，
// 不阻断列表）。
func (svc *NetworksService) peerNetProjection(ctx context.Context, p *networkpeer.Peer) *networkrepo.Network {
	net, err := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), p.NetworkID)
	if err != nil {
		return &networkrepo.Network{ID: p.NetworkID}
	}
	return net
}

// networkPeerMsg 行 → proto 映射（网络侧便捷字段来自读投影）。
func networkPeerMsg(p *networkpeer.Peer, net *networkrepo.Network) *structurev1.NetworkPeer {
	msg := &structurev1.NetworkPeer{
		Id: p.ID, PeerProjectId: p.PeerProjectID, State: string(p.State),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ApprovedAt: p.ApprovedAt,
	}
	if net != nil {
		msg.NetworkId = p.NetworkID
		msg.NetworkName = net.Name
		msg.NetworkProjectId = net.ProjectID
	}
	return msg
}
