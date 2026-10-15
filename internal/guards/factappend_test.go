package guards

// Append 白名单（P2-4 守卫 ①，ADR-0064 →收敛报告 §刀1）：outbox.Repo.Append
// 与 audit.Repo.Append 的生产调用面只准事实发射 kernel 自身
// （internal/domain/services/writes）。双行扇出（流面 + 取证面、两轴
// 分立、flag 语义、惰性求值）是 kernel 拥有的不变量——绕过 kernel 直调
// Repo 就是在 kernel 外手搓第二套事实发射，四族 CRUD 漏声明事件的漂移史
// （2026-10-03）正是这么长出来的。
//
// 机制：AST 扫描全部生产 .go（排除 _test.go、生成物与守卫自身）的选择器
// 调用 `.Append(`。实测（2026-10-15 批三落扫描）：生产面 Append 方法仅
// 存在于这两个 Repo 接口（persistence 实现是方法声明非选择器调用；其余
// 模块零命中），故选择器命中即 repo 味，kernel 目录之外一律红；第三方
// 类型未来出现同名方法走豁免表带理由。测试文件不在执法面（repo 自测/
// apitest 夹具按本义直调落行），statetest 夹具经 kernel 组合，同不命中。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// factKernelDir 是唯一允许直调 outbox/audit Append 的生产目录。
const factKernelDir = "internal/domain/services/writes"

// appendExemptions 是 rel 路径 → 理由（kernel 之外的 `.Append(` 直调）。
// 当前为空：生产直调面只剩 kernel 自身。豁免不再命中即红（双向保鲜）；
// 第三方类型的 Append 方法在此登记并说明为什么它不是事实发射第二源。
var appendExemptions = map[string]string{}

// appendCall 是一处 `.Append(` 选择器调用命中。
type appendCall struct {
	rel      string
	line     int
	receiver string // 接收者表达式文本（诊断面）
}

// scanAppendCalls 枚举生产 .go 的全部 `.Append(` 选择器调用（AST 级，
// 注释免疫；排除测试与生成物——与 scanGoFiles 同口径但需全语法树）。
func scanAppendCalls(t *testing.T) []appendCall {
	t.Helper()
	root := repoRoot(t)
	var calls []appendCall
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".go") || !scanGoModulePath(rel) {
			return nil
		}
		if generatedFile(rel) || strings.HasPrefix(rel, "internal/guards/") {
			return nil
		}
		if strings.HasSuffix(rel, "_test.go") {
			return nil // 生产调用面执法：夹具/自测不在面上
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Append" {
				return true
			}
			calls = append(calls, appendCall{
				rel:      rel,
				line:     fset.Position(call.Pos()).Line,
				receiver: renderExpr(t, sel.X),
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("append scan found no call sites — guard is blind, fix the scan")
	}
	return calls
}

// renderExpr 把接收者表达式渲染为文本（诊断面；不做语义解析）。
func renderExpr(t *testing.T, e ast.Expr) string {
	t.Helper()
	var sb strings.Builder
	fset := token.NewFileSet()
	if err := printer.Fprint(&sb, fset, e); err != nil {
		return "?"
	}
	return sb.String()
}

// TestFactAppendWhitelist：`.Append(` 生产调用面 × kernel 白名单对账。
func TestFactAppendWhitelist(t *testing.T) {
	calls := scanAppendCalls(t)

	kernelCalls := 0
	usedExemptions := map[string]bool{}
	var violations []string
	for _, c := range calls {
		if strings.HasPrefix(c.rel, factKernelDir+"/") {
			kernelCalls++
			continue
		}
		if reason, ok := appendExemptions[c.rel]; ok && reason != "" {
			usedExemptions[c.rel] = true
			continue
		}
		violations = append(violations, sprintfAppendViolation(c))
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s", v)
	}
	// 双向保鲜：豁免不再命中即红；空理由即红。
	for rel, reason := range appendExemptions {
		if reason == "" {
			t.Errorf("append exemption %s must carry a reason", rel)
		}
		if !usedExemptions[rel] {
			t.Errorf("append exemption %s no longer matches a real .Append( call; remove the entry", rel)
		}
	}

	// 防盲：kernel 自身必须仍有直调面（扇出双行的两个落库点）。kernel
	// 重构到零命中说明扫描器或 kernel 形态漂了，两头都要查。
	if kernelCalls < 2 {
		t.Errorf("fact kernel %s should own the outbox/audit Append call sites (found %d — guard or kernel drifted)", factKernelDir, kernelCalls)
	}
}

// sprintfAppendViolation 组装红灯诊断文案。
func sprintfAppendViolation(c appendCall) string {
	return fmt.Sprintf("%s:%d calls %s.Append directly — the fact-emitting kernel (internal/domain/services/writes) owns outbox/audit Append; declare a writes.FactRow and go through Sequence/Emit, or exempt with a reason (P2-4 guard ①)", c.rel, c.line, c.receiver)
}
