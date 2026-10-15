// Package app 是 App 聚合 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/structure/app（ADR-0075 Repo 接口层），本包只持不导出实现与构造。
package app

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain app.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain app.Repo）。
func New(clock state.Clock) app.Repo { return &repo{clock: clock} }

// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
// ID 空则装配 "<name>-<seq>"；Seq 零值则事务内取 MAX+1（ADR-0070）。
func (r *repo) Create(ctx context.Context, run state.Runner, a *app.App) error {
	now := state.FormatTime(r.clock.Now())
	a.CreatedAt, a.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, run, "apps")
	if err != nil {
		return err
	}
	a.Seq = seq
	if a.ID == "" {
		a.ID = entityid.Join(a.Name, seq)
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO apps (id, seq, project_id, name, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		a.ID, a.Seq, a.ProjectID, a.Name, a.CreatedAt, a.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: app name %q already exists in project", state.ErrAlreadyExists, a.Name)
	}
	return err
}

// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在——
// 已删行对 Get/GetByName/List 同构隐藏，删除语义不在读面分叉）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*app.App, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE id = ? AND deleted_at = ''`, id)
	return scanApp(row)
}

// GetByName 在 Project 内按名读活跃行。
func (r *repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*app.App, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanApp(row)
}

// GetAnyByID 按 ID 读行（含 tombstone；ADR-0035 授权专用面——App 删除后
// ListDeployments/ListRevisions/ListBuilds 等审计型读面仍须过行级授权，
// 归属判定需要已删行的 project_id；不改变 Get 的活跃行语义）。
func (r *repo) GetAnyByID(ctx context.Context, run state.Runner, id string) (*app.App, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE id = ?`, id)
	return scanApp(row)
}

// ListByProject 返回 Project 内全部活跃 App（全量面——删除守卫与引擎
// 隔离扫描消费；API 分页读面走 ListByProjectPage）。
func (r *repo) ListByProject(ctx context.Context, run state.Runner, projectID string) ([]app.App, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE project_id = ? AND deleted_at = '' ORDER BY seq`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []app.App
	for rows.Next() {
		a, err := scanAppRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ListByProjectPage 是 ListByProject 的分页读面（ADR-0026 after_* + limit；
// 游标轴 = seq 创建序升序——既有响应序不变）。limit<=0 或 >200 回落/
// 钳制缺省 50。
func (r *repo) ListByProjectPage(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]app.App, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := `
		SELECT id, seq, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterSeq > 0 {
		q += ` AND seq > ?`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []app.App
	for rows.Next() {
		a, err := scanAppRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// CountByProject 返回 Project 内活跃 App 行数（ADR-0017 附录 A.1 配额
// 口径；受理位在事务内读）。
func (r *repo) CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error) {
	var n int
	err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM apps WHERE project_id = ? AND deleted_at = ''`, projectID).Scan(&n)
	if err != nil {
		return 0, persistence.MapScanErr(err)
	}
	return n, nil
}

// List 返回全部活跃 App（ADR-0022 启动基线重放的枚举面）。
func (r *repo) List(ctx context.Context, run state.Runner) ([]app.App, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE deleted_at = '' ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []app.App
	for rows.Next() {
		a, err := scanAppRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// SoftDelete 落 tombstone（tombstone 件）。命中 0 行（不存在或已删）返回
// ErrNotFound——与 Get 口径一致："已删"对调用方即"不存在"（再删 404）。
func (r *repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE apps SET deleted_at = ?, updated_at = ?
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

func scanApp(row *sql.Row) (*app.App, error) {
	var a app.App
	err := row.Scan(&a.ID, &a.Seq, &a.ProjectID, &a.Name, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &a, nil
}

func scanAppRow(rows *sql.Rows) (*app.App, error) {
	var a app.App
	if err := rows.Scan(&a.ID, &a.Seq, &a.ProjectID, &a.Name, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt); err != nil {
		return nil, err
	}
	return &a, nil
}
