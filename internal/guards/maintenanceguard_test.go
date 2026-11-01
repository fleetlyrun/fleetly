package guards

// 载体写动词维护锁反扫（R13-L1 / ADR-0046 决策 3）：`maintenanceMu` 的读
// 半边纪律——**engine 包内每个调用 Runtime 载体写动词的函数都必须在调用
// 之前持读锁**（maintenance 端口的锁入口，见 maintenanceLockEntryMethods），
// 与 Ensure 族及网络重建写半边串行化。新增写动词 = 新调用点必须同批补锁；
// 漏锁/锁在调用之后即红；显式豁免必须带理由**且**满足机械条件（唯一调用
// 者，且调用者在该调用前已持读锁——防「豁免表变成盲区」）。豁免不再命中
// 即红（双向保鲜）。
//
// 判据是 AST（注释免疫）。P4 wave-0（ADR-0088）起**按类型认定**（两遍
// AST，transitionguard engineSelfIdents 范式）：第一遍收全包具名 struct 的
// `capability.Runtime` 型字段（Engine.runtime 与未来卫星 struct 自持字段
// 同权——wave-1 接收者迁移后接收者名/字段名不再恒为 e/runtime，字段名字
// 面判据会静默致盲，这是 wave-0 先行于一切接收者迁移的时序理由）；第二遍
// 绑定「接收者/形参标识符 × 其类型可达的 Runtime 字段」后匹配
// `<标识符>.<Runtime 字段>.<载体写动词>`。**绑定接收者类型 × 字段**防跨
// struct 同名误报（Ensure/Remove 是通用名）。已知局限（与 transitionguard
// 同口径）：函数体内局部构造的 struct 值不经绑定、其载体调用不可见——封
// 堵由评审承载。锁与调用的先后按 token 序比较（同一函数体内先锁后写）。
// 递归持读锁被 sync.RWMutex 明令禁止（写者等待时二次 RLock 会死锁），所以
// 「整步持锁 + 内部收敛函数裸调」是唯一合法形态，由 Caller 豁免机制登记。
//
// 锁入口判定是**方法名集** maintenanceLockEntryMethods（现集 =
// {lockMaintenance}，为 maintenance 端口（ADR-0088 决策 3 / 设计稿 §5 的
// gate 注入形态）留扩展位）。扩展协议：端口方法名入集必须同批补正向锚腿
// （判据自检夹具或 testdata/maintenanceanchor 卫星锚上「该方法名先锁后
// 写=绿、不锁=红」两腿）——入集不加锚，新入口即无活体证明。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// maintenanceCarrierWriteVerbs 是 Runtime 端口上的载体写动词集（Ensure 族 +
// Remove + 网络 attach/detach）。Runtime 契约新增写动词时本表同批扩列（表
// 与契约漂移由扫描下限锚兜底：命中点数量低于下限即红）。
var maintenanceCarrierWriteVerbs = map[string]bool{
	"Ensure":        true,
	"Remove":        true,
	"AttachNetwork": true,
	"DetachNetwork": true,
}

// maintenanceLockEntryMethods 是维护读锁入口的方法名集（判据的一部分）。
// 现集唯 lockMaintenance（networkrebuild.go）；maintenance 端口成形后其
// gate 方法名入集须遵守文件头扩展协议（同批补正向锚腿）。
var maintenanceLockEntryMethods = map[string]bool{
	"lockMaintenance": true,
}

// maintenanceLockExemption 是一条豁免：Reason 必填；Caller 是**唯一**调用
// 者函数名（该调用者必须在调用本函数之前已持 maintenance 读锁）；
// CallerFile 非空时以 文件:函数 双键限定调用者（P5 wave-4：各卫星环步
// 统一命名 Step——backups/browses/logpipes/metrics/manageds 五星同名，
// 裸函数名在合并扫描面上不再唯一，豁免必须落到文件轴）。
type maintenanceLockExemption struct {
	Reason     string
	Caller     string
	CallerFile string
}

