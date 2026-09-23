package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSessionLifecycle（RBAC 设计 §2.2）：创建（明文只返回一次、库存哈希、
// 明文不落库）→ 认证 → 吊销（删行）→ 单用户全吊销。
func TestSessionLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, UserWrite{Email: "web@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	sess, plaintext, err := st.CreateSession(ctx, u.ID, expires)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID == "" || sess.UserID != u.ID {
		t.Fatalf("unexpected session row: %+v", sess)
	}
	if plaintext == "" || len(plaintext) != 64 {
		t.Fatalf("plaintext token = %q, want 64-char hex (32B random)", plaintext)
	}
	if !sess.LastSeenAt.IsZero() {
		t.Fatal("fresh session LastUsedAt must be zero (never used)")
	}
	// 哈希存储断言：库内不落明文，落的是 sha256 hex。
	var stored string
	if err := st.db.QueryRowContext(ctx, `SELECT token_hash FROM sessions WHERE id = ?`, sess.ID).Scan(&stored); err != nil {
		t.Fatalf("scan stored session: %v", err)
	}
	if strings.Contains(stored, plaintext) || stored != HashToken(plaintext) {
		t.Fatalf("session storage must hold sha256(plaintext), got %q", stored)
	}

	got, err := st.AuthenticateSession(ctx, plaintext)
	if err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	if got.ID != sess.ID || got.UserID != u.ID {
		t.Fatalf("authenticated session = %+v, want id %s", got, sess.ID)
	}

	// 错误凭据 → ErrSessionInvalid（同码，不泄漏细节）。
	if _, err := st.AuthenticateSession(ctx, "not-a-session-token"); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("wrong token err = %v, want ErrSessionInvalid", err)
	}

	// 全吊销（「全部注销」原语）。
	n, err := st.RevokeUserSessions(ctx, u.ID)
	if err != nil || n != 1 {
		t.Fatalf("RevokeUserSessions: n=%d err=%v, want 1", n, err)
	}
	if _, err := st.AuthenticateSession(ctx, plaintext); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("auth after revoke-all err = %v, want ErrSessionInvalid", err)
	}

	// 单行吊销：存在 → 删；不存在 → ErrSessionNotFound。
	s2, plain2, err := st.CreateSession(ctx, u.ID, expires)
	if err != nil {
		t.Fatalf("CreateSession second: %v", err)
	}
	if err := st.RevokeSession(ctx, s2.ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, plain2); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("revoked session must not authenticate")
	}
	if err := st.RevokeSession(ctx, s2.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("revoke missing err = %v, want ErrSessionNotFound", err)
	}

	// 写入防御：空 user / 零 expires 拒绝。
	if _, _, err := st.CreateSession(ctx, "", expires); err == nil {
		t.Fatal("empty user id must be rejected")
	}
	if _, _, err := st.CreateSession(ctx, u.ID, time.Time{}); err == nil {
		t.Fatal("zero expires must be rejected")
	}
}

// TestSessionExpiry（RBAC 设计 §2.2）：过期会话认证拒绝（ErrSessionExpired
// ——与查无此 token 可区分，api 面可据此发新 cookie 前清理）；janitor 清扫
// 挂接：SweepExpiredSessions 只删过期行。
func TestSessionExpiry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, UserWrite{Email: "ttl@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// 已过期会话（expires 由调用方传——直接传过去时刻，不需要 sleep）。
	expired, plainExpired, err := st.CreateSession(ctx, u.ID, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("CreateSession expired: %v", err)
	}
	live, _, err := st.CreateSession(ctx, u.ID, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession live: %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, plainExpired); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired session err = %v, want ErrSessionExpired", err)
	}

	// 清扫：只删过期行，返回删除数；重复清扫 0。
	n, err := st.SweepExpiredSessions(ctx, time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("SweepExpiredSessions: n=%d err=%v, want 1", n, err)
	}
	var cnt int64
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sessions WHERE id IN (?, ?)`, expired.ID, live.ID).Scan(&cnt); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("live session must survive sweep, rows = %d", cnt)
	}
	if n, err := st.SweepExpiredSessions(ctx, time.Now().UTC()); err != nil || n != 0 {
		t.Fatalf("second sweep: n=%d err=%v, want 0", n, err)
	}
}

// TestTouchSessionUsedThrottle（A2 同款节流盖写，api/auth.go 60s 窗口）：
// 首次盖写落值；窗口内重复调用不产生写（last_seen 精确不变）；last_seen
// 回拨出窗口后恢复盖写。节流窗口经回拨 last_seen 验证（store 层无时钟
// 注入先例，不为测试重构基建——直接构造窗口外的存量值）。
func TestTouchSessionUsedThrottle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, UserWrite{Email: "touch@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sess, _, err := st.CreateSession(ctx, u.ID, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 认证不盖 last_seen（纯认证语义，A2：盖写走节流端口）。
	if _, err := st.AuthenticateSession(ctx, func() string { _, p, _ := st.CreateSession(ctx, u.ID, time.Now().UTC().Add(time.Hour)); return p }()); err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	firstSeen := lastSeenOf(t, st, ctx, sess.ID)
	if !firstSeen.IsZero() {
		t.Fatal("AuthenticateSession must not touch last_seen_at")
	}

	if err := st.TouchSessionUsed(ctx, sess.ID); err != nil {
		t.Fatalf("TouchSessionUsed: %v", err)
	}
	v1 := lastSeenOf(t, st, ctx, sess.ID)
	if v1.IsZero() {
		t.Fatal("first touch must set last_seen_at")
	}

	// 窗口内（60s）第二次调用：不产生写——last_seen 精确等于 v1。
	if err := st.TouchSessionUsed(ctx, sess.ID); err != nil {
		t.Fatalf("second touch: %v", err)
	}
	if got := lastSeenOf(t, st, ctx, sess.ID); !got.Equal(v1) {
		t.Fatalf("second touch rewrote last_seen_at: %v != %v (60s window must suppress the write)", got, v1)
	}

	// 回拨 last_seen 出窗口（模拟时间流逝），恢复盖写。断言相对回拨值
	// （新写值必然晚于 2 分钟前的回拨值——墙钟刻度对齐不构成误判）。
	backdate := time.Now().UTC().Add(-2 * time.Minute)
	if _, err := st.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, backdate.UnixNano(), sess.ID); err != nil {
		t.Fatalf("backdate last_seen: %v", err)
	}
	if err := st.TouchSessionUsed(ctx, sess.ID); err != nil {
		t.Fatalf("touch after window: %v", err)
	}
	v2 := lastSeenOf(t, st, ctx, sess.ID)
	if !v2.After(backdate) {
		t.Fatalf("touch after window must rewrite last_seen_at: %v not after backdated %v", v2, backdate)
	}

	// 已消失的会话：静默（尽力而为观测面，不报错）。
	if err := st.TouchSessionUsed(ctx, "01MISSING000000000000000000"); err != nil {
		t.Fatalf("touch missing session must be silent: %v", err)
	}
}

// lastSeenOf 读单条会话的 last_seen_at（零值 = 未盖写）。
func lastSeenOf(t *testing.T, st *Store, ctx context.Context, id string) time.Time {
	t.Helper()
	var lastSeen sql.NullInt64
	if err := st.db.QueryRowContext(ctx, `SELECT last_seen_at FROM sessions WHERE id = ?`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("read last_seen_at: %v", err)
	}
	if !lastSeen.Valid {
		return time.Time{}
	}
	return time.Unix(0, lastSeen.Int64).UTC()
}
