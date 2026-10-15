package network

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Network 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行；同 Project 同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, run state.Runner, n *Network) error
	// GetByID 按 ID 直读活跃行（peer 声明解析面：声明行只挂活跃网络）。
	GetByID(ctx context.Context, run state.Runner, id string) (*Network, error)
	// GetByName 读活跃行。
	GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Network, error)
	// ListByProject 返回 Project 全部活跃网络（全量面——配额检查与引擎
	// 挂网装配消费；API 分页读面走 ListByProjectPage）。
	ListByProject(ctx context.Context, run state.Runner, projectID string) ([]Network, error)
	// ListByProjectPage 是 ListByProject 的分页读面（ADR-0026 after_* +
	// limit；游标轴 = 既有排序轴 name 字典序升序——既有响应序不变）。
	// limit<=0 或 >200 回落/钳制缺省 50。
	ListByProjectPage(ctx context.Context, run state.Runner, projectID, afterName string, limit int) ([]Network, error)
	// List 返回全部活跃网络（跨 Project；受管 Proxy 挂网的全量真源，N0
	// 修复批 B1）。
	List(ctx context.Context, run state.Runner) ([]Network, error)
}
