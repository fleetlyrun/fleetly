package guards

// G7「页宽钳位律」+ G8「statekit 机械件律」——执法手册 G7/G8 条目的反推
// 形态。
//
// 渊源：G7/G8 条目名已随执法手册底稿失考（同 P4-3 G17 先例），形态由
// P5-1 statekit 收口反推——2026-10-16 persistence 五件共享件普查落地
// internal/infrastructure/persistence/statekit.go 六原子（ClampLimit/
// Placeholders/StateIn/JoinStates/ToAny/TransitWhere）后，本文件把「唯一
// 入口」纪律转为静态执法：
//   - G7：页宽钳位语义全仓单一（<=0 或 >max 回落 def），列表方法不得再
//     手写 `if X <= 0 || X > N { X = M }`（X 名含 limit/Limit）——一律走
//     persistence.ClampLimit；ClampLimit 名字本身只在 statekit.go 定义。
//   - G8：SQL 占位串、状态 CAS 前置检查骨架、状态集文案、[]any 参数铸造
//     与 CAS WHERE 片段各有唯一入口（Placeholders/StateIn/JoinStates/
//     ToAny/TransitWhere）——persistence 生产代码不得再定义本地副本
//     （placeholders/stateIn/joinStates/toAny），不得手搓
//     strings.Repeat("?,") 占位串，不得在 statekit.go 之外重定义
//     TransitWhere。
//
// 扫描面：internal/infrastructure/persistence 全部生产 .go（非 _test、
// 生成物排除；statekit.go 是唯一入口本体，豁免于全部红规——它定义的是
// 导出原子，不是被禁的本地副本）。
//
// 豁免表带理由、双向保鲜（nilfacelaw 范式）：确属例外须显式登记（文件 +
// 锚 snippet + 理由），锚必须在命中节点源文本中出现；条目不再命中即红。
// 现树两表均零豁免。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// statekitScanRoot 是执法面的仓库相对根（persistence 层）。
const statekitScanRoot = "internal/infrastructure/persistence"

// statekitEntry 是唯一入口本体的仓库相对路径——豁免于全部红规。
const statekitEntry = statekitScanRoot + "/statekit.go"

// statekitFile 是一个被扫描的 persistence 生产 .go 文件。
type statekitFile struct {
	rel  string // 仓库相对路径（斜杠分隔）
	fset *token.FileSet
	file *ast.File
	src  []byte // 命中节点源文本提取面（豁免锚对账用）
}

// statekitHit 是一处 statekit 律命中。
type statekitHit struct {
	rel    string // 仓库相对路径
	line   int    // 命中节点行号
	rule   string // 规则名（见各 rule* 常量）
	source string // 命中节点源文本（豁免锚对账面）
}

// 规则名：G7 两条 + G8 三条。
const (
	ruleClampCond    = "G7 hand-written page-width clamp"      // 手写 if X <= 0 || X > N 钳位
	ruleClampDef     = "G7 ClampLimit redefinition"            // statekit.go 之外定义 ClampLimit
	ruleHelperDef    = "G8 statekit local helper redefinition" // 本地 placeholders/stateIn/joinStates/toAny
	ruleRepeatLit    = "G8 hand-rolled placeholder via Repeat" // strings.Repeat("?,") 手搓占位串
	ruleTransitWhere = "G8 TransitWhere redefinition"          // statekit.go 之外定义 TransitWhere
)

// statekitExemption 是一条豁免：锚 snippet 必须在命中节点的源文本中出现。
type statekitExemption struct {
	anchor string // 命中节点源文本中必含的锚（取足以定位的片段）
	reason string // 为何允许例外（评审记录；空理由即红）
}

// clampLawExemptions 是 G7 豁免表（文件 → 豁免条目）。登记纪律：确属例外
// 的手写钳位须文件+锚+理由三项齐备，锚必须命中该 if 语句源文本；条目不再
// 命中即红（双向保鲜）。现树零豁免——页宽钳位已全量收敛到
// persistence.ClampLimit。
var clampLawExemptions = map[string][]statekitExemption{}

