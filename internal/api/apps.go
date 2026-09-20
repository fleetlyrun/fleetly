package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// AppsService 实现 server.v1.AppsService（T2.17）：列表带派生状态、详情
// 带 placement 与最近部署、删除走 tombstone 生命周期（state 现有状态机）。
// T2.19 增补 webhook/git 触发配置面（SetAppWebhookSecret/ShowAppWebhook/
// SetAppSource——secret 与认证材料经平台 envelope 加密落库，明文不落、
// 永不回读；show 面只回 configured 位）。
type AppsService struct {
	serverv1.UnimplementedAppsServiceServer
	st  *state.Store
	box *secrets.Box
	// withdraw 是路由撤销端口（H9：app 删除管线接通——deleting 清理时
	// 撤销该 app 全部路由）。nil = 不撤销（无 ingress 装配的测试面）。
	withdraw RouteWithdrawer
	// gitEndpoint 是 SSH git 面 host:port（git_remote_hint 的拼装原料；
	// 空 = 未启用，hint 留空）。
	gitEndpoint string
}

// RouteWithdrawer 是路由撤销端口（internal/ingress.Manager 隐式实现）：
// 删该 app 域名台账行 + 全量重发布（空视图落 noop 兜底，旧路由在 Traefik
// 侧真实撤销）。域名台账写入方唯一 = internal/ingress，故删除清理必须经
// 此通道而非直写 state。
type RouteWithdrawer interface {
	WithdrawAppRoutes(ctx context.Context, appID string) error
}

// NewAppsService 构造 AppsService（box 为 webhook secret 加密器；
// gitEndpoint 供 git remote 提示；withdraw 为路由撤销端口，可 nil）。
func NewAppsService(st *state.Store, box *secrets.Box, gitEndpoint string, withdraw RouteWithdrawer) *AppsService {
	return &AppsService{st: st, box: box, withdraw: withdraw, gitEndpoint: gitEndpoint}
}

// defaultListAppsLimit 是 ListApps 的 limit 缺省（proto 注释口径「缺省 100」；
// S18-A4 前 limit=0 无截断，与注释契约不符）。
const defaultListAppsLimit = 100

// ListApps 列出应用（active + deleting；deleted tombstone 不进默认列表
// ——GetApp 可显式查看，列表面向运营主视图）。派生状态批量化（S18-A4）：
// limit 截断后的 app 集合经 state 层两次 IN 查询（PlacementByApp +
// LatestDeploymentsByApp）取全部派生输入，替代逐 app 的 GetPlacement +
// ListAppDeployments N+1 形态。
func (s *AppsService) ListApps(ctx context.Context, req *serverv1.ListAppsRequest) (*serverv1.ListAppsResponse, error) {
	apps, err := s.st.ListActiveApps(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultListAppsLimit // 缺省 100（proto 注释本就如此，A4 落实）
	}
	if limit < len(apps) {
		apps = apps[:limit]
	}
	ids := make([]string, 0, len(apps))
	for _, app := range apps {
		ids = append(ids, app.ID)
	}
	placements, err := s.st.PlacementByApp(ctx, ids)
	if err != nil {
		return nil, err
	}
	deployments, err := s.st.LatestDeploymentsByApp(ctx, ids, derivedWindow)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.AppView, 0, len(apps))
	for _, app := range apps {
		// 批量 map 缺席键 = 零值 Placement（State 空串 = 无绑定记录，
		// 与 per-app 路径的 ErrPlacementNotFound 分支同派生语义）。
		derived := engine.DeriveAppState(factsFromWindow(placements[app.ID], deployments[app.ID]))
		out = append(out, &serverv1.AppView{
			Id:           app.ID,
			Name:         app.Name,
			Lifecycle:    string(app.Lifecycle),
			DerivedState: derived,
			CreatedAt:    timestamppb.New(app.CreatedAt),
			UpdatedAt:    timestamppb.New(app.UpdatedAt),
		})
	}
	return &serverv1.ListAppsResponse{Apps: out}, nil
}

