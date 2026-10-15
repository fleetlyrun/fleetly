package guards

// G13「迁移链锚律」——执法手册 G13 条目的反推形态。
//
// 渊源：G13 条目名已随执法手册底稿失考（同 P4-3 G17 先例、P5-1 G7/G8 同
// 款反推）；按 P5 批内真实缺口反推钉定为迁移链锚律，属对账家族——迁移链
// 前进（新增 *.sql）时 forward_test.go 的两处锚必须同批跟进，否则前滚演
// 练静默停在旧链头（era 库换装演练失真）。若手册原文另有所指，可回退本
// 律重钉，裁决注记随主会话 commit 在案。
//
// 律文：internal/infrastructure/persistence/migrations/forward_test.go 的
// latestMigrationVersion 常量必须等于迁移目录实际最大版本号 N
//（000NN_*.sql 形），且 TestMigrateForwardFromRecentCheckpoints 的近期检
// 查点表必须含 N-1——链前进时本守卫直接指出该改的两处（常量 + 检查点表）。
//
// 防盲：目录实测最大版本 N=32（2026-10-16），阈值钉 ≥20——正则或目录漂
// 移致 N 骤小即红，不静默放行。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// migrationChainDir 是迁移目录（仓库相对，斜杠分隔）。
const migrationChainDir = "internal/infrastructure/persistence/migrations"

// migrationChainBlindFloor 是防盲下限：目录扫描失明（正则/路径漂移）时
// N 骤小即红。实测 N=32（2026-10-16）。
const migrationChainBlindFloor = 20

// migrationFileRe 匹配 000NN_name.sql 的零填充版本号（前导零逐段吃掉，
// Atoi 对 000032 与 032 同值——宽于五位也不误判）。
var migrationFileRe = regexp.MustCompile(`^0*(\d+)_.+\.sql$`)

var (
	// latestConstRe 提取 forward_test.go 的 latestMigrationVersion 常量值。
	latestConstRe = regexp.MustCompile(`(?m)^const latestMigrationVersion = (\d+)$`)
	// checkpointTableRe 提取近期检查点测试的 []int64{...} 字面量（钉在
	// TestMigrateForwardFromRecentCheckpoints 名下——他处 []int64 不混入）。
	checkpointTableRe = regexp.MustCompile(`(?s)func TestMigrateForwardFromRecentCheckpoints.*?\[\]int64\{([^}]*)\}`)
)

// latestVersionOf 迁移文件名集的最大版本号（无匹配 = 0，由防盲下限接住）。
func latestVersionOf(names []string) int {
	n := 0
	for _, name := range names {
		if m := migrationFileRe.FindStringSubmatch(name); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil && v > n {
				n = v
			}
		}
	}
	return n
}

// checkpointsOf 解析检查点表字面量（逗号分隔 int64）；非数字 token 或空表
// 视为形态漂移（ok=false，由调用方报红）。
func checkpointsOf(literal string) (cps []int64, ok bool) {
	for _, tok := range strings.Split(literal, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		v, err := strconv.ParseInt(tok, 10, 64)
		if err != nil {
			return nil, false
		}
		cps = append(cps, v)
	}
	return cps, len(cps) > 0
}

// scanMigrationChainAnchors 纯核（shapes 红灯实验直测）：sqlNames 是迁移
// 目录 .sql 文件名集，forwardTest 是 forward_test.go 文本；返回违规清单，
// 空 = 绿。
func scanMigrationChainAnchors(sqlNames []string, forwardTest string) []string {
	n := latestVersionOf(sqlNames)
	if n < migrationChainBlindFloor {
		return []string{fmt.Sprintf(
			"migration dir scan went blind: max version = %d < floor %d (measured 32 on 2026-10-16) — filename regex or directory moved",
			n, migrationChainBlindFloor)}
	}
	var errs []string
	m := latestConstRe.FindStringSubmatch(forwardTest)
	if m == nil {
		return append(errs, "forward_test.go: latestMigrationVersion 常量缺席或改形 — 链头锚失明，守卫正则须随常量形态同批更新")
	}
	cv, _ := strconv.Atoi(m[1])
	if cv != n {
		errs = append(errs, fmt.Sprintf(
			"migration chain anchor lag: latestMigrationVersion = %d but migrations dir max = %d — 链前进须同批改两处：① forward_test.go 常量 latestMigrationVersion → %d；② 同文件 TestMigrateForwardFromRecentCheckpoints 检查点表补 %d（N-1）",
			cv, n, n, n-1))
	}
	cm := checkpointTableRe.FindStringSubmatch(forwardTest)
	if cm == nil {
		return append(errs, "forward_test.go: TestMigrateForwardFromRecentCheckpoints 检查点表（[]int64{...}）缺席或改形 — 守卫正则须随测试形态同批更新")
	}
	cps, ok := checkpointsOf(cm[1])
	if !ok {
		return append(errs, fmt.Sprintf(
			"forward_test.go: 检查点表字面量含非数字 token（%q）— 形态漂移，守卫解析须同批更新", cm[1]))
	}
	hasPrev := false
	for _, cp := range cps {
		if cp == int64(n-1) {
			hasPrev = true
		}
	}
	if !hasPrev {
		errs = append(errs, fmt.Sprintf(
			"forward_test.go: 近期检查点表 %v 缺 N-1 = %d — 链前进须同批改两处：① latestMigrationVersion 常量；② 本检查点表补 %d",
			cps, n-1, n-1))
	}
	return errs
}

