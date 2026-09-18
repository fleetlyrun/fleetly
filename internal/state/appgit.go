package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// apps 表 git 触发配置列（T2.19 迁移 00007 加法列）读写：
//   - git_branch        —— push 触发分支（默认 main；webhook 分支过滤共用）；
//   - webhook_secret    —— HMAC-SHA256 签名密钥，envelope 密文形态（NULL =
//                          未配置即未启用）；明文不落库、永不回读；
//   - source_url        —— webhook 拉源 remote URL；
//   - source_auth_kind  —— none | https_token | ssh_key（词表本层承载，
//                          迁移无 CHECK，00005 同款口径）；
//   - source_auth_secret—— 认证材料 envelope 密文（'' = 无）。
//
// 读面投影（AppGitConfig）不回读 secret 明文形态——密文列只在 daemon 侧
// webhook 拉源路径解密消费（api show 面只回 configured 位）。

// SourceAuthKind 是 webhook 拉源认证形态词表。
type SourceAuthKind string

const (
	// SourceAuthNone 匿名拉取（file:// / 公开 https 仓库）。
	SourceAuthNone SourceAuthKind = "none"
	// SourceAuthToken HTTPS + token（http.extraHeader 形态注入）。
	SourceAuthToken SourceAuthKind = "https_token"
	// SourceAuthSSHKey SSH 私钥（GIT_SSH_COMMAND 临时密钥文件注入）。
	SourceAuthSSHKey SourceAuthKind = "ssh_key"
)

// Valid 报告认证形态是否为已定义词。
func (k SourceAuthKind) Valid() bool {
	switch k {
	case SourceAuthNone, SourceAuthToken, SourceAuthSSHKey:
		return true
	}
	return false
}

// DefaultGitBranch 是 app 配置分支的缺省值（push 与 webhook 共用）。
const DefaultGitBranch = "main"

// AppGitConfig 是 app git 触发配置投影（webhook_secret 携带密文形态，
// daemon 侧解密消费；api 读面只用 SecretSet）。
type AppGitConfig struct {
	Branch string
	// SecretSet 报告 webhook 签名密钥已配置。
	SecretSet bool
	// WebhookSecret 是密文形态（SecretSet=false 时为空）。
	WebhookSecret string
	SourceURL     string
	AuthKind      SourceAuthKind
	// AuthSecret 是密文形态（'' = 无认证材料）。
	AuthSecret string
}

// ErrAppNotFound 沿用 apps.go 哨兵（配置读面不发明新错误族）——GetAppGit
// Config 不存在时返回它。

// GetAppGitConfig 读 app git 触发配置（appID 必须存在——不存在返回
// ErrAppNotFound）。
func (s *Store) GetAppGitConfig(ctx context.Context, appID string) (AppGitConfig, error) {
	const q = `SELECT git_branch, webhook_secret, source_url, source_auth_kind, source_auth_secret
		FROM apps WHERE id = ?`
	var c AppGitConfig
	var branch string
	var secret sql.NullString
	var url, kind, authSecret string
	err := s.db.QueryRowContext(ctx, q, appID).Scan(&branch, &secret, &url, &kind, &authSecret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AppGitConfig{}, ErrAppNotFound
		}
		return AppGitConfig{}, fmt.Errorf("state: read app git config: %w", err)
	}
	c.Branch = branch
	if c.Branch == "" {
		c.Branch = DefaultGitBranch
	}
	c.SecretSet = secret.Valid && secret.String != ""
	c.WebhookSecret = secret.String
	c.SourceURL = url
	c.AuthKind = SourceAuthKind(kind)
	if !c.AuthKind.Valid() {
		c.AuthKind = SourceAuthNone
	}
	c.AuthSecret = authSecret
	return c, nil
}

// SetAppWebhookSecret 设置/清除 webhook 签名密钥（ciphertext 为 envelope
// 密文；空串 = 清除，端点回到未启用语义）。与审计（app.webhook_secret_set）
// 同事务 fail-closed；动作可能是清除——diff 如实记录 set/clear。
func (s *Store) SetAppWebhookSecret(ctx context.Context, appID, ciphertext, actorTokenID string) error {
	action := "set"
	if ciphertext == "" {
		action = "clear"
	}
	return s.InTx(ctx, func(tx *Tx) error {
		if _, err := appExists(ctx, tx, appID); err != nil {
			return err
		}
		now := nowNano()
		if ciphertext == "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE apps SET webhook_secret = NULL, updated_at = ? WHERE id = ?`, now, appID); err != nil {
				return fmt.Errorf("state: set app webhook secret: %w", err)
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`UPDATE apps SET webhook_secret = ?, updated_at = ? WHERE id = ?`, ciphertext, now, appID); err != nil {
				return fmt.Errorf("state: set app webhook secret: %w", err)
			}
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        "human",
			ActorTokenID: actorTokenID,
			Action:       "app.webhook_secret_set",
			Target:       "app:" + appID,
			Result:       "ok",
			DiffSummary:  `{"action":"` + action + `"}`,
		})
	})
}

// AppSourceWrite 是一次拉源配置写入（ciphertext 密文形态入参）。
type AppSourceWrite struct {
	// URL 为空 = 清除拉源配置（auth 字段一并清空）。
	URL        string
	Branch     string
	AuthKind   SourceAuthKind
	AuthSecret string // envelope 密文
	// ActorTokenID 审计调用方 token。
	ActorTokenID string
}

// SetAppSource 写入拉源配置（branch 同步承载 push 触发分支语义）。与审计
// （app.source_set）同事务 fail-closed。
func (s *Store) SetAppSource(ctx context.Context, appID string, w AppSourceWrite) error {
	if w.URL != "" {
		if w.Branch == "" {
			w.Branch = DefaultGitBranch
		}
		if !w.AuthKind.Valid() {
			return fmt.Errorf("state: set app source: invalid auth kind %q", w.AuthKind)
		}
	}
	kind := string(w.AuthKind)
	if w.URL == "" {
		kind = string(SourceAuthNone)
	}
	diff := `{"url":"` + w.URL + `","branch":"` + w.Branch + `","auth_kind":"` + kind + `"}`
	return s.InTx(ctx, func(tx *Tx) error {
		if _, err := appExists(ctx, tx, appID); err != nil {
			return err
		}
		const q = `UPDATE apps SET source_url = ?, git_branch = ?, source_auth_kind = ?, source_auth_secret = ?, updated_at = ?
			WHERE id = ?`
		if _, err := tx.ExecContext(ctx, q, w.URL, w.Branch, kind, w.AuthSecret, nowNano(), appID); err != nil {
			return fmt.Errorf("state: set app source: %w", err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        "human",
			ActorTokenID: w.ActorTokenID,
			Action:       "app.source_set",
			Target:       "app:" + appID,
			Result:       "ok",
			DiffSummary:  diff,
		})
	})
}

// appExists 事务内探活 app 行（配置写的 FK 语义防线——迁移无外键新增，
// 存在性由本谓词承载）；不存在返回 ErrAppNotFound。
func appExists(ctx context.Context, tx *Tx, appID string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM apps WHERE id = ?`, appID).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrAppNotFound
		}
		return false, fmt.Errorf("state: probe app: %w", err)
	}
	return true, nil
}
