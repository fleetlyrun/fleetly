// Package backup 是 Backup 台账聚合（F2.2，ADR-0039 决策 6）：行先落
// （pending）再执行，成功回填 ObjectStore 回执（key/digest/size——restore
// verify 的校验锚）。保留窗创建时刻冻结在行上（retention_secs 快照）。
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// parseCreatedAt 解析行上时间（FormatTime 的读回面，RFC3339）。
func parseCreatedAt(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// 状态值域（migration 00019 注释同源）。
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Backup 是一次备份的台账行。
type Backup struct {
	ID            string
	ProjectID     string
	DatabaseID    string
	Engine        string
	ObjectKey     string
	Digest        string
	SizeBytes     int64
	RetentionSecs int64
	Status        string
	Error         string
	StartedAt     string
	FinishedAt    string
	CreatedAt     string
}

// Repo 是 Backup 台账存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

const selectCols = `SELECT id, project_id, database_id, engine, object_key, digest,
	size_bytes, retention_secs, status, error, started_at, finished_at, created_at FROM backups`

func scanBackup(scan func(...any) error) (*Backup, error) {
	var b Backup
	err := scan(&b.ID, &b.ProjectID, &b.DatabaseID, &b.Engine, &b.ObjectKey, &b.Digest,
		&b.SizeBytes, &b.RetentionSecs, &b.Status, &b.Error, &b.StartedAt, &b.FinishedAt, &b.CreatedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	return &b, nil
}

// Create 落一行 pending（调度与手动触发共用；object_key 执行成功后回填）。
func (r *Repo) Create(ctx context.Context, run state.Runner, b *Backup) error {
	now := state.FormatTime(r.clock.Now())
	b.CreatedAt, b.Status = now, StatusPending
	_, err := run.ExecContext(ctx, `
		INSERT INTO backups (id, project_id, database_id, engine, object_key, digest,
			size_bytes, retention_secs, status, error, started_at, finished_at, created_at)
		VALUES (?, ?, ?, ?, '', '', 0, ?, 'pending', '', '', '', ?)`,
		b.ID, b.ProjectID, b.DatabaseID, b.Engine, b.RetentionSecs, b.CreatedAt)
	return err
}

// Get 按 ID 读一行。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Backup, error) {
	row := run.QueryRowContext(ctx, selectCols+` WHERE id = ?`, id)
	return scanBackup(row.Scan)
}

// ListByDatabase 新→旧分页（ADR-0026 after_* + limit 惯例；含终态与在途）。
func (r *Repo) ListByDatabase(ctx context.Context, run state.Runner, databaseID, afterID string, limit int) ([]Backup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := selectCols + ` WHERE database_id = ?`
	args := []any{databaseID}
	if afterID != "" {
		q += ` AND id < ?`
		args = append(args, afterID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// OldestPending 返回最早的一行 pending（执行器单飞取件；无在途即空）。
// in-flight 排他：同库存在 running 行时返回 ok=false（串行语义在环层，
// 本查询兜底防同库双在途）。
func (r *Repo) OldestPending(ctx context.Context, run state.Runner) (*Backup, bool, error) {
	var n int
	if err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM backups WHERE status = 'running'`).Scan(&n); err != nil {
		return nil, false, err
	}
	if n > 0 {
		return nil, false, nil
	}
	row := run.QueryRowContext(ctx,
		selectCols+` WHERE status = 'pending' ORDER BY id LIMIT 1`)
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
func (r *Repo) HasActiveFor(ctx context.Context, run state.Runner, databaseID string) (bool, error) {
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
func (r *Repo) LatestFailedFor(ctx context.Context, run state.Runner, databaseID string) (*Backup, bool, error) {
	row := run.QueryRowContext(ctx,
		selectCols+` WHERE database_id = ? AND status = 'failed' ORDER BY id DESC LIMIT 1`, databaseID)
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
func (r *Repo) DueForFailedRowSweep(ctx context.Context, run state.Runner, now time.Time, retention time.Duration, limit int) ([]Backup, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cutoff := now.Add(-retention).UTC().Format(time.RFC3339)
	return r.query(ctx, run,
		selectCols+` WHERE status = 'failed' AND created_at < ? ORDER BY created_at LIMIT ?`, cutoff, limit)
}

// MarkRunning 摘件落执行态（CAS：仅 pending 行命中）。
func (r *Repo) MarkRunning(ctx context.Context, run state.Runner, id string) error {
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

// FinishSucceeded 回填成功（ObjectStore 回执三元组 + 终态）。
func (r *Repo) FinishSucceeded(ctx context.Context, run state.Runner, id, objectKey, digest string, size int64) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE backups SET status = 'succeeded', object_key = ?, digest = ?, size_bytes = ?,
			finished_at = ? WHERE id = ?`, objectKey, digest, size, now, id)
	return err
}

// FinishFailed 落失败终态（error 用户可见文本英文）。
func (r *Repo) FinishFailed(ctx context.Context, run state.Runner, id, errMsg string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE backups SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`, errMsg, now, id)
	return err
}

// SweepInterrupted 把重启打断的 running 行落 failed（引擎启动清扫；返回
// 清扫行数供日志）。
func (r *Repo) SweepInterrupted(ctx context.Context, run state.Runner) (int64, error) {
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
func (r *Repo) DueForPrune(ctx context.Context, run state.Runner, now time.Time, lookahead int) ([]Backup, error) {
	if lookahead <= 0 || lookahead > 500 {
		lookahead = 100
	}
	rows, err := r.query(ctx, run,
		selectCols+` ORDER BY created_at LIMIT ?`, lookahead)
	if err != nil {
		return nil, err
	}
	var out []Backup
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
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	_, err := run.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, id)
	return err
}

func (r *Repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]Backup, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面（database repo 同款）
	var out []Backup
	for rows.Next() {
		b, err := scanBackup(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// KeyMint 铸对象键（backups/<projectID>/<databaseID>/<ts>-<id>；无冒号
// 紧凑 UTC 时间戳——Windows 控制面纪律，ADR-0039 决策 6）。
func KeyMint(projectID, databaseID, backupID string, now time.Time) string {
	return fmt.Sprintf("backups/%s/%s/%s-%s", projectID, databaseID, now.UTC().Format("20060102T150405Z"), backupID)
}
