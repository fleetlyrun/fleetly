package guards

// 环表与 lifecycle 注册点登记制守卫（生长律执法，ADR-0088 决策 5 / 验收锚
// 「生长律守卫」，P5 wave-1 2026-11-01）。
//
// 律文（ADR-0088 决策 5）：「环表与 lifecycle 注册点是 engine 仅有的两个
// 合法生长点」——新域进 engine 只准经这两处挂载，且每条目必须登记（带
// 域锚/ADR 锚）。「来自立域模板批」在 commit 语境不可树内判定，降级为
// 登记制 + 评审可查锚（设计稿 §7 立域模板第 3 条）。
//
// 双锚（决策 5 原文口径）：
//
//	锚① construct.go 的 `e.rings = []ring{...}` 环表条目集——逐条目冻结
//	    （name/loop/step 三元组 + 表序；表序即 DriveOnce 手动驱动序，
//	    schedule 先于 task 是既约语义——换序是行为变更，同批改登记）；
//	锚② lifecycle.go 的 goroutine 挂载集——Start/StartWatch 的全部
//	    go 语句：持 `defer e.wg.Done()` 的受管挂载（driftScanLoop 独立
//	    Run、exec 回环中继挂载等——决策 5 点名的两类）与 bare 挂载
//	    （Stop 排水等待 helper）分册登记。挂载集是「新域私起 goroutine」
//	    的唯一入口面——漏 wg.Add/Done 配对的裸挂载同样在本锚射程。
//
// 判据说理：AST（注释免疫、结构稳健——loopguard R12-L2 教训：文本子串
// 判据在骨架换形后静默变哑；本守卫取 AssignStmt LHS `e.rings` × RHS
// CompositeLit、GoStmt × FuncLit 体的结构位，注释与字符串不参与）。环表
// 基名与字段名（ring/name/loop/step）是 construct.go 在册结构契约，改形
// 即本守卫 fatal（防盲优先于误报）。
//
// 双向保鲜：新条目未登记红（登记须带域锚/ADR 锚）；登记条目消失红
// （环删除/挂载收编必须同批删条目）。防盲：环表字面量缺失 fatal、条目数
// 下限、lifecycle.go 无挂载 fatal、construct.go/lifecycle.go 缺失 fatal。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ringRow 是环表的一条机械提取产物/登记条目（锚①的判据面；anchor 只在
// 登记侧承载域锚/ADR 锚——判据只比 name/loop/step 与表序）。
type ringRow struct {
	name   string
	loop   string
	step   string
	anchor string
}

// lifecycleGoStmt 是 lifecycle.go 的一句 go 语句的机械提取产物（锚②的
// 判据面）：entry 是体内首个非 e.wg.Done 调用的选择链渲染；managed =
// 体内出现 defer e.wg.Done()（受管挂载——Stop 排水覆盖承诺）。
type lifecycleGoStmt struct {
	entry    string
	managed  bool
	position token.Pos
}

// ringRegistry 是锚①的登记表（表序即冻结序——与 construct.go 逐条对位）。
// 每条锚注域语义与 ADR 锚；新增环 = 同批登记（生长律：新域动词落卫星/
// 新域，进核只加环表一行 + 本表一行）。
var ringRegistry = []ringRow{
	{name: "deployment", loop: "e.loop", step: "e.step",
		anchor: "收敛主环：Deployment 状态机单写者驱动（ADR-0016 受理队列；架构 §6 决策读权威表）"},
	{name: "build", loop: "e.buildLoop", step: "e.buildStep",
		anchor: "构建环（F0.9/F1.14 builder 家族；intake 域文件，P4 wave-2b 嵌入提升）"},
	{name: "managed", loop: "e.managedLoop", step: "e.managedStep",
		anchor: "受管 Provider reconciler 环（ADR-0004 单受管 Provider）"},
	{name: "database", loop: "e.databaseLoop", step: "e.databaseStep",
		anchor: "Database 收敛环（ADR-0029 最小数据库集）"},
	{name: "backup", loop: "e.backupLoop", step: "e.backupStep",
		anchor: "Backup 调度/执行/恢复/保留环（ADR-0039 备份默认开）"},
	{name: "logging", loop: "e.loggingLoop", step: "e.logpipes.Step",
		anchor: "采集环（F2.4/ADR-0040；logpipes 卫星——P5 wave-1 拆包，step 经卫星方法值）"},
	{name: "metrics", loop: "e.metricsLoop", step: "e.metricsStep",
		anchor: "指标采集+告警评估环（F2.5/ADR-0041）"},
	{name: "schedule", loop: "e.scheduleLoop", step: "e.scheduleStep",
		anchor: "Schedule 到期拍环（F1.7）；表序锚：schedule 先于 task——同轮 DriveOnce 铸出的 Task 即刻进补足链"},
	{name: "task", loop: "e.taskLoop", step: "e.taskStep",
		anchor: "Task/Run 收敛环（ADR-0012/0025 任务两形态：janitor/补足/Ensure/收口）"},
	{name: "browse", loop: "e.browseLoop", step: "e.browseStep",
		anchor: "Browse 会话收敛环（F3.6/ADR-0051）"},
}