// mechanicalLawExemptions 是 G8 豁免表（文件 → 豁免条目）。登记纪律同 G7
// 表。现树零豁免——四包本地 helper 已全量删除、占位串与 CAS WHERE 尾缀
// 已全量走唯一入口。
var mechanicalLawExemptions = map[string][]statekitExemption{}

// scanStatekitFiles 解析 internal/infrastructure/persistence 全部生产 .go
// （非 _test、生成物排除——同 nilfacelaw 裁决：生成物形态由生成模板决定，
// 不靠本守卫执法）。
func scanStatekitFiles(t *testing.T) []statekitFile {
	t.Helper()
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.FromSlash(statekitScanRoot))
	var files []statekitFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(path[len(repoRoot(t))+1:])
		if generatedFile(rel) {
			return nil
		}
		src, readErr := os.ReadFile(path) //nolint:gosec // 守卫扫描仓库自有源文件
		if readErr != nil {
			return readErr
		}
		file, parseErr := parser.ParseFile(fset, path, src, 0)
		if parseErr != nil {
			return parseErr
		}
		files = append(files, statekitFile{rel: rel, fset: fset, file: file, src: src})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("scan found no Go files under internal/infrastructure/persistence — guard is blind, fix the scan")
	}
	return files
}

// statekitIsLimitExpr 报告 e 是否页宽形态的操作数：ident 或 selector 尾名
// 名字含 limit/Limit（f.Limit 同在射程）。
func statekitIsLimitExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return strings.Contains(strings.ToLower(x.Name), "limit")
	case *ast.SelectorExpr:
		return strings.Contains(strings.ToLower(x.Sel.Name), "limit")
	}
	return false
}

// statekitIsIntLit 报告 e 是否整型字面量 v。
func statekitIsIntLit(e ast.Expr, v string) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == v
}

// statekitClampCond 报告条件表达式含 `X <= 0`（X 为页宽形态操作数）。
// 复合条件取保守面：&&/||/括号逐支递归——漏报比误报代价高（律的目标是
// 钳位语义单源）。
func statekitClampCond(cond ast.Expr) bool {
	switch c := cond.(type) {
	case *ast.ParenExpr:
		return statekitClampCond(c.X)
	case *ast.BinaryExpr:
		if c.Op == token.LEQ {
			if statekitIsLimitExpr(c.X) && statekitIsIntLit(c.Y, "0") {
				return true
			}
			if statekitIsLimitExpr(c.Y) && statekitIsIntLit(c.X, "0") {
				return true
			}
		}
		if c.Op == token.LAND || c.Op == token.LOR {
			return statekitClampCond(c.X) || statekitClampCond(c.Y)
		}
	}
	return false
}

// statekitRepeatPlaceholderLit 报告 call 是否 strings.Repeat 且首参含 `"?`
// 形态字符串字面量（手搓占位串的指纹形态）。
func statekitRepeatPlaceholderLit(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Repeat" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "strings" {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "\"?")
}

// statekitDefines 收集文件顶层定义的给定名字 FuncDecl（方法与普通函数同
// 在射程；一名多个定义同语法非法，但不同名各报各的命中）。
func statekitDefines(file *ast.File, names ...string) []*ast.FuncDecl {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var found []*ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && want[fn.Name.Name] {
			found = append(found, fn)
		}
	}
	return found
}

// statekitDefinesTransitWhere 报告文件是否在顶层定义名为 TransitWhere 的
// Const/Var。
func statekitDefinesTransitWhere(file *ast.File) *ast.GenDecl {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if n.Name == "TransitWhere" {
					return gd
				}
			}
		}
	}
	return nil
}

// nodeSource 提取节点源文本（豁免锚对账面；越界记空串）。
func nodeSource(fset *token.FileSet, src []byte, n ast.Node) string {
	start, end := fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset
	if start < 0 || end > len(src) || start >= end {
		return ""
	}
	return string(src[start:end])
}

