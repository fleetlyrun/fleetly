package catalog

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是快照存取。方法集机械平移自既有 repo 导出面；Runner 一律由
// 调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Put 原子覆盖快照（整体替换；空 body 是编程错误由约束/调用侧承载）。
	Put(ctx context.Context, run state.Runner, s *Snapshot) error
	// Get 读快照（缺席 → ErrNoSnapshot；Scan 归一哨兵由实现侧
	// persistence.MapScanErr）。
	Get(ctx context.Context, run state.Runner) (*Snapshot, error)
}