// managedMountRegistry 是锚②受管挂载的登记表（持 wg.Done 的 go 语句——
// Stop 排水覆盖承诺面）。决策 5 点名两类：driftLoop 独立 Run、exec relay
// 挂载；其余为在册固定机械。
var managedMountRegistry = map[string]string{
	"r.loop.Run":              "环表挂载循环本体（生长点①的执行面；条目集由锚①另执法——ADR-0088 决策 5）",
	"e.runBaselineReplay":     "启动基线重放 goroutine（H7 Route 就绪门 + 归属/期望缓存重建）",
	"e.driftScanLoop":         "漂移扫描环独立 Run（ADR-0022；ADR-0088 决策 5 点名的 lifecycle 注册点）",
	"e.execFace.RunNodeRelay": "exec 回环中继挂载（ADR-0049/H10；ADR-0088 决策 5 点名的 lifecycle 注册点）",
	"e.consumeWatch":          "Runtime Watch 消费（C3 观测扇出，L0#7；StartWatch 面——apitest 手动形态同入口）",
}

// bareMountRegistry 是锚② bare 挂载的登记表（不持 wg.Done 的 go 语句——
// 白名单短且必须逐条给「为何免排水纪律」的理由）。
var bareMountRegistry = map[string]string{
	"e.wg.Wait": "Stop 排水等待 helper（等待完成后 close(done) 通知超时面；自身即排水机械，无再入 wg 语义）",
}

// renderCallChain 渲染一个调用的完整选择链（e.wg.Done / r.loop.Run /
// e.runBaselineReplay 形态；链基非 Ident 返回 ""——不可归因）。segs 已含
// 末端段（transitionguard selectorChain 契约），此处只拼不再追加。
func renderCallChain(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	base, segs := selectorChain(sel)
	if base == nil {
		return ""
	}
	return strings.Join(append([]string{base.Name}, segs...), ".")
}

// scanRingTable 提取 construct.go 的环表条目集（锚①）。结构契约：AssignStmt
// LHS 渲染为 e.rings、RHS 是 CompositeLit；逐元素取 name/loop/step 三键。
// 契约改形即 fatal（防盲优先于误报）。
func scanRingTable(t *testing.T, root string) []ringRow {
	t.Helper()
	path := filepath.Join(root, "construct.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ring registry scan: %v — construct.go is part of the frozen growth-point contract (ADR-0088 decision 5)", err)
	}
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, path, src, 0)
	if perr != nil {
		t.Fatalf("ring registry scan: parse construct.go: %v", perr)
	}
	var rows []ringRow
	found := false
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			lhsSel, ok := assign.Lhs[0].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if base, segs := selectorChain(lhsSel); base == nil || strings.Join(append([]string{base.Name}, segs...), ".") != "e.rings" {
				return true
			}
			lit, ok := assign.Rhs[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			found = true
			for _, elt := range lit.Elts {
				// 元素是省类型 struct 字面量（[]ring{{name: ..., loop: ...,
				// step: ...}}）；键值对在元素内层。
				elem, ok := elt.(*ast.CompositeLit)
				if !ok {
					t.Fatalf("ring table element at %s is not a struct literal — the []ring literal shape changed, fix the guard judgment first", fset.Position(elt.Pos()))
				}
				row := ringRow{}
				for _, pair := range elem.Elts {
					kv, ok := pair.(*ast.KeyValueExpr)
					if !ok {
						t.Fatalf("ring table entry field at %s is not a key-value pair — the ring struct literal shape changed, fix the guard judgment first", fset.Position(pair.Pos()))
					}
					for _, slot := range []struct {
						key  string
						want *string
					}{{"name", &row.name}, {"loop", &row.loop}, {"step", &row.step}} {
						if id, ok := kv.Key.(*ast.Ident); ok && id.Name == slot.key {
							*slot.want = renderRingValue(t, fset, kv.Value)
						}
					}
				}
				if row.name == "" || row.loop == "" || row.step == "" {
					t.Fatalf("ring table element at %s lacks name/loop/step keys — the ring struct literal shape changed, fix the guard judgment first", fset.Position(elt.Pos()))
				}
				rows = append(rows, row)
			}
			return true
		})
	}
	if !found {
		t.Fatalf("no `e.rings = []ring{...}` assignment found in %s — the ring table is growth point #1 (ADR-0088 decision 5); if the shape changed, fix the guard judgment in the same commit", path)
	}
	return rows
}

