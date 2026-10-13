package fleetlygrpc

// GitHubApps 接入材料服务实现（ADR-0073 阶段 B）：私有 Git 仓库的一等
// 接入体。私钥 age 信封只写不读——明文只进受理与验证（.jwt/列仓即用即
// 弃），响应与读面只回指纹。受理即验证：SetGitHubApp 落库前实调 GitHub
// API 校验三元组并解析属主（请求不携带属主，系统填充）；上游可用性错误
// 归 E_GITHUB_UPSTREAM，输入值错误归 E_INVALID_ARGUMENT。
//
// 双作用域授权分野：Project 级行走行级 Project 授权；平台级（Global）行
// 是平台材料——写/删/列仓一律平台管理门（requirePlatformAdmin），行无
// Team 轴故行级授权不可达，此处是唯一门。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/github"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/githubapp"
)

type GitHubAppsService struct {
	deliveryv1.UnimplementedGitHubAppsServiceServer
	s     *Services
	flows *manifestFlows
}

// newGitHubAppsService 构造服务并铸流态签名钥（进程内随机——流不跨重启，
// 中断即重走，无持久化需求）。
func newGitHubAppsService(s *Services) *GitHubAppsService {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// 随机源失败 = 平台级故障；流禁用（start 报错），其余面不受影响。
		key = nil
	}
	return &GitHubAppsService{s: s, flows: newManifestFlows(key)}
}

// ---- Manifest 自动建流（ADR-0073 决策 8）----
//
// 三步跳转：start（签名 state + manifest 跳转 URL）→ 用户在 GitHub 创建
// App（GitHub 回调 redirect_url 带 code）→ consume（code 换凭据：私钥即
// 收即封暂存流存储，给安装页 URL）→ 用户安装 → setup 回调带
// installation_id → complete（受理即验证 + upsert 落行，nonce 一次性）。
// 手动表单保留为 fallback（GHES/CLI/受限环境；manifest 流是 github.com
// 专属）。

const (
	// manifestFlowTTL 是流态时效（code 本身 10 分钟有效，覆盖三步跳转
	// 富余）；过期即作废，用户重走。
	manifestFlowTTL = 15 * time.Minute
	// manifestHookPath 是 App webhook 占位（fleetly 推送触发走自有
	// flthook_ 通道，App 不声明 webhook——active=false 满足 GitHub 表单
	// 必填）。
	manifestHookPath = "/v1/github-apps/manifest/hook-placeholder"
	// manifestConnectPath 是 console 回调路由（redirect_url 与 setup_url
	// 共用；phase 由查询参数区分：code+state / installation_id+state）。
	manifestConnectPath = "/github-apps/connect"
)

// manifestFlow 是 consume 后、complete 前的暂存态（私钥密文在手，但内存
// 短驻——15 分钟 TTL + 一次性 nonce）。
type manifestFlow struct {
	pem       []byte
	appID     int64
	slug      string
	name      string // GitHub 表单显示名（重放 consume 时回显）
	projectID string // 空 = global
	exp       time.Time
}

// manifestFlows 是流存储：nonce → 暂存态；签名态校验与一次性消费的载体。
type manifestFlows struct {
	mu      sync.Mutex
	hmacKey []byte
	flows   map[string]*manifestFlow
}

func newManifestFlows(hmacKey []byte) *manifestFlows {
	return &manifestFlows{hmacKey: hmacKey, flows: map[string]*manifestFlow{}}
}

// manifestStatePayload 是签名态的载荷（state 对调用方不透明——内容可读
// 但不可伪造；作用域绑定防 CSRF）。
type manifestStatePayload struct {
	Nonce     string `json:"n"`
	ProjectID string `json:"p,omitempty"`
	Global    bool   `json:"g,omitempty"`
	Exp       int64  `json:"e"`
}

