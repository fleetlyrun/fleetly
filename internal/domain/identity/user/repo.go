package user

import (
	"context"
)

// Repo 是 User 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, u *User) error
	// Get 按 ID 直读。
	Get(ctx context.Context, id string) (*User, error)
	// GetByName 按名直读（Login 密码自证的入口；用户名全局唯一）。
	GetByName(ctx context.Context, name string) (*User, error)
	// SetPassword 写密码哈希（admin 设置/重置；Login 的自证物单源）。
	SetPassword(ctx context.Context, id, hash string) error
	// Count 返回用户总数（Bootstrap Token 的"无用户"判定）。
	Count(ctx context.Context) (int, error)
	// List 返回全部用户（ID 序稳定）。
	List(ctx context.Context) ([]User, error)
	// Delete 按 ID 删行。仍被 Token 引用时 FK RESTRICT 归一为
	// state.ErrConflict（ADR-0028 user repo FK 归一，对齐 role repo 先例
	// ——先吊销/删名下 Token 再删用户，不再误导 E_INTERNAL）；有
	// membership 的用户已在 API 层先解绑。
	Delete(ctx context.Context, id string) error
}
