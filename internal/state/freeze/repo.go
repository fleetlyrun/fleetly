// Package freeze 是 Change Freeze 聚合 repo（CONTEXT.md Change Freeze 词条：
// 变更冻结窗——按资源所属 Team 封禁变更型动词的治理刹车；ADR-0017 附录
// A.3，F1.9）。team_id 空串是全局冻结行；每 scope 至多一条活跃行（部分
// 唯一索引把守）；lift 落 lifted_at，历史行保留可回读。
package freeze

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Freeze 是聚合行（lifted_at 空 = 活跃冻结）。
type Freeze struct {
	ID        string
	TeamID    string // '' = 全局冻结（封禁全部 Team）
	Reason    string
	CreatedBy string
	CreatedAt string
	LiftedAt  string
}

// Active 报告是否活跃冻结。
func (f *Freeze) Active() bool { return f.LiftedAt == "" }

// Repo 是 Change Freeze 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

const selectCols = "id, team_id, reason, created_by, created_at, lifted_at"

// Create 落一条活跃冻结；同 scope 已有活跃行 → ErrAlreadyExists（部分唯一
// 索引把守——Set 面映射 E_CONFLICT，先 lift 再 set）。
func (r *Repo) Create(ctx context.Context, run state.Runner, f *Freeze) error {
	f.CreatedAt, f.LiftedAt = state.FormatTime(r.clock.Now()), ""
	_, err := run.ExecContext(ctx, `
		INSERT INTO change_freezes (id, team_id, reason, created_by, created_at, lifted_at)
		VALUES (?, ?, ?, ?, ?, '')`,
		f.ID, f.TeamID, f.Reason, f.CreatedBy, f.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: an active change freeze already covers team %q", state.ErrAlreadyExists, f.TeamID)
	}
	return err
}

// Get 按 ID 读行（含历史行——lift 幂等面需要区分"已 lift"与"不存在"）。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Freeze, error) {
	row := run.QueryRowContext(ctx,
		"SELECT "+selectCols+" FROM change_freezes WHERE id = ?", id)
	return scanFreeze(row)
}

// ActiveForTeam 返回命中该 Team 的活跃冻结（team_id ∈ {”, teamID}）；
// Team 特定行排前（拒绝信封优先报更具体的原因）。
func (r *Repo) ActiveForTeam(ctx context.Context, run state.Runner, teamID string) ([]Freeze, error) {
	rows, err := run.QueryContext(ctx,
		"SELECT "+selectCols+" FROM change_freezes WHERE lifted_at = '' AND team_id IN ('', ?)"+
			" ORDER BY (team_id = '') ASC, created_at ASC, id ASC", teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Freeze
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
func (r *Repo) Lift(ctx context.Context, run state.Runner, id string) error {
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
func (r *Repo) List(ctx context.Context, run state.Runner, afterID string, limit int) ([]Freeze, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := "SELECT " + selectCols + " FROM change_freezes"
	args := []any{}
	if afterID != "" {
		q += " WHERE id < ?"
		args = append(args, afterID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Freeze
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

func scanFreeze(row scanner) (*Freeze, error) {
	var f Freeze
	if err := row.Scan(&f.ID, &f.TeamID, &f.Reason, &f.CreatedBy, &f.CreatedAt, &f.LiftedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &f, nil
}
