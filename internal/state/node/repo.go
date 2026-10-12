// Package node 是 Node 观测缓存 repo（架构 §5：nodes 表只是观测缓存，
// 非权威——权威归属判定永远查平台表；Provider Watch 流的 node.joined/
// leave 事实在此落行，节点 ID 永不复用）。
package node

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Node 是一行观测缓存。
type Node struct {
	PlatformID  string
	Seq         int64
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

// MintPlatformID 铸下一平台节点 ID（node-<seq>；ADR-0070 独立类）。锚定
// 环先铸 ID → Provider 写回载体标记 → Upsert 落行（ID 是锚，铸造与落行
// 同事务由调用方组合；seq 构造性单调 = 节点 ID 永不复用）。
func (r *Repo) MintPlatformID(ctx context.Context, run state.Runner) (string, error) {
	seq, err := state.NextSeq(ctx, run, "nodes")
	if err != nil {
		return "", err
	}
	return entityid.Join("node", seq), nil
}

// Upsert 刷新一行观测（首见落 first_seen_at；available 变化即刷新）。
// 先 UPDATE（观测刷新不耗 seq 号），零行 = 首见 → 铸 seq 落行（ADR-0070）。
func (r *Repo) Upsert(ctx context.Context, run state.Runner, n *Node) error {
	now := state.FormatTime(r.clock.Now())
	n.LastSeenAt = now
	res, err := run.ExecContext(ctx, `
		UPDATE nodes SET carrier_id = ?, hostname = ?, role = ?, available = ?, last_seen_at = ?
		WHERE platform_id = ?`,
		n.CarrierID, n.Hostname, n.Role, n.Available, now, n.PlatformID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected > 0 {
		return nil
	}
	seq, err := state.NextSeq(ctx, run, "nodes")
	if err != nil {
		return err
	}
	n.Seq = seq
	_, err = run.ExecContext(ctx, `
		INSERT INTO nodes (platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.PlatformID, n.Seq, n.CarrierID, n.Hostname, n.Role, n.Available, now, now)
	return err
}

// List 返回全部观测行（seq 序 = 锚定序；引擎观测对账全量面——API 分页
// 读面走 ListPage）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Node, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// ListPage 是 List 的分页读面（ADR-0026 after_* + limit）。游标轴 = seq
// 锚定序升序。limit<=0 或 >200 回落/钳制缺省 50。
func (r *Repo) ListPage(ctx context.Context, run state.Runner, afterSeq int64, limit int) ([]Node, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes`
	args := []any{}
	if afterSeq > 0 {
		q += ` WHERE seq > ?`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Get 按 platform_id 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, platformID string) (*Node, error) {
	row := run.QueryRowContext(ctx, `
		SELECT platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes WHERE platform_id = ?`, platformID)
	return scanNode(row.Scan)
}

// ByCarrier 按载体节点 ID 反查（ADR-0049 中继握手绑定面：中继握手携带
// 载体节点 ID，平台锚定表反查平台节点 ID——与 Watch 锚定同一张表）。
func (r *Repo) ByCarrier(ctx context.Context, run state.Runner, carrierID string) (*Node, error) {
	row := run.QueryRowContext(ctx, `
		SELECT platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes WHERE carrier_id = ?`, carrierID)
	return scanNode(row.Scan)
}

func scanNode(scan func(dest ...any) error) (*Node, error) {
	var n Node
	var avail int
	if err := scan(&n.PlatformID, &n.Seq, &n.CarrierID, &n.Hostname, &n.Role, &avail, &n.FirstSeenAt, &n.LastSeenAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	n.Available = avail != 0
	return &n, nil
}
