package database

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Database 聚合存取。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, run state.Runner, d *Database) error
	// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在）。
	Get(ctx context.Context, run state.Runner, id string) (*Database, error)
	// GetByName 在 Project 内按名读活跃行。
	GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Database, error)
	// ListByProject 新→旧分页（ADR-0026 after_* + limit；游标 = seq
	// 创建序）。
	ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]Database, error)
	// List 返回全部活跃行（收敛环的枚举面；按 id 升序稳定遍历）。
	List(ctx context.Context, run state.Runner) ([]Database, error)
	// ListRestorePending 返回恢复挂起中的活跃行（备份环恢复面的枚举面；
	// 挂起列在场即恢复在途，ADR-0039 决策 6）。
	ListRestorePending(ctx context.Context, run state.Runner) ([]Database, error)
	// CountByProject 返回 Project 内活跃行数（受理位配额口径）。
	CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error)
	// EnsureGeneration 是收敛环的下发编号口（ADR-0029 决策 3）：投影指纹
	// 与行上指纹一致 → 返回现行 gen（幂等重放同号，不触发载体滚动）；
	// 变化 → gen+1 与新指纹同事务落行并返回新号（Ensure 失败不回退——
	// 下一拍指纹已一致，同号干净重试）。已删/不存在返回
	// state.ErrNotFound（收敛环跳过）。
	EnsureGeneration(ctx context.Context, run state.Runner, id, fingerprint string) (uint64, error)
	// SetStatus 落观测状态（收敛环从观测缓存推进；同值写入不迁
	// updated_at 的守卫是实现细节）。命中 0 行静默返回（观测迟到于删除）。
	SetStatus(ctx context.Context, run state.Runner, id, status string) error
	// SoftDelete 落 tombstone（幂等口径与 Get 一致：已删 = 不存在 →
	// 再删 state.ErrNotFound）。
	SoftDelete(ctx context.Context, run state.Runner, id string) error
	// SetLastBackupAt 落调度锚（成功 Backup 的完成时刻；幂等写）。
	SetLastBackupAt(ctx context.Context, run state.Runner, id, at string) error
	// SetRestorePending 落恢复挂起（API 创建面消费：restore_from_backup
	// 指向一颗成功备份行；备份环执行后清位）。
	SetRestorePending(ctx context.Context, run state.Runner, id, backupID string) error
	// SetRestoreError 落恢复失败事实（挂起清位 + error 留痕；半恢复态
	// 重试不可幂等，诚实留给用户重建——ADR-0039 决策 6）。
	SetRestoreError(ctx context.Context, run state.Runner, id, errMsg string) error
	// ClearRestoreSucceeded 清恢复挂起（成功路径；restore_error 不动
	// ——历史事实保留）。
	ClearRestoreSucceeded(ctx context.Context, run state.Runner, id string) error
}
