// Package project 是 Project 聚合 repo（CONTEXT.md Project 词条：归属与
// 网络隔离轴，属于一个 Team；资源名只在 Project 内唯一）。
package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Project 是聚合行（软删 tombstone：deleted_at 非空即已删）。
type Project struct {
	ID        string
	Name      string
	TeamID    string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (p *Project) Deleted() bool { return p.DeletedAt != "" }

// Repo 是 Project 聚合存取（无门面：仅本聚合查询）。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同名活跃 Project 冲突返回 state.ErrAlreadyExists。
func (r *Repo) Create(ctx context.Context, run state.Runner, p *Project) error {
	now := state.FormatTime(r.clock.Now())
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO projects (id, name, team_id, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, '')`,
		p.ID, p.Name, p.TeamID, p.CreatedAt, p.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: project name %q already exists", state.ErrAlreadyExists, p.Name)
	}
	return err
}

// Get 按 ID 直读（含已删行——tombstone 是事实，不是秘密）。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Project, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE id = ?`, id)
	return scanProject(row)
}

// GetByName 按名读活跃行（无或已删 → state.ErrNotFound）。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, name string) (*Project, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE name = ? AND deleted_at = ''`, name)
	return scanProject(row)
}

// List 返回全部活跃行（ULID 主键序 = 创建序；N0 单团队规模无分页诉求）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Project, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE deleted_at = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.TeamID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SoftDelete 落 tombstone（四件一拍的 tombstone 件；Outbox/审计由调用方
// 同事务组合）。幂等：已删行不报错。
func (r *Repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE projects SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 已删（幂等）或不存在：区分只在审计语义上有意义，读路径已覆盖。
		if _, err := r.Get(ctx, run, id); err != nil {
			return err
		}
	}
	return nil
}

func scanProject(row *sql.Row) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.TeamID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
