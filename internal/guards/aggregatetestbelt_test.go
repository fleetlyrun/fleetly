package guards

// G19 聚合测试带守卫。渊源：G19 条目名随执法手册底稿失考，形态由 P5-3
// 聚合带反推定形（同 G17 先例——条目名失考时以执法对象反推守卫骨架）。
//
// 律文：internal/infrastructure/persistence/ 每个聚合子包必须携带 ≥1 个
// *_test.go（repo 测试带 = 聚合行为契约的最低执法面——冲突归一、钳位、
// 幂等、CAS 等行级语义没有测试载体即不受保护）。migrations（迁移 SQL）与
// statetest（hermetic 夹具包）不在聚合带，排除。白名单双向保鲜：测试落
// 地即删条目；条目指向的包出现测试即红；包消失条目悬空即红。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// aggregateTestWhitelist 是聚合测试带白名单（包名 → 理由，中文一句话）。
// 现入白名单十件（P5-3 批次：volume/run/user/audit 四件已带测试落地，
// 余十件薄面缓补）：条目理由如实描述 repo 面现状；测试落地即删条目。
var aggregateTestWhitelist = map[string]string{
	"alertrule":   "repo 面薄（规则 CRUD 直写 + Observe/Transition 观测迁移），带测试待补",
	"browse":      "repo 面薄（会话台账 Create/List/CountByProject/Delete 直写），带测试待补",
	"catalog":     "repo 面薄（单行快照 Put/Get 两方法，固定 id=1 覆盖写），带测试待补",
	"channel":     "repo 面薄（通道 CRUD 直写 + RecordDelivery 失败回写），带测试待补",
	"idempotency": "repo 面薄（Claim/Complete/Release/Sweep 幂等记录直写），带测试待补",
	"invitation":  "repo 面薄（Create/GetBySHA256/List/MarkConsumed 直写），带测试待补",
	"logcursor":   "repo 面薄（单游标 Get/Save 两方法，零值回落有界回看窗），带测试待补",
	"membership":  "repo 面薄（关联 Create/GetByUser/DeleteByUser/ListByUser 直写），带测试待补",
	"role":        "repo 面薄（CRUD + UpsertBuiltin 种子幂等直写），带测试待补",
	"team":        "repo 面薄（Create/Get/List/Delete 直写），带测试待补",
}

// aggregateBeltExcluded 是聚合带排除目录（非聚合包）。
var aggregateBeltExcluded = map[string]bool{
	"migrations": true, // 迁移 SQL 脚本目录（forward_test 已自成测试带）
	"statetest":  true, // hermetic 测试夹具包（元层，非聚合）
}

// scanAggregateTestBelt 扫描 root 的一级子目录（可注入根——shapes 用例
// 据此做红灯自证），返回 (聚合目录名集, 违规描述集)。违规三类：
// 缺测试且未豁免、白名单条目过期（包已带测试/包已消失）、豁免缺理由。
func scanAggregateTestBelt(t *testing.T, root string, whitelist map[string]string) ([]string, []string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var aggs []string
	var gaps []string
	for _, e := range entries {
		if !e.IsDir() || aggregateBeltExcluded[e.Name()] {
			continue
		}
		name := e.Name()
		aggs = append(aggs, name)
		hasTest := false
		files, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), "_test.go") {
				hasTest = true
				break
			}
		}
		switch {
		case hasTest:
			if reason, ok := whitelist[name]; ok {
				gaps = append(gaps, fmt.Sprintf(
					"whitelist entry %q is stale: the package now carries tests (registered reason: %s); delete the entry",
					name, reason))
			}
		default:
			reason, ok := whitelist[name]
			if !ok {
				gaps = append(gaps, fmt.Sprintf(
					"persistence aggregate %q carries no *_test.go and is not whitelisted — add a repo test or register an exemption with a reason (G19 聚合测试带)", name))
				continue
			}
			if reason == "" {
				gaps = append(gaps, fmt.Sprintf("whitelist entry %q must carry a reason", name))
			}
		}
	}
	// 条目悬空：白名单指向的包已消失（改名/搬迁未同步条目）。
	for name := range whitelist {
		if !contains(aggs, name) {
			gaps = append(gaps, fmt.Sprintf(
				"whitelist entry %q points at a package that no longer exists under %s — update or delete the entry", name, root))
		}
	}
	sort.Strings(gaps)
	sort.Strings(aggs)
	return aggs, gaps
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// TestAggregateTestBelt：persistence 全聚合测试带执法（真仓根）。
// 防盲：一级聚合子目录实测 37（2026-10-16 P5-3；39 目录 − migrations −
// statetest），阈值 30 留余量——扫描退化（根错/目录挪走）在此显形。
func TestAggregateTestBelt(t *testing.T) {
	root := filepath.Join(repoRoot(t), "internal", "infrastructure", "persistence")
	aggs, gaps := scanAggregateTestBelt(t, root, aggregateTestWhitelist)
	if len(aggs) < 30 {
		t.Fatalf("aggregate belt scan found only %d packages (expected >= 30) — guard is blind, fix the scan", len(aggs))
	}
	for _, g := range gaps {
		t.Error(g)
	}
}

