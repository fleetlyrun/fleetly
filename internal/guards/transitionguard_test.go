package guards

// engine 侧四件一反扫（transition.go 收口批同批契约测试，2026-10-03；镜像
// acceptanceguard 的两层执法），两个不变式：
//
//  1. 聚合 repo 的 Transit CAS 只许出现在 transition.go（类型化前门与
//     脊柱本体）——驱动路径一律经前门，from 前置/事件抑制/审计不再散落；
//  2. 同一函数内"行写 repo 调用 × outbox/audits 事实发射"配对只许出现在
//     transition.go——手写四件一编排（2026-10-03 收口前 engine 内达 13
//     处）不得回潮；豁免表带理由、双向保鲜。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
)

// transitionReadPrefixes 是聚合 repo 读面的方法名前缀。各 repo 接口注释逐个
// 核过（2026-11-24）：Next* 是「返回下一序号/版本号」的读，
// RelayCredentialBySHA256 是按 hash 查一行；Observe/Lift/AdvanceStatus/
// BeginPending/EnsureGeneration/DecideCapacity 落行，是写。不命中读面前缀的
// 方法名一律按行写——分类不确定时多执法，不漏执法。
var transitionReadPrefixes = []string{
	"List", "Get", "Count", "Find", "Latest", "Last", "Oldest", "Earliest",
	"Has", "Stats", "Team", "Active", "Driving", "Due", "Bytes", "By", "Next",
	"RelayCredentialBy",
}

