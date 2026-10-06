package fleetlygrpc

// TemplatesService 实现（F3.3，ADR-0050）：App 模板目录读面 + 一键部署
// 服务端编排 + 目录刷新。实例化是 create-or-reuse 序（App/网络/Secret/
// Database → 渲染 → 既有 Deploy 链 → Route）——每步复用既有服务方法
// （同包直调，受理位/事件/审计/收敛随各服务原样发射）；模板自身的摘要
// 事件与审计在序列尾一拍落账。渲染/刷新错误面 fail-closed（坏模板与坏
// 目录零副作用）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/apptemplate"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	catalogrepo "github.com/fleetlyrun/fleetly/internal/state/catalog"
)

// 模板面事件名（usage 反扫的字面量锚点）。
const (
	eventTemplateInstantiated = "template.instantiated"
	eventTemplatesRefreshed   = "templates.refreshed"
)

// templateEventPayload 是 template.instantiated 的载荷（字段只增）。
type templateEventPayload struct {
	Template   string `json:"template"`
	Version    string `json:"version"`
	Deployment string `json:"deployment"`
}

// templatesRefreshedPayload 是 templates.refreshed 的载荷（字段只增）。
type templatesRefreshedPayload struct {
	Previous string `json:"previous"`
	Digest   string `json:"digest"`
	Count    int    `json:"count"`
}

type TemplatesService struct {
	deliveryv1.UnimplementedTemplatesServiceServer
	s *Services
}

// catalogView 是一次目录解析的产物（服务源标注：内嵌或刷新快照）。
type catalogView struct {
	entries []apptemplate.Entry
	source  string // builtin | refreshed
	digest  string // 聚合 digest（快照 digest 或内嵌聚合）
}

