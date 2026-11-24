package guards

// engine 包域 struct 禁回引 *Engine（P3-0 契约测试前置批，2026-10-15）。
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
// 同批删条目。本契约测试与 transitionguard 的接收者泛化（同批）构成前置
// 封堵：P3 各批的收编改动不落契约测试盲区。

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
// 当前为零——三条遗留回引（nsBatch/buildLogIngester/bufferWriter）已于
// P3 批 6 显式化并同批删条目；后续新域 struct 一律显式最小依赖，回引即红。
var engineBackrefExemptions = map[string]string{}

// engineSatellite 是一个应用模块（仓库相对包路径）。
type engineSatellite struct {
	path string // 仓库相对包路径
}

// engineContractLeaf 是内核契约叶子的 import 路径——应用模块的判据。
const engineContractLeaf = "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// applicationModuleFloor 是应用模块数的防盲下限（2026-11-24 实测 11：backups/
// browses/capacity/dataservices/diagnosis/execs/functions/intake/logpipes/
// manageds/metrics）。派生结果少于它 = 扫描退化，不是模块变少。
const applicationModuleFloor = 11

// applicationModules 由 import 图派生应用模块集（取代手工登记表，2026-11-24
// 二期设计批 0）：internal/application/<pkg>/ 下任一生产包 import 了
// engine/contract，该包就是应用模块。
//
// 为什么不再登记：登记制的失败形态是「漏登记不报错，只是静默不扫」。functions
// 与 capacity 两批立域都漏了登记表，七件模块纪律守卫对它们自上线起失明
// （二期设计稿 §1.4 实验 1–5：模块间硬边 import、回引内核根、私建 ticker、
// 不持维护锁调 Runtime.Ensure——对已登记模块全红，对这两模块全绿）。依赖图
// 是事实，登记表是抄本；按事实派生后，新模块一旦依赖契约叶子就自动进入全部
// 模块纪律守卫的扫描面。
//
// 模块谱系（文档，不参与执法）：logpipes=采集环（ADR-0040）；execs=exec 会话
// 域（ADR-0049）；metrics=指标采集+告警（ADR-0041）；backups=Backup 域
// （ADR-0039）；browses=Browse 会话域（ADR-0051）；manageds=通用受管 reconciler
// （ADR-0004）；dataservices=托管数据服务（ADR-0096）；intake=构建摄取；
// diagnosis=诊断 facts 装配器（规则层在 domain）；functions=Function 域
// （ADR-0102）；capacity=通用伸缩决策环（ADR-0103）。
func applicationModules(t *testing.T) []engineSatellite {
	t.Helper()
	// 不缓存：包列举失败时由本调用的 t 直接 Fatal——进程级缓存会把一次失败的
	// 半成品（空集合）留给后续每件守卫，等于派生为空、全体失明而无人报错。
	const appPrefix = "internal/application/"
	seen := map[string]bool{}
	for _, p := range listModulePackages(t, "") {
		name := strings.TrimPrefix(p.rel, appPrefix)
		if name == p.rel || name == "" {
			continue
		}
		if k := strings.IndexByte(name, '/'); k >= 0 {
			name = name[:k]
		}
		if name == "engine" {
			continue // 内核与其叶子（contract / api）不是应用模块
		}
		for _, imp := range p.imports {
			if imp == engineContractLeaf {
				seen[name] = true
				break
			}
		}
	}
	out := make([]engineSatellite, 0, len(seen))
	for name := range seen {
		out = append(out, engineSatellite{path: appPrefix + name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	if len(out) < applicationModuleFloor {
		t.Fatalf("import-graph derivation found only %d application modules (floor %d: %v) — the scan stopped seeing the contract leaf; fix the scan, do not lower the floor", len(out), applicationModuleFloor, out)
	}
	return out
}

// satellitePackageRoots 返回应用模块包的绝对路径集（五件 root=engine 契约
// 测试扫描面扩容的共用面：loopguard / maintenanceguard / transitionguard /
// ensuredledger / metastatewrite，另 bluegreen 的 Remove 白名单同源；repoRoot
// 注入）。
func satellitePackageRoots(t *testing.T) []string {
	t.Helper()
	mods := applicationModules(t)
	roots := make([]string, 0, len(mods))
	for _, sat := range mods {
		roots = append(roots, filepath.Join(repoRoot(t), filepath.FromSlash(sat.path)))
	}
	return roots
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
	// 防盲自检：具名 struct 类型数实测 87（2026-10-15）→ 85（P5 wave-1
	// logpipes 拆包迁出 logpipes/nsBatch 两 struct）→ 64（P5 wave-2
	// execs/metrics 拆包迁出 21 struct——exec 会话/帧族 10 + metrics 域
	// 11）→ 74（P5 wave-4 重测勘误：wave-3 注记的 53 与 AST 口径实测不符，
	// 以 74 为准——注记口径勘误，非结构性回升）→ 69（P5 wave-4 databases/
	// manageds 拆包迁出 5 struct〔databases/manageds/managedGen→ManagedGen/
	// managedProviderDecl/limitedBuffer〕）→ 55（P5 wave-5 末模块 intake 拆包
	// 迁出 14 struct〔intake/builds/buildLimits/logBuffer/seqFrame/
	// bufferWriter/buildLogWriter/buildLogIngester/redactTable/redactEntry/
	// gitCredentialJSON/gitMaterial/registryCredentialJSON 等 + buildEventPayload
	// 载荷下沉 engine/contract〕）→ 55（P5 wave-6 实测持平：diagnosis 拆包
	// 迁出的 diagnose.go 无 struct 声明），阈值 45 给后续演进留余量——
	// 每波先重测再调值；Engine 声明必须可见。
	if scan.namedStructs < 45 {
		t.Fatalf("engine package scan found only %d named struct types — guard is blind, fix the scan", scan.namedStructs)
	}
	if !scan.sawEngine {
		t.Fatalf("scan found no Engine type declaration in the engine package — guard is blind, fix the scan")
	}
}

// TestSplitSatellitePackagesDoNotImportEngineCore 是 R1/R2 的跨包腿（ADR-
// 0088 决策 8 / P5 首项「应用模块包回引 *engine.Engine 的跨包执法」）：应用
// 模块包禁 import engine 根包——import 禁令是 Engine/*Engine 类型引用的机械
// 全称（类型不可名即字段/形参/断言各引用位全数不可达，R1/R2 跨包同义）。
// 核持应用模块（engine → 应用模块，构造注入）与应用模块禁返核构成单向边；
// 应用模块的核依赖面一律走 engine/contract 契约叶子与注入端口。测试文件不在
// 面（测试天然跨层；应用模块白盒测 engine 内部件在同包 import 下本就编译环）。
// 模块集由 import 图派生（applicationModules）——两套扫描（派生用的 go list
// 与本腿的包列举）对不上即红。
func TestSplitSatellitePackagesDoNotImportEngineCore(t *testing.T) {
	mods := applicationModules(t)
	seen := map[string]bool{}
	for _, p := range listModulePackages(t, "") {
		for _, sat := range mods {
			if !depPrefixMatch(p.rel, sat.path) {
				continue
			}
			seen[sat.path] = true
			for _, imp := range p.imports {
				if imp == "github.com/fleetlyrun/fleetly/internal/application/engine" {
					t.Errorf("%s imports the engine core package — a split satellite reaches the core only via injected ports and the engine/contract leaf (ADR-0088 decision 2 P5 form; the core holds the satellite, never the reverse)", p.rel)
				}
			}
		}
	}
	for _, sat := range mods {
		if !seen[sat.path] {
			t.Errorf("application module %s was derived from the import graph but has no production package in the package listing — the two scans disagree; fix the scan", sat.path)
		}
	}
}