// transitionIsReadMethod 报告 repo 方法名是否落在读面前缀。
func transitionIsReadMethod(name string) bool {
	for _, prefix := range transitionReadPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// transitionRepoInterfaces 是 transact.Stores 上 repo 接口的个数（防盲锚）。
var transitionRepoInterfaces int

// transitionWriteMethods / transitionRepoFields 是反射派生的两张表（包级
// 初始化——类型是编译期事实，反射不会失败；没有 *testing.T 的扫描帮手也可
// 消费）。手抄表只列了 10 个写方法名，而 Stores 上有 145 个不同方法名：
// RequestWake 这类条件 CAS 写此前不在集里，写点对守卫不可见。反射是事实，
// 手抄是抄本——repo 接口新增写方法自动入集（2026-11-24 二期设计批 0）。
var (
	transitionWriteMethods = deriveRepoWriteMethods()
	transitionRepoFields   = deriveRepoFieldNames()
)

func deriveRepoWriteMethods() map[string]bool {
	st := reflect.TypeOf(transact.Stores{})
	out := map[string]bool{}
	for i := 0; i < st.NumField(); i++ {
		ft := st.Field(i).Type
		if ft.Kind() != reflect.Interface {
			continue
		}
		transitionRepoInterfaces++
		for k := 0; k < ft.NumMethod(); k++ {
			if name := ft.Method(k).Name; !transitionIsReadMethod(name) {
				out[name] = true
			}
		}
	}
	return out
}

// deriveRepoFieldNames 返回聚合 repo 的载体字段名集（ADR-0080 事务作用域的
// 大写字段名）。不再推导「Engine 上同名的小写私有字段」：Engine 已不直接持有
// repo，而它的小写字段里有与 Stores 字段同名的**应用模块指针**（backups /
// functions / metrics …）——按名字硬套会把 e.backups.SweepInterrupted 这类
// 模块方法调用误判成行写（2026-11-24 实证）。
func deriveRepoFieldNames() map[string]bool {
	st := reflect.TypeOf(transact.Stores{})
	out := map[string]bool{}
	for i := 0; i < st.NumField(); i++ {
		out[st.Field(i).Name] = true
	}
	return out
}

// checkTransitionDerivedTables 是派生表的防盲校验：低于下限 = 反射面退化，
// 不是接口变少（2026-11-24 实测：44 个 repo 接口 / 46 个载体字段名）。
func checkTransitionDerivedTables(t *testing.T) {
	t.Helper()
	if transitionRepoInterfaces < 40 || len(transitionWriteMethods) < 40 || len(transitionRepoFields) < 40 {
		t.Fatalf("transact.Stores reflection saw %d repo interfaces, %d write method names, %d repo field names (floors 40/40/40) — the scan is blind, fix it", transitionRepoInterfaces, len(transitionWriteMethods), len(transitionRepoFields))
	}
}

// engineFourInOneExemptions 是函数名 → 理由（行写 × 事实发射共存但确不
// 属四件一的函数）。豁免不再命中即红。
var engineFourInOneExemptions = map[string]string{
	// nodes 表是观测缓存（架构 §1"nodes 表只是观测缓存"）而非聚合状态：
	// node.left 发射与观测缓存下线标记是相互独立的 best-effort 观测事实
	//（各自容错、行保留），无"CAS+事件+审计同事务"的原子配对语义。
	"reconcileNodes": "observation cache (arch §1) best-effort facts; node.left emission and unavailable-mark are independent, no atomic pairing",
	// 同上：缓存 upsert 与仅铸造时发的 node.joined 独立容错（nodes 表非
	// 权威，参与决策前必直读）。
	"handleNodeJoined": "observation cache upsert + mint-only event, independently tolerated; nodes table is not authoritative",
}

// engineFourInOneDebt 是函数名 → 理由（经裁决**属四件一而未做**的写点：行写
// 与事实发射不在同一事务）。与豁免表分册——豁免是「这样是对的」，本表是
// 「这样是欠账」。条目随修复同批删除；双向保鲜：不再命中即红。
//
// 2026-11-24 首次入册的八处此前对守卫不可见：行写方法名集是手抄表（只列了
// 10 个名字——MarkRunning / FinishSucceeded / DeleteRelayCredential 都不在），
// 且判违不看经辅助函数的发射。
var engineFourInOneDebt = map[string]string{
	"WakeFunction":            "capacity CAS (RequestWake) then a best-effort function.scaled fact via emitWakeFact — not atomic; ruled D8 (2026-11-24): moves into the functions module through the four-in-one write front in phase-two batch 4",
	"revokeDepartedNodeRelay": "the relay credential row is deleted, then the node.relay_credential_revoked audit is emitted separately — a security write whose audit is not atomic with the delete; filed 2026-11-24, fix pending",
	"executeDatabaseBackup":   "backup row status written directly, events emitted afterwards (ADR-0039 form, not atomic); filed 2026-11-24, backups wave",
	"executeOneBackup":        "backup row FinishFailed written directly, backup.failed emitted afterwards (ADR-0039 form, not atomic); filed 2026-11-24, backups wave",
	"executePubSubBackup":     "backup row status written directly, events emitted afterwards (ADR-0039 form, not atomic); filed 2026-11-24, backups wave",
	"restoreDatabase":         "restore status columns written directly, database.restored emitted afterwards (not atomic); filed 2026-11-24, backups wave",
	"restorePubSub":           "restore status columns written directly, the restored fact emitted afterwards (not atomic); filed 2026-11-24, backups wave",
	"sealBackupSet":           "backup row FinishSucceeded written directly, backup.succeeded emitted afterwards (ADR-0039 form, not atomic); filed 2026-11-24, backups wave",
}

// engineFuncScan 解析 internal/application/engine 与已拆应用模块包
// （应用模块集（import 图派生 applicationModules），P5 首项扫描面扩容）全部非测试 .go，
// 产出：文件相对路径 → 函数名 → 该函数体含 Transit 直调/行写/事实发射位。
//
// P3-0 接收者泛化（2026-10-15）：两遍扫描。第一遍收集包内全部命名类型名
// （Engine、execs、Loop……动态全集，后续批域 struct 改名零维护）；
// 第二遍逐函数解析 engine-self 标识符集后做调用链分析。P3 域化九批会把
// 字段与行为收编进未导出域 struct，方法接收者随之不再是 *Engine、名字
// 不再是 e，repo 调用链也会变多级（x.repos.tasks.Transit）——字面 e +
// 两层选择器匹配会落盲区，故按声明动态解析 self、按链匹配调用。
type engineFuncFacts struct {
	file     string // 文件相对路径
	pkg      string // 包目录——同包调用传播的边界
	owner    string // 接收者基类型名（包级函数为空）
	calls    map[string]bool
	transit  bool // <self>.<repo>.Transit 直调（任意链深）
	rowWrite bool // 裸行写：<self>…<repo>.<行写方法>，且不在提交原语的参数里
	factEmit bool // 函数体内直接发射：<self>.outbox/audits.Append、emitObservation、writes.Emit/Sequence
	// indirectEmit：本函数经同包直接调用链（≤4 跳）到达一个发射函数。
	indirectEmit bool
}

func scanEngineFuncs(t *testing.T) map[string]engineFuncFacts {
	t.Helper()
	checkTransitionDerivedTables(t)
	fset := token.NewFileSet()
	// P5 首项扫描面扩容：engine 树 + 已拆应用模块包（应用模块集（import 图派生）
	// 登记表）——应用模块经注入端口/repos 的行写与事实发射配对同样在本律
	// 射程（拆包不是执法豁免）；类型全集跨树合并（应用模块接收者同权入 self
	// 集，跨包重名类型由 P5 拆包登记表管住——每模块独立成包后包内命名
	// 空间不再强制全域唯一，重名红即提示登记表收紧）。
	roots := append([]string{filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))}, satellitePackageRoots(t)...)

	// 第一遍：包内命名类型全集 + 缓存解析结果供第二遍复用（单次 WalkDir）。
	engineTypes := map[string]bool{}
	var parsed []*ast.File
	var rels []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						engineTypes[ts.Name.Name] = true
					}
				}
			}
			parsed = append(parsed, file)
			rels = append(rels, filepath.ToSlash(path[len(repoRoot(t))+1:]))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// 第二遍：逐函数解析 self 集并按调用链归类。
	funcs := map[string]engineFuncFacts{}
	for i, file := range parsed {
		rel := rels[i]
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := fd.Name.Name + "@" + rel
			facts := engineFuncFacts{file: rel, pkg: filepath.Dir(rel), calls: map[string]bool{}}
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				if base := baseIdent(fd.Recv.List[0].Type); base != nil {
					facts.owner = base.Name
				}
			}
			owner := facts.owner
			self := engineSelfIdents(fd, engineTypes)
			for name := range engineStoresIdents(fd) {
				self[name] = true
			}
			// 包在提交原语参数里的行写不是裸写：commitWrite(ctx, s, WriteFact{…})
			// 是四件一原语本体（行写 + 事件 + 审计在 transition.go 同事务），其
			// Write 闭包里的 s.X.Create(...) 是合法形态；WriteStep{Write: …} 字面量
			// 同理（mintOneShotTask 等复合原语消费，同事务记账发事件）。裸写 =
			// 原语之外的直接行写调用。不区分的话，发射沿调用链传播后原语的每个
			// 调用者都会被判成手搓编排（2026-11-24 实证 16 个命中里 7 个如此）。
			wrapped := map[*ast.CallExpr]bool{}
			// ast.Inspect 的前序/后序回调配一个「祖先是否经原语」的栈：进入提交
			// 原语调用或 WriteStep 字面量后压 true，子树内的行写调用即记为已包裹。
			stack := []bool{false}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if n == nil { // 后序：退出一个节点
					if len(stack) > 1 {
						stack = stack[:len(stack)-1]
					}
					return false
				}
				inside := stack[len(stack)-1]
				switch x := n.(type) {
				case *ast.CallExpr:
					if isCommitPrimitiveCall(x) {
						inside = true
					}
				case *ast.CompositeLit:
					if isWriteStepLiteral(x) {
						inside = true
					}
				}
				stack = append(stack, inside)
				if c, ok := n.(*ast.CallExpr); ok && inside && isRepoWriteCall(c, self) {
					wrapped[c] = true
				}
				return true
			})
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				base, segs := selectorChain(sel)
				terminal := sel.Sel.Name
				// 同包调用边：包级函数 call(...) 记 ".name"；对自身接收者的
				// 单段方法调用 x.method(...)（x ∈ self 集）记 "Owner.method"。
				// 只认同接收者类型的方法与同包自由函数——跨类型的同名方法不算。
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					facts.calls["."+fun.Name] = true
				case *ast.SelectorExpr:
					if base != nil && len(segs) == 1 && self[base.Name] {
						facts.calls[owner+"."+segs[0]] = true
					}
				}
				if base != nil && self[base.Name] {
					// 行写：链上任一段命中聚合 repo 字段即按末端方法归类——
					// 多级链 x.repos.tasks.Transit 与两层链 e.tasks.Transit
					// 同权。false positive 检查：e.db.Runner 的 db、
					// e.build.logs.write 的 build（repoFields 是 builds）均
					// 不在 repoFields，不误伤。
					inRepo := false
					for _, seg := range segs {
						if transitionRepoFields[seg] {
							inRepo = true
							break
						}
					}
					if inRepo {
						switch {
						case terminal == "Transit":
							facts.transit = true
							facts.rowWrite = true
						case transitionWriteMethods[terminal]:
							if !wrapped[call] {
								facts.rowWrite = true
							}
						}
					}
					// 事实发射：outbox/audits 的 Append（任意链深）与便捷前门
					// emitObservation（P3 域化后接收者随域 struct 换名仍在列）。
					switch {
					case terminal == "Append":
						for _, seg := range segs {
							if seg == "outbox" || seg == "audits" || seg == "Outbox" || seg == "Audits" {
								facts.factEmit = true
								break
							}
						}
					case terminal == "emitObservation":
						facts.factEmit = true
					}
				}
				// P2 刀 1 起事实发射收编进 ledger kernel（writes.Emit /
				// writes.Sequence——本体住 transition.go；acceptance.go 受理位
				// 自 P3b（ADR-0087 决策 4 写原语腿）起消费 engine 写原语
				// Commit，不再持本体），包限定形态 base 是包名、不必在
				// self 集，契约测试不因 API 换形失明。
				if base != nil && base.Name == "writes" &&
					(terminal == "Emit" || terminal == "Sequence") {
					facts.factEmit = true
				}
				return true
			})
			funcs[key] = facts
		}
	}
	propagateIndirectEmission(funcs)
	// 防盲自检：扫描面必须足够大，且 transition.go 里必须真有 Transit
	// 直调（前门本体）——扫描器失明即红。
	if len(funcs) < 150 {
		t.Fatalf("engine scan found only %d functions — guard is blind, fix the scan", len(funcs))
	}
	sawTransitFront := false
	for _, f := range funcs {
		if f.transit && f.file == "internal/application/engine/transition.go" {
			sawTransitFront = true
		}
	}
	if !sawTransitFront {
		t.Fatalf("scan found no Transit call in transition.go — guard is blind, fix the scan")
	}
	return funcs
}

