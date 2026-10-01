// Package role 是 Role 聚合 repo（CONTEXT.md Role 词条：命名的 Scope 集合，
// 在 Team 内授予 User 或 Token）。scopes 列是 JSON 数组（colon 形态）。
package role

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Role 是聚合行（Builtin 行 team_id 为空——平台级模板）。
type Role struct {
	ID        string
	TeamID    string
	Name      string
	Builtin   bool
	Scopes    []string
	CreatedAt string
}

// Repo 是 Role 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同 Team 内同名冲突返回 state.ErrAlreadyExists。
func (r *Repo) Create(ctx context.Context, run state.Runner, ro *Role) error {
	ro.CreatedAt = state.FormatTime(r.clock.Now())
	scopes, err := json.Marshal(ro.Scopes)
	if err != nil {
		return fmt.Errorf("role: scopes json: %w", err)
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO roles (id, team_id, name, builtin, scopes, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		ro.ID, ro.TeamID, ro.Name, boolInt(ro.Builtin), string(scopes), ro.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: role name %q already exists", state.ErrAlreadyExists, ro.Name)
	}
	return err
}

// UpsertBuiltin 落/刷内置角色（固定 slug ID；scopes 由代码单一源刷新——
// 词表演进时启动即同步，不待人工迁移）。
func (r *Repo) UpsertBuiltin(ctx context.Context, run state.Runner, ro *Role) error {
	ro.CreatedAt = state.FormatTime(r.clock.Now())
	scopes, err := json.Marshal(ro.Scopes)
	if err != nil {
		return fmt.Errorf("role: scopes json: %w", err)
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO roles (id, team_id, name, builtin, scopes, created_at)
		VALUES (?, '', ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET scopes = excluded.scopes`,
		ro.ID, ro.Name, string(scopes), ro.CreatedAt)
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Role, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, team_id, name, builtin, scopes, created_at
		FROM roles WHERE id = ?`, id)
	return scanRole(row.Scan)
}

// List 返回全部 Role（内置在前、ID 序稳定）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Role, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, team_id, name, builtin, scopes, created_at
		FROM roles ORDER BY builtin DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Role
	for rows.Next() {
		ro, err := scanRole(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *ro)
	}
	return out, rows.Err()
}

// Delete 按 ID 删行。内置角色拒绝删（代码单一源）；仍被 Token/membership
// 引用时 FK RESTRICT 归一为 state.ErrConflict。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	ro, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if ro.Builtin {
		return fmt.Errorf("%w: builtin role %q cannot be deleted", state.ErrConflict, ro.Name)
	}
	res, err := run.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	if err != nil {
		if state.IsUniqueViolation(err) || state.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: role %s is still referenced by tokens or users", state.ErrConflict, id)
		}
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

func scanRole(scan func(dest ...any) error) (*Role, error) {
	var ro Role
	var builtin int
	var scopes string
	if err := scan(&ro.ID, &ro.TeamID, &ro.Name, &builtin, &scopes, &ro.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	ro.Builtin = builtin != 0
	if scopes != "" {
		if err := json.Unmarshal([]byte(scopes), &ro.Scopes); err != nil {
			return nil, fmt.Errorf("role: scopes json: %w", err)
		}
	}
	return &ro, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
