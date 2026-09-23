package state

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTokenHashStorageAndAuth T2.17 验收：哈希存储（明文永不落库）+ 认证
// + 吊销 + last_used。
func TestTokenHashStorageAndAuth(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	plaintext := "flt_0123456789abcdef0123456789abcdef0123456789abcdef"
	tok, err := st.CreateToken(ctx, TokenWrite{Hash: HashToken(plaintext), Name: "CI deploy", Scopes: "read,deploy", ActorTokenID: "01ADMIN00000000000000000000"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if tok.ID == "" || tok.Name != "CI deploy" {
		t.Fatalf("unexpected token row: %+v", tok)
	}

	// 哈希存储断言：库内任何 token 列都不含明文。
	var storedHash, storedName string
	if err := st.db.QueryRowContext(ctx, `SELECT token_hash, name FROM tokens WHERE id = ?`, tok.ID).
		Scan(&storedHash, &storedName); err != nil {
		t.Fatalf("scan stored token: %v", err)
	}
	if strings.Contains(storedHash, plaintext) || strings.Contains(storedName, plaintext) {
		t.Fatal("plaintext token leaked into storage")
	}
	if storedHash != HashToken(plaintext) {
		t.Fatalf("stored hash mismatch: %s", storedHash)
	}
	if len(tok.HashPrefix) != 12 || tok.HashPrefix != storedHash[:12] {
		t.Fatalf("hash prefix = %q, want first 12 of %s", tok.HashPrefix, storedHash)
	}

	// 认证成功（S18-A2 后纯认证语义：AuthenticateToken 不再盖 last_used_at
	// ——盖写职责上移到调用方 internal/api 的认证路径，经进程内节流后调
	// TouchTokenUsed；此处钉死两层契约：认证返回零值 + 盖写原语生效）。
	got, err := st.AuthenticateToken(ctx, plaintext)
	if err != nil {
		t.Fatalf("AuthenticateToken: %v", err)
	}
	if got.ID != tok.ID {
		t.Fatalf("authenticated id = %s, want %s", got.ID, tok.ID)
	}
	if !got.LastUsedAt.IsZero() {
		t.Fatal("AuthenticateToken must not touch last_used_at (A2: touch moved to api layer)")
	}
	if err := st.TouchTokenUsed(ctx, tok.ID); err != nil {
		t.Fatalf("TouchTokenUsed: %v", err)
	}
	touched, err := st.ListTokens(ctx)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	touchedOK := false
	for _, r := range touched {
		if r.ID == tok.ID && !r.LastUsedAt.IsZero() {
			touchedOK = true
		}
	}
	if !touchedOK {
		t.Fatal("last_used_at not touched by TouchTokenUsed")
	}

	// 错误凭据 → ErrTokenInvalid（不是 ErrTokenNotFound——不泄漏存在性）。
	if _, err := st.AuthenticateToken(ctx, "flt_wrong"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("wrong credential err = %v, want ErrTokenInvalid", err)
	}

	// 吊销后认证拒绝；再吊销幂等成功；列表不再出现。
	if err := st.RevokeToken(ctx, tok.ID, "01ADMIN00000000000000000000"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := st.AuthenticateToken(ctx, plaintext); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("auth after revoke err = %v, want ErrTokenRevoked", err)
	}
	if err := st.RevokeToken(ctx, tok.ID, ""); err != nil {
		t.Fatalf("idempotent RevokeToken: %v", err)
	}
	if err := st.RevokeToken(ctx, "01UNKNOWN00000000000000000", ""); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("revoke unknown err = %v, want ErrTokenNotFound", err)
	}
	rows, err := st.ListTokens(ctx)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	for _, r := range rows {
		if r.ID == tok.ID {
			t.Fatal("revoked token must not appear in ListTokens")
		}
	}

	// HasAnyToken：吊销行也算「存在过」（bootstrap 一次性语义）。
	any, err := st.HasAnyToken(ctx)
	if err != nil {
		t.Fatalf("HasAnyToken: %v", err)
	}
	if !any {
		t.Fatal("HasAnyToken = false after token created (revoked rows still count)")
	}

	// 非法哈希形态拒绝（防明文旁路落库）。
	if _, err := st.CreateToken(ctx, TokenWrite{Hash: plaintext, Name: "n", Scopes: "read"}); err == nil {
		t.Fatal("CreateToken with plaintext (not 64-hex) must fail")
	}
}

// TestTokenAuditFailClosed token 生命周期动作与审计同事务（token.create /
// token.revoke 在档；审计行写不进则整事务回滚的 fail-closed 语义由表级
// CHECK 承载，audit_test.go 已验证）。
func TestTokenAuditFailClosed(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	tok, err := st.CreateToken(ctx, TokenWrite{Hash: HashToken("flt_audit"), Name: "n", Scopes: "admin"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := st.RevokeToken(ctx, tok.ID, ""); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var created, revoked bool
	for _, a := range audits {
		if a.Action == "token.create" && a.Target == "token:"+tok.ID {
			created = true
		}
		if a.Action == "token.revoke" && a.Target == "token:"+tok.ID {
			revoked = true
		}
	}
	if !created || !revoked {
		t.Fatalf("audit missing: create=%v revoke=%v", created, revoked)
	}
}

// isAdminScopes 是守卫测试的 scope 判定注入（与 api.containsScope 的
// admin 蕴含语义一致：scopes 含 admin 词即 admin）。
func isAdminScopes(scopes string) bool {
	for _, s := range strings.Split(scopes, ",") {
		if strings.TrimSpace(s) == "admin" {
			return true
		}
	}
	return false
}

// TestRevokeTokenGuardLastAdmin M4-6 回归：最后管理员守卫——吊销后不存在
// 任何未吊销 admin token 时拒绝（ErrTokenLastAdmin，事务回滚吊销不生效）；
// 存在第二枚 admin（或吊销非 admin token）时放行；幂等面（已吊销目标）
// 与不存在面不受守卫影响。
func TestRevokeTokenGuardLastAdmin(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 唯一 admin：吊销被拒，行仍在册。
	only, err := st.CreateToken(ctx, TokenWrite{Hash: HashToken("flt_only_admin"), Name: "only", Scopes: "admin"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := st.RevokeTokenGuardLastAdmin(ctx, only.ID, "", isAdminScopes); !errors.Is(err, ErrTokenLastAdmin) {
		t.Fatalf("revoke last admin err = %v, want ErrTokenLastAdmin", err)
	}
	if rows, err := st.ListTokens(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("last admin must remain registered after guard rejection: rows=%d err=%v", len(rows), err)
	}

	// 存在第二枚 admin：两枚均可吊销（吊销第一枚后第二枚成为「最后一枚」，
	// 第三枚 read token 不解除守卫）。
	second, err := st.CreateToken(ctx, TokenWrite{Hash: HashToken("flt_second_admin"), Name: "second", Scopes: "read,admin"})
	if err != nil {
		t.Fatalf("CreateToken second: %v", err)
	}
	reader, err := st.CreateToken(ctx, TokenWrite{Hash: HashToken("flt_reader"), Name: "reader", Scopes: "read"})
	if err != nil {
		t.Fatalf("CreateToken reader: %v", err)
	}
	if err := st.RevokeTokenGuardLastAdmin(ctx, only.ID, "caller-tok", isAdminScopes); err != nil {
		t.Fatalf("revoke with second admin present: %v", err)
	}

	// 非 admin token 的吊销不受守卫影响（admin 仍在册）。
	if err := st.RevokeTokenGuardLastAdmin(ctx, reader.ID, "caller-tok", isAdminScopes); err != nil {
		t.Fatalf("revoke non-admin token: %v", err)
	}

	// 只剩第二枚 admin：吊销被拒（read token 不算 admin——守卫只认 admin）。
	if err := st.RevokeTokenGuardLastAdmin(ctx, second.ID, "", isAdminScopes); !errors.Is(err, ErrTokenLastAdmin) {
		t.Fatalf("revoke final admin err = %v, want ErrTokenLastAdmin", err)
	}

	// 幂等/不存在面：已吊销目标幂等成功；不存在 ErrTokenNotFound。
	if err := st.RevokeTokenGuardLastAdmin(ctx, only.ID, "", isAdminScopes); err != nil {
		t.Fatalf("revoke already-revoked must be idempotent success: %v", err)
	}
	if err := st.RevokeTokenGuardLastAdmin(ctx, "missing-token", "", isAdminScopes); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("revoke missing err = %v, want ErrTokenNotFound", err)
	}
}

// TestTokenUserAndProjectBinding（v0.3 W1，RBAC 设计 §2.3）：CreateToken/
// ListTokens 落 user_id/project_id 两列（'' = NULL）；机具令牌（user NULL）
// 投影两列为空且不受 users 表影响；PAT 认证投影带 UserID，属主禁用 →
// ErrTokenInvalid 同码拒认（不泄漏存在性），解禁恢复；吊销面语义不变。
func TestTokenUserAndProjectBinding(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, UserWrite{Email: "pat@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// 用户 PAT：user/project 维度往返。
	pat, err := st.CreateToken(ctx, TokenWrite{Hash: HashToken("flt_user_pat"), Name: "cli pat", Scopes: "read,deploy", UserID: u.ID, ProjectID: "01PROJ1"})
	if err != nil {
		t.Fatalf("CreateToken PAT: %v", err)
	}
	if pat.UserID != u.ID || pat.ProjectID != "01PROJ1" {
		t.Fatalf("PAT projection = %+v, want user %s project 01PROJ1", pat, u.ID)
	}
	got, err := st.AuthenticateToken(ctx, "flt_user_pat")
	if err != nil || got.UserID != u.ID || got.ProjectID != "01PROJ1" {
		t.Fatalf("AuthenticateToken PAT: %v (%+v)", err, got)
	}

	// 机具令牌（user NULL）：投影两列为空，认证不查 users 侧。
	machine, err := st.CreateToken(ctx, TokenWrite{Hash: HashToken("flt_machine"), Name: "ci", Scopes: "admin"})
	if err != nil {
		t.Fatalf("CreateToken machine: %v", err)
	}
	if machine.UserID != "" || machine.ProjectID != "" {
		t.Fatalf("machine token must have empty user/project: %+v", machine)
	}
	if got, err := st.AuthenticateToken(ctx, "flt_machine"); err != nil || got.UserID != "" {
		t.Fatalf("machine token auth: %v (%+v)", err, got)
	}

	// 列表投影带两列。
	rows, err := st.ListTokens(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListTokens: %v (n=%d)", err, len(rows))
	}

	// 属主禁用 → PAT 拒认（ErrTokenInvalid 同码），机具令牌不受影响；
	// 解禁 → PAT 恢复。
	if err := st.DisableUser(ctx, u.ID, "", ""); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if _, err := st.AuthenticateToken(ctx, "flt_user_pat"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("disabled owner PAT err = %v, want ErrTokenInvalid", err)
	}
	if _, err := st.AuthenticateToken(ctx, "flt_machine"); err != nil {
		t.Fatalf("machine token must be unaffected by user disable: %v", err)
	}
	if err := st.EnableUser(ctx, u.ID, "", ""); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	if got, err := st.AuthenticateToken(ctx, "flt_user_pat"); err != nil || got.ID != pat.ID {
		t.Fatalf("PAT auth after re-enable: %v", err)
	}

	// 幂等/吊销面回归（新维度列不改变既有语义）。
	if err := st.RevokeToken(ctx, pat.ID, ""); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := st.AuthenticateToken(ctx, "flt_user_pat"); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("revoked PAT err = %v, want ErrTokenRevoked", err)
	}
}
