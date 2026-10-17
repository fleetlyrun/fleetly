package role

import (
	"context"
)

// Repo 是 Role 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行；同 Team 内同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, ro *Role) error
	// UpsertBuiltin 落/刷内置角色（固定 slug ID；scopes 由代码单一源刷新
	// ——词表演进时启动即同步，不待人工迁移）。幂等刷新不耗 seq 号：先
	// UPDATE，零行 = 首播 → 铸 seq 落行（ADR-0070）。
	UpsertBuiltin(ctx context.Context, ro *Role) error
	// Get 按 ID 直读。
	Get(ctx context.Context, id string) (*Role, error)
	// List 返回全部 Role（内置在前、ID 序稳定）。
	List(ctx context.Context) ([]Role, error)
	// Delete 按 ID 删行。内置角色拒绝删（代码单一源）；仍被 Token/
	// membership 引用时 FK RESTRICT 归一为 state.ErrConflict。
	Delete(ctx context.Context, id string) error
}
