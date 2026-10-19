package guards

// 期望账本写点守卫（ADR-0082 §6；R12-M3/S17-c2）。
//
// 律文：`ensuredGen`/`ensuredSpec`/`ensuredBy`（observations 域）与
// `expect.expected`（App 域期望锚）只能由**登记函数**写入——Ensure 路径的
// materialize 单序列（recordSpecs/recordOwners/recordEnsured）与清理面
// （forgetOwner/reconcileOwner/forget/TeardownApp）。禁止观测播种：把
// "看到 running/drift 的载体"直接塞进期望账本会把手动热修播种成期望态，
// 静默击穿 controller-wins（受管域 seedFromRuntime 先例不得照抄到 App 域）。
//
// 判据：AST 扫 internal/application/engine 全部生产 .go，命中
//   - `x.ensuredGen[...] = ` / `x.ensuredSpec[...] = ` / `x.ensuredBy[...] = `
//   - `delete(x.ensuredGen, ...)`（同族三张表）
//   - 整表替换 `x.ensuredGen = make(...)`（只准 init）
//   - `x.expect.expected[...] = ` / `delete(x.expect.expected, ...)`，且
//     index 含 appOwner（App 域；database/system/browse 的期望锚另域自管）
// 后按"字段 × 所在函数"过白名单；越界写点即红。
//
// 防盲：生产文件数/写点数/白名单函数命中数三重下限 + recordSpecs/
// recordEnsured 正向锚；形状用例（临时树）自证红灯与不误伤。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// engineLedgerTree 是期望账本写点的执法树。
const engineLedgerTree = "internal/application/engine"

// ledgerFields 是被执法字段（selector 的 Sel 名）。
var ledgerFields = map[string]bool{
	"ensuredGen":  true,
	"ensuredSpec": true,
	"ensuredBy":   true,
	"expected":    true,
}

// ledgerWriteWhitelist 是字段 × 函数白名单（函数名 → 理由）。整表替换只
// 准构造面（init/New）；元素写按登记函数。名称本身是契约：改名必须同批
// 改守卫（白名单函数命中数有正向锚）。
var ledgerWriteWhitelist = map[string]map[string]string{
	"ensuredGen": {
		"recordSpecs":    "Ensure 路径：materialize 单序列写期望账本（ADR-0082 §6 唯一写点）",
		"recordOwners":   "非 App 域登记：只记归属与 gen（受管域/Database 域不参与 spec 对照）",
		"forgetOwner":    "App 收口清理：按 owner 清账本（TeardownApp 消费）",
		"reconcileOwner": "本代保留集差集清理（BG 退役代/缩减载体）",
		"forget":         "单载体缓存面收口（Database 拆载体等域内动作）",
		"init":           "域内 map 构造（装配期一次）",
	},
	"ensuredSpec": {
		"recordSpecs":          "Ensure 路径：materialize 单序列写投影 spec",
		"recordProjectedSpecs": "posture=detect 的启动账本重建（基线行写入者；不写 gen）",
		"forgetOwner":          "App 收口清理",
		"reconcileOwner":       "本代保留集差集清理",
		"forget":               "单载体缓存面收口",
		"init":                 "域内 map 构造（装配期一次）",
	},
	"ensuredBy": {
		"recordSpecs":          "Ensure 路径：写账本归属部署（ADR-0082 §6 采用规则）",
		"recordProjectedSpecs": "posture=detect 启动账本重建：写入者 = 基线行",
		"forgetOwner":          "App 收口清理",
		"reconcileOwner":       "本代保留集差集清理",
		"forget":               "单载体缓存面收口",
		"init":                 "域内 map 构造（装配期一次）",
	},
	"expected": {
		"recordEnsured": "App 域期望锚：recordEnsured 是 Ensure 事实的唯一写点",
		"TeardownApp":   "App 收口清理：删 App 域期望锚",
		"New":           "引擎构造面 map 初始化（装配期一次）",
	},
}

// ledgerWrite 是一条被识别的账本写点。
type ledgerWrite struct {
	file  string
	line  int
	fn    string
	field string
	kind  string // assign | delete | replace
	// appOwned 只对 expected 有意义：index 里出现 appOwner(...) = App 域
	// 期望锚（执法面）；databaseOwner/systemOwner/browseOwner 另域自管。
	appOwned bool
}

// ledgerFieldOf 解析表达式是否指向受执法字段（穿透 IndexExpr/ParenExpr）。
func ledgerFieldOf(expr ast.Expr) (string, bool) {
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		if ledgerFields[x.Sel.Name] {
			return x.Sel.Name, true
		}
	case *ast.IndexExpr:
		return ledgerFieldOf(x.X)
	case *ast.ParenExpr:
		return ledgerFieldOf(x.X)
	}
	return "", false
}

