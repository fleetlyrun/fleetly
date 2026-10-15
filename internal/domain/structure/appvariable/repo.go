package appvariable

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 AppVariable 聚合存取。方法集机械平移自既有 repo 导出面；
// Runner 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可
// 组合进四件一拍事务，ADR-0005）。
type Repo interface {
	// Upsert 写入（同名活跃行覆盖值；tombstone 行复活——shared_variables
	// 同款，唯一索引只拦活跃行）。
	Upsert(ctx context.Context, run state.Runner, v *AppVariable) error
	// GetByName 读活跃行（回显与配额计数的读面）。
	GetByName(ctx context.Context, run state.Runner, appID, name string) (*AppVariable, error)
	// ListActive 返回 App 内全部活跃行（冻结咽喉合成装载面 + 配额计数面）。
	ListActive(ctx context.Context, run state.Runner, appID string) ([]AppVariable, error)
	// ListPage 分页读（ADR-0026 after_* + limit 惯例；游标轴 = App 内
	// name 字典序升序）。limit<=0 或 >200 回落/钳制缺省 50（与
	// shared_variables 同款）。
	ListPage(ctx context.Context, run state.Runner, appID, afterName string, limit int) ([]AppVariable, error)
	// SoftDelete 落 tombstone（幂等；删除不存在/已删的名按 ErrNotFound
	// 诚实上抛——shared_variables 同款）。
	SoftDelete(ctx context.Context, run state.Runner, appID, name string) error
}
