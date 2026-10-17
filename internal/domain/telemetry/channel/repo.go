package channel

import (
	"context"
)

// Repo 是 notification_channels 表的仓储。方法集机械平移自既有 repo
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一条通道；重名 → state.ErrAlreadyExists（name 唯一索引
	// 把守）。
	Create(ctx context.Context, c *Channel) error
	// Get 按 ID 读行。
	Get(ctx context.Context, id string) (*Channel, error)
	// ListEnabled 列启用通道（派发面）。
	ListEnabled(ctx context.Context) ([]Channel, error)
	// ListAll 列全部通道（管理面）。
	ListAll(ctx context.Context) ([]Channel, error)
	// RecordDelivery 落最近一次派发结果（空 err 文本 = 成功清位）。
	RecordDelivery(ctx context.Context, id, failure string) error
	// Delete 删行（不存在 → state.ErrNotFound）。
	Delete(ctx context.Context, id string) error
}
