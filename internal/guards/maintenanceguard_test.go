package guards

// 载体写动词维护锁反扫（R13-L1 / ADR-0046 决策 3）：`maintenanceMu` 的读
// 半边纪律——**engine 包内每个调用 Runtime 载体写动词的函数都必须在调用
// 之前持读锁**（lockMaintenance），与 Ensure 族及网络重建写半边串行化。
// 新增写动词 = 新调用点必须同批补锁；漏锁/锁在调用之后即红；显式豁免必须
// 带理由**且**满足机械条件（唯一调用者，且调用者在该调用前已持读锁——防
// 「豁免表变成盲区」）。豁免不再命中即红（双向保鲜）。
//
// 判据是 AST（注释免疫）：只认引擎字段 `e.runtime` 上的写动词调用；锁与
// 调用的先后按 token 序比较（同一函数体内先锁后写）。递归持读锁被
// sync.RWMutex 明令禁止（写者等待时二次 RLock 会死锁），所以「整步持锁 +
// 内部收敛函数裸调」是唯一合法形态，由 Caller 豁免机制登记。

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

// maintenanceLockExemption 是一条豁免：Reason 必填；Caller 是**唯一**调用
// 者函数名（该调用者必须在调用本函数之前已持 maintenance 读锁）。
type maintenanceLockExemption struct {
	Reason string
	Caller string
}

// maintenanceLockExemptions 是 engine 函数名 → 豁免（当前仅整步持锁形态）。
var maintenanceLockExemptions = map[string]maintenanceLockExemption{
	// managedStep 在函数入口取读锁并 defer 释放（整步覆盖），内部收敛函数
	// 在同一步内直接调 Ensure——按唯一调用者 + 调用者持锁机制豁免，避免
	// 递归 RLock（sync.RWMutex 写者等待时二次读锁会死锁）。
	"reconcileManaged": {
		Reason: "called only from managedStep, which holds the maintenance read lock for the whole step",
		Caller: "managedStep",
	},
}

// maintenanceCarrierCall 是一次载体写调用点（用于豁免的调用者持锁验证）。
type maintenanceCarrierCall struct {
	caller string
	file   string
	pos    token.Pos
}

// maintenanceFuncInfo 是一个包内函数的判据面。
type maintenanceFuncInfo struct {
	name    string
	file    string
	body    *ast.BlockStmt
	lockPos token.Pos // 本函数体内最早的 lockMaintenance 调用（NoPos = 未持锁）
}

// maintenancePackageScan 是一次 engine 包扫描的产物。
type maintenancePackageScan struct {
	files int
	calls int // 载体写调用点总数（防盲锚）
	funcs map[string][]maintenanceFuncInfo
	// carrierCalls 记录载体写调用点（caller + 位置）：豁免验证用。
	carrierCalls []maintenanceCarrierCall
	// allCalls 记录全部「函数名调用」点（callee → 调用点）：唯一调用者
	// 验证用（只用于豁免函数的调用者计数）。
	allCalls map[string][]maintenanceCarrierCall
}

// maintenanceCarrierWriteCall 报告 call 是否是引擎字段上的载体写动词调用。
func maintenanceCarrierWriteCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !maintenanceCarrierWriteVerbs[sel.Sel.Name] {
		return false
	}
	recv, ok := sel.X.(*ast.SelectorExpr)
	return ok && recv.Sel.Name == "runtime"
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

// scanMaintenanceFile 解析单个文件并把函数/调用点并入扫描。
func scanMaintenanceFile(fset *token.FileSet, path string, scan *maintenancePackageScan) error {
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return err
	}
	scan.files++
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		info := maintenanceFuncInfo{name: fd.Name.Name, file: filepath.Base(path), body: fd.Body}
		scanFuncBody(fd.Name.Name, filepath.Base(path), fd.Body, scan, &info)
		scan.funcs[info.name] = append(scan.funcs[info.name], info)
	}
	return nil
}