// commitPrimitiveNames 是四件一提交原语的方法名——行写闭包包在它们的参数里
// 即「经原语」。普通事务（Tx.Do）不在列：事务里裸写不是四件一。
var commitPrimitiveNames = map[string]bool{
	"commitWrite": true, "CommitWrite": true, "Commit": true, "commit": true,
}

// isCommitPrimitiveCall 报告 call 是否调用了提交原语。
func isCommitPrimitiveCall(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return commitPrimitiveNames[fun.Name]
	case *ast.SelectorExpr:
		return commitPrimitiveNames[fun.Sel.Name]
	}
	return false
}

// isWriteStepLiteral 报告字面量是否是 WriteStep{…} 或 []WriteStep{{…}}。
func isWriteStepLiteral(lit *ast.CompositeLit) bool {
	switch t := lit.Type.(type) {
	case *ast.Ident:
		return t.Name == "WriteStep"
	case *ast.ArrayType:
		if id, ok := t.Elt.(*ast.Ident); ok {
			return id.Name == "WriteStep"
		}
	}
	return false
}

// isRepoWriteCall 报告 call 是否为 <self>…<repo>.<行写方法> 形态（任意链深）。
func isRepoWriteCall(call *ast.CallExpr, self map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !transitionWriteMethods[sel.Sel.Name] {
		return false
	}
	base, segs := selectorChain(sel)
	if base == nil || !self[base.Name] {
		return false
	}
	for _, seg := range segs {
		if transitionRepoFields[seg] {
			return true
		}
	}
	return false
}

