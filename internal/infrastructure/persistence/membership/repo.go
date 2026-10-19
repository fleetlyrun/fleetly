// Package membership 是 membership 关联聚合 repo 的 SQLite 实现：行类型
// 与 Repo 接口住 domain/identity/membership（ADR-0075 Repo 接口层），本包
// 只持不导出实现与构造。
package membership

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain membership.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain membership.Repo）。
func New(clock state.Clock, run state.Runner) membership.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同 User 同 Team 重复授权返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, m *membership.Membership) error {
	seq, err := persistence.NextSeq(ctx, r.run, "memberships")
	if err != nil {
		return err
	}
	m.Seq = seq
	if m.ID == "" {
		m.ID = entityid.Join("membership", seq)
	}
	m.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO memberships (id, seq, user_id, team_id, role_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		m.ID, m.Seq, m.UserID, m.TeamID, m.RoleID, m.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: user %s already has a role in team %s", state.ErrAlreadyExists, m.UserID, m.TeamID)
	}
	if persistence.IsForeignKeyViolation(err) {
		return fmt.Errorf("fk: user=%s team=%s role=%s: %w", m.UserID, m.TeamID, m.RoleID, err)
	}
	return err
}

// GetByUser 返回 User 在指定 Team 的 membership。
func (r *repo) GetByUser(ctx context.Context, userID, teamID string) (*membership.Membership, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, user_id, team_id, role_id, created_at
		FROM memberships WHERE user_id = ? AND team_id = ?`, userID, teamID)
	return scanOne(row.Scan)
}

// DeleteByUser 解绑 User 在 Team 的授权（删用户前解引用）。
func (r *repo) DeleteByUser(ctx context.Context, userID, teamID string) error {
	_, err := r.run.ExecContext(ctx, `
		DELETE FROM memberships WHERE user_id = ? AND team_id = ?`, userID, teamID)
	return err
}

// DeleteAllForUser 解绑 User 的全部 membership 行（删用户级联）。
func (r *repo) DeleteAllForUser(ctx context.Context, userID string) (int64, error) {
	res, err := r.run.ExecContext(ctx, `DELETE FROM memberships WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func scanOne(scan func(dest ...any) error) (*membership.Membership, error) {
	var m membership.Membership
	if err := scan(&m.ID, &m.Seq, &m.UserID, &m.TeamID, &m.RoleID, &m.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &m, nil
}

// ListByUser 返回用户的全部 membership（Login 密码会话铸 Token 的
// team/role 解析源；C6 第一期——单 membership 取首行，多队诚实取 ID 序
// 首个，跨队会话面随多队裁决扩展）。
func (r *repo) ListByUser(ctx context.Context, userID string) ([]membership.Membership, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, user_id, team_id, role_id, created_at FROM memberships WHERE user_id = ? ORDER BY seq`, userID)
	if err != nil {
		return nil, err
	}
	return scanMany(rows)
}

// ListByTeam 返回 Team 内的全部 membership（身份目录读面按队过滤的单源：
// ListUsers 非平台管理员一次取本队成员全集，避免逐用户回读的 N+1）。
func (r *repo) ListByTeam(ctx context.Context, teamID string) ([]membership.Membership, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, user_id, team_id, role_id, created_at FROM memberships WHERE team_id = ? ORDER BY seq`, teamID)
	if err != nil {
		return nil, err
	}
	return scanMany(rows)
}

// scanMany 消费 membership 行集（ListByUser/ListByTeam 共用扫描列序）。
func scanMany(rows *sql.Rows) ([]membership.Membership, error) {
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []membership.Membership
	for rows.Next() {
		var m membership.Membership
		if err := rows.Scan(&m.ID, &m.Seq, &m.UserID, &m.TeamID, &m.RoleID, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
