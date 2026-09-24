package state

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// sessions 表读写（v0.3 W1，RBAC 设计 §2.2）：Console 浏览器会话原语。
// cookie 值 = 随机 32B（hex 64 字符），库存 sha256 hex（HashToken 同形态
// ——tokens/sessions/invites 三处凭据同一哈希口径）；明文值只在创建响应
// 一次性交调用方（Set-Cookie），绝不落库/进日志/事件/审计。
//
// 注销 = 删行（设计 §2.2）；「全部注销」= 删该用户全部行（RevokeUserSessions，
// 亦是 DisableUser 会话联动的底层原语）。
//
// last_seen_at 盖写节流（A2 同款，api/auth.go defaultTouchEvery）：60s 窗口
// 内重复认证不重复落写——节流收口在 UPDATE 的 WHERE 谓词里（原子判定、
// 进程内无状态，多连接并发安全）；会话行已消失时静默（尽力而为观测面）。
//
// 登录/注销审计（auth.login/auth.logout，设计 §6）属认证流程面（W1 api），
// 本层保持纯原语不写审计。

// Session 是一条会话行（只读投影；token_hash 不在投影内）。
type Session struct {
	ID string
	// UserID 是会话属主（会话生命周期与用户禁用联动，设计 §2.1/§2.2）。
	UserID    string
	CreatedAt time.Time
	// ExpiresAt 是绝对过期时刻（TTL/滑动窗口策略在调用面计算，设计 §2.2）。
	ExpiresAt time.Time
	// LastSeenAt 零值 = 从未使用。
	LastSeenAt time.Time
}

// session 相关哨兵错误。
var (
	// ErrSessionInvalid 表示凭据不匹配/属主用户已禁用/属主行缺失（认证
	// 拒绝路径；统一同码，不泄漏细节）。
	ErrSessionInvalid = errors.New("session invalid")
	// ErrSessionExpired 表示会话已过期（认证拒绝路径；清扫前的过期行）。
	ErrSessionExpired = errors.New("session expired")
	// ErrSessionNotFound 表示目标会话不存在。
	ErrSessionNotFound = errors.New("session not found")
)

// sessionTouchWindow 是 last_seen_at 盖写节流窗口（A2 同款 60s：1 次/分钟
// /会话的观测精度对「最近使用」语义足够，SQLite 写放大收口）。
const sessionTouchWindow = 60 * time.Second

// 会话 TTL 策略常量（v0.3 W2-S4，rbac-teams 设计 §2.2「7 天滑动 + 30 天
// 绝对上限」；滑动窗口时长可经 config auth.session_ttl_hours 调整，绝对上限
// 是硬封顶常量不可配置——可调 TTL 只能 ≤ 它生效）。
const (
	// DefaultSessionTTL 是滑动窗口缺省时长（config 缺省值；api 层可注入
	// auth.session_ttl_hours 覆盖）。
	DefaultSessionTTL = 7 * 24 * time.Hour
	// MaxSessionLifetime 是会话绝对寿命上限（自 created_at 起算；滑动续期
	// 在此处封顶——无限滑动 = 永不过期，与设计 §2.2 相悖）。
	MaxSessionLifetime = 30 * 24 * time.Hour
)

// randomTokenHex 生成随机 32B 的 hex 串（64 字符）——会话 cookie 值与
// 邀请链接 token 的统一原文形态（入库前经 HashToken 转 sha256 hex）。
func randomTokenHex() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("state: read random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateSession 落一条会话行，返回（行投影, 明文 token）——明文只出现
// 这一次（调用方写 Set-Cookie）。expires 由调用方传（设计 §2.2 的 7 天
// 滑动 + 30 天绝对上限在 api 层计算）。
func (s *Store) CreateSession(ctx context.Context, userID string, expiresAt time.Time) (Session, string, error) {
	if userID == "" {
		return Session{}, "", fmt.Errorf("state: create session: user id is empty")
	}
	if expiresAt.IsZero() {
		return Session{}, "", fmt.Errorf("state: create session: expires is zero")
	}
	token, err := randomTokenHex()
	if err != nil {
		return Session{}, "", err
	}
	id := ulid.Make().String()
	now := nowNano()
	const q = `INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, q, id, HashToken(token), userID, now, expiresAt.UnixNano()); err != nil {
		return Session{}, "", fmt.Errorf("state: insert session: %w", err)
	}
	return Session{
		ID:        id,
		UserID:    userID,
		CreatedAt: time.Unix(0, now).UTC(),
		ExpiresAt: expiresAt.UTC(),
	}, token, nil
}

// AuthenticateSession 按明文会话 token 认证：哈希查行（+常量时间二次比对，
// tokens 同款 belt-and-suspenders）→ 属主禁用判断（users JOIN；属主行缺失
// 一并拒认，fail-closed）→ 过期判断。不盖 last_seen_at（盖写走
// TouchSessionUsed 节流端口，调用方按 A2 形态接线）。
func (s *Store) AuthenticateSession(ctx context.Context, plaintext string) (Session, error) {
	hash := HashToken(plaintext)
	row := s.db.QueryRowContext(ctx, `SELECT s.id, s.user_id, s.created_at, s.expires_at, s.last_seen_at, s.token_hash, u.id, u.disabled_at
		FROM sessions s LEFT JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, hash)
	var (
		sess         Session
		created      int64
		expires      int64
		lastSeen     sql.NullInt64
		storedHash   string
		ownerID      sql.NullString
		userDisabled sql.NullInt64
	)
	if err := row.Scan(&sess.ID, &sess.UserID, &created, &expires, &lastSeen, &storedHash, &ownerID, &userDisabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionInvalid
		}
		return Session{}, fmt.Errorf("state: scan session: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hash)) != 1 {
		return Session{}, ErrSessionInvalid
	}
	if !ownerID.Valid {
		// 属主用户行缺失（理论不可达：本包无用户物理删除原语）一并拒认
		// ——fail-closed：无法证明属主在册即拒绝。注意探针必须用属主行
		// 本身（u.id）：disabled_at IS NULL 是在册健康态，不是行缺失。
		return Session{}, ErrSessionInvalid
	}
	if time.Unix(0, expires).UTC().Before(time.Now().UTC()) {
		return Session{}, ErrSessionExpired
	}
	sess.CreatedAt = time.Unix(0, created).UTC()
	sess.ExpiresAt = time.Unix(0, expires).UTC()
	if lastSeen.Valid {
		sess.LastSeenAt = time.Unix(0, lastSeen.Int64).UTC()
	}
	return sess, nil
}

