package guards

// Team 轴守卫（ADR-0028 验收锚）：装配/引擎零 "default" 团队字面量——
// 域解析（appTeam/taskTeam/resolveBackend/activeProjectNetworks 同族）一律
// 从 Project 行实取 team_id。AST 级扫描（注释免疫）：engine 与 assembly
// 包的非测试源码不得出现值为 "default" 的字符串字面量；缺省归属是 identity
// 包的 DefaultTeamID（api 面缺省约定），不属引擎语义。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestEngineAssemblyNoDefaultTeamLiteral：engine/assembly 零 "default"
// 字面量（ADR-0028 决策 1 的可静态执法承诺）。
func TestEngineAssemblyNoDefaultTeamLiteral(t *testing.T) {
	root := repoRoot(t)
	scanned := 0
	for _, pkg := range []string{filepath.Join("internal", "engine"), filepath.Join("internal", "assembly")} {
		err := filepath.WalkDir(filepath.Join(root, pkg), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if generatedFile(rel) {
				return nil
			}
			scanned++
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"default"` {
					t.Errorf("%s contains a \"default\" string literal; team resolution must read the project row (ADR-0028) and api defaults belong to identity.DefaultTeamID", rel)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("team-axis guard scanned no files — guard is blind, fix the scan")
	}
}
