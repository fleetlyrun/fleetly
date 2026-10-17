// Package invitation 是 Invitation 聚合 repo 的 SQLite 实现：行类型与
// Repo 接口住 domain/identity/invitation（ADR-0075 Repo 接口层；材质生成
// 在 identity 种子包），本包只持不导出实现与构造。
package invitation

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/invitation"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain invitation.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain invitation.Repo）。
func New(clock state.Clock, run state.Runner) invitation.Repo { return &repo{clock: clock, run: run} }

// Create 落一行。
func (r *repo) Create(ctx context.Context, inv *invitation.Invitation) error {
	seq, err := persistence.NextSeq(ctx, r.run, "invitations")
	if err != nil {
		return err
	}
	inv.Seq = seq
	if inv.ID == "" {
		inv.ID = entityid.Join("inv", seq)
	}
	inv.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO invitations (id, seq, token_sha256, team_id, role_id, created_by, expires_at, consumed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?)`,
		inv.ID, inv.Seq, inv.TokenSHA256, inv.TeamID, inv.RoleID, inv.CreatedBy, inv.ExpiresAt, inv.CreatedAt)
	return err
}

// GetBySHA256 按明文摘要查行（accept 面的查询键）。
func (r *repo) GetBySHA256(ctx context.Context, sha string) (*invitation.Invitation, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, token_sha256, team_id, role_id, created_by, expires_at, consumed_at, created_at
		FROM invitations WHERE token_sha256 = ?`, sha)
	return scanOne(row.Scan)
}

// List 返回全部邀请（ID 序稳定；含已消费/已过期——状态是可见事实）。
func (r *repo) List(ctx context.Context) ([]invitation.Invitation, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, token_sha256, team_id, role_id, created_by, expires_at, consumed_at, created_at
		FROM invitations ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []invitation.Invitation
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
func (r *repo) MarkConsumed(ctx context.Context, id string) error {
	res, err := r.run.ExecContext(ctx, `
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

func scanOne(scan func(dest ...any) error) (*invitation.Invitation, error) {
	var inv invitation.Invitation
	if err := scan(&inv.ID, &inv.Seq, &inv.TokenSHA256, &inv.TeamID, &inv.RoleID, &inv.CreatedBy,
		&inv.ExpiresAt, &inv.ConsumedAt, &inv.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &inv, nil
}
