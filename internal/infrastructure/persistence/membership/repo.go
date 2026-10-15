// Package membership 是 membership 表 repo（User 在 Team 内经 Role 获权的
// 关联聚合；一 Team 一 Role，UNIQUE(user_id, team_id)）。
package membership

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// Membership 是关联行。
type Membership struct {
	ID        string
	Seq       int64
	UserID    string
	TeamID    string
	RoleID    string
	CreatedAt string
}

// Repo 是 membership 存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同 User 同 Team 重复授权返回 state.ErrAlreadyExists。
func (r *Repo) Create(ctx context.Context, run state.Runner, m *Membership) error {
	seq, err := persistence.NextSeq(ctx, run, "memberships")
	if err != nil {
		return err
	}
	m.Seq = seq
	if m.ID == "" {
		m.ID = entityid.Join("membership", seq)
	}
	m.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = run.ExecContext(ctx, `
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
func (r *Repo) GetByUser(ctx context.Context, run state.Runner, userID, teamID string) (*Membership, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, user_id, team_id, role_id, created_at
		FROM memberships WHERE user_id = ? AND team_id = ?`, userID, teamID)
	return scanOne(row.Scan)
}

// DeleteByUser 解绑 User 在 Team 的授权（删用户前解引用）。
func (r *Repo) DeleteByUser(ctx context.Context, run state.Runner, userID, teamID string) error {
	_, err := run.ExecContext(ctx, `
		DELETE FROM memberships WHERE user_id = ? AND team_id = ?`, userID, teamID)
	return err
}

func scanOne(scan func(dest ...any) error) (*Membership, error) {
	var m Membership
	if err := scan(&m.ID, &m.Seq, &m.UserID, &m.TeamID, &m.RoleID, &m.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &m, nil
}

// ListByUser 返回用户的全部 membership（Login 密码会话铸 Token 的
// team/role 解析源；C6 第一期——单 membership 取首行，多队诚实取 ID 序
// 首个，跨队会话面随多队裁决扩展）。
func (r *Repo) ListByUser(ctx context.Context, run state.Runner, userID string) ([]Membership, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, seq, user_id, team_id, role_id, created_at FROM memberships WHERE user_id = ? ORDER BY seq`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.ID, &m.Seq, &m.UserID, &m.TeamID, &m.RoleID, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
