package user

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 User 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, run state.Runner, u *User) error
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*User, error)
	// GetByName 按名直读（Login 密码自证的入口；用户名全局唯一）。
	GetByName(ctx context.Context, run state.Runner, name string) (*User, error)
	// SetPassword 写密码哈希（admin 设置/重置；Login 的自证物单源）。
	SetPassword(ctx context.Context, run state.Runner, id, hash string) error
	// Count 返回用户总数（Bootstrap Token 的"无用户"判定）。
	Count(ctx context.Context, run state.Runner) (int, error)
	// List 返回全部用户（ID 序稳定）。
	List(ctx context.Context, run state.Runner) ([]User, error)
	// Delete 按 ID 删行。仍被 Token 引用时 FK RESTRICT 归一为
	// state.ErrConflict（ADR-0028 user repo FK 归一，对齐 role repo 先例
	// ——先吊销/删名下 Token 再删用户，不再误导 E_INTERNAL）；有
	// membership 的用户已在 API 层先解绑。
	Delete(ctx context.Context, run state.Runner, id string) error
}