// statekitLawHitsInFile 对单个文件执法 G7+G8 全部红规，返回全部命中。
// rel == statekitEntry 的文件（唯一入口本体）豁免于全部红规。
func statekitLawHitsInFile(sf statekitFile) []statekitHit {
	var hits []statekitHit
	if sf.rel == statekitEntry {
		return hits
	}
	// G7：手写页宽钳位（IfStmt 条件含 X <= 0，X 名含 limit）。
	ast.Inspect(sf.file, func(n ast.Node) bool {
		ifstmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if statekitClampCond(ifstmt.Cond) {
			hits = append(hits, statekitHit{
				rel:    sf.rel,
				line:   sf.fset.Position(ifstmt.Pos()).Line,
				rule:   ruleClampCond,
				source: nodeSource(sf.fset, sf.src, ifstmt),
			})
		}
		return true
	})
	// G7：statekit.go 之外定义 ClampLimit。
	for _, fn := range statekitDefines(sf.file, "ClampLimit") {
		hits = append(hits, statekitHit{
			rel: sf.rel, line: sf.fset.Position(fn.Pos()).Line, rule: ruleClampDef,
			source: nodeSource(sf.fset, sf.src, fn),
		})
	}
	// G8：本地 helper 副本（placeholders/stateIn/joinStates/toAny）。
	for _, fn := range statekitDefines(sf.file, "placeholders", "stateIn", "joinStates", "toAny") {
		hits = append(hits, statekitHit{
			rel: sf.rel, line: sf.fset.Position(fn.Pos()).Line, rule: ruleHelperDef,
			source: nodeSource(sf.fset, sf.src, fn),
		})
	}
	// G8：strings.Repeat("?,…") 手搓占位串。
	ast.Inspect(sf.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !statekitRepeatPlaceholderLit(call) {
			return true
		}
		hits = append(hits, statekitHit{
			rel: sf.rel, line: sf.fset.Position(call.Pos()).Line, rule: ruleRepeatLit,
			source: nodeSource(sf.fset, sf.src, call),
		})
		return true
	})
	// G8：statekit.go 之外定义 TransitWhere（Const/Var）。
	if gd := statekitDefinesTransitWhere(sf.file); gd != nil {
		hits = append(hits, statekitHit{
			rel: sf.rel, line: sf.fset.Position(gd.Pos()).Line, rule: ruleTransitWhere,
			source: nodeSource(sf.fset, sf.src, gd),
		})
	}
	return hits
}

// statekitCountCalls 统计扫描面上 `persistence.<name>(` 形态的调用点数
// （防盲自检面：迁移落地后调用点数是守卫在场的实证）。
func statekitCountCalls(files []statekitFile, name string) int {
	n := 0
	for _, sf := range files {
		ast.Inspect(sf.file, func(x ast.Node) bool {
			call, ok := x.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if ok && id.Name == "persistence" && sel.Sel.Name == name {
				n++
			}
			return true
		})
	}
	return n
}

// statekitExempted 对账豁免表：命中被有效豁免则登记使用并返回 true。
func statekitExempted(t *testing.T, table map[string][]statekitExemption, used map[string]map[int]bool, h statekitHit) bool {
	t.Helper()
	for i, ex := range table[h.rel] {
		if ex.anchor == "" || !strings.Contains(h.source, ex.anchor) {
			continue
		}
		if used[h.rel] == nil {
			used[h.rel] = map[int]bool{}
		}
		used[h.rel][i] = true
		return true
	}
	return false
}

// statekitFreshnessCheck 双向保鲜：豁免条目必须仍命中真实节点且理由非空。
func statekitFreshnessCheck(t *testing.T, table map[string][]statekitExemption, used map[string]map[int]bool, label string) {
	t.Helper()
	for rel, entries := range table {
		for i, ex := range entries {
			if ex.reason == "" {
				t.Errorf("%s exemption %s#%d must carry a reason", label, rel, i)
			}
			if !used[rel][i] {
				t.Errorf("%s exemption %s (#%d, anchor %q) no longer matches a real violation; remove the entry", label, rel, i, ex.anchor)
			}
		}
	}
}

