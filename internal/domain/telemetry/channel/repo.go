package channel

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 notification_channels 表的仓储。方法集机械平移自既有 repo
// 导出面；Runner 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立
// 执行也可组合进四件一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一条通道；重名 → state.ErrAlreadyExists（name 唯一索引
	// 把守）。
	Create(ctx context.Context, run state.Runner, c *Channel) error
	// Get 按 ID 读行。
	Get(ctx context.Context, run state.Runner, id string) (*Channel, error)
	// ListEnabled 列启用通道（派发面）。
	ListEnabled(ctx context.Context, run state.Runner) ([]Channel, error)
	// ListAll 列全部通道（管理面）。
	ListAll(ctx context.Context, run state.Runner) ([]Channel, error)
	// RecordDelivery 落最近一次派发结果（空 err 文本 = 成功清位）。
	RecordDelivery(ctx context.Context, run state.Runner, id, failure string) error
	// Delete 删行（不存在 → state.ErrNotFound）。
	Delete(ctx context.Context, run state.Runner, id string) error
}
