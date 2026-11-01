package guards

// 星间零硬边 AST 守卫（ADR-0088 决策 7 第一件 + 验收锚 P4 条，P4 wave-3
// 收官双守卫之一，2026-11-01）。
//
// 律文：9 卫星域（intake/backups/logpipes/metrics 域/execs/manageds/
// databases/browses/diagnosis）之间零硬边——卫星文件不得调用/引用**他星**
// 定义的方法、函数与 struct 字段链。卫星→核（机制端口 + 受理线/平台面
// 帮手）与核→卫星（环表/组合根方向）不在执法面；P5 拆包后本守卫降级为
// import 级并继续执法（dep_matrix 扩面接管，ADR-0088 决策 7）。
//
// 三要素（ADR-0088 决策 7）：
//
//	① 文件→子系统归属表 interSatFileOwnership：67 文件 = 设计稿 §3 的
//	  66 文件口径 + wave-0 新增 resolve.go（解析面，真核）。双向保鲜——
//	  表内文件消失红、盘上新 .go 无归属红、文件数下限防盲。
//	② struct→子系统映射 interSatStructs：8 卫星 struct（diagnosis 域按
//	  ADR-0088 决策 1 过渡条款驻 *Engine 方法/自由函数，无卫星 struct，
//	  文件级归属仍在 ①）。条目消失红、struct 声明文件的归属与映射不一致红。
//	③ 符号→定义文件反查：包内两遍 AST——先集齐方法/包级函数定义文件集
//	  与 struct 字段面，再在卫星文件内对每个调用/引用反查定义归属。
//
// 判据（只执法卫星文件；own = 所在文件子系统）：
//
//	R1 调用：接收者可归因（本函数接收者 / 形参 / 局部变量绑定为 Engine
//	  或某卫星 struct 类型）且接收者 struct **无同名字段**（端口字段遮蔽
//	  ——databases 的 pinVolumes 端口与 intake 提升方法同名是现役形态），
//	  名字定义文件集的卫星子系统恰为单一他星（不含核、不含本星）→ 红；
//	  定义集含核或多星 → 静态不可归因（Engine 提升深度歧义/跨星重名），
//	  放行——保守面，P5 import 级接管后归零。包级函数调用同口径。
//	R2 字段链：选择子链出现他星 struct 名段（e.intake.xxx / 卫星指针传递）
//	  → 红。本星侧合法：materials.go 的 e.intake.pinVolumes 访问器与
//	  managed.go 的 &e.manageds 限定指针是现役合法形态。
//	R3 类型夹带：形参/局部/复合字面量/类型断言引用他星 struct 类型 → 红
//	  （P5 视角的 import 前体；引擎反证：卫星间互传 struct 即硬边）。
//
// 端口绑定除外：组合根装配区 construct.go 归属真核、不在执法面——
// volumePinner/databasePassword 终形均为组合根绑定（dbs.pinVolumes =
// e.pinVolumes / dbs.databasePassword = e.databasePassword），星间消费经
// 端口字段、绑定在核侧，**端口实现边类白名单实体归零**（ADR-0088 决策 7
// 终值裁定）。interSatExemptions 亦为零表——wave-3 上线扫出的 7 条函数级
// 残留硬边债已随 wave-3b 归位批清偿（见该表注）。
//
// 防盲：文件数（≥60）/方法符号（≥200）/包级函数（≥50）下限；9 子系统各
// 有文件、8 struct 全命中、Engine 声明可见；正向锚——真实树卫星→核调用
// 归因计数 ≥20（归因管线非哑）+ 同星链观察 ≥1 + 合成树 shapes（全绿/
// 端口绑定非命中/R1-R3 各一红/归属与 struct 表双向漂移红，
// resolutionface 范式）。

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

// interSatCore 标记真核归属（组合根 + 受理族 + 状态机收敛 + 任务族 +
// 平台面 + resolve.go 解析面——设计稿 §3 真核 43 + resolve.go）。
const interSatCore = "core"

