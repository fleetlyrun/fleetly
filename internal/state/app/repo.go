// Package app 是 App 聚合 repo（CONTEXT.md App 词条：长运行可部署单元，
// 由一个或多个 Process 组成；Process 形态住 Spec/Revision，App 行只承载
// 骨架与命名）。
package app

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// App 是聚合行（软删 tombstone）。
type App struct {
	ID        string
	ProjectID string
	Name      string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (a *App) Deleted() bool { return a.DeletedAt != "" }

// Repo 是 App 聚合存取（无门面：仅本聚合查询）。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
func (r *Repo) Create(ctx context.Context, run state.Runner, a *App) error {
	now := state.FormatTime(r.clock.Now())
	a.CreatedAt, a.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO apps (id, project_id, name, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, '')`,
		a.ID, a.ProjectID, a.Name, a.CreatedAt, a.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: app name %q already exists in project", state.ErrAlreadyExists, a.Name)
	}
	return err
}

// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在——
// 已删行对 Get/GetByName/List 同构隐藏，删除语义不在读面分叉）。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*App, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE id = ? AND deleted_at = ''`, id)
	return scanApp(row)
}

// GetByName 在 Project 内按名读活跃行。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*App, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanApp(row)
}

// GetAnyByID 按 ID 读行（含 tombstone；ADR-0035 授权专用面——App 删除后
// ListDeployments/ListRevisions/ListBuilds 等审计型读面仍须过行级授权，
// 归属判定需要已删行的 project_id；不改变 Get 的活跃行语义）。
func (r *Repo) GetAnyByID(ctx context.Context, run state.Runner, id string) (*App, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE id = ?`, id)
	return scanApp(row)
}

// ListByProject 返回 Project 内全部活跃 App（全量面——删除守卫与引擎
// 隔离扫描消费；API 分页读面走 ListByProjectPage）。
func (r *Repo) ListByProject(ctx context.Context, run state.Runner, projectID string) ([]App, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE project_id = ? AND deleted_at = '' ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []App
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
// 游标轴 = ULID 创建序升序——既有响应序不变）。limit<=0 或 >200 回落/
// 钳制缺省 50。
func (r *Repo) ListByProjectPage(ctx context.Context, run state.Runner, projectID, afterID string, limit int) ([]App, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT id, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterID != "" {
		q += ` AND id > ?`
		args = append(args, afterID)
	}
	q += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []App
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
func (r *Repo) CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error) {
	var n int
	err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM apps WHERE project_id = ? AND deleted_at = ''`, projectID).Scan(&n)
	if err != nil {
		return 0, state.MapScanErr(err)
	}
	return n, nil
}

// List 返回全部活跃 App（ADR-0022 启动基线重放的枚举面）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]App, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, created_at, updated_at, deleted_at
		FROM apps WHERE deleted_at = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []App
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
func (r *Repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
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

func scanApp(row *sql.Row) (*App, error) {
	var a App
	err := row.Scan(&a.ID, &a.ProjectID, &a.Name, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	return &a, nil
}

func scanAppRow(rows *sql.Rows) (*App, error) {
	var a App
	if err := rows.Scan(&a.ID, &a.ProjectID, &a.Name, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt); err != nil {
		return nil, err
	}
	return &a, nil
}
