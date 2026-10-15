// Package backup 是 Backup 台账聚合 repo 的 SQLite 实现：行类型、对象键
// 公式与 Repo 接口住 domain/data/backup（ADR-0075 Repo 接口层），本包只
// 持不导出实现与构造。
package backup

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// parseCreatedAt 解析行上时间（FormatTime 的读回面，RFC3339）。
func parseCreatedAt(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// repo 是 domain backup.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain backup.Repo）。
func New(clock state.Clock) backup.Repo { return &repo{clock: clock} }

const selectCols = `SELECT id, seq, project_id, database_id, engine, object_key, digest,
	size_bytes, retention_secs, status, error, started_at, finished_at, created_at FROM backups`

func scanBackup(scan func(...any) error) (*backup.Backup, error) {
	var b backup.Backup
	err := scan(&b.ID, &b.Seq, &b.ProjectID, &b.DatabaseID, &b.Engine, &b.ObjectKey, &b.Digest,
		&b.SizeBytes, &b.RetentionSecs, &b.Status, &b.Error, &b.StartedAt, &b.FinishedAt, &b.CreatedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &b, nil
}

// Create 落一行 pending（调度与手动触发共用；object_key 执行成功后回填）。
func (r *repo) Create(ctx context.Context, run state.Runner, b *backup.Backup) error {
	seq, err := persistence.NextSeq(ctx, run, "backups")
	if err != nil {
		return err
	}
	b.Seq = seq
	if b.ID == "" {
		b.ID = entityid.Derive(b.DatabaseID, "b", seq)
	}
	now := state.FormatTime(r.clock.Now())
	b.CreatedAt, b.Status = now, backup.StatusPending
	_, err = run.ExecContext(ctx, `
		INSERT INTO backups (id, seq, project_id, database_id, engine, object_key, digest,
			size_bytes, retention_secs, status, error, started_at, finished_at, created_at)
		VALUES (?, ?, ?, ?, ?, '', '', 0, ?, 'pending', '', '', '', ?)`,
		b.ID, b.Seq, b.ProjectID, b.DatabaseID, b.Engine, b.RetentionSecs, b.CreatedAt)
	return err
}

// Get 按 ID 读一行。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*backup.Backup, error) {
	row := run.QueryRowContext(ctx, selectCols+` WHERE id = ?`, id)
	return scanBackup(row.Scan)
}

// ListByDatabase 新→旧分页（ADR-0026 after_* + limit 惯例；含终态与在途）。
func (r *repo) ListByDatabase(ctx context.Context, run state.Runner, databaseID string, afterSeq int64, limit int) ([]backup.Backup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := selectCols + ` WHERE database_id = ?`
	args := []any{databaseID}
	if afterSeq > 0 {
		q += ` AND seq < ?`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// OldestPending 返回最早的一行 pending（执行器单飞取件；无在途即空）。
// in-flight 排他：同库存在 running 行时返回 ok=false（串行语义在环层，
// 本查询兜底防同库双在途）。
func (r *repo) OldestPending(ctx context.Context, run state.Runner) (*backup.Backup, bool, error) {
	var n int
	if err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM backups WHERE status = 'running'`).Scan(&n); err != nil {
		return nil, false, err
	}
	if n > 0 {
		return nil, false, nil
	}
	row := run.QueryRowContext(ctx,
		selectCols+` WHERE status = 'pending' ORDER BY seq LIMIT 1`)
	b, err := scanBackup(row.Scan)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}

// HasActiveFor 报告库是否存在在途行（pending|running——调度面防同库双行）。
func (r *repo) HasActiveFor(ctx context.Context, run state.Runner, databaseID string) (bool, error) {
	var n int
	if err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM backups WHERE database_id = ? AND status IN ('pending','running')`,
		databaseID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// LatestFailedFor 返回该库最新一行失败（退避锚——失败不推进 last_backup_at，
// 重试节奏由本行 FinishedAt 承载；id 是 ULID 时序，走 (database_id, id)
// 索引序即时间序。无失败行即 ok=false）。
func (r *repo) LatestFailedFor(ctx context.Context, run state.Runner, databaseID string) (*backup.Backup, bool, error) {
	row := run.QueryRowContext(ctx,
		selectCols+` WHERE database_id = ? AND status = 'failed' ORDER BY seq DESC LIMIT 1`, databaseID)
	b, err := scanBackup(row.Scan)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}

// DueForFailedRowSweep 返回失败行保留窗外的行（失败行无对象产物，仅删行
// ——错误文本的诊断价值短命，48h 窗后由保留滚动同环清扫；created_at 升序
// 限量分拍，DueForPrune 同款形态）。
func (r *repo) DueForFailedRowSweep(ctx context.Context, run state.Runner, now time.Time, retention time.Duration, limit int) ([]backup.Backup, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cutoff := now.Add(-retention).UTC().Format(time.RFC3339)
	return r.query(ctx, run,
		selectCols+` WHERE status = 'failed' AND created_at < ? ORDER BY created_at LIMIT ?`, cutoff, limit)
}

// MarkRunning 摘件落执行态（CAS：仅 pending 行命中）。
func (r *repo) MarkRunning(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx,
		`UPDATE backups SET status = 'running', started_at = ? WHERE id = ? AND status = 'pending'`, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrConflict
	}
	return nil
}

// FinishSucceeded 回填成功（ObjectStore 回执三元组 + 终态；CAS：仅
// running 行命中——终态是事实，启动清扫 SweepInterrupted 落 failed 后的
// 迟到成功回执按 ErrConflict 拒绝，不得覆写）。
func (r *repo) FinishSucceeded(ctx context.Context, run state.Runner, id, objectKey, digest string, size int64) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE backups SET status = 'succeeded', object_key = ?, digest = ?, size_bytes = ?,
			finished_at = ? WHERE id = ? AND status = 'running'`, objectKey, digest, size, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrConflict
	}
	return nil
}

// FinishFailed 落失败终态（error 用户可见文本英文；CAS：pending/running
// 行命中——失败回执在 MarkRunning 前后都合法，唯终态不可覆写，同
// FinishSucceeded 口径）。
func (r *repo) FinishFailed(ctx context.Context, run state.Runner, id, errMsg string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE backups SET status = 'failed', error = ?, finished_at = ?
		WHERE id = ? AND status IN ('pending', 'running')`, errMsg, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrConflict
	}
	return nil
}

// SweepInterrupted 把重启打断的 running 行落 failed（引擎启动清扫；返回
// 清扫行数供日志）。
func (r *repo) SweepInterrupted(ctx context.Context, run state.Runner) (int64, error) {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE backups SET status = 'failed', error = 'interrupted by platform restart',
			finished_at = ? WHERE status = 'running'`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DueForPrune 返回保留窗外的行（行驱动保留滚动，ADR-0039 决策 7）：按
// created_at 升序取前 lookahead 行、Go 侧按行上 retention 快照过滤（窗在
// 行上逐行不同，SQL 单 cutoff 表达不了；升序保证到期行必在前沿，清扫
// 跟得上即 O(到期行+前瞻)，积压窗拖不垮环——限量分拍）。
func (r *repo) DueForPrune(ctx context.Context, run state.Runner, now time.Time, lookahead int) ([]backup.Backup, error) {
	if lookahead <= 0 || lookahead > 500 {
		lookahead = 100
	}
	rows, err := r.query(ctx, run,
		selectCols+` ORDER BY created_at LIMIT ?`, lookahead)
	if err != nil {
		return nil, err
	}
	var out []backup.Backup
	for _, b := range rows {
		created, err := parseCreatedAt(b.CreatedAt)
		if err != nil {
			continue // 时间面异常的行不删（诚实跳过，等人工分诊）
		}
		if now.Sub(created) > time.Duration(b.RetentionSecs)*time.Second {
			out = append(out, b)
		}
	}
	return out, nil
}

// Delete 删一行（保留滚动收口；与对象删除由调用方成对执行）。
func (r *repo) Delete(ctx context.Context, run state.Runner, id string) error {
	_, err := run.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, id)
	return err
}

func (r *repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]backup.Backup, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面（database repo 同款）
	var out []backup.Backup
	for rows.Next() {
		b, err := scanBackup(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}
