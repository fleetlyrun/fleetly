package networkpeer

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Network Peer 聚合存取。方法集机械平移自既有 repo 导出面；
// Runner 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可
// 组合进四件一拍事务，ADR-0005）。
type Repo interface {
	// Create 落声明行（挂靠方 declare）。同 (network, peer) 已有非 revoked
	// 行 → ErrAlreadyExists（pending/approved 在场不可重复声明；撤销后可
	// 再来——新审批环）。
	Create(ctx context.Context, run state.Runner, p *Peer) error
	// Get 按 ID 直读（含 revoked——终态事实可见）。
	Get(ctx context.Context, run state.Runner, id string) (*Peer, error)
	// FindActive 按 (network, peer project) 读非 revoked 行（无 →
	// ErrNotFound；pending/approved 均返回——批准态由调用方判定）。
	FindActive(ctx context.Context, run state.Runner, networkID, peerProjectID string) (*Peer, error)
	// Approve 是接收方批准的 CAS（pending → approved，落 approved_at）。
	// 前置不符 → ErrConflict（已批准/已撤销）。
	Approve(ctx context.Context, run state.Runner, id string) error
	// Revoke 是撤销的 CAS（pending|approved → revoked）。已 revoked 由
	// 调用方幂等短路（Get 判态），此处只负责单拍迁移。
	Revoke(ctx context.Context, run state.Runner, id string) error
	// List 分页枚举（新→旧 + after 游标；ADR-0026）。network_id /
	// peer_project_id 过滤可选——接收方与挂靠方两侧视图同面。
	List(ctx context.Context, run state.Runner, networkID, peerProjectID string, afterSeq int64, limit int) ([]Peer, error)
	// ListApprovedByPeerProject 返回挂靠方项目的全部 approved 声明（隔离
	// 扫描面：受影响 App 的跨域附件批准态复核）。
	ListApprovedByPeerProject(ctx context.Context, run state.Runner, peerProjectID string) ([]Peer, error)
}
