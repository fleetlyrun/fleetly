// Package networkpeer 是 Network Peer 聚合 repo（ADR-0013 附录 A.1，
// F1.8）：一条跨 Project 挂靠声明——挂靠方项目 declare（pending）→
// 接收方 approve（approved）→ 任一侧 revoke（revoked；重新挂靠走新声明
// 行，旧 revoked 行留作审计事实）。唯一性 (network_id, peer_project_id)
// 只在非 revoked 行间成立（部分唯一索引）。
package networkpeer

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// State 是声明状态机值（kebab 拼写冻结，与 Task/Schedule 同款）。
type State string

const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateRevoked  State = "revoked"
)

// Peer 是聚合行（revoked 行不删——审批历史是审计事实）。
type Peer struct {
	ID            string
	NetworkID     string
	PeerProjectID string
	State         State
	CreatedAt     string
	UpdatedAt     string
	ApprovedAt    string
}

// Repo 是 Network Peer 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落声明行（挂靠方 declare）。同 (network, peer) 已有非 revoked 行
// → ErrAlreadyExists（pending/approved 在场不可重复声明；撤销后可再来——
// 新审批环）。
func (r *Repo) Create(ctx context.Context, run state.Runner, p *Peer) error {
	now := state.FormatTime(r.clock.Now())
	p.CreatedAt, p.UpdatedAt = now, now
	p.State = StatePending
	_, err := run.ExecContext(ctx, `
		INSERT INTO network_peers (id, network_id, peer_project_id, state, created_at, updated_at, approved_at)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		p.ID, p.NetworkID, p.PeerProjectID, string(p.State), p.CreatedAt, p.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: network %s already has an active peer declaration from project %s",
			state.ErrAlreadyExists, p.NetworkID, p.PeerProjectID)
	}
	return err
}

// Get 按 ID 直读（含 revoked——终态事实可见）。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Peer, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanPeer(row.Scan)
}

// FindActive 按 (network, peer project) 读非 revoked 行（无 → ErrNotFound；
// pending/approved 均返回——批准态由调用方判定）。
func (r *Repo) FindActive(ctx context.Context, run state.Runner, networkID, peerProjectID string) (*Peer, error) {
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE network_id = ? AND peer_project_id = ? AND state != 'revoked'",
		networkID, peerProjectID)
	return scanPeer(row.Scan)
}

// Approve 是接收方批准的 CAS（pending → approved，落 approved_at）。
// 前置不符 → ErrConflict（已批准/已撤销）。
func (r *Repo) Approve(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE network_peers SET state = ?, approved_at = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		string(StateApproved), now, now, id, string(StatePending))
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
func (r *Repo) Revoke(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE network_peers SET state = ?, updated_at = ?
		WHERE id = ? AND state IN ('pending', 'approved')`,
		string(StateRevoked), state.FormatTime(r.clock.Now()), id)
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
func (r *Repo) List(ctx context.Context, run state.Runner, networkID, peerProjectID, afterID string, limit int) ([]Peer, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
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
	if afterID != "" {
		q += " AND id < ?"
		args = append(args, afterID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// ListApprovedByPeerProject 返回挂靠方项目的全部 approved 声明（隔离扫描
// 面：受影响 App 的跨域附件批准态复核）。
func (r *Repo) ListApprovedByPeerProject(ctx context.Context, run state.Runner, peerProjectID string) ([]Peer, error) {
	return r.query(ctx, run,
		selectCols+" WHERE peer_project_id = ? AND state = 'approved' ORDER BY id", peerProjectID)
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, network_id, peer_project_id, state, created_at, updated_at, approved_at
	FROM network_peers`

func (r *Repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]Peer, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Peer
	for rows.Next() {
		p, err := scanPeer(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanPeer(scan func(dest ...any) error) (*Peer, error) {
	var p Peer
	var stateStr string
	if err := scan(&p.ID, &p.NetworkID, &p.PeerProjectID, &stateStr, &p.CreatedAt, &p.UpdatedAt, &p.ApprovedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	p.State = State(stateStr)
	return &p, nil
}
