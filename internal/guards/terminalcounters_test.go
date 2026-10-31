package guards

// 终态计数无独立计数器状态（B2a W1b，zeabur 借鉴设计 §2.4 第二轮定稿）：
// per-workload 终态计数 = 事件账本派生（engine.DeriveTerminalCounts——
// 计数完全由事件切片计算，纯度由实现面单测承载），持久面不得出现计数表/
// 计数列——独立计数器状态会脱离事件保留窗自演化（重启漂移、TrimBefore
// 后失同步），与「事实持久化、派生不留状态」的裁决冲突。守卫形态最小：
// grep 断言迁移目录零计数表/零计数语义列。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// counterTableRe 匹配 CREATE TABLE 的计数语义表名（单复数计数器族）。
var counterTableRe = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[^\s(]*counts?[^\s(]*\s*\(`)

// counterColumnRe 匹配计数语义列（restart/crash/terminal/exit 计数器列，
// 建表列定义与 ADD COLUMN 两形态）。
var counterColumnRe = regexp.MustCompile(`(?i)[a-z_]*(?:restart|crash|terminal|exit)_counts?\s+(?:INTEGER|INT|BIGINT)`)

// scanNoPersistentCounters 纯核（shapes 红灯实验直测）：sql 名 → 归一化
// 文本；返回违规清单，空 = 绿。
func scanNoPersistentCounters(sources map[string]string) []string {
	var errs []string
	for _, name := range sortedKeys(sources) {
		body := sources[name]
		if m := counterTableRe.FindString(body); m != "" {
			errs = append(errs, fmt.Sprintf(
				"%s: persistent counter table detected (%q) — terminal counts must be derived from the event ledger (DeriveTerminalCounts), never stored in a counter table", name, m))
		}
		for _, hit := range counterColumnRe.FindAllString(body, -1) {
			errs = append(errs, fmt.Sprintf(
				"%s: persistent counter column detected (%q) — terminal counts must be derived from the event ledger, no independent counter state", name, hit))
		}
	}
	return errs
}

// sortedKeys 是 map 键字典序（违规清单输出稳定）。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// TestNoPersistentTerminalCounterState：对账真迁移目录——零计数表、零
// 计数语义列（目录零 .sql 即 Fatal，扫描失明不静默）。
func TestNoPersistentTerminalCounterState(t *testing.T) {
	dir := filepath.Join(repoRoot(t), filepath.FromSlash(migrationChainDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		sources[e.Name()] = readFileLF(t, migrationChainDir+"/"+e.Name())
	}
	if len(sources) == 0 {
		t.Fatal("no .sql migrations found — guard is blind, fix the scan")
	}
	if errs := scanNoPersistentCounters(sources); len(errs) > 0 {
		t.Fatalf("persistent terminal-counter state detected:\n%s", strings.Join(errs, "\n"))
	}
}

// TestNoPersistentTerminalCounterStateShapes：纯核红灯实验——合法列/表
// 绿，计数表/计数列红。
func TestNoPersistentTerminalCounterStateShapes(t *testing.T) {
	// 绿：现有词汇面（pending_since、last_value、代戳列、普通表名）。
	green := map[string]string{
		"00038_alert_pending_window.sql": readFileLF(t, migrationChainDir+"/00038_alert_pending_window.sql"),
		"00022_alerting.sql":             readFileLF(t, migrationChainDir+"/00022_alerting.sql"),
		"00001_core.sql":                 readFileLF(t, migrationChainDir+"/00001_core.sql"),
	}
	if errs := scanNoPersistentCounters(green); len(errs) != 0 {
		t.Fatalf("existing migrations must be green, got %v", errs)
	}

	// 红：计数表。
	errs := scanNoPersistentCounters(map[string]string{"x.sql": "CREATE TABLE restart_counts (id TEXT PRIMARY KEY, n INTEGER);"})
	if len(errs) != 1 || !strings.Contains(errs[0], "counter table") {
		t.Fatalf("counter table must be red, got %v", errs)
	}
	// 红：计数列（ADD COLUMN 形态与建表列定义形态）。
	for _, body := range []string{
		"ALTER TABLE apps ADD COLUMN crash_count INTEGER NOT NULL DEFAULT 0;",
		"CREATE TABLE t (id TEXT PRIMARY KEY, restart_count INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE t (id TEXT PRIMARY KEY, exit_counts BIGINT NOT NULL DEFAULT 0);",
	} {
		errs = scanNoPersistentCounters(map[string]string{"x.sql": body})
		if len(errs) != 1 || !strings.Contains(errs[0], "counter column") {
			t.Fatalf("counter column must be red, got %v (body: %s)", errs, body)
		}
	}
	// 绿：非计数语义的 count 词汇（如 window_count 之外的普通计数文本注释）。
	if errs := scanNoPersistentCounters(map[string]string{"x.sql": "-- the event count is derived, not stored\nCREATE TABLE facts (id TEXT PRIMARY KEY);"}); len(errs) != 0 {
		t.Fatalf("comment-only mentions must stay green, got %v", errs)
	}
}