// signState 序列化载荷并附 HMAC（面向 start；consume/complete 侧 verify）。
func (f *manifestFlows) signState(payload manifestStatePayload) (string, error) {
	if len(f.hmacKey) == 0 {
		return "", fmt.Errorf("flow signing key unavailable")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, f.hmacKey)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// verifyState 校验签名与时效并还原载荷（不查流存储——nonce 在册性由
// consume/complete 各自判定）。
func (f *manifestFlows) verifyState(state string, now time.Time) (manifestStatePayload, error) {
	var payload manifestStatePayload
	body, sig, ok := strings.Cut(state, ".")
	if !ok || len(f.hmacKey) == 0 {
		return payload, fmt.Errorf("malformed flow state")
	}
	mac := hmac.New(sha256.New, f.hmacKey)
	mac.Write([]byte(body))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return payload, fmt.Errorf("flow state signature mismatch")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return payload, fmt.Errorf("flow state payload: %w", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, fmt.Errorf("flow state payload: %w", err)
	}
	if time.Unix(payload.Exp, 0).Before(now) {
		return payload, fmt.Errorf("flow state expired — restart the manifest flow")
	}
	return payload, nil
}

// put/take:consume 暂存 / complete 一次性取走（取走即删——nonce 单用）。
func (f *manifestFlows) put(nonce string, flow *manifestFlow) {
	f.mu.Lock()
	f.flows[nonce] = flow
	f.mu.Unlock()
}

// peek 在册即取不消费（consume 幂等重放用：code 在 GitHub 侧一次性，刷新
// 页面带已耗 code 再来时，凭据已在流内则直接回放安装页）。
func (f *manifestFlows) peek(nonce string, now time.Time) *manifestFlow {
	f.mu.Lock()
	defer f.mu.Unlock()
	flow, ok := f.flows[nonce]
	if !ok || flow.exp.Before(now) {
		return nil
	}
	return flow
}

func (f *manifestFlows) take(nonce string, now time.Time) (*manifestFlow, error) {
	f.mu.Lock()
	flow, ok := f.flows[nonce]
	if ok {
		delete(f.flows, nonce)
	}
	// 顺带清扫过期流（单控制面小集合，懒清扫足够）。
	for n, fl := range f.flows {
		if fl.exp.Before(now) {
			delete(f.flows, n)
		}
	}
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("manifest flow not found — it expired or was already completed; restart the flow")
	}
	return flow, nil
}

// SetGitHubApp 受理即验证 + 落库（同作用域同属主重复调用 = 覆盖更新，
// 私钥轮换 = 重放 put）。
func (svc *GitHubAppsService) SetGitHubApp(ctx context.Context, req *deliveryv1.SetGitHubAppRequest) (*deliveryv1.SetGitHubAppResponse, error) {
	projectID := ""
	switch scope := req.GetScope().(type) {
	case *deliveryv1.SetGitHubAppRequest_ProjectId:
		projectID = strings.TrimSpace(scope.ProjectId)
	case *deliveryv1.SetGitHubAppRequest_Global:
		if !scope.Global {
			return nil, apperr.New("E_INVALID_ARGUMENT", "global: set true to configure a platform-scoped app")
		}
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT", "scope: set either project_id or global=true (exactly one)")
	}
	if projectID != "" {
		if err := svc.s.authorizeProjectID(ctx, projectID); err != nil {
			return nil, err
		}
	} else if err := svc.s.requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	if req.GetAppId() <= 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must be the numeric GitHub App ID (positive)")
	}
	if req.GetInstallationId() <= 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "installation_id: must be the numeric installation ID (positive)")
	}
	if err := validateAPIBase(req.GetApiBase()); err != nil {
		return nil, err
	}
	pemBytes := []byte(req.GetPrivateKeyPem())
	if _, err := github.ParsePrivateKeyPEM(pemBytes); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	// 受理即验证（ADR-0073 决策 3）：私钥签 JWT 读 installation——错值
	// 当场精确报错，属主顺带解析，不等到首次构建。
	plain := &github.App{
		AppID: req.GetAppId(), InstallationID: req.GetInstallationId(),
		APIBase: strings.TrimSpace(req.GetApiBase()), PrivateKeyPEM: pemBytes,
	}
	inst, err := svc.s.GitHub.ResolveInstallation(ctx, plain)
	if err != nil {
		return nil, mapGitHubVerifyError(err)
	}
	sealed, err := svc.s.Cipher.Produce(pemBytes)
	if err != nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "sealing the private key failed: %v", err)
	}
	row := &githubapp.App{
		ProjectID: projectID, AppID: req.GetAppId(), InstallationID: req.GetInstallationId(),
		AccountLogin: inst.AccountLogin, APIBase: strings.TrimSpace(req.GetApiBase()),
		PrivateKeyCiphertext: sealed.Ciphertext, PrivateKeyFingerprint: sealed.Fingerprint,
	}
	txErr := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.GitHubApps.Upsert(ctx, tx, row)
		},
		audits: []*audit.Entry{{
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "github_app.put", Resource: "github_app/" + row.AccountLogin, AfterFP: sealed.Fingerprint,
		}},
	})
	if txErr != nil {
		return nil, mapStateError(txErr, "github app")
	}
	return &deliveryv1.SetGitHubAppResponse{App: githubAppMsg(row)}, nil
}

