package guards

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// P1a-3b 契约测试换装：子树依赖矩阵（架构收敛终态五行规则的现状登记版）。
// 数据表 = 子树 → 允许的 internal import 前缀集；红 = 生产包的 internal
// import 超出允许集。双向保鲜：子树消失（改名/搬迁）即红、允许前缀不再被
// 任何包命中即红（登记必须与现况互证，防僵尸行）。防盲自检：矩阵未覆盖
// 的 internal 生产包即红（新顶层子树必须显式登记），零包由
// listModulePackages 的防盲 fatal 承担。
//
// 终态五行（架构收敛计划 P1）：
//
//	domain/** → 零 external（纯领域，repo 经接口反转）
//	application/** → domain（P1b B/C 批已收：DB 契约接口化 + 构造上提组合根）
//	infrastructure/** → domain
//	interfaces/** → application + domain（读写分离；admin 离线面例外行）
//	assembly/config/testsupport/guards → 全部（组合根/元层）
//
// 本表是现状登记：与终态的差距全部以豁免行落位并带理由，收紧完成后删行
// ——删行后本契约测试不改自绿，豁免残留不再命中即红（双向保鲜）。
type depRow struct {
	subtree  string   // internal/ 下的子树前缀（斜杠分隔，含尾斜杠）
	allow    []string // 允许的 internal import 前缀（internal/ 相对形态）
	allowAll bool     // 组合根/元层：允许任意 internal import（前缀白名单不适用）
	note     string   // 登记理由（终态锚点/收紧计划）
}

var depMatrix = []depRow{
	{
		subtree: "internal/domain/",
		allow:   nil,
		note:    "终态零外依赖（纯领域）；根包契约面已上移 domain/shared/state（P1b 根包拆分），残留豁免见 depExemptions（anchor 直引具体 repo，后续批次清零——writes 豁免已随批二 outbox/audit 上移消解删行）",
	},
	{
		subtree: "internal/application/",
		allow: []string{
			"internal/domain/",
		},
		note: "application → domain only（P1b B/C 批收口：persistence 经 state.DB 契约消费、repo 构造上提 assembly）；github/upload 客户端注入走豁免行（P3 锚）",
	},
	{
		subtree: "internal/infrastructure/",
		allow:   []string{"internal/domain/"},
		note:    "infrastructure → domain；providers 圈禁由 importguard 进一步收紧（providers 只准 capability/spec/model），本行不重复执法",
	},
	{
		subtree: "internal/interfaces/",
		allow: []string{
			"internal/application/",
			"internal/domain/",
		},
		note: "读写分离律：读径 repo 直读经 domain repo 接口（P1b B/C 批后零 persistence 引用）；admin 离线维护面例外行（ADR-0075 例外②）；写面纪律（写路径必经 application/engine）由 acceptanceguard 承载，本行只执法依赖方向",
	},
	{
		subtree:  "internal/assembly/",
		allowAll: true,
		note:     "组合根：装配一切（wire 依赖图天然全向）",
	},
	{
		subtree:  "internal/config/",
		allowAll: true,
		note:     "配置装载（组合根同款元层）",
	},
	{
		subtree:  "internal/testsupport/",
		allowAll: true,
		note:     "测试支撑（跨层 harness 组装，元层）",
	},
	// internal/guards/ 属元层（五行第 5 行），但 go list 投影已跳过契约测试
	// 自身（纯测试包），故不登记为行——登记即触发行保鲜红。
}

// depExemptions 是矩阵豁免行：在某子树内额外放行的 internal 前缀，必须
// 带理由；行不再被任何实际 import 命中即红（收紧完成后删行）。
type depExemption struct {
	subtree string // 豁免生效的子树前缀（须窄于所在矩阵行）
	allow   string // 额外放行的 internal import 前缀
	reason  string // 豁免理由（含收紧计划锚点）
}

