package guards

// TestEngineCoreFuseBaseline：真核熔断线（ADR-0088 后果段/验收锚「P4 收官」
// ——基线于 P4 收官冻结〔2026-11-01，commit d4ad5f71 时点〕，P5 只加不减）。
//
// 判据：engine 包（internal/application/engine）全部非测试 .go 的 FuncDecl
// 计数（方法+包级函数，含卫星 struct 方法——包级计数封死「挂已嵌入 struct
// 新方法」与「新 struct 自立」两条绕道，接收者字面计数在嵌入改形后失效）
// 超基线即红。基线值 579；上涨须 ADR 显式修订本守卫并带理由（真核膨胀
// 20% 熔断语义的机械承载面；新域动词应落卫星/新域——生长律，ADR-0088
// 决策 5）。
//
// 防盲：0 文件即 fatal；文件数下限 60（P4 收官 66 文件口径余量）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// engineCoreFuseBaseline 是 P4 收官冻结的 FuncDecl 基线（2026-11-01）。
const engineCoreFuseBaseline = 579

// engineCoreFuseMinFiles 是防盲下限（engine 非测试文件数；实测 66（P4
// 收官）→ 65（wave-1 logpipes 迁出）→ 59（wave-2 execs/metrics 迁出），
// 阈值 40 给剩余五卫星拆包波次留余量——每波拆包同批重测重锚）。
const engineCoreFuseMinFiles = 40

func TestEngineCoreFuseBaseline(t *testing.T) {
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))
	count, files := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			if _, ok := decl.(*ast.FuncDecl); ok {
				count++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < engineCoreFuseMinFiles {
		t.Fatalf("engine package scan found only %d non-test files — guard is blind, fix the scan", files)
	}
	if count > engineCoreFuseBaseline {
		t.Errorf("engine core package holds %d FuncDecls, exceeding the frozen baseline %d (P4 close, 2026-11-01): the core may only grow via ADR amendment — new domain verbs belong in satellites/new domains (growth law, ADR-0088 decision 5)", count, engineCoreFuseBaseline)
	}
}
