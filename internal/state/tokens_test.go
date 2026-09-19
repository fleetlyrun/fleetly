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