// interSatFileOwnership 是 ① 文件→子系统归属表（67 文件全量，基名键——
// engine 包生产面为平铺单层）。新增/删除文件必须同批改表（双向保鲜）；
// 新卫星域立域（生长律）= 新子系统条目成批登记。
var interSatFileOwnership = map[string]string{
	// ---- 真核（44）----
	// 组合根（§3）。
	"engine.go": interSatCore, "construct.go": interSatCore, "options.go": interSatCore,
	"lifecycle.go": interSatCore, "health.go": interSatCore, "events.go": interSatCore,
	"schemareg.go": interSatCore, "loop.go": interSatCore,
	// 受理族（§3，含 B1 增补 preview/scaleprocess）。
	"admission.go": interSatCore, "confirm.go": interSatCore, "deployspec.go": interSatCore,
	"predeploy.go": interSatCore, "render.go": interSatCore, "projection.go": interSatCore,
	"materialize.go": interSatCore, "preview.go": interSatCore, "scaleprocess.go": interSatCore,
	// 状态机与收敛（§3）。
	"transition.go": interSatCore, "bluegreen.go": interSatCore, "rollback.go": interSatCore,
	"restart.go": interSatCore, "teardown.go": interSatCore, "hygiene.go": interSatCore,
	"observ.go": interSatCore, "observdomain.go": interSatCore, "converge.go": interSatCore,
	"drift.go": interSatCore, "drive.go": interSatCore, "ensure.go": interSatCore,
	// 任务族（§3）。
	"task.go": interSatCore, "taskverbs.go": interSatCore, "taskobs.go": interSatCore,
	"taskview.go": interSatCore, "minttask.go": interSatCore, "schedule.go": interSatCore,
	"processrestart.go": interSatCore, "componentrestart.go": interSatCore,
	// 平台面（§3）。
	"nodes.go": interSatCore, "peers.go": interSatCore, "routepublish.go": interSatCore,
	"apipublish.go": interSatCore, "console.go": interSatCore, "networkrebuild.go": interSatCore,
	// 解析面（wave-0 收拢落点，L1#9——星间零硬边的新消费者唯一入口）。
	"resolve.go": interSatCore,
	// ---- 卫星（23）----
	// intake（构建摄取域，6）。
	"gitmaterial.go": "intake", "materials.go": "intake", "buildsource.go": "intake",
	"builder.go": "intake", "buildlog.go": "intake", "registrycontent.go": "intake",
	// backups（2）。
	"backup.go": "backups", "platformbackup.go": "backups",
	// metrics 域（5；词条避用 telemetry，CONTEXT.md Metrics _Avoid_）。
	"metrics.go": "metrics", "alerting.go": "metrics", "channelnotify.go": "metrics",
	"exposition.go": "metrics", "crashnotify.go": "metrics",
	// execs（2）。
	"exec.go": "execs", "execrelay.go": "execs",
	// databases（3）。
	"database.go": "databases", "databaseenv.go": "databases", "rotate.go": "databases",
	// browses（2）。
	"browse.go": "browses", "browseloop.go": "browses",
	// manageds（1）。
	"managed.go": "manageds",
	// logpipes：已随 P5 wave-1 拆包迁出 engine 树（internal/application/
	// logpipes/，ADR-0088 决策 2 P5 形态）——条目与 struct 映射同步删
	//（双向保鲜强制），卫星包由 engineSatellitePackages 登记表与跨包腿
	// 接管执法；后续七星拆包同款同批删条目。
	// diagnosis（P2 新晋过渡条款，1——无卫星 struct，见 interSatStructs 注）。
	"diagnose.go": "diagnosis",
}

