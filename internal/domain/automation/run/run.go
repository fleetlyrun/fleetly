// Package run 是 Run 聚合的领域面（CONTEXT.md Run 词条：Task 的一次执行，
// 产出结果、日志与停止原因）。状态机 pending → running → stopping →
// stopped | failed（ADR-0012）；终态携带停止原因七枚举。TTL 与停止收口
// 兜底共用绝对 deadline 列（ADR-0018 墙钟续算；stopping 迁移时覆写为
// 停止收口截止）。行类型、状态机词表与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/run（ADR-0075 Repo 接口层路线）。
package run

// State 是 Run 状态机值（ADR-0012）。
type State string

const (
	StatePending  State = "pending"  // 行已落，载体未下发
	StateRunning  State = "running"  // 载体观测 running
	StateStopping State = "stopping" // 停止已请求（SIGTERM + StopGrace 路径）
	StateStopped  State = "stopped"  // 终态（携带起因）
	StateFailed   State = "failed"   // 终态（进程失败）
)

// Terminal 报告是否终态。
func (s State) Terminal() bool {
	return s == StateStopped || s == StateFailed
}

// Active 报告是否活跃态（pending/running；stopping 是收口态——不再计入
// 期望并发的活槽位，但仍在驱动集合内）。
func (s State) Active() bool {
	return s == StatePending || s == StateRunning
}

// ActiveStates 是 Active() 的集合形态——CAS from-state 集与
// ListByTaskStates 过滤集共用（stopping 不在列：收口态不占活槽位）。
// ADR-0063 单源收编 engine 六处字面量；共享切片只读（Transit/runStateIn
// 均不持留不变更）。
var ActiveStates = []State{StatePending, StateRunning}

// StopReason 是停止原因七枚举（ADR-0012；终态携带，snake_case 冻结）。
const (
	ReasonCompleted       = "completed"        // 自然退出（退出码 0）
	ReasonFailed          = "failed"           // 进程失败（退出码非 0）
	ReasonStoppedByUser   = "stopped_by_user"  // 用户/API 停止
	ReasonTTLExpired      = "ttl_expired"      // TTL 到期
	ReasonLeaseExpired    = "lease_expired"    // Owner Lease 失联超宽限
	ReasonOwnerRevoked    = "owner_revoked"    // 属主 Token 吊销 → 宽限排空
	ReasonPlatformDrained = "platform_drained" // 平台排空（节点排空/载体被平台移除）
)

// Run 是聚合行（Run 行不删——审计单位；终态行的载体由收敛移除）。
type Run struct {
	ID         string
	Seq        int64
	TaskID     string
	ProjectID  string
	State      State
	StopReason string // 七枚举；stopping 起因即写入，终态携带
	ExitCode   *int   // 终态退出码（nil = 未观测）
	WorkloadID string // Run 载体 Workload ID（观测对账锚）
	DNSName    string // per-Run 稳定 DNS（engine 铸名）
	Deadline   string // 双语义绝对 deadline：活跃 = TTL 截止；stopping = 停止收口兜底截止
	CreatedAt  string
	UpdatedAt  string
	StartedAt  string
	FinishedAt string
}
