package guards

// FactRow 双真纪律守卫（P2-4 守卫 ②，ADR-0064 →收敛报告 §刀1）：默认
// 双真（outbox 流面行 + audit 取证面行同源落库）是事实发射 kernel 拥有
// 的不变量；偏离双真（LedgerOnly/StreamOnly）必须在声明处注释理由。
// 批一/批二/批三 A 化把全部调用点收编为 kernel 单声明后，单侧旗就是
// 唯一的"两行变一行"显式口子——无理由的旗 = 无声丢行（订阅面或取证
// 面悄悄缺一条），这正是四族 CRUD 漏声明事件漂移史的回潮形态。
//
// 机制（低门槛存在性断言，与 wording 豁免同风格）：AST 全语法树扫描
// （含测试——flag 语义探针同受纪律）两类带旗形态：
//  1. FactRow 字面量的 `LedgerOnly: true` / `StreamOnly: true` 键值对；
//  2. `row.LedgerOnly = true` 赋值形态（commitTransition 前门的原地迁移
//     分支）。
// 窗口 = 字面量开括号行（或赋值行）向上攀爬：紧邻注释块 + 祖先包装层
// 开行（闭包/外层字面量）皆可穿——覆盖两种既成注释风格（旗行上方注释、
// 闭包前导注释）；窗口内存在任一非空注释即绿。豁免表按 rel:line 登记
// 带理由；不再命中即红（双向保鲜）。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// factFlags 是偏离双真的两面旗（writes.FactRow 专有字段名）。
var factFlags = map[string]bool{"LedgerOnly": true, "StreamOnly": true}

// factDualExemptions 是 "rel路径:行号" → 理由（窗口内无理由注释的带旗
// 声明）。当前为空：全部带旗点已有理由注释。行号漂移即失效（红）——
// 豁免必须随声明移动同批维护，这是有意的保鲜压力。
var factDualExemptions = map[string]string{}

// factFlagSite 是一处带旗声明（key-value 字面量或赋值）。
type factFlagSite struct {
	rel    string
	line   int
	flag   string
	reason bool // 窗口内已带非空注释
}

// scanFactFlagSites 全语法树扫描带旗声明并裁决理由注释在场性。
func scanFactFlagSites(t *testing.T) []factFlagSite {
	t.Helper()
	root := repoRoot(t)
	var sites []factFlagSite
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
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		// 注释行集合（含尾注释——按行裁决即可覆盖旗行同行的尾注释）。
		commentLines := map[int]bool{}
		for _, cg := range file.Comments {
			for _, c := range cg.List {
				for ln := fset.Position(c.Pos()).Line; ln <= fset.Position(c.End()).Line; ln++ {
					commentLines[ln] = true
				}
			}
		}
		// 父链与祖先开行集合（攀爬窗口用）。
		parent := map[ast.Node]ast.Node{}
		ast.Inspect(file, func(n ast.Node) bool {
			for _, child := range nodeChildren(n) {
				parent[child] = n
			}
			return true
		})
		ancestorOpens := map[int]bool{}
		markAncestorOpens := func(n ast.Node) {
			for p := parent[n]; p != nil; p = parent[p] {
				switch a := p.(type) {
				case *ast.CompositeLit:
					ancestorOpens[fset.Position(a.Lbrace).Line] = true
				case *ast.FuncLit:
					if a.Body != nil {
						ancestorOpens[fset.Position(a.Body.Lbrace).Line] = true
					}
				case *ast.FuncDecl:
					return // 函数边界为顶：再向上是整函数域，窗口失真
				}
			}
		}
		// 窗口起点向上攀爬：紧邻注释块与祖先包装层开行皆可穿。
		windowStart := func(start int) int {
			for i := 0; i < 40; i++ { // 上界防御（最深嵌套远小于此）
				above := start - 1
				if commentLines[above] || ancestorOpens[above] {
					start = above
					continue
				}
				break
			}
			return start
		}
		hasReason := func(from, to int) bool {
			for ln := from; ln <= to; ln++ {
				if commentLines[ln] {
					return true
				}
			}
			return false
		}

		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				key, ok := node.Key.(*ast.Ident)
				if !ok || !factFlags[key.Name] {
					return true
				}
				if val, ok := node.Value.(*ast.Ident); !ok || val.Name != "true" {
					return true // 仅执法显式 true 字面量（false/变量不在偏离形态）
				}
				flagLine := fset.Position(node.Pos()).Line
				markAncestorOpens(node)
				lit, ok := parent[node].(*ast.CompositeLit)
				if !ok {
					return true
				}
				start := windowStart(fset.Position(lit.Lbrace).Line)
				sites = append(sites, factFlagSite{
					rel: rel, line: flagLine, flag: key.Name,
					reason: hasReason(start, flagLine),
				})
			case *ast.AssignStmt:
				if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
					return true
				}
				sel, ok := node.Lhs[0].(*ast.SelectorExpr)
				if !ok || !factFlags[sel.Sel.Name] {
					return true
				}
				if val, ok := node.Rhs[0].(*ast.Ident); !ok || val.Name != "true" {
					return true
				}
				flagLine := fset.Position(node.Pos()).Line
				markAncestorOpens(node)
				start := windowStart(flagLine)
				sites = append(sites, factFlagSite{
					rel: rel, line: flagLine, flag: sel.Sel.Name,
					reason: hasReason(start, flagLine),
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) < 15 {
		t.Fatalf("fact dual-truth scan found only %d flag sites — guard is blind, fix the scan", len(sites))
	}
	return sites
}

