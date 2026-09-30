// Package user 是 User 聚合 repo（CONTEXT.md User 词条：人类身份；经
// membership 在 Team 内持 Role 获权）。
package user

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// User 是聚合行。
type User struct {
	ID        string
	Name      string
	CreatedAt string
}

// Repo 是 User 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同名冲突返回 state.ErrConflict。
func (r *Repo) Create(ctx context.Context, run state.Runner, u *User) error {
	u.CreatedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO users (id, name, created_at) VALUES (?, ?, ?)`,
		u.ID, u.Name, u.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: user name %q already exists", state.ErrConflict, u.Name)
	}
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*User, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, created_at FROM users WHERE id = ?`, id)
	var u User
	if err := row.Scan(&u.ID, &u.Name, &u.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &u, nil
}

// Count 返回用户总数（Bootstrap Token 的"无用户"判定）。
func (r *Repo) Count(ctx context.Context, run state.Runner) (int, error) {
	var n int
	err := run.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// List 返回全部用户（ID 序稳定）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]User, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, name, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Delete 按 ID 删行（引用完整性由 memberships FK RESTRICT 把守——有
// membership 的用户先解绑再删）。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
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
