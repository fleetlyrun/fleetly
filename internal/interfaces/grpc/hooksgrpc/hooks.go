// Package hooksgrpc 实现 fleetly.delivery.v1.HooksService：per-App Git
// 触发配置面与 GitHub push webhook 接收链（HMAC 签名即凭证）。
package hooksgrpc

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"google.golang.org/grpc"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/composeintake"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// Deploy 是部署受理面的窄消费接口（engine 门面实现）。
type Deploy interface {
	SubmitSpec(ctx context.Context, req api.SubmitSpecRequest) (*revision.Revision, *deployment.Deployment, *api.Admission, error)
}

// Registry 是构建源解析面的窄消费接口（engine 门面实现）。
type Registry interface {
	ResolveGitHead(ctx context.Context, projectID, repoURL, branch string) (string, error)
	CloneAt(ctx context.Context, projectID, repoURL, ref, dir string) error
}

// Authorizer 是行级授权消费面（fleetlygrpc.Services 实现）。
type Authorizer interface {
	AuthorizeAppID(ctx context.Context, appID string) (*app.App, error)
}

// Deps 是 hook 服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	Hooks     hook.Repo
	Apps      app.Repo
	Projects  project.Repo
	Cipher    *material.Cipher
	Deploy    Deploy
	Registry  Registry
	Auth      Authorizer
	Commit    func(ctx context.Context, fact grpcapi.WriteFact) error
	VarLookup func(ctx context.Context, appRow *app.App) (composeintake.VariableLookup, error)
}

// Service 满足 deliveryv1.HooksServiceServer。
type Service struct {
	deliveryv1.UnimplementedHooksServiceServer
	deps Deps
}

// New 构造 hook 服务。
func New(deps Deps) *Service { return &Service{deps: deps} }

// Register 注册 hook 服务。
func Register(srv *grpc.Server, deps Deps) {
	deliveryv1.RegisterHooksServiceServer(srv, New(deps))
}

var _ deliveryv1.HooksServiceServer = (*Service)(nil)

// HookPushAcceptedPayload 是 hook.push_accepted 的载荷（触发的部署锚）。
type HookPushAcceptedPayload struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Deployment string `json:"deployment"`
}

// HookRefreshAcceptedPayload 是 hook.refresh_accepted 的载荷（同形）。
type HookRefreshAcceptedPayload = HookPushAcceptedPayload

// HooksURLPrefix 是接收面 REST 入口前缀（gateway 原生 handler 挂载点；
// CLI 展示 URL 时拼接）。
const HooksURLPrefix = "/v1/hooks/"

// hook 面事件名（usage 反扫的字面量锚点；四族 CRUD 补事件批——P2 批二
// 声明级遗漏收口：SetGitHook/Rotate 此前只有审计无事件）。
const (
	EventHookCreated = "hook.created"
	EventHookUpdated = "hook.updated"
	EventHookRotated = "hook.rotated"
	EventHookDeleted = "hook.deleted"
)

// HookEventPayload 是 hook.* 事件的载荷（字段只增；token 材料永不出现——
// URL token 与 webhook secret 同源，明文只在铸造/再生成响应出现一次）。
type HookEventPayload struct {
	AppID string `json:"app_id"`
}

