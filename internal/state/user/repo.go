// Package user 是 User 聚合 repo（CONTEXT.md User 词条：人类身份；经
// membership 在 Team 内持 Role 获权）。
package user

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// User 是聚合行。PasswordHash 是密码凭证第二形态（C6 第一期；bcrypt；
// 空 = 未设密——密码登录诚实拒绝）。
type User struct {
	ID           string
	Name         string
	CreatedAt    string
	PasswordHash string
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
		INSERT INTO users (id, name, created_at, password_hash) VALUES (?, ?, ?, ?)`,
		u.ID, u.Name, u.CreatedAt, u.PasswordHash)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: user name %q already exists", state.ErrAlreadyExists, u.Name)
	}
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*User, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, created_at, password_hash FROM users WHERE id = ?`, id)
	var u User
	if err := row.Scan(&u.ID, &u.Name, &u.CreatedAt, &u.PasswordHash); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &u, nil
}

// GetByName 按名直读（Login 密码自证的入口；用户名全局唯一）。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, name string) (*User, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, created_at, password_hash FROM users WHERE name = ?`, name)
	var u User
	if err := row.Scan(&u.ID, &u.Name, &u.CreatedAt, &u.PasswordHash); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &u, nil
}

// SetPassword 写密码哈希（admin 设置/重置；Login 的自证物单源）。
func (r *Repo) SetPassword(ctx context.Context, run state.Runner, id, hash string) error {
	res, err := run.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return state.ErrNotFound
	}
	return nil
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
		SELECT id, name, created_at, password_hash FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.CreatedAt, &u.PasswordHash); err != nil {
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