// violations 返回「载体写调用早于本函数 lockMaintenance（或未持锁）」的
// 函数名（file:fn 形态，排序）。豁免不在此处消费。
func (s *maintenancePackageScan) violations() []string {
	var out []string
	for _, calls := range s.funcs {
		for _, fn := range calls {
			// 逐个调用点无法直接反查位置→函数体；按函数体重新走一遍。
			early := false
			ast.Inspect(fn.body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !maintenanceCarrierWriteCall(call) {
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
		holder, ok := s.singleFunc(ex.Caller)
		if !ok {
			return "exemption caller " + ex.Caller + " for " + fn + " not found in the engine package"
		}
		if holder.lockPos == token.NoPos || c.pos < holder.lockPos {
			return "exemption caller " + ex.Caller + " does not hold the maintenance read lock before calling " + fn
		}
	}
	return ""
}

// singleFunc 返回唯一同名函数（重名 = 判据不成立，返回 false）。
func (s *maintenancePackageScan) singleFunc(name string) (maintenanceFuncInfo, bool) {
	infos := s.funcs[name]
	if len(infos) != 1 {
		return maintenanceFuncInfo{}, false
	}
	return infos[0], true
}

// TestMaintenanceLockScannerJudgment 是判据自检（夹具源码，不经真实包）：
// 先锁后写 = 通过；无锁 = 红；锁在写之后 = 红（同函数内顺序也算纪律）。
func TestMaintenanceLockScannerJudgment(t *testing.T) {
	src := `package engine

type Engine struct{ runtime int }

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
	_ = e.other.Remove(ctx, ns) // 非 Runtime 接收者：不判
}
`
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	scan := &maintenancePackageScan{funcs: map[string][]maintenanceFuncInfo{}, allCalls: map[string][]maintenanceCarrierCall{}}
	scan.files = 1
	scanOneFile(t, f, scan)
	got := scan.violations()
	want := []string{"fixture.go:lateLock", "fixture.go:unlocked"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("violations = %v, want %v", got, want)
	}
	if scan.calls != 3 {
		t.Fatalf("fixture carrier-write call sites = %d, want 3 (needle regression)", scan.calls)
	}
}

// scanOneFile 把已解析的夹具文件并入扫描（与 scanMaintenanceFile 同判据，
// 供判据自检复用）。
func scanOneFile(t *testing.T, f *ast.File, scan *maintenancePackageScan) {
	t.Helper()
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		info := maintenanceFuncInfo{name: fd.Name.Name, file: "fixture.go", body: fd.Body}
		scanFuncBody(fd.Name.Name, "fixture.go", fd.Body, scan, &info)
		scan.funcs[info.name] = append(scan.funcs[info.name], info)
	}
}

// scanFuncBody 走一个函数体：记录锁位置与调用点（scanMaintenanceFile 与
// 判据自检共享同一判据实现）。
func scanFuncBody(name, file string, body *ast.BlockStmt, scan *maintenancePackageScan, info *maintenanceFuncInfo) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if calledFuncName(call) == "lockMaintenance" {
			if info.lockPos == token.NoPos || call.Pos() < info.lockPos {
				info.lockPos = call.Pos()
			}
		}
		if called := calledFuncName(call); called != "" {
			scan.allCalls[called] = append(scan.allCalls[called], maintenanceCarrierCall{caller: name, file: file, pos: call.Pos()})
		}
		if maintenanceCarrierWriteCall(call) {
			scan.calls++
			scan.carrierCalls = append(scan.carrierCalls, maintenanceCarrierCall{caller: name, file: file, pos: call.Pos()})
		}
		return true
	})
}

// TestEngineCarrierWriteVerbsHoldMaintenanceLock 反扫真实 engine 包。
func TestEngineCarrierWriteVerbsHoldMaintenanceLock(t *testing.T) {
	root := filepath.Join(repoRoot(t), "internal", "application", "engine")
	scan := &maintenancePackageScan{funcs: map[string][]maintenanceFuncInfo{}, allCalls: map[string][]maintenanceCarrierCall{}}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return scanMaintenanceFile(fset, path, scan)
	})
	if err != nil {
		t.Fatal(err)
	}
	// 防盲锚：扫描面文件数与写调用点下限（目录/针漂移导致的零扫描即红）。
	if scan.files < 40 {
		t.Fatalf("scanned only %d engine files — guard is blind, fix the scan", scan.files)
	}
	if scan.calls < 11 {
		t.Fatalf("found only %d carrier write call sites — the needle is stale (want >= 11)", scan.calls)
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
		t.Errorf("%s calls a Runtime carrier write verb without holding the maintenance read lock — acquire e.lockMaintenance() before the call, or exempt with a reason (ADR-0046 decision 3 / R13-L1)", v)
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
