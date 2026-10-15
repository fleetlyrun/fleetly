package role

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Role 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行；同 Team 内同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, run state.Runner, ro *Role) error
	// UpsertBuiltin 落/刷内置角色（固定 slug ID；scopes 由代码单一源刷新
	// ——词表演进时启动即同步，不待人工迁移）。幂等刷新不耗 seq 号：先
	// UPDATE，零行 = 首播 → 铸 seq 落行（ADR-0070）。
	UpsertBuiltin(ctx context.Context, run state.Runner, ro *Role) error
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*Role, error)
	// List 返回全部 Role（内置在前、ID 序稳定）。
	List(ctx context.Context, run state.Runner) ([]Role, error)
	// Delete 按 ID 删行。内置角色拒绝删（代码单一源）；仍被 Token/
	// membership 引用时 FK RESTRICT 归一为 state.ErrConflict。
	Delete(ctx context.Context, run state.Runner, id string) error
}
