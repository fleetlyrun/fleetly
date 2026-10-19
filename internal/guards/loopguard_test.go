package guards

// TestEngineLoopSkeletonSingular：kick/tick 收敛循环骨架全仓唯一一份
// （架构 §0 修正 2："每段只许有一份"——六份循环骨架的历史不再发生）。
//
// R12-L2 换装（2026-10-19）：旧判据是源码文本 `strings.Contains("time.NewTicker")`
// ——把骨架换成 `time.Tick`/`time.After` 环后守卫静默变哑，且无"骨架本体
// 真有一处 ticker"的正向锚。本守卫改为 AST 判据：
//   - `time.NewTicker` / `time.Tick` / `time.AfterFunc` 调用在任何 engine
//     生产文件中出现即红（周期/延迟环形态；注释与字符串不再命中）；
//   - `time.After` 只有出现在 `for`/`for range` 体内才判环（select 超时
//     的 helper 形态如 `sleepCtx` 不误伤）；
//   - 正向锚：`loop.go` 必须真有一处 ticker 构造（NewTicker/Tick），否则
//     骨架承诺已名存实亡——改成 time.After 环也红，而不是零命中即绿；
//   - 防盲：扫到 0 个生产文件即 fatal。
//
// 扫描面仍是 internal/application/engine（架构 §0 的收敛骨架域；Provider/
// 装配/接口层的合理轮询异类不在本律射程，见 R12-L2 报告的建议面与
// e2e/loop 的边界注记）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// engineLoopTree 是收敛骨架唯一性的执法树。
const engineLoopTree = "internal/application/engine"

// loopGuardExemptions 是"非收敛骨架的定时器形态"显式边界表（仓库相对
// 路径 → 理由）：条目不再命中即红（双向保鲜）。当前唯一条目是 exec 会话
// 的**一次性**关闭宽限（ctx.Done 分支里的 time.After，非每拍重建），与
// 周期收敛环语义不同。
var loopGuardExemptions = map[string]string{
	"internal/application/engine/exec.go": "one-shot exec close grace: time.After(execCloseGrace) is created once in the ctx.Done branch (relay teardown wait), not a periodic convergence loop — keep the exemption only while this file has no NewTicker/Tick/AfterFunc",
}

// loopSkeletonFile 是骨架本体（豁免于"自建环"判据，但受正向锚约束）。
const loopSkeletonFile = "loop.go"

// periodCall 报告一个调用表达式是否属于周期环形态，以及是否只在 for 体内
// 才判环（time.After 的 select 超时用法常见于 helper）。
func periodCall(call *ast.CallExpr, inFor bool) (name string, flagged bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return "", false
	}
	switch sel.Sel.Name {
	case "NewTicker", "Tick", "AfterFunc":
		return "time." + sel.Sel.Name, true
	case "After":
		return "time.After", inFor
	}
	return "", false
}

// scanPeriodCalls 用一次 AST 遍历统计文件内的周期环调用：返回（环调用列表,
// 是否出现骨架 ticker 构造）。inFor 深度经节点栈维护（ast.Inspect 的
// f(nil) 在子节点遍历完后回到父节点，正好可弹栈）。
func scanPeriodCalls(file *ast.File) (loops []string, ticker bool) {
	var stack []ast.Node
	inFor := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				switch top.(type) {
				case *ast.ForStmt, *ast.RangeStmt:
					inFor--
				}
			}
			return true
		}
		stack = append(stack, n)
		switch n.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			inFor++
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, flagged := periodCall(call, inFor > 0); flagged {
			loops = append(loops, name)
		}
		if name, _ := periodCall(call, false); name == "time.NewTicker" || name == "time.Tick" {
			ticker = true
		}
		return true
	})
	return loops, ticker
}

