// Package browse 是 Browse 会话回收台账聚合（F3.6，ADR-0051 决策 1）：
// 行承载"哪个库开了什么形态的会话、何时到期"——凭证不在行上（Ensure 期
// 从 Secret 单真源重新解封渲染）。回收即删行（会话非资源行，无软删）。
package browse

import (
	"context"
	"database/sql"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Session 是一行回收台账。
type Session struct {
	ID         string
	ProjectID  string
	DatabaseID string
	Engine     string
	ReadOnly   bool
	CreatedAt  string
	ExpiresAt  string
}

// Repo 是 browse 会话台账存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

const selectCols = `SELECT id, project_id, database_id, engine, read_only, created_at, expires_at FROM browse_sessions`

func scanSession(scan func(...any) error) (*Session, error) {
	var s Session
	var readOnly int
	err := scan(&s.ID, &s.ProjectID, &s.DatabaseID, &s.Engine, &readOnly, &s.CreatedAt, &s.ExpiresAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	s.ReadOnly = readOnly != 0
	return &s, nil
}

// Create 落一行台账（受理事务内——与事件/审计同拍）。
func (r *Repo) Create(ctx context.Context, run state.Runner, s *Session) error {
	ro := 0
	if s.ReadOnly {
		ro = 1
	}
	_, err := run.ExecContext(ctx,
		`INSERT INTO browse_sessions (id, project_id, database_id, engine, read_only, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, s.DatabaseID, s.Engine, ro, s.CreatedAt, s.ExpiresAt)
	return err
}

// List 返回全部在册行（到期判定在引擎域——repo 只做存取；id 升序稳定）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]*Session, error) {
	rows, err := run.QueryContext(ctx, selectCols+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面（backup repo 同款）
	var out []*Session
	for rows.Next() {
		s, err := scanSession(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountByProject 对在册行按 project 计数（quota 判定输入）。
func (r *Repo) CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error) {
	var n int
	err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM browse_sessions WHERE project_id = ?`, projectID).Scan(&n)
	return n, err
}

// Delete 删一行（回收收口；不存在是幂等成功）。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	_, err := run.ExecContext(ctx, `DELETE FROM browse_sessions WHERE id = ?`, id)
	if err == sql.ErrNoRows {
		return nil
	}
	return err
}

// ExpiryTime 解析行上到期时刻（RFC3339 读回面）。
func (s *Session) ExpiryTime() (time.Time, error) {
	return time.Parse(time.RFC3339, s.ExpiresAt)
}

// CreatedTime 解析行上受理时刻。
func (s *Session) CreatedTime() (time.Time, error) {
	return time.Parse(time.RFC3339, s.CreatedAt)
}