// maintenanceLockExemptions 是 engine 函数名 → 豁免（当前仅整步持锁形态）。
var maintenanceLockExemptions = map[string]maintenanceLockExemption{
	// manageds.Step（P5 wave-4 前名 managedStep）在函数入口取读锁并 defer
	// 释放（整步覆盖），内部收敛函数在同一步内直接调 Ensure——按唯一
	// 调用者 + 调用者持锁机制豁免，避免递归 RLock（sync.RWMutex 写者等待
	// 时二次读锁会死锁）。CallerFile 限定到卫星包的 managed.go（Step 名
	// 五星同形）。
	"reconcileManaged": {
		Reason:     "called only from manageds.Step, which holds the maintenance read lock for the whole step",
		Caller:     "Step",
		CallerFile: "managed.go",
	},
}

// maintenanceCarrierCall 是一次载体写调用点（用于豁免的调用者持锁验证）。
type maintenanceCarrierCall struct {
	caller string
	file   string
	pos    token.Pos
}

// maintenanceFuncInfo 是一个包内函数的判据面。rtIdents 绑定「标识符 → 该
// 标识符类型上可达的 capability.Runtime 型字段名集」（两遍 AST 第二遍的
// 绑定产物，判据的类型面）。
type maintenanceFuncInfo struct {
	name     string
	file     string
	body     *ast.BlockStmt
	lockPos  token.Pos // 本函数体内最早的锁入口调用（NoPos = 未持锁）
	rtIdents map[string]map[string]bool
}

// maintenancePendingFunc 是两遍 AST 的中间产物：第一遍挂起的函数声明，
// 待类型索引齐备后由 bindMaintenanceFuncs 绑定判据面。
type maintenancePendingFunc struct {
	name string
	file string
	fd   *ast.FuncDecl
}

// maintenancePackageScan 是一次 engine 包扫描的产物。
type maintenancePackageScan struct {
	files int
	calls int // 载体写调用点总数（防盲锚）
	funcs map[string][]maintenanceFuncInfo
	// runtimeFields 是具名 struct 类型 → capability.Runtime 型字段名集
	//（两遍 AST 第一遍的产物；Engine 与未来卫星 struct 同权入册）。
	runtimeFields map[string]map[string]bool
	sawEngine     bool // Engine 类型声明可见（防盲）
	pending       []maintenancePendingFunc
	// carrierCalls 记录载体写调用点（caller + 位置）：豁免验证用。
	carrierCalls []maintenanceCarrierCall
	// allCalls 记录全部「函数名调用」点（callee → 调用点）：唯一调用者
	// 验证用（只用于豁免函数的调用者计数）。
	allCalls map[string][]maintenanceCarrierCall
}

// isCapabilityRuntimeType 报告类型表达式是否 capability.Runtime 字面形态
// （字段判据的类型面；指针/别名形态不认——Engine 卫星字段按端口直持）。
func isCapabilityRuntimeType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "capability" && sel.Sel.Name == "Runtime"
}

// ingestMaintenanceFile 并入一个已解析文件（两遍 AST 第一遍：类型索引 +
// 函数挂起；真实包扫描与判据自检夹具同权）。
func ingestMaintenanceFile(path string, file *ast.File, scan *maintenancePackageScan) {
	scan.files++
	base := filepath.Base(path)
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
			if ts.Name.Name == "Engine" {
				scan.sawEngine = true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, f := range st.Fields.List {
				if !isCapabilityRuntimeType(f.Type) {
					continue
				}
				if scan.runtimeFields[ts.Name.Name] == nil {
					scan.runtimeFields[ts.Name.Name] = map[string]bool{}
				}
				for _, name := range f.Names {
					scan.runtimeFields[ts.Name.Name][name.Name] = true
				}
			}
		}
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		scan.pending = append(scan.pending, maintenancePendingFunc{name: fd.Name.Name, file: base, fd: fd})
	}
}