// resolveCatalog 解析当前服务目录（ADR-0050 决策 4 解析序：快照在场优先、
// 内嵌兜底）。快照 body 已在刷新时逐条核对与预校验——此处信任存储面。
func (s *Services) resolveCatalog(ctx context.Context) (*catalogView, error) {
	snap, err := s.Catalog.Get(ctx, s.DB.Runner())
	if err == nil {
		var manifest apptemplate.Manifest
		if jerr := json.Unmarshal([]byte(snap.Body), &manifest); jerr != nil {
			return nil, apperr.New("E_INTERNAL", "stored template catalog snapshot is not valid JSON").WithCause(jerr)
		}
		return &catalogView{entries: manifest.Templates, source: "refreshed", digest: snap.Digest}, nil
	}
	if !errors.Is(err, catalogrepo.ErrNoSnapshot) {
		return nil, mapStateError(err, "template catalog")
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
	view, err := svc.s.resolveCatalog(ctx)
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
	if req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
	view, err := svc.s.resolveCatalog(ctx)
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

// InstantiateTemplate 一键部署编排（ADR-0050 决策 3 的 create-or-reuse 序）。
// 部分失败诚实语义：失败即停带精确错误；重跑按 create-or-reuse 收敛
// （不旋转密码、不重建库/路由）。
func (svc *TemplatesService) InstantiateTemplate(ctx context.Context, req *deliveryv1.InstantiateTemplateRequest) (*deliveryv1.InstantiateTemplateResponse, error) {
	if req.GetProjectId() == "" || req.GetTemplate() == "" || req.GetAppName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id, template and app_name: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	view, err := svc.s.resolveCatalog(ctx)
	if err != nil {
		return nil, err
	}
	entry, doc, err := view.findTemplate(req.GetTemplate())
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
	if hasSecrets && svc.s.Cipher == nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}

	resources := []*deliveryv1.InstantiatedResource{}
	record := func(kind, id, name string, reused bool) {
		resources = append(resources, &deliveryv1.InstantiatedResource{Kind: kind, Id: id, Name: name, Reused: reused})
	}

	// 1. App create-or-reuse（按名——重跑即重部署，latest-wins）。
	var appID string
	if existing, err := svc.s.Apps.GetByName(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetAppName()); err == nil {
		appID = existing.ID
		record("app", appID, req.GetAppName(), true)
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, mapStateError(err, "app")
	} else {
		created, err := (&AppsService{s: svc.s}).CreateApp(ctx, &structurev1.CreateAppRequest{
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
		if _, err := svc.s.Networks.GetByName(ctx, svc.s.DB.Runner(), req.GetProjectId(), "default"); err == nil {
			record("network", "", "default", true)
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, mapStateError(err, "network")
		} else {
			if _, err := (&NetworksService{s: svc.s}).CreateNetwork(ctx, &structurev1.CreateNetworkRequest{
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
			return nil, mapValidationError(err)
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
		if _, err := svc.s.Secrets.GetByName(ctx, svc.s.DB.Runner(), req.GetProjectId(), secretName); err == nil {
			record("secret", "", secretName, true)
			continue
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, mapStateError(err, "secret")
		}
		if _, err := (&SecretsService{s: svc.s}).PutSecret(ctx, &structurev1.PutSecretRequest{
			ProjectId: req.GetProjectId(), Name: secretName, Value: mintDatabasePassword(),
		}); err != nil {
			return nil, err
		}
		record("secret", "", secretName, false)
	}

	// 5. databases create-or-reuse（凭证单真源链复用——ADR-0029 决策 6）。
	for _, decl := range doc.Databases {
		if existing, err := svc.s.Databases.GetByName(ctx, svc.s.DB.Runner(), req.GetProjectId(), decl.Name); err == nil {
			record("database", existing.ID, decl.Name, true)
			continue
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, mapStateError(err, "database")
		}
		created, err := (&DatabasesService{s: svc.s}).CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
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
	dep, err := (&DeploymentsService{s: svc.s}).Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, ComposeYaml: string(composeYAML),
	})
	if err != nil {
		return nil, err
	}
	deploymentID := dep.GetDeployment().GetId()
	record("deployment", deploymentID, "", false)

	// 7. routes create-or-reuse（host 全局唯一——同 host 既有 Route 复用，
	// quickstart 同款；tls 缺省 none——模板 routes 声明可显式 auto/ACME）。
	allRoutes, err := svc.s.Routes.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "route")
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
		created, err := (&RoutesService{s: svc.s}).CreateRoute(ctx, &proxyv1.CreateRouteRequest{
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
	err = svc.s.commit(ctx, writeFact{
		write: nil,
		events: []eventFact{identityEvent(eventTemplateInstantiated, "app", appID,
			templateEventPayload{Template: entry.Name, Version: entry.Version, Deployment: deploymentID})},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "template.instantiate",
			Resource: "app/" + appID, AfterFP: entry.Name + "@" + entry.Version,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "template instantiation")
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
	if svc.s.TemplatesCatalogURL == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"the template catalog refresh is disabled: server.templates_catalog_url is empty and the embedded catalog is the whole catalog")
	}
	previous, err := svc.s.resolveCatalog(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := apptemplate.FetchCatalog(ctx, svc.s.TemplatesCatalogURL)
	if err != nil {
		return nil, apperr.New("E_CATALOG_UNAVAILABLE", "%v", err).WithCause(err)
	}
	body, err := json.Marshal(apptemplate.Manifest{Version: 1, Templates: entries})
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "refreshed catalog could not be encoded").WithCause(err)
	}
	digest := apptemplate.AggregateDigest(entries)
	snap := &catalogrepo.Snapshot{Digest: digest, Body: string(body)}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Catalog.Put(ctx, tx, snap)
		},
		events: []eventFact{identityEvent(eventTemplatesRefreshed, "templates", digest,
			templatesRefreshedPayload{Previous: previous.digest, Digest: digest, Count: len(entries)})},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "templates.refresh",
			Resource: "templates/catalog", AfterFP: fmt.Sprintf("%s -> %s (%d entries)", previous.digest, digest, len(entries)),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "template catalog")
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
