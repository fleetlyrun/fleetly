package guards

// engine 侧四件一反扫（transition.go 收口批同批守卫，2026-10-03；镜像
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
	"sort"
	"strings"
	"testing"
)

// transitionWriteMethods 是行写方法名集（聚合 repo 的变更面；读面
// List*/Get*/Stats*/Count*/Find* 不在列）。
var transitionWriteMethods = map[string]bool{
	"Create": true, "Transit": true, "Update": true, "UpdateLease": true,
	"Delete": true, "Put": true, "Upsert": true,
	"Fire": true, "RecordFire": true, "Insert": true, "Remove": true,
}

// transitionRepoFields 是 Engine 的聚合 repo 字段集（行写接收者白名单；
// outbox/audits 是事实存储、runtime 是 Capability 端口——均不在列）。
var transitionRepoFields = map[string]bool{
	"projects": true, "apps": true, "revisions": true, "deployments": true,
	"builds": true, "nodes": true, "tasks": true, "runs": true,
	"tokens": true, "schedules": true, "peerDecls": true, "uploads": true,
	"databases": true, "routes": true, "secrets": true, "configs": true,
	"volumes": true, "networks": true,
	// ADR-0080：事务作用域经 transact.Stores（大写字段）——同一聚合 repo
	// 的 tx 绑定形态与 db 绑定形态同权执法。
	"Projects": true, "Apps": true, "Revisions": true, "Deployments": true,
	"Builds": true, "Nodes": true, "Audits": true, "Tasks": true, "Runs": true,
	"Tokens": true, "Schedules": true, "NetworkPeers": true, "SourceUploads": true,
	"Databases": true, "Routes": true, "Secrets": true, "Configs": true,
	"Volumes": true, "Networks": true, "GitHubApps": true,
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

// engineFuncScan 解析 internal/application/engine 全部非测试 .go，产出：
// 文件相对路径 → 函数名 → 该函数体含 Transit 直调/行写/事实发射位。
//
// P3-0 接收者泛化（2026-10-15）：两遍扫描。第一遍收集包内全部命名类型名
// （Engine、execs、Loop……动态全集，后续批域 struct 改名零维护）；
// 第二遍逐函数解析 engine-self 标识符集后做调用链分析。P3 域化九批会把
// 字段与行为收编进未导出域 struct，方法接收者随之不再是 *Engine、名字
// 不再是 e，repo 调用链也会变多级（x.repos.tasks.Transit）——字面 e +
// 两层选择器匹配会落盲区，故按声明动态解析 self、按链匹配调用。
type engineFuncFacts struct {
	file     string // 仓库相对路径
	transit  bool   // <self>.<repo>.Transit 直调（任意链深）
	rowWrite bool   // <self>.<repo>.<行写方法>
	factEmit bool   // <self>.outbox/audits.Append、emitObservation、writes.Emit/Sequence
}

func scanEngineFuncs(t *testing.T) map[string]engineFuncFacts {
	t.Helper()
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))

	// 第一遍：包内命名类型全集 + 缓存解析结果供第二遍复用（单次 WalkDir）。
	engineTypes := map[string]bool{}
	var parsed []*ast.File
	var rels []string
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
			facts := engineFuncFacts{file: rel}
			self := engineSelfIdents(fd, engineTypes)
			for name := range engineStoresIdents(fd) {
				self[name] = true
			}
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
							facts.rowWrite = true
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
				// P2 刀 1 起事实发射收编进 writes kernel（writes.Emit /
				// writes.Sequence——本体住 transition.go/acceptance.go），包限
				// 定形态 base 是包名、不必在 self 集，守卫不因 API 换形失明。
				if base != nil && base.Name == "writes" &&
					(terminal == "Emit" || terminal == "Sequence") {
					facts.factEmit = true
				}
				return true
			})
			funcs[key] = facts
		}
	}
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
// 壳本体"，扩散形态各归各的守卫）。
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
	used := map[string]bool{}
	var bad []string
	for key, f := range funcs {
		if !f.rowWrite || !f.factEmit {
			continue
		}
		if f.file == "internal/application/engine/transition.go" {
			continue
		}
		name := key[:strings.Index(key, "@")]
		if reason, ok := engineFourInOneExemptions[name]; ok && reason != "" {
			used[name] = true
			continue
		}
		bad = append(bad, key)
	}
	sort.Strings(bad)
	for _, k := range bad {
		t.Errorf("%s pairs a row write with outbox/audit emission — hand-rolled four-in-one choreography; declare a writeFact/transitionFact and go through transition.go's commit primitives", k)
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
}
