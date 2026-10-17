package volume

import (
	"context"
)

// Repo 是 Volume 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行；同 Project 同名冲突返回 state.ErrAlreadyExists。
	// pinnedNodeID 为空表示待钉住（首次挂载时由引擎分配——分配后不可变）。
	Create(ctx context.Context, v *Volume) error
	// GetByName 读活跃行。
	GetByName(ctx context.Context, projectID, name string) (*Volume, error)
	// Get 读活跃行（按 ID；anchor 解析链与 API 单体读面共用）。
	Get(ctx context.Context, id string) (*Volume, error)
	// SoftDelete 收口删除（tombstone；活跃行口径——被引用卷由受理位前置
	// 拒绝，本方法只管落账）。
	SoftDelete(ctx context.Context, id string) error
	// Pin 钉住节点（一次性：已钉住时幂等返回；节点 ID 永不复用——不改锚）。
	Pin(ctx context.Context, projectID, name, nodeID string) error
	// ListByProject 返回 Project 全部活跃卷。
	ListByProject(ctx context.Context, projectID string) ([]Volume, error)
}
