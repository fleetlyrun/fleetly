package guards

// 事实 Team 轴纪律守卫（R08-M8/S14-c7）：资源轴事实的 Team 必须来自
// factOf 的 fail-closed 解析——空 Team 行按 outbox 过滤口径对**全部 Team**
// 可见，任何"吞掉解析失败"的形态都是跨队事件泄漏。
//
// 三条静态断言（fleetlygrpc 包，非测试文件）：
//  1. factOf 调用必须是双值赋值（`fact, err := ...`）：单值使用意味着错误
//     被丢弃（或未来有人改回"解析失败吞成 ''"形态）；
//  2. 显式 `TeamID: ""` 字面量必须带理由注释（平台级事实的白名单形态是
//     platformFact + 注释，不是散落的空串）；
//  3. 盲区锚：factOf 调用点与 FactRow 字面量数量下限 + platformFact 至少
//     一处使用（扫描失明或平台级构造口消失即红）。

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

// factTeamScanDir 是被扫描的包目录（写入路径的事实发射面单源）。
const factTeamScanDir = "internal/interfaces/grpc/fleetlygrpc"

// TestFactTeamAxisFailClosed：factOf 双值使用 + 空 Team 字面量理由 + 盲区锚。
func TestFactTeamAxisFailClosed(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, filepath.FromSlash(factTeamScanDir))
	fset := token.NewFileSet()
	var (
		factOfCalls int
		factRows    int
		platformUse int
		problems    []string
	)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		// 父链（复用 factdual 守卫的 nodeChildren）。
		parent := map[ast.Node]ast.Node{}
		ast.Inspect(file, func(n ast.Node) bool {
			for _, child := range nodeChildren(n) {
				parent[child] = n
			}
			return true
		})
		commentLines := map[int]bool{}
		for _, cg := range file.Comments {
			for _, c := range cg.List {
				for ln := fset.Position(c.Pos()).Line; ln <= fset.Position(c.End()).Line; ln++ {
					commentLines[ln] = true
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// 调用形态：包级 helper `platformFact(...)` 与既有 repo 方法
			// `x.factOf(...)`。
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				name = fun.Sel.Name
			}
			switch name {
			case "factOf":
				factOfCalls++
				// 向上找最近的语句：必须是双值赋值。
				for p := parent[call]; p != nil; p = parent[p] {
					if assign, ok := p.(*ast.AssignStmt); ok {
						if len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
							problems = append(problems, fmt.Sprintf("%s:%d factOf must be used as a two-value assignment (fact, err := ...) so ownership failures cannot be swallowed", filepath.ToSlash(rel), fset.Position(call.Pos()).Line))
						}
						break
					}
					if _, ok := p.(*ast.FuncDecl); ok {
						break
					}
				}
			case "platformFact":
				platformUse++
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isFactRowLiteral(lit) {
				return true
			}
			factRows++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "TeamID" {
					continue
				}
				basic, ok := kv.Value.(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING || basic.Value != `""` {
					continue
				}
				line := fset.Position(kv.Pos()).Line
				// 理由窗口：字面量前后 4 行内的非空注释（平台级理由写在
				// 声明处；空白注释不算）。
				reason := false
				for ln := line - 4; ln <= line+4; ln++ {
					if commentLines[ln] {
						reason = true
						break
					}
				}
				if !reason {
					problems = append(problems, fmt.Sprintf("%s:%d writes.FactRow sets TeamID to the empty string without a rationale comment — platform-level facts must go through platformFact with a stated reason; resource-axis facts must come from factOf (R08-M8)", filepath.ToSlash(rel), line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 盲区锚：扫描面少于下限即红（文件移动/重命名会让本守卫失明）。
	if factOfCalls < 20 {
		t.Fatalf("factOf scan found only %d call sites — guard is blind or the write surface moved; fix the scan", factOfCalls)
	}
	if factRows < 20 {
		t.Fatalf("FactRow scan found only %d literals — guard is blind; fix the scan", factRows)
	}
	if platformUse < 1 {
		t.Fatalf("platformFact is not used anywhere in %s — the explicit platform-level fact constructor disappeared (empty-Team facts would have no whitelisted shape)", factTeamScanDir)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Errorf("%s", p)
	}
}

// isFactRowLiteral 判定复合字面量是否为 writes.FactRow{...}。
func isFactRowLiteral(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "writes" && sel.Sel.Name == "FactRow"
}