// propagateIndirectEmission 把事实发射沿同包直接调用链向上传播（至多 4 跳，
// 同包自由函数与同接收者类型的方法两种边）。
//
// 为什么需要：判违条件是「行写 × 事实发射同函数共存」，把发射拆进一个辅助
// 函数就落在射程之外——WakeFunction（行写 CAS）+ emitWakeFact（发射）正是
// 这个形态，守卫此前看不见（2026-11-23 三步实验：只补两张表仍绿，把发射
// 内联回 WakeFunction 才红）。
func propagateIndirectEmission(funcs map[string]engineFuncFacts) {
	byKey := make(map[string][]string, len(funcs)) // pkg|owner.name → funcs 键
	for k, f := range funcs {
		name := k[:strings.Index(k, "@")]
		id := f.pkg + "|" + f.owner + "." + name
		byKey[id] = append(byKey[id], k)
	}
	for hop := 0; hop < 4; hop++ {
		changed := false
		for k, f := range funcs {
			if f.factEmit || f.indirectEmit {
				continue
			}
			for callee := range f.calls {
				emits := false
				for _, gk := range byKey[f.pkg+"|"+callee] {
					if g := funcs[gk]; g.factEmit || g.indirectEmit {
						emits = true
						break
					}
				}
				if emits {
					f.indirectEmit = true
					funcs[k] = f
					changed = true
					break
				}
			}
		}
		if !changed {
			return
		}
	}
}

