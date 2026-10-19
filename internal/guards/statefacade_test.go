package guards

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// state 根级文件白名单（架构 §2/§6：internal/infrastructure/persistence 根包只承载连接、事务
// 与时间语义；聚合查询一律住在 internal/infrastructure/persistence/<聚合>/ 子包，禁止门面
// store.go——旧仓 state=2.9 万行门面的教训）。新增根级文件必须在此登记
// 并说明为什么它不是门面；白名单条目删除后文件移走即绿（双向保鲜）。
var stateRootFiles = map[string]string{
	"state.go":      "connection, pragmas, goose wiring, Tx boundary, driver error predicates — no aggregate queries; contract sentinels live in internal/domain/shared/state",
	"state_test.go": "four-write transaction composition test for the root Tx boundary",
	"statekit.go":   "shared mechanical atoms consumed by aggregate repos (page-width clamp, IN placeholders, state-set text, []any casting, CAS WHERE fragment) — no aggregate queries; single-entry-point law enforced by internal/guards/statekitlaw_test.go (G7/G8, P5-1)",
}

// stateMigrationsDir 是聚合表清单的唯一真源（R05-M8）：迁移目录解析
// CREATE TABLE，不再维护手写表清单（旧清单停在 00005，19/38 表覆盖）。
const stateMigrationsDir = "internal/infrastructure/persistence/migrations"

// migrationCreateTableRe 匹配迁移里的建表语句（容忍 IF NOT EXISTS 与大小写）。
// 表名带数字（如 idempotency_records_v2 重建表）一并纳入。
var migrationCreateTableRe = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_]+)`)

// parseMigrationTables 从迁移 SQL 文本解析建表名（去 `--` 行注释后匹配；
// 迁移是 schema 真源，表清单随它自动增长）。
func parseMigrationTables(sql string) []string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		for _, m := range migrationCreateTableRe.FindAllStringSubmatch(line, -1) {
			out = append(out, strings.ToLower(m[1]))
		}
	}
	return out
}

// migrationTablesIn 解析目录下全部 *.sql 的建表名集（防盲由调用方断言）。
func migrationTablesIn(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", dir, err)
	}
	tables := map[string]bool{}
	sqlFiles := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		sqlFiles++
		data, readErr := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // 守卫扫描仓库自有迁移文件
		if readErr != nil {
			t.Fatalf("read migration %s: %v", e.Name(), readErr)
		}
		for _, name := range parseMigrationTables(string(data)) {
			tables[name] = true
		}
	}
	if sqlFiles == 0 {
		t.Fatalf("migrations dir %s has no .sql files — guard is blind, fix the scan", dir)
	}
	return tables
}

var tableRefPattern = regexp.MustCompile(`(?i)\b(FROM|INTO|UPDATE)\s+([a-z_][a-z0-9_]*)\b`)

// TestStateRootIsNotAFacade：internal/infrastructure/persistence 根级 .go 文件必须在白名单内，
// 且内容不得引用聚合表（新聚合 repo 落子包，不落根）。聚合表清单从
// migrations/*.sql 动态解析（R05-M8：新增迁移自动纳入扫描面）。
func TestStateRootIsNotAFacade(t *testing.T) {
	root := repoRoot(t)
	stateDir := filepath.Join(root, "internal", "infrastructure", "persistence")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	tables := migrationTablesIn(t, filepath.Join(root, stateMigrationsDir))
	// 防盲：报告实测 38 表；当前迁移实测 40 个建表名（含 00034 的 v2
	// 重建表）。低于 38 = 解析退化，守卫必须先自红。
	if len(tables) < 38 {
		t.Fatalf("migration scan found only %d aggregate tables (want >= 38) — guard is blind, fix the scan", len(tables))
	}
	// 正向锚：几类跨年代的表必须在册（00005 旧清单之外的新表 + 最新迁移）。
	for _, want := range []string{"projects", "tasks", "alert_rules", "node_relay_credentials"} {
		if !tables[want] {
			t.Fatalf("migration scan missed table %q — guard is blind, fix the scan", want)
		}
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		reason, ok := stateRootFiles[e.Name()]
		if !ok {
			t.Errorf("internal/infrastructure/persistence/%s is not whitelisted — root package must stay aggregate-free; "+
				"put aggregate queries in internal/infrastructure/persistence/<aggregate>/ or register the file with a reason", e.Name())
			continue
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("state root whitelist entry %s must carry a reason", e.Name())
		}
		content := readFileLF(t, "internal/infrastructure/persistence/"+e.Name())
		for _, m := range tableRefPattern.FindAllStringSubmatch(content, -1) {
			table := strings.ToLower(m[2])
			if tables[table] {
				t.Errorf("internal/infrastructure/persistence/%s references aggregate table %q — root package must stay aggregate-free (架构 §6)", e.Name(), table)
			}
		}
	}
	// 双向保鲜：白名单条目不再存在即红。
	for name := range stateRootFiles {
		found := false
		for _, e := range entries {
			if !e.IsDir() && e.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("state root whitelist entry %s no longer exists — remove the entry", name)
		}
	}
}

// TestStateFacadeTableListFollowsMigrations 是 R05-M8 的形状用例：给定迁移
// 目录的解析结果必须随新增迁移增长（不依赖仓库当前快照）——新增一张表即
// 自动进入根包反扫面，无需再手改清单。
func TestStateFacadeTableListFollowsMigrations(t *testing.T) {
	dir := t.TempDir()
	write := func(name, sql string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("00001_core.sql", "CREATE TABLE projects (id TEXT);\nCREATE TABLE IF NOT EXISTS tasks (id TEXT);\n")
	// 迁移注释里的 CREATE TABLE 不算（-- 行注释先剥）。
	write("00002_more.sql", "-- legacy: CREATE TABLE commented_out (id TEXT);\nCREATE TABLE alert_rules (id TEXT);\n")
	tables := migrationTablesIn(t, dir)
	for _, want := range []string{"projects", "tasks", "alert_rules"} {
		if !tables[want] {
			t.Errorf("parser missed table %q in the temp migration dir", want)
		}
	}
	if tables["commented_out"] {
		t.Errorf("parser must ignore CREATE TABLE inside SQL line comments")
	}
	// 新增迁移即纳入：再落一张表，重新解析必须可见。
	write("00003_new.sql", "CREATE TABLE brand_new_aggregate (id TEXT);\n")
	if !migrationTablesIn(t, dir)["brand_new_aggregate"] {
		t.Errorf("a newly added migration table must join the scan face automatically (R05-M8)")
	}
}
