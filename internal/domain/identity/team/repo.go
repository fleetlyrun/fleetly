package team

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Team 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行；同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, run state.Runner, t *Team) error
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*Team, error)
	// List 返回全部 Team（ID 序稳定）。
	List(ctx context.Context, run state.Runner) ([]Team, error)
	// Delete 按 ID 删行（FK RESTRICT 把守：仍有 Project/Token/membership
	// 引用的 Team 拒删，错误归一为 state.ErrConflict——调用方提示先解绑）。
	Delete(ctx context.Context, run state.Runner, id string) error
}
