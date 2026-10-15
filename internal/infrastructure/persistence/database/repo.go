// Package database 是 Database 聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/data/database（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain database.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain database.Repo）。
func New(clock state.Clock) database.Repo { return &repo{clock: clock} }

// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, run state.Runner, d *database.Database) error {
	seq, err := persistence.NextSeq(ctx, run, "databases")
	if err != nil {
		return err
	}
	d.Seq = seq
	if d.ID == "" {
		d.ID = entityid.Join(d.Name, seq)
	}
	now := state.FormatTime(r.clock.Now())
	d.CreatedAt, d.UpdatedAt, d.Status = now, now, database.StatusPending
	d.Generation, d.SpecFingerprint = 0, ""
	_, err = run.ExecContext(ctx, `
		INSERT INTO databases (id, seq, project_id, name, engine, credentials_ref,
			generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?, ?, ?, '')`,
		d.ID, d.Seq, d.ProjectID, d.Name, d.Engine, d.CredentialsRef,
		d.BackupIntervalSecs, d.BackupRetentionSecs, d.Status, d.CreatedAt, d.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: database name %q already exists in project", state.ErrAlreadyExists, d.Name)
	}
	return err
}

// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*database.Database, error) {
	row := run.QueryRowContext(ctx, selectCols+` WHERE id = ? AND deleted_at = ''`, id)
	return scanDatabase(row.Scan)
}

// GetByName 在 Project 内按名读活跃行。
func (r *repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*database.Database, error) {
	row := run.QueryRowContext(ctx, selectCols+`
		WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanDatabase(row.Scan)
}

// ListByProject 新→旧分页（ADR-0026 after_* + limit；游标 = seq 创建序）。
func (r *repo) ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]database.Database, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + ` WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterSeq > 0 {
		q += ` AND seq < ?`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// List 返回全部活跃行（收敛环的枚举面；按 id 升序稳定遍历）。
func (r *repo) List(ctx context.Context, run state.Runner) ([]database.Database, error) {
	return r.query(ctx, run, selectCols+` WHERE deleted_at = '' ORDER BY seq`)
}

// ListRestorePending 返回恢复挂起中的活跃行（备份环恢复面的枚举面；
// 挂起列在场即恢复在途，ADR-0039 决策 6）。
func (r *repo) ListRestorePending(ctx context.Context, run state.Runner) ([]database.Database, error) {
	return r.query(ctx, run,
		selectCols+` WHERE deleted_at = '' AND restore_from_backup != '' ORDER BY seq`)
}

// CountByProject 返回 Project 内活跃行数（受理位配额口径）。
func (r *repo) CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error) {
	var n int
	err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM databases WHERE project_id = ? AND deleted_at = ''`, projectID).Scan(&n)
	if err != nil {
		return 0, persistence.MapScanErr(err)
	}
	return n, nil
}

// EnsureGeneration 是收敛环的下发编号口（ADR-0029 决策 3）：投影指纹与
// 行上指纹一致 → 返回现行 gen（幂等重放同号，不触发载体滚动）；变化 →
// gen+1 与新指纹同事务落行并返回新号（Ensure 失败不回退——下一拍指纹
// 已一致，同号干净重试）。已删/不存在返回 ErrNotFound（收敛环跳过）。
func (r *repo) EnsureGeneration(ctx context.Context, run state.Runner, id, fingerprint string) (uint64, error) {
	now := state.FormatTime(r.clock.Now())
	var gen uint64
	err := run.QueryRowContext(ctx, `
		UPDATE databases SET generation = generation + 1, spec_fingerprint = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND spec_fingerprint != ?
		RETURNING generation`, fingerprint, now, id, fingerprint).Scan(&gen)
	if err == nil {
		return gen, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		// 指纹一致（或行不存在）：一致 → 回现行 gen（幂等重放同号）；
		// 不存在 → ErrNotFound（收敛环跳过）。
		row := run.QueryRowContext(ctx,
			`SELECT generation FROM databases WHERE id = ? AND deleted_at = ''`, id)
		if serr := row.Scan(&gen); serr != nil {
			return 0, persistence.MapScanErr(serr)
		}
		return gen, nil
	}
	return 0, persistence.MapScanErr(err)
}

// SetStatus 落观测状态（收敛环从观测缓存推进；WHERE 带 status != ? 守卫
// ——同值写入不迁 updated_at）。命中 0 行静默返回（观测迟到于删除）。
func (r *repo) SetStatus(ctx context.Context, run state.Runner, id, status string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE databases SET status = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND status != ?`, status, now, id, status)
	return err
}

// SoftDelete 落 tombstone（幂等口径与 Get 一致：已删 = 不存在 → 再删
// ErrNotFound）。
func (r *repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE databases SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, now, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// SetLastBackupAt 落调度锚（成功 Backup 的完成时刻；幂等写）。
func (r *repo) SetLastBackupAt(ctx context.Context, run state.Runner, id, at string) error {
	_, err := run.ExecContext(ctx,
		`UPDATE databases SET last_backup_at = ? WHERE id = ?`, at, id)
	return err
}

// SetRestorePending 落恢复挂起（API 创建面消费：restore_from_backup 指向
// 一颗成功备份行；备份环执行后清位）。
func (r *repo) SetRestorePending(ctx context.Context, run state.Runner, id, backupID string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE databases SET restore_from_backup = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, backupID, now, id)
	return err
}

// SetRestoreError 落恢复失败事实（挂起清位 + error 留痕；半恢复态重试
// 不可幂等，诚实留给用户重建——ADR-0039 决策 6）。
func (r *repo) SetRestoreError(ctx context.Context, run state.Runner, id, errMsg string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE databases SET restore_from_backup = '', restore_error = ?, updated_at = ?
		WHERE id = ?`, errMsg, now, id)
	return err
}

// ClearRestoreSucceeded 清恢复挂起（成功路径；restore_error 不动——历史
// 事实保留）。
func (r *repo) ClearRestoreSucceeded(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE databases SET restore_from_backup = '', updated_at = ?
		WHERE id = ?`, now, id)
	return err
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, project_id, name, engine, credentials_ref,
		generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status,
		last_backup_at, restore_from_backup, restore_error, created_at, updated_at, deleted_at
	FROM databases`

func (r *repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]database.Database, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []database.Database
	for rows.Next() {
		d, err := scanDatabase(rows.Scan)
		if err != nil {
			return nil, persistence.MapScanErr(err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func scanDatabase(scan func(dest ...any) error) (*database.Database, error) {
	var d database.Database
	err := scan(&d.ID, &d.Seq, &d.ProjectID, &d.Name, &d.Engine, &d.CredentialsRef,
		&d.Generation, &d.SpecFingerprint, &d.BackupIntervalSecs, &d.BackupRetentionSecs, &d.Status,
		&d.LastBackupAt, &d.RestoreFromBackup, &d.RestoreError,
		&d.CreatedAt, &d.UpdatedAt, &d.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &d, nil
}
