// Package schedule 是 Schedule 聚合 repo 的 SQLite 实现：行类型、带时区
// cron 解释面与 Repo 接口住 domain/automation/schedule（ADR-0075 Repo
// 接口层），本包只持不导出实现与构造。
package schedule

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain schedule.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain schedule.Repo）。
func New(clock state.Clock) schedule.Repo { return &repo{clock: clock} }

// Create 落一行（活跃名唯一索引防项目内重名 → ErrAlreadyExists）。
func (r *repo) Create(ctx context.Context, run state.Runner, s *schedule.Schedule) error {
	seq, err := persistence.NextSeq(ctx, run, "schedules")
	if err != nil {
		return err
	}
	s.Seq = seq
	if s.ID == "" {
		s.ID = entityid.Join(s.Name, seq)
	}
	now := state.FormatTime(r.clock.Now())
	s.CreatedAt, s.UpdatedAt = now, now
	_, err = run.ExecContext(ctx, `
		INSERT INTO schedules (id, seq, project_id, name, state, cron_expr, timezone, spec,
			 next_fire_at, last_task_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Seq, s.ProjectID, s.Name, string(s.State), s.CronExpr, s.Timezone, s.Spec,
		s.NextFireAt, s.LastTaskID, s.CreatedAt, s.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: an active schedule named %q already exists in project %s", state.ErrAlreadyExists, s.Name, s.ProjectID)
	}
	return err
}

// Get 按 ID 直读（tombstone 行照常返回——终态事实可见）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*schedule.Schedule, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanSchedule(row.Scan)
}

// GetByName 按项目内名直读（活跃名唯一；无 → ErrNotFound）。
func (r *repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*schedule.Schedule, error) {
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE project_id = ? AND name = ? AND state != 'deleted'", projectID, name)
	return scanSchedule(row.Scan)
}

// ListByProject 返回项目内 Schedule（新→旧 + after 游标；ADR-0026 after_* +
// limit 惯例——游标 = seq 创建序）。
func (r *repo) ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]schedule.Schedule, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + " WHERE project_id = ?"
	args := []any{projectID}
	if afterSeq > 0 {
		q += " AND seq < ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// ListDriving 返回全部活跃行（到期判定由调用方按墙钟解析比较）。
func (r *repo) ListDriving(ctx context.Context, run state.Runner) ([]schedule.Schedule, error) {
	return r.query(ctx, run, selectCols+" WHERE state = 'active' ORDER BY seq")
}

// Fire 是到期拍/跳过后推进 next_fire_at 的 CAS：仅当行活跃且 next_fire_at
// 仍为 from 时落 next 与 last_task_id。前置不符 → ErrConflict（并发已拍/
// 已删——与 Task 落行同事务时整体回滚，双发防御纵深）；行不存在 →
// ErrNotFound。
func (r *repo) Fire(ctx context.Context, run state.Runner, id, from, next, lastTaskID string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE schedules SET next_fire_at = ?, last_task_id = ?, updated_at = ?
		WHERE id = ? AND state = 'active' AND next_fire_at = ?`,
		next, lastTaskID, state.FormatTime(r.clock.Now()), id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := r.Get(ctx, run, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w: schedule %s next_fire_at changed concurrently", state.ErrConflict, id)
	}
	return nil
}

// RecordFire 是手动触发的记账面：last_task_id 推进、next_fire_at 不动
// （cron 节奏不被手动拍打乱）。fromLastTaskID 是 CAS 锚（调用方读行时的
// 旧值，B12 P3-4）：并发双拍时输家的锚失配 → ErrConflict——与 Task 落行
// 同事务整体回滚，双铸防御。行不活跃 → ErrConflict；不存在 → ErrNotFound。
func (r *repo) RecordFire(ctx context.Context, run state.Runner, id, fromLastTaskID, lastTaskID string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE schedules SET last_task_id = ?, updated_at = ?
		WHERE id = ? AND state = 'active' AND last_task_id = ?`,
		lastTaskID, state.FormatTime(r.clock.Now()), id, fromLastTaskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := r.Get(ctx, run, id)
		if gerr != nil {
			return gerr
		}
		if cur.State != schedule.StateActive {
			return fmt.Errorf("%w: schedule %s is not active", state.ErrConflict, id)
		}
		return fmt.Errorf("%w: schedule %s last_task_id changed concurrently", state.ErrConflict, id)
	}
	return nil
}

// Transit 是状态 CAS（active → deleted 的 tombstone 面；单前置态——
// Schedule 无中间态迁移）。
func (r *repo) Transit(ctx context.Context, run state.Runner, id string, from, to schedule.State) error {
	res, err := run.ExecContext(ctx, `
		UPDATE schedules SET state = ?, updated_at = ?
		`+persistence.TransitWhere,
		string(to), state.FormatTime(r.clock.Now()), id, string(from))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := r.Get(ctx, run, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w: schedule %s is not %s", state.ErrConflict, id, from)
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, project_id, name, state, cron_expr, timezone, spec,
	       next_fire_at, last_task_id, created_at, updated_at
	FROM schedules`

func (r *repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]schedule.Schedule, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []schedule.Schedule
	for rows.Next() {
		s, err := scanSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func scanSchedule(scan func(dest ...any) error) (*schedule.Schedule, error) {
	var s schedule.Schedule
	var stateStr string
	err := scan(&s.ID, &s.Seq, &s.ProjectID, &s.Name, &stateStr, &s.CronExpr, &s.Timezone, &s.Spec,
		&s.NextFireAt, &s.LastTaskID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	s.State = schedule.State(stateStr)
	return &s, nil
}
