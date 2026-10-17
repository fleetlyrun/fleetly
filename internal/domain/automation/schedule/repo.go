package schedule

import (
	"context"
)

// Repo 是 Schedule 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行（活跃名唯一索引防项目内重名 →
	// state.ErrAlreadyExists）。
	Create(ctx context.Context, s *Schedule) error
	// Get 按 ID 直读（tombstone 行照常返回——终态事实可见）。
	Get(ctx context.Context, id string) (*Schedule, error)
	// GetByName 按项目内名直读（活跃名唯一；无 → state.ErrNotFound）。
	GetByName(ctx context.Context, projectID, name string) (*Schedule, error)
	// ListByProject 返回项目内 Schedule（新→旧 + after 游标；ADR-0026
	// after_* + limit 惯例——游标 = seq 创建序）。
	ListByProject(ctx context.Context, projectID string, afterSeq int64, limit int) ([]Schedule, error)
	// ListDriving 返回全部活跃行（到期判定由调用方按墙钟解析比较）。
	ListDriving(ctx context.Context) ([]Schedule, error)
	// Fire 是到期拍/跳过后推进 next_fire_at 的 CAS：仅当行活跃且
	// next_fire_at 仍为 from 时落 next 与 last_task_id。前置不符 →
	// state.ErrConflict（并发已拍/已删——与 Task 落行同事务时整体回滚，
	// 双发防御纵深）；行不存在 → state.ErrNotFound。
	Fire(ctx context.Context, id, from, next, lastTaskID string) error
	// RecordFire 是手动触发的记账面：last_task_id 推进、next_fire_at 不动
	// （cron 节奏不被手动拍打乱）。fromLastTaskID 是 CAS 锚（调用方读行
	// 时的旧值，B12 P3-4）：并发双拍时输家的锚失配 →
	// state.ErrConflict——与 Task 落行同事务整体回滚，双铸防御。行不
	// 活跃 → state.ErrConflict；不存在 → state.ErrNotFound。
	RecordFire(ctx context.Context, id, fromLastTaskID, lastTaskID string) error
	// Transit 是状态 CAS（active → deleted 的 tombstone 面；单前置态——
	// Schedule 无中间态迁移）。
	Transit(ctx context.Context, id string, from, to State) error
}
