package guards

// 诊断 facts 装配零旁路守卫（B2a W2，zeabur 借鉴设计 §2.4：「facts 经
// 导出面装配」——obs 私有观测缓存的唯一合法出口是导出面
// ObservationSnapshot/WorkloadProcess）：AST 级扫描装配文件
// internal/application/diagnosis/diagnose.go。
//
// P5 wave-6 拆包前：装配器与 obs 同包（engine），旁路只有本守卫能红
//（编译器对同包私有无免疫）。拆包后：跨包私有由编译器执法（卫星结构
// 性不可名 e.obs 等未导出域状态），本守卫的常驻职责收敛为两件——
// ① 绿锚：装配文件必须在场且实际经 observationSnapshot 端口取观测
//（文件消失/改名/端口不再使用 = 守卫失明，红）；② 回归装甲：装配器
// 禁触观测姊妹域状态名（obs/drift/expect/…——若卫星未来添字段撞名并
// 直触即红，零旁路纪律的文档化判据）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// diagnoseAssemblyFile 是诊断 facts 装配文件的仓内路径（守卫的扫描对象；
// 改名须同批改这里——文件缺席即红，不留静默通道）。P5 wave-6 拆包：
// engine/diagnose.go → diagnosis 卫星包（ADR-0088 决策 1 终态第 9 星）。
const diagnoseAssemblyFile = "internal/application/diagnosis/diagnose.go"

// bannedAssemblyFields 是装配文件禁触的观测姊妹域状态字段名（观测缓存
// 及其姊妹域——facts 面只准经导出面端口/repo 端口，见设计 §2.4；拆包后
// 为回归装甲：编译器执法主面，此处文档化判据防未来字段撞名复燃）。
var bannedAssemblyFields = map[string]string{
	"obs":      "observation cache (exported face port: observationSnapshot / workloadProcess)",
	"drift":    "drift debounce state (not a facts source)",
	"expect":   "expectation anchors (not a facts source)",
	"task":     "task domain state (not a facts source)",
	"managed":  "managed domain state (B2b export face territory)",
	"converge": "convergence domain state (not a facts source)",
	"database": "database domain state (not a facts source)",
	"delivery": "delivery domain state (not a facts source)",
	"metrics":  "metrics domain state (alerts ride the alertRules repo port)",
}

// TestDiagnoseFactsAssemblyZeroBypass：装配文件 AST 扫描——d.<域状态> 形态
// 的旁路访问即红（回归装甲）；observationSnapshot 端口必须真实在用
// （绿锚，防守卫失明）。
func TestDiagnoseFactsAssemblyZeroBypass(t *testing.T) {
	src := readFileLF(t, diagnoseAssemblyFile)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, diagnoseAssemblyFile, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("diagnosis assembly file %s does not parse: %v", diagnoseAssemblyFile, err)
	}

	usesSnapshotPort := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "observationSnapshot" || sel.Sel.Name == "ObservationSnapshot" {
			usesSnapshotPort = true
		}
		// d.<field> 形态：X 是标识符 d（Diagnosis 接收者惯例），Sel 落禁触
		// 字段集。
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != "d" {
			return true
		}
		if why, banned := bannedAssemblyFields[sel.Sel.Name]; banned {
			t.Errorf("%s: diagnosis facts assembly touches domain state field d.%s (%s) — facts must ride the exported-face ports and repo ports only (design 2026-10-30 §2.4 zero-bypass)",
				fset.Position(sel.Pos()), sel.Sel.Name, why)
		}
		return true
	})
	if !usesSnapshotPort {
		t.Errorf("%s: the assembly no longer consumes the observationSnapshot exported-face port — the guard would be blind; keep observations assembly on the exported face (design 2026-10-30 §2.4)", diagnoseAssemblyFile)
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
