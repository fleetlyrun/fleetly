package guards

// Task 行铸造单源反扫（ADR-0062 同批守卫）：Task 行装配（state/task.Task
// 复合字面）在非测试代码里只许出现在铸造 module minttask.go——身份注入
// （TaskRef/属主）、spec 验证、protojson 冻结、Form 推导、one-shot 缺省并发
// 与 DNS 铸名的行形状知识单源，受理位与域铸造壳都经 engine.MintTaskRow
// 取行。新铸造调用点不得另开行装配点（2026-10-11 前曾在受理位与铸造壳各
// 手搓一份，Form 推导已分叉出两种写法）。
//
// 判定按 go/ast 复合字面类型精判（`map[string][]*task.Task{}` 之类的元素
// 类型字面不误报），包名按各文件对 task 包（P1b 起行类型单源住
// domain/automation/task——persistence/task 已不持行类型，批一迁移期的
// 旧路径双匹配随批二收口，"迁完即收"）的真实 import 别名解析——改别名
// 不静默漏扫。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rowMintAllowances 是允许 Task 行复合字面的非测试文件 → 理由。豁免不再
// 命中即红（与受理面豁免表同款双向保鲜）。
var rowMintAllowances = map[string]string{
	"internal/application/engine/minttask.go": "铸造 module 本体：MintTaskRow 是行装配唯一所在（ADR-0062）",
}

func TestTaskRowMintingSingleSite(t *testing.T) {
	root := repoRoot(t)
	used := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// 生成物/前端/独立 module 不持 state 聚合行；跳过避免误扫。
			switch d.Name() {
			case ".git", ".tmp-bin", "genproto", "console", "node_modules", "dist", "sdk":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(path[len(root)+1:])
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		// 该文件内 task 包的本地名（import 别名或缺省 task；P1b 后行类型
		// 单源在 domain/automation/task）。
		local := ""
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasSuffix(path, "/internal/domain/automation/task") {
				local = "task"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" {
			return nil
		}
		hit := false
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if sel, ok := lit.Type.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == local && sel.Sel.Name == "Task" {
					hit = true
				}
			}
			return true
		})
		if !hit {
			return nil
		}
		if _, ok := rowMintAllowances[rel]; ok {
			used[rel] = true
			return nil
		}
		t.Errorf("%s hand-assembles a Task row (%s.Task{...}); identity injection, spec validation/freeze, form derivation, and DNS minting live only in engine.MintTaskRow (ADR-0062) — call it instead of shaping rows at the call site", rel, local)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, reason := range rowMintAllowances {
		if !used[rel] {
			t.Errorf("row-minting allowance %s no longer matches a real site; remove the entry (%s)", rel, reason)
		}
	}
}
