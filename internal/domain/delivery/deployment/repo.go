package deployment

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// Repo 是 Deployment 聚合存取。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一行（活跃态幂等键唯一索引防并发重复入队 → ErrAlreadyExists）。
	// ID 空则装配 "<app id>-<seq>"；Seq 零值则事务内取 MAX+1（ADR-0070）。
	Create(ctx context.Context, run state.Runner, d *Deployment) error
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*Deployment, error)
	// ListByApp 返回 App 全部 Deployment（新→旧 + after 游标；ADR-0026
	// after_* + limit 惯例——游标 = seq 创建序）。limit<=0 或 >200 回落/钳制
	// 缺省 50。
	ListByApp(ctx context.Context, run state.Runner, appID string, afterSeq int64, limit int) ([]Deployment, error)
	// ActiveByApp 返回 App 全部活跃态 Deployment（应至多一条在途 + 任意排队）。
	ActiveByApp(ctx context.Context, run state.Runner, appID string) ([]Deployment, error)
	// FindActiveByIdempotencyKey 返回该 App 内持该键的活跃 Deployment（无 →
	// ErrNotFound）。键的作用域是 App（B10）：异 App 同键不命中——作用域
	// 收紧后的正确语义，异 App 的键不在本 App 作用域内；存量迁移窗口若出现
	// 跨 App 同键活跃行，对本 App 亦视同无键。
	FindActiveByIdempotencyKey(ctx context.Context, run state.Runner, appID, key string) (*Deployment, error)
	// ListDriving 返回全部待驱动行：活跃态 + 待自动回滚的 failed（领域模型
	// §4：failed → (自动) rolling-back；rollback_attempted=1 或无 from_revision
	// 的 failed 是终态，不拾取）。
	ListDriving(ctx context.Context, run state.Runner) ([]Deployment, error)
	// NextGeneration 返回 App 下一 Generation（单调；Drift 判定与幂等重放的锚）。
	NextGeneration(ctx context.Context, run state.Runner, appID string) (uint64, error)
	// LatestSucceeded 返回 App 最近一次 succeeded 的 Deployment（ADR-0022
	// 启动基线重放的锚；无成功基线 → ErrNotFound）。
	LatestSucceeded(ctx context.Context, run state.Runner, appID string) (*Deployment, error)
	// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
	// error/finished_at/generation/superseded_by/observe_deadline 等字段）。
	// 前置不符 → ErrConflict；行不存在 → ErrNotFound（四件一拍的"状态"件；
	// UPDATE 带 state 前置条件作并发防御纵深，即使未来放开连接池也安全）。
	Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Deployment)) error
}
