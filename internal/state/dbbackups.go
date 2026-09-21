package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// db_backups 台账读写（managed-databases 设计 §2.6）：库数据备份台账，
// restic snapshot ID 寻址（非文件路径）——与控制面 state_backups 分立
//（保留策略/恢复语义/schema 全不同，D-DB-6 被否方案行）。verify_status
// 三态承载「备份假成功零容忍」口径：备份落账即 unverified，回读校验通过
// 置 verified、失败置 failed（失败原因摘要进 error 列，单行化、不含凭据）。

// DatabaseBackupKind 是备份类别词表（kind CHECK 列）。
type DatabaseBackupKind string

const (
	// DatabaseBackupDaily 定时备份（每日计划，per 实例可覆盖时刻）。
	DatabaseBackupDaily DatabaseBackupKind = "daily"
	// DatabaseBackupManual 手动触发（fleetly databases backup）。
	DatabaseBackupManual DatabaseBackupKind = "manual"
	// DatabaseBackupPreUpgrade 升级前门禁备份（verify 通过才继续升级）。
	DatabaseBackupPreUpgrade DatabaseBackupKind = "pre_upgrade"
)

// DatabaseVerifyStatus 是备份回读校验状态位（verify_status CHECK 列）。
type DatabaseVerifyStatus string

const (
	// DatabaseVerifyUnverified 已落账、回读校验未完成。
	DatabaseVerifyUnverified DatabaseVerifyStatus = "unverified"
	// DatabaseVerifyVerified 回读校验通过。
	DatabaseVerifyVerified DatabaseVerifyStatus = "verified"
	// DatabaseVerifyFailed 回读校验失败（红色告警面）。
	DatabaseVerifyFailed DatabaseVerifyStatus = "failed"
)

// ErrDatabaseBackupNotFound 表示目标备份行不存在。
var ErrDatabaseBackupNotFound = errors.New("database backup not found")

// DatabaseBackup 是一行库备份台账。
type DatabaseBackup struct {
	ID string
	// DatabaseID 是归属库实例平台 ID。
	DatabaseID string
	// Kind 取 DatabaseBackup* 词表。
	Kind DatabaseBackupKind
	// ResticSnapshot 是 restic repo 内的 snapshot 标识（db/<instance>/
	// 命名空间——repo 访问形态由 E3 基础设施承载）。
	ResticSnapshot string
	// SizeBytes 是逻辑备份产物大小（导出流字节量；0 = 未记录）。
	SizeBytes int64
	// VerifyStatus 取 DatabaseVerify* 词表。
	VerifyStatus DatabaseVerifyStatus
	// Error 是失败/校验失败原因摘要（单行化；不含凭据材料）。
	Error string
	// CreatedAt 是备份落账时刻（= 备份完成时刻，台账只记完成行）。
	CreatedAt time.Time
}

