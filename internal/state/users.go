package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// users 表读写（v0.3 W1，RBAC 设计 §2.1）：注册/认证/查询/禁用/口令重置
// 原语。email 小写归一在本层写入与查询通道统一做（UNIQUE 冲突 →
// ErrEmailTaken）；口令明文入参、argon2id PHC 串落库（password.go），投影
// 永不携带 password_hash（凭据不回读）。
//
// 首用户规则（设计 §2.1）：users 表为空时 CreateUser 强制
// is_platform_admin=1（首注册者 = 平台管理员，无用户窗口恒开的对偶），
// 调用方传入值被覆盖。
//
// 禁用联动（设计 §2.1/§10）：DisableUser 在同事务删除该用户全部会话行；
// 其 PAT 的拒认由 AuthenticateToken 的 users JOIN 判（disabled_at 非空 →
// ErrTokenInvalid 同码，不泄漏存在性）。
//
// 审计 actor 归一（设计 §6）：用户操作填 user:<id>（本包 auditActor），
// 空回落 human（种子/系统代写路径）。

// User 是一条用户行（只读投影；password_hash 不在投影内）。
type User struct {
	ID string
	// Email 是归一（小写）后的登录标识。
	Email string
	// DisplayName 是人读显示名（'' 缺省 = email 本地部分，建行时落定）。
	DisplayName string
	// IsPlatformAdmin 是平台管理员标志（用户标志非角色，设计 §3.2）。
	IsPlatformAdmin bool
	CreatedAt       time.Time
	// DisabledAt 零值 = 在册（未禁用）。
	DisabledAt time.Time
}

// user 相关哨兵错误。
var (
	// ErrUserNotFound 表示目标用户不存在。
	ErrUserNotFound = errors.New("user not found")
	// ErrEmailTaken 表示 email 已注册（小写归一后冲突）。
	ErrEmailTaken = errors.New("email already registered")
	// ErrInvalidCredentials 表示认证失败——email 不存在/口令不符/账号已
	// 禁用统一同码（不泄漏存在性与账号状态；api 层另有 email+IP 双键
	// 限流，设计 §2.1）。
	ErrInvalidCredentials = errors.New("invalid email or password")
)

// userCols 是用户行投影列清单（不含 password_hash）。
const userCols = `id, email, display_name, is_platform_admin, created_at, disabled_at`

// normalizeEmail 归一登录 email：trim + 小写（设计 §2.1）；空串或无 @ 视为
// 非法（格式细则校验在上层，本层只挡明显畸形）。
func normalizeEmail(email string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" || !strings.Contains(e, "@") {
		return "", fmt.Errorf("state: invalid email %q", email)
	}
	return e, nil
}

// auditActor 审计主体归一（设计 §6：用户操作 actor 填 user:<id>；空回落
// human——既有 actor 词表 human/ai_agent/system）。
func auditActor(userID string) string {
	if userID == "" {
		return "human"
	}
	return "user:" + userID
}

// UserWrite 是一次用户写入。
type UserWrite struct {
	// ID 留空自动生成 ULID。
	ID string
	// Email 小写归一在本层做（调用方可传任意大小写形态）。
	Email string
	// DisplayName 留空取 email 本地部分（设计 §2.1）。
	DisplayName string
	// Password 是明文口令（哈希后落库；明文不入库/审计/日志）。非空必填
	// （口令强度策略在上层）。
	Password string
	// IsPlatformAdmin 是建行时的平台管理员标志；users 表为空（首用户）时
	// 强制置 1（首注册者 = 平台管理员，设计 §2.1）。
	IsPlatformAdmin bool
	// ActorUserID 是发起写入的用户（审计 actor user:<id>；空 = human）。
	ActorUserID string
	// ActorTokenID 是发起写入的调用方 PAT（可空）。
	ActorTokenID string
}