// renderRingValue 渲染环表键值（name 字符串字面量 / loop 与 step 选择链）。
func renderRingValue(t *testing.T, fset *token.FileSet, expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return strings.Trim(v.Value, `"`)
	case *ast.SelectorExpr:
		if base, segs := selectorChain(v); base != nil {
			return strings.Join(append([]string{base.Name}, segs...), ".")
		}
	}
	t.Fatalf("ring table value at %s is neither a string literal nor a selector chain — the []ring literal shape changed, fix the guard judgment first", fset.Position(expr.Pos()))
	return ""
}

// scanLifecycleMounts 提取 lifecycle.go 的 go 语句挂载集（锚②）。
func scanLifecycleMounts(t *testing.T, root string) []lifecycleGoStmt {
	t.Helper()
	path := filepath.Join(root, "lifecycle.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ring registry scan: %v — lifecycle.go is part of the frozen growth-point contract (ADR-0088 decision 5)", err)
	}
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, path, src, 0)
	if perr != nil {
		t.Fatalf("ring registry scan: parse lifecycle.go: %v", perr)
	}
	var mounts []lifecycleGoStmt
	ast.Inspect(file, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		m := lifecycleGoStmt{position: goStmt.Pos()}
		// managed = 体内 defer e.wg.Done()；entry = 首个非 e.wg.Done 调用链。
		ast.Inspect(goStmt.Call, func(inner ast.Node) bool {
			if call, ok := inner.(*ast.CallExpr); ok {
				chain := renderCallChain(call)
				if chain == "e.wg.Done" {
					m.managed = true
					return true
				}
				if m.entry == "" && chain != "" {
					m.entry = chain
				}
			}
			return true
		})
		if m.entry == "" {
			t.Fatalf("go statement at %s has no attributable call — fix the guard judgment in the same commit", fset.Position(goStmt.Pos()))
		}
		mounts = append(mounts, m)
		return true
	})
	if len(mounts) == 0 {
		t.Fatalf("no go statements found in %s — the lifecycle mount set is growth point #2 (ADR-0088 decision 5); guard is blind, fix the scan", path)
	}
	return mounts
}

// compareRingTables 是锚①的判据（纯函数，shapes 共用）：长度 + 逐位三元组。
func compareRingTables(got, want []ringRow) []string {
	var bad []string
	if len(got) != len(want) {
		bad = append(bad, fmt.Sprintf("ring table holds %d entries, registry freezes %d — a ring was added/removed without registering it in ringRegistry (growth law, ADR-0088 decision 5)", len(got), len(want)))
	}
	n := min(len(got), len(want))
	for i := 0; i < n; i++ {
		if got[i].name != want[i].name || got[i].loop != want[i].loop || got[i].step != want[i].step {
			bad = append(bad, fmt.Sprintf("ring table entry #%d is {%s %s %s}, registry freezes {%s %s %s} — table order is DriveOnce order; re-wiring or re-ordering rings must update ringRegistry in the same commit", i+1, got[i].name, got[i].loop, got[i].step, want[i].name, want[i].loop, want[i].step))
		}
	}
	return bad
}

