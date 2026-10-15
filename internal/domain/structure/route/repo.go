package route

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Route 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行；host+path 全局冲突（跨项目同 host 同为活跃行）返回
	// state.ErrAlreadyExists（安全批：host 命名空间是平台级资产，唯一索引
	// 已从项目内升级为全局——idx_routes_host_path）。
	Create(ctx context.Context, run state.Runner, rt *Route) error
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*Route, error)
	// List 返回全部活跃 Route（Proxy 全量发布面；跨 Project）。
	List(ctx context.Context, run state.Runner) ([]Route, error)
	// ListPage 分页读面（ADR-0026 after_* + limit；游标轴 = seq 创建序
	// 升序——既有响应序不变）。projectID 过滤可选（过滤语义与既有内存
	// 过滤一致，下推 SQL 使游标分页与过滤叠加）；projectID 为空 = 全量
	// 分页。limit<=0 或 >200 回落/钳制缺省 50。
	ListPage(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]Route, error)
	// SoftDelete 落 tombstone（幂等）。
	SoftDelete(ctx context.Context, run state.Runner, id string) error
}