// containsIdentCall 报告子树里是否出现指定标识符调用（如 appOwner）。
func containsIdentCall(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// ledgerWritesInFile 提取单文件内的全部账本写点（不判白名单）。
func ledgerWritesInFile(fset *token.FileSet, file *ast.File) []ledgerWrite {
	var out []ledgerWrite
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		stack = append(stack, n)
		fn := ""
		for i := len(stack) - 1; i >= 0; i-- {
			if fd, ok := stack[i].(*ast.FuncDecl); ok && fd.Recv != nil {
				fn = fd.Name.Name
				break
			}
		}
		if fn == "" {
			for i := len(stack) - 1; i >= 0; i-- {
				if fd, ok := stack[i].(*ast.FuncDecl); ok {
					fn = fd.Name.Name
					break
				}
			}
		}
		pos := fset.Position(n.Pos())
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok != token.ASSIGN {
				return true
			}
			for _, lhs := range x.Lhs {
				field, ok := ledgerFieldOf(lhs)
				if !ok {
					continue
				}
				w := ledgerWrite{file: pos.Filename, line: pos.Line, fn: fn, field: field, kind: "replace"}
				if idx, isIndex := lhs.(*ast.IndexExpr); isIndex {
					w.kind = "assign"
					w.appOwned = containsIdentCall(idx.Index, "appOwner")
				}
				out = append(out, w)
			}
		case *ast.IncDecStmt:
			if field, ok := ledgerFieldOf(x.X); ok {
				w := ledgerWrite{file: pos.Filename, line: pos.Line, fn: fn, field: field, kind: "assign"}
				if idx, isIndex := x.X.(*ast.IndexExpr); isIndex {
					w.appOwned = containsIdentCall(idx.Index, "appOwner")
				}
				out = append(out, w)
			}
		case *ast.CallExpr:
			id, ok := x.Fun.(*ast.Ident)
			if !ok || id.Name != "delete" || len(x.Args) < 2 {
				return true
			}
			field, ok := ledgerFieldOf(x.Args[0])
			if !ok {
				return true
			}
			out = append(out, ledgerWrite{
				file: pos.Filename, line: pos.Line, fn: fn, field: field, kind: "delete",
				appOwned: containsIdentCall(x.Args[1], "appOwner"),
			})
		}
		return true
	})
	return out
}

// ledgerViolation 判定一条写点是否越界（"" = 合法）。expected 的
// database/system/browse 域期望锚不在 App 账本纪律面（另域自管，写点各自
// 领域函数），只有含 appOwner 的元素写受本守卫执法。
func ledgerViolation(w ledgerWrite) string {
	allowed := ledgerWriteWhitelist[w.field]
	if w.field == "expected" {
		if w.kind == "replace" {
			if _, ok := allowed[w.fn]; ok {
				return ""
			}
			return "whole-map replacement of expect.expected is only allowed in New (engine construction)"
		}
		if !w.appOwned {
			// 元素写：database/system/browse 域的期望锚另域自管，不在 App
			// 账本纪律面。
			return ""
		}
	}
	if reason, ok := allowed[w.fn]; ok && strings.TrimSpace(reason) != "" {
		return ""
	}
	return "write point is outside the ledger registration functions"
}

