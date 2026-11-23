package guards

// B3b 契约双跑的两侧同名对账守卫（zeabur 设计 §5「守卫断言两侧同名零
// 缺席」；设计稿验收锚原条目）。
//
// runtime 契约清单（internal/testsupport/runtimecontract）的同一份断言对
// 两个 Runtime 执行：Fake 侧（apitest.FakeRuntime 与引擎白盒 fakeRuntime）
// 与真机侧（e2e/runtimecontract 执行器对 dind 内真 swarm）。双跑的价值
// 完全取决于「两侧跑的是不是同一份清单、清单里的用例有没有一边缺席」：
//
//   - 零缺席：清单用例常量集 == RunE 的 cases 表 == CaseNames() 导出面
//     （CaseNames 只为对账而导出，本守卫是它的消费者——用例增删不同步
//     即红）；
//   - 两侧在线：Fake 侧两份 fake 的测试与真机执行器都必须调用清单本体
//     （Run / RunE），e2e 腿脚本必须真构建并运行执行器，mise 任务必须
//     指向该脚本（CI 承载由 e2eciaccount 另行对账）。
//
// 本守卫 2026-11-23 补立：B3b 落地批的三处注释声称本守卫存在，实际未写
// ——声称的守卫比没有守卫更糟，注释当时一并更正。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/testsupport/runtimecontract"
)

// runtimeContractRunRe 匹配对清单测试入口的调用 `runtimecontract.Run(`。
var runtimeContractRunRe = regexp.MustCompile(`\bruntimecontract\.Run\(`)

// runtimeContractRunERe 匹配对清单执行入口的调用 `runtimecontract.RunE(`。
var runtimeContractRunERe = regexp.MustCompile(`\bruntimecontract\.RunE\(`)

// runtimeContractBuildRe 匹配 e2e 腿脚本里对执行器的构建（go build
// ./e2e/runtimecontract）。
var runtimeContractBuildRe = regexp.MustCompile(`(?m)^\s*(?:[A-Z_]+="[^"]*"\s+|[A-Z_]+=\S+\s+)*go build\b.*\./e2e/runtimecontract\b`)

// runtimeContractExecRe 匹配 e2e 腿脚本里对执行器的运行（docker exec …
// runtimecontract）——只查子串不够：docker cp 目标路径同样含执行器名。
var runtimeContractExecRe = regexp.MustCompile(`(?m)^\s*docker exec\b.*\bruntimecontract\b`)

// runtimeContractCasePrefix 是清单用例常量的命名前缀。
const runtimeContractCasePrefix = "Case"

// runtimeContractContractFile 返回清单本体路径。
func runtimeContractContractFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "internal", "testsupport", "runtimecontract", "contract.go")
}

// parseGoFile 解析单个 Go 文件。
func parseGoFile(t *testing.T, path string) (*ast.File, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, data, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f, string(data)
}

// runtimeContractDeclaredCases 返回 contract.go 里声明的用例常量（名 → 字面
// 值）。只收带字符串字面值的声明——CaseNames() 导出的是值，对账按值比。
func runtimeContractDeclaredCases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, runtimeContractCasePrefix) || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					out[name.Name], _ = strconv.Unquote(lit.Value)
				}
			}
		}
	}
	return out
}

// runtimeContractCasesUsed 返回函数 fn 体内引用到的、属 declared 集的用例
// （常量名 → 字面值；fn 不存在返回空）。
func runtimeContractCasesUsed(f *ast.File, fn string, declared map[string]string) map[string]string {
	out := map[string]string{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if v, ok := declared[id.Name]; ok {
					out[id.Name] = v
				}
			}
			return true
		})
	}
	return out
}

// runtimeContractSortedNames 返回用例集的排序字面值（对账比较键）。
func runtimeContractSortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRuntimeContractChecklistZeroAbsence：用例常量集、RunE 的 cases 表与
// CaseNames() 导出面三处同一——任一处缺席即红（用例增删不同步）。
func TestRuntimeContractChecklistZeroAbsence(t *testing.T) {
	f, _ := parseGoFile(t, runtimeContractContractFile(t))
	declared := runtimeContractDeclaredCases(f)
	if len(declared) < 4 {
		t.Fatalf("only %d contract cases declared (%v) — the parse stopped seeing the const block, or the checklist shrank below its designed four (ensure-idempotent/remove-idempotent/scale-only/wire-gen-stable)", len(declared), runtimeContractSortedNames(declared))
	}
	decl := runtimeContractSortedNames(declared)
	if used := runtimeContractSortedNames(runtimeContractCasesUsed(f, "RunE", declared)); !sameStringSet(decl, used) {
		t.Errorf("RunE's case table is out of step with the declared cases: declared %v, executed %v — every declared case must execute (and no undeclared name may execute)", decl, used)
	}
	if used := runtimeContractSortedNames(runtimeContractCasesUsed(f, "CaseNames", declared)); !sameStringSet(decl, used) {
		t.Errorf("CaseNames() is out of step with the declared cases: declared %v, exported %v", decl, used)
	}
	exported := append([]string(nil), runtimecontract.CaseNames()...)
	sort.Strings(exported)
	if !sameStringSet(decl, exported) {
		t.Errorf("runtimecontract.CaseNames() %v != declared %v", exported, decl)
	}
	if runtimecontract.ChecklistName() == "" {
		t.Error("runtimecontract.ChecklistName() must be a non-empty checklist anchor")
	}
}

// TestRuntimeContractRunsOnBothSides：两侧都必须消费清单本体，e2e 腿必须
// 真构建并运行执行器。
func TestRuntimeContractRunsOnBothSides(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		// Fake 侧两份假底座：进程内夹具 + 引擎白盒。
		"internal/testsupport/apitest",
		"internal/application/engine",
	} {
		found := false
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(rel)), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if runtimeContractRunRe.Match(data) {
				found = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Errorf("no test under %s runs the runtime contract checklist (runtimecontract.Run) — the fake runtime used by this package's tests is no longer pinned to the same assertions the real swarm passes", rel)
		}
	}

	_, src := parseGoFile(t, filepath.Join(root, "e2e", "runtimecontract", "main.go"))
	if !runtimeContractRunERe.MatchString(src) {
		t.Error("e2e/runtimecontract/main.go must execute the checklist via runtimecontract.RunE — the real-swarm side must run the same checklist body as the fake side")
	}

	script, err := os.ReadFile(filepath.Join(root, "e2e", "dind-runtime-contract.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeContractBuildRe.Match(script) {
		t.Error("e2e/dind-runtime-contract.sh no longer builds the executor (go build ./e2e/runtimecontract)")
	}
	if !runtimeContractExecRe.Match(script) {
		t.Error("e2e/dind-runtime-contract.sh no longer runs the executor (docker exec … runtimecontract)")
	}

	mise, err := os.ReadFile(filepath.Join(root, "mise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mise), "sh e2e/dind-runtime-contract.sh") {
		t.Error("no mise task runs e2e/dind-runtime-contract.sh — the real-swarm side is unreachable from the task runner (CI coverage is reconciled by e2eciaccount)")
	}
}
