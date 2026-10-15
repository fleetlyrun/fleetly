package guards

// engine 包域 struct 禁回引 *Engine（P3-0 守卫前置批，2026-10-15）。
//
// P3（engine 域化九批）要把 Engine 的字段与行为收编进未导出域 struct
//（type execs 等），域化的后门形态是给域 struct 挂 `e *Engine` 回引——
// 壳不缩、依赖不显式、初始化环风险，域化等于没发生。域 struct 只准显式
// 持最小依赖（repo/端口/logger/clock）。两条不变式（只执法 engine 包
// 生产 .go，_test.go 不在面）：
//
//	R1 任何 struct 字段（含嵌入字段 Names==nil、含匿名 inline struct）
//	   类型为 Engine/*Engine → 红；
//	R2 非 *Engine 接收者的方法签名带 Engine/*Engine 参数 → 红
//	   （构造器/自由函数持 *Engine 形参不在列——组合根装配本就要递壳）。
//
// 豁免表带理由、双向保鲜：后续批去除回引时豁免条目不再命中即红，强制
// 同批删条目。本守卫与 transitionguard 的接收者泛化（同批）构成前置
// 封堵：P3 各批的收编改动不落守卫盲区。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// engineBackrefExemptions 是 struct 名 → 理由（现树遗留的 *Engine 回引，
// 各自的收编批落地时必须去除并同批删条目）。R1 按字段归属 struct 名对账，
// R2 按接收者类型名对账；匿名 inline struct 的回引无名字可豁免，一律红。
var engineBackrefExemptions = map[string]string{
	// 流级批汇器（logging.go）持 Engine 用 clock/logging/log，批 2 observ
	// 收编时去除。
	"nsBatch": "stream batcher borrows Engine for clock/logging provider/logger; remove when batch 2 absorbs the observ domain",
	// build 日志攒批器（buildlog.go）经 e.logging/e.log 落库，批 6 build
	// 收编时去除。
	"buildLogIngester": "build-log batcher borrows Engine for logging provider/logger; remove when batch 6 absorbs the build domain",
	// LogWriter → 环形缓冲适配器（builder.go）经 engine.build.logs 写环，
	// 批 6 build 收编时去除。
	"bufferWriter": "LogWriter-to-ring-buffer adapter writes via engine.build.logs; remove when batch 6 absorbs the build domain",
}

// engineBackrefHit 是一处 Engine/*Engine 回引命中。
type engineBackrefHit struct {
	owner string // 归属命名 struct（R1 字段归属 / R2 接收者类型；无归属记 <anonymous>）
	rel   string // 仓库相对路径
	line  int
	kind  string // "field"（R1）/ "method"（R2）
}

// engineBackrefScan 是一次回引扫描的产出：命中位 + 防盲自检的计数面。
type engineBackrefScan struct {
	hits         []engineBackrefHit
	namedStructs int  // 包内具名 struct 类型数（防盲）
	sawEngine    bool // 是否见到 Engine 类型声明（防盲）
}

// scanEngineBackrefs 扫描 internal/application/engine 全部非测试 .go 的
// R1/R2 回引命中。R1 用 go/ast.Inspect 找全部 *ast.StructType 节点（覆盖
// 具名/嵌入/匿名 inline 三形态），命中按最内层包住它的具名 struct 归属。
func scanEngineBackrefs(t *testing.T) engineBackrefScan {
	t.Helper()
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))
	scan := engineBackrefScan{}
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

		// 具名 struct 的位置区间（供匿名 inline struct 归属到外层命名
		// 类型；嵌套时取最内层——区间最小者）。
		type structSpan struct {
			name     string
			pos, end token.Pos
		}
		var spans []structSpan
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
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				scan.namedStructs++
				if ts.Name.Name == "Engine" {
					scan.sawEngine = true
				}
				spans = append(spans, structSpan{ts.Name.Name, st.Pos(), st.End()})
			}
		}

		// R1：全部 StructType 节点逐字段查（嵌入字段 Names 为 nil 同样
		// 在 f.Fields.List 里，类型判定只看 f.Type）。
		ast.Inspect(file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			owner := "<anonymous>"
			best := token.Pos(0)
			for _, s := range spans {
				if s.pos <= st.Pos() && st.End() <= s.end {
					if owner == "<anonymous>" || s.end-s.pos < best {
						owner, best = s.name, s.end-s.pos
					}
				}
			}
			for _, f := range st.Fields.List {
				if isEngineType(f.Type) {
					scan.hits = append(scan.hits, engineBackrefHit{
						owner: owner, rel: rel, line: fset.Position(f.Pos()).Line, kind: "field",
					})
				}
			}
			return true
		})

		// R2：非 Engine 接收者的方法签名带 Engine/*Engine 参数。
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			base := baseIdent(fd.Recv.List[0].Type)
			if base == nil || base.Name == "Engine" {
				continue
			}
			if fd.Type == nil || fd.Type.Params == nil {
				continue
			}
			for _, param := range fd.Type.Params.List {
				if !isEngineType(param.Type) {
					continue
				}
				scan.hits = append(scan.hits, engineBackrefHit{
					owner: base.Name, rel: rel, line: fset.Position(fd.Pos()).Line, kind: "method",
				})
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return scan
}

// TestNoEngineBackReferenceInEnginePackageStructs：engine 包生产面禁止
// struct 字段（R1）与非 Engine 接收者方法参数（R2）回引 Engine/*Engine
// ——P3 域化的后门封堵。
func TestNoEngineBackReferenceInEnginePackageStructs(t *testing.T) {
	scan := scanEngineBackrefs(t)
	used := map[string]bool{}
	var bad []string
	for _, h := range scan.hits {
		if reason, ok := engineBackrefExemptions[h.owner]; ok && reason != "" {
			used[h.owner] = true
			continue
		}
		if h.kind == "field" {
			bad = append(bad, fmt.Sprintf(
				"%s:%d struct %s holds a field of type Engine/*Engine — a domain struct must hold only its minimal explicit dependencies (repos/ports/logger/clock), never a back-reference to the Engine shell; absorb the concrete dependency instead (P3 domainization guard)",
				h.rel, h.line, h.owner))
			continue
		}
		bad = append(bad, fmt.Sprintf(
			"%s:%d method on %s takes an Engine/*Engine parameter — non-Engine receivers must not smuggle the Engine shell through their signature (P3 domainization guard)",
			h.rel, h.line, h.owner))
	}
	sort.Strings(bad)
	for _, v := range bad {
		t.Errorf("%s", v)
	}
	// 双向保鲜：豁免不再命中即红（后续批去除回引必须同批删条目）。
	for name, reason := range engineBackrefExemptions {
		if reason == "" {
			t.Errorf("engine back-reference exemption %s must carry a reason", name)
		}
		if !used[name] {
			t.Errorf("engine back-reference exemption %s no longer matches a real Engine/*Engine occurrence; remove the entry", name)
		}
	}
	// 防盲自检：具名 struct 类型数实测 87（2026-10-15），阈值 60 给域化
	// 重构留余量——红时先重测再调值；Engine 声明必须可见。
	if scan.namedStructs < 60 {
		t.Fatalf("engine package scan found only %d named struct types — guard is blind, fix the scan", scan.namedStructs)
	}
	if !scan.sawEngine {
		t.Fatalf("scan found no Engine type declaration in the engine package — guard is blind, fix the scan")
	}
}