func (svc *GitHubAppsService) ListGitHubApps(ctx context.Context, req *deliveryv1.ListGitHubAppsRequest) (*deliveryv1.ListGitHubAppsResponse, error) {
	switch scope := req.GetScope().(type) {
	case *deliveryv1.ListGitHubAppsRequest_ProjectId:
		if err := svc.s.authorizeProjectID(ctx, strings.TrimSpace(scope.ProjectId)); err != nil {
			return nil, err
		}
		rows, err := svc.s.GitHubApps.ListByProject(ctx, svc.s.DB.Runner(), strings.TrimSpace(scope.ProjectId))
		if err != nil {
			return nil, mapStateError(err, "github app")
		}
		return &deliveryv1.ListGitHubAppsResponse{Apps: githubAppMsgs(rows)}, nil
	case *deliveryv1.ListGitHubAppsRequest_Global:
		if err := svc.s.requirePlatformAdmin(ctx); err != nil {
			return nil, err
		}
		rows, err := svc.s.GitHubApps.ListGlobal(ctx, svc.s.DB.Runner())
		if err != nil {
			return nil, mapStateError(err, "github app")
		}
		return &deliveryv1.ListGitHubAppsResponse{Apps: githubAppMsgs(rows)}, nil
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT", "scope: set either project_id or global=true (exactly one)")
	}
}

// DeleteGitHubApp 软删（解析面即时失效；tombstone 行留档，密文随 KEK
// rewrap 保留可解）。
func (svc *GitHubAppsService) DeleteGitHubApp(ctx context.Context, req *deliveryv1.DeleteGitHubAppRequest) (*deliveryv1.DeleteGitHubAppResponse, error) {
	row, err := svc.s.GitHubApps.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "github app")
	}
	if row.ProjectID != "" {
		if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
			return nil, err
		}
	} else if err := svc.s.requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	txErr := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.GitHubApps.SoftDelete(ctx, tx, row.ID)
		},
		audits: []*audit.Entry{{
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "github_app.delete", Resource: "github_app/" + row.AccountLogin,
		}},
	})
	if txErr != nil {
		return nil, mapStateError(txErr, "github app")
	}
	return &deliveryv1.DeleteGitHubAppResponse{}, nil
}

// ListGitHubAppRepos 现铸 token 列 installation 可达仓库（安装健康验证
// 面；上行解封只在内存短驻）。错误归 E_GITHUB_UPSTREAM——上游状态在此
// 是"安装健康"事实，不是输入错误。
func (svc *GitHubAppsService) ListGitHubAppRepos(ctx context.Context, req *deliveryv1.ListGitHubAppReposRequest) (*deliveryv1.ListGitHubAppReposResponse, error) {
	row, err := svc.s.GitHubApps.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "github app")
	}
	if row.Deleted() {
		return nil, mapStateError(state.ErrNotFound, "github app")
	}
	if row.ProjectID != "" {
		if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
			return nil, err
		}
	} else if err := svc.s.requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	if svc.s.Cipher == nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "the master key is not available")
	}
	plain, err := svc.s.Cipher.Open(row.PrivateKeyCiphertext)
	if err != nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "opening the private key failed: %v", err)
	}
	repos, err := svc.s.GitHub.ListRepos(ctx, &github.App{
		EntityID: row.ID, KeyFingerprint: row.PrivateKeyFingerprint,
		AppID: row.AppID, InstallationID: row.InstallationID,
		APIBase: row.APIBase, PrivateKeyPEM: plain,
	})
	if err != nil {
		return nil, apperr.New("E_GITHUB_UPSTREAM", "listing repositories: %v", err)
	}
	out := make([]*deliveryv1.GitHubAppRepo, 0, len(repos))
	for _, r := range repos {
		out = append(out, &deliveryv1.GitHubAppRepo{FullName: r.FullName, Private: r.Private})
	}
	return &deliveryv1.ListGitHubAppReposResponse{Repos: out}, nil
}

