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

// aggregateTables 是聚合表名集（根级文件不得出现对这些表的 DML 引用；
// goose 迁移 SQL 住在 migrations/ 子目录不受此限）。
var aggregateTables = []string{
	"projects", "apps", "revisions", "deployments", "builds",
	"outbox", "audit", "nodes", "secrets", "configs", "volumes", "networks", "routes",
	"users", "teams", "roles", "memberships", "tokens", "invitations",
}

var tableRefPattern = regexp.MustCompile(`(?i)\b(FROM|INTO|UPDATE)\s+([a-z_]+)\b`)

// TestStateRootIsNotAFacade：internal/infrastructure/persistence 根级 .go 文件必须在白名单内，
// 且内容不得引用聚合表（新聚合 repo 落子包，不落根）。
func TestStateRootIsNotAFacade(t *testing.T) {
	root := repoRoot(t)
	stateDir := filepath.Join(root, "internal", "infrastructure", "persistence")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
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
			table := m[2]
			for _, want := range aggregateTables {
				if strings.EqualFold(table, want) {
					t.Errorf("internal/infrastructure/persistence/%s references aggregate table %q — root package must stay aggregate-free (架构 §6)", e.Name(), table)
				}
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
