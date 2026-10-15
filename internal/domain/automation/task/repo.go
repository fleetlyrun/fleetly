package task

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Task 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行（活跃名唯一索引防项目内重名 → ErrAlreadyExists）。
	Create(ctx context.Context, run state.Runner, t *Task) error
	// Get 按 ID 直读（tombstone 行照常返回——终态事实可见）。
	Get(ctx context.Context, run state.Runner, id string) (*Task, error)
	// GetByName 按项目内名直读（活跃名唯一；无 → ErrNotFound）。
	GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Task, error)
	// ListByProject 返回项目内 Task（新→旧 + after 游标；ADR-0026 after_* + limit
	// 惯例——游标 = seq 创建序）。
	ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]Task, error)
	// ListDriving 返回全部待驱动行（active/draining；tombstone 与终态不拾取）。
	ListDriving(ctx context.Context, run state.Runner) ([]Task, error)
	// ListRecentlyFinished 返回收口时刻在窗内的终态 Task（收尾批 E29-2 残留
	// 载体清扫的候选集）。deleted 不在列——其载体拆除是 DeleteTask 自身的
	// Remove 步（先收口后落账），tombstone 无残留面（词引用类型常量单源，
	// 状态词变更不腐化）。finished_at 是 RFC3339
	// UTC 串（state.FormatTime），字典序即时序。limit 钳制与 ListByProject
	// 同口径（<=0 或超上限回落/钳缺省）。新→旧序（finished_at DESC）：近期
	// 收口的行才是残留的实际所在（崩溃/失败窗口），旧行近乎必然已收敛。
	ListRecentlyFinished(ctx context.Context, run state.Runner, since string, limit int) ([]Task, error)
	// ListByOwner 返回属主 Token 名下活跃行（吊销排空的拉式扫描面，P1-8）。
	ListByOwner(ctx context.Context, run state.Runner, tokenID string) ([]Task, error)
	// StatsByProject 返回 Project 内非终态 Task 的配额口径统计（ADR-0017
	// 附录 A.1）：活跃行数与 desired_concurrency 之和（受理位与引擎配额检查
	// 共用，事务内读）。非终态 = 终态词表取反（TerminalStateStrings 单源）。
	StatsByProject(ctx context.Context, run state.Runner, projectID string) (active int, desiredSum int64, err error)
	// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
	// lease_deadline/desired_concurrency 等字段）。前置不符 → ErrConflict；
	// 行不存在 → ErrNotFound。UPDATE 带 state 前置条件作并发防御纵深。
	Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Task)) error
	// UpdateLease 更新租约 deadline（原地：state 不变，ADR-0024 原地迁移不发
	// 事件的同款口径——lease 续期由 lease.renewed 事件显式承载）。
	UpdateLease(ctx context.Context, run state.Runner, id, deadline string) error
}