// githubAppMsg 投影聚合行（私钥永不回显，只回指纹）。
func githubAppMsg(a *githubapp.App) *deliveryv1.GitHubApp {
	return &deliveryv1.GitHubApp{
		Id: a.ID, ProjectId: a.ProjectID, AppId: a.AppID, InstallationId: a.InstallationID,
		AccountLogin: a.AccountLogin, ApiBase: a.APIBase,
		PrivateKeyFingerprint: a.PrivateKeyFingerprint,
		CreatedAt:             a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func githubAppMsgs(rows []githubapp.App) []*deliveryv1.GitHubApp {
	out := make([]*deliveryv1.GitHubApp, 0, len(rows))
	for i := range rows {
		out = append(out, githubAppMsg(&rows[i]))
	}
	return out
}

// mapGitHubVerifyError 把受理即验证链路的错误映射为 API 信封：输入值
// 问题（错配 / installation 404）归 E_INVALID_ARGUMENT（打错当场报），
// 上游可用性问题归 E_GITHUB_UPSTREAM（网络故障/限流/5xx——部署与重试
// 语义在 clone 侧另有精确失败，不静默降级）。
func mapGitHubVerifyError(err error) error {
	var upstream *github.UpstreamError
	switch {
	case errors.Is(err, github.ErrAppMismatch):
		return apperr.New("E_INVALID_ARGUMENT", "%v", err)
	case errors.As(err, &upstream) && upstream.Status == http.StatusNotFound:
		return apperr.New("E_INVALID_ARGUMENT", "%v", err)
	default:
		return apperr.New("E_GITHUB_UPSTREAM", "%v", err)
	}
}

// validateAPIBase 校验 api_base（GHES 基址；空 = 缺省 github.com 合法）。
// 基址会拼进 GitHub API 请求 URL——https-only + 空白/控制字符拒绝（与
// validateGitRepo 同口径）。
func validateAPIBase(b string) error {
	v := strings.TrimSpace(b)
	if v == "" {
		return nil
	}
	if !strings.HasPrefix(v, "https://") {
		return apperr.New("E_INVALID_ARGUMENT",
			"api_base: only https:// base URLs are supported (got %q)", v)
	}
	for i := 0; i < len(v); i++ {
		if c := rune(v[i]); unicode.IsSpace(c) || unicode.IsControl(c) {
			return apperr.New("E_INVALID_ARGUMENT",
				"api_base: must not contain whitespace or control characters (byte offset %d)", i)
		}
	}
	return nil
}

// ghManifestScope 解析 manifest 三动词的 (--project|--global) 互斥必一；
// 返回 project id 与 global 标记（oneof 包装由调用方自组）。
func ghManifestScope(project string, global bool) (string, bool, error) {
	if project != "" && global {
		return "", false, apperr.New("E_INVALID_ARGUMENT", "scope: set exactly one of project_id or global=true")
	}
	if project == "" && !global {
		return "", false, apperr.New("E_INVALID_ARGUMENT", "scope: set exactly one of project_id or global=true")
	}
	return project, global, nil
}

// StartManifestFlow 生成 manifest POST 表单材料（action_url + manifest JSON
// + 签名 state；state 绑作用域，15 分钟时效）。调用方渲染隐藏表单自动提交。
// 未配置公网 console 基址 = 精确失败（手动表单兜底）。
func (svc *GitHubAppsService) StartManifestFlow(ctx context.Context, req *deliveryv1.StartManifestFlowRequest) (*deliveryv1.StartManifestFlowResponse, error) {
	projectID, isGlobal, err := ghManifestScope(req.GetProjectId(), req.GetGlobal())
	if err != nil {
		return nil, err
	}
	if projectID != "" {
		if err := svc.s.authorizeProjectID(ctx, projectID); err != nil {
			return nil, err
		}
	} else if err := svc.s.requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(svc.s.GitHubManifestBase, "/")
	if base == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"the manifest flow needs the public console URL configured (console.host); use the manual form instead")
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, apperr.New("E_INTERNAL", "manifest flow nonce generation failed")
	}
	exp := time.Now().Add(manifestFlowTTL)
	state, err := svc.flows.signState(manifestStatePayload{
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), ProjectID: projectID, Global: isGlobal, Exp: exp.Unix(),
	})
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "manifest flow state signing failed: %v", err)
	}
	manifest := map[string]any{
		"name":                "fleetly-" + fmt.Sprintf("%x", nonce)[:6],
		"url":                 base,
		"hook_attributes":     map[string]any{"url": base + manifestHookPath, "active": false},
		"redirect_url":        base + manifestConnectPath,
		"setup_url":           base + manifestConnectPath,
		"public":              false,
		"default_permissions": map[string]string{"contents": "read", "metadata": "read"},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "manifest encoding failed: %v", err)
	}
	// 提交形态（GitHub 文档 "Creating a GitHub App from a manifest"）：浏览器
	// 以 HTML form **POST** 提交到 apps/new，字段名 manifest、值为 JSON 字符
	// 串；action 上的 state 查询参数是 GitHub 原样回显的 CSRF 串。GET 跳转
	// 带 query 一律被静默忽略（真机两连咬：base64url 与 std-base64 编码都修
	// 不好空表单——根因是形态而非编码）。
	action := "https://github.com/settings/apps/new?state=" + url.QueryEscape(state)
	return &deliveryv1.StartManifestFlowResponse{ActionUrl: action, Manifest: string(raw), State: state}, nil
}