// interSatStructs 是 ② struct→子系统映射：Engine 裸嵌入的卫星 struct
// （logpipes 已随 P5 wave-1 拆包迁出，映射同批删条目）。条目消失（改名/
// 迁出）红、struct 声明落错子系统文件红；新卫星 struct（P5 拆包/新域
// 立域）必须同批登记。diagnosis 域的 application 半边按 ADR-0088 决策 1
// 过渡条款驻 *Engine 方法与自由函数（diagnose.go），无卫星 struct——域
// 归属由 ① 承载。
var interSatStructs = map[string]string{
	"backups":   "backups",
	"execs":     "execs",
	"browses":   "browses",
	"metrics":   "metrics",
	"manageds":  "manageds",
	"databases": "databases",
	"intake":    "intake",
}

// interSatExemptions 是星间硬边豁免表（file:fn → 理由）。**终值 = 空表**：
// 端口实现边类豁免自始为零（ADR-0088 决策 7：volumePinner/databasePassword
// 两条预估边终形均为 construct.go 组合根绑定——归属真核天然合法）；wave-3
// 上线时扫出的 7 条函数级残留硬边债已由 wave-3b 星间债务清偿批归零
// （DBCredentialSecretName/DatabaseDNSName → resolve.go 解析面、
// managedFingerprint/materialsFingerprint → converge.go 签名构造邻域、
// stderrTail/trimTrailing → observ.go 包级帮手区，搬迁即消边，消费点
// 零改）。新豁免 = 新债：登记必须带理由与归位方向，条目不再命中即红
// （双向保鲜），豁免是债不是免检。
var interSatExemptions = map[string]string{}

// interSatResult 是一次星间扫描的产出（root 注入：真实树 / shapes 合成树
// 同权；归属表与 struct 表随扫描注入，判据可对合成树独立执法）。
type interSatResult struct {
	files         int                        // 盘上生产 .go 数（防盲）
	diskFiles     map[string]bool            // 盘上文件基名集（归属对账面）
	methodDefs    map[string]map[string]bool // 方法名 → 定义文件集（③）
	funcDefs      map[string]map[string]bool // 包级函数名 → 定义文件集（③）
	structFile    map[string]string          // struct 名 → 声明文件基名
	structFields  map[string]map[string]bool // struct 名 → 字段名集（R1 遮蔽判定）
	sawEngine     bool                       // Engine 声明可见（防盲）
	violations    []string                   // R1/R2/R3 命中（豁免后）
	exemptUsed    map[string]bool            // 豁免条目命中面（双向保鲜）
	coreCalls     int                        // 卫星文件内归因为核的调用数（正向锚）
	sameSubChains int                        // 卫星文件内同星 struct 链观察数（正向锚）
}

// scanInterSatellite 两遍 AST 扫描 root：第一遍集符号定义面，第二遍执法
// 卫星文件（fileOwn 注入文件归属、satStructs 注入 struct 映射——shapes
// 用局部表，真实树用包级表）。
func scanInterSatellite(root string, fileOwn, satStructs map[string]string) (*interSatResult, error) {
	fset := token.NewFileSet()
	res := &interSatResult{
		diskFiles:    map[string]bool{},
		methodDefs:   map[string]map[string]bool{},
		funcDefs:     map[string]map[string]bool{},
		structFile:   map[string]string{},
		structFields: map[string]map[string]bool{},
		exemptUsed:   map[string]bool{},
	}
	type parsedFile struct {
		file *ast.File
		base string
	}
	var parsed []parsedFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		base := filepath.Base(path)
		res.files++
		res.diskFiles[base] = true
		parsed = append(parsed, parsedFile{file: file, base: base})
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				res.structFile[ts.Name.Name] = base
				if ts.Name.Name == "Engine" {
					res.sawEngine = true
				}
				if st, ok := ts.Type.(*ast.StructType); ok && st.Fields != nil {
					fields := map[string]bool{}
					for _, f := range st.Fields.List {
						for _, name := range f.Names {
							fields[name.Name] = true
						}
					}
					res.structFields[ts.Name.Name] = fields
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 第二遍之一：符号→定义文件集（③ 反查面）。
	for _, pf := range parsed {
		for _, decl := range pf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fd.Recv != nil && len(fd.Recv.List) > 0 && baseIdent(fd.Recv.List[0].Type) != nil {
				addDef(res.methodDefs, fd.Name.Name, pf.base)
				continue
			}
			addDef(res.funcDefs, fd.Name.Name, pf.base)
		}
	}

	// 第二遍之二：卫星文件执法（own == interSatCore 的真核文件整体跳过
	// ——核→卫星与组合根端口绑定不在执法面；未注册文件由归属腿对账，
	// 边判据对其放行避免重复报）。
	flag := func(base string, pos token.Pos, fn, kind, detail string) {
		key := base + ":" + fn
		if _, ok := interSatExemptions[key]; ok {
			res.exemptUsed[key] = true
			return
		}
		res.violations = append(res.violations, fmt.Sprintf(
			"%s:%d %s — %s (inter-satellite hard edge, ADR-0088 decision 7: satellites must reach each other only via mechanism ports wired in the composition root)",
			base, fset.Position(pos).Line, kind, detail))
	}
	for _, pf := range parsed {
		own, registered := fileOwn[pf.base]
		if !registered || own == interSatCore {
			continue
		}
		for _, decl := range pf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			intersatEnforceFunc(fd, pf.base, own, satStructs, fileOwn, res, fset, flag)
		}
	}
	sort.Strings(res.violations)
	return res, nil
}

