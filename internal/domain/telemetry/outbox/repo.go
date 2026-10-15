package outbox

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Outbox 聚合存取（只增：事件是既成事实，无 UPDATE/DELETE——保留
// 窗内；见 TrimBefore）。方法集机械平移自既有 repo 导出面（含 P0-2 的
// team 过滤参数形态）；Runner 一律由调用方注入（*sql.DB 或 *sql.Tx 均
// 满足，可独立执行也可组合进四件一拍事务，ADR-0005）。
type Repo interface {
	// Append 落一条事件并返回其单调 seq。team 是资源轴归属 Team（P0-2）：
	// ” = 平台级事件（对全部 Team 可见）。事件名未注册 → 明确报错（fail-fast
	// 于调用方测试，而非静默落库污染订阅面）。
	Append(ctx context.Context, run state.Runner, team, name, aggregate, aggregateID string, payload []byte) (int64, error)
	// ListAfter 返回 seq 之后的事件（升序；limit 上界钳制）。team 非空 =
	// 行级过滤（team_id = team 或 ”平台级行——与 audit 面 ADR-0035 决策 6
	// 同口径）；team 空 = 不过滤（平台 owner / 内部消费面，P0-2）。
	ListAfter(ctx context.Context, run state.Runner, afterSeq int64, limit int, team string) ([]Event, error)
	// LastSeq 返回当前最大 seq（空表 = 0；快照重同步端点的基准）。
	LastSeq(ctx context.Context, run state.Runner) (int64, error)
	// EarliestSeq 返回保留窗内最早事件 seq（空表 = 0）。断档判定锚：订阅方
	// after_seq < earliest 即落窗外（410 + 快照重同步，ADR-0026）。
	EarliestSeq(ctx context.Context, run state.Runner) (int64, error)
	// TrimBefore 删除保留窗外的全部行（janitor 周期执行；返回清理行数供
	// 观测）。事件既成事实落审计后由本面回收——"只增"修订为"保留窗内只增"
	// （ADR-0026：订阅契约以 earliest_seq 划界，窗内不删不改）。
	TrimBefore(ctx context.Context, run state.Runner, cutoff time.Time) (int64, error)
}
