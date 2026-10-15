// Package networkpeer 是 Network Peer 聚合 repo 的 SQLite 实现：行类型与
// Repo 接口住 domain/structure/networkpeer（ADR-0075 Repo 接口层），本包
// 只持不导出实现与构造。
package networkpeer

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain networkpeer.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain networkpeer.Repo）。
func New(clock state.Clock) networkpeer.Repo { return &repo{clock: clock} }

// Create 落声明行（挂靠方 declare）。同 (network, peer) 已有非 revoked 行
// → ErrAlreadyExists（pending/approved 在场不可重复声明；撤销后可再来——
// 新审批环）。
func (r *repo) Create(ctx context.Context, run state.Runner, p *networkpeer.Peer) error {
	seq, err := persistence.NextSeq(ctx, run, "network_peers")
	if err != nil {
		return err
	}
	p.Seq = seq
	if p.ID == "" {
		p.ID = entityid.Derive(p.NetworkID, "p", seq)
	}
	now := state.FormatTime(r.clock.Now())
	p.CreatedAt, p.UpdatedAt = now, now
	p.State = networkpeer.StatePending
	_, err = run.ExecContext(ctx, `
		INSERT INTO network_peers (id, seq, network_id, peer_project_id, state, created_at, updated_at, approved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
		p.ID, p.Seq, p.NetworkID, p.PeerProjectID, string(p.State), p.CreatedAt, p.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: network %s already has an active peer declaration from project %s",
			state.ErrAlreadyExists, p.NetworkID, p.PeerProjectID)
	}
	return err
}

// Get 按 ID 直读（含 revoked——终态事实可见）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*networkpeer.Peer, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanPeer(row.Scan)
}

// FindActive 按 (network, peer project) 读非 revoked 行（无 → ErrNotFound；
// pending/approved 均返回——批准态由调用方判定）。
func (r *repo) FindActive(ctx context.Context, run state.Runner, networkID, peerProjectID string) (*networkpeer.Peer, error) {
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE network_id = ? AND peer_project_id = ? AND state != 'revoked'",
		networkID, peerProjectID)
	return scanPeer(row.Scan)
}

// Approve 是接收方批准的 CAS（pending → approved，落 approved_at）。
// 前置不符 → ErrConflict（已批准/已撤销）。
func (r *repo) Approve(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE network_peers SET state = ?, approved_at = ?, updated_at = ?
		`+persistence.TransitWhere,
		string(networkpeer.StateApproved), now, now, id, string(networkpeer.StatePending))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := r.Get(ctx, run, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w: peer declaration %s is not pending", state.ErrConflict, id)
	}
	return nil
}

// Revoke 是撤销的 CAS（pending|approved → revoked）。已 revoked 由调用方
// 幂等短路（Get 判态），此处只负责单拍迁移。
func (r *repo) Revoke(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE network_peers SET state = ?, updated_at = ?
		WHERE id = ? AND state IN ('pending', 'approved')`,
		string(networkpeer.StateRevoked), state.FormatTime(r.clock.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := r.Get(ctx, run, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w: peer declaration %s is not revocable", state.ErrConflict, id)
	}
	return nil
}

// List 分页枚举（新→旧 + after 游标；ADR-0026）。network_id / peer_project_id
// 过滤可选——接收方与挂靠方两侧视图同面。
func (r *repo) List(ctx context.Context, run state.Runner, networkID, peerProjectID string, afterSeq int64, limit int) ([]networkpeer.Peer, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + " WHERE 1=1"
	var args []any
	if networkID != "" {
		q += " AND network_id = ?"
		args = append(args, networkID)
	}
	if peerProjectID != "" {
		q += " AND peer_project_id = ?"
		args = append(args, peerProjectID)
	}
	if afterSeq > 0 {
		q += " AND seq < ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// ListApprovedByPeerProject 返回挂靠方项目的全部 approved 声明（隔离扫描
// 面：受影响 App 的跨域附件批准态复核）。
func (r *repo) ListApprovedByPeerProject(ctx context.Context, run state.Runner, peerProjectID string) ([]networkpeer.Peer, error) {
	return r.query(ctx, run,
		selectCols+" WHERE peer_project_id = ? AND state = 'approved' ORDER BY seq", peerProjectID)
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, network_id, peer_project_id, state, created_at, updated_at, approved_at
	FROM network_peers`

func (r *repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]networkpeer.Peer, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []networkpeer.Peer
	for rows.Next() {
		p, err := scanPeer(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanPeer(scan func(dest ...any) error) (*networkpeer.Peer, error) {
	var p networkpeer.Peer
	var stateStr string
	if err := scan(&p.ID, &p.Seq, &p.NetworkID, &p.PeerProjectID, &stateStr, &p.CreatedAt, &p.UpdatedAt, &p.ApprovedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	p.State = networkpeer.State(stateStr)
	return &p, nil
}
