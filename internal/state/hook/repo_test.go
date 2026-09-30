package hook

// Git 触发配置聚合 hermetic 测试（F0.13）：配置读写、Token 材料双换、
// 重投去重（含保留窗清理）。FK 面铺 project+app 最小行。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func seedApp(t *testing.T, db *state.DB) {
	t.Helper()
	ctx := context.Background()
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		now := "2026-09-30T00:00:00Z"
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, team_id, created_at, updated_at) VALUES ('p1', 'shop', 'default', ?, ?)`, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO apps (id, project_id, name, created_at, updated_at) VALUES ('a1', 'p1', 'web', ?, ?)`, now, now)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestHookConfigRoundTrip(t *testing.T) {
	db, clock := statetest.New(t)
	seedApp(t, db)
	ctx := context.Background()
	repo := New(clock)

	h := &Hook{ //nolint:gosec // 测试夹具的假材料（占位密文/前缀），非真实凭据
		AppID: "a1", Repo: "https://github.com/acme/shop.git", Branch: "main",
		Dockerfile: "Dockerfile", WatchPaths: []string{"web/", "libs/core"},
		TokenSHA256: "sha-a", TokenPrefix: "flthook_ab", SecretCiphertext: []byte("ciphertext"),
	}
	if err := repo.Create(ctx, db.Runner(), h); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.Get(ctx, db.Runner(), "a1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Repo != h.Repo || got.Branch != "main" || got.TokenPrefix != "flthook_ab" {
		t.Fatalf("got = %+v", got)
	}
	if len(got.WatchPaths) != 2 || got.WatchPaths[0] != "web/" || got.WatchPaths[1] != "libs/core" {
		t.Fatalf("watch paths = %v", got.WatchPaths)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("timestamps not set: %+v", got)
	}

	// URL token 查找键（接收面入口）。
	byToken, err := repo.GetByTokenSHA256(ctx, db.Runner(), "sha-a")
	if err != nil {
		t.Fatalf("get by token: %v", err)
	}
	if byToken.AppID != "a1" {
		t.Fatalf("byToken = %+v", byToken)
	}

	// 配置更新不换 Token 材料。
	clock.Advance(2 * time.Second)
	got.Branch = "release"
	got.WatchPaths = nil
	if err := repo.UpdateConfig(ctx, db.Runner(), got); err != nil {
		t.Fatalf("update config: %v", err)
	}
	after, err := repo.Get(ctx, db.Runner(), "a1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if after.Branch != "release" || len(after.WatchPaths) != 0 {
		t.Fatalf("after = %+v", after)
	}
	if after.TokenSHA256 != "sha-a" || string(after.SecretCiphertext) != "ciphertext" {
		t.Fatalf("token material must not change on config update: %+v", after)
	}
	if after.UpdatedAt == after.CreatedAt {
		t.Fatalf("updated_at not advanced")
	}

	// Rotate 双换。
	clock.Advance(2 * time.Second)
	if err := repo.RotateToken(ctx, db.Runner(), "a1", "sha-b", "flthook_cd", []byte("ct-b")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	rotated, err := repo.GetByTokenSHA256(ctx, db.Runner(), "sha-b")
	if err != nil {
		t.Fatalf("get by new token: %v", err)
	}
	if rotated.TokenPrefix != "flthook_cd" || string(rotated.SecretCiphertext) != "ct-b" {
		t.Fatalf("rotated = %+v", rotated)
	}
	if _, err := repo.GetByTokenSHA256(ctx, db.Runner(), "sha-a"); err != state.ErrNotFound {
		t.Fatalf("old token must be gone, got %v", err)
	}

	// 未存在行的更新/换材料是 NotFound。
	if err := repo.UpdateConfig(ctx, db.Runner(), &Hook{AppID: "missing"}); err != state.ErrNotFound {
		t.Fatalf("update missing = %v", err)
	}
	if err := repo.RotateToken(ctx, db.Runner(), "missing", "x", "y", nil); err != state.ErrNotFound {
		t.Fatalf("rotate missing = %v", err)
	}
}

func TestRecordDeliveryDedup(t *testing.T) {
	db, clock := statetest.New(t)
	seedApp(t, db)
	ctx := context.Background()
	repo := New(clock)
	if err := repo.Create(ctx, db.Runner(), &Hook{
		AppID: "a1", Repo: "https://example.com/repo.git", TokenSHA256: "sha-a", SecretCiphertext: []byte("ct"),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	dup, err := repo.RecordDelivery(ctx, db.Runner(), "a1", "d-1")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if dup {
		t.Fatalf("first delivery must not be duplicate")
	}
	dup, err = repo.RecordDelivery(ctx, db.Runner(), "a1", "d-1")
	if err != nil {
		t.Fatalf("record again: %v", err)
	}
	if !dup {
		t.Fatalf("redelivery must be flagged duplicate")
	}
	// 不同 delivery 不互斥。
	dup, err = repo.RecordDelivery(ctx, db.Runner(), "a1", "d-2")
	if err != nil {
		t.Fatalf("record d-2: %v", err)
	}
	if dup {
		t.Fatalf("different delivery must not be duplicate")
	}

	// 保留窗清理：推进 8 天后 d-1 的行被清走，可再次受理。
	clock.Advance(8 * 24 * time.Hour)
	dup, err = repo.RecordDelivery(ctx, db.Runner(), "a1", "d-1")
	if err != nil {
		t.Fatalf("record after retention: %v", err)
	}
	if dup {
		t.Fatalf("delivery row past the retention window must be pruned")
	}
}
