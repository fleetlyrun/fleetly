package build

import (
	"context"
)

// Repo 是 Build 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行。ID 空则装配 "<app id>-<seq>"；Seq 零值则事务内取 MAX+1
	// （ADR-0070）。
	Create(ctx context.Context, b *Build) error
	// Get 按 ID 直读。
	Get(ctx context.Context, id string) (*Build, error)
	// ListByApp 返回 App 全部 Build（新→旧 + after 游标；ADR-0026 after_* +
	// limit 惯例——游标 = seq 创建序）。limit<=0 或 >200 回落/钳制缺省 50。
	ListByApp(ctx context.Context, appID string, afterSeq int64, limit int) ([]Build, error)
	// ListByRevision 返回 Revision 的全部 Build 行（新→旧；Revision 级构建
	// 一次，历史行应只有一条——含终态供部署驱动判定复用或失败）。
	ListByRevision(ctx context.Context, revisionID string) ([]Build, error)
	// ListActive 返回全部活跃 Build（构建循环拾取面）。
	ListActive(ctx context.Context) ([]Build, error)
	// Transit 是状态 CAS（同 deployment.Transit 口径；mut 补充 digest/error）。
	Transit(ctx context.Context, id string, from []State, to State, mut func(*Build)) error
}
