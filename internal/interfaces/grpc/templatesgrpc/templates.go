// Package templatesgrpc 实现 fleetly.delivery.v1.TemplatesService：App 模板
// 目录读面、一键部署服务端编排与目录刷新。
package templatesgrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/apptemplate"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/catalog"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// Authorizer 是行级授权消费面（fleetlygrpc.Services 实现）。
type Authorizer interface {
	AuthorizeProjectID(ctx context.Context, projectID string) error
}

// Deps 是模板服务的依赖（窄接口 + 内层动作函数，不持整个 gRPC Services）。
type Deps struct {
	Catalog              catalog.Repo
	CatalogURL           func() string
	Cipher               *material.Cipher
	Apps                 app.Repo
	Networks             network.Repo
	Secrets              secret.Repo
	Databases            database.Repo
	Routes               route.Repo
	Projects             project.Repo
	Auth                 Authorizer
	RequireScope         func(ctx context.Context, resource string, op string) error
	Commit               func(ctx context.Context, fact grpcapi.WriteFact) error
	CreateApp            func(ctx context.Context, req *structurev1.CreateAppRequest) (*structurev1.CreateAppResponse, error)
	CreateNetwork        func(ctx context.Context, req *structurev1.CreateNetworkRequest) (*structurev1.CreateNetworkResponse, error)
	PutSecret            func(ctx context.Context, req *structurev1.PutSecretRequest) (*structurev1.PutSecretResponse, error)
	CreateDatabase       func(ctx context.Context, req *structurev1.CreateDatabaseRequest) (*structurev1.CreateDatabaseResponse, error)
	Deploy               func(ctx context.Context, req *deliveryv1.DeployRequest) (*deliveryv1.DeployResponse, error)
	CreateRoute          func(ctx context.Context, req *proxyv1.CreateRouteRequest) (*proxyv1.CreateRouteResponse, error)
	MintDatabasePassword func() string
}

// catalogURL 返回动态目录基址（apitest/golden 在构造后覆写 Services.TemplatesCatalogURL）。
func (d Deps) catalogURL() string {
	if d.CatalogURL == nil {
		return ""
	}
	return d.CatalogURL()
}

// TemplatesService 满足 deliveryv1.TemplatesServiceServer。
type TemplatesService struct {
	deliveryv1.UnimplementedTemplatesServiceServer
	deps Deps
}

// Register 注册模板服务。
func Register(srv *grpc.Server, deps Deps) {
	deliveryv1.RegisterTemplatesServiceServer(srv, &TemplatesService{deps: deps})
}

var _ deliveryv1.TemplatesServiceServer = (*TemplatesService)(nil)

// 模板面事件名与载荷导出面。
const (
	EventTemplateInstantiated = "template.instantiated"
	EventTemplateRefreshed    = "template.refreshed"
)

// TemplateEventPayload 是 template.instantiated 的载荷（字段只增）。
type TemplateEventPayload struct {
	Template   string `json:"template"`
	Version    string `json:"version"`
	Deployment string `json:"deployment"`
}

// TemplateRefreshedPayload 是 template.refreshed 的载荷（字段只增）。
type TemplateRefreshedPayload struct {
	Previous string `json:"previous"`
	Digest   string `json:"digest"`
	Count    int    `json:"count"`
}

// catalogView 是一次目录解析的产物（服务源标注：内嵌或刷新快照）。
type catalogView struct {
	entries []apptemplate.Entry
	source  string // builtin | refreshed
	digest  string // 聚合 digest（快照 digest 或内嵌聚合）
}

// resolveCatalog 解析当前服务目录（ADR-0050 决策 4 解析序：快照在场优先、
// 内嵌兜底）。快照 body 已在刷新时逐条核对与预校验——此处信任存储面。
func (d Deps) resolveCatalog(ctx context.Context) (*catalogView, error) {
	snap, err := d.Catalog.Get(ctx)
	if err == nil {
		var manifest apptemplate.Manifest
		if jerr := json.Unmarshal([]byte(snap.Body), &manifest); jerr != nil {
			return nil, apperr.New("E_INTERNAL", "stored template catalog snapshot is not valid JSON").WithCause(jerr)
		}
		return &catalogView{entries: manifest.Templates, source: "refreshed", digest: snap.Digest}, nil
	}
	if !errors.Is(err, catalog.ErrNoSnapshot) {
		return nil, grpcapi.MapStateError(err, "template catalog")
	}
	builtin, err := apptemplate.Builtin()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "embedded template catalog is unavailable").WithCause(err)
	}
	return &catalogView{entries: builtin, source: "builtin", digest: apptemplate.AggregateDigest(builtin)}, nil
}

