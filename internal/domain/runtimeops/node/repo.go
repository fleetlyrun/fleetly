package node

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是观测缓存存取（Upsert 语义：观测刷新非状态机迁移）。方法集机械
// 平移自既有 repo 导出面；Runner 一律由调用方注入（*sql.DB 或 *sql.Tx
// 均满足，可独立执行也可组合进四件一拍事务，ADR-0005）。
type Repo interface {
	// MintPlatformID 铸下一平台节点 ID（node-<seq>；ADR-0070 独立类）。
	// 锚定环先铸 ID → Provider 写回载体标记 → Upsert 落行（ID 是锚，
	// 铸造与落行同事务由调用方组合；seq 构造性单调 = 节点 ID 永不复用）。
	MintPlatformID(ctx context.Context, run state.Runner) (string, error)
	// Upsert 刷新一行观测（首见落 first_seen_at）。落笔条件：观测值变化
	// （carrier/hostname/role/available）或 last_seen 距上次刷新超过刷新
	// 窗（窗宽是实现细节，P0-7.4 的零写路径）——"值未变也恒写"与观测
	// 缓存语义不符。先 UPDATE（观测刷新不耗 seq 号），零行 = 首见 →
	// 铸 seq 落行（ADR-0070）。
	Upsert(ctx context.Context, run state.Runner, n *Node) error
	// List 返回全部观测行（seq 序 = 锚定序；引擎观测对账全量面——API
	// 分页读面走 ListPage）。
	List(ctx context.Context, run state.Runner) ([]Node, error)
	// ListPage 是 List 的分页读面（ADR-0026 after_* + limit）。游标轴 =
	// seq 锚定序升序。limit<=0 或 >200 回落/钳制缺省 50。
	ListPage(ctx context.Context, run state.Runner, afterSeq int64, limit int) ([]Node, error)
	// Get 按 platform_id 直读。
	Get(ctx context.Context, run state.Runner, platformID string) (*Node, error)
	// ByCarrier 按载体节点 ID 反查（ADR-0049 中继握手绑定面：中继握手
	// 携带载体节点 ID，平台锚定表反查平台节点 ID——与 Watch 锚定同一张
	// 表）。
	ByCarrier(ctx context.Context, run state.Runner, carrierID string) (*Node, error)
}
