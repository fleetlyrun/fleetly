package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// auth.* 运行期设置（v0.3 W1，RBAC 设计 §2.1/§2.2/§10）：platform_settings
// 库内设置，logsettings/metricssettings 同型（读取点每次现读不缓存长驻；
// 保存 = 校验 + 审计同事务 fail-closed）。
//
// 缺省语义（设计 §2.1/§10：注册缺省关 + 无用户窗口恒开）：auth.registration
// 未显式设置 = 缺省 `closed`——users 表为空的恒开判定不在本层（调用方先查
// HasAnyUser；RegisterUser 在注册事务内原子判窗），本层只诚实投影「未设置
// = closed 缺省生效」。保存动作落审计 auth.registration_changed（设计 §6
// 动作清单）；不落事件（设计 §6 事件面注册态仅 user.registered /
// team.created / project.created 三类）。

// AuthKeyRegistration 是注册开关设置键（词表只增；键名常量为本包唯一登记点）。
const AuthKeyRegistration = "auth.registration"

// auth.registration 词表（设计 §2.1：open | closed，缺省 closed）。
const (
	AuthRegistrationOpen   = "open"
	AuthRegistrationClosed = "closed"
)

// AuthSettings 是 auth.* 设置的 typed 视图。Registration 恒为生效形态
//（未设置时 = 缺省 closed）；Set 报告是否显式设置过。
type AuthSettings struct {
	// Registration 是生效的注册开关（open | closed）。
	Registration string
	// Set 报告该键是否被显式保存过（false = 缺省态生效）。
	Set bool
	// UpdatedAt 是设置行 updated_at（只读投影；未设置 = 零值）。
	UpdatedAt time.Time
}

// ValidateRegistrationMode 校验注册开关值域（保存与读侧共用；非法值显式
// 拒绝——设置面 loud-fail，不静默回落）。
func ValidateRegistrationMode(mode string) error {
	switch mode {
	case AuthRegistrationOpen, AuthRegistrationClosed:
		return nil
	default:
		return fmt.Errorf("state: auth.registration %q not in {open, closed}", mode)
	}
}

// authSettingQueryer 是设置读取的最小查询面（*sql.DB 与事务内 *sql.Tx 均满足
// ——注册事务内的原子判窗与 api 面现读共用同一投影逻辑）。
type authSettingQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// loadAuthSettingsFrom 从给定查询面读 auth.registration（读取点现读共用形）。
func loadAuthSettingsFrom(ctx context.Context, q authSettingQueryer) (AuthSettings, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT value, updated_at FROM platform_settings WHERE key = ?`, AuthKeyRegistration)
	if err != nil {
		return AuthSettings{}, fmt.Errorf("state: query auth settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := AuthSettings{Registration: AuthRegistrationClosed}
	for rows.Next() {
		var value string
		var updatedAt int64
		if err := rows.Scan(&value, &updatedAt); err != nil {
			return AuthSettings{}, fmt.Errorf("state: scan auth setting: %w", err)
		}
		if err := ValidateRegistrationMode(value); err != nil {
			return AuthSettings{}, fmt.Errorf("state: stored auth setting invalid: %w", err)
		}
		out.Registration = value
		out.Set = true
		out.UpdatedAt = time.Unix(0, updatedAt).UTC()
	}
	if err := rows.Err(); err != nil {
		return AuthSettings{}, fmt.Errorf("state: iterate auth settings: %w", err)
	}
	return out, nil
}

// LoadAuthSettings 读取 auth.* 设置（每次现读）。空库/无行 → 缺省态
//（Registration=closed，Set=false）。存储值畸形显式报错：设置损坏
// loud-fail，不静默回落缺省（与 logs.backend 同口径）。
func (s *Store) LoadAuthSettings(ctx context.Context) (AuthSettings, error) {
	return loadAuthSettingsFrom(ctx, s.db)
}

// AuthSaveOptions 是保存的上下文选项（actor 进审计，logs 同型——api 面
// 传 user:<id> 或 human）。
type AuthSaveOptions struct {
	Actor        string
	ActorTokenID string
}

// SaveRegistration 保存 auth.registration + 审计 auth.registration_changed
//（同一事务，fail-closed；设计 §6 动作注册表。不落事件——事件面注册态
// 只有三类，见包注释）。payload/审计 diff 只带开关值（词表内枚举，无
// 敏感材料）。
func (s *Store) SaveRegistration(ctx context.Context, mode string, opts AuthSaveOptions) error {
	if err := ValidateRegistrationMode(mode); err != nil {
		return err
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		const q = `INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
		if _, err := tx.ExecContext(ctx, q, AuthKeyRegistration, mode, nowNano()); err != nil {
			return fmt.Errorf("state: upsert auth setting %s: %w", AuthKeyRegistration, err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        opts.Actor,
			ActorTokenID: opts.ActorTokenID,
			Action:       "auth.registration_changed",
			Target:       "platform:auth",
			Result:       "ok",
			DiffSummary:  DiffSummary("registration", mode),
		})
	})
	if err != nil {
		return fmt.Errorf("state: save auth settings: %w", err)
	}
	return nil
}
