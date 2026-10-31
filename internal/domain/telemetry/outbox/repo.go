package outbox

import (
	"context"
	"time"
)

// Repo 是 Outbox 聚合存取（只增：事件是既成事实，无 UPDATE/DELETE——保留
// 窗内；见 TrimBefore）。方法集机械平移自既有 repo 导出面（含 P0-2 的
// team 过滤参数形态）。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Append 落一条事件并返回其单调 seq。team 是资源轴归属 Team（P0-2）：
	// ” = 平台级事件（对全部 Team 可见）。事件名未注册 → 明确报错（fail-fast
	// 于调用方测试，而非静默落库污染订阅面）。
	Append(ctx context.Context, team, name, aggregate, aggregateID string, payload []byte) (int64, error)
	// ListAfter 返回 seq 之后的事件（升序；limit 上界钳制）。team 非空 =
	// 行级过滤（team_id = team 或 ”平台级行——与 audit 面 ADR-0035 决策 6
	// 同口径）；team 空 = 不过滤（平台 owner / 内部消费面，P0-2）。
	ListAfter(ctx context.Context, afterSeq int64, limit int, team string) ([]Event, error)
	// ListByAggregate 返回某聚合名下 seq 之后的事件（升序前向扫描按聚合
	// 过滤；limit 上界钳制）。team 语义同 ListAfter。B2a W1b facts 地基：
	// 诊断「近窗事件」的查询面（(team, aggregate, aggregate_id, seq) 索引
	// 承载，zeabur 借鉴设计 §2.4 件 3）。
	ListByAggregate(ctx context.Context, team, aggregate, aggregateID string, afterSeq int64, limit int) ([]Event, error)
	// LastSeq 返回当前最大 seq（空表 = 0；快照重同步端点的基准）。
	LastSeq(ctx context.Context) (int64, error)
	// EarliestSeq 返回保留窗内最早事件 seq（空表 = 0）。断档判定锚：订阅方
	// after_seq < earliest 即落窗外（410 + 快照重同步，ADR-0026）。
	EarliestSeq(ctx context.Context) (int64, error)
	// TrimBefore 删除保留窗外的全部行（janitor 周期执行；返回清理行数供
	// 观测）。事件既成事实落审计后由本面回收——"只增"修订为"保留窗内只增"
	// （ADR-0026：订阅契约以 earliest_seq 划界，窗内不删不改）。
	TrimBefore(ctx context.Context, cutoff time.Time) (int64, error)
}