// addDef 记录符号名的一个定义文件。
func addDef(defs map[string]map[string]bool, name, file string) {
	if defs[name] == nil {
		defs[name] = map[string]bool{}
	}
	defs[name][file] = true
}

// interSatBindings 收集函数内可归因的标识符绑定（标识符 → Engine 或卫星
// struct 类型名）：接收者、形参、`var x T` 局部、`x := &T{}` / `x := T{}`
// 局部。R3 同步执法：绑定源引用他星 struct 类型即红。
func interSatBindings(fd *ast.FuncDecl, base, own string, satStructs map[string]string, flag func(base string, pos token.Pos, fn, kind, detail string)) map[string]string {
	bindings := map[string]string{}
	bind := func(id *ast.Ident, typeName string) {
		if id == nil {
			return
		}
		bindings[id.Name] = typeName
	}
	// 卫星 struct 类型名或 Engine。
	isBindable := func(name string) bool { return name == "Engine" || satStructs[name] != "" }
	// R3：类型上下文里出现他星 struct 名 → 红（返回类型名供绑定）。
	typeRef := func(expr ast.Expr, pos token.Pos) string {
		var id *ast.Ident
		switch t := expr.(type) {
		case *ast.Ident:
			id = t
		case *ast.StarExpr:
			if inner, ok := t.X.(*ast.Ident); ok {
				id = inner
			}
		}
		if id == nil {
			return ""
		}
		if sub, ok := satStructs[id.Name]; ok && sub != own {
			flag(base, pos, fd.Name.Name, "R3 type smuggling",
				"references satellite struct "+id.Name+" (subsystem "+sub+") by type — satellites must not hold each other's struct types (params/locals/literals); go through mechanism ports")
		}
		return id.Name
	}
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		if names := fd.Recv.List[0].Names; len(names) > 0 {
			bind(names[0], typeRef(fd.Recv.List[0].Type, fd.Recv.List[0].Type.Pos()))
		}
	}
	if fd.Type != nil && fd.Type.Params != nil {
		for _, field := range fd.Type.Params.List {
			tn := typeRef(field.Type, field.Type.Pos())
			if !isBindable(tn) {
				continue
			}
			for _, name := range field.Names {
				bind(name, tn)
			}
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.DeclStmt:
			gd, ok := x.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) == 0 {
					continue
				}
				if vs.Type != nil {
					tn := typeRef(vs.Type, vs.Type.Pos())
					if isBindable(tn) {
						for _, name := range vs.Names {
							bind(name, tn)
						}
					}
					continue
				}
				for i, rhs := range vs.Values {
					tn := typeRefOfExpr(rhs)
					if tn != "" && isBindable(tn) && i < len(vs.Names) {
						bind(vs.Names[i], tn)
					}
				}
			}
		case *ast.AssignStmt:
			if x.Tok != token.DEFINE {
				return true
			}
			for i, rhs := range x.Rhs {
				tn := typeRefOfExpr(rhs)
				if tn == "" || !isBindable(tn) || i >= len(x.Lhs) {
					continue
				}
				if id, ok := x.Lhs[i].(*ast.Ident); ok {
					bind(id, tn)
				}
			}
		}
		return true
	})
	return bindings
}

