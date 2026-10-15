// Package node 是 Node 观测缓存 repo 的 SQLite 实现：行类型与 Repo 接口
// 住 domain/runtimeops/node（ADR-0075 Repo 接口层），本包只持不导出实现
// 与构造。
package node

import (
	"context"
	"errors"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain node.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain node.Repo）。
func New(clock state.Clock) node.Repo { return &repo{clock: clock} }

// MintPlatformID 铸下一平台节点 ID（node-<seq>；ADR-0070 独立类）。锚定
// 环先铸 ID → Provider 写回载体标记 → Upsert 落行（ID 是锚，铸造与落行
// 同事务由调用方组合；seq 构造性单调 = 节点 ID 永不复用）。
func (r *repo) MintPlatformID(ctx context.Context, run state.Runner) (string, error) {
	seq, err := persistence.NextSeq(ctx, run, "nodes")
	if err != nil {
		return "", err
	}
	return entityid.Join("node", seq), nil
}

// seenRefreshInterval 是观测刷新的最小落笔间隔（P0-7.4）：对账节拍每秒
// 一拍，无条件 UPDATE last_seen_at=now 会把空闲 WAL 撑成每节点每天 ~86k
// 次写——值未变且 last_seen 仍在窗内即零写。
const seenRefreshInterval = 30 * time.Second

// Upsert 刷新一行观测（首见落 first_seen_at）。落笔条件：观测值变化
// （carrier/hostname/role/available）或 last_seen 距上次刷新超过
// seenRefreshInterval——"值未变也恒写"与观测缓存语义不符（本注释曾宣称
// "available 变化即刷新"，代码却恒写，P0-7.4 对齐）。先 UPDATE（观测刷新
// 不耗 seq 号），零行 = 首见 → 铸 seq 落行（ADR-0070）。
func (r *repo) Upsert(ctx context.Context, run state.Runner, n *node.Node) error {
	var firstSeen string
	if existing, err := r.Get(ctx, run, n.PlatformID); err == nil {
		firstSeen = existing.FirstSeenAt
		if existing.CarrierID == n.CarrierID && existing.Hostname == n.Hostname &&
			existing.Role == n.Role && existing.Available == n.Available &&
			freshSeen(existing.LastSeenAt, r.clock.Now()) {
			// 零写路径：调用方行镜像同步成库内事实（行为面与写路径一致）。
			n.Seq = existing.Seq
			n.FirstSeenAt = existing.FirstSeenAt
			n.LastSeenAt = existing.LastSeenAt
			return nil
		}
	} else if !errors.Is(err, state.ErrNotFound) {
		return err
	}
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
		n.FirstSeenAt = firstSeen
		return nil
	}
	seq, err := persistence.NextSeq(ctx, run, "nodes")
	if err != nil {
		return err
	}
	n.Seq = seq
	n.FirstSeenAt = now
	_, err = run.ExecContext(ctx, `
		INSERT INTO nodes (platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.PlatformID, n.Seq, n.CarrierID, n.Hostname, n.Role, n.Available, now, now)
	return err
}

// freshSeen 报告 last_seen 是否仍在刷新窗内（行内时间戳不可解析按陈旧
// 处理——保守落笔，观测缓存可自愈）。
func freshSeen(lastSeen string, now time.Time) bool {
	ts, err := time.Parse(time.RFC3339, lastSeen)
	return err == nil && now.Sub(ts) < seenRefreshInterval
}

// List 返回全部观测行（seq 序 = 锚定序；引擎观测对账全量面——API 分页
// 读面走 ListPage）。
func (r *repo) List(ctx context.Context, run state.Runner) ([]node.Node, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []node.Node
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
func (r *repo) ListPage(ctx context.Context, run state.Runner, afterSeq int64, limit int) ([]node.Node, error) {
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
	var out []node.Node
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
func (r *repo) Get(ctx context.Context, run state.Runner, platformID string) (*node.Node, error) {
	row := run.QueryRowContext(ctx, `
		SELECT platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes WHERE platform_id = ?`, platformID)
	return scanNode(row.Scan)
}

// ByCarrier 按载体节点 ID 反查（ADR-0049 中继握手绑定面：中继握手携带
// 载体节点 ID，平台锚定表反查平台节点 ID——与 Watch 锚定同一张表）。
func (r *repo) ByCarrier(ctx context.Context, run state.Runner, carrierID string) (*node.Node, error) {
	row := run.QueryRowContext(ctx, `
		SELECT platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at
		FROM nodes WHERE carrier_id = ?`, carrierID)
	return scanNode(row.Scan)
}

func scanNode(scan func(dest ...any) error) (*node.Node, error) {
	var n node.Node
	var avail int
	if err := scan(&n.PlatformID, &n.Seq, &n.CarrierID, &n.Hostname, &n.Role, &avail, &n.FirstSeenAt, &n.LastSeenAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	n.Available = avail != 0
	return &n, nil
}