// engineSelfIdents 解析函数的 engine-self 标识符集：接收者基类型 ∈ 包内
// 命名类型集（Engine 本体或 P3 域化后的任意域 struct）时接收者名入集；
// Engine/*Engine 形参名入集（自由助手 func fourInOne(eng *Engine) 形态）。
func engineSelfIdents(fd *ast.FuncDecl, engineTypes map[string]bool) map[string]bool {
	self := map[string]bool{}
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		recv := fd.Recv.List[0]
		if base := baseIdent(recv.Type); base != nil && engineTypes[base.Name] {
			for _, name := range recv.Names {
				self[name.Name] = true
			}
		}
	}
	if fd.Type != nil && fd.Type.Params != nil {
		for _, param := range fd.Type.Params.List {
			if !isEngineType(param.Type) {
				continue
			}
			for _, name := range param.Names {
				self[name.Name] = true
			}
		}
	}
	return self
}

// engineStoresIdents 收集函数体内 transact.Stores 类型的参数名（含嵌套
// 闭包——四件一拍经 Tx.Do 的闭包 s 走事务作用域 repo，ADR-0080；这些名
// 字与 Engine 接收者同权进入 self 集）。
func engineStoresIdents(fd *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	if fd.Body == nil {
		return names
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		fl, ok := n.(*ast.FuncLit)
		if !ok || fl.Type == nil || fl.Type.Params == nil {
			return true
		}
		for _, p := range fl.Type.Params.List {
			sel, ok := p.Type.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "transact" || sel.Sel.Name != "Stores" {
				continue
			}
			for _, name := range p.Names {
				names[name.Name] = true
			}
		}
		return true
	})
	return names
}

// selectorChain 沿选择器调用链下行到基 Ident，返回基与自基向末的段名序列
// （x.repos.tasks.Transit → base=x, segs=[repos tasks]）；链基非 Ident
// （foo().tasks.Transit 形态）返回 nil——无 self 归属，不匹配。
func selectorChain(sel *ast.SelectorExpr) (*ast.Ident, []string) {
	var segs []string
	x := ast.Expr(sel)
	for {
		s, ok := x.(*ast.SelectorExpr)
		if !ok {
			break
		}
		segs = append(segs, s.Sel.Name)
		x = s.X
	}
	id, ok := x.(*ast.Ident)
	if !ok {
		return nil, nil
	}
	for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
		segs[i], segs[j] = segs[j], segs[i]
	}
	return id, segs
}