// typeRefOfExpr 从表达式形态提取卫星/Engine 类型名（&T{} / T{} / T(...)）。
func typeRefOfExpr(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return typeRefOfExpr(x.X)
		}
	case *ast.CompositeLit:
		if id, ok := x.Type.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// intersatEnforceFunc 对单个函数执法 R1/R2/R3。
func intersatEnforceFunc(fd *ast.FuncDecl, base, own string, satStructs, fileOwn map[string]string, res *interSatResult, fset *token.FileSet, flag func(base string, pos token.Pos, fn, kind, detail string)) {
	bindings := interSatBindings(fd, base, own, satStructs, flag)

	// subSystemsOf 返回定义文件集的子系统集（未注册文件记 "?"——归属腿
	// 另执法，边判据对其放行）。
	subSystemsOf := func(defs map[string]bool) map[string]string {
		out := map[string]string{}
		for f := range defs {
			if sub, ok := fileOwn[f]; ok {
				out[sub] = f
			} else {
				out["?"] = f
			}
		}
		return out
	}
	// classifyCall：R1 归因。返回 true=已定性（无论红绿），false=放行。
	classifyCall := func(name string, defs map[string]bool, pos token.Pos, recvNote string) {
		if len(defs) == 0 {
			return // 包外符号（repo/capability 方法等）。
		}
		subs := subSystemsOf(defs)
		if _, unknown := subs["?"]; unknown {
			return
		}
		satSubs := map[string]bool{}
		hasCore := false
		for sub := range subs {
			if sub == interSatCore {
				hasCore = true
			} else {
				satSubs[sub] = true
			}
		}
		switch {
		case len(satSubs) == 0:
			res.coreCalls++ // 卫星→核：合法（机制端口/受理线帮手方向）。
		case len(satSubs) == 1 && satSubs[own]:
			// 本星定义：合法。
		case len(satSubs) == 1 && !hasCore:
			other := ""
			for sub := range satSubs {
				other = sub
			}
			flag(base, pos, fd.Name.Name, "R1 call",
				recvNote+"calls "+name+" which is defined only in subsystem "+other+" — invoke the mechanism port instead (composition root wiring)")
		default:
			// 含核或多星重名：静态不可归因（Engine 提升深度歧义），放行。
		}
	}

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			switch fun := x.Fun.(type) {
			case *ast.SelectorExpr:
				id, ok := fun.X.(*ast.Ident)
				if !ok {
					return true
				}
				tn, bound := bindings[id.Name]
				if !bound {
					return true // 接收者不可归因（外部对象/链式调用）→ 放行。
				}
				if res.structFields[tn][fun.Sel.Name] {
					return true // 端口/字段遮蔽（如 databases.pinVolumes 端口）。
				}
				classifyCall(fun.Sel.Name, res.methodDefs[fun.Sel.Name], x.Pos(),
					"on "+tn+"-bound identifier "+id.Name+" ")
			case *ast.Ident:
				classifyCall(fun.Name, res.funcDefs[fun.Name], x.Pos(), "")
			}
		case *ast.SelectorExpr:
			if sub, ok := satStructs[x.Sel.Name]; ok {
				if sub == own {
					res.sameSubChains++ // 本星 struct 链（合法现役形态）。
				} else {
					flag(base, x.Pos(), fd.Name.Name, "R2 field chain",
						"selector chain reaches satellite struct "+x.Sel.Name+" (subsystem "+sub+") — cross-satellite field/method reach-through is a hard edge")
				}
			}
		case *ast.CompositeLit:
			if id, ok := x.Type.(*ast.Ident); ok {
				if sub, isSat := satStructs[id.Name]; isSat && sub != own {
					flag(base, x.Pos(), fd.Name.Name, "R3 type smuggling",
						"composite literal of satellite struct "+id.Name+" (subsystem "+sub+")")
				}
			}
		case *ast.TypeAssertExpr:
			if id, ok := x.Type.(*ast.Ident); ok {
				if sub, isSat := satStructs[id.Name]; isSat && sub != own {
					flag(base, x.Pos(), fd.Name.Name, "R3 type smuggling",
						"type assertion to satellite struct "+id.Name+" (subsystem "+sub+")")
				}
			}
		}
		return true
	})
}

