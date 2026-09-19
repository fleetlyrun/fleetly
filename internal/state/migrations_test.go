package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestOpenAppliesMigrationsIdempotently 验证迁移体系：首次打开应用全部
// 迁移（goose 版本表可见）；重复打开（幂等）不产生错误与重复应用。
func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	ctx := context.Background()
	st1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	t.Cleanup(func() { _ = st1.Close() }) // 断言失败路径也释放文件句柄（Windows）
	var gotVersion int64
	if err := st1.db.QueryRowContext(ctx,
		`SELECT max(version_id) FROM `+gooseVersionTableName).Scan(&gotVersion); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if gotVersion < 1 {
		t.Fatalf("migration version = %d, want >= 1", gotVersion)
	}
	// 核心表全部存在（对照冻结清单逐表核对）。
	for _, table := range []string{
		"meta", "apps", "revisions", "deployments", "env_vars", "domains",
		"placements", "volumes", "nodes", "runtime_node_refs", "tokens",
		"events", "audit_log", "state_backups", "orphans",
	} {
		var name string
		err := st1.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	// 二次打开 = 幂等（goose 只应用未执行迁移）。
	st2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open (idempotent): %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	var versionAfter int64
	if err := st2.db.QueryRowContext(ctx,
		`SELECT max(version_id) FROM `+gooseVersionTableName).Scan(&versionAfter); err != nil {
		t.Fatalf("read goose version after reopen: %v", err)
	}
	if versionAfter != gotVersion {
		t.Fatalf("version changed on idempotent reopen: %d -> %d", gotVersion, versionAfter)
	}
}

// TestOpenEnablesWALAndForeignKeys 验证连接参数真实生效（WAL、外键）。
func TestOpenEnablesWALAndForeignKeys(t *testing.T) {
	st := newTestStore(t)
	defer func() { _ = st.Close() }()
	var mode string
	if err := st.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
}