// CreateUser 落一条用户行并与审计（user.created）同事务 fail-closed；
// email 冲突（小写归一后）返回 ErrEmailTaken。首用户强制平台管理员。
func (s *Store) CreateUser(ctx context.Context, w UserWrite) (User, error) {
	email, err := normalizeEmail(w.Email)
	if err != nil {
		return User{}, fmt.Errorf("state: create user: %w", err)
	}
	if strings.TrimSpace(w.Password) == "" {
		return User{}, fmt.Errorf("state: create user: password is empty")
	}
	hash, err := HashPassword(w.Password)
	if err != nil {
		return User{}, fmt.Errorf("state: create user: %w", err)
	}
	display := strings.TrimSpace(w.DisplayName)
	if display == "" {
		display = email[:strings.IndexByte(email, '@')]
	}
	id := w.ID
	if id == "" {
		id = ulid.Make().String()
	}
	adminFlag := 0
	if w.IsPlatformAdmin {
		adminFlag = 1
	}
	err = s.InTx(ctx, func(tx *Tx) error {
		// 首用户规则：users 空表 → 首注册者即平台管理员（设计 §2.1）。
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n); err != nil {
			return fmt.Errorf("state: count users: %w", err)
		}
		if n == 0 {
			adminFlag = 1
		}
		const q = `INSERT INTO users (id, email, password_hash, display_name, is_platform_admin, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, id, email, hash, display, adminFlag, nowNano()); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrEmailTaken, email)
			}
			return fmt.Errorf("state: insert user: %w", err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(w.ActorUserID),
			ActorTokenID: w.ActorTokenID,
			Action:       "user.created",
			Target:       "user:" + id,
			Result:       "ok",
			DiffSummary:  DiffSummary("email", email, "is_platform_admin", adminFlag == 1),
		})
	})
	if err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, id)
}

// AuthenticateUser 按 email + 口令认证（登录原语）：小写归一查行 →
// argon2id 常量时间校验（VerifyPassword）→ 禁用判断。任何失败（不存在/
// 口令错/已禁用）统一 ErrInvalidCredentials；email 查无此行时也执行一次
// 等价 argon2 派生（哑哈希），抹平「查无此号快返回」的时序差（防撞库
// 侧信道）。认证失败不入审计（登录/登录失败审计在认证流程面，设计 §6 W1）。
func (s *Store) AuthenticateUser(ctx context.Context, email, password string) (User, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return User{}, ErrInvalidCredentials
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+`, password_hash FROM users WHERE email = ?`, e)
	var u User
	var hash string
	if err := scanUserCols(row, &u, &hash); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			_, _ = VerifyPassword(password, dummyPasswordHash()) // 时序抹平（见上）
			return User{}, ErrInvalidCredentials
		}
		return User{}, err
	}
	ok, err := VerifyPassword(password, hash)
	if err != nil {
		return User{}, err
	}
	if !ok || !u.DisabledAt.IsZero() {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

var (
	dummyHashOnce  sync.Once
	dummyHashValue string
)

// dummyPasswordHash 是用户不存在路径的等价 argon2 校验材料（惰性派生一
// 次；非凭据——只为抹平时序差的哑串，参数与写侧一致）。
func dummyPasswordHash() string {
	dummyHashOnce.Do(func() {
		dummyHashValue, _ = HashPassword("fleetly timing equalizer")
	})
	return dummyHashValue
}

// GetUser 按 ID 取用户行；不存在返回 ErrUserNotFound。
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id)
	var u User
	if err := scanUserCols(row, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUserByEmail 按归一后 email 取用户行（注册窗口/登录预检用）；不存在
// 返回 ErrUserNotFound。
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return User{}, fmt.Errorf("state: get user by email: %w", err)
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, e)
	var u User
	if err := scanUserCols(row, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// ListUsers 返回全部用户（created_at 升序；平台管理员用户管理面读）。
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("state: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		var u User
		if err := scanUserCols(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate users: %w", err)
	}
	return out, nil
}

// SetPlatformAdmin 授予/撤销平台管理员标志（幂等：已是目标态 = 幂等成功；
// 不存在返回 ErrUserNotFound）。与审计同事务 fail-closed。
func (s *Store) SetPlatformAdmin(ctx context.Context, id string, admin bool, actorUserID, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		var q, action string
		if admin {
			q = `UPDATE users SET is_platform_admin = 1 WHERE id = ? AND is_platform_admin = 0`
			action = "user.platform_admin_granted"
		} else {
			q = `UPDATE users SET is_platform_admin = 0 WHERE id = ? AND is_platform_admin = 1`
			action = "user.platform_admin_revoked"
		}
		res, err := tx.ExecContext(ctx, q, id)
		if err != nil {
			return fmt.Errorf("state: set platform admin: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read platform admin update count: %w", err)
		}
		if n == 0 {
			// 区分「已是目标态（幂等成功）」与「不存在」。
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, id).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrUserNotFound
				}
				return fmt.Errorf("state: probe user: %w", err)
			}
			return nil
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       action,
			Target:       "user:" + id,
			Result:       "ok",
		})
	})
}

// DisableUser 禁用用户（幂等）：置 disabled_at，并在同事务删除其全部会话
// 行（设计 §2.1：禁用 = 会话联动吊销；PAT 拒认由 AuthenticateToken 的
// disabled_at JOIN 判承担——无行可删）。已禁用 = 幂等成功；不存在返回
// ErrUserNotFound。
func (s *Store) DisableUser(ctx context.Context, id, actorUserID, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET disabled_at = ? WHERE id = ? AND disabled_at IS NULL`, nowNano(), id)
		if err != nil {
			return fmt.Errorf("state: disable user: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read disable update count: %w", err)
		}
		if n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, id).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrUserNotFound
				}
				return fmt.Errorf("state: probe user: %w", err)
			}
			return nil // 已禁用：幂等成功
		}
		// 会话联动吊销（删行，设计 §2.2 注销语义）：禁用与吊销原子生效，
		// 不留「已禁用但会话仍有效」窗口。
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return fmt.Errorf("state: revoke sessions on disable: %w", err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "user.disabled",
			Target:       "user:" + id,
			Result:       "ok",
		})
	})
}

