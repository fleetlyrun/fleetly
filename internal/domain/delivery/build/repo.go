package build

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Build 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行。ID 空则装配 "<app id>-<seq>"；Seq 零值则事务内取 MAX+1
	// （ADR-0070）。
	Create(ctx context.Context, run state.Runner, b *Build) error
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*Build, error)
	// ListByApp 返回 App 全部 Build（新→旧 + after 游标；ADR-0026 after_* +
	// limit 惯例——游标 = seq 创建序）。limit<=0 或 >200 回落/钳制缺省 50。
	ListByApp(ctx context.Context, run state.Runner, appID string, afterSeq int64, limit int) ([]Build, error)
	// ListByRevision 返回 Revision 的全部 Build 行（新→旧；Revision 级构建
	// 一次，历史行应只有一条——含终态供部署驱动判定复用或失败）。
	ListByRevision(ctx context.Context, run state.Runner, revisionID string) ([]Build, error)
	// ListActive 返回全部活跃 Build（构建循环拾取面）。
	ListActive(ctx context.Context, run state.Runner) ([]Build, error)
	// StampRepo 给存量在途行补章 repo（前纲切换的兼容面：升级时点 queued/
	// building 的行按新公式重放输入，产物将落前缀仓——repo 必须与之一致；
	// WHERE repo=” 使补章幂等一次性，已章行不受扰）。
	StampRepo(ctx context.Context, run state.Runner, id, repo string) error
	// Transit 是状态 CAS（同 deployment.Transit 口径；mut 补充 digest/error）。
	Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Build)) error
}
