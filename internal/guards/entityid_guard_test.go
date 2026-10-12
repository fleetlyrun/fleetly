package guards

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ADR-0070 结构守卫三件：
//
//  1. state 层零 `ORDER BY id`——seq 是创建序唯一真源，id 十进制字典序陷阱
//     （web-9 > web-1042）必须无处遁形；排序/游标一律 seq。
//  2. 全仓零 oklog/ulid import——可读 id 单一格式，ULID 退役（go.mod 同步
//     出依赖，守卫防复引）。
//  3. 迁移 seq 不变量——每张 TEXT 主键聚合表必须携带 seq INTEGER NOT NULL
//     且配 UNIQUE(seq)（双铸 fail-closed + MAX 快路径）；豁免表带理由，
//     不再豁免即红（双向保鲜）。

// stateOrderByIDRe 命中 state 层 SQL 的 id 排序（含 id ASC/DESC 变体）。
var stateOrderByIDRe = regexp.MustCompile(`ORDER BY\s+(?:[a-z]+\.)?id\b`)

// ulidImportRe 命中 oklog/ulid 的 import 路径。
var ulidImportRe = regexp.MustCompile(`github\.com/oklog/ulid`)

// seqExemptTables 是无自有 seq 的表（豁免带理由，双向保鲜）：
//   - outbox：seq 即 INTEGER PK AUTOINCREMENT（事件流单调队号，先例）。
//   - revisions：per-App seq（UNIQUE(app_id,seq)）即修订创建序，不另设全局列。
//   - app_hooks / hook_deliveries / idempotency_records / log_cursors /
//     template_catalog：无 TEXT 实体 id 主键（复合键/固定单行/namespace 键），
//     非聚合实体行。
var seqExemptTables = map[string]string{
	"outbox":              "seq 即 INTEGER PK AUTOINCREMENT（事件流队号）",
	"revisions":           "per-App seq（UNIQUE(app_id,seq)）即修订创建序",
	"app_hooks":           "主键 = app_id（1:1 载体行），无自有实体 id",
	"hook_deliveries":     "复合主键 (app_id, delivery)（webhook 去重账），非实体行",
	"idempotency_records": "主键 = idem_key（幂等键），非实体行",
	"log_cursors":         "主键 = namespace（日志游标键），非实体行",
	"template_catalog":    "INTEGER 主键固定单行（模板目录），非 ULID 世代实体",
}

func TestStateLayerNeverOrdersByID(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "state")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if stateOrderByIDRe.MatchString(line) {
				t.Errorf("%s: %q — state 层禁止 ORDER BY id（ADR-0070：seq 是创建序唯一真源；十进制字典序陷阱）", rel, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoULIDImports(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel == ".git" || rel == "console" || rel == "docs" || rel == ".tmp-bin" ||
				strings.HasPrefix(rel, "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || generatedFile(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if ulidImportRe.Match(data) {
			t.Errorf("%s: oklog/ulid 复引（ADR-0070：ULID 已退役，id 一律 entityid 装配）", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// createTableBlock 是迁移文件里的一张建表块。
var createTableRe = regexp.MustCompile(`(?s)CREATE TABLE (\w+) \((.*?)\n\);`)

func TestMigrationsAggregateTablesCarrySeq(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "state", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(data), -1) {
			table, body := m[1], m[2]
			if seen[table] {
				continue
			}
			seen[table] = true
			if _, exempt := seqExemptTables[table]; exempt {
				continue
			}
			if !regexp.MustCompile(`(?m)^\s*id\s+TEXT PRIMARY KEY`).MatchString(body) {
				continue // 非实体 id 主键表（复合键等）不载体裁
			}
			if !regexp.MustCompile(`(?m)^\s*seq\s+INTEGER NOT NULL`).MatchString(body) {
				t.Errorf("%s: 表 %s 缺 seq INTEGER NOT NULL（ADR-0070：创建序唯一真源 + 可读 id 后缀）", e.Name(), table)
			}
			if !strings.Contains(string(data), "CREATE UNIQUE INDEX idx_"+table+"_seq ON "+table+"(seq)") {
				t.Errorf("%s: 表 %s 缺 UNIQUE(seq) 索引（双铸 fail-closed + MAX 快路径）", e.Name(), table)
			}
		}
	}
}
