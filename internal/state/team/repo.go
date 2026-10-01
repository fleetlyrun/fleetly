// Package team 是 Team 聚合 repo（CONTEXT.md Team 词条：权限轴，User 与
// Token 通过 Team 内的角色获得资源访问权；Project 归属 Team）。
package team

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Team 是聚合行。
type Team struct {
	ID        string
	Name      string
	CreatedAt string
}

// Repo 是 Team 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
func (r *Repo) Create(ctx context.Context, run state.Runner, t *Team) error {
	t.CreatedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO teams (id, name, created_at) VALUES (?, ?, ?)`,
		t.ID, t.Name, t.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: team name %q already exists", state.ErrAlreadyExists, t.Name)
	}
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Team, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, created_at FROM teams WHERE id = ?`, id)
	var t Team
	if err := row.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &t, nil
}

// List 返回全部 Team（ID 序稳定）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Team, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, name, created_at FROM teams ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Delete 按 ID 删行（FK RESTRICT 把守：仍有 Project/Token/membership
// 引用的 Team 拒删，错误归一为 state.ErrConflict——调用方提示先解绑）。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, `DELETE FROM teams WHERE id = ?`, id)
	if err != nil {
		if state.IsUniqueViolation(err) || state.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: team %s is still referenced", state.ErrConflict, id)
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