// TestMigrationChainAnchorMatchesDir：对账真目录与真 forward_test.go——
// 常量 = 目录最大版本、检查点含 N-1（目录零 .sql 即 Fatal，扫描失明不静默）。
func TestMigrationChainAnchorMatchesDir(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(repoRoot(t), filepath.FromSlash(migrationChainDir)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no .sql migrations found — guard is blind, fix the scan")
	}
	errs := scanMigrationChainAnchors(names, readFileLF(t, migrationChainDir+"/forward_test.go"))
	if len(errs) > 0 {
		t.Fatalf("migration chain anchor drifted:\n%s", strings.Join(errs, "\n"))
	}
}

// TestMigrationChainAnchorShapes：纯核红灯实验——一致=绿 / 常量落后=红
// （报错指明改两处）/ 检查点缺 N-1=红 / 防盲（N 骤小）=红。
func TestMigrationChainAnchorShapes(t *testing.T) {
	// 合成 00001..max 零填充链。
	dirNames := func(max int) []string {
		names := make([]string, 0, max)
		for v := 1; v <= max; v++ {
			names = append(names, fmt.Sprintf("%05d_step.sql", v))
		}
		return names
	}
	forwardTest := func(latest int64, cps ...int64) string {
		parts := make([]string, len(cps))
		for i, cp := range cps {
			parts[i] = strconv.FormatInt(cp, 10)
		}
		return "package migrations\n" +
			"const latestMigrationVersion = " + strconv.FormatInt(latest, 10) + "\n" +
			"func TestMigrateForwardFromRecentCheckpoints(t *testing.T) {\n" +
			"\tfor _, cp := range []int64{" + strings.Join(parts, ", ") + "} {\n\t}\n}\n"
	}

	// 绿：常量 = 目录最大版本 32，检查点含 N-1 = 31。
	if errs := scanMigrationChainAnchors(dirNames(32), forwardTest(32, 29, 30, 31)); len(errs) != 0 {
		t.Fatalf("consistent chain must be green, got %v", errs)
	}

	// 红：常量落后（目录 32 / 常量 31）——报错直接指明改哪两处。
	errs := scanMigrationChainAnchors(dirNames(32), forwardTest(31, 29, 30, 31))
	if len(errs) != 1 ||
		!strings.Contains(errs[0], "latestMigrationVersion") ||
		!strings.Contains(errs[0], "检查点表") ||
		!strings.Contains(errs[0], "两处") {
		t.Fatalf("lagging constant must be red naming both fix sites, got %v", errs)
	}

	// 红：检查点缺 N-1（31 缺席）。
	errs = scanMigrationChainAnchors(dirNames(32), forwardTest(32, 28, 29, 30))
	if len(errs) != 1 || !strings.Contains(errs[0], "31") {
		t.Fatalf("checkpoint table missing N-1 must be red, got %v", errs)
	}

	// 红：防盲——目录骤缩（N=5 < 20）即红，不静默放行。
	errs = scanMigrationChainAnchors(dirNames(5), forwardTest(5, 3, 4))
	if len(errs) != 1 || !strings.Contains(errs[0], "blind") {
		t.Fatalf("shrunken dir must trip the blind floor, got %v", errs)
	}
}