// templateMsg 目录条目 → proto 投影。
func templateMsg(e apptemplate.Entry, doc *apptemplate.Doc) *deliveryv1.Template {
	msg := &deliveryv1.Template{Name: e.Name, Version: e.Version, Digest: e.Digest, Description: doc.Description}
	for _, v := range doc.Variables {
		msg.Variables = append(msg.Variables, &deliveryv1.TemplateVariable{
			Name: v.Name, Type: string(v.Type), Description: v.Description,
			Default: v.Default, Required: v.Required,
		})
	}
	return msg
}

// findTemplate 目录内按名取条目 + 解析态。
func (v *catalogView) findTemplate(name string) (apptemplate.Entry, *apptemplate.Doc, error) {
	for _, e := range v.entries {
		if e.Name != name {
			continue
		}
		doc, err := apptemplate.Parse([]byte(e.Body))
		if err != nil {
			// 快照/内嵌在落位前已预校验——此处恒不可达，仍诚实失败。
			return e, nil, apperr.New("E_TEMPLATE_INVALID", "template %s@%s failed to parse: %v", e.Name, e.Version, err).WithCause(err)
		}
		return e, doc, nil
	}
	names := make([]string, 0, len(v.entries))
	for _, e := range v.entries {
		names = append(names, e.Name)
	}
	return apptemplate.Entry{}, nil, apperr.New("E_NOT_FOUND",
		"template %q is not in the catalog (available: %v; source: %s)", name, names, v.source)
}

func (svc *TemplatesService) ListTemplates(ctx context.Context, _ *deliveryv1.ListTemplatesRequest) (*deliveryv1.ListTemplatesResponse, error) {
	view, err := svc.deps.resolveCatalog(ctx)
	if err != nil {
		return nil, err
	}
	out := &deliveryv1.ListTemplatesResponse{Source: view.source}
	for _, e := range view.entries {
		doc, err := apptemplate.Parse([]byte(e.Body))
		if err != nil {
			return nil, apperr.New("E_INTERNAL", "catalog entry %s failed to parse", e.Name).WithCause(err)
		}
		out.Templates = append(out.Templates, templateMsg(e, doc))
	}
	return out, nil
}

func (svc *TemplatesService) GetTemplate(ctx context.Context, req *deliveryv1.GetTemplateRequest) (*deliveryv1.GetTemplateResponse, error) {
	// name 必填检查已退役为 GetTemplateRequest 的 protovalidate CEL 注解
	//（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	view, err := svc.deps.resolveCatalog(ctx)
	if err != nil {
		return nil, err
	}
	entry, doc, err := view.findTemplate(req.GetName())
	if err != nil {
		return nil, err
	}
	return &deliveryv1.GetTemplateResponse{Template: templateMsg(entry, doc), Body: entry.Body}, nil
}

// mapRenderError 占位移除：渲染错误二分逻辑内联在 InstantiateTemplate
//（values.* 前缀 = 调用方供值错误 E_INVALID_ARGUMENT；其余 = 模板文档
// 缺陷 E_TEMPLATE_INVALID——ADR-0050 决策 3）。

// innerScopeNeed 是一条内层动作的能力需求（resource 与内层 RPC 的
// api_key_scope 同源；action 是执行序上的动作名，供对照表与错误定位）。
type innerScopeNeed struct {
	action   string
	resource string
}

// templateInnerScopes 列出本次实例化将执行的内层写动作及其 scope（顺序 =
// 执行序；首个缺口即拒）。逐动作对照（R08-M3 收口表）：
//
//	CreateApp       → apps:write
//	CreateNetwork   → networks:write（模板声明 databases 时确保 default 网）
//	PutSecret       → secrets:write（secret 型变量平台铸造）
//	CreateDatabase  → databases:write
//	Deploy          → deployments:write（既有部署链）
//	CreateRoute     → routes:write
//
// 复用路径（create-or-reuse 命中既有行）不改变能力声明：动作集按"可能创建"
// 计（fail-closed，能力面与声明并集一致）。
func templateInnerScopes(doc *apptemplate.Doc, hasSecrets bool) []innerScopeNeed {
	out := []innerScopeNeed{
		{action: "CreateApp", resource: "apps"},
	}
	if len(doc.Databases) > 0 {
		out = append(out, innerScopeNeed{action: "CreateNetwork", resource: "networks"})
	}
	if hasSecrets {
		out = append(out, innerScopeNeed{action: "PutSecret", resource: "secrets"})
	}
	if len(doc.Databases) > 0 {
		out = append(out, innerScopeNeed{action: "CreateDatabase", resource: "databases"})
	}
	out = append(out, innerScopeNeed{action: "Deploy", resource: "deployments"})
	if len(doc.Routes) > 0 {
		out = append(out, innerScopeNeed{action: "CreateRoute", resource: "routes"})
	}
	return out
}

