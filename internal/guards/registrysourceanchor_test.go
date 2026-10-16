package guards

// 「注册表 Source 锚点律」——errcode/eventcode 排障入口对账。
//
// 渊源:2026-10-16 独立审计发现 P1a 四环搬移(ADR-0075)后,errcode/eventcode
// 注册表 builtins 的 Source 锚系统性悬空——105 条仍指向搬移前旧路径
// (internal/api/fleetlygrpc → internal/interfaces/grpc/fleetlygrpc、
// internal/engine → internal/application/engine 等),另有个别条目锚形态
// 失准(目录充当路径、裸文件名缺路径分隔)。Source 是错误码/事件排障的
// 第一入口:排障者从 golden/文档按码索锚,锚悬空即被引向不存在的文件。
// 本批同 commit 完成全量改写,本守卫为对账律,防悬空回归。
//
// 律文:errcode(codes.go)/eventcode(events.go)注册表每条 Source——
//   - 不含 "/" 与 "\" 的条目是 "added during implementation" 豁免形态
//     (事件接入前的临时占位,eventcode 包头注 sanction),不在本律射程;
//   - 含 "/" 的条目,首 token 是主锚:必须含 "/"(仓根相对路径)且以
//     .go 结尾、无反斜杠——目录锚、裸文件名锚、Windows 形态即红(形态律);
//   - 其余 token 中含 "/" 且以 .go 结尾的是次锚(+/;/→ 连接的多锚条目),
//     同样断言存在;含反斜杠即红;
//   - 每条锚在 repoRoot 下 os.Stat 必须命中常规文件——指向不存在路径
//     即红(存在律)。行号不锚(易腐),限定符一律函数名。
//
// 识别精度(防误报):路径 token 必须独立成词(路径与限定符间一个空格,
// 照抄注册表现有最优条目风格);斜杠复合限定符(DeleteApp/DeleteProject)
// 与括注不是路径,不在存在律射程。
//
// 防盲:Source 条目总数、路径形态条目数、存在性断言次数均设下限(实测值
// 记注释,留余量)——注册表被清空或提取正则失配时守卫显式红而非静默变盲。
// golden 快照(errcode_test.go/eventcode_test.go)逐字钉死 Source 全文:
// 改锚必同批 -update 再生成 golden,两守卫夹住"改锚不更 golden"的缝。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// registrySourceFiles 是注册表真源(改锚只改这里,golden 同批再生成)。
var registrySourceFiles = []string{
	"internal/domain/shared/model/errcode/codes.go",
	"internal/domain/shared/model/eventcode/events.go",
}

// sourceFieldRe 提取注册表源文本中的每条 Source 字面量(builtins 的统一
// 字段形态;散文注释无 `Source:"…"` 紧邻形态,不失配)。
var sourceFieldRe = regexp.MustCompile(`Source:\s*"([^"]*)"`)

// registrySource 是一条注册表 Source 的解析结果。
type registrySource struct {
	regFile string // 所属注册表文件(仓根相对,斜杠分隔)
	value   string // Source 字面量原文
}

// extractRegistrySources 从注册表源文本提取全部 Source 条目(执法与
// shapes 共用的提取面)。
func extractRegistrySources(regFile, src string) []registrySource {
	var out []registrySource
	for _, m := range sourceFieldRe.FindAllStringSubmatch(src, -1) {
		out = append(out, registrySource{regFile: regFile, value: m[1]})
	}
	return out
}

// scanRegistrySources 读仓内注册表真源,返回全部 Source 条目。
func scanRegistrySources(t *testing.T) []registrySource {
	t.Helper()
	root := repoRoot(t)
	var out []registrySource
	for _, rel := range registrySourceFiles {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // 守卫扫描仓库自有源文件
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, extractRegistrySources(rel, string(src))...)
	}
	return out
}

// sourceAnchors 对单条 Source 做形态执法并解析路径锚:返回应断言存在的
// 锚列表与形态问题列表(不触磁盘——存在律由 missingSourceAnchors 承担)。
func sourceAnchors(value string) (anchors []string, problems []string) {
	if !strings.ContainsAny(value, "/\\") {
		return nil, nil // "added during implementation" 豁免形态(纯反斜杠路径不含 "/",必须放行进执法面)
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return nil, []string{fmt.Sprintf("source %q carries \"/\" but no token", value)}
	}
	first := fields[0]
	switch {
	case strings.ContainsRune(first, '\\'):
		problems = append(problems, fmt.Sprintf("primary anchor %q uses backslash separators (Windows form)", first))
	case !strings.Contains(first, "/") || !strings.HasSuffix(first, ".go"):
		problems = append(problems, fmt.Sprintf("primary anchor %q must be a repo-relative path ending in .go — dir-only or bare-filename anchors rot; use path + space + function qualifier", first))
	default:
		anchors = append(anchors, first)
	}
	for _, f := range fields[1:] {
		if strings.ContainsRune(f, '\\') {
			problems = append(problems, fmt.Sprintf("token %q uses backslash separators (Windows form)", f))
			continue
		}
		// 次锚识别:含 "/" 且以 .go 结尾才是路径 token——斜杠复合限定符
		//(DeleteApp/DeleteProject)与括注不是路径,不在射程。
		if strings.Contains(f, "/") && strings.HasSuffix(f, ".go") {
			anchors = append(anchors, f)
		}
	}
	return anchors, problems
}

// missingSourceAnchors 在 root 下逐条断言锚是现存常规文件,返回悬空锚
// 列表(root 注入,shapes 用临时树复用同一执法面)。
func missingSourceAnchors(root string, anchors []string) []string {
	var missing []string
	for _, a := range anchors {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(a)))
		if err != nil || !info.Mode().IsRegular() {
			missing = append(missing, a)
		}
	}
	return missing
}

