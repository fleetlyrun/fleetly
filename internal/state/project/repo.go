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

// Create 落一行；同 Team 同名活跃 Project 冲突返回 state.ErrAlreadyExists
// （ADR-0028：唯一性口径 (team_id, name)——同名项目跨 Team 并存）。
func (r *Repo) Create(ctx context.Context, run state.Runner, p *Project) error {
	now := state.FormatTime(r.clock.Now())
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO projects (id, name, team_id, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, '')`,
		p.ID, p.Name, p.TeamID, p.CreatedAt, p.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: project name %q already exists in team %s", state.ErrAlreadyExists, p.Name, p.TeamID)
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

// GetByNameInTeam 在 Team 内按名读活跃行（ADR-0028：名字唯一性只在
// Team 内成立——跨 Team 同名并存，裸名查询语义不再成立）。
func (r *Repo) GetByNameInTeam(ctx context.Context, run state.Runner, teamID, name string) (*Project, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE team_id = ? AND name = ? AND deleted_at = ''`, teamID, name)
	return scanProject(row)
}

// List 返回全部活跃行（全量面——ULID 主键序 = 创建序；授权解析与引擎
// 枚举消费；API 分页读面走 ListPage）。
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

// ListPage 是 List 的分页读面（ADR-0026 after_* + limit；游标轴 = ULID
// 创建序升序——既有响应序不变）。limit<=0 或 >200 回落/钳制缺省 50。
func (r *Repo) ListPage(ctx context.Context, run state.Runner, afterID string, limit int) ([]Project, error) {
	return r.page(ctx, run, "", afterID, limit)
}

// ListByTeam 返回 Team 内全部活跃行（ADR-0035 行级授权的 List 面过滤锚
// 与 authz 解析面——全量；非 owner 的 API 分页读面走 ListByTeamPage）。
func (r *Repo) ListByTeam(ctx context.Context, run state.Runner, teamID string) ([]Project, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE deleted_at = '' AND team_id = ? ORDER BY id`, teamID)
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

// ListByTeamPage 是 ListByTeam 的分页读面（ADR-0026 after_* + limit；
// Team 过滤与游标分页叠加）。
func (r *Repo) ListByTeamPage(ctx context.Context, run state.Runner, teamID, afterID string, limit int) ([]Project, error) {
	return r.page(ctx, run, teamID, afterID, limit)
}

// page 是两种分页形态的共用体：teamID 空 = 全局（owner 面），非空 =
// Team 过滤（ADR-0035 List 面）。
func (r *Repo) page(ctx context.Context, run state.Runner, teamID, afterID string, limit int) ([]Project, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT id, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE deleted_at = ''`
	args := []any{}
	if teamID != "" {
		q += ` AND team_id = ?`
		args = append(args, teamID)
	}
	if afterID != "" {
		q += ` AND id > ?`
		args = append(args, afterID)
	}
	q += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
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

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

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
