package guards

// application 环 genproto import 基线冻结守卫（ADR-0088 决策 6「O4 审计门」
// 裁决 B：豁免+基线冻结，P4.5 批 2026-11-01）。立法来源：docs/adr/
// 0088-engine-satellite-packages-and-growth-law.md 决策 6——O4 全量清点
// （2026-11-01：22 非测试文件 / 23 import 点，engine 20 + composeintake 1 +
// schema 1；含 preview.go 的 delivery/v1 一条——ADR 原基线 19 只计 spec/v1，
// 实测已漂移，本守卫以实测定基）裁决驱逐不可行：spec IR 生成类型是
// domain(shared/spec)↔application 双向管道的流通类型（Revision 持久化体
// 即 AppSpec 的 protojson blob——observ.go unmarshalSpec；domain 环自身经
// domainpurity 豁免在案），驱逐=全 IR 面双类型系统+持久化格式迁移，推翻
// 「proto 是唯一契约源」的既有仓级裁决（schema/specs.go 与 preview.go 的
// 直引注释同款论证）。结论：冻结既有 import 点，扩张必须随评审裁定同
// commit 刷新基线。
//
// 机制（enginefuncregistry 范式：基线快照 + 超基线即红 + 双向保鲜 + 防盲锚）：
//
//	基线  internal/application 全部非测试 .go 的 genproto import 点全集
//	      （key = 文件路径 -> import 路径），冻结为
//	      testdata/application-genproto-baseline.golden；
//	判据  超基线的新 genproto import 点即红——扩基线必须随评审裁定
//	      （新 import 点 = genproto 面向 application 环的新扩张，不是
//	      机械重构）同 commit 再生成：
//	      go test ./internal/guards -run TestApplicationGenprotoBaselineFreeze -update
//	      （-update flag 与 enginefuncregistry 共享同一注册，见
//	      engineFuncBaselineUpdate）；
//	保鲜  基线条目不再命中（import 移除/文件删除）即红——基线与代码
//	      同 commit 收缩，基线不得腐化出死条目；
//	防盲  ①基线条目数下限（20，实测 2026-11-01 = 23）；②0 文件扫描
//	      fatal；③判据分辨力锚（合成 fixture：genproto import 必须命中、
//	      标准库/genproto 模块外的第三方 import 必须不命中——前缀判据
//	      失明/过宽任一漂移都红）。
//
// P5 关联（ADR-0088 决策 1/7）：卫星包拆包后若平级驻 internal/application/
// 下，扫描面自动覆盖；若路径裁定挪出 application 环，本守卫扫描面随 P5
// 首项守卫扫描面整节重构同批调整。

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// appGenprotoBaselineGolden 是 application genproto import 点基线快照路径
// （一行一个 key：文件路径 -> import 路径，# 起始行为注释）。
const appGenprotoBaselineGolden = "internal/guards/testdata/application-genproto-baseline.golden"

// appGenprotoFreezeRoot 是执法面根（application 环全部子包，非测试 .go）。
const appGenprotoFreezeRoot = "internal/application"

// appGenprotoBaselineFloor 是防盲①的基线条目数下限。实测 2026-11-01 = 23
// （22 文件；preview.go 持 spec/v1 与 delivery/v1 两点），阈值 20 给读侧
// 拆小文件留余量——红时先重测再调值。
const appGenprotoBaselineFloor = 20

// appGenprotoKey 生成基线 key：文件 -> import（确定性形态，golden 一行）。
func appGenprotoKey(fileRel, imp string) string {
	return fileRel + " -> " + imp
}

// isGenprotoImport 报告 imp 是否 fleetly genproto module 的生成物面
// （红线②同源判据：domainpurity 的 genprotoImportPrefix——两守卫共用
// 「genproto module = 契约生成物面」的语义，不各自造前缀）。
func isGenprotoImport(imp string) bool {
	return strings.HasPrefix(imp, genprotoImportPrefix)
}