// EnableUser 解除禁用（幂等：disabled_at 置 NULL）；已解禁 = 幂等成功；
// 不存在返回 ErrUserNotFound。与审计同事务 fail-closed。
func (s *Store) EnableUser(ctx context.Context, id, actorUserID, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET disabled_at = NULL WHERE id = ? AND disabled_at IS NOT NULL`, id)
		if err != nil {
			return fmt.Errorf("state: enable user: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read enable update count: %w", err)
		}
		if n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, id).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrUserNotFound
				}
				return fmt.Errorf("state: probe user: %w", err)
			}
			return nil // 已解禁：幂等成功
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "user.enabled",
			Target:       "user:" + id,
			Result:       "ok",
		})
	})
}

// ResetPassword 重设用户口令（设计 §2.1：v0.3 无 SMTP，找回口令 = 平台
// 管理员重置）：重算 argon2id 哈希落库，明文与哈希不入审计。不存在返回
// ErrUserNotFound。不吊销既有会话（设计仅对 DisableUser 定义会话联动；
// 「口令重置后强制全端下线」留 W1 API 票裁决）。
func (s *Store) ResetPassword(ctx context.Context, id, newPassword, actorUserID, actorTokenID string) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("state: reset password: password is empty")
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("state: reset password: %w", err)
	}
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
		if err != nil {
			return fmt.Errorf("state: reset password: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read reset update count: %w", err)
		}
		if n == 0 {
			return ErrUserNotFound
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "user.password_reset",
			Target:       "user:" + id,
			Result:       "ok",
		})
	})
}

// scanUserCols 扫一行到 u（extra 追加列由调用方提供，如 password_hash）。
func scanUserCols(row interface{ Scan(dest ...any) error }, u *User, extra ...any) error {
	var admin int
	var created int64
	var disabled sql.NullInt64
	dest := []any{&u.ID, &u.Email, &u.DisplayName, &admin, &created, &disabled}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return fmt.Errorf("state: scan user: %w", err)
	}
	u.IsPlatformAdmin = admin != 0
	u.CreatedAt = time.Unix(0, created).UTC()
	if disabled.Valid {
		u.DisabledAt = time.Unix(0, disabled.Int64).UTC()
	}
	return nil
}
