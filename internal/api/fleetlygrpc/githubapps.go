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
	"database/sql"
	"errors"
	"net/http"
	"strings"
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
	s *Services
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
