// Package appvariable 是 AppVariable 聚合 repo（CONTEXT.md Variable 词条：
// App 级平台变量——三级合成的顶层，冻结咽喉合成进 AppSpec，同键覆盖
// 源声明 env 与 SharedVariable，ADR-0072）。值是明文非敏感面（敏感值走
// internal/state/secret 的 age 信封）；软删 tombstone 与
// shared_variables 同款（聚合镜像先例）。
package appvariable

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// AppVariable 是聚合行。
type AppVariable struct {
	ID        string
	Seq       int64
	AppID     string
	Name      string
	Value     string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (v *AppVariable) Deleted() bool { return v.DeletedAt != "" }

// Repo 是 AppVariable 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Upsert 写入（同名活跃行覆盖值；tombstone 行复活——shared_variables
// 同款，唯一索引只拦活跃行）。
func (r *Repo) Upsert(ctx context.Context, run state.Runner, v *AppVariable) error {
	now := state.FormatTime(r.clock.Now())
	if existing, err := r.GetByName(ctx, run, v.AppID, v.Name); err == nil {
		_, err := run.ExecContext(ctx, `
			UPDATE app_variables SET value = ?, updated_at = ?, deleted_at = ''
			WHERE id = ?`,
			v.Value, now, existing.ID)
		v.ID, v.CreatedAt = existing.ID, existing.CreatedAt
		v.UpdatedAt = now
		v.DeletedAt = ""
		return err
	}
	v.CreatedAt, v.UpdatedAt = now, now
	seq, err := state.NextSeq(ctx, run, "app_variables")
	if err != nil {
		return err
	}
	v.Seq = seq
	if v.ID == "" {
		v.ID = entityid.Join("appvar", seq)
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO app_variables (id, seq, app_id, name, value, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
		v.ID, v.Seq, v.AppID, v.Name, v.Value, v.CreatedAt, v.UpdatedAt)
	// 并发同名 PUT 交错撞部分唯一索引时归一为 ErrAlreadyExists——secrets/
	// shared_variables 同款两步形态。
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: app variable %q already exists in app", state.ErrAlreadyExists, v.Name)
	}
	return err
}

// GetByName 读活跃行（回显与配额计数的读面）。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, appID, name string) (*AppVariable, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, app_id, name, value, created_at, updated_at, deleted_at
		FROM app_variables WHERE app_id = ? AND name = ? AND deleted_at = ''`,
		appID, name)
	return scanVariable(row.Scan)
}

// ListActive 返回 App 内全部活跃行（冻结咽喉合成装载面 + 配额计数面）。
func (r *Repo) ListActive(ctx context.Context, run state.Runner, appID string) ([]AppVariable, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, seq, app_id, name, value, created_at, updated_at, deleted_at
		FROM app_variables WHERE app_id = ? AND deleted_at = ''
		ORDER BY name ASC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []AppVariable
	for rows.Next() {
		v, err := scanVariable(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// ListPage 分页读（ADR-0026 after_* + limit 惯例；游标轴 = App 内 name
// 字典序升序）。limit<=0 或 >200 回落/钳制缺省 50（与 shared_variables
// 同款）。
func (r *Repo) ListPage(ctx context.Context, run state.Runner, appID, afterName string, limit int) ([]AppVariable, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT id, seq, app_id, name, value, created_at, updated_at, deleted_at
		FROM app_variables WHERE app_id = ? AND deleted_at = ''`
	args := []any{appID}
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
	var out []AppVariable
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
// 上抛——shared_variables 同款）。
func (r *Repo) SoftDelete(ctx context.Context, run state.Runner, appID, name string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE app_variables SET deleted_at = ?, updated_at = ?
		WHERE app_id = ? AND name = ? AND deleted_at = ''`, now, now, appID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := r.GetByName(ctx, run, appID, name)
		return err
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

func scanVariable(scan func(dest ...any) error) (*AppVariable, error) {
	var v AppVariable
	if err := scan(&v.ID, &v.Seq, &v.AppID, &v.Name, &v.Value, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &v, nil
}