// interSatOwnershipViolations 是 ① 的双向保鲜判据（纯函数，shapes 共用）：
// 表内文件消失 / 盘上文件无归属。
func interSatOwnershipViolations(table map[string]string, disk map[string]bool) []string {
	var bad []string
	for f := range table {
		if !disk[f] {
			bad = append(bad, fmt.Sprintf("ownership table lists %s but the file is gone from the engine package — remove the entry or restore the file (双向保鲜, ADR-0088 P4)", f))
		}
	}
	for f := range disk {
		if _, ok := table[f]; !ok {
			bad = append(bad, fmt.Sprintf("engine file %s has no subsystem ownership — register it in interSatFileOwnership (core or a satellite) in the same commit (双向保鲜, ADR-0088 P4)", f))
		}
	}
	sort.Strings(bad)
	return bad
}

// interSatStructViolations 是 ② 的双向保鲜判据（纯函数）：映射条目必须有
// 真实 struct 声明、且声明文件的归属与映射一致。
func interSatStructViolations(satStructs, structFile, fileOwn map[string]string) []string {
	var bad []string
	for name, want := range satStructs {
		file, ok := structFile[name]
		if !ok {
			bad = append(bad, fmt.Sprintf("satellite struct %s (subsystem %s) is not declared in the engine package — renamed/removed/migrated? update interSatStructs in the same commit (双向保鲜, ADR-0088 P4)", name, want))
			continue
		}
		if got := fileOwn[file]; got != want {
			bad = append(bad, fmt.Sprintf("satellite struct %s is declared in %s (ownership %q) but mapped to subsystem %q — the struct must live in its own satellite's files; fix the table or the placement", name, file, got, want))
		}
	}
	sort.Strings(bad)
	return bad
}