// GetApp 应用详情：placement / 最近部署 / 派生状态正交细节。
func (s *AppsService) GetApp(ctx context.Context, req *serverv1.GetAppRequest) (*serverv1.GetAppResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetName())
	if err != nil {
		return nil, err
	}
	derived, err := s.derivedState(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	resp := &serverv1.GetAppResponse{
		Id:           app.ID,
		Name:         app.Name,
		Lifecycle:    string(app.Lifecycle),
		DerivedState: derived,
		CreatedAt:    timestamppb.New(app.CreatedAt),
		UpdatedAt:    timestamppb.New(app.UpdatedAt),
	}
	if p, err := s.st.GetPlacement(ctx, app.ID); err == nil {
		resp.Placement = placementView(p)
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return nil, err
	}
	rows, err := s.st.ListAppDeployments(ctx, app.ID, 5)
	if err != nil {
		return nil, err
	}
	resp.RecentDeployments = deploymentViews(rows)
	return resp, nil
}

// DeleteApp 推进 active → deleting（tombstone 第一拍）：保留期内名字仍
// 占用、不复活；与审计同事务 fail-closed。actor 记调用方 token（审计
// action = api.AppsService.DeleteApp，方法级；不发明新事件）。
// H9：deleting 第一拍提交后同步撤销路由（域名台账清理 + 全量重发布，
// 空视图落 noop 兜底——Traefik 侧真实撤销旧路由而非 502 残留）。
func (s *AppsService) DeleteApp(ctx context.Context, req *serverv1.DeleteAppRequest) (*serverv1.DeleteAppResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetName())
	if err != nil {
		return nil, err
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.MarkAppDeleting(ctx, app.ID); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, auditEntry(ctx, "app:"+app.ID, state.DiffSummary("lifecycle", "deleting"))) // B4：构造器替换手拼 JSON
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrInvalidLifecycleTransition):
			return nil, conflict("app not deletable from current lifecycle: " + app.Name)
		default:
			return nil, err
		}
	}
	// 路由撤销（同步清理，不引入新异步机制）：失败不回滚 lifecycle
	// （deleting 已提交、DeleteApp 不可重入），落失败审计 + 错误返回披露
	// ——sweep 的重发布不会重试台账删除（写入方唯一 = ingress），需运维
	// 处置；这是评审 H9 定级下最响的告警面。
	if s.withdraw != nil {
		if werr := s.withdraw.WithdrawAppRoutes(ctx, app.ID); werr != nil {
			entry := auditEntry(ctx, "app:"+app.ID, state.DiffSummary("lifecycle", "deleting", "route_withdraw", "failed")) // B4：构造器替换手拼 JSON
			entry.Result = "error"
			if aerr := s.st.InTx(ctx, func(tx *state.Tx) error {
				return tx.WriteAudit(ctx, entry)
			}); aerr != nil {
				return nil, fmt.Errorf("app %s entered deleting; route withdrawal failed (%v) and recording the failure audit also failed: %w", app.Name, werr, aerr)
			}
			return nil, fmt.Errorf("app %s entered deleting but route withdrawal failed (routes remain published; see audit): %w", app.Name, werr)
		}
	}
	return &serverv1.DeleteAppResponse{Name: app.Name, Lifecycle: string(state.LifecycleDeleting)}, nil
}

// SetAppWebhookSecret 设置 per-app webhook 签名密钥（admin；T2.19）：值经
// 平台 envelope 加密落库（明文不落），审计 app.webhook_secret_set 在 state
// 层同事务 fail-closed。secret 缺失的端点是 404 语义（未配置即未启用）。
func (s *AppsService) SetAppWebhookSecret(ctx context.Context, req *serverv1.SetAppWebhookSecretRequest) (*serverv1.SetAppWebhookSecretResponse, error) {
	if len(req.GetSecret()) < 16 {
		return nil, statusInvalidArgument("webhook secret must be at least 16 characters (验签是该端点的唯一认证)")
	}
	app, err := resolveApp(ctx, s.st, req.GetName())
	if err != nil {
		return nil, err
	}
	cipher, err := s.box.Encrypt([]byte(req.GetSecret()))
	if err != nil {
		return nil, fmt.Errorf("encrypt webhook secret: %w", err)
	}
	if err := s.st.SetAppWebhookSecret(ctx, app.ID, string(cipher), callerTokenID(ctx)); err != nil {
		return nil, err
	}
	return &serverv1.SetAppWebhookSecretResponse{Name: app.Name, Configured: true}, nil
}

