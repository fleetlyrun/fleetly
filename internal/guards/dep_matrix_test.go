package guards

import (
	"strings"
	"testing"
)

// P1a-3b 守卫换装：子树依赖矩阵（架构收敛终态五行规则的现状登记版）。
// 数据表 = 子树 → 允许的 internal import 前缀集；红 = 生产文件的 internal
// import 超出允许集。双向保鲜：子树消失（改名/搬迁）即红、允许前缀不再被
// 任何文件命中即红（登记必须与现况互证，防僵尸行）。防盲自检：矩阵未覆盖
// 的 internal 生产文件即红（新顶层子树必须显式登记），扫描零文件由
// scanGoFiles 既有 fatal 承担。
//
// 终态五行（架构收敛计划 P1）：
//
//	domain/** → 零 external（纯领域，repo 经接口反转）
//	application/** → domain + persistence（消费侧接口化后 github/upload 收进）
//	infrastructure/** → domain
//	interfaces/** → application + domain + persistence（读写分离）
//	assembly/config/buildinfo/testsupport/guards → 全部（组合根/元层）
//
// 本表是现状登记：与终态的差距全部以豁免行落位并带理由，收紧（P1b Repo
// 接口抽取、消费侧接口化）完成后删行——删行后本守卫不改自绿，豁免残留
// 不再命中即红（双向保鲜）。
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
		note:    "终态零外依赖（纯领域）；现状残留见 depExemptions（P1b Repo 接口抽取后清零）",
	},
	{
		subtree: "internal/application/",
		allow: []string{
			"internal/domain/",
			// 客户端注入豁免：github API 客户端与 upload blob 存取仍是具体
			// 类型直注（P1b 消费侧接口化后从本行删除，改走接口落 domain）。
			"internal/infrastructure/persistence/",
			"internal/infrastructure/github/",
			"internal/infrastructure/upload/",
		},
		note: "application → domain + persistence；github/upload 是 P1b 前的登记豁免",
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
			"internal/infrastructure/persistence/",
		},
		note: "读写分离律：读径 repo 直读允许（读侧无需经 application 转手）；写面纪律（写路径必经 application/engine）由 acceptanceguard 承载，本行只执法依赖方向",
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
		subtree:  "internal/buildinfo/",
		allowAll: true,
		note:     "构建元数据（版本锚点，零依赖叶子）",
	},
	{
		subtree:  "internal/testsupport/",
		allowAll: true,
		note:     "测试支撑（跨层 harness 组装，元层）",
	},
	// internal/guards/ 属元层（五行第 5 行），但 scanGoFiles 既有排除清单
	// 已跳过守卫自身（守卫不审守卫），故不登记为行——登记即触发行保鲜红。
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
		subtree: "internal/domain/",
		allow:   "internal/infrastructure/persistence/",
		reason:  "Repo 接口层过渡期（ADR-0075 P1b）：domain 聚合的 Repo 接口按原签名引用根包 state.Runner/Clock/哨兵——根包随 statekit 批（P5）迁入 domain 后删本行；合法命中面仅各聚合 repo.go 接口文件对根包的引用，聚合实现包互引应走 domain 聚合包不经本行",
	},
	{
		subtree: "internal/domain/services/anchor",
		allow:   "internal/infrastructure/persistence/",
		reason:  "anchor 聚合仍直引 repo 具体类型（P1b Repo 接口抽取后收紧，届时删本行）",
	},
	{
		subtree: "internal/domain/services/writes",
		allow:   "internal/infrastructure/persistence/",
		reason:  "writes 聚合直引 outbox/audit repo（P1b Repo 接口抽取后收紧，届时删本行）",
	},
	{
		subtree: "internal/interfaces/grpc/fleetlygrpc",
		allow:   "internal/infrastructure/github/",
		reason:  "gRPC 面直注 github 客户端（P1b 消费侧接口化后收紧，届时删本行）",
	},
	{
		subtree: "internal/interfaces/grpc/fleetlygrpc",
		allow:   "internal/infrastructure/upload/",
		reason:  "gRPC 面直注 upload blob 存取（P1b 消费侧接口化后收紧，届时删本行）",
	},
}

// globalMetaPrefixes 是全子树可消费的元层前缀（五行第 5 行的反向读法：
// 元层不执法消费方）。每个前缀必须仍被命中，否则登记过期即红。
var globalMetaPrefixes = []string{
	// buildinfo：构建版本元数据（ldflags 注入的纯标量），任何层可读。
	"internal/buildinfo/",
}

// depMatrixFiles 收集 internal/ 下全部生产 .go（复用 scanGoFiles：AST
// import 解析 + 生成物排除 + 守卫自身排除 + 零文件防盲 fatal；测试文件
// 不在依赖方向执法面——测试天然跨层）。
func depMatrixFiles(t *testing.T) []goFile {
	t.Helper()
	var out []goFile
	for _, f := range scanGoFiles(t) {
		if !strings.HasPrefix(f.rel, "internal/") || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		t.Fatal("dep matrix found no production files under internal/ — guard is blind, fix the scan")
	}
	return out
}

// TestSubtreeDependencyMatrix：生产文件 internal import 必须落在所在行
// 允许集 ∪ 豁免行 ∪ 元层前缀内；矩阵与现况双向保鲜。
func TestSubtreeDependencyMatrix(t *testing.T) {
	files := depMatrixFiles(t)

	// 行命中计数（子树保鲜 + 允许前缀保鲜）与豁免/元层命中计数。
	rowFiles := make(map[int]int, len(depMatrix))
	allowHits := make(map[string]int) // "rowIdx\x00prefix" -> 命中数
	exemptionHits := make(map[int]int, len(depExemptions))
	metaHits := make(map[string]int, len(globalMetaPrefixes))

	for _, f := range files {
		rowIdx := -1
		for i, row := range depMatrix {
			if strings.HasPrefix(f.rel, row.subtree) {
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
				if strings.HasPrefix(f.rel, ex.subtree) && depPrefixMatch(rel, ex.allow) {
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
// 只执法跨子树方向，环内叶子纯度由 importguard TestLeafPackagesPurity 承担。
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
