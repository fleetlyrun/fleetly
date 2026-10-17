package catalog

import (
	"context"
)

// Repo 是快照存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Put 原子覆盖快照（整体替换；空 body 是编程错误由约束/调用侧承载）。
	Put(ctx context.Context, s *Snapshot) error
	// Get 读快照（缺席 → ErrNoSnapshot；Scan 归一哨兵由实现侧
	// persistence.MapScanErr）。
	Get(ctx context.Context) (*Snapshot, error)
}