// TestInterSatelliteZeroHardEdges：真实 engine 树的星间零硬边执法
// （三要素 + 防盲 + 正向锚 + 豁免双向保鲜）。
func TestInterSatelliteZeroHardEdges(t *testing.T) {
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))
	res, err := scanInterSatellite(root, interSatFileOwnership, interSatStructs)
	if err != nil {
		t.Fatal(err)
	}
	// 防盲：扫描面下限（文件 67/方法 ≥200/函数 ≥50 实测量级；红时先重测
	// 再调值）。Engine 声明必须可见。
	if res.files < 60 {
		t.Fatalf("scanned only %d production engine files — guard is blind, fix the scan", res.files)
	}
	if !res.sawEngine {
		t.Fatalf("scan found no Engine declaration — guard is blind, fix the scan")
	}
	if len(res.methodDefs) < 200 || len(res.funcDefs) < 50 {
		t.Fatalf("symbol index too small (methods %d, funcs %d) — guard is blind, fix the scan", len(res.methodDefs), len(res.funcDefs))
	}
	// ① 归属表双向保鲜 + 表内每个卫星子系统至少一个文件（P5 起子系统
	// 清单从归属表派生——logpipes 拆包迁出后不再硬编码在册；新卫星立域/
	// 拆包迁出都经表增删驱动本腿）。
	for _, v := range interSatOwnershipViolations(interSatFileOwnership, res.diskFiles) {
		t.Errorf("%s", v)
	}
	subFileCount := map[string]int{}
	for f, sub := range interSatFileOwnership {
		if sub != interSatCore && res.diskFiles[f] {
			subFileCount[sub]++
		}
	}
	tabledSubs := map[string]bool{}
	for _, sub := range interSatFileOwnership {
		if sub != interSatCore {
			tabledSubs[sub] = true
		}
	}
	for sub := range tabledSubs {
		if subFileCount[sub] == 0 {
			t.Errorf("satellite subsystem %s has no files on disk — ownership table is stale or the satellite was dismantled; re-adjudicate (ADR-0088 P4/P5)", sub)
		}
	}
	// ② struct 映射双向保鲜。
	for _, v := range interSatStructViolations(interSatStructs, res.structFile, interSatFileOwnership) {
		t.Errorf("%s", v)
	}
	if len(res.structFile) < 60 {
		t.Fatalf("scanned only %d struct declarations — guard is blind, fix the scan", len(res.structFile))
	}
	// ③ 星间硬边判据（豁免后剩余即红）。
	for _, v := range res.violations {
		t.Errorf("%s", v)
	}
	// 正向锚：归因管线非哑——卫星→核调用必须真实可观（≥20，实测口径
	// 见日志）；本星 struct 链（materials.go 访问器/managed.go 限定指针）
	// 必须 ≥1。
	t.Logf("intersat scan: %d files, %d methods, %d funcs, %d core-attributed satellite calls, %d same-subsystem chains",
		res.files, len(res.methodDefs), len(res.funcDefs), res.coreCalls, res.sameSubChains)
	if res.coreCalls < 20 {
		t.Fatalf("only %d satellite→core calls were attributed — the attribution pipeline is blind, fix the scan", res.coreCalls)
	}
	if res.sameSubChains < 1 {
		t.Fatalf("no same-subsystem struct chain observed — the R2 detector is blind, fix the scan")
	}
	// 豁免双向保鲜：条目必须携带理由且在本次扫描中真实命中（帮手归位/
	// 改名/调用点消失后条目不再命中即红——豁免是债，不是永久免检）。
	for key, reason := range interSatExemptions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("inter-satellite exemption %s must carry a reason", key)
		}
		if !res.exemptUsed[key] {
			t.Errorf("inter-satellite exemption %s no longer matches a real hard edge — remove the entry (双向保鲜)", key)
		}
	}
}

