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

// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
func (r *Repo) Create(ctx context.Context, run state.Runner, u *User) error {
	u.CreatedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO users (id, name, created_at) VALUES (?, ?, ?)`,
		u.ID, u.Name, u.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: user name %q already exists", state.ErrAlreadyExists, u.Name)
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

// Delete 按 ID 删行。仍被 Token 引用时 FK RESTRICT 归一为 state.ErrConflict
// （ADR-0028 user repo FK 归一，对齐 role repo 先例——先吊销/删名下 Token
// 再删用户，不再误导 E_INTERNAL）；有 membership 的用户已在 API 层先解绑。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		if state.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: user %s is still referenced by tokens; revoke or delete them first", state.ErrConflict, id)
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
