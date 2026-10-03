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

// engineFuncScan 解析 internal/engine 全部非测试 .go，产出：
// 文件相对路径 → 函数名 → 该函数体含 Transit 直调/行写/事实发射位。
type engineFuncFacts struct {
	file     string // 仓库相对路径
	transit  bool   // e.<repo>.Transit 直调
	rowWrite bool   // e.<repo>.<行写方法>
	factEmit bool   // outbox.Append / audits.Append
}

func scanEngineFuncs(t *testing.T) map[string]engineFuncFacts {
	t.Helper()
	funcs := map[string]engineFuncFacts{}
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "engine"))
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
		rel := filepath.ToSlash(path[len(repoRoot(t))+1:])
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := fd.Name.Name + "@" + rel
			facts := engineFuncFacts{file: rel}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "Append" {
					if inner, ok := sel.X.(*ast.SelectorExpr); ok {
						if id, ok := inner.X.(*ast.Ident); ok && id.Name == "e" &&
							(inner.Sel.Name == "outbox" || inner.Sel.Name == "audits") {
							facts.factEmit = true
						}
					}
					return true
				}
				inner, ok := sel.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				field, ok := inner.X.(*ast.Ident)
				if !ok || field.Name != "e" || !transitionRepoFields[inner.Sel.Name] {
					return true
				}
				switch {
				case sel.Sel.Name == "Transit":
					facts.transit = true
					facts.rowWrite = true
				case transitionWriteMethods[sel.Sel.Name]:
					facts.rowWrite = true
				}
				return true
			})
			funcs[key] = facts
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 防盲自检：扫描面必须足够大，且 transition.go 里必须真有 Transit
	// 直调（前门本体）——扫描器失明即红。
	if len(funcs) < 150 {
		t.Fatalf("engine scan found only %d functions — guard is blind, fix the scan", len(funcs))
	}
	sawTransitFront := false
	for _, f := range funcs {
		if f.transit && f.file == "internal/engine/transition.go" {
			sawTransitFront = true
		}
	}
	if !sawTransitFront {
		t.Fatalf("scan found no Transit call in transition.go — guard is blind, fix the scan")
	}
	return funcs
}

// TestTransitCASLivesOnlyInTransition：聚合 repo 的 Transit 直调只许在
// transition.go。
func TestTransitCASLivesOnlyInTransition(t *testing.T) {
	funcs := scanEngineFuncs(t)
	var bad []string
	for key, f := range funcs {
		if f.transit && f.file != "internal/engine/transition.go" {
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
		if f.file == "internal/engine/transition.go" {
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