// TestRegistrySourceAnchorLaw:errcode/eventcode 注册表每条 Source 的路径
// 锚必须指向仓内现存文件,排障入口失准即红(渊源与律文见文件头注)。
func TestRegistrySourceAnchorLaw(t *testing.T) {
	entries := scanRegistrySources(t)
	root := repoRoot(t)

	// 防盲自检(实测 2026-10-16):Source 条目 132(codes 36 + events 96,
	// 阈值 130);路径形态条目 107(codes 36 + events 71,阈值 100);存在性
	// 断言 119 次(主锚 107 + 多锚条目次锚 12,阈值 110)——红时先重测再调值。
	const minEntries, minPathFormed, minAsserted = 130, 100, 110
	if len(entries) < minEntries {
		t.Fatalf("scan extracted only %d Source entries (want ≥%d) across %d registries — guard is blind, fix the scan",
			len(entries), minEntries, len(registrySourceFiles))
	}

	pathFormed, asserted := 0, 0
	var bad []string
	for _, e := range entries {
		anchors, problems := sourceAnchors(e.value)
		if len(anchors) > 0 {
			pathFormed++
		}
		for _, p := range problems {
			bad = append(bad, fmt.Sprintf("%s: %s", e.regFile, p))
		}
		for _, m := range missingSourceAnchors(root, anchors) {
			bad = append(bad, fmt.Sprintf(
				"%s: Source anchor %q does not exist in the repo — the troubleshooting entry is dangling; "+
					"repoint it at the current file (path + space + function qualifier, no line numbers) "+
					"and regenerate the registry golden in the same commit (registry source-anchor law)",
				e.regFile, m))
		}
		asserted += len(anchors)
	}
	if pathFormed < minPathFormed {
		t.Fatalf("only %d Source entries carry path anchors (want ≥%d) — guard is blind, fix the scan", pathFormed, minPathFormed)
	}
	if asserted < minAsserted {
		t.Fatalf("only %d anchor-existence assertions ran (want ≥%d) — guard is blind, fix the scan", asserted, minAsserted)
	}

	sort.Strings(bad)
	for _, v := range bad {
		t.Errorf("%s", v)
	}
}

// TestRegistrySourceAnchorLawShapes 是对账律的用例面(注入临时树):四态
// ——锚点文件存在=绿/指向不存在路径=红/形态坏(缺 .go 后缀、目录锚、
// 反斜杠)=红/零锚点=防盲红。
func TestRegistrySourceAnchorLawShapes(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("锚点文件存在=绿", func(t *testing.T) {
		mk("internal/x/mapping.go")
		mk("internal/x/other.go")
		anchors, problems := sourceAnchors("internal/x/mapping.go mapStateError (context); internal/x/other.go OtherFn")
		if len(problems) != 0 {
			t.Fatalf("want no form problems, got %v", problems)
		}
		if len(anchors) != 2 {
			t.Fatalf("want 2 anchors (primary + secondary), got %v", anchors)
		}
		if missing := missingSourceAnchors(root, anchors); len(missing) != 0 {
			t.Fatalf("want no missing anchors, got %v", missing)
		}
	})

	t.Run("指向不存在路径=红", func(t *testing.T) {
		mk("internal/x/present.go")
		anchors, problems := sourceAnchors("internal/x/present.go Fn + internal/x/gone.go Fn")
		if len(problems) != 0 {
			t.Fatalf("well-formed source must carry no form problems, got %v", problems)
		}
		missing := missingSourceAnchors(root, anchors)
		if len(missing) != 1 || missing[0] != "internal/x/gone.go" {
			t.Fatalf("want exactly the dangling anchor reported, got %v", missing)
		}
	})

	t.Run("形态坏=红(缺 .go 后缀/目录锚/裸文件名/反斜杠/主锚缺位)", func(t *testing.T) {
		for _, value := range []string{
			"internal/engine/admission.go.bak Fn", // 非 .go 结尾
			"internal/x/mapping Fn",               // 目录级锚(P1a 悬空形态)
			"internal/x webhook.go Fn",            // 目录 + 裸文件名(缺路径分隔)
			`internal\x\mapping.go Fn`,            // Windows 反斜杠形态
			"mapStateError internal/x/other.go",   // 主锚不在首 token
		} {
			if _, problems := sourceAnchors(value); len(problems) == 0 {
				t.Fatalf("source %q must be flagged by the form law", value)
			}
		}
	})

	t.Run("豁免形态与限定符不误报", func(t *testing.T) {
		if anchors, problems := sourceAnchors("added during implementation"); anchors != nil || problems != nil {
			t.Fatalf("placeholder form must be out of scope, got anchors=%v problems=%v", anchors, problems)
		}
		// 斜杠复合限定符与括注不是路径 token,不进存在律射程。
		anchors, problems := sourceAnchors("internal/x/mapping.go DeleteApp/DeleteProject (state.ErrConflict: CAS / FK)")
		if len(problems) != 0 || len(anchors) != 1 {
			t.Fatalf("qualifier tokens must not be treated as anchors, got anchors=%v problems=%v", anchors, problems)
		}
	})

	t.Run("零锚点=红(防盲)", func(t *testing.T) {
		if got := extractRegistrySources("empty.go", "package p\n\nvar builtins = []Code{}\n"); len(got) != 0 {
			t.Fatalf("empty registry must extract 0 entries, got %d", len(got))
		}
		// 提取数为 0 时主测试的三重下限(条目/路径形态/断言 ≥130/100/110)
		// 必然 Fatal——提取正则失配或注册表被清空不会静默变盲。
	})
}
