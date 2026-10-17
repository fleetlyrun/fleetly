// Package channel 是通知通道 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/telemetry/channel（ADR-0075 Repo 接口层），本包只持不导出实现
// 与构造（age 信封加密机械在此）。
package channel

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain channel.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain channel.Repo）。
func New(clock state.Clock, run state.Runner) channel.Repo { return &repo{clock: clock, run: run} }

const selectCols = "id, seq, name, kind, config_ciphertext, enabled, last_failure, created_at, updated_at"

// Create 落一条通道；重名 → ErrAlreadyExists（name 唯一索引把守）。
func (r *repo) Create(ctx context.Context, c *channel.Channel) error {
	seq, err := persistence.NextSeq(ctx, r.run, "notification_channels")
	if err != nil {
		return err
	}
	c.Seq = seq
	if c.ID == "" {
		c.ID = entityid.Join(c.Name, seq)
	}
	c.CreatedAt = state.FormatTime(r.clock.Now())
	c.UpdatedAt = c.CreatedAt
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO notification_channels (id, seq, name, kind, config_ciphertext, enabled, last_failure, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`,
		c.ID, c.Seq, c.Name, c.Kind, c.ConfigCiphertext, boolInt(c.Enabled), c.CreatedAt, c.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return state.ErrAlreadyExists
	}
	return err
}

// Get 按 ID 读行。
func (r *repo) Get(ctx context.Context, id string) (*channel.Channel, error) {
	row := r.run.QueryRowContext(ctx, "SELECT "+selectCols+" FROM notification_channels WHERE id = ?", id)
	return scanChannel(row)
}

// ListEnabled 列启用通道（派发面）。
func (r *repo) ListEnabled(ctx context.Context) ([]channel.Channel, error) {
	return r.listWhere(ctx, "WHERE enabled = 1 ORDER BY seq")
}

// ListAll 列全部通道（管理面）。
func (r *repo) ListAll(ctx context.Context) ([]channel.Channel, error) {
	return r.listWhere(ctx, "ORDER BY seq")
}

func (r *repo) listWhere(ctx context.Context, where string, args ...any) ([]channel.Channel, error) {
	rows, err := r.run.QueryContext(ctx, "SELECT "+selectCols+" FROM notification_channels "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []channel.Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// RecordDelivery 落最近一次派发结果（空 err 文本 = 成功清位）。
func (r *repo) RecordDelivery(ctx context.Context, id, failure string) error {
	_, err := r.run.ExecContext(ctx,
		"UPDATE notification_channels SET last_failure = ?, updated_at = ? WHERE id = ?",
		failure, state.FormatTime(r.clock.Now()), id)
	return err
}

// Delete 删行（不存在 → ErrNotFound）。
func (r *repo) Delete(ctx context.Context, id string) error {
	res, err := r.run.ExecContext(ctx, "DELETE FROM notification_channels WHERE id = ?", id)
	if err != nil {
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

func scanChannel(row interface{ Scan(dest ...any) error }) (*channel.Channel, error) {
	var c channel.Channel
	var enabled int
	err := row.Scan(&c.ID, &c.Seq, &c.Name, &c.Kind, &c.ConfigCiphertext, &enabled, &c.LastFailure, &c.CreatedAt, &c.UpdatedAt)
	if persistence.MapScanErr(err) == state.ErrNotFound {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	return &c, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
