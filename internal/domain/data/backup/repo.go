package backup

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Backup 台账存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行 pending（调度与手动触发共用；object_key 执行成功后
	// 回填）。
	Create(ctx context.Context, run state.Runner, b *Backup) error
	// Get 按 ID 读一行。
	Get(ctx context.Context, run state.Runner, id string) (*Backup, error)
	// ListByDatabase 新→旧分页（ADR-0026 after_* + limit 惯例；含终态与
	// 在途）。
	ListByDatabase(ctx context.Context, run state.Runner, databaseID string, afterSeq int64, limit int) ([]Backup, error)
	// OldestPending 返回最早的一行 pending（执行器单飞取件；无在途即
	// 空）。in-flight 排他：同库存在 running 行时返回 ok=false（串行语义
	// 在环层，本查询兜底防同库双在途）。
	OldestPending(ctx context.Context, run state.Runner) (*Backup, bool, error)
	// HasActiveFor 报告库是否存在在途行（pending|running——调度面防同库
	// 双行）。
	HasActiveFor(ctx context.Context, run state.Runner, databaseID string) (bool, error)
	// LatestFailedFor 返回该库最新一行失败（退避锚——失败不推进
	// last_backup_at，重试节奏由本行 FinishedAt 承载；id 是 ULID 时序，
	// 走 (database_id, id) 索引序即时间序。无失败行即 ok=false）。
	LatestFailedFor(ctx context.Context, run state.Runner, databaseID string) (*Backup, bool, error)
	// DueForFailedRowSweep 返回失败行保留窗外的行（失败行无对象产物，
	// 仅删行——错误文本的诊断价值短命，48h 窗后由保留滚动同环清扫；
	// created_at 升序限量分拍，DueForPrune 同款形态）。
	DueForFailedRowSweep(ctx context.Context, run state.Runner, now time.Time, retention time.Duration, limit int) ([]Backup, error)
	// MarkRunning 摘件落执行态（CAS：仅 pending 行命中）。
	MarkRunning(ctx context.Context, run state.Runner, id string) error
	// FinishSucceeded 回填成功（ObjectStore 回执三元组 + 终态；CAS：仅
	// running 行命中——终态是事实，启动清扫 SweepInterrupted 落 failed
	// 后的迟到成功回执按 state.ErrConflict 拒绝，不得覆写）。
	FinishSucceeded(ctx context.Context, run state.Runner, id, objectKey, digest string, size int64) error
	// FinishFailed 落失败终态（error 用户可见文本英文；CAS：pending/
	// running 行命中——失败回执在 MarkRunning 前后都合法，唯终态不可
	// 覆写，同 FinishSucceeded 口径）。
	FinishFailed(ctx context.Context, run state.Runner, id, errMsg string) error
	// SweepInterrupted 把重启打断的 running 行落 failed（引擎启动清扫；
	// 返回清扫行数供日志）。
	SweepInterrupted(ctx context.Context, run state.Runner) (int64, error)
	// DueForPrune 返回保留窗外的行（行驱动保留滚动，ADR-0039 决策 7）：
	// 按 created_at 升序取前 lookahead 行、Go 侧按行上 retention 快照
	// 过滤（窗在行上逐行不同，SQL 单 cutoff 表达不了；升序保证到期行必
	// 在前沿，清扫跟得上即 O(到期行+前瞻)，积压窗拖不垮环——限量分拍）。
	DueForPrune(ctx context.Context, run state.Runner, now time.Time, lookahead int) ([]Backup, error)
	// Delete 删一行（保留滚动收口；与对象删除由调用方成对执行）。
	Delete(ctx context.Context, run state.Runner, id string) error
}