// baseIdent 解析类型表达式的基类型 Ident（剥一层 * 与泛型实例化实参）。
func baseIdent(expr ast.Expr) *ast.Ident {
	switch t := expr.(type) {
	case *ast.Ident:
		return t
	case *ast.StarExpr:
		return baseIdent(t.X)
	case *ast.IndexExpr:
		return baseIdent(t.X)
	case *ast.IndexListExpr:
		return baseIdent(t.X)
	}
	return nil
}

// isEngineType 报告类型表达式是否 Engine/*Engine 字面形态（严格字面：
// 别名、限定名 pkg.Engine、切片/映射元素位不认——守卫语义是"递了引擎
// 壳本体"，扩散形态各归各的契约测试）。
func isEngineType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Engine"
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name == "Engine"
		}
	}
	return false
}

// TestTransitCASLivesOnlyInTransition：聚合 repo 的 Transit 直调只许在
// transition.go。
func TestTransitCASLivesOnlyInTransition(t *testing.T) {
	funcs := scanEngineFuncs(t)
	var bad []string
	for key, f := range funcs {
		if f.transit && f.file != "internal/application/engine/transition.go" {
			bad = append(bad, key)
		}
	}
	sort.Strings(bad)
	for _, k := range bad {
		t.Errorf("%s calls a state repo Transit directly — route it through the transition.go typed front (from-precondition, event suppression, and audit live in one place)", k)
	}
}

// TestNoHandRolledFourInOneChoreography：行写 × 事实发射的配对只许在
// transition.go（或豁免表带理由）。
func TestNoHandRolledFourInOneChoreography(t *testing.T) {
	funcs := scanEngineFuncs(t)
	used, usedDebt := map[string]bool{}, map[string]bool{}
	var bad []string
	var indirect []string
	for key, f := range funcs {
		if !f.rowWrite || !(f.factEmit || f.indirectEmit) {
			continue
		}
		if f.file == "internal/application/engine/transition.go" {
			continue
		}
		if f.indirectEmit && !f.factEmit {
			indirect = append(indirect, key)
		}
		name := key[:strings.Index(key, "@")]
		if reason, ok := engineFourInOneExemptions[name]; ok && reason != "" {
			used[name] = true
			continue
		}
		if reason, ok := engineFourInOneDebt[name]; ok && reason != "" {
			usedDebt[name] = true
			continue
		}
		bad = append(bad, key)
	}
	sort.Strings(bad)
	for _, k := range bad {
		t.Errorf("%s pairs a row write with outbox/audit emission — hand-rolled four-in-one choreography; declare a writeFact/transitionFact and go through transition.go's commit primitives", k)
	}
	// 防盲：拆开发射形态必须持续可见（WakeFunction + emitWakeFact 现役在册）。
	if len(indirect) < 1 {
		t.Errorf("no function reaches fact emission through a same-package helper — the call-chain propagation stopped seeing edges (WakeFunction -> emitWakeFact must stay visible while its debt entry stands)")
	}
	// 双向保鲜：豁免不再命中即红。
	for name, reason := range engineFourInOneExemptions {
		if reason == "" {
			t.Errorf("four-in-one exemption %s must carry a reason", name)
		}
		if !used[name] {
			t.Errorf("four-in-one exemption %s no longer matches a real pairing; remove the entry", name)
		}
	}
	// 欠账表同样双向保鲜：条目不再命中 = 该写点已修，同批删条目。
	for name, reason := range engineFourInOneDebt {
		if reason == "" {
			t.Errorf("four-in-one debt entry %s must carry a reason", name)
		}
		if !usedDebt[name] {
			t.Errorf("four-in-one debt entry %s no longer matches a real pairing — the write was fixed or renamed; remove the entry in the same commit", name)
		}
	}
}
