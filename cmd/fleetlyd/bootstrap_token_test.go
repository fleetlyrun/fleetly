package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestBootstrapTokenShownOnce T2.17 验收：首启（库内无任何 token）生成
// bootstrap admin token 且**只显示一次**；二次启动不再显示；明文不落库。
func TestBootstrapTokenShownOnce(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	// 首启：生成 + 打印。
	if err := bootstrapAdminToken(log, st); err != nil {
		t.Fatalf("bootstrapAdminToken: %v", err)
	}
	first := buf.String()
	if !strings.Contains(first, "flt_") {
		t.Fatalf("bootstrap token not printed: %q", first)
	}
	if got := strings.Count(first, "flt_"); got != 1 {
		t.Fatalf("token printed %d times, want 1: %q", got, first)
	}

	// 二启：HasAnyToken 恒真 → 不再打印。
	if err := bootstrapAdminToken(log, st); err != nil {
		t.Fatalf("second bootstrapAdminToken: %v", err)
	}
	if got := strings.Count(buf.String(), "flt_"); got != 1 {
		t.Fatalf("token shown %d times after restart, want 1: %q", got, buf.String())
	}

	// 明文不落库：库内只有哈希；scope = admin；审计在档。
	rows, err := st.ListTokens(context.Background())
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("token rows = %d, want 1", len(rows))
	}
	if rows[0].Scopes != "admin" {
		t.Fatalf("bootstrap scopes = %q, want admin", rows[0].Scopes)
	}
	if strings.Contains(rows[0].TokenHash, "flt_") {
		t.Fatal("plaintext prefix found in stored hash")
	}
	audits, err := st.RecentAudits(context.Background(), 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var found bool
	for _, a := range audits {
		if a.Action == "token.create" && a.Target == "token:"+rows[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("bootstrap token.create audit missing")
	}
}