// SetGitHook 建/改配置：无既有行时铸造 Token（明文仅本次响应返回），
// 既有行只改配置字段（换 Token 走 RotateHookToken）。
func (svc *Service) SetGitHook(ctx context.Context, req *deliveryv1.SetGitHookRequest) (*deliveryv1.SetGitHookResponse, error) {
	// app_id/repo 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截（repo 空值文案随字段原文逐字节上
	// 注解）；validateGitRepo 的 https 白名单/嵌入材料/空白控制字符/私网
	// 段是动态形态，保留手写。
	// 受理面防线（安全批）：repo 会冻结进 GitSource 并由控制面原样送进
	// `git clone` 的 argv——ext::/--upload-pack= 等传输形态在 root 控制面
	// 上等价任意命令执行（P0）。create 与 update 两路径都在此处拦截。
	if err := validateGitRepo(req.GetRepo()); err != nil {
		return nil, err
	}
	if err := validateGitBranch(normalizeBranch(req.GetBranch())); err != nil {
		return nil, err
	}
	// Dockerfile 路径入口校验（N1 收尾批 A7）：归一后校验（create 与
	// update 两路径共用）。
	if err := validateDockerfile(normalizeDockerfile(req.GetDockerfile())); err != nil {
		return nil, err
	}
	// 行级授权（ADR-0035）：App 归属 Team 比对（载行复用；Apps.Get 活跃行
	// 口径——tombstone App 在此即 404）。
	appRow, err := svc.deps.Auth.AuthorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}

	existing, err := svc.deps.Hooks.Get(ctx, appRow.ID)
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return nil, grpcapi.MapStateError(err, "hook")
	}

	var secret string
	if errors.Is(err, state.ErrNotFound) {
		material, merr := identity.NewHookToken()
		if merr != nil {
			return nil, apperr.New("E_INTERNAL", "hook token generation failed").WithCause(merr)
		}
		sealed, serr := svc.deps.Cipher.Produce([]byte(material.Secret))
		if serr != nil {
			return nil, apperr.New("E_SECRET_UNAVAILABLE", "sealing the hook secret failed: %v", serr)
		}
		row := &hook.Hook{
			AppID: appRow.ID, Repo: strings.TrimSpace(req.GetRepo()),
			Branch: normalizeBranch(req.GetBranch()), Dockerfile: normalizeDockerfile(req.GetDockerfile()),
			WatchPaths:  normalizeWatchPaths(req.GetWatchPaths()),
			TokenSHA256: material.SHA256, TokenPrefix: material.Prefix, SecretCiphertext: sealed.Ciphertext,
		}
		// 事件归属进事务前解析（R08-M8：解析失败整单拒绝）。
		fact, ferr := grpcapi.FactOf(ctx, svc.deps.Projects, appRow.ProjectID, func(team string) writes.FactRow {
			return writes.FactRow{
				Name: EventHookCreated, Aggregate: "hook", ID: appRow.ID,
				Payload: grpcapi.MustJSON(HookEventPayload{AppID: appRow.ID}),
				TeamID:  team,
				Action:  "hook.create", Resource: "app/" + appRow.ID, AfterFP: row.Repo + "@" + row.Branch,
				Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			}
		})
		if ferr != nil {
			return nil, ferr
		}
		txErr := svc.deps.Commit(ctx, grpcapi.WriteFact{
			Write: func(ctx context.Context, st transact.Stores) error {
				return st.Hooks.Create(ctx, row)
			},
			Facts: []func() writes.FactRow{fact},
		})
		if txErr != nil {
			return nil, grpcapi.MapStateError(txErr, "hook")
		}
		secret = material.Secret
		return &deliveryv1.SetGitHookResponse{Hook: hookMsg(row), Secret: secret}, nil
	}

	existing.Repo = strings.TrimSpace(req.GetRepo())
	existing.Branch = normalizeBranch(req.GetBranch())
	existing.Dockerfile = normalizeDockerfile(req.GetDockerfile())
	existing.WatchPaths = normalizeWatchPaths(req.GetWatchPaths())
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, appRow.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: EventHookUpdated, Aggregate: "hook", ID: appRow.ID,
			Payload: grpcapi.MustJSON(HookEventPayload{AppID: appRow.ID}),
			TeamID:  team,
			Action:  "hook.update", Resource: "app/" + appRow.ID, AfterFP: existing.Repo + "@" + existing.Branch,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	txErr := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Hooks.UpdateConfig(ctx, existing)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if txErr != nil {
		return nil, grpcapi.MapStateError(txErr, "hook")
	}
	return &deliveryv1.SetGitHookResponse{Hook: hookMsg(existing)}, nil
}

