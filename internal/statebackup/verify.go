package statebackup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 回读校验（状态诚实契约，architecture §4.2）：快照不是「写出去了」就算
// 备份——立即重新打开，integrity_check + 关键表行数抽查 + 迁移版本核对，
// 任何一步失败 → verify_status=failed（台账/审计/组件三面红）。校验产物
// （schema 版本 + 行数）随 manifest 存档，恢复演练据此对照。

// snapshotFacts 是校验步骤的产物（manifest 的内容来源）。
type snapshotFacts struct {
	// SchemaVersion 是快照内已应用迁移版本（goose 版本表最大 version_id）。
	SchemaVersion int64
	// Tables 是关键表行数抽查结果（表名 → 行数；恢复演练的对照基线）。
	Tables map[string]int64
}

// verifyTables 是行数抽查的表清单（核心表全量——量级小，COUNT 全表即廉价；
// 固定白名单字面量，拼接无注入面）。nodes/orphans 是可整表重建的观测/
// 登记缓存，不进抽查集（行数无恢复价值）。
var verifyTables = []string{
	"apps", "deployments", "revisions", "env_vars", "domains",
	"placements", "volumes", "tokens", "events", "audit_log", "state_backups",
}

// verifyTimeout 是单次校验的预算（打开 + integrity_check + 全部 COUNT）。
const verifyTimeout = 60 * time.Second

// gooseVersionTable 是 goose 版本表名（internal/state 的迁移体系缺省表）。
const gooseVersionTable = "goose_db_version"

// verifySnapshot 执行回读校验：默认走真实校验（verifySnapshotFile）；
// verifyFn 非空时（测试注入）直接使用注入结果——负面测试经此钉死
// 「verify 失败绝不产生绿色台账行」。
func (m *Manager) verifySnapshot(dbPath string) (snapshotFacts, error) {
	if m.verifyFn != nil {
		return m.verifyFn(dbPath)
	}
	return verifySnapshotFile(dbPath)
}

// verifySnapshotFile 重新打开快照文件执行校验（独立连接——VACUUM INTO 的
// 产物是独立单文件库，非 WAL；query_only 确保校验路径零写入）。
func verifySnapshotFile(dbPath string) (snapshotFacts, error) {
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return snapshotFacts{}, fmt.Errorf("statebackup: open snapshot %s: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return snapshotFacts{}, fmt.Errorf("statebackup: ping snapshot: %w", err)
	}
	// 校验路径只读（防未来的校验逻辑意外写快照——写坏产物比校验失败更糟）。
	if _, err := db.ExecContext(ctx, `PRAGMA query_only = 1`); err != nil {
		return snapshotFacts{}, fmt.Errorf("statebackup: set query_only: %w", err)
	}

	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return snapshotFacts{}, fmt.Errorf("statebackup: integrity_check: %w", err)
	}
	if !strings.EqualFold(integrity, "ok") {
		return snapshotFacts{}, fmt.Errorf("statebackup: integrity_check reported %q", integrity)
	}

	var schemaVersion int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM `+gooseVersionTable).Scan(&schemaVersion); err != nil {
		return snapshotFacts{}, fmt.Errorf("statebackup: read schema version: %w", err)
	}
	if schemaVersion <= 0 {
		return snapshotFacts{}, errors.New("statebackup: snapshot has no applied migrations (goose version table empty)")
	}

	facts := snapshotFacts{SchemaVersion: schemaVersion, Tables: map[string]int64{}}
	for _, table := range verifyTables {
		var n int64
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			return snapshotFacts{}, fmt.Errorf("statebackup: count %s: %w", table, err)
		}
		facts.Tables[table] = n
	}
	return facts, nil
}