// compareLifecycleMounts 是锚②的判据（纯函数，shapes 共用）：受管与 bare
// 两册分开对账（集合语义，与 Start 内顺序无关）。
func compareLifecycleMounts(got []lifecycleGoStmt) []string {
	var bad []string
	gotManaged, gotBare := map[string]bool{}, map[string]bool{}
	for _, m := range got {
		if m.managed {
			gotManaged[m.entry] = true
		} else {
			gotBare[m.entry] = true
		}
	}
	for entry := range gotManaged {
		if _, ok := managedMountRegistry[entry]; !ok {
			bad = append(bad, fmt.Sprintf("lifecycle mount %q carries wg discipline but is not registered in managedMountRegistry — a new background process entering the engine must land with a domain/ADR anchor (growth law, ADR-0088 decision 5)", entry))
		}
	}
	for entry := range gotBare {
		if _, ok := bareMountRegistry[entry]; !ok {
			bad = append(bad, fmt.Sprintf("lifecycle mount %q has NO wg.Done discipline and is not registered in bareMountRegistry — unmanaged goroutines break the bounded-drain contract; register with a reason or mount via spawn/wg accounting", entry))
		}
	}
	for entry, anchor := range managedMountRegistry {
		if !gotManaged[entry] {
			bad = append(bad, fmt.Sprintf("managedMountRegistry entry %q no longer matches a real mount (removed/renamed?) — remove the entry in the same commit (双向保鲜; %s)", entry, anchor))
		}
	}
	for entry, anchor := range bareMountRegistry {
		if !gotBare[entry] {
			bad = append(bad, fmt.Sprintf("bareMountRegistry entry %q no longer matches a real mount — remove the entry in the same commit (双向保鲜; %s)", entry, anchor))
		}
	}
	sort.Strings(bad)
	return bad
}

// TestRingRegistryGrowthLaw：真实 engine 包的双锚执法（环表条目集 +
// lifecycle 挂载集 vs 登记表，双向保鲜 + 防盲）。
func TestRingRegistryGrowthLaw(t *testing.T) {
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))

	rows := scanRingTable(t, root)
	if len(rows) < 8 {
		t.Fatalf("ring table holds only %d entries (want >= 8) — guard is blind or the table was dismantled; re-measure before adjusting", len(rows))
	}
	for _, v := range compareRingTables(rows, ringRegistry) {
		t.Errorf("%s", v)
	}

	mounts := scanLifecycleMounts(t, root)
	if len(mounts) < 4 {
		t.Fatalf("lifecycle holds only %d go-statement mounts (want >= 4) — guard is blind or Start was dismantled; re-measure before adjusting", len(mounts))
	}
	for _, v := range compareLifecycleMounts(mounts) {
		t.Errorf("%s", v)
	}
}