func (svc *Service) GetGitHook(ctx context.Context, req *deliveryv1.GetGitHookRequest) (*deliveryv1.GetGitHookResponse, error) {
	// 行级授权（ADR-0035）+ 活跃行口径：hook 配置挂在 App 轴上，App 已删
	// 即 404（孤儿子女拒绝——与接收面同语义，级联删除前的窗口期也闭合）。
	if _, err := svc.deps.Auth.AuthorizeAppID(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	row, err := svc.deps.Hooks.Get(ctx, req.GetAppId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "hook")
	}
	return &deliveryv1.GetGitHookResponse{Hook: hookMsg(row)}, nil
}

// RotateHookToken 吊销旧串铸新（URL token 与 GitHub webhook secret 同源
// 双换；GitHub 侧需同步更新 webhook secret）。行级授权（ADR-0035）载 App
// 行比对归属——顺带闭合批 1 记档的"不校验 App 存活"：Apps.Get 活跃行口
// 径，tombstone App 即 404（此前 rotate 对已删 App 静默换串成功）。
func (svc *Service) RotateHookToken(ctx context.Context, req *deliveryv1.RotateHookTokenRequest) (*deliveryv1.RotateHookTokenResponse, error) {
	appRow, err := svc.deps.Auth.AuthorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	if _, err := svc.deps.Hooks.Get(ctx, req.GetAppId()); err != nil {
		return nil, grpcapi.MapStateError(err, "hook")
	}
	material, err := identity.NewHookToken()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "hook token generation failed").WithCause(err)
	}
	sealed, err := svc.deps.Cipher.Produce([]byte(material.Secret))
	if err != nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "sealing the hook secret failed: %v", err)
	}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, appRow.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: EventHookRotated, Aggregate: "hook", ID: req.GetAppId(),
			Payload: grpcapi.MustJSON(HookEventPayload{AppID: req.GetAppId()}),
			TeamID:  team,
			Action:  "hook.rotate", Resource: "app/" + req.GetAppId(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	txErr := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Hooks.RotateToken(ctx, req.GetAppId(), material.SHA256, material.Prefix, sealed.Ciphertext)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if txErr != nil {
		return nil, grpcapi.MapStateError(txErr, "hook")
	}
	row, err := svc.deps.Hooks.Get(ctx, req.GetAppId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "hook")
	}
	return &deliveryv1.RotateHookTokenResponse{Hook: hookMsg(row), Secret: material.Secret}, nil
}

// DeleteGitHook 移除 hook 配置（W-8：解除通道——SetGitHook 是全量
// upsert，表达不了"解除"；没有本 RPC 时材料闸门是单向门，唯一出口是
// override_hook）。URL token 与 webhook secret 即刻失效（旧行即逝——
// 接收面在活跃行里查 token，旧 URL 的 push 401）；材料所有权归还
// deploy 面（ADR-0072）。非幂等：无 hook 即 404（与 Get 同形——误删
// 可见，不静默成功）。DeleteByApp 清整个聚合（去重行 + hook 行，
// FB-1 级联同缝）。
func (svc *Service) DeleteGitHook(ctx context.Context, req *deliveryv1.DeleteGitHookRequest) (*deliveryv1.DeleteGitHookResponse, error) {
	// 行级授权（ADR-0035）：Apps.Get 活跃行口径，tombstone App 即 404。
	appRow, err := svc.deps.Auth.AuthorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	if _, err := svc.deps.Hooks.Get(ctx, req.GetAppId()); err != nil {
		return nil, grpcapi.MapStateError(err, "hook")
	}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, appRow.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: EventHookDeleted, Aggregate: "hook", ID: appRow.ID,
			Payload: grpcapi.MustJSON(HookEventPayload{AppID: appRow.ID}),
			TeamID:  team,
			Action:  "hook.delete", Resource: "app/" + appRow.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	txErr := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Hooks.DeleteByApp(ctx, req.GetAppId())
		},
		Facts: []func() writes.FactRow{fact},
	})
	if txErr != nil {
		return nil, grpcapi.MapStateError(txErr, "hook")
	}
	return &deliveryv1.DeleteGitHookResponse{}, nil
}