// TestLoopGuardASTDiscriminates 是 R12-L2 的形状用例：注释/字符串里的
// time.NewTicker 不算环；for+select 里的 time.After 算环；非循环 select
// 的 After 超时不误伤；AnyTicker 形态与正向锚单独列。退回裸子串判据时
// 第 1 例必红（注释命中）——永久反验锚。
func TestLoopGuardASTDiscriminates(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		wantLoops  string
		wantTicker bool
	}{
		{
			name: "comments and strings do not count",
			src: `package fixture
// time.NewTicker(time.Second) time.Tick(time.Second) time.After(time.Second)
var _ = "time.NewTicker(time.Second)"
`,
			wantLoops:  "",
			wantTicker: false,
		},
		{
			name: "time.After as a loop driver in for+select",
			src: `package fixture
func loop() {
	for {
		select {
		case <-time.After(1):
		}
	}
}
`,
			wantLoops:  "time.After",
			wantTicker: false,
		},
		{
			name: "one-shot select timeout outside a loop is not a loop",
			src: `package fixture
func sleepCtx() {
	select {
	case <-time.After(1):
	}
}
`,
			wantLoops:  "",
			wantTicker: false,
		},
		{
			name: "time.NewTicker is a loop driver anywhere",
			src: `package fixture
func run() {
	t := time.NewTicker(1)
	_ = t
	for {
		select {
		case <-t.C:
		}
	}
}
`,
			wantLoops:  "time.NewTicker",
			wantTicker: true,
		},
		{
			name: "time.Tick and time.AfterFunc are loop drivers",
			src: `package fixture
func run() {
	_ = time.Tick(1)
	_ = time.AfterFunc(1, func() {})
}
`,
			wantLoops:  "time.Tick,time.AfterFunc",
			wantTicker: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			loops, ticker := scanPeriodCalls(file)
			if got := strings.Join(loops, ","); got != tc.wantLoops {
				t.Errorf("period calls = %q, want %q", got, tc.wantLoops)
			}
			if ticker != tc.wantTicker {
				t.Errorf("ticker construct = %v, want %v", ticker, tc.wantTicker)
			}
		})
	}
}

// TestEngineLoopSkeletonSingular：engine 树内周期环只准出现在 loop.go
// （骨架本体），且 loop.go 必须真持有 ticker 构造（正向锚）。
func TestEngineLoopSkeletonSingular(t *testing.T) {
	root := repoRoot(t)
	engineDir := filepath.Join(root, engineLoopTree)
	files := 0
	skeletonTicker := false
	usedExemptions := map[string]bool{}
	err := filepath.WalkDir(engineDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		files++
		loops, ticker := scanPeriodCalls(file)
		if name == loopSkeletonFile {
			skeletonTicker = ticker
			return nil // 骨架本体：正向锚在循环外统一断言
		}
		if len(loops) > 0 {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			// 豁免只覆盖"单一一处 time.After"的一次性宽限形态；同文件若出现
			// NewTicker/Tick/AfterFunc 或第二处 After，仍红（理由文本的机器
			// 执法面——豁免不是整文件免检）。
			if reason, ok := loopGuardExemptions[rel]; ok && strings.TrimSpace(reason) != "" &&
				len(loops) == 1 && loops[0] == "time.After" {
				usedExemptions[rel] = true
				return nil
			}
			t.Errorf("%s builds its own timer/ticker loop (%s) — converge via engine.NewLoop (架构 §0 唯一骨架)", rel, strings.Join(unique(loops), ", "))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatalf("loop skeleton scan found no production files under %s — guard is blind, fix the scan", engineLoopTree)
	}
	// 豁免双向保鲜：理由为空即红；不再命中也红（定时器形态已被移走/重构）。
	for rel, reason := range loopGuardExemptions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("loop guard exemption %s must carry a reason", rel)
		}
		if !usedExemptions[rel] {
			t.Errorf("loop guard exemption %s no longer matches a flagged timer form; remove the entry", rel)
		}
	}
	// 正向锚：骨架本体必须持有 ticker 构造。若未来把 loop.go 改成等价形态
	// （time.After/Tick 环），这里红——守卫必须先换判据再改骨架，不能静默
	// 失去"唯一骨架"承诺。
	if !skeletonTicker {
		t.Errorf("%s no longer constructs a ticker (time.NewTicker/time.Tick) — the skeleton promise is broken or the guard is blind; update the guard judgement before changing the skeleton", engineLoopTree+"/"+loopSkeletonFile)
	}
}