// TestClampLimitLaw：persistence 生产代码的页宽钳位一律走
// persistence.ClampLimit（唯一入口）——手写 `if X <= 0 || …`（X 名含
// limit/Limit）即红，statekit.go 之外定义 ClampLimit 即红。G7 反推形态
// （渊源见文件头注）。
func TestClampLimitLaw(t *testing.T) {
	files := scanStatekitFiles(t)
	var hits []statekitHit
	for _, sf := range files {
		for _, h := range statekitLawHitsInFile(sf) {
			if h.rule == ruleClampCond || h.rule == ruleClampDef {
				hits = append(hits, h)
			}
		}
	}

	// 防盲自检：persistence 生产文件数实测 40（2026-10-16），阈值 32 留
	// 余量；`persistence.ClampLimit(` 调用点实测 26（17 常量包 19 站点 +
	// freeze/sourceupload/backup×3/outbox/audit 字面量 7 站点），阈值 20
	// ——红时先重测再调值。
	if len(files) < 32 {
		t.Fatalf("persistence scan found only %d production Go files — guard is blind, fix the scan", len(files))
	}
	if calls := statekitCountCalls(files, "ClampLimit"); calls < 20 {
		t.Fatalf("scan found only %d persistence.ClampLimit call sites — guard is blind, fix the scan", calls)
	}

	used := map[string]map[int]bool{}
	var bad []string
	for _, h := range hits {
		if statekitExempted(t, clampLawExemptions, used, h) {
			continue
		}
		bad = append(bad, fmt.Sprintf(
			"%s:%d %s — clamp semantics are single-sourced in persistence.ClampLimit; "+
				"call it instead of hand-writing the <=0/>max fallback (G7 page-width clamp law, P5-1)",
			h.rel, h.line, h.rule))
	}
	sort.Strings(bad)
	for _, v := range bad {
		t.Errorf("%s", v)
	}

	statekitFreshnessCheck(t, clampLawExemptions, used, "page-width clamp law")
}

// TestStatekitMechanicalLaw：persistence 生产代码不得再持 statekit 六原子
// 的本地副本——placeholders/stateIn/joinStates/toAny 本地定义、
// strings.Repeat("?,") 手搓占位串、statekit.go 之外的 TransitWhere 定义
// 全部即红。G8 反推形态（渊源见文件头注）。
func TestStatekitMechanicalLaw(t *testing.T) {
	files := scanStatekitFiles(t)
	var hits []statekitHit
	for _, sf := range files {
		for _, h := range statekitLawHitsInFile(sf) {
			if h.rule == ruleHelperDef || h.rule == ruleRepeatLit || h.rule == ruleTransitWhere {
				hits = append(hits, h)
			}
		}
	}

	// 防盲自检：文件数阈值同 G7；`persistence.Placeholders(` 调用点实测
	// 7（task 2 + deployment 3 + run 2，2026-10-16），阈值 3。
	if len(files) < 32 {
		t.Fatalf("persistence scan found only %d production Go files — guard is blind, fix the scan", len(files))
	}
	if calls := statekitCountCalls(files, "Placeholders"); calls < 3 {
		t.Fatalf("scan found only %d persistence.Placeholders call sites — guard is blind, fix the scan", calls)
	}

	used := map[string]map[int]bool{}
	var bad []string
	for _, h := range hits {
		if statekitExempted(t, mechanicalLawExemptions, used, h) {
			continue
		}
		bad = append(bad, fmt.Sprintf(
			"%s:%d %s — this mechanical shape is single-sourced in persistence statekit "+
				"(Placeholders/StateIn/JoinStates/ToAny/TransitWhere); delete the local copy and call the atom (G8 statekit mechanical law, P5-1)",
			h.rel, h.line, h.rule))
	}
	sort.Strings(bad)
	for _, v := range bad {
		t.Errorf("%s", v)
	}

	statekitFreshnessCheck(t, mechanicalLawExemptions, used, "statekit mechanical law")
}

