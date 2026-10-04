// Package sharedvariable 是 SharedVariable 聚合 repo（CONTEXT.md Variable
// 词条：Project 级共享变量，归一化期合成进 AppSpec——Project 层在下、
// App 层 env 覆盖，ADR-0027/0043）。值是明文非敏感面（敏感值走
// internal/state/secret 的 age 信封）；软删 tombstone 与 secrets 同款。
package sharedvariable

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// SharedVariable 是聚合行。
type SharedVariable struct {
	ID        string
	ProjectID string
	Name      string
	Value     string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (v *SharedVariable) Deleted() bool { return v.DeletedAt != "" }

// Repo 是 SharedVariable 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Upsert 写入（同名活跃行覆盖值；tombstone 行复活——upsert 语义与
// secrets 同款，唯一索引只拦活跃行）。
func (r *Repo) Upsert(ctx context.Context, run state.Runner, v *SharedVariable) error {
	now := state.FormatTime(r.clock.Now())
	if existing, err := r.GetByName(ctx, run, v.ProjectID, v.Name); err == nil {
		_, err := run.ExecContext(ctx, `
			UPDATE shared_variables SET value = ?, updated_at = ?, deleted_at = ''
			WHERE id = ?`,
			v.Value, now, existing.ID)
		v.ID, v.CreatedAt = existing.ID, existing.CreatedAt
		v.UpdatedAt = now
		v.DeletedAt = ""
		return err
	}
	v.CreatedAt, v.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO shared_variables (id, project_id, name, value, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		v.ID, v.ProjectID, v.Name, v.Value, v.CreatedAt, v.UpdatedAt)
	return err
}

// GetByName 读活跃行（API 回显与受影响 App 计算的读面）。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*SharedVariable, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, value, created_at, updated_at, deleted_at
		FROM shared_variables WHERE project_id = ? AND name = ? AND deleted_at = ''`,
		projectID, name)
	return scanVariable(row.Scan)
}

// ListActive 返回项目内全部活跃行（归一化期合成装载面 + 配额计数面；
// 项目内共享变量受配额钳制，结果集有界）。
func (r *Repo) ListActive(ctx context.Context, run state.Runner, projectID string) ([]SharedVariable, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, value, created_at, updated_at, deleted_at
		FROM shared_variables WHERE project_id = ? AND deleted_at = ''
		ORDER BY name ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []SharedVariable
	for rows.Next() {
		v, err := scanVariable(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// ListPage 分页读（ADR-0026 after_* + limit 惯例；游标轴 = 项目内 name
// 字典序升序）。limit<=0 或 >200 回落/钳制缺省 50（与 secrets/configs
// 同款）。
func (r *Repo) ListPage(ctx context.Context, run state.Runner, projectID, afterName string, limit int) ([]SharedVariable, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT id, project_id, name, value, created_at, updated_at, deleted_at
		FROM shared_variables WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterName != "" {
		q += " AND name > ?"
		args = append(args, afterName)
	}
	q += " ORDER BY name ASC LIMIT ?"
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []SharedVariable
	for rows.Next() {
		v, err := scanVariable(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// SoftDelete 落 tombstone（幂等；删除不存在/已删的名按 ErrNotFound 诚实
// 上抛——secrets 同款）。
func (r *Repo) SoftDelete(ctx context.Context, run state.Runner, projectID, name string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE shared_variables SET deleted_at = ?, updated_at = ?
		WHERE project_id = ? AND name = ? AND deleted_at = ''`, now, now, projectID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := r.GetByName(ctx, run, projectID, name)
		return err
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

func scanVariable(scan func(dest ...any) error) (*SharedVariable, error) {
	var v SharedVariable
	if err := scan(&v.ID, &v.ProjectID, &v.Name, &v.Value, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &v, nil
}