// InstantiateTemplate 一键部署编排（ADR-0050 决策 3 的 create-or-reuse 序）。
// 部分失败诚实语义：失败即停带精确错误；重跑按 create-or-reuse 收敛
// （不旋转密码、不重建库/路由）。
func (svc *TemplatesService) InstantiateTemplate(ctx context.Context, req *deliveryv1.InstantiateTemplateRequest) (*deliveryv1.InstantiateTemplateResponse, error) {
	// project_id/template/app_name 必填组合检查已退役为
	// InstantiateTemplateRequest 的 protovalidate CEL 注解（P4-1 批 1 Run
	// B1）：validate 拦截器在 handler 之前执法，拆 per-field 文案。
	// app_name 的 dns 标签形状也在该注解上（create-or-reuse 序内进程内
	// 直调 CreateApp/CreateNetwork 不经 validate 拦截器，形状约束必须在
	// 边界自足——单源 entityid.NamePatternSource 的 descriptor 对齐测试
	// 执法）。RefreshTemplates 的目录配置检查（业务面）留在服务内。
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 摘要事件的归属在进事务前解析（R08-M8：解析失败整单拒绝）。本编排的
	// 内层步骤各自提交，故解析先于任何副作用——不留下半实例化缺口。
	// 依赖闭包在求值（提交）时才读 appID/deploymentID/entry（已就位）。
	var (
		appID        string
		deploymentID string
		entry        apptemplate.Entry
	)
	summaryFact, err := grpcapi.FactOf(ctx, svc.deps.Projects, req.GetProjectId(), func(team string) writes.FactRow {
		return writes.FactRow{
			Name: EventTemplateInstantiated, Aggregate: "app", ID: appID,
			Payload: grpcapi.MustJSON(TemplateEventPayload{Template: entry.Name, Version: entry.Version, Deployment: deploymentID}),
			TeamID:  team,
			Action:  "template.instantiate", Resource: "app/" + appID, AfterFP: entry.Name + "@" + entry.Version,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	view, err := svc.deps.resolveCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var doc *apptemplate.Doc
	entry, doc, err = view.findTemplate(req.GetTemplate())
	if err != nil {
		return nil, err
	}
	hasSecrets := false
	for _, v := range doc.Variables {
		if v.Type == apptemplate.VarSecret {
			hasSecrets = true
			break
		}
	}
	if hasSecrets && svc.deps.Cipher == nil {
		// 未配主密钥是显式关态（P4-3 批 3）：cipher 面缺席精确 503，自
		// E_SECRET_UNAVAILABLE 双语义拆出，文案逐字节保留。
		return nil, apperr.New("E_MASTER_KEY_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}

	// 内层动作能力预检（R08-M3）：本 handler 在进程内直调 Apps/Networks/
	// Secrets/Databases/Deployments/Routes 服务方法，authn 拦截器只在 RPC
	// 边界按外层 templates:write 执法一次——内层动词的 scope 必须在此显式
	// 复核（与各内层 RPC 的 api_key_scope 同源）。预检覆盖本次将执行的
	// 动作集、先于任何副作用：缺任一能力即 403 零副作用；各动作调用点仍有
	// 二次复核（防后续新增内层动作漏配预检）。
	for _, need := range templateInnerScopes(doc, hasSecrets) {
		if err := svc.deps.RequireScope(ctx, need.resource, identity.OpWrite); err != nil {
			return nil, err
		}
	}

	resources := []*deliveryv1.InstantiatedResource{}
	record := func(kind, id, name string, reused bool) {
		resources = append(resources, &deliveryv1.InstantiatedResource{Kind: kind, Id: id, Name: name, Reused: reused})
	}

	// 1. App create-or-reuse（按名——重跑即重部署，latest-wins）。
	if existing, err := svc.deps.Apps.GetByName(ctx, req.GetProjectId(), req.GetAppName()); err == nil {
		appID = existing.ID
		record("app", appID, req.GetAppName(), true)
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, grpcapi.MapStateError(err, "app")
	} else {
		if err := svc.deps.RequireScope(ctx, "apps", identity.OpWrite); err != nil {
			return nil, err
		}
		created, err := svc.deps.CreateApp(ctx, &structurev1.CreateAppRequest{
			ProjectId: req.GetProjectId(), Name: req.GetAppName(),
		})
		if err != nil {
			return nil, err
		}
		appID = created.GetApp().GetId()
		record("app", appID, req.GetAppName(), false)
	}

	// 2. 模板声明 databases 时确保项目 default 网在场（CreateDatabase 受理
	// 位要求活跃网络——零网项目的库不可达，拒绝优于静默孤岛）。
	if len(doc.Databases) > 0 {
		if _, err := svc.deps.Networks.GetByName(ctx, req.GetProjectId(), "default"); err == nil {
			record("network", "", "default", true)
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, grpcapi.MapStateError(err, "network")
		} else {
			if err := svc.deps.RequireScope(ctx, "networks", identity.OpWrite); err != nil {
				return nil, err
			}
			if _, err := svc.deps.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{
				ProjectId: req.GetProjectId(), Name: "default",
			}); err != nil {
				return nil, err
			}
			record("network", "", "default", false)
		}
	}

	// 3. 渲染（values 校验 fail-closed；secret 名参与命名）。
	rendered, err := doc.Render(req.GetValues(), req.GetAppName())
	if err != nil {
		var verr *spec.ValidationError
		if errors.As(err, &verr) && strings.HasPrefix(verr.Field, "values.") {
			return nil, grpcapi.MapValidationError(err)
		}
		return nil, apperr.New("E_TEMPLATE_INVALID", "%s: %s (template %s@%s failed its fail-closed render chain)", verr.Field, verr.Reason, entry.Name, entry.Version).WithCause(err)
	}

	// 4. secret 变量铸造/复用（重试不旋转——已有同名行直接复用）。
	secretVarNames := make([]string, 0, len(rendered.SecretNames))
	for name := range rendered.SecretNames {
		secretVarNames = append(secretVarNames, name)
	}
	sort.Strings(secretVarNames)
	for _, varName := range secretVarNames {
		secretName := rendered.SecretNames[varName]
		if _, err := svc.deps.Secrets.GetByName(ctx, req.GetProjectId(), secretName); err == nil {
			record("secret", "", secretName, true)
			continue
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, grpcapi.MapStateError(err, "secret")
		}
		if err := svc.deps.RequireScope(ctx, "secrets", identity.OpWrite); err != nil {
			return nil, err
		}
		if _, err := svc.deps.PutSecret(ctx, &structurev1.PutSecretRequest{
			ProjectId: req.GetProjectId(), Name: secretName, Value: svc.deps.MintDatabasePassword(),
		}); err != nil {
			return nil, err
		}
		record("secret", "", secretName, false)
	}

	// 5. databases create-or-reuse（凭证单真源链复用——ADR-0029 决策 6）。
	for _, decl := range doc.Databases {
		if existing, err := svc.deps.Databases.GetByName(ctx, req.GetProjectId(), decl.Name); err == nil {
			record("database", existing.ID, decl.Name, true)
			continue
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, grpcapi.MapStateError(err, "database")
		}
		if err := svc.deps.RequireScope(ctx, "databases", identity.OpWrite); err != nil {
			return nil, err
		}
		created, err := svc.deps.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
			ProjectId: req.GetProjectId(), Name: decl.Name, Engine: decl.Engine,
		})
		if err != nil {
			return nil, err
		}
		record("database", created.GetDatabase().GetId(), decl.Name, false)
	}

	// 6. 渲染产物 → 既有 Deploy 链（compose_yaml 形态——归一化/冻结/
	// admission/审计/事件全复用，零新部署轨）。
	composeYAML, err := yaml.Marshal(rendered.Compose)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "rendered compose could not be encoded").WithCause(err)
	}
	if err := svc.deps.RequireScope(ctx, "deployments", identity.OpWrite); err != nil {
		return nil, err
	}
	dep, err := svc.deps.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, ComposeYaml: string(composeYAML),
	})
	if err != nil {
		return nil, err
	}
	deploymentID = dep.GetDeployment().GetId()
	record("deployment", deploymentID, "", false)

	// 7. routes create-or-reuse（host 全局唯一——同 host 既有 Route 复用，
	// quickstart 同款；tls 缺省 none——模板 routes 声明可显式 auto/ACME）。
	allRoutes, err := svc.deps.Routes.List(ctx)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "route")
	}
	byHost := map[string]string{} // host → route id
	for i := range allRoutes {
		byHost[allRoutes[i].Host] = allRoutes[i].ID
	}
	for _, r := range rendered.Routes {
		if existingID, ok := byHost[r.Host]; ok {
			record("route", existingID, r.Host, true)
			continue
		}
		if err := svc.deps.RequireScope(ctx, "routes", identity.OpWrite); err != nil {
			return nil, err
		}
		created, err := svc.deps.CreateRoute(ctx, &proxyv1.CreateRouteRequest{
			ProjectId: req.GetProjectId(), Host: r.Host, AppId: appID,
			Process: r.Process, Port: r.Port, Protocol: r.Protocol, TlsMode: routeTLS(r.TLS),
		})
		if err != nil {
			return nil, err
		}
		routeID := created.GetRoute().GetId()
		byHost[r.Host] = routeID
		record("route", routeID, r.Host, false)
	}

	// 8. 摘要事件 + 审计一拍（伴生资源的事件已由各服务步骤发射）。
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: nil,
		Facts: []func() writes.FactRow{summaryFact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "template instantiation")
	}
	return &deliveryv1.InstantiateTemplateResponse{
		AppId: appID, DeploymentId: deploymentID, TemplateVersion: entry.Version,
		Resources: resources,
	}, nil
}

