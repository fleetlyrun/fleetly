package hook

// Git 触发配置聚合 hermetic 测试（F0.13）：配置读写、Token 材料双换、
// 重投去重（含保留窗清理）。FK 面铺 project+app 最小行。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func seedApp(t *testing.T, db *persistence.DB) {
	t.Helper()
	ctx := context.Background()
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		now := "2026-09-30T00:00:00Z"
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, seq, name, team_id, created_at, updated_at) VALUES ('p1', 1, 'shop', 'default', ?, ?)`, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO apps (id, seq, project_id, name, created_at, updated_at) VALUES ('a1', 1, 'p1', 'web', ?, ?)`, now, now)
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

	h := &hook.Hook{ //nolint:gosec // 测试夹具的假材料（占位密文/前缀），非真实凭据
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
	if err := repo.UpdateConfig(ctx, db.Runner(), &hook.Hook{AppID: "missing"}); err != state.ErrNotFound {
		t.Fatalf("update missing = %v", err)
	}
	if err := repo.RotateToken(ctx, db.Runner(), "missing", "x", "y", nil); err != state.ErrNotFound {
		t.Fatalf("rotate missing = %v", err)
	}
}

// TestHookCreateConflictNormalization：双唯一键（app_id 主键 / token_
// sha256 UNIQUE）命中归一为 ErrAlreadyExists——并发首配交错的裸
// "constraint failed: UNIQUE" 不得上抛成 E_INTERNAL（SetGitHook 重试即走
// UpdateConfig 臂）。
func TestHookCreateConflictNormalization(t *testing.T) {
	db, clock := statetest.New(t)
	seedApp(t, db)
	// 第二 App 行（token 查找键冲突臂的载体）。
	ctx := context.Background()
	_, err := db.Runner().ExecContext(ctx,
		`INSERT INTO apps (id, seq, project_id, name, created_at, updated_at) VALUES ('a2', 2, 'p1', 'worker', '2026-09-30T00:00:00Z', '2026-09-30T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seed a2: %v", err)
	}
	repo := New(clock)

	if err := repo.Create(ctx, db.Runner(), &hook.Hook{
		AppID: "a1", Repo: "https://example.com/repo.git",
		TokenSHA256: "sha-a", SecretCiphertext: []byte("ct"),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 同 App 重复首配 → 主键冲突。
	err = repo.Create(ctx, db.Runner(), &hook.Hook{
		AppID: "a1", Repo: "https://example.com/other.git",
		TokenSHA256: "sha-b", SecretCiphertext: []byte("ct"),
	})
	if !errors.Is(err, state.ErrAlreadyExists) {
		t.Fatalf("duplicate app create = %v, want ErrAlreadyExists", err)
	}

	// 异 App 撞 token 查找键 → UNIQUE(token_sha256) 冲突。
	err = repo.Create(ctx, db.Runner(), &hook.Hook{
		AppID: "a2", Repo: "https://example.com/repo.git",
		TokenSHA256: "sha-a", SecretCiphertext: []byte("ct"),
	})
	if !errors.Is(err, state.ErrAlreadyExists) {
		t.Fatalf("token collision create = %v, want ErrAlreadyExists", err)
	}
}

func TestRecordDeliveryDedup(t *testing.T) {
	db, clock := statetest.New(t)
	seedApp(t, db)
	ctx := context.Background()
	repo := New(clock)
	if err := repo.Create(ctx, db.Runner(), &hook.Hook{
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

// TestDeleteByAppCascadesDeliveries（FB-1 回归）：hook_deliveries.app_id
// REFERENCES app_hooks 无 ON DELETE CASCADE——只删 hook 行会撞 FOREIGN
// KEY constraint failed（配置过 GitHook 的 App 删除实咬）。聚合级联：
// 去重行先清、hook 行后清，App tombstone 事务得以整体成功。
func TestDeleteByAppCascadesDeliveries(t *testing.T) {
	db, clock := statetest.New(t)
	seedApp(t, db)
	ctx := context.Background()
	repo := New(clock)
	if err := repo.Create(ctx, db.Runner(), &hook.Hook{
		AppID: "a1", Repo: "https://example.com/repo.git", TokenSHA256: "sha-a", SecretCiphertext: []byte("ct"),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, d := range []string{"d-1", "d-2"} {
		if _, err := repo.RecordDelivery(ctx, db.Runner(), "a1", d); err != nil {
			t.Fatalf("record %s: %v", d, err)
		}
	}

	if err := repo.DeleteByApp(ctx, db.Runner(), "a1"); err != nil {
		t.Fatalf("delete by app: %v", err)
	}
	if _, err := repo.Get(ctx, db.Runner(), "a1"); err != state.ErrNotFound {
		t.Fatalf("hook row must be gone, got %v", err)
	}
	var deliveries int
	if err := db.Runner().QueryRowContext(ctx,
		`SELECT count(*) FROM hook_deliveries WHERE app_id = 'a1'`).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if deliveries != 0 {
		t.Fatalf("delivery dedup rows must be cascaded away, got %d", deliveries)
	}

	// 聚合既净：同 App 重新首配照常铸造（删除-重配环全通）。
	if err := repo.Create(ctx, db.Runner(), &hook.Hook{
		AppID: "a1", Repo: "https://example.com/repo.git", TokenSHA256: "sha-b", SecretCiphertext: []byte("ct"),
	}); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}
