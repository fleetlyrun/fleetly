// Package channel 是通知通道的行仓储（F2.5，ADR-0041 决策 4）：配置 age
// 信封入库（ADR-0014——URL/bot_token 是凭证材料，只写不读）；last_failure
// 是派发诊断面（成功清位）。
package channel

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 通道类别值域（冻结词汇）。
const (
	KindWebhook  = "webhook"
	KindTelegram = "telegram"
)

// Channel 是一行通知通道。
type Channel struct {
	ID               string
	Name             string
	Kind             string
	ConfigCiphertext []byte // age 信封载荷
	Enabled          bool
	LastFailure      string
	CreatedAt        string
	UpdatedAt        string
}

// Repo 是 notification_channels 表的仓储。
type Repo struct {
	clock state.Clock
}

// New 构造仓储。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

const selectCols = "id, name, kind, config_ciphertext, enabled, last_failure, created_at, updated_at"

// Create 落一条通道；重名 → ErrAlreadyExists（name 唯一索引把守）。
func (r *Repo) Create(ctx context.Context, run state.Runner, c *Channel) error {
	c.CreatedAt = state.FormatTime(r.clock.Now())
	c.UpdatedAt = c.CreatedAt
	_, err := run.ExecContext(ctx, `
		INSERT INTO notification_channels (id, name, kind, config_ciphertext, enabled, last_failure, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
		c.ID, c.Name, c.Kind, c.ConfigCiphertext, boolInt(c.Enabled), c.CreatedAt, c.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return state.ErrAlreadyExists
	}
	return err
}

// Get 按 ID 读行。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Channel, error) {
	row := run.QueryRowContext(ctx, "SELECT "+selectCols+" FROM notification_channels WHERE id = ?", id)
	return scanChannel(row)
}

// ListEnabled 列启用通道（派发面）。
func (r *Repo) ListEnabled(ctx context.Context, run state.Runner) ([]Channel, error) {
	return r.listWhere(ctx, run, "WHERE enabled = 1 ORDER BY id")
}

// ListAll 列全部通道（管理面）。
func (r *Repo) ListAll(ctx context.Context, run state.Runner) ([]Channel, error) {
	return r.listWhere(ctx, run, "ORDER BY id")
}

func (r *Repo) listWhere(ctx context.Context, run state.Runner, where string, args ...any) ([]Channel, error) {
	rows, err := run.QueryContext(ctx, "SELECT "+selectCols+" FROM notification_channels "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Channel
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
func (r *Repo) RecordDelivery(ctx context.Context, run state.Runner, id, failure string) error {
	_, err := run.ExecContext(ctx,
		"UPDATE notification_channels SET last_failure = ?, updated_at = ? WHERE id = ?",
		failure, state.FormatTime(r.clock.Now()), id)
	return err
}

// Delete 删行（不存在 → ErrNotFound）。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, "DELETE FROM notification_channels WHERE id = ?", id)
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

func scanChannel(row interface{ Scan(dest ...any) error }) (*Channel, error) {
	var c Channel
	var enabled int
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.ConfigCiphertext, &enabled, &c.LastFailure, &c.CreatedAt, &c.UpdatedAt)
	if state.MapScanErr(err) == state.ErrNotFound {
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