// nodeChildren 枚举直接子节点（父链构建用；只覆盖含声明体的容器节点）。
func nodeChildren(n ast.Node) []ast.Node {
	var out []ast.Node
	switch v := n.(type) {
	case *ast.File:
		for _, d := range v.Decls {
			out = append(out, d)
		}
	case *ast.FuncDecl:
		if v.Body != nil {
			out = append(out, v.Body)
		}
	case *ast.FuncLit:
		out = append(out, v.Body)
	case *ast.BlockStmt:
		out = append(out, anyNodes(v.List)...)
	case *ast.IfStmt:
		out = append(out, v.Cond, v.Body)
		if v.Else != nil {
			out = append(out, v.Else)
		}
	case *ast.ForStmt:
		if v.Cond != nil {
			out = append(out, v.Cond)
		}
		out = append(out, v.Body)
	case *ast.RangeStmt:
		out = append(out, v.X, v.Body)
	case *ast.SwitchStmt:
		if v.Tag != nil {
			out = append(out, v.Tag)
		}
		out = append(out, v.Body)
	case *ast.CaseClause:
		out = append(out, anyNodes(v.List)...)
		out = append(out, anyNodes(v.Body)...)
	case *ast.SelectStmt:
		out = append(out, v.Body)
	case *ast.CommClause:
		out = append(out, anyNodes(v.Body)...)
	case *ast.ReturnStmt:
		out = append(out, anyNodes(v.Results)...)
	case *ast.AssignStmt:
		out = append(out, anyNodes(v.Lhs)...)
		out = append(out, anyNodes(v.Rhs)...)
	case *ast.ExprStmt:
		out = append(out, v.X)
	case *ast.DeclStmt:
		out = append(out, v.Decl)
	case *ast.CallExpr:
		out = append(out, v.Fun)
		out = append(out, anyNodes(v.Args)...)
	case *ast.SelectorExpr:
		out = append(out, v.X)
	case *ast.ParenExpr:
		out = append(out, v.X)
	case *ast.StarExpr:
		out = append(out, v.X)
	case *ast.UnaryExpr:
		out = append(out, v.X)
	case *ast.BinaryExpr:
		out = append(out, v.X, v.Y)
	case *ast.IndexExpr:
		out = append(out, v.X, v.Index)
	case *ast.SliceExpr:
		out = append(out, v.X)
	case *ast.TypeAssertExpr:
		out = append(out, v.X)
	case *ast.CompositeLit:
		out = append(out, anyNodes(v.Elts)...)
	case *ast.KeyValueExpr:
		out = append(out, v.Key, v.Value)
	case *ast.GenDecl:
		out = append(out, anyNodes(v.Specs)...)
	case *ast.ValueSpec:
		out = append(out, anyNodes(v.Values)...)
	}
	return out
}

// anyNodes 把 []ast.Stmt / []ast.Expr / []ast.Spec 等节点切片归一为
// []ast.Node（父链构建用；三者皆实现 ast.Node）。
func anyNodes[T ast.Node](xs []T) []ast.Node {
	out := make([]ast.Node, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// TestFactDualTruthDiscipline：全部 LedgerOnly/StreamOnly 带旗声明 ×
// 理由注释在场性对账。
func TestFactDualTruthDiscipline(t *testing.T) {
	sites := scanFactFlagSites(t)

	usedExemptions := map[string]bool{}
	var missing []string
	for _, s := range sites {
		if s.reason {
			continue
		}
		key := fmt.Sprintf("%s:%d", s.rel, s.line)
		if reason, ok := factDualExemptions[key]; ok && reason != "" {
			usedExemptions[key] = true
			continue
		}
		missing = append(missing, fmt.Sprintf("%s:%d %s has no deviation rationale comment in its declaration window — dual-truth (outbox stream row + audit ledger row) is the kernel invariant; a flag without a stated reason silently drops one row (P2-4 guard ②)", s.rel, s.line, s.flag))
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s", m)
	}

	// 双向保鲜：豁免不再命中即红；空理由即红。
	for key, reason := range factDualExemptions {
		if reason == "" {
			t.Errorf("fact dual-truth exemption %s must carry a reason", key)
		}
		if !usedExemptions[key] {
			t.Errorf("fact dual-truth exemption %s no longer matches an uncommented flag site; remove the entry", key)
		}
	}
}
