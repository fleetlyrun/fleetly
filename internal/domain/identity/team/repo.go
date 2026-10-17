package team

import (
	"context"
)

// Repo 是 Team 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, t *Team) error
	// Get 按 ID 直读。
	Get(ctx context.Context, id string) (*Team, error)
	// List 返回全部 Team（ID 序稳定）。
	List(ctx context.Context) ([]Team, error)
	// Delete 按 ID 删行（FK RESTRICT 把守：仍有 Project/Token/membership
	// 引用的 Team 拒删，错误归一为 state.ErrConflict——调用方提示先解绑）。
	Delete(ctx context.Context, id string) error
}