var depExemptions = []depExemption{
	{
		subtree: "internal/application/intake",
		allow:   "internal/infrastructure/github/",
		reason:  "intake 卫星直注 github 客户端（客户端注入面；P3 客户端接口化后收进 domain，届时删本行。P5 wave-5 豁免行平移：subtree 自 engine 改写随末星拆包，条数不增——ADR-0088 决策 8 平移条款）",
	},
	{
		subtree: "internal/application/intake",
		allow:   "internal/infrastructure/upload/",
		reason:  "intake 卫星消费 upload blob 路径/解包纯函数（客户端注入面；P3 客户端接口化后收进 domain，届时删本行。P5 wave-5 豁免行平移同上）",
	},
	{
		subtree: "internal/interfaces/grpc/fleetlygrpc",
		allow:   "internal/infrastructure/upload/",
		reason:  "gRPC 面直注 upload blob 存取（客户端注入面；P3 客户端接口化后收进 domain，届时删本行）",
	},
	{
		subtree: "internal/interfaces/admin",
		allow:   "internal/infrastructure/persistence/",
		reason:  "admin 离线维护面直开库（ADR-0075 例外②：不经运行时装配，persistence.Open 直连 + 自管生命周期；常驻矩阵不放宽）",
	},
	// 下面一行是 application 层内互引矩阵（TestApplicationIntraRingMatrix）
	// 的豁免行：层内矩阵在应用模块包内只放行 {domain, engine/contract, transact} ∪
	// 本表——应用模块消费 application 同侪包须逐行登记带理由（P5 wave-6 立法，
	// ADR-0088 决策 7 P5 项「层内互引矩阵」，豁免行同样受主测试双向保鲜）。
	{
		subtree: "internal/application/backups",
		allow:   "internal/application/authn",
		reason:  "backups 卫星读平台备份快照的审计 actor（platformbackup.go：authn.ActorFromContext/SourceFromContext——快照行是审计事实，actor 随请求上下文取；authn 是无状态上下文读取面，非域耦合。消费面消失即删本行）",
	},
}

// globalMetaPrefixes 是全子树可消费的元层前缀（五行第 5 行的反向读法：
// 元层不执法消费方）。每个前缀必须仍被命中，否则登记过期即红。
var globalMetaPrefixes = []string{}

