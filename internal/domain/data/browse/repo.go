package browse

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 browse 会话台账存取。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一行台账（受理事务内——与事件/审计同拍）。
	Create(ctx context.Context, run state.Runner, s *Session) error
	// List 返回全部在册行（到期判定在引擎域——repo 只做存取；id 升序
	// 稳定）。
	List(ctx context.Context, run state.Runner) ([]*Session, error)
	// CountByProject 对在册行按 project 计数（quota 判定输入）。
	CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error)
	// Delete 删一行（回收收口；不存在是幂等成功）。
	Delete(ctx context.Context, run state.Runner, id string) error
}