// ShowAppWebhook 回读 webhook/git 触发配置（无敏感投影；admin）。
func (s *AppsService) ShowAppWebhook(ctx context.Context, req *serverv1.ShowAppWebhookRequest) (*serverv1.ShowAppWebhookResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetName())
	if err != nil {
		return nil, err
	}
	cfg, err := s.st.GetAppGitConfig(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	resp := &serverv1.ShowAppWebhookResponse{
		Name:             app.Name,
		SecretConfigured: cfg.SecretSet,
		SourceUrl:        cfg.SourceURL,
		SourceBranch:     cfg.Branch,
		SourceAuthKind:   string(cfg.AuthKind),
	}
	if s.gitEndpoint != "" {
		resp.GitRemoteHint = "ssh://git@" + s.gitEndpoint + "/" + app.Name + ".git"
	}
	return resp, nil
}

// SetAppSource 设置拉源配置（admin；T2.19）：url + branch + 认证形态。
// 整体替换语义（每次调用写全量字段）；认证材料必须与 auth_kind 同调提供
// ——材料经 envelope 加密落库，读回面无明文，因此无法「留旧」。
func (s *AppsService) SetAppSource(ctx context.Context, req *serverv1.SetAppSourceRequest) (*serverv1.SetAppSourceResponse, error) {
	kind := state.SourceAuthKind(req.GetSourceAuthKind())
	if !kind.Valid() {
		return nil, statusInvalidArgument("source_auth_kind must be none|https_token|ssh_key")
	}
	if kind == state.SourceAuthNone && req.GetSourceAuthSecret() != "" {
		return nil, statusInvalidArgument("source_auth_secret requires source_auth_kind https_token or ssh_key")
	}
	if kind != state.SourceAuthNone && req.GetSourceAuthSecret() == "" {
		return nil, statusInvalidArgument("source_auth_kind " + string(kind) + " requires source_auth_secret (整体替换语义——无法留旧)")
	}
	// E7④（S19）：认证材料最小长度与 webhook secret 同标（≥16）——弱材料
	// 即便 envelope 加密落库，认证面仍在线可穷举。
	if kind != state.SourceAuthNone && len(req.GetSourceAuthSecret()) < 16 {
		return nil, statusInvalidArgument("source_auth_secret must be at least 16 characters")
	}
	if req.GetSourceUrl() != "" && strings.ContainsAny(req.GetSourceUrl(), " \t\n\r") {
		return nil, statusInvalidArgument("source url must not contain whitespace")
	}
	// E7⑤（S19）：https_token 强制 https:// 源——http:// 明文链路会随请求
	// 泄露 token（gitserver buildFetch 拉源前同款防线，此处配置期早拒）。
	if kind == state.SourceAuthToken && strings.HasPrefix(req.GetSourceUrl(), "http://") {
		return nil, statusInvalidArgument("https_token auth requires an https:// source url")
	}
	app, err := resolveApp(ctx, s.st, req.GetName())
	if err != nil {
		return nil, err
	}
	secretCipher := ""
	if req.GetSourceAuthSecret() != "" {
		cipher, encErr := s.box.Encrypt([]byte(req.GetSourceAuthSecret()))
		if encErr != nil {
			return nil, fmt.Errorf("encrypt source auth secret: %w", encErr)
		}
		secretCipher = string(cipher)
	}
	if err := s.st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL:          req.GetSourceUrl(),
		Branch:       req.GetSourceBranch(),
		AuthKind:     kind,
		AuthSecret:   secretCipher,
		ActorTokenID: callerTokenID(ctx),
	}); err != nil {
		return nil, err
	}
	return &serverv1.SetAppSourceResponse{
		Name:           app.Name,
		SourceUrl:      req.GetSourceUrl(),
		SourceBranch:   req.GetSourceBranch(),
		SourceAuthKind: string(kind),
	}, nil
}

// derivedState 读面即时派生应用状态（state-model §2.10 纯函数，与引擎/CLI
// 共用 engine.DeriveAppState 同一实现）。单 app 读面（GetApp 等）沿用
// per-app 查询；ListApps 走 S18-A4 批量形态（factsFromWindow）。
func (s *AppsService) derivedState(ctx context.Context, appID string) (string, error) {
	facts, err := appFacts(ctx, s.st, appID)
	if err != nil {
		return "", err
	}
	return engine.DeriveAppState(facts), nil
}

// derivedWindow 是派生输入的部署回看窗口（与引擎 appFactsOf 同值：最近
// 25 条内找最近一次 succeeded）。
const derivedWindow = 25