// runtimeTypedIdents 解析函数的「标识符 → 可达 Runtime 字段」绑定：接收者
// 基类型与形参基类型落在 runtimeFields 索引内时，其名字入绑定（自由函数
// 持 *T 形态同权——engineSelfIdents 范式）。
func runtimeTypedIdents(fd *ast.FuncDecl, runtimeFields map[string]map[string]bool) map[string]map[string]bool {
	rt := map[string]map[string]bool{}
	bind := func(expr ast.Expr, names []*ast.Ident) {
		base := baseIdent(expr)
		if base == nil {
			return
		}
		fields, ok := runtimeFields[base.Name]
		if !ok || len(fields) == 0 {
			return
		}
		for _, n := range names {
			rt[n.Name] = fields
		}
	}
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		bind(fd.Recv.List[0].Type, fd.Recv.List[0].Names)
	}
	if fd.Type != nil && fd.Type.Params != nil {
		for _, p := range fd.Type.Params.List {
			bind(p.Type, p.Names)
		}
	}
	return rt
}

// bindMaintenanceFuncs 执行两遍 AST 的第二遍：按类型索引绑定各函数判据面
// 后走函数体（锁位 + 调用点）。
func (s *maintenancePackageScan) bindMaintenanceFuncs() {
	for _, p := range s.pending {
		info := maintenanceFuncInfo{
			name: p.name, file: p.file, body: p.fd.Body,
			lockPos:  token.NoPos,
			rtIdents: runtimeTypedIdents(p.fd, s.runtimeFields),
		}
		scanFuncBody(p.name, p.file, p.fd.Body, s, &info)
		s.funcs[info.name] = append(s.funcs[info.name], info)
	}
	s.pending = nil
}

// maintenanceCarrierWriteCall 报告 call 是否是类型绑定面上的载体写动词调用：
// `<绑定标识符>.<Runtime 型字段>.<载体写动词>`（类型×字段绑定，跨 struct
// 同名字段不误报）。
func maintenanceCarrierWriteCall(call *ast.CallExpr, rtIdents map[string]map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !maintenanceCarrierWriteVerbs[sel.Sel.Name] {
		return false
	}
	field, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	base, ok := field.X.(*ast.Ident)
	if !ok {
		return false
	}
	fields, bound := rtIdents[base.Name]
	return bound && fields[field.Sel.Name]
}

// calledFuncName 返回调用目标的方法名（selector / ident 两形态）。
func calledFuncName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel.Name
	case *ast.Ident:
		return fun.Name
	}
	return ""
}