// scanLedgerFiles 解析一棵树的生产 .go 并汇总写点与文件数。
func scanLedgerFiles(t *testing.T, root, tree string) ([]ledgerWrite, int) {
	t.Helper()
	var writes []ledgerWrite
	files := 0
	err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		files++
		rel, _ := filepath.Rel(root, path)
		for _, w := range ledgerWritesInFile(fset, file) {
			w.file = filepath.ToSlash(rel)
			writes = append(writes, w)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return writes, files
}

// TestExpectedLedgerWritePoints：生产引擎树内账本写点全部过白名单，且
// 白名单函数真实在用（正向锚）。
func TestExpectedLedgerWritePoints(t *testing.T) {
	root := repoRoot(t)
	writes, files := scanLedgerFiles(t, root, engineLedgerTree)
	if files < 40 {
		t.Fatalf("ledger scan parsed only %d production files under %s — guard is blind, fix the scan", files, engineLedgerTree)
	}
	if len(writes) < 16 {
		t.Fatalf("ledger scan found only %d write points (want >= 16) — guard is blind, fix the scan", len(writes))
	}
	usedWhitelisted := map[string]bool{}
	for _, w := range writes {
		if msg := ledgerViolation(w); msg != "" {
			t.Errorf("%s:%d %s on %s — %s (ADR-0082 §6: the expectation ledger is written only by record*/forget*/reconcile* registration functions; observation seeding is forbidden)", w.file, w.line, w.fn, w.field, msg)
			continue
		}
		if _, ok := ledgerWriteWhitelist[w.field][w.fn]; ok {
			usedWhitelisted[w.field+"."+w.fn] = true
		}
	}
	// 正向锚：Ensure 单点与 detect 重建单点必须在场（否则白名单已死，
	// 或扫描面搬走）。
	for _, want := range []string{"ensuredGen.recordSpecs", "ensuredSpec.recordProjectedSpecs", "ensuredBy.recordSpecs", "expected.recordEnsured"} {
		if !usedWhitelisted[want] {
			t.Errorf("positive anchor: whitelisted write point %s was not observed in %s — guard is blind or the ledger contract moved", want, engineLedgerTree)
		}
	}
	if len(usedWhitelisted) < 8 {
		t.Errorf("only %d whitelisted field-function pairs were observed (want >= 8) — guard is blind, fix the scan", len(usedWhitelisted))
	}
	// 双向保鲜：白名单条目必须真在写、且带理由；函数改名/写点搬走后条目
	// 不再命中即红（豁免是债，不是永久免检）。
	for field, funcs := range ledgerWriteWhitelist {
		for fn, reason := range funcs {
			key := field + "." + fn
			if strings.TrimSpace(reason) == "" {
				t.Errorf("ledger whitelist entry %s must carry a reason", key)
			}
			if !usedWhitelisted[key] {
				t.Errorf("ledger whitelist entry %s no longer matches a write point; remove the entry (双向保鲜)", key)
			}
		}
	}
}

// TestExpectedLedgerShape 是守卫本体的形状用例：临时源码树自证"越界写点
// 红 / 登记函数不误伤 / 非 App 域期望锚不在执法面"。
func TestExpectedLedgerShape(t *testing.T) {
	const fixturePkg = "package engine\n"
	cases := []struct {
		name       string
		src        string
		wantClean  bool
		wantDetail string
	}{
		{
			name: "observation seeding is red",
			src: fixturePkg + `
func seedFromObservation(o *observations, w capability.Workload) {
	o.ensuredSpec[w.ID] = w
}
`,
			wantClean:  false,
			wantDetail: "ledger registration functions",
		},
		{
			name: "app expectation seeded outside recordEnsured is red",
			src: fixturePkg + `
func seedExpected(e *Engine, appID string) { e.expect.expected[appOwner(appID)] = 1 }
`,
			wantClean:  false,
			wantDetail: "ledger registration functions",
		},
		{
			name: "whole-map ledger replacement outside init is red",
			src: fixturePkg + `
func resetLedger(o *observations) { o.ensuredGen = map[string]uint64{} }
`,
			wantClean:  false,
			wantDetail: "ledger registration functions",
		},
		{
			name: "non-app expectation anchors are out of the app ledger scope",
			src: fixturePkg + `
func databaseEnsure(e *Engine, id string) { e.expect.expected[databaseOwner(id)] = 1 }
func managedStep(e *Engine, process string) { e.expect.expected[systemOwner(process)] = 1 }
`,
			wantClean: true,
		},
		{
			name: "registered functions are allowed",
			src: fixturePkg + `
func (o *observations) recordSpecs(gen uint64, ws []capability.Workload) {
	for _, w := range ws { o.ensuredGen[w.ID] = gen; o.ensuredSpec[w.ID] = w; o.ensuredBy[w.ID] = "d" }
}
func (o *observations) init() {
	o.ensuredGen = map[string]uint64{}
	o.ensuredSpec = map[string]capability.Workload{}
	o.ensuredBy = map[string]string{}
}
func (o *observations) reconcileOwner(wid string) { delete(o.ensuredSpec, wid) }
func (o *observations) forget(wid string) { delete(o.ensuredGen, wid) }
func (e *Engine) recordEnsured(appID string, gen uint64) { e.expect.expected[appOwner(appID)] = gen }
func (e *Engine) TeardownApp(appID string) { delete(e.expect.expected, appOwner(appID)) }
func New(e *Engine) { e.expect.expected = map[workloadOwner]uint64{} }
`,
			wantClean: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			var violations []string
			for _, w := range ledgerWritesInFile(fset, file) {
				if msg := ledgerViolation(w); msg != "" {
					violations = append(violations, w.field+": "+msg)
				}
			}
			if tc.wantClean {
				if len(violations) > 0 {
					t.Errorf("fixture must stay clean, got violations: %v", violations)
				}
				return
			}
			if len(violations) == 0 {
				t.Errorf("fixture must be flagged as a ledger violation (the guard shape case went blind)")
				return
			}
			if tc.wantDetail != "" && !strings.Contains(strings.Join(violations, " | "), tc.wantDetail) {
				t.Errorf("violation detail must mention %q, got %v", tc.wantDetail, violations)
			}
		})
	}
}