// ConsumeManifestCode 用一次性 code 换 App 凭据：私钥即收即封暂存流存储
// （15 分钟 + 一次性 nonce），返回安装页 URL。流态签名先验、作用域授权
// 复核（防御纵深）。
func (svc *GitHubAppsService) ConsumeManifestCode(ctx context.Context, req *deliveryv1.ConsumeManifestCodeRequest) (*deliveryv1.ConsumeManifestCodeResponse, error) {
	if strings.TrimSpace(req.GetCode()) == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "code: must not be empty")
	}
	payload, err := svc.flows.verifyState(req.GetState(), time.Now())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	if payload.Global {
		if err := svc.s.requirePlatformAdmin(ctx); err != nil {
			return nil, err
		}
	} else if err := svc.s.authorizeProjectID(ctx, payload.ProjectID); err != nil {
		return nil, err
	}
	converted, err := svc.s.GitHub.ConvertManifestCode(ctx, svc.s.GitHubManifestAPIBase, req.GetCode())
	if err != nil {
		// code 是一次性的：成功兑换后刷新页面带已耗 code 再来 = 上游 404。
		// 凭据已在流内则幂等重放安装页；无在册流才升级为上游错误。
		if flow := svc.flows.peek(payload.Nonce, time.Now()); flow != nil {
			return &deliveryv1.ConsumeManifestCodeResponse{
				InstallUrl: "https://github.com/apps/" + flow.slug + "/installations/new",
				AppName:    flow.name,
			}, nil
		}
		return nil, apperr.New("E_GITHUB_UPSTREAM", "manifest conversion: %v", err)
	}
	svc.flows.put(payload.Nonce, &manifestFlow{
		pem: converted.PEM, appID: converted.AppID, slug: converted.Slug, name: converted.Name,
		projectID: payload.ProjectID, exp: time.Now().Add(manifestFlowTTL),
	})
	return &deliveryv1.ConsumeManifestCodeResponse{
		InstallUrl: "https://github.com/apps/" + converted.Slug + "/installations/new",
		AppName:    converted.Name,
	}, nil
}

// CompleteManifestFlow 以 setup 回调的 installation_id 落行：受理即验证
// （ResolveInstallation 校验 App 配对并解析属主）+ 密封 upsert。state 的
// 作用域与请求回传的 project_id 一致性校验（谎言即拒——冻结锚不可骗）。
func (svc *GitHubAppsService) CompleteManifestFlow(ctx context.Context, req *deliveryv1.CompleteManifestFlowRequest) (*deliveryv1.CompleteManifestFlowResponse, error) {
	payload, err := svc.flows.verifyState(req.GetState(), time.Now())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	if req.GetProjectId() != payload.ProjectID {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: does not match the flow scope")
	}
	flow, err := svc.flows.take(payload.Nonce, time.Now())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	if flow.projectID != "" {
		if err := svc.s.authorizeProjectID(ctx, flow.projectID); err != nil {
			return nil, err
		}
	} else if err := svc.s.requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	if req.GetInstallationId() <= 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "installation_id: must be positive (from the setup callback)")
	}
	if svc.s.Cipher == nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "the master key is not available")
	}
	// 受理即验证（与 SetGitHubApp 同链）：installation 配对校验 + 属主解析。
	// APIBase = manifest 流的 GitHub 基址（生产缺省 github.com；测试注入假件）。
	inst, err := svc.s.GitHub.ResolveInstallation(ctx, &github.App{
		AppID: flow.appID, InstallationID: req.GetInstallationId(),
		APIBase: svc.s.GitHubManifestAPIBase, PrivateKeyPEM: flow.pem,
	})
	if err != nil {
		return nil, mapGitHubVerifyError(err)
	}
	sealed, err := svc.s.Cipher.Produce(flow.pem)
	if err != nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "sealing the private key failed: %v", err)
	}
	row := &githubapp.App{
		ProjectID: flow.projectID, AppID: flow.appID, InstallationID: req.GetInstallationId(),
		AccountLogin:         inst.AccountLogin,
		PrivateKeyCiphertext: sealed.Ciphertext, PrivateKeyFingerprint: sealed.Fingerprint,
	}
	txErr := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.GitHubApps.Upsert(ctx, tx, row)
		},
		audits: []*audit.Entry{{
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "github_app.put", Resource: "github_app/" + row.AccountLogin, AfterFP: sealed.Fingerprint,
		}},
	})
	if txErr != nil {
		return nil, mapStateError(txErr, "github app")
	}
	return &deliveryv1.CompleteManifestFlowResponse{App: githubAppMsg(row)}, nil
}
