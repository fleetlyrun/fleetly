package guards

// 诊断 facts 装配零旁路守卫（B2a W2，zeabur 借鉴设计 §2.4：「facts 经
// Engine 导出面装配」——obs 私有观测缓存的唯一合法出口是导出面
// ObservationSnapshot/WorkloadProcess）：AST 级扫描装配文件
// internal/application/engine/diagnose.go，禁直触 Engine 的观测/域状态
// 未导出字段（e.obs/e.drift/e.expect/…）与未导出观测访问器——编译器对
// 跨包私有天然免疫，但装配器与 obs 同包（engine），旁路只有本守卫能红。
// 绿锚双向：装配文件必须在场且实际经 ObservationSnapshot 导出面取观测
// （文件消失/改名/不再使用导出面 = 守卫失明，红）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// diagnoseAssemblyFile 是诊断 facts 装配文件的仓内路径（守卫的扫描对象；
// 改名须同批改这里——文件缺席即红，不留静默通道）。
const diagnoseAssemblyFile = "internal/application/engine/diagnose.go"

// bannedEngineFields 是装配文件禁触的 Engine 未导出域状态字段（观测缓存
// 及其姊妹域——facts 面只准经导出面/repo 端口，见设计 §2.4）。
var bannedEngineFields = map[string]string{
	"obs":      "observation cache (exported face: ObservationSnapshot / WorkloadProcess)",
	"drift":    "drift debounce state (not a facts source)",
	"expect":   "expectation anchors (not a facts source)",
	"task":     "task domain state (not a facts source)",
	"managed":  "managed domain state (B2b export face territory)",
	"converge": "convergence domain state (not a facts source)",
	"database": "database domain state (not a facts source)",
	"delivery": "delivery domain state (not a facts source)",
	"metrics":  "metrics domain state (alerts ride the AlertRules repo port)",
}

// TestDiagnoseFactsAssemblyZeroBypass：装配文件 AST 扫描——e.<域状态> 形态
// 的旁路访问即红；ObservationSnapshot 导出面必须真实在用（绿锚，防守卫
// 失明）。
func TestDiagnoseFactsAssemblyZeroBypass(t *testing.T) {
	src := readFileLF(t, diagnoseAssemblyFile)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, diagnoseAssemblyFile, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("diagnosis assembly file %s does not parse: %v", diagnoseAssemblyFile, err)
	}

	usesExportedSnapshot := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "ObservationSnapshot" {
			usesExportedSnapshot = true
		}
		// e.<field> 形态：X 是标识符 e（Engine 接收者惯例），Sel 落禁触字段集。
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != "e" {
			return true
		}
		if why, banned := bannedEngineFields[sel.Sel.Name]; banned {
			t.Errorf("%s: diagnosis facts assembly touches engine internal field e.%s (%s) — facts must ride the exported faces and repo ports only (design 2026-10-30 §2.4 zero-bypass)",
				fset.Position(sel.Pos()), sel.Sel.Name, why)
		}
		return true
	})
	if !usesExportedSnapshot {
		t.Errorf("%s: the assembly no longer consumes the ObservationSnapshot exported face — the guard would be blind; keep observations assembly on the exported face (design 2026-10-30 §2.4)", diagnoseAssemblyFile)
	}
}

// TestDiagnoseFactsAssemblyFilePresent：绿锚的存在面——装配文件缺席即红
// （改名/移位必须同批改 diagnoseAssemblyFile，不留静默通道）。
func TestDiagnoseFactsAssemblyFilePresent(t *testing.T) {
	// repoRoot 锚 + 路径拼接：与 terminalcounters 守卫同款 repoRoot 面。
	if _, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(diagnoseAssemblyFile))); err != nil {
		t.Fatalf("diagnosis facts assembly file %s is missing — the zero-bypass guard has nothing to scan; if the file moved, update diagnoseAssemblyFile in the same commit (%v)", diagnoseAssemblyFile, err)
	}
}