// RefreshTemplates 目录刷新（ADR-0050 决策 4）：拉取 → digest 核对 + 全量
// 预校验（apptemplate.FetchCatalog 单源）→ 原子覆盖快照 + 事件/审计。
// 未配置 catalog URL = 精确拒绝（内嵌目录即全部——冷启动诚实边界）。
func (svc *TemplatesService) RefreshTemplates(ctx context.Context, _ *deliveryv1.RefreshTemplatesRequest) (*deliveryv1.RefreshTemplatesResponse, error) {
	// 平台管理员门（R08-H1）：模板目录是平台级材料（一次刷新服务全体
	// Project 的一键部署目录），门在配置探测/拉取/审计之前。
	if err := grpcapi.RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	if svc.deps.catalogURL() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"the template catalog refresh is disabled: server.templates_catalog_url is empty and the embedded catalog is the whole catalog")
	}
	previous, err := svc.deps.resolveCatalog(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := apptemplate.FetchCatalog(ctx, svc.deps.catalogURL())
	if err != nil {
		return nil, apperr.New("E_CATALOG_UNAVAILABLE", "%v", err).WithCause(err)
	}
	body, err := json.Marshal(apptemplate.Manifest{Version: 1, Templates: entries})
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "refreshed catalog could not be encoded").WithCause(err)
	}
	digest := apptemplate.AggregateDigest(entries)
	snap := &catalog.Snapshot{Digest: digest, Body: string(body)}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Catalog.Put(ctx, snap)
		},
		Facts: []func() writes.FactRow{
			// 平台级事件（模板目录无 owner 域——平台级，全员可见）：team
			// 留 ''（P0-2 终态口径），经 platformFact 显式声明（R08-M8）。
			// aggregate 随前缀归一：templates → template（单数域，P2 批二
			// 裁决——见事件名常量注释）。
			grpcapi.PlatformFact(func() writes.FactRow {
				return writes.FactRow{
					Name: EventTemplateRefreshed, Aggregate: "template", ID: digest,
					Payload: grpcapi.MustJSON(TemplateRefreshedPayload{Previous: previous.digest, Digest: digest, Count: len(entries)}),
					Action:  "templates.refresh", Resource: "templates/catalog",
					AfterFP: fmt.Sprintf("%s -> %s (%d entries)", previous.digest, digest, len(entries)),
					Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			}),
		},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "template catalog")
	}
	return &deliveryv1.RefreshTemplatesResponse{
		PreviousDigest: previous.digest, Digest: digest, TemplateCount: int32(len(entries)), //nolint:gosec // 目录条目数
	}, nil
}

// routeTLS 是模板 routes 声明的 tls 值（缺省 none——quickstart CLI 同款；
// CreateRoute 的服务端缺省是 auto，模板面 sslip 形态无公网 DNS，显式收口）。
func routeTLS(tls string) string {
	if tls == "" {
		return "none"
	}
	return tls
}
