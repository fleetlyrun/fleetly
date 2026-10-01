package fleetlygrpc

// Structure 上下文服务实现（Projects/Apps/Secrets/Configs/Volumes/Networks）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
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
	eventVolumeCreated      = "volume.created"
	eventNetworkCreated     = "network.created"
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
	p := &project.Project{ID: newID(), Name: req.GetName(), TeamID: req.GetTeamId()}
	if p.TeamID == "" {
		p.TeamID = identity.DefaultTeamID
	}
	// Team 轴接实（ADR-0028）：归属 Team 必须存在——Project 落在不存在的
	// Team 上会让域解析（engine projectTeam）悬空。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.teamExists(p.TeamID)},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Projects.Create(ctx, tx, p)
		},
		events: []eventFact{structureEvent(eventProjectCreated, "project", p.ID, "")},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "project.create",
			Resource: "project/" + p.ID, AfterFP: p.Name,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	return &structurev1.CreateProjectResponse{Project: projectMsg(p)}, nil
}

func (svc *ProjectsService) GetProject(ctx context.Context, req *structurev1.GetProjectRequest) (*structurev1.GetProjectResponse, error) {
	p, err := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	return &structurev1.GetProjectResponse{Project: projectMsg(p)}, nil
}

func (svc *ProjectsService) ListProjects(ctx context.Context, _ *structurev1.ListProjectsRequest) (*structurev1.ListProjectsResponse, error) {
	list, err := svc.s.Projects.List(ctx, svc.s.DB.Runner())
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
	a := &app.App{ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName()}
	// 父资源存活校验（批 0 复核）：apps.project_id 无 FK，不校验则对
	// 不存在/已删 project 建 App 直接成功，活 App 落已删项目、路由照发
	// ——DeleteProject 守卫的"窗口闭合"承诺以此对偶守卫成立。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
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
	return &structurev1.GetAppResponse{App: appMsg(a)}, nil
}

func (svc *AppsService) ListApps(ctx context.Context, req *structurev1.ListAppsRequest) (*structurev1.ListAppsResponse, error) {
	list, err := svc.s.Apps.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
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
	ct, err := svc.s.Cipher.Seal([]byte(req.GetValue()))
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	row := &secret.Secret{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(),
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(req.GetValue())),
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
	list, err := svc.s.Secrets.ListFingerprints(ctx, svc.s.DB.Runner(), req.GetProjectId())
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
)

func (svc *ConfigsService) PutConfig(ctx context.Context, req *structurev1.PutConfigRequest) (*structurev1.PutConfigResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	if len(req.GetContent()) > maxConfigBytes {
		return nil, apperr.New("E_INVALID_ARGUMENT", "content: exceeds the %d-byte per-config limit", maxConfigBytes)
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
	// 列表面只回 Project 内最新版（版本明细随版本面扩展）。
	list, err := svc.s.Configs.LatestByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "config")
	}
	out := &structurev1.ListConfigsResponse{}
	for _, row := range list {
		out.Configs = append(out.Configs, configMsg(row, false))
	}
	return out, nil
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
	list, err := svc.s.Networks.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	out := &structurev1.ListNetworksResponse{}
	for _, row := range list {
		out.Networks = append(out.Networks, networkMsg(row))
	}
	return out, nil
}