// TestStatekitLawShapes 是识别精度的用例面（内联夹具逐例断言：手写钳位、
// selector 页宽、本地 helper、手搓占位串、TransitWhere 重定义全红；走唯一
// 入口的合法形态与非页宽名不误报）。
func TestStatekitLawShapes(t *testing.T) {
	// demoRel 是夹具的假想仓库相对路径（不在 statekit.go，即全规在射程）。
	const demoRel = statekitScanRoot + "/demo/repo.go"
	scan := func(t *testing.T, src string) []statekitHit {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, demoRel, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		return statekitLawHitsInFile(statekitFile{rel: demoRel, fset: fset, file: file, src: []byte(src)})
	}
	rulesOf := func(hits []statekitHit) []string {
		var out []string
		for _, h := range hits {
			out = append(out, h.rule)
		}
		return out
	}

	t.Run("G7 手写页宽钳位红", func(t *testing.T) {
		hits := scan(t, `package p
func f(limit int) int {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return limit
}`)
		if len(hits) != 1 || hits[0].rule != ruleClampCond {
			t.Fatalf("hand-written clamp must be flagged, got %v", rulesOf(hits))
		}
	})

	t.Run("G7 selector 页宽形态红", func(t *testing.T) {
		hits := scan(t, `package p
type filter struct{ Limit int }
func g(f filter) int {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 1000
	}
	return f.Limit
}`)
		if len(hits) != 1 || hits[0].rule != ruleClampCond {
			t.Fatalf("selector page-width clamp must be flagged, got %v", rulesOf(hits))
		}
	})

	t.Run("G7 走 ClampLimit 不红", func(t *testing.T) {
		hits := scan(t, `package p
import "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
func h(limit int) int {
	limit = persistence.ClampLimit(limit, 50, 200)
	return limit
}`)
		if len(hits) != 0 {
			t.Fatalf("the single entry point must not be flagged, got %v", rulesOf(hits))
		}
	})

	t.Run("G7 非页宽名不在射程", func(t *testing.T) {
		hits := scan(t, `package p
func w(window int) int {
	if window <= 0 || window > 500 {
		window = 100
	}
	return window
}`)
		if len(hits) != 0 {
			t.Fatalf("non-page-width clamp names must not be flagged, got %v", rulesOf(hits))
		}
	})

	t.Run("G8 本地 placeholders 与手搓占位串红", func(t *testing.T) {
		hits := scan(t, `package p
import "strings"
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}`)
		got := rulesOf(hits)
		if len(hits) != 2 {
			t.Fatalf("want helper-def + repeat hits, got %v", got)
		}
		if got[0] != ruleHelperDef || got[1] != ruleRepeatLit {
			t.Fatalf("unexpected rules %v", got)
		}
	})

	t.Run("G8 stateIn/toAny 本地定义红", func(t *testing.T) {
		hits := scan(t, `package p
func stateIn(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}
func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}`)
		got := rulesOf(hits)
		if len(hits) != 2 || got[0] != ruleHelperDef || got[1] != ruleHelperDef {
			t.Fatalf("local stateIn/toAny defs must be flagged, got %v", got)
		}
	})

	t.Run("G8 TransitWhere 重定义红", func(t *testing.T) {
		hits := scan(t, `package p
const TransitWhere = "WHERE id = ? AND state = ?"`)
		if len(hits) != 1 || hits[0].rule != ruleTransitWhere {
			t.Fatalf("TransitWhere redefinition must be flagged, got %v", rulesOf(hits))
		}
	})

	t.Run("G8 走唯一入口不红", func(t *testing.T) {
		hits := scan(t, `package p
import "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
func q() string {
	return persistence.Placeholders(3)
}`)
		if len(hits) != 0 {
			t.Fatalf("calling the atom must not be flagged, got %v", rulesOf(hits))
		}
	})
}