// TestInterSatelliteShapes：判据分辨力锚（合成树注入，resolutionface 范式）
// ——全绿+端口绑定非命中 / R1 方法调用红 / R1 包级函数红 / R2 字段链红 /
// R3 类型夹带红 / 归属表双向漂移红 / struct 表漂移红。
func TestInterSatelliteShapes(t *testing.T) {
	synthTree := func(files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		for rel, src := range files {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	// 合成树五文件：核（resolve/construct）+ 三星（intake/databases/backups）。
	// databaseStep 的 d.pinVolumes() 是端口字段消费（与 intake 提升方法
	// 同名）——字段遮蔽判据的非命中锚；assemble 的两行绑定是组合根端口
	// 绑定形态（volumePinner 终形复刻）——核侧不在执法面。
	materialsSrc := `package engine

type intake struct{ repos int }

func (in *intake) pinVolumes() error { return nil }

func resolveGitToken() string { return "" }
`
	databaseSrc := `package engine

type databases struct{ pinVolumes func() error }

func (d *databases) databaseStep() error { return d.pinVolumes() }
`
	backupSrc := `package engine

type backups struct{ log int }
`
	resolveSrc := `package engine

type Engine struct {
	databases
	intake
}

func (e *Engine) databasePassword() (string, error) { return "", nil }
`
	constructSrc := `package engine

func assemble(e *Engine) {
	e.databases.pinVolumes = e.intake.pinVolumes
	e.databases.databasePassword = e.databasePassword
}
`
	baseTree := map[string]string{
		"engine/materials.go": materialsSrc,
		"engine/database.go":  databaseSrc,
		"engine/backup.go":    backupSrc,
		"engine/resolve.go":   resolveSrc,
		"engine/construct.go": constructSrc,
	}
	miniOwn := map[string]string{
		"materials.go": "intake", "database.go": "databases", "backup.go": "backups",
		"resolve.go": interSatCore, "construct.go": interSatCore,
	}
	miniStructs := map[string]string{"intake": "intake", "databases": "databases", "backups": "backups"}

	t.Run("全绿+端口绑定非命中", func(t *testing.T) {
		res, err := scanInterSatellite(synthTree(baseTree), miniOwn, miniStructs)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.violations) != 0 {
			t.Fatalf("want a clean satellite tree with port wiring, got %v", res.violations)
		}
	})
	t.Run("R1 星间方法调用=红", func(t *testing.T) {
		tree := map[string]string{}
		for k, v := range baseTree {
			tree[k] = v
		}
		tree["engine/database.go"] = databaseSrc + "\nfunc (e *Engine) badEdge() error { return e.pinVolumes() }\n"
		res, err := scanInterSatellite(synthTree(tree), miniOwn, miniStructs)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.violations) != 1 || !strings.Contains(res.violations[0], "R1 call") || !strings.Contains(res.violations[0], "intake") {
			t.Fatalf("want exactly the R1 inter-satellite call violation, got %v", res.violations)
		}
	})
	t.Run("R1 星间包级函数=红", func(t *testing.T) {
		tree := map[string]string{}
		for k, v := range baseTree {
			tree[k] = v
		}
		tree["engine/backup.go"] = backupSrc + "\nfunc backupBad() string { return resolveGitToken() }\n"
		res, err := scanInterSatellite(synthTree(tree), miniOwn, miniStructs)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.violations) != 1 || !strings.Contains(res.violations[0], "R1 call") || !strings.Contains(res.violations[0], "intake") {
			t.Fatalf("want exactly the R1 package-function violation, got %v", res.violations)
		}
	})
	t.Run("R2 他星字段链=红", func(t *testing.T) {
		tree := map[string]string{}
		for k, v := range baseTree {
			tree[k] = v
		}
		tree["engine/backup.go"] = backupSrc + "\nfunc (e *Engine) chainBad() int { return e.intake.repos }\n"
		res, err := scanInterSatellite(synthTree(tree), miniOwn, miniStructs)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.violations) != 1 || !strings.Contains(res.violations[0], "R2 field chain") {
			t.Fatalf("want exactly the R2 field-chain violation, got %v", res.violations)
		}
	})
	t.Run("R3 类型夹带=红", func(t *testing.T) {
		tree := map[string]string{}
		for k, v := range baseTree {
			tree[k] = v
		}
		tree["engine/backup.go"] = backupSrc + "\nfunc smuggle(in *intake) int { return in.repos }\n"
		res, err := scanInterSatellite(synthTree(tree), miniOwn, miniStructs)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.violations) != 1 || !strings.Contains(res.violations[0], "R3 type smuggling") {
			t.Fatalf("want exactly the R3 type-smuggling violation, got %v", res.violations)
		}
	})
	t.Run("归属表双向漂移=红", func(t *testing.T) {
		disk := map[string]bool{"materials.go": true, "ghost.go": false}
		v := interSatOwnershipViolations(map[string]string{"materials.go": "intake", "gone.go": "intake"}, disk)
		if len(v) != 2 {
			t.Fatalf("want both stale-entry and unregistered-file violations, got %v", v)
		}
	})
	t.Run("struct 表漂移=红", func(t *testing.T) {
		v := interSatStructViolations(
			map[string]string{"intake": "intake", "ghost": "ghost"},
			map[string]string{"intake": "materials.go"},
			map[string]string{"materials.go": "intake"})
		if len(v) != 1 || !strings.Contains(v[0], "ghost") {
			t.Fatalf("want exactly the missing-struct violation, got %v", v)
		}
	})
}