// scanMaintenancePackage 解析 roots 下全部非测试 .go（跳过 testdata/）并
// 入扫描（真实 engine 包 + 已拆卫星包〔P5 首项扫描面扩容〕与 testdata
// 合成锚同权）。
func scanMaintenancePackage(t *testing.T, roots ...string) *maintenancePackageScan {
	t.Helper()
	scan := &maintenancePackageScan{
		funcs:         map[string][]maintenanceFuncInfo{},
		runtimeFields: map[string]map[string]bool{},
		allCalls:      map[string][]maintenanceCarrierCall{},
	}
	fset := token.NewFileSet()
	for _, root := range roots {
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
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			ingestMaintenanceFile(path, f, scan)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scan.files == 0 {
		t.Fatalf("maintenance scan found no production .go under %v — guard is blind, fix the scan", roots)
	}
	scan.bindMaintenanceFuncs()
	return scan
}

// violations 返回「载体写调用早于本函数锁入口（或未持锁）」的函数名
// （file:fn 形态，排序）。豁免不在此处消费。
func (s *maintenancePackageScan) violations() []string {
	var out []string
	for _, calls := range s.funcs {
		for _, fn := range calls {
			// 逐个调用点无法直接反查位置→函数体；按函数体重新走一遍。
			early := false
			ast.Inspect(fn.body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !maintenanceCarrierWriteCall(call, fn.rtIdents) {
					return true
				}
				if fn.lockPos == token.NoPos || call.Pos() < fn.lockPos {
					early = true
				}
				return true
			})
			if early {
				out = append(out, fn.file+":"+fn.name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// exemptionViolation 验证一条豁免的机械条件：免除函数的唯一调用者必须是
// 登记的 Caller，且该调用者在该调用点之前已持读锁。
func (s *maintenancePackageScan) exemptionViolation(fn string, ex maintenanceLockExemption) string {
	var callers []maintenanceCarrierCall
	callerSet := map[string]bool{}
	for _, c := range s.allCalls[fn] {
		if c.caller == fn {
			continue // 自调用不算入口
		}
		callers = append(callers, c)
		callerSet[c.caller] = true
	}
	if len(callerSet) != 1 || !callerSet[ex.Caller] {
		names := make([]string, 0, len(callerSet))
		for name := range callerSet {
			names = append(names, name)
		}
		sort.Strings(names)
		return "exemption for " + fn + " requires exactly one caller " + ex.Caller + ", found [" + strings.Join(names, ",") + "]"
	}
	for _, c := range callers {
		holder, ok := s.singleFunc(ex.Caller, ex.CallerFile)
		if !ok {
			return "exemption caller " + ex.Caller + " for " + fn + " not found in the scanned packages"
		}
		if holder.lockPos == token.NoPos || c.pos < holder.lockPos {
			return "exemption caller " + ex.Caller + " does not hold the maintenance read lock before calling " + fn
		}
	}
	return ""
}

// singleFunc 返回唯一同名函数（重名 = 判据不成立，返回 false）。fileKey
// 非空时按 文件:函数 双键过滤（P5 wave-4：卫星环步统一命名 Step，跨包
// 合并扫描面上同名并存）。
func (s *maintenancePackageScan) singleFunc(name, fileKey string) (maintenanceFuncInfo, bool) {
	infos := s.funcs[name]
	if fileKey != "" {
		var filtered []maintenanceFuncInfo
		for _, fn := range infos {
			if fn.file == fileKey {
				filtered = append(filtered, fn)
			}
		}
		infos = filtered
	}
	if len(infos) != 1 {
		return maintenanceFuncInfo{}, false
	}
	return infos[0], true
}

// TestMaintenanceLockScannerJudgment 是判据自检（夹具源码，不经真实包）：
// 先锁后写 = 通过；无锁 = 红；锁在写之后 = 红（同函数内顺序也算纪律）；
// 非 Runtime 端口接收者与跨 struct 同名字段（类型绑定）不误报。
func TestMaintenanceLockScannerJudgment(t *testing.T) {
	src := `package engine

type Engine struct{ runtime capability.Runtime }

// sameShell 是同名异型腿：字段名撞 runtime 但类型非 capability.Runtime，
// 类型绑定后不判（防跨 struct 同名误报——Ensure/Remove 是通用名）。
type sameShell struct{ runtime int }

func (e *Engine) locked(ctx context.Context) {
	unlock := e.lockMaintenance()
	defer unlock()
	_ = e.runtime.Remove(ctx, ns)
}

func (e *Engine) unlocked(ctx context.Context) {
	_ = e.runtime.Ensure(ctx, ns, nil, 0, materials)
}

func (e *Engine) lateLock(ctx context.Context) {
	_ = e.runtime.Remove(ctx, ns)
	unlock := e.lockMaintenance()
	_ = unlock
}

func (e *Engine) unrelated(ctx context.Context) {
	_ = e.other.Remove(ctx, ns) // 非 Runtime 字段：不判
}

func (s *sameShell) sameNameField(ctx context.Context) {
	_ = s.runtime.Ensure(ctx, ns, nil, 0, materials) // 同名字段非 Runtime 型：类型绑定后不判
}
`
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	scan := &maintenancePackageScan{
		funcs:         map[string][]maintenanceFuncInfo{},
		runtimeFields: map[string]map[string]bool{},
		allCalls:      map[string][]maintenanceCarrierCall{},
	}
	ingestMaintenanceFile("fixture.go", f, scan)
	scan.bindMaintenanceFuncs()
	got := scan.violations()
	want := []string{"fixture.go:lateLock", "fixture.go:unlocked"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("violations = %v, want %v", got, want)
	}
	if scan.calls != 3 {
		t.Fatalf("fixture carrier-write call sites = %d, want 3 (needle regression; same-name non-Runtime field must not count)", scan.calls)
	}
}

// scanFuncBody 走一个函数体：记录锁位置与调用点（真实包扫描与判据自检
// 共享同一判据实现）。
func scanFuncBody(name, file string, body *ast.BlockStmt, scan *maintenancePackageScan, info *maintenanceFuncInfo) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if maintenanceLockEntryMethods[calledFuncName(call)] {
			if info.lockPos == token.NoPos || call.Pos() < info.lockPos {
				info.lockPos = call.Pos()
			}
		}
		if called := calledFuncName(call); called != "" {
			scan.allCalls[called] = append(scan.allCalls[called], maintenanceCarrierCall{caller: name, file: file, pos: call.Pos()})
		}
		if maintenanceCarrierWriteCall(call, info.rtIdents) {
			scan.calls++
			scan.carrierCalls = append(scan.carrierCalls, maintenanceCarrierCall{caller: name, file: file, pos: call.Pos()})
		}
		return true
	})
}

// TestMaintenanceLockSatelliteRuntimeFieldAnchor：判据泛化的卫星正向锚
// （ADR-0088 P4 wave-0，testdata/maintenanceanchor 合成样本）。卫星形
// struct 自持 capability.Runtime 型字段（wave-1 接收者迁移后的形态——
// 字段名/接收者名与 Engine 全不同）：先锁后写=绿、不锁=红、锁在写之后=
// 红（token 序判据复用）。字面判据（字段名 runtime）对该形态静默致盲，
// 本锚是类型化判据的活体证明。
func TestMaintenanceLockSatelliteRuntimeFieldAnchor(t *testing.T) {
	scan := scanMaintenancePackage(t, filepath.Join(repoRoot(t), "internal", "guards", "testdata", "maintenanceanchor"))
	got := scan.violations()
	want := []string{"anchor.go:bareWrite", "anchor.go:lockAfterWrite"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("satellite anchor violations = %v, want %v — the type-based judgment must keep enforcing self-held Runtime fields (old literal criterion is blind here)", got, want)
	}
	if scan.calls != 3 {
		t.Fatalf("satellite anchor carrier-write call sites = %d, want 3 (needle regression)", scan.calls)
	}
}

// TestEngineCarrierWriteVerbsHoldMaintenanceLock 反扫真实 engine 包 + 已拆
// 卫星包（P5 首项扫描面扩容：engineSatellitePackages 登记表——卫星自持
// Runtime 字段的载体写动词同样在锁纪律射程，拆包不是执法豁免）。
func TestEngineCarrierWriteVerbsHoldMaintenanceLock(t *testing.T) {
	roots := append([]string{filepath.Join(repoRoot(t), "internal", "application", "engine")}, satellitePackageRoots(t)...)
	scan := scanMaintenancePackage(t, roots...)
	// 防盲锚：扫描面文件数与写调用点下限（目录/针漂移导致的零扫描即红）。
	if scan.files < 40 {
		t.Fatalf("scanned only %d engine files — guard is blind, fix the scan", scan.files)
	}
	if scan.calls < 11 {
		t.Fatalf("found only %d carrier write call sites — the needle is stale (want >= 11)", scan.calls)
	}
	// 防盲锚：Engine 声明可见 + 类型索引必须收到 Engine 的 Runtime 型字段
	//（索引失明 = 类型化判据整体失明）。
	if !scan.sawEngine {
		t.Fatalf("scan found no Engine type declaration in the engine package — guard is blind, fix the scan")
	}
	if len(scan.runtimeFields["Engine"]) == 0 {
		t.Fatalf("type index saw no capability.Runtime field on Engine — the type-based judgment is blind, fix the scan")
	}

	reported := map[string]bool{}
	for _, v := range scan.violations() {
		fn := v[strings.LastIndex(v, ":")+1:]
		if ex, ok := maintenanceLockExemptions[fn]; ok && ex.Reason != "" {
			if msg := scan.exemptionViolation(fn, ex); msg != "" {
				t.Errorf("%s: %s", v, msg)
			}
			reported[fn] = true
			continue
		}
		t.Errorf("%s calls a Runtime carrier write verb without holding the maintenance read lock — acquire the lock via a maintenanceLockEntryMethods entry before the call, or exempt with a reason (ADR-0046 decision 3 / R13-L1)", v)
	}
	// 双向保鲜：豁免必须仍在真实缺口上（不再命中即红）。
	for fn, ex := range maintenanceLockExemptions {
		if ex.Reason == "" {
			t.Errorf("maintenance lock exemption %q must carry a reason", fn)
		}
		if !reported[fn] {
			t.Errorf("maintenance lock exemption %q no longer matches a real gap; remove the entry", fn)
		}
	}
}
