// Package freeze 是 Change Freeze 聚合 repo 的 SQLite 实现：行类型与
// Repo 接口住 domain/runtimeops/freeze（ADR-0075 Repo 接口层），本包只持
// 不导出实现与构造。
package freeze

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/freeze"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain freeze.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain freeze.Repo）。
func New(clock state.Clock) freeze.Repo { return &repo{clock: clock} }

const selectCols = "id, seq, team_id, reason, created_by, created_at, lifted_at"

// Create 落一条活跃冻结；同 scope 已有活跃行 → ErrAlreadyExists（部分唯一
// 索引把守——Set 面映射 E_CONFLICT，先 lift 再 set）。
func (r *repo) Create(ctx context.Context, run state.Runner, f *freeze.Freeze) error {
	seq, err := persistence.NextSeq(ctx, run, "change_freezes")
	if err != nil {
		return err
	}
	f.Seq = seq
	if f.ID == "" {
		f.ID = entityid.Join("freeze", seq)
	}
	f.CreatedAt, f.LiftedAt = state.FormatTime(r.clock.Now()), ""
	_, err = run.ExecContext(ctx, `
		INSERT INTO change_freezes (id, seq, team_id, reason, created_by, created_at, lifted_at)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		f.ID, f.Seq, f.TeamID, f.Reason, f.CreatedBy, f.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: an active change freeze already covers team %q", state.ErrAlreadyExists, f.TeamID)
	}
	return err
}

// Get 按 ID 读行（含历史行——lift 幂等面需要区分"已 lift"与"不存在"）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*freeze.Freeze, error) {
	row := run.QueryRowContext(ctx,
		"SELECT "+selectCols+" FROM change_freezes WHERE id = ?", id)
	return scanFreeze(row)
}

// ActiveForTeam 返回命中该 Team 的活跃冻结（team_id ∈ {”, teamID}）；
// Team 特定行排前（拒绝信封优先报更具体的原因）。
func (r *repo) ActiveForTeam(ctx context.Context, run state.Runner, teamID string) ([]freeze.Freeze, error) {
	rows, err := run.QueryContext(ctx,
		"SELECT "+selectCols+" FROM change_freezes WHERE lifted_at = '' AND team_id IN ('', ?)"+
			" ORDER BY (team_id = '') ASC, created_at ASC, id ASC", teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []freeze.Freeze
	for rows.Next() {
		f, err := scanFreeze(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// Lift 落 lifted_at（CAS：仅活跃行）。0 行回查：行不存在 → ErrNotFound；
// 已 lift → nil（幂等——Lift 面对历史行直接成功）。
func (r *repo) Lift(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx,
		`UPDATE change_freezes SET lifted_at = ? WHERE id = ? AND lifted_at = ''`,
		state.FormatTime(r.clock.Now()), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, gerr := r.Get(ctx, run, id); gerr != nil {
			return gerr // ErrNotFound
		}
		return nil // 已 lift：幂等成功
	}
	return nil
}

// List 返回全部冻结行（新→旧 + after 游标；ADR-0026 after_* + limit 惯例）。
func (r *repo) List(ctx context.Context, run state.Runner, afterSeq int64, limit int) ([]freeze.Freeze, error) {
	limit = persistence.ClampLimit(limit, 50, 200)
	q := "SELECT " + selectCols + " FROM change_freezes"
	args := []any{}
	if afterSeq > 0 {
		q += " WHERE seq < ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []freeze.Freeze
	for rows.Next() {
		f, err := scanFreeze(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// scanner 兼容 *sql.Row 与 *sql.Rows。
type scanner interface{ Scan(dest ...any) error }

func scanFreeze(row scanner) (*freeze.Freeze, error) {
	var f freeze.Freeze
	if err := row.Scan(&f.ID, &f.Seq, &f.TeamID, &f.Reason, &f.CreatedBy, &f.CreatedAt, &f.LiftedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &f, nil
}
