// Package user 是 User 聚合 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/identity/user（ADR-0075 Repo 接口层），本包只持不导出实现与
// 构造。
package user

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/user"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain user.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain user.Repo）。
func New(clock state.Clock, run state.Runner) user.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, u *user.User) error {
	seq, err := persistence.NextSeq(ctx, r.run, "users")
	if err != nil {
		return err
	}
	u.Seq = seq
	if u.ID == "" {
		u.ID = entityid.Join(u.Name, seq)
	}
	u.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO users (id, seq, name, created_at, password_hash) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Seq, u.Name, u.CreatedAt, u.PasswordHash)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: user name %q already exists", state.ErrAlreadyExists, u.Name)
	}
	return err
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, id string) (*user.User, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, name, created_at, password_hash FROM users WHERE id = ?`, id)
	var u user.User
	if err := row.Scan(&u.ID, &u.Seq, &u.Name, &u.CreatedAt, &u.PasswordHash); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &u, nil
}

// GetByName 按名直读（Login 密码自证的入口；用户名全局唯一）。
func (r *repo) GetByName(ctx context.Context, name string) (*user.User, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, name, created_at, password_hash FROM users WHERE name = ?`, name)
	var u user.User
	if err := row.Scan(&u.ID, &u.Seq, &u.Name, &u.CreatedAt, &u.PasswordHash); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &u, nil
}

// SetPassword 写密码哈希（admin 设置/重置；Login 的自证物单源）。
func (r *repo) SetPassword(ctx context.Context, id, hash string) error {
	res, err := r.run.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// Count 返回用户总数（Bootstrap Token 的"无用户"判定）。
func (r *repo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.run.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// List 返回全部用户（ID 序稳定）。
func (r *repo) List(ctx context.Context) ([]user.User, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, name, created_at, password_hash FROM users ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []user.User
	for rows.Next() {
		var u user.User
		if err := rows.Scan(&u.ID, &u.Seq, &u.Name, &u.CreatedAt, &u.PasswordHash); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Delete 按 ID 删行。仍被 Token 引用时 FK RESTRICT 归一为 state.ErrConflict
// （ADR-0028 user repo FK 归一，对齐 role repo 先例——先吊销/删名下 Token
// 再删用户，不再误导 E_INTERNAL）；有 membership 的用户已在 API 层先解绑。
func (r *repo) Delete(ctx context.Context, id string) error {
	res, err := r.run.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		if persistence.IsForeignKeyViolation(err) {
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
