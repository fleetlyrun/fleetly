// Package browse 是 Browse 会话回收台账 repo 的 SQLite 实现：行类型与
// Repo 接口住 domain/data/browse（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。
package browse

import (
	"context"
	"database/sql"

	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain browse.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain browse.Repo）。
func New(clock state.Clock) browse.Repo { return &repo{clock: clock} }

const selectCols = `SELECT id, seq, project_id, database_id, engine, read_only, created_at, expires_at FROM browse_sessions`

func scanSession(scan func(...any) error) (*browse.Session, error) {
	var s browse.Session
	var readOnly int
	err := scan(&s.ID, &s.Seq, &s.ProjectID, &s.DatabaseID, &s.Engine, &readOnly, &s.CreatedAt, &s.ExpiresAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	s.ReadOnly = readOnly != 0
	return &s, nil
}

// Create 落一行台账（受理事务内——与事件/审计同拍）。
func (r *repo) Create(ctx context.Context, run state.Runner, s *browse.Session) error {
	seq, err := persistence.NextSeq(ctx, run, "browse_sessions")
	if err != nil {
		return err
	}
	s.Seq = seq
	if s.ID == "" {
		s.ID = entityid.Derive(s.DatabaseID, "s", seq)
	}
	ro := 0
	if s.ReadOnly {
		ro = 1
	}
	_, err = run.ExecContext(ctx,
		`INSERT INTO browse_sessions (id, seq, project_id, database_id, engine, read_only, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Seq, s.ProjectID, s.DatabaseID, s.Engine, ro, s.CreatedAt, s.ExpiresAt)
	return err
}

// List 返回全部在册行（到期判定在引擎域——repo 只做存取；id 升序稳定）。
func (r *repo) List(ctx context.Context, run state.Runner) ([]*browse.Session, error) {
	rows, err := run.QueryContext(ctx, selectCols+` ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面（backup repo 同款）
	var out []*browse.Session
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
func (r *repo) CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error) {
	var n int
	err := run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM browse_sessions WHERE project_id = ?`, projectID).Scan(&n)
	return n, err
}

// Delete 删一行（回收收口；不存在是幂等成功）。
func (r *repo) Delete(ctx context.Context, run state.Runner, id string) error {
	_, err := run.ExecContext(ctx, `DELETE FROM browse_sessions WHERE id = ?`, id)
	if err == sql.ErrNoRows {
		return nil
	}
	return err
}