// TestOpenRefusesNewerSchema 高版本守卫（升级回退错配整改③的验收断言）：
// DB schema 版本 > 本二进制已知最大迁移版本 → state.Open 拒绝启动，错误
// 信息指明版本差与可行动路径（按快照恢复）——旧二进制对新 schema 静默
// no-op 运行的路径不存在。
func TestOpenRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	// 正常打开一次：迁移到本二进制的最大版本。
	st1, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	ctx := context.Background()
	var maxKnown int64
	if err := st1.db.QueryRowContext(ctx,
		`SELECT max(version_id) FROM `+gooseVersionTableName).Scan(&maxKnown); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	// 把库版本顶到「更新版本」（模拟：新版 fleetlyd 升级后按旧二进制回退
	// 的现场——版本差 = future-gap）。
	const futureGap = int64(7)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	future := maxKnown + futureGap
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO `+gooseVersionTableName+` (version_id, is_applied) VALUES (?, 1)`, future); err != nil {
		t.Fatalf("bump schema version to %d: %v", future, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	_, err = Open(context.Background(), path)
	if err == nil {
		t.Fatal("Open must refuse a database from a newer schema (silent no-op path exists!)")
	}
	for _, want := range []string{
		"更新版本",
		strconv.FormatInt(future, 10),
		strconv.FormatInt(maxKnown, 10),
		"backup-restore",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("guard error %q missing %q", err.Error(), want)
		}
	}

	// 守卫只在版本超前时触发：同库把版本退回本二进制最大版本后照常打开
	// （幂等 no-op），确认拒绝面没有误伤正常升级路径。
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen raw sqlite: %v", err)
	}
	if _, err := raw.ExecContext(ctx,
		`DELETE FROM `+gooseVersionTableName+` WHERE version_id = ?`, future); err != nil {
		t.Fatalf("revert schema version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	st2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen after reverting future version: %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}
}

// TestSchemaVersionsReadOnly 是 F5（S20 升级回退 schema 感知）的机制验收：
// SchemaVersions 对三种现场给出正确读数，且只读（不建库、不迁移、不触发
// 高版本守卫——回退编排在旧二进制拒绝打开之前就能比对两侧版本）：
//   - 库文件不存在 → db=0（不创建文件，无副作用）；
//   - 正常迁移库 → db=max（与本二进制天花板一致）；
//   - 未来版本库（回退现场）→ db>max 照常读出（守卫不触发）。
func TestSchemaVersionsReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// ① 库文件不存在：db=0，且绝不落盘建库。
	missing := filepath.Join(dir, "missing.db")
	dbV, maxV, err := SchemaVersions(ctx, missing)
	if err != nil {
		t.Fatalf("missing db: %v", err)
	}
	if dbV != 0 {
		t.Errorf("missing db: db = %d, want 0", dbV)
	}
	if maxV <= 0 {
		t.Errorf("missing db: max = %d, want > 0", maxV)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Errorf("missing db: SchemaVersions must not create the file, stat err = %v", statErr)
	}

	// ② 正常迁移库：db == max。
	path := filepath.Join(dir, "state.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	dbV, maxV, err = SchemaVersions(ctx, path)
	if err != nil {
		t.Fatalf("migrated db: %v", err)
	}
	if dbV != maxV {
		t.Errorf("migrated db: db = %d, max = %d, want equal", dbV, maxV)
	}

	// ③ 回退现场（库来自更新版本）：db > max 照常读出，守卫不触发。
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	future := maxV + 5
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO `+gooseVersionTableName+` (version_id, is_applied) VALUES (?, 1)`, future); err != nil {
		t.Fatalf("bump schema version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	dbV, _, err = SchemaVersions(ctx, path)
	if err != nil {
		t.Fatalf("newer-schema db: %v (guard must NOT fire on the read-only path)", err)
	}
	if dbV != future {
		t.Errorf("newer-schema db: db = %d, want %d", dbV, future)
	}
}

// TestMigrationsAreAdditiveOnly 钉死「迁移只加法」纪律：
// ① 不存在 down 迁移文件（回滚 = 恢复快照，架构 §2.8）；
// ② 版本号连续递增（无空洞、无重复）；
// ③ 已应用迁移内容 sha256 与 golden 一致——改写已应用文件即测试失败，
//
//	只允许新增更高版本文件。
func TestMigrationsAreAdditiveOnly(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("migrations"))
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".down.sql") {
			t.Fatalf("down migration found: %s（回滚 = 恢复快照，禁止 down 迁移）", e.Name())
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	seen := map[int64]bool{}
	for _, name := range names {
		lead, _, found := strings.Cut(name, "_")
		if !found {
			t.Fatalf("migration %s: filename must be <version>_<name>.sql", name)
		}
		version, err := strconv.ParseInt(lead, 10, 64)
		if err != nil {
			t.Fatalf("migration %s: parse leading version: %v", name, err)
		}
		if seen[version] {
			t.Fatalf("duplicate migration version %d", version)
		}
		seen[version] = true
	}
	for v := int64(1); v <= int64(len(names)); v++ {
		if !seen[v] {
			t.Fatalf("migration versions not contiguous: missing %d (have %v)", v, names)
		}
	}

	hashes, err := migrationHashes()
	if err != nil {
		t.Fatalf("hash migrations: %v", err)
	}
	goldenPath := filepath.Join("testdata", "migrations.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		var b strings.Builder
		for _, name := range names {
			b.WriteString(name)
			b.WriteByte(' ')
			b.WriteString(hashes[name])
			b.WriteByte('\n')
		}
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(b.String()), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	raw, err := os.ReadFile(goldenPath) //nolint:gosec // 路径为仓内固定相对路径 testdata/migrations.golden
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	var want []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			want = append(want, line)
		}
	}
	var got []string
	for _, name := range names {
		got = append(got, name+" "+hashes[name])
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("applied migration content changed (只加法纪律：已应用文件禁止改写):\n got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