// TouchSessionUsed 节流盖写 last_seen_at（A2 同款 60s 窗口）：从未写过或
// 距上次盖写超过窗口才落写，判定在 UPDATE WHERE 谓词内原子完成；窗口内
// 重复调用不产生写（观察误差 ≤ 一个窗口，与 api/auth.go 同口径）。会话行
// 已消失时静默（尽力而为观测面）。
func (s *Store) TouchSessionUsed(ctx context.Context, id string) error {
	cutoff := time.Now().UTC().Add(-sessionTouchWindow).UnixNano()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)`,
		nowNano(), id, cutoff); err != nil {
		return fmt.Errorf("state: touch session used: %w", err)
	}
	return nil
}

// SlideSession 滑动续期（v0.3 W2-S4，设计 §2.2「7 天滑动 + 30 天绝对上限」
// 的执行原语）：盖写 last_seen_at 的同一拍把 expires_at 延长到
// min(last_seen + ttl, created_at + MaxSessionLifetime)——滑动窗口 TTL 由
// 调用方注入（config auth.session_ttl_hours；非正值回落 DefaultSessionTTL），
// 绝对上限在本层封顶。节流判定与 TouchSessionUsed 同款 60s 窗口（UPDATE
// WHERE 谓词内原子完成——窗口内重复调用零写；会话行已消失静默）。
func (s *Store) SlideSession(ctx context.Context, id string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	cutoff := time.Now().UTC().Add(-sessionTouchWindow).UnixNano()
	// expires 上界的两参形态（SQLite MIN 标量函数多参）：滑动目标与绝对
	// 寿命封顶取小——created_at 为基的绝对上界在每次续期拍重算，语义恒等。
	q := `UPDATE sessions SET
		last_seen_at = ?,
		expires_at = MIN(? + ?, created_at + ?)
		WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)`
	if _, err := s.db.ExecContext(ctx, q, nowNano(), nowNano(), int64(ttl), int64(MaxSessionLifetime), id, cutoff); err != nil {
		return fmt.Errorf("state: slide session: %w", err)
	}
	return nil
}

// RevokeSession 删除一条会话行（注销 = 删行，设计 §2.2）；不存在返回
// ErrSessionNotFound。
func (s *Store) RevokeSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("state: revoke session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("state: read revoke count: %w", err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeUserSessions 删除该用户全部会话行（「全部注销」原语；DisableUser
// 的会话联动同款），返回删除行数。
func (s *Store) RevokeUserSessions(ctx context.Context, userID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, fmt.Errorf("state: revoke user sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read revoke count: %w", err)
	}
	return n, nil
}

// revokeUserSessionsTx 是 RevokeUserSessions 的事务内形态（ResetPassword
// 的「重置即全端下线」联动在口令写事务内调用——原子生效，无中间窗口）。
func revokeUserSessionsTx(ctx context.Context, tx *Tx, userID string) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, fmt.Errorf("state: revoke user sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read revoke count: %w", err)
	}
	return n, nil
}

// SweepExpiredSessions 删除过期会话行（janitor 挂接；认证路径本就拒认
// 过期行——清扫只回收表空间），返回删除行数。now 注入（janitor PruneOnce
// 同口径，测试可驱动）。
func (s *Store) SweepExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("state: sweep expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read sweep count: %w", err)
	}
	return n, nil
}
