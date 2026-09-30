// Package node 是 Node 观测缓存 repo（架构 §5：nodes 表只是观测缓存，
// 非权威——权威归属判定永远查平台表；Provider Watch 流的 node.joined/
// leave 事实在此落行，节点 ID 永不复用）。
package node

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Node 是一行观测缓存。
type Node struct {
	PlatformID  string
	CarrierID   string
	Hostname    string
	Role        string
	Available   bool
	FirstSeenAt string
	LastSeenAt  string
}

// Repo 是观测缓存存取（Upsert 语义：观测刷新非状态机迁移）。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Upsert 刷新一行观测（首见落 first_seen_at；available 变化即刷新）。
func (r *Repo) Upsert(ctx context.Context, run state.Runner, n *Node) error {
	now := state.FormatTime(r.clock.Now())
	n.LastSeenAt = now
	_, err := run.ExecContext(ctx, `
		INSERT INTO nodes (platform_id, carrier_id, hostname, role, available, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform_id) DO UPDATE SET
			carrier_id = excluded.carrier_id,
			hostname = excluded.hostname,
			role = excluded.role,
			available = excluded.available,
			last_seen_at = excluded.last_seen_at`,
		n.PlatformID, n.CarrierID, n.Hostname, n.Role, n.Available, now, n.LastSeenAt)
	return err
}

// List 返回全部观测行（platform_id 序）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Node, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT platform_id, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes ORDER BY platform_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Node
	for rows.Next() {
		var n Node
		var avail int
		if err := rows.Scan(&n.PlatformID, &n.CarrierID, &n.Hostname, &n.Role, &avail, &n.FirstSeenAt, &n.LastSeenAt); err != nil {
			return nil, err
		}
		n.Available = avail != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// Get 按 platform_id 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, platformID string) (*Node, error) {
	row := run.QueryRowContext(ctx, `
		SELECT platform_id, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes WHERE platform_id = ?`, platformID)
	var n Node
	var avail int
	err := row.Scan(&n.PlatformID, &n.CarrierID, &n.Hostname, &n.Role, &avail, &n.FirstSeenAt, &n.LastSeenAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	n.Available = avail != 0
	return &n, nil
}