// appFacts 读取派生输入事实（placement + 部署窗口）——单 app 形态。
func appFacts(ctx context.Context, st *state.Store, appID string) (engine.AppFacts, error) {
	var placement state.Placement
	if p, err := st.GetPlacement(ctx, appID); err == nil {
		placement = p
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return engine.AppFacts{}, err
	}
	rows, err := st.ListAppDeployments(ctx, appID, derivedWindow)
	if err != nil {
		return engine.AppFacts{}, err
	}
	return factsFromWindow(placement, rows), nil
}

// factsFromWindow 从「绑定 + 最近部署窗口（created_at 倒序）」构造派生输入
// 事实——per-app 与批量（S18-A4）两种读路径共用同一装配逻辑，杜绝两形态
// 的派生口径漂移。placement 零值（State 空串）= 无绑定记录的合法运行态，
// DeriveAppState 对空串与缺席同判（自由调度非 blocked）。
func factsFromWindow(placement state.Placement, rows []state.DeployRecord) engine.AppFacts {
	facts := engine.AppFacts{PlacementState: string(placement.State)}
	if len(rows) > 0 {
		facts.Latest = rows[0]
	}
	for _, r := range rows {
		if r.Status == state.DeploySucceeded {
			facts.LatestSucceeded = r
			break
		}
	}
	return facts
}

// placementView 构造 placement 投影。
func placementView(p state.Placement) *serverv1.PlacementView {
	v := &serverv1.PlacementView{
		PlatformNodeId: p.PlatformNodeID,
		LabelRef:       p.LabelRef,
		State:          string(p.State),
		Reason:         p.Reason,
		Source:         string(p.Source),
	}
	if !p.PinnedAt.IsZero() {
		v.PinnedAt = timestamppb.New(p.PinnedAt)
	}
	v.CreatedAt = timestamppb.New(p.CreatedAt)
	v.UpdatedAt = timestamppb.New(p.UpdatedAt)
	return v
}

// deploymentViews 构造部署投影列表。
func deploymentViews(rows []state.DeployRecord) []*serverv1.DeploymentView {
	out := make([]*serverv1.DeploymentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, deploymentView(r))
	}
	return out
}

func deploymentView(r state.DeployRecord) *serverv1.DeploymentView {
	v := &serverv1.DeploymentView{
		Id:              r.ID,
		App:             r.AppName,
		Kind:            r.Kind,
		Status:          string(r.Status),
		Phase:           r.Phase,
		RevisionId:      r.RevisionID,
		ErrorCode:       r.ErrorCode,
		Verdict:         r.Verdict,
		Recovery:        r.Recovery,
		SubstrateHalted: r.SubstrateHalted,
		DowntimeMs:      r.DowntimeMS,
		CreatedAt:       timestamppb.New(r.CreatedAt),
		UpdatedAt:       timestamppb.New(r.UpdatedAt),
		// git 触发来源（T2.19）：仅 git push/webhook 入队的部署非空——空串
		// 在 EmitUnpopulated=false 语义下不输出（读面缺省即「非 git 来源」）。
		SourceGitSha: r.SourceGitSHA,
		SourceGitRef: r.SourceGitRef,
	}
	if !r.FirstHealthyAt.IsZero() {
		v.FirstHealthyAt = timestamppb.New(r.FirstHealthyAt)
	}
	return v
}

// auditEntryFromPrincipal 构造审计条目骨架（actor 恒 human——API 无法区分
// 人类/AI 代理，token 备注/-ID 承载可追溯性；state-model §2.9 actor 词表
// 不扩）。
func auditEntry(ctx context.Context, target, diff string) state.AuditEntry {
	actorTokenID := ""
	if p, ok := PrincipalFromContext(ctx); ok {
		actorTokenID = p.TokenID
	}
	return state.AuditEntry{
		Actor:        "human",
		ActorTokenID: actorTokenID,
		Action:       actionFromContext(ctx),
		Target:       target,
		Result:       "ok",
		DiffSummary:  diff,
	}
}

// actionFromContext 由拦截器注入的方法名推导审计 action（api.<Service>.<Method>）。
func actionFromContext(ctx context.Context) string {
	if a, ok := ctx.Value(actionKey{}).(string); ok {
		return a
	}
	return "api.unknown"
}

type actionKey struct{}
