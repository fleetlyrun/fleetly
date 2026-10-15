// Package task 是 Task 聚合的领域面（CONTEXT.md Task 词条：程序化工作负载，
// 双形态 one-shot/resident；ADR-0012/0025）：行类型、状态机词表与 Repo
// 接口在此单源，SQLite 实现住 infrastructure/persistence/task（ADR-0075
// Repo 接口层路线）。行本身即受理真源（spec 冻结 protojson）；状态迁移
// 一律经 Transit CAS（四件一拍的"状态"件，Outbox/审计由调用方同事务组合）。
// TTL 与 Owner Lease 的绝对 deadline 语义在 Run 行（ADR-0018 墙钟续算）。
package task

// State 是 Task 状态机值（ADR-0012：双形态生命周期）。
type State string

const (
	StateActive    State = "active"    // 保温中（resident 补足维持 desired；one-shot 等其唯一 Run 终态）
	StateDraining  State = "draining"  // 排空中：停止补足，存量 Run 按起因收口
	StateCompleted State = "completed" // 终态（one-shot 的 Run 自然完成）
	StateFailed    State = "failed"    // 终态（one-shot 的 Run 失败）
	StateDrained   State = "drained"   // 终态（排空完成：lease_expired / owner_revoked / 用户排空）
	StateDeleted   State = "deleted"   // 终态 tombstone（显式删除；载体已拆）
)

// Terminal 报告是否终态（终态行不再参与驱动）。
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateDrained, StateDeleted:
		return true
	}
	return false
}

// TerminalStates 是全部终态（deployment.ActiveStates 同款集合形态）。
func TerminalStates() []State {
	return []State{StateCompleted, StateFailed, StateDrained, StateDeleted}
}

// TerminalStateStrings 是终态的 SQL IN 参数形态（deployment.
// ActiveStateStrings 同款先例）：run 聚合的僵尸收口子查询与本包配额
// 口径共用——词表单源，task 状态词变更不再手抄腐化。
func TerminalStateStrings() []string {
	out := make([]string, len(TerminalStates()))
	for i, s := range TerminalStates() {
		out[i] = string(s)
	}
	return out
}

// Form 是双形态值（CONTEXT.md Task 词条；kebab-case 与存储一致）。
const (
	FormOneShot  = "one-shot"
	FormResident = "resident"
)

// Task 是聚合行（Task 行不删——审计单位；deleted 是 tombstone）。
type Task struct {
	ID                 string
	Seq                int64
	ProjectID          string
	Name               string
	Form               string // one-shot | resident（spec.Form 同值）
	State              State
	Spec               []byte // 冻结 TaskSpec（protojson）
	OwnerTokenID       string // 属主 Token 行引用（非明文，P1-8）
	DesiredConcurrency int64
	NetworkGroup       string
	DNSName            string // per-Task 池级稳定 DNS（engine 铸名）
	LeaseDeadline      string // RFC3339 绝对 deadline（空 = 无租约约束）
	CreatedAt          string
	UpdatedAt          string
	FinishedAt         string
}
