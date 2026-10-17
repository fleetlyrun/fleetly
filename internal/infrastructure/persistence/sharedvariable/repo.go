// Package sharedvariable 是 SharedVariable 聚合 repo 的 SQLite 实现：
// 行类型与 Repo 接口住 domain/structure/sharedvariable（ADR-0075 Repo
// 接口层），本包只持不导出实现与构造。
package sharedvariable

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain sharedvariable.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain sharedvariable.Repo）。
func New(clock state.Clock, run state.Runner) sharedvariable.Repo {
	return &repo{clock: clock, run: run}
}

// Upsert 写入（同名活跃行覆盖值；tombstone 行复活——upsert 语义与
// secrets 同款，唯一索引只拦活跃行）。
func (r *repo) Upsert(ctx context.Context, v *sharedvariable.SharedVariable) error {
	now := state.FormatTime(r.clock.Now())
	if existing, err := r.GetByName(ctx, v.ProjectID, v.Name); err == nil {
		_, err := r.run.ExecContext(ctx, `
			UPDATE shared_variables SET value = ?, updated_at = ?, deleted_at = ''
			WHERE id = ?`,
			v.Value, now, existing.ID)
		v.ID, v.CreatedAt = existing.ID, existing.CreatedAt
		v.UpdatedAt = now
		v.DeletedAt = ""
		return err
	}
	v.CreatedAt, v.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, r.run, "shared_variables")
	if err != nil {
		return err
	}
	v.Seq = seq
	if v.ID == "" {
		v.ID = entityid.Join("var", seq)
	}
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO shared_variables (id, seq, project_id, name, value, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
		v.ID, v.Seq, v.ProjectID, v.Name, v.Value, v.CreatedAt, v.UpdatedAt)
	// 并发同名 PUT 交错撞部分唯一索引时归一为 ErrAlreadyExists——secrets/
	// app_variables 同款两步形态。
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: shared variable %q already exists in project", state.ErrAlreadyExists, v.Name)
	}
	return err
}

// GetByName 读活跃行（API 回显与受影响 App 计算的读面）。
func (r *repo) GetByName(ctx context.Context, projectID, name string) (*sharedvariable.SharedVariable, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, value, created_at, updated_at, deleted_at
		FROM shared_variables WHERE project_id = ? AND name = ? AND deleted_at = ''`,
		projectID, name)
	return scanVariable(row.Scan)
}

// ListActive 返回项目内全部活跃行（归一化期合成装载面 + 配额计数面；
// 项目内共享变量受配额钳制，结果集有界）。
func (r *repo) ListActive(ctx context.Context, projectID string) ([]sharedvariable.SharedVariable, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, value, created_at, updated_at, deleted_at
		FROM shared_variables WHERE project_id = ? AND deleted_at = ''
		ORDER BY name ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []sharedvariable.SharedVariable
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
func (r *repo) ListPage(ctx context.Context, projectID, afterName string, limit int) ([]sharedvariable.SharedVariable, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := `
		SELECT id, seq, project_id, name, value, created_at, updated_at, deleted_at
		FROM shared_variables WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterName != "" {
		q += " AND name > ?"
		args = append(args, afterName)
	}
	q += " ORDER BY name ASC LIMIT ?"
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []sharedvariable.SharedVariable
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
func (r *repo) SoftDelete(ctx context.Context, projectID, name string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE shared_variables SET deleted_at = ?, updated_at = ?
		WHERE project_id = ? AND name = ? AND deleted_at = ''`, now, now, projectID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := r.GetByName(ctx, projectID, name)
		return err
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

func scanVariable(scan func(dest ...any) error) (*sharedvariable.SharedVariable, error) {
	var v sharedvariable.SharedVariable
	if err := scan(&v.ID, &v.Seq, &v.ProjectID, &v.Name, &v.Value, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &v, nil
}