// depMatrixPackages 收集 internal/ 下全部生产包（go list 依赖图：生产
// import 集，测试文件不在依赖方向执法面——测试天然跨层；契约测试自身
// 是纯测试包，GoFiles 为空自然排除）。
func depMatrixPackages(t *testing.T) []listedPackage {
	t.Helper()
	var out []listedPackage
	for _, p := range listModulePackages(t, "") {
		if !strings.HasPrefix(p.rel, "internal/") {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		t.Fatal("dep matrix found no production packages under internal/ — guard is blind, fix the scan")
	}
	return out
}

// TestSubtreeDependencyMatrix：生产包 internal import 必须落在所在行
// 允许集 ∪ 豁免行 ∪ 元层前缀内；矩阵与现况双向保鲜。
func TestSubtreeDependencyMatrix(t *testing.T) {
	packages := depMatrixPackages(t)

	// 行命中计数（子树保鲜 + 允许前缀保鲜）与豁免/元层命中计数。
	rowFiles := make(map[int]int, len(depMatrix))
	allowHits := make(map[string]int) // "rowIdx\x00prefix" -> 命中数
	exemptionHits := make(map[int]int, len(depExemptions))
	metaHits := make(map[string]int, len(globalMetaPrefixes))

	for _, f := range packages {
		rowIdx := -1
		for i, row := range depMatrix {
			if depPrefixMatch(f.rel, row.subtree) {
				if rowIdx >= 0 {
					t.Fatalf("%s matches both %s and %s — matrix rows must be disjoint", f.rel, depMatrix[rowIdx].subtree, row.subtree)
				}
				rowIdx = i
			}
		}
		if rowIdx < 0 {
			t.Errorf("%s is under no matrix row — every internal/ production subtree must be registered in depMatrix (架构 §2 分层树)", f.rel)
			continue
		}
		rowFiles[rowIdx]++

		for _, imp := range f.imports {
			if !fleetlyInternal(imp) {
				continue
			}
			rel := strings.TrimPrefix(imp, "github.com/fleetlyrun/fleetly/")
			if allowedByDepMatrix(rowIdx, f.rel, rel) {
				continue
			}
			t.Errorf("%s imports %q — outside the allowed set for %s (dep matrix, 架构 §2)", f.rel, rel, depMatrix[rowIdx].subtree)
		}
		// 记账（仅计数，判定在下方统一做）。
		row := depMatrix[rowIdx]
		for _, imp := range f.imports {
			if !fleetlyInternal(imp) {
				continue
			}
			rel := strings.TrimPrefix(imp, "github.com/fleetlyrun/fleetly/")
			if !row.allowAll {
				for _, p := range row.allow {
					if depPrefixMatch(rel, p) {
						allowHits[matrixKey(rowIdx, p)]++
					}
				}
			}
			for i, ex := range depExemptions {
				if depPrefixMatch(f.rel, ex.subtree) && depPrefixMatch(rel, ex.allow) {
					exemptionHits[i]++
				}
			}
			for _, mp := range globalMetaPrefixes {
				if depPrefixMatch(rel, mp) {
					metaHits[mp]++
				}
			}
		}
	}

	// 子树保鲜：行零文件 = 子树消失/改名（登记过期）。
	for i, row := range depMatrix {
		if rowFiles[i] == 0 {
			t.Errorf("matrix row %s matches no production file — subtree renamed or removed, update depMatrix", row.subtree)
		}
	}
	// 允许前缀保鲜：前缀零命中 = 放行不再被使用（收紧已落地或登记虚胖）。
	for i, row := range depMatrix {
		if row.allowAll {
			continue
		}
		for _, p := range row.allow {
			if allowHits[matrixKey(i, p)] == 0 {
				t.Errorf("matrix row %s allows %s but nothing imports it — stale allowance, tighten the row", row.subtree, p)
			}
		}
	}
	// 豁免保鲜：豁免行零命中 = 理由已消化（P1b 收紧后删行）。
	for i, ex := range depExemptions {
		if exemptionHits[i] == 0 {
			t.Errorf("exemption %s -> %s matches no import — reason consumed, delete the row (双向保鲜)", ex.subtree, ex.allow)
		}
	}
	// 元层前缀保鲜。
	for _, mp := range globalMetaPrefixes {
		if metaHits[mp] == 0 {
			t.Errorf("global meta prefix %s matches no import — stale registration, remove it", mp)
		}
	}
}

// depPrefixMatch 报告 internal import rel 是否落在前缀 prefix 下（前缀可
// 带可不带尾斜杠；rel == prefix 或 rel 是 prefix 子包都算命中——Go import
// 的是包路径，根包与子包同权）。
func depPrefixMatch(rel, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

// allowedByDepMatrix 判定 rel（internal 相对形态）对 fileRow 行 + file 子树
// 是否放行：本子树内聚 ∪ 行允许集（或 allowAll）∪ 命中子树的豁免行 ∪ 全局
// 元层前缀。子树内部互引（如 persistence 各 repo → persistence 根、
// domain/shared/apperr → domain/shared/model/errcode）不在执法面——矩阵
// 只执法跨子树方向，层内叶子纯度由 importguard TestLeafPackagesPurity 承担。
func allowedByDepMatrix(fileRow int, fileRel, rel string) bool {
	row := depMatrix[fileRow]
	if depPrefixMatch(rel, row.subtree) {
		return true
	}
	if row.allowAll {
		return true
	}
	for _, p := range row.allow {
		if depPrefixMatch(rel, p) {
			return true
		}
	}
	for _, ex := range depExemptions {
		if strings.HasPrefix(fileRel, ex.subtree) && depPrefixMatch(rel, ex.allow) {
			return true
		}
	}
	for _, mp := range globalMetaPrefixes {
		if depPrefixMatch(rel, mp) {
			return true
		}
	}
	return false
}

func matrixKey(rowIdx int, prefix string) string {
	return string(rune('A'+rowIdx)) + "\x00" + prefix
}

// ---- application 层内互引矩阵（P5 wave-6 立法，ADR-0088 决策 7 P5 项） ----
//
// 背景：dep_matrix 主表只执法跨子树方向，application 行的「子树内部互引」
// 放行条款在 P5 拆包后构成敞口——九个应用模块出宫后应用模块两两 import、应用模块 import
// engine 根都落在「层内互引」里无人执法（编译器不拦合法 import；intersatellite
// 的 AST 腿已随九个应用模块全出退守 shapes）。本矩阵封堵该敞口。
//
// 判据（只执法生产文件；测试天然跨层）：
//
//	应用模块包（engineSatellitePackages 登记表为单一真源，enginebackref_test.go
//	  在册）的 internal import 只准 {domain/**, engine/contract, transact} ∪ 本
//	  文件 depExemptions 命中行 ∪ 自身子树；其中——
//	  · 应用模块↔应用模块 import = 红（模块间零硬边的 import 级承载，ADR-0088 决策 7）；
//	  · 应用模块→engine 根 = 不在本测试执法（分工：enginebackref 跨包腿
//	    TestSplitSatellitePackagesDoNotImportEngineCore 执法 import 禁令——
//	    类型不可名即 R1/R2 各引用位全数不可达，两契约测试不重复报）；
//	  · 应用模块→application 同侪包（authn/governance/idem/…）= 除非 depExemptions
//	    登记，否则红（同侪消费是例外不是默认）。
//	engine/contract 契约叶子的 internal import 只准 {domain/**, transact}——
//	  · engine/contract →任何应用模块 = 红（契约叶子被应用模块类型污染即内核/应用模块双向耦合）；
//	  · engine/contract →engine 根 = 不在本测试执法（编译器执法：engine import
//	    engine/contract，反向即 import cycle，编译红先到）。
//	engine 核→应用模块 = 合法（构造注入持有，ADR-0088 P5 首项注记裁定三），
//	  主表「子树内部互引」条款继续承载，本测试不重复判。
//
// 防盲：登记表条目目录消失/无生产文件即红（enginebackref 双向保鲜同源）；
// 本矩阵扫描面零命中（无任何应用模块/engine/contract 文件被扫到）即红。

// appRingSatellitePrefixes 返回应用模块包前缀集（engineSatellitePackages 单一
// 真源——登记表增删即本矩阵执法面增删，无第二份名单）。
func appRingSatellitePrefixes() map[string]bool {
	out := make(map[string]bool, len(engineSatellitePackages))
	for _, sat := range engineSatellitePackages {
		out[sat.path] = true
	}
	return out
}

// contractLeaf 是 application 环契约叶子前缀（ADR-0088 P5 首项注记
// 裁定二：应用模块与核都 import 它、无环；按需最小面下沉）。
const contractLeaf = "internal/application/engine/contract"

// intraRingViolations 是层内互引矩阵的纯判据（files 注入：真实树经
// depMatrixPackages，红灯实验经合成条目；satPrefixes 注入应用模块前缀集）。返回
// 命中清单（空 = 绿）。
func intraRingViolations(packages []listedPackage, satPrefixes map[string]bool) []string {
	var bad []string
	for _, f := range packages {
		ownSat := ""
		for p := range satPrefixes {
			if depPrefixMatch(f.rel, p) {
				ownSat = p
				break
			}
		}
		isPortsLeaf := depPrefixMatch(f.rel, contractLeaf)
		if ownSat == "" && !isPortsLeaf {
			continue // engine 核与 application 同侪包不在本矩阵执法面
		}
		for _, imp := range f.imports {
			if !fleetlyInternal(imp) {
				continue
			}
			rel := strings.TrimPrefix(imp, "github.com/fleetlyrun/fleetly/")
			// 自身子树（应用模块包子包互引合法，现役应用模块皆平铺单层）。
			if ownSat != "" && depPrefixMatch(rel, ownSat) {
				continue
			}
			if isPortsLeaf {
				// 契约叶子：只准 domain + transact；染应用模块即红。
				if depPrefixMatch(rel, "internal/domain/") || rel == "internal/application/transact" {
					continue
				}
				if satPrefixes[rel] {
					bad = append(bad, fmt.Sprintf(
						"%s imports satellite package %s — the engine/contract leaf must stay free of satellite types (core/star coupling through the leaf; ADR-0088 P5 note ruling 2)",
						f.rel, rel))
					continue
				}
				bad = append(bad, fmt.Sprintf(
					"%s imports %s — the engine/contract leaf may only consume domain and transact (application intra-ring matrix, ADR-0088 decision 7 P5)",
					f.rel, rel))
				continue
			}
			// 应用模块包：domain/engine/contract/transact 放行。
			if depPrefixMatch(rel, "internal/domain/") || rel == "internal/application/transact" || rel == contractLeaf {
				continue
			}
			// 应用模块↔应用模块 = 红（模块间零硬边的 import 级承载）。
			if satPrefixes[rel] {
				bad = append(bad, fmt.Sprintf(
					"%s imports satellite package %s — satellites must not import each other; reach the core or peers only via injected mechanism ports (inter-satellite zero hard edges, import level; ADR-0088 decision 7 P5)",
					f.rel, rel))
				continue
			}
			// 应用模块→engine 根：enginebackref 跨包腿管辖（见头注分工），跳过。
			if rel == "internal/application/engine" {
				continue
			}
			// 其余 internal import：豁免行命中放行，否则红。
			exempt := false
			for _, ex := range depExemptions {
				if strings.HasPrefix(f.rel, ex.subtree) && depPrefixMatch(rel, ex.allow) {
					exempt = true
					break
				}
			}
			if exempt {
				continue
			}
			bad = append(bad, fmt.Sprintf(
				"%s imports %s — a satellite package may only consume domain/engine/contract/transact plus registered exemptions (application intra-ring matrix, ADR-0088 decision 7 P5)",
				f.rel, rel))
		}
	}
	sort.Strings(bad)
	return bad
}

// TestApplicationIntraRingMatrix：真实树执法（应用模块↔应用模块红 / engine/contract
// 叶子纯度红 / 应用模块层内依赖白名单）+ 防盲（扫描面零命中红）。
func TestApplicationIntraRingMatrix(t *testing.T) {
	files := depMatrixPackages(t)
	sats := appRingSatellitePrefixes()
	scanned := 0
	for _, f := range files {
		if depPrefixMatch(f.rel, contractLeaf) {
			scanned++
			continue
		}
		for p := range sats {
			if depPrefixMatch(f.rel, p) {
				scanned++
				break
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("intra-ring matrix matched no satellite or engine/contract files — guard is blind, fix the scan (registered satellites: %d)", len(sats))
	}
	for _, v := range intraRingViolations(files, sats) {
		t.Errorf("%s", v)
	}
}

// TestApplicationIntraRingShapes：红灯实验两腿（合成应用模块互引样例）——
// ①应用模块↔应用模块 import 红；②engine/contract →应用模块红。判据分辨力的活体证明
// （真实树恒绿，分辨率由合成样例承载——intersatellite shapes 同款范式）。
func TestApplicationIntraRingShapes(t *testing.T) {
	sats := map[string]bool{
		"internal/application/logpipes": true,
		"internal/application/metrics":  true,
	}
	t.Run("卫星互引=红", func(t *testing.T) {
		got := intraRingViolations([]listedPackage{
			{rel: "internal/application/metrics/metrics.go", imports: []string{
				"github.com/fleetlyrun/fleetly/internal/application/logpipes",
			}},
		}, sats)
		if len(got) != 1 || !strings.Contains(got[0], "logpipes") || !strings.Contains(got[0], "must not import each other") {
			t.Fatalf("want exactly the satellite-to-satellite violation, got %v", got)
		}
	})
	t.Run("contract 染卫星=红", func(t *testing.T) {
		got := intraRingViolations([]listedPackage{
			{rel: "internal/application/engine/contract/facts.go", imports: []string{
				"github.com/fleetlyrun/fleetly/internal/application/metrics",
			}},
		}, sats)
		if len(got) != 1 || !strings.Contains(got[0], "metrics") || !strings.Contains(got[0], "engine/contract leaf") {
			t.Fatalf("want exactly the leaf-contamination violation, got %v", got)
		}
	})
	t.Run("合法面=绿", func(t *testing.T) {
		got := intraRingViolations([]listedPackage{
			{rel: "internal/application/metrics/metrics.go", imports: []string{
				"github.com/fleetlyrun/fleetly/internal/application/engine/contract",
				"github.com/fleetlyrun/fleetly/internal/application/transact",
				"github.com/fleetlyrun/fleetly/internal/domain/shared/state",
			}},
			{rel: "internal/application/engine/contract/facts.go", imports: []string{
				"github.com/fleetlyrun/fleetly/internal/application/transact",
			}},
			// 应用模块→engine 根：enginebackref 跨包腿管辖（分工注记），本矩阵放行。
			{rel: "internal/application/logpipes/sub.go", imports: []string{
				"github.com/fleetlyrun/fleetly/internal/application/engine",
				"github.com/fleetlyrun/fleetly/internal/application/logpipes/inner",
			}},
		}, sats)
		if len(got) != 0 {
			t.Fatalf("want the legal faces to stay green, got %v", got)
		}
	})
}