// TestAggregateTestBeltShapes：内联临时目录红灯自证（扫描器根可注入）——
// 缺测试包即红、migrations/statetest 排除、白名单豁免即绿、白名单过期
// （包已带测试/包已消失）即红、豁免缺理由即红。
func TestAggregateTestBeltShapes(t *testing.T) {
	mk := func(t *testing.T, files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		for rel, content := range files {
			path := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	root := mk(t, map[string]string{
		"migrations/00001_core.sql": "-- 迁移脚本",
		"statetest/statetest.go":    "package statetest",
		"volume/repo.go":            "package volume",
		"volume/repo_test.go":       "package volume_test",
		"team/repo.go":              "package team",
		"team/repo_test.go":         "package team_test",
		"alertrule/repo.go":         "package alertrule",
		"ghost/repo.go":             "package ghost",
	})

	// 形 1：缺测试包（alertrule/ghost）即红；volume 有测试、排除目录不执法。
	_, gaps := scanAggregateTestBelt(t, root, nil)
	if len(gaps) != 2 || !strings.Contains(gaps[0], "alertrule") || !strings.Contains(gaps[1], "ghost") {
		t.Fatalf("shape 1: want exactly alertrule+ghost gaps, got %v", gaps)
	}

	// 形 2：白名单豁免 alertrule → 只剩 ghost；缺理由条目即红。
	_, gaps = scanAggregateTestBelt(t, root, map[string]string{"alertrule": ""})
	if len(gaps) != 2 || !strings.Contains(gaps[0], "ghost") || !strings.Contains(gaps[1], "alertrule") {
		t.Fatalf("shape 2: want alertrule empty-reason + ghost gaps, got %v", gaps)
	}
	_, gaps = scanAggregateTestBelt(t, root, map[string]string{"alertrule": "repo 面薄，带测试待补"})
	if len(gaps) != 1 || !strings.Contains(gaps[0], "ghost") {
		t.Fatalf("shape 2b: want only ghost gap, got %v", gaps)
	}

	// 形 3：白名单过期——volume/team 已带测试，条目即红；vanished 包根本
	// 不在夹具里，悬空条目即红；alertrule/ghost 无测试，条目合法不红。
	_, gaps = scanAggregateTestBelt(t, root, map[string]string{
		"volume":    "已带测试仍挂条目",
		"team":      "已带测试仍挂条目",
		"vanished":  "包已消失仍挂条目",
		"alertrule": "repo 面薄，带测试待补",
		"ghost":     "repo 面薄，带测试待补",
	})
	if len(gaps) != 3 || !strings.Contains(gaps[0], "\"team\"") ||
		!strings.Contains(gaps[1], "\"vanished\"") || !strings.Contains(gaps[2], "\"volume\"") {
		t.Fatalf("shape 3: want stale volume/team + dangling vanished entries, got %v", gaps)
	}

	// 形 4：全绿形态——alertrule 豁免、volume/team 带测试无条目，只剩
	// ghost 一个缺口。
	_, gaps = scanAggregateTestBelt(t, root, map[string]string{"alertrule": "repo 面薄，带测试待补"})
	if len(gaps) != 1 || !strings.Contains(gaps[0], "ghost") {
		t.Fatalf("shape 4: want only ghost gap, got %v", gaps)
	}
}
