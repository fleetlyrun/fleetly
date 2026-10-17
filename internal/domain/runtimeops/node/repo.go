package node

import (
	"context"
)

// Repo 是观测缓存存取（Upsert 语义：观测刷新非状态机迁移）。方法集机械
// 平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// MintPlatformID 铸下一平台节点 ID（node-<seq>；ADR-0070 独立类）。
	// 锚定环先铸 ID → Provider 写回载体标记 → Upsert 落行（ID 是锚，
	// 铸造与落行同事务由调用方组合；seq 构造性单调 = 节点 ID 永不复用）。
	MintPlatformID(ctx context.Context) (string, error)
	// Upsert 刷新一行观测（首见落 first_seen_at）。落笔条件：观测值变化
	// （carrier/hostname/role/available）或 last_seen 距上次刷新超过刷新
	// 窗（窗宽是实现细节，P0-7.4 的零写路径）——"值未变也恒写"与观测
	// 缓存语义不符。先 UPDATE（观测刷新不耗 seq 号），零行 = 首见 →
	// 铸 seq 落行（ADR-0070）。
	Upsert(ctx context.Context, n *Node) error
	// List 返回全部观测行（seq 序 = 锚定序；引擎观测对账全量面——API
	// 分页读面走 ListPage）。
	List(ctx context.Context) ([]Node, error)
	// ListPage 是 List 的分页读面（ADR-0026 after_* + limit）。游标轴 =
	// seq 锚定序升序。limit<=0 或 >200 回落/钳制缺省 50。
	ListPage(ctx context.Context, afterSeq int64, limit int) ([]Node, error)
	// Get 按 platform_id 直读。
	Get(ctx context.Context, platformID string) (*Node, error)
	// ByCarrier 按载体节点 ID 反查（ADR-0049 中继握手绑定面：中继握手
	// 携带载体节点 ID，平台锚定表反查平台节点 ID——与 Watch 锚定同一张
	// 表）。
	ByCarrier(ctx context.Context, carrierID string) (*Node, error)
}
