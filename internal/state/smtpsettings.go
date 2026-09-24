package state

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// notify.smtp.* 运行期设置（observability 设计 §8.3，D-W4-4，W4-S3）：
// platform_settings 库内设置，s3settings/authsettings 同型——平台级一份
// SMTP 配置，全部 email 端点共用（通道设置与端点解耦：email 端点只存
// target 收件地址，投递凭据在平台设置面）。
//
// password 存 envelope 密文（internal/secrets）——**state 层存密文不解释**
// ：加密边界在调用方（internal/api），与 s3.secret_access_key 同型纪律；
// 密码明文绝不进日志/事件/审计/错误（state-model §2.9，负面测试钉死）。
// 读面只出密文（调用方解出指纹），永不回明文。
//
// 变更路径（§8.3）：UpdateSmtpSettings → 校验 + 审计同事务 fail-closed；
// 审计 action = notify.smtp_changed（authsettings 审计形态：只落审计**不
// 落事件**——§5.2 红线的精神延伸：通知配置面自身零事件）。

// notify.smtp.* 设置键词表（只增；键名常量为本包唯一登记点）。
const (
	NotifySmtpKeyHost     = "notify.smtp.host"
	NotifySmtpKeyPort     = "notify.smtp.port"
	NotifySmtpKeyUsername = "notify.smtp.username"
	NotifySmtpKeyPassword = "notify.smtp.password" //nolint:gosec // G101：设置键名字面量，非凭据材料
	NotifySmtpKeyFrom     = "notify.smtp.from"
)

// smtpSettingsKeys 是保存时全量落库的键清单（PUT 语义：每次保存写全五键，
// 未提供的键回落空值——避免「改了 host 残留旧密码」的静默状态，s3 同款）。
var smtpSettingsKeys = []string{
	NotifySmtpKeyHost, NotifySmtpKeyPort, NotifySmtpKeyUsername,
	NotifySmtpKeyPassword, NotifySmtpKeyFrom,
}

// SmtpSettings 是 notify.smtp.* 设置的 typed 视图。PasswordCipher 为**存储
// 形态**（envelope 密文，由调用方加密/解密）；空串 = 未设置。
type SmtpSettings struct {
	Host           string
	Port           int
	Username       string
	PasswordCipher string // 密文（存储形态）；空 = 未设置
	From           string
	// UpdatedAt 是各设置行 updated_at 的最大值（只读投影）。
	UpdatedAt time.Time
}

// ValidateSmtpSettings 是保存前校验（fail-fast）：host 必填；port
// 1..65535；from 必须是裸邮箱地址（email 端点 target 同一词表）；username
// 可选（无认证中继合法）。PasswordCipher 不在本层解释（密文形态原样落库
// ——空串合法：无密码中继/只测连通）。
func ValidateSmtpSettings(in SmtpSettings) error {
	if strings.TrimSpace(in.Host) == "" {
		return fmt.Errorf("state: notify.smtp.host is required")
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("state: notify.smtp.port %d out of range (1..65535)", in.Port)
	}
	if err := ValidateWebhookEmailTarget(in.From); err != nil {
		return fmt.Errorf("state: notify.smtp.from invalid: %w", err)
	}
	return nil
}

// LoadSmtpSettings 读取全部 notify.smtp.* 设置（读取点每次现读，不缓存长
// 驻——保存即对下一次投递生效）。空库/无行 → 全空缺省态。存储值畸形（端口
// 非整数）显式报错：设置损坏 loud-fail，不静默回落。
func (s *Store) LoadSmtpSettings(ctx context.Context) (SmtpSettings, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value, updated_at FROM platform_settings WHERE key LIKE 'notify.smtp.%'`)
	if err != nil {
		return SmtpSettings{}, fmt.Errorf("state: query smtp settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	vals := map[string]string{}
	var latest int64
	for rows.Next() {
		var key, value string
		var updatedAt int64
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			return SmtpSettings{}, fmt.Errorf("state: scan smtp setting: %w", err)
		}
		vals[key] = value
		if updatedAt > latest {
			latest = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return SmtpSettings{}, fmt.Errorf("state: iterate smtp settings: %w", err)
	}
	out := SmtpSettings{
		Host:           vals[NotifySmtpKeyHost],
		Username:       vals[NotifySmtpKeyUsername],
		PasswordCipher: vals[NotifySmtpKeyPassword], // 密文原样（本层不解释）
		From:           vals[NotifySmtpKeyFrom],
	}
	if raw := vals[NotifySmtpKeyPort]; raw != "" {
		port, perr := strconv.Atoi(raw)
		if perr != nil {
			return SmtpSettings{}, fmt.Errorf("state: setting %s has invalid integer value %q", NotifySmtpKeyPort, raw)
		}
		out.Port = port
	}
	out.UpdatedAt = time.Unix(0, latest).UTC()
	return out, nil
}

// SmtpSaveOptions 是保存的上下文选项（actor 进审计，authsettings 同型——
// api 面传调用者 token 身份；actor 恒 "human" 词表不扩）。
type SmtpSaveOptions struct {
	Actor        string
	ActorTokenID string
}

// SaveSmtpSettings 全量保存 notify.smtp.* 设置（PUT 语义）+ 审计（同一事
// 务，fail-closed）。PasswordCipher 必须已由调用方 envelope 加密（密文入参
// ——本层不解释密文，也绝不把任何设置值拼进错误/审计：审计 diff 只带
// host/port/from 与用户名/密码的「是否设置」布尔，凭证材料零出现）。不落
// 事件（§8.3：通知配置面自身零事件——设计红线延伸）。
func (s *Store) SaveSmtpSettings(ctx context.Context, in SmtpSettings, opts SmtpSaveOptions) error {
	if err := ValidateSmtpSettings(in); err != nil {
		return err
	}
	values := map[string]string{
		NotifySmtpKeyHost:     strings.TrimSpace(in.Host),
		NotifySmtpKeyPort:     strconv.Itoa(in.Port),
		NotifySmtpKeyUsername: in.Username,
		NotifySmtpKeyPassword: in.PasswordCipher,
		NotifySmtpKeyFrom:     in.From,
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		for _, key := range smtpSettingsKeys {
			const q = `INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
			if _, err := tx.ExecContext(ctx, q, key, values[key], now); err != nil {
				return fmt.Errorf("state: upsert smtp setting %s: %w", key, err)
			}
		}
		// 审计（§8.3）：action = notify.smtp_changed；diff 摘要只含非凭据
		// 事实（host/port/from + 用户名/密码是否设置的布尔）——密文/明文零出现。
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        opts.Actor,
			ActorTokenID: opts.ActorTokenID,
			Action:       "notify.smtp_changed",
			Target:       "platform:smtp",
			Result:       "ok",
			DiffSummary: DiffSummary(
				"host", values[NotifySmtpKeyHost],
				"port", in.Port,
				"from", in.From,
				"username_set", in.Username != "",
				"password_set", in.PasswordCipher != ""),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("state: save smtp settings: %w", err)
	}
	return nil
}
