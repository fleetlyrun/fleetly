// Package project 是 Project 聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/structure/project（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。
package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain project.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain project.Repo）。
func New(clock state.Clock) project.Repo { return &repo{clock: clock} }

// Create 落一行；同 Team 同名活跃 Project 冲突返回 state.ErrAlreadyExists
// （ADR-0028：唯一性口径 (team_id, name)——同名项目跨 Team 并存）。
// ID 空则装配 "<name>-<seq>"（ADR-0070）；Seq 零值则事务内取 MAX+1（铸造
// 单源在 repo 行装配处）。
func (r *repo) Create(ctx context.Context, run state.Runner, p *project.Project) error {
	now := state.FormatTime(r.clock.Now())
	p.CreatedAt, p.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, run, "projects")
	if err != nil {
		return err
	}
	p.Seq = seq
	if p.ID == "" {
		p.ID = entityid.Join(p.Name, seq)
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO projects (id, seq, name, team_id, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		p.ID, p.Seq, p.Name, p.TeamID, p.CreatedAt, p.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: project name %q already exists in team %s", state.ErrAlreadyExists, p.Name, p.TeamID)
	}
	return err
}

// Get 按 ID 直读（含已删行——tombstone 是事实，不是秘密）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*project.Project, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE id = ?`, id)
	return scanProject(row)
}

// GetByNameInTeam 在 Team 内按名读活跃行（ADR-0028：名字唯一性只在
// Team 内成立——跨 Team 同名并存，裸名查询语义不再成立）。
func (r *repo) GetByNameInTeam(ctx context.Context, run state.Runner, teamID, name string) (*project.Project, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE team_id = ? AND name = ? AND deleted_at = ''`, teamID, name)
	return scanProject(row)
}

// List 返回全部活跃行（全量面——seq 序 = 创建序；授权解析与引擎枚举
// 消费；API 分页读面走 ListPage）。
func (r *repo) List(ctx context.Context, run state.Runner) ([]project.Project, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, seq, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE deleted_at = '' ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []project.Project
	for rows.Next() {
		var p project.Project
		if err := rows.Scan(&p.ID, &p.Seq, &p.Name, &p.TeamID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPage 是 List 的分页读面（ADR-0026 after_* + limit；游标轴 = seq
// 创建序升序——既有响应序不变）。limit<=0 或 >200 回落/钳制缺省 50。
func (r *repo) ListPage(ctx context.Context, run state.Runner, afterSeq int64, limit int) ([]project.Project, error) {
	return r.page(ctx, run, "", afterSeq, limit)
}

// ListByTeam 返回 Team 内全部活跃行（ADR-0035 行级授权的 List 面过滤锚
// 与 authz 解析面——全量；非 owner 的 API 分页读面走 ListByTeamPage）。
func (r *repo) ListByTeam(ctx context.Context, run state.Runner, teamID string) ([]project.Project, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, seq, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE deleted_at = '' AND team_id = ? ORDER BY seq`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []project.Project
	for rows.Next() {
		var p project.Project
		if err := rows.Scan(&p.ID, &p.Seq, &p.Name, &p.TeamID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListByTeamPage 是 ListByTeam 的分页读面（ADR-0026 after_* + limit；
// Team 过滤与游标分页叠加）。
func (r *repo) ListByTeamPage(ctx context.Context, run state.Runner, teamID string, afterSeq int64, limit int) ([]project.Project, error) {
	return r.page(ctx, run, teamID, afterSeq, limit)
}

// page 是两种分页形态的共用体：teamID 空 = 全局（owner 面），非空 =
// Team 过滤（ADR-0035 List 面）。
func (r *repo) page(ctx context.Context, run state.Runner, teamID string, afterSeq int64, limit int) ([]project.Project, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := `
		SELECT id, seq, name, team_id, created_at, updated_at, deleted_at
		FROM projects WHERE deleted_at = ''`
	args := []any{}
	if teamID != "" {
		q += ` AND team_id = ?`
		args = append(args, teamID)
	}
	if afterSeq > 0 {
		q += ` AND seq > ?`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []project.Project
	for rows.Next() {
		var p project.Project
		if err := rows.Scan(&p.ID, &p.Seq, &p.Name, &p.TeamID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); err != nil {
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
func (r *repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
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

func scanProject(row *sql.Row) (*project.Project, error) {
	var p project.Project
	err := row.Scan(&p.ID, &p.Seq, &p.Name, &p.TeamID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
