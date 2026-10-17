// Package team 是 Team 聚合 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/identity/team（ADR-0075 Repo 接口层），本包只持不导出实现与
// 构造。
package team

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain team.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain team.Repo）。
func New(clock state.Clock, run state.Runner) team.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, t *team.Team) error {
	seq, err := persistence.NextSeq(ctx, r.run, "teams")
	if err != nil {
		return err
	}
	t.Seq = seq
	if t.ID == "" {
		t.ID = entityid.Join(t.Name, seq)
	}
	t.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO teams (id, seq, name, created_at) VALUES (?, ?, ?, ?)`,
		t.ID, t.Seq, t.Name, t.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: team name %q already exists", state.ErrAlreadyExists, t.Name)
	}
	return err
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, id string) (*team.Team, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, name, created_at FROM teams WHERE id = ?`, id)
	var t team.Team
	if err := row.Scan(&t.ID, &t.Seq, &t.Name, &t.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &t, nil
}

// List 返回全部 Team（ID 序稳定）。
func (r *repo) List(ctx context.Context) ([]team.Team, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, name, created_at FROM teams ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []team.Team
	for rows.Next() {
		var t team.Team
		if err := rows.Scan(&t.ID, &t.Seq, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Delete 按 ID 删行（FK RESTRICT 把守：仍有 Project/Token/membership
// 引用的 Team 拒删，错误归一为 state.ErrConflict——调用方提示先解绑）。
func (r *repo) Delete(ctx context.Context, id string) error {
	res, err := r.run.ExecContext(ctx, `DELETE FROM teams WHERE id = ?`, id)
	if err != nil {
		if persistence.IsUniqueViolation(err) || persistence.IsForeignKeyViolation(err) {
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
