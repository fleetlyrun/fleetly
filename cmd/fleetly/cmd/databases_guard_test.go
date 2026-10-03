package cmd

// CLI 帮助文案 × 模板注册表对账守卫（架构评审第二轮候选 1）：databases
// 命令组的三处引擎枚举（组帮助、create synopsis、--engine 旗标说明）曾
// 是 engine.DBEngines() 的无守卫硬拷贝——F2.1 加 mysql/mongo 漏改即静默
// 陈旧。本守卫按各自形态解析三处文案的枚举集，与 dbtemplate.Engines()
// （唯一真源，test-only import 零运行时耦合）双向对账；锚点正则失配 =
// 守卫面搬了，fatal 提示随面更新（buildernamesguard 同款纪律）。

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
)

// 数据库引擎枚举的三处锚点（失配即 fatal：文案形态是冻结面，改形态须
// 随批更新锚点）。
var (
	// 组帮助：…(postgres/pgvector/redis templates; …
	dbGroupHelpRe = regexp.MustCompile(`groupVerb\("databases", "[^"]*?\(([a-z/]+) templates`)
	// create synopsis：Create a database from a template (a / b / c)
	dbCreateSynopsisRe = regexp.MustCompile(`synopsis: "Create a database from a template \(([a-z /]+)\)"`)
	// --engine 旗标说明：template engine: a | b | c (required)
	dbEngineFlagRe = regexp.MustCompile(`"template engine: ([a-z |]+) \(required\)"`)
)

// parseEngineEnumeration 把文案里的枚举段拆为引擎集合（"/" 与 " | " 与
// " / " 三种连接形态归一）。
func parseEngineEnumeration(segment string) []string {
	segment = strings.ReplaceAll(segment, " | ", "/")
	segment = strings.ReplaceAll(segment, " / ", "/")
	parts := strings.Split(segment, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// TestDatabaseEngineHelpMatchesRegistry：三处文案枚举集 == 注册表值域
// （双向：漏列新引擎与列了退役引擎都红）。
func TestDatabaseEngineHelpMatchesRegistry(t *testing.T) {
	want := dbtemplate.Engines()

	for _, tc := range []struct {
		name string
		file string
		re   *regexp.Regexp
	}{
		{"group-help", "app.go", dbGroupHelpRe},
		{"create-synopsis", "verbs_databases.go", dbCreateSynopsisRe},
		{"engine-flag", "verbs_databases.go", dbEngineFlagRe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			m := tc.re.FindStringSubmatch(string(src))
			if m == nil {
				t.Fatalf("engine enumeration anchor not found in %s — the wording shape changed; update this guard with it", tc.file)
			}
			got := parseEngineEnumeration(m[1])
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("engine list in %s = %v, registry = %v — the CLI help is a stale copy of dbtemplate.Engines(); align it with the template registry", tc.file, got, want)
			}
		})
	}
}