// scanApplicationGenprotoImports 解析 root 下全部非测试 .go（跳过
// testdata/），产出 genproto import 点 key 全集。0 文件即 fatal（防盲②：
// 守卫不得静默通过）。
func scanApplicationGenprotoImports(t *testing.T, root string) []string {
	t.Helper()
	var keys []string
	sawFile := false
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
		sawFile = true
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		rel, rerr := filepath.Rel(repoRoot(t), path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if isGenprotoImport(p) {
				keys = append(keys, appGenprotoKey(rel, p))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawFile {
		t.Fatalf("application genproto scan found no production .go under %s — guard is blind, fix the scan", root)
	}
	sort.Strings(keys)
	return keys
}

// loadAppGenprotoBaseline 载入基线 golden（# 起始行与空行跳过；空基线
// fatal——防盲）。
func loadAppGenprotoBaseline(t *testing.T) map[string]bool {
	t.Helper()
	baseline := map[string]bool{}
	for _, line := range strings.Split(readFileLF(t, appGenprotoBaselineGolden), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		baseline[line] = true
	}
	if len(baseline) < appGenprotoBaselineFloor {
		t.Fatalf("baseline golden %s holds only %d keys — guard is blind, fix the scan or regenerate the baseline", appGenprotoBaselineGolden, len(baseline))
	}
	return baseline
}

// writeAppGenprotoBaseline 重写基线 golden（-update 通道，flag 与
// enginefuncregistry 共享：go test ./internal/guards -run TestApplicationGenprotoBaselineFreeze -update）。
func writeAppGenprotoBaseline(t *testing.T, keys []string) {
	t.Helper()
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("# application 环 genproto import 基线（ADR-0088 决策 6 O4 审计门，裁决 B）——冻结快照\n")
	b.WriteString("#（一行一个 key：文件路径 -> import 路径）。超基线的新 import 点即红：扩张\n")
	b.WriteString("# 须随评审裁定同 commit 再生成；基线条目不再命中（import 移除/文件删除）\n")
	b.WriteString("# 也红——基线与代码同 commit 收缩（appgenprotofreeze_test.go）。\n")
	b.WriteString("#\n# 再生成：go test ./internal/guards -run TestApplicationGenprotoBaselineFreeze -update\n")
	for _, k := range sorted {
		b.WriteString(k)
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(repoRoot(t), filepath.FromSlash(appGenprotoBaselineGolden)), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestApplicationGenprotoBaselineFreeze：application genproto import 基线
// 冻结执法（ADR-0088 决策 6 O4 裁决 B）。超基线即红（扩张须评审裁定后
// 同 commit 刷新）；基线条目不再命中即红（双向保鲜）。
func TestApplicationGenprotoBaselineFreeze(t *testing.T) {
	keys := scanApplicationGenprotoImports(t, filepath.Join(repoRoot(t), appGenprotoFreezeRoot))

	// -update 通道：重写基线后返回（刷新动作见文件头注释）。
	if *engineFuncBaselineUpdate {
		writeAppGenprotoBaseline(t, keys)
		t.Logf("rewrote %s with %d keys — re-run without -update to enforce", appGenprotoBaselineGolden, len(keys))
		return
	}

	t.Logf("application genproto import points: %d (frozen baseline)", len(keys))
	baseline := loadAppGenprotoBaseline(t)

	// 判据面：超基线即红。
	current := make(map[string]bool, len(keys))
	for _, k := range keys {
		current[k] = true
		if baseline[k] {
			continue
		}
		t.Errorf("new genproto import point %s — beyond the frozen baseline (ADR-0088 decision 6, O4 verdict B): expanding the application ring's genproto surface requires an adjudicated baseline refresh in the same commit (-update), not a silent addition", k)
	}
	// 双向保鲜：基线条目不再命中即红（import 移除/文件删除须同 commit 收缩基线）。
	for k := range baseline {
		if !current[k] {
			t.Errorf("baseline entry %s no longer matches a real import point (import removed / file deleted) — shrink the baseline in the same commit (-update)", k)
		}
	}
}

// TestApplicationGenprotoFreezeDiscrimination：判据分辨力锚（防盲③，
// testdata/appgenprotoanchor/anchor.go 合成样本）。两腿：①扫描器腿——
// fixture 的 genproto import 必须被提取、标准库与 genproto 模块外的第三
// 方 import 必须不被提取（扫描失明/过宽任一漂移都红）；②前缀腿——
// isGenprotoImport 对 spec/v1 与 delivery/v1 两形态必须命中、对 genproto
// 前缀外的路径必须不命中。
func TestApplicationGenprotoFreezeDiscrimination(t *testing.T) {
	keys := scanApplicationGenprotoImports(t, filepath.Join(repoRoot(t), "internal", "guards", "testdata", "appgenprotoanchor"))
	want := "internal/guards/testdata/appgenprotoanchor/anchor.go -> " + genprotoImportPrefix + "fleetly/spec/v1"
	found := false
	for _, k := range keys {
		if k == want {
			found = true
		}
	}
	if !found {
		t.Errorf("scan failed to extract the genproto import from the anchor fixture (expected %q, got %v) — the import predicate is blind; fix appgenprotofreeze_test.go, not the fixture", want, keys)
	}
	if len(keys) != 1 {
		t.Errorf("anchor fixture must yield exactly 1 genproto import point (stdlib and non-genproto third-party imports must not match), got %d (%v) — the predicate is over-broad; fix appgenprotofreeze_test.go, not the fixture", len(keys), keys)
	}
	// 前缀腿：两真实形态命中 + 三反例不命中。
	for _, imp := range []string{
		genprotoImportPrefix + "fleetly/spec/v1",
		genprotoImportPrefix + "fleetly/delivery/v1",
	} {
		if !isGenprotoImport(imp) {
			t.Errorf("isGenprotoImport(%q) = false — lost a real genproto shape", imp)
		}
	}
	for _, imp := range []string{
		"github.com/fleetlyrun/fleetly/internal/domain/shared/spec",
		"google.golang.org/protobuf/encoding/protojson",
		"github.com/fleetlyrun/fleetly/sdk/go/client",
	} {
		if isGenprotoImport(imp) {
			t.Errorf("isGenprotoImport(%q) = true — predicate is over-broad", imp)
		}
	}
}