// InsertDatabaseBackup 落一行备份台账（备份完成后调用；与 db.backup_* 事件
// 同事务组合用 Tx 形态）。id 留空自动生成 ULID；kind/快照标识必填。
func (s *Store) InsertDatabaseBackup(ctx context.Context, b DatabaseBackup) (DatabaseBackup, error) {
	var out DatabaseBackup
	err := s.InTx(ctx, func(tx *Tx) error {
		row, err := tx.InsertDatabaseBackup(ctx, b)
		if err != nil {
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return DatabaseBackup{}, err
	}
	return out, nil
}

// InsertDatabaseBackup 是事务内落账（供与事件同事务组合）。
func (t *Tx) InsertDatabaseBackup(ctx context.Context, b DatabaseBackup) (DatabaseBackup, error) {
	if b.DatabaseID == "" {
		return DatabaseBackup{}, errors.New("state: insert database backup: db_id is empty")
	}
	if b.Kind == "" {
		return DatabaseBackup{}, errors.New("state: insert database backup: kind is empty")
	}
	switch b.Kind {
	case DatabaseBackupDaily, DatabaseBackupManual, DatabaseBackupPreUpgrade:
	default:
		return DatabaseBackup{}, fmt.Errorf("state: database backup kind %q not in {daily, manual, pre_upgrade}", b.Kind)
	}
	if b.ResticSnapshot == "" {
		return DatabaseBackup{}, errors.New("state: insert database backup: restic snapshot is empty")
	}
	if b.ID == "" {
		b.ID = ulid.Make().String()
	}
	if b.VerifyStatus == "" {
		b.VerifyStatus = DatabaseVerifyUnverified
	}
	now := nowNano()
	const q = `INSERT INTO db_backups
		(id, db_id, kind, restic_snapshot, size_bytes, verify_status, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := t.ExecContext(ctx, q,
		b.ID, b.DatabaseID, string(b.Kind), b.ResticSnapshot, b.SizeBytes,
		string(b.VerifyStatus), b.Error, now); err != nil {
		return DatabaseBackup{}, fmt.Errorf("state: insert database backup: %w", err)
	}
	b.CreatedAt = time.Unix(0, now).UTC()
	return b, nil
}

// GetDatabaseBackup 按 ID 取备份行；不存在返回 ErrDatabaseBackupNotFound
// （恢复 API 的按 ID 寻址面）。
func (s *Store) GetDatabaseBackup(ctx context.Context, id string) (DatabaseBackup, error) {
	const q = `SELECT ` + dbBackupScanCols + ` FROM db_backups WHERE id = ?`
	return scanDatabaseBackup(s.db.QueryRowContext(ctx, q, id))
}

// ListDatabaseBackups 按库实例倒序列出备份台账（created_at 降序；limit
// ≤0 回落 20）。Console 库详情备份列表与恢复目标选择的数据源。
func (s *Store) ListDatabaseBackups(ctx context.Context, dbID string, limit int) ([]DatabaseBackup, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT ` + dbBackupScanCols + ` FROM db_backups WHERE db_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, dbID, limit)
	if err != nil {
		return nil, fmt.Errorf("state: query database backups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DatabaseBackup
	for rows.Next() {
		b, err := scanDatabaseBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate database backups: %w", err)
	}
	return out, nil
}

// UpdateDatabaseBackupVerifyStatus 置位回读校验状态（Verify 适配器回读后
// 调用；失败原因摘要随 error 列落账，成功路径 error 清空）。返回被更新行
// 的存在性：行不存在返回 ErrDatabaseBackupNotFound（幂等窗口显式化）。
func (s *Store) UpdateDatabaseBackupVerifyStatus(ctx context.Context, id string, status DatabaseVerifyStatus, verifyError string) error {
	switch status {
	case DatabaseVerifyUnverified, DatabaseVerifyVerified, DatabaseVerifyFailed:
	default:
		return fmt.Errorf("state: database backup verify status %q not in {unverified, verified, failed}", status)
	}
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE db_backups SET verify_status = ?, error = ? WHERE id = ?`,
			string(status), verifyError, id)
		if err != nil {
			return fmt.Errorf("state: update database backup verify status %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read database backup verify count: %w", err)
		}
		if n == 0 {
			return ErrDatabaseBackupNotFound
		}
		return nil
	})
}

// GetDatabaseBackupBySnapshot 按 (库实例, restic snapshot) 取备份行（恢复
// 受理的快照归属守卫——只重放本实例台账内的快照，跨实例误指在此拦下）。
// 不存在返回 ErrDatabaseBackupNotFound。
func (s *Store) GetDatabaseBackupBySnapshot(ctx context.Context, dbID, snapshot string) (DatabaseBackup, error) {
	const q = `SELECT ` + dbBackupScanCols + ` FROM db_backups WHERE db_id = ? AND restic_snapshot = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`
	return scanDatabaseBackup(s.db.QueryRowContext(ctx, q, dbID, snapshot))
}

// PruneDatabaseBackups 保留对齐的台账镜像（S5 备份编排尾部）：删除该实例
// 台账中最旧的行、保留最新 keep 条（created_at 降序锚定）。返回删除行数
// （0 = 无可删）。keep ≤ 0 不删（调用方回落平台缺省）。
func (s *Store) PruneDatabaseBackups(ctx context.Context, dbID string, keep int) (int64, error) {
	if keep <= 0 {
		return 0, fmt.Errorf("state: prune database backups: keep %d must be positive", keep)
	}
	var removed int64
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM db_backups WHERE db_id = ? AND id NOT IN (
			SELECT id FROM db_backups WHERE db_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)`,
			dbID, dbID, keep)
		if err != nil {
			return fmt.Errorf("state: prune database backups %s: %w", dbID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read database backup prune count %s: %w", dbID, err)
		}
		removed = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// dbBackupScanCols 是备份台账行查询列清单（新增列只加在此与扫描函数）。
const dbBackupScanCols = `id, db_id, kind, restic_snapshot, size_bytes, verify_status, error, created_at`

// scanDatabaseBackup 从单行构造 DatabaseBackup。
func scanDatabaseBackup(row interface{ Scan(dest ...any) error }) (DatabaseBackup, error) {
	var b DatabaseBackup
	var kind, verify string
	var created int64
	if err := row.Scan(&b.ID, &b.DatabaseID, &kind, &b.ResticSnapshot, &b.SizeBytes,
		&verify, &b.Error, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DatabaseBackup{}, ErrDatabaseBackupNotFound
		}
		return DatabaseBackup{}, fmt.Errorf("state: scan database backup: %w", err)
	}
	b.Kind = DatabaseBackupKind(kind)
	b.VerifyStatus = DatabaseVerifyStatus(verify)
	b.CreatedAt = time.Unix(0, created).UTC()
	return b, nil
}
