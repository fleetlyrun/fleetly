package invitation

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Invitation 聚合存取。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一行。
	Create(ctx context.Context, run state.Runner, inv *Invitation) error
	// GetBySHA256 按明文摘要查行（accept 面的查询键）。
	GetBySHA256(ctx context.Context, run state.Runner, sha string) (*Invitation, error)
	// List 返回全部邀请（ID 序稳定；含已消费/已过期——状态是可见事实）。
	List(ctx context.Context, run state.Runner) ([]Invitation, error)
	// MarkConsumed 落单次使用位（条件更新：仅未消费行命中——并发双 accept
	// 只有一个成功，另一个读到的仍是未消费但重查即 NOT_FOUND 语义）。
	MarkConsumed(ctx context.Context, run state.Runner, id string) error
}
