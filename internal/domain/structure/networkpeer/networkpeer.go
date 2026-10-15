// Package networkpeer 是 Network Peer 聚合的领域面（ADR-0013 附录 A.1，
// F1.8）：一条跨 Project 挂靠声明——挂靠方项目 declare（pending）→
// 接收方 approve（approved）→ 任一侧 revoke（revoked；重新挂靠走新声明
// 行，旧 revoked 行留作审计事实）。唯一性 (network_id, peer_project_id)
// 只在非 revoked 行间成立（部分唯一索引）。与 network 聚合二跳锚强耦
// （归属解析链 peer → network → project）。行类型与状态机词表、Repo
// 接口在此单源，SQLite 实现住 infrastructure/persistence/networkpeer
// （ADR-0075 Repo 接口层）。
package networkpeer

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
	Seq           int64
	NetworkID     string
	PeerProjectID string
	State         State
	CreatedAt     string
	UpdatedAt     string
	ApprovedAt    string
}