// hookMsg 投影聚合行（secret 永不回显）。
func hookMsg(h *hook.Hook) *deliveryv1.GitHook {
	return &deliveryv1.GitHook{
		AppId: h.AppID, Repo: h.Repo, Branch: h.Branch, Dockerfile: h.Dockerfile,
		WatchPaths: h.WatchPaths, TokenPrefix: h.TokenPrefix,
		CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}

// normalizeBranch 归一分支过滤（剥 refs/heads/ 前缀；空 = 全部分支）。
func normalizeBranch(b string) string {
	b = strings.TrimSpace(b)
	return strings.TrimPrefix(b, "refs/heads/")
}

// normalizeDockerfile 归一构建文件路径（缺省 Dockerfile；剥首尾斜杠）。
func normalizeDockerfile(d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return "Dockerfile"
	}
	return strings.Trim(d, "/")
}

// validateDockerfile 校验构建文件路径（N1 收尾批 A7，入口校验与 branch 同
// 口径）：路径进 buildkit solve 的 filename 与构建上下文读取——控制字符/
// 空白逐字节拒绝（argv/日志走私面），".."/"/" 前缀与反斜杠拒绝（上下文
// 目录逃逸的受理面防线；buildkit 自身还有一层 context 边界执法——纵深）。
func validateDockerfile(d string) error {
	if d == "" {
		return nil
	}
	for i := 0; i < len(d); i++ {
		if c := rune(d[i]); unicode.IsSpace(c) || unicode.IsControl(c) {
			return apperr.New("E_INVALID_ARGUMENT",
				"dockerfile: must not contain whitespace or control characters (byte offset %d)", i)
		}
	}
	if strings.Contains(d, "..") || strings.Contains(d, "\\") || strings.HasPrefix(d, "/") {
		return apperr.New("E_INVALID_ARGUMENT",
			"dockerfile: %q must stay a relative path inside the build context (no \"..\" segments or backslashes)", d)
	}
	return nil
}

// normalizeWatchPaths 归一触发路径（剥空白项与首尾斜杠、去重保序；空集 =
// 全部变更触发）。
func normalizeWatchPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// validateGitRepo 校验 hook 仓库 URL（受理面，P0 防线）：repo 经 webhook
// 触发冻结进 GitSource 后由控制面以 root 原样送进 `git clone` 的 argv——
// `ext::sh -c ...`、`--upload-pack=` 等传输形态等价任意命令执行。因此只
// 接受 https:// 直连仓库：明文 http、git、ssh、file、本地路径与 ext 一律
// 拒绝；控制字符与空白逐字节拒绝（防 argv 拼接注入与日志走私）。
//
// 空值分支已退役 descriptor 注解（P4-1 批 1 Run B2）：SetGitHookRequest
// .repo 的 required 规则随字段原文上注解，空值由 protovalidate 拦截器在
// 受理面拦截（文案逐字节一致）——本函数只剩形态执法。
// validateGitRepo 是 grpcapi.ValidateGitRepo 的既名委托。
func validateGitRepo(repo string) error { return grpcapi.ValidateGitRepo(repo) }

// validateGitBranch 校验 hook 分支过滤（防线加深）：branch 会进
// `git clone --branch` 的参数位，控制字符/空白形态一律拒绝。空值合法
// （= 全部分支）。
func validateGitBranch(branch string) error {
	if branch == "" {
		return nil
	}
	for i := 0; i < len(branch); i++ {
		if c := rune(branch[i]); unicode.IsSpace(c) || unicode.IsControl(c) {
			return apperr.New("E_INVALID_ARGUMENT",
				"branch: must not contain whitespace or control characters (byte offset %d)", i)
		}
	}
	return nil
}