// TestRingRegistryShapes：判据分辨力锚（合成树，resolutionface/intersatellite
// 范式）——红灯实验的永久化：全绿 / 新环未登记红 / 环消失红 / 换序红 /
// 挂载换线红（受管→bare）/ 新挂载未登记红 / 登记条目消失红。
func TestRingRegistryShapes(t *testing.T) {
	synthTree := func(files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		for rel, src := range files {
			abs := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	constructSrc := `package engine

func assemble(e *shell) {
	e.rings = []ring{
		{name: "deployment", loop: e.loop, step: e.step},
		{name: "logging", loop: e.loggingLoop, step: e.logPipes.Step},
	}
}
`
	lifecycleSrc := `package engine

func start(e *shell, ctx context.Context) {
	for _, r := range e.rings {
		go func() {
			defer e.wg.Done()
			r.loop.Run(ctx, r.step)
		}()
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.driftScanLoop(ctx)
	}()
	go func() {
		e.wg.Wait()
		close(e.done)
	}()
}
`
	baseTree := map[string]string{
		"construct.go": constructSrc,
		"lifecycle.go": lifecycleSrc,
	}
	miniRings := []ringRow{
		{name: "deployment", loop: "e.loop", step: "e.step"},
		{name: "logging", loop: "e.loggingLoop", step: "e.logPipes.Step"},
	}
	miniManaged := map[string]string{
		"r.loop.Run":      "ring mount",
		"e.driftScanLoop": "drift",
	}
	miniBare := map[string]string{"e.wg.Wait": "drain waiter"}

	judge := func(tree map[string]string, rings []ringRow, managed, bare map[string]string) []string {
		savedManaged, savedBare := managedMountRegistry, bareMountRegistry
		managedMountRegistry, bareMountRegistry = managed, bare
		defer func() { managedMountRegistry, bareMountRegistry = savedManaged, savedBare }()
		root := synthTree(tree)
		var bad []string
		bad = append(bad, compareRingTables(scanRingTable(t, root), rings)...)
		bad = append(bad, compareLifecycleMounts(scanLifecycleMounts(t, root))...)
		return bad
	}

	t.Run("全绿", func(t *testing.T) {
		if bad := judge(baseTree, miniRings, miniManaged, miniBare); len(bad) != 0 {
			t.Fatalf("clean fixture must pass, got %v", bad)
		}
	})
	t.Run("新环未登记=红", func(t *testing.T) {
		tree := map[string]string{"construct.go": strings.Replace(constructSrc, "{name: \"logging\"", "{name: \"extra\", loop: e.xLoop, step: e.xStep},\n\t\t{name: \"logging\"", 1), "lifecycle.go": lifecycleSrc}
		bad := judge(tree, miniRings, miniManaged, miniBare)
		if len(bad) == 0 || !strings.Contains(bad[0], "holds 3 entries") {
			t.Fatalf("want the unregistered-ring red, got %v", bad)
		}
	})
	t.Run("登记环消失=红", func(t *testing.T) {
		tree := map[string]string{"construct.go": strings.Replace(constructSrc, "{name: \"logging\"", "{name: \"renamed\", loop: e.loggingLoop, step: e.logPipes.Step},\n\t\t// {name: \"logging\"", 1), "lifecycle.go": lifecycleSrc}
		bad := judge(tree, miniRings, miniManaged, miniBare)
		if len(bad) == 0 {
			t.Fatalf("want the missing-registry-entry red, got %v", bad)
		}
	})
	t.Run("换序=红", func(t *testing.T) {
		swapped := `package engine

func assemble(e *shell) {
	e.rings = []ring{
		{name: "logging", loop: e.loggingLoop, step: e.logPipes.Step},
		{name: "deployment", loop: e.loop, step: e.step},
	}
}
`
		tree := map[string]string{"construct.go": swapped, "lifecycle.go": lifecycleSrc}
		bad := judge(tree, miniRings, miniManaged, miniBare)
		if len(bad) == 0 || !strings.Contains(bad[0], "entry #1") {
			t.Fatalf("want the order-drift red (table order is DriveOnce order), got %v", bad)
		}
	})
	t.Run("挂载失去排水纪律=红", func(t *testing.T) {
		tree := map[string]string{"construct.go": constructSrc, "lifecycle.go": strings.Replace(lifecycleSrc, "defer e.wg.Done()\n\t\te.driftScanLoop", "e.driftScanLoop", 1)}
		bad := judge(tree, miniRings, miniManaged, miniBare)
		if len(bad) == 0 {
			t.Fatalf("want the lost-wg-discipline red, got %v", bad)
		}
	})
	t.Run("新挂载未登记=红", func(t *testing.T) {
		tree := map[string]string{"construct.go": constructSrc, "lifecycle.go": lifecycleSrc + `
func startWatch(e *shell, ctx context.Context) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.consumeWatch(ctx)
	}()
}
`}
		bad := judge(tree, miniRings, miniManaged, miniBare)
		if len(bad) == 0 || !strings.Contains(strings.Join(bad, " | "), "consumeWatch") {
			t.Fatalf("want the unregistered-mount red, got %v", bad)
		}
	})
	t.Run("登记挂载消失=红", func(t *testing.T) {
		tree := map[string]string{"construct.go": constructSrc, "lifecycle.go": strings.Replace(lifecycleSrc, "e.driftScanLoop", "e.driftLoop2", 1)}
		bad := judge(tree, miniRings, miniManaged, miniBare)
		if len(bad) == 0 || !strings.Contains(strings.Join(bad, " | "), "e.driftScanLoop") {
			t.Fatalf("want the stale-registry-entry red, got %v", bad)
		}
	})
}
