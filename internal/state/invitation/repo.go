// Package invitation 是 Invitation 聚合 repo（F0.5 邀请流：一次性、短时
// 窗、绑 team+role；sha256 存储，明文只在创建响应出现一次）。
package invitation

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Invitation 是聚合行（ConsumedAt 空串=未使用；ExpiresAt 是 RFC3339）。
type Invitation struct {
	ID          string
	TokenSHA256 string
	TeamID      string
	RoleID      string
	CreatedBy   string
	ExpiresAt   string
	ConsumedAt  string
	CreatedAt   string
}

// Repo 是 Invitation 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行。
func (r *Repo) Create(ctx context.Context, run state.Runner, inv *Invitation) error {
	inv.CreatedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO invitations (id, token_sha256, team_id, role_id, created_by, expires_at, consumed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?)`,
		inv.ID, inv.TokenSHA256, inv.TeamID, inv.RoleID, inv.CreatedBy, inv.ExpiresAt, inv.CreatedAt)
	return err
}

// GetBySHA256 按明文摘要查行（accept 面的查询键）。
func (r *Repo) GetBySHA256(ctx context.Context, run state.Runner, sha string) (*Invitation, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, token_sha256, team_id, role_id, created_by, expires_at, consumed_at, created_at
		FROM invitations WHERE token_sha256 = ?`, sha)
	return scanOne(row.Scan)
}

// List 返回全部邀请（ID 序稳定；含已消费/已过期——状态是可见事实）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Invitation, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, token_sha256, team_id, role_id, created_by, expires_at, consumed_at, created_at
		FROM invitations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Invitation
	for rows.Next() {
		inv, err := scanOne(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// MarkConsumed 落单次使用位（条件更新：仅未消费行命中——并发双 accept
// 只有一个成功，另一个读到的仍是未消费但重查即 NOT_FOUND 语义）。
func (r *Repo) MarkConsumed(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE invitations SET consumed_at = ? WHERE id = ? AND consumed_at = ''`,
		state.FormatTime(r.clock.Now()), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrConflict
	}
	return nil
}

func scanOne(scan func(dest ...any) error) (*Invitation, error) {
	var inv Invitation
	if err := scan(&inv.ID, &inv.TokenSHA256, &inv.TeamID, &inv.RoleID, &inv.CreatedBy,
		&inv.ExpiresAt, &inv.ConsumedAt, &inv.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &inv, nil
}
