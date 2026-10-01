package fleetlygrpc

// Hooks 上下文服务实现（F0.13）：per-App Git 触发配置面（Set/Get/Rotate，
// apps 读写 Scope）与 GitHub push 接收面（ReceiveWebhook，PUBLIC——HMAC
// 签名即凭证；接收链在 webhook.go）。
//
// Token 材料合一：URL token 与 GitHub webhook secret 是同一串。存储侧
// sha256（URL 查找键）+ age 信封（验签需原串）；明文只在铸造/再生成响应
// 出现一次；rotate 双换、旧串即刻失效。

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/hook"
)

// HooksURLPrefix 是接收面 REST 入口前缀（gateway 原生 handler 挂载点；
// CLI 展示 URL 时拼接）。
const HooksURLPrefix = "/v1/hooks/"

type HooksService struct {
	deliveryv1.UnimplementedHooksServiceServer
	s *Services
}

// SetGitHook 建/改配置：无既有行时铸造 Token（明文仅本次响应返回），
// 既有行只改配置字段（换 Token 走 RotateHookToken）。
func (svc *HooksService) SetGitHook(ctx context.Context, req *deliveryv1.SetGitHookRequest) (*deliveryv1.SetGitHookResponse, error) {
	if req.GetAppId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	if strings.TrimSpace(req.GetRepo()) == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "repo: must not be empty (the git URL builds are checked out from)")
	}
	appRow, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}

	existing, err := svc.s.Hooks.Get(ctx, svc.s.DB.Runner(), appRow.ID)
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return nil, mapStateError(err, "hook")
	}

	var secret string
	if errors.Is(err, state.ErrNotFound) {
		material, merr := identity.NewHookToken()
		if merr != nil {
			return nil, apperr.New("E_INTERNAL", "hook token generation failed")
		}
		ciphertext, serr := svc.s.Cipher.Seal([]byte(material.Secret))
		if serr != nil {
			return nil, apperr.New("E_SECRET_UNAVAILABLE", "sealing the hook secret failed: %v", serr)
		}
		row := &hook.Hook{
			AppID: appRow.ID, Repo: strings.TrimSpace(req.GetRepo()),
			Branch: normalizeBranch(req.GetBranch()), Dockerfile: normalizeDockerfile(req.GetDockerfile()),
			WatchPaths:  normalizeWatchPaths(req.GetWatchPaths()),
			TokenSHA256: material.SHA256, TokenPrefix: material.Prefix, SecretCiphertext: ciphertext,
		}
		txErr := svc.s.commit(ctx, writeFact{
			write: func(ctx context.Context, tx *sql.Tx) error {
				return svc.s.Hooks.Create(ctx, tx, row)
			},
			audits: []*audit.Entry{{
				ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				Action: "hook.create", Resource: "app/" + appRow.ID, AfterFP: row.Repo + "@" + row.Branch,
			}},
		})
		if txErr != nil {
			return nil, mapStateError(txErr, "hook")
		}
		secret = material.Secret
		return &deliveryv1.SetGitHookResponse{Hook: hookMsg(row), Secret: secret}, nil
	}

	existing.Repo = strings.TrimSpace(req.GetRepo())
	existing.Branch = normalizeBranch(req.GetBranch())
	existing.Dockerfile = normalizeDockerfile(req.GetDockerfile())
	existing.WatchPaths = normalizeWatchPaths(req.GetWatchPaths())
	txErr := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Hooks.UpdateConfig(ctx, tx, existing)
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "hook.update", Resource: "app/" + appRow.ID, AfterFP: existing.Repo + "@" + existing.Branch,
		}},
	})
	if txErr != nil {
		return nil, mapStateError(txErr, "hook")
	}
	return &deliveryv1.SetGitHookResponse{Hook: hookMsg(existing)}, nil
}

func (svc *HooksService) GetGitHook(ctx context.Context, req *deliveryv1.GetGitHookRequest) (*deliveryv1.GetGitHookResponse, error) {
	row, err := svc.s.Hooks.Get(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "hook")
	}
	return &deliveryv1.GetGitHookResponse{Hook: hookMsg(row)}, nil
}

// RotateHookToken 吊销旧串铸新（URL token 与 GitHub webhook secret 同源
// 双换；GitHub 侧需同步更新 webhook secret）。
func (svc *HooksService) RotateHookToken(ctx context.Context, req *deliveryv1.RotateHookTokenRequest) (*deliveryv1.RotateHookTokenResponse, error) {
	if _, err := svc.s.Hooks.Get(ctx, svc.s.DB.Runner(), req.GetAppId()); err != nil {
		return nil, mapStateError(err, "hook")
	}
	material, err := identity.NewHookToken()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "hook token generation failed")
	}
	ciphertext, err := svc.s.Cipher.Seal([]byte(material.Secret))
	if err != nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "sealing the hook secret failed: %v", err)
	}
	txErr := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Hooks.RotateToken(ctx, tx, req.GetAppId(), material.SHA256, material.Prefix, ciphertext)
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "hook.rotate", Resource: "app/" + req.GetAppId(),
		}},
	})
	if txErr != nil {
		return nil, mapStateError(txErr, "hook")
	}
	row, err := svc.s.Hooks.Get(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "hook")
	}
	return &deliveryv1.RotateHookTokenResponse{Hook: hookMsg(row), Secret: material.Secret}, nil
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
