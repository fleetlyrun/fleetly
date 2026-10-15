package run

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Run 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律由
// 调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行。
	Create(ctx context.Context, run state.Runner, m *Run) error
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*Run, error)
	// ListByTask 返回 Task 名下 Run（新→旧 + after 游标；ADR-0026 惯例）。
	ListByTask(ctx context.Context, run state.Runner, taskID string, afterSeq int64, limit int) ([]Run, error)
	// ListByProject 返回 Project 内 Run（新→旧 + after 游标；ADR-0035 ListRuns
	// 的 project 过滤面——runs 行自带冗余 project_id，免二跳）。
	ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]Run, error)
	// ListDriving 返回全部待驱动行（pending/running/stopping）。
	ListDriving(ctx context.Context, run state.Runner) ([]Run, error)
	// ListDrivingOfTerminalTasks 返回终态 Task 名下的 driving 态 Run（僵尸
	// 收口面：正常链路不产生，pre-fix 存量行在此显形——Schedule 重叠判定以
	// Run 行为真源，僵尸行会让该 Schedule 永久 skip）。终态词表单源引用
	// task.TerminalStateStrings（deployment.ActiveStateStrings 同款先例——
	// 词表变更编译期咬合，不再手抄腐化）。
	ListDrivingOfTerminalTasks(ctx context.Context, run state.Runner) ([]Run, error)
	// ListByTaskStates 返回 Task 名下指定状态的 Run（新→旧，与 ListByTask
	// 同一惯例；顺序是过量排空的方向锚——升序会把"停新保老"倒置成停老保新，
	// 2026-10-02 实证缺陷）。
	ListByTaskStates(ctx context.Context, run state.Runner, taskID string, states []State) ([]Run, error)
	// CountActiveByTasks 返回各 Task 的活 Run 计数（pending/running；列表投影
	// 的 active_run_count 单查询面——避免 N+1）。
	CountActiveByTasks(ctx context.Context, run state.Runner) (map[string]int, error)
	// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
	// stop_reason/exit_code/deadline 等字段）。前置不符 → ErrConflict；行不
	// 存在 → ErrNotFound。UPDATE 带 state 前置条件作并发防御纵深。
	Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Run)) error
}
