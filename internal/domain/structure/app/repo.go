package app

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 App 聚合存取（无门面：仅本聚合查询）。方法集机械平移自既有
// repo 导出面；Runner 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立
// 执行也可组合进四件一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
	// ID 空则装配 "<name>-<seq>"；Seq 零值则事务内取 MAX+1（ADR-0070）。
	Create(ctx context.Context, run state.Runner, a *App) error
	// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在——
	// 已删行对 Get/GetByName/List 同构隐藏，删除语义不在读面分叉）。
	Get(ctx context.Context, run state.Runner, id string) (*App, error)
	// GetByName 在 Project 内按名读活跃行。
	GetByName(ctx context.Context, run state.Runner, projectID, name string) (*App, error)
	// GetAnyByID 按 ID 读行（含 tombstone；ADR-0035 授权专用面——App 删除后
	// ListDeployments/ListRevisions/ListBuilds 等审计型读面仍须过行级授权，
	// 归属判定需要已删行的 project_id；不改变 Get 的活跃行语义）。
	GetAnyByID(ctx context.Context, run state.Runner, id string) (*App, error)
	// ListByProject 返回 Project 内全部活跃 App（全量面——删除守卫与引擎
	// 隔离扫描消费；API 分页读面走 ListByProjectPage）。
	ListByProject(ctx context.Context, run state.Runner, projectID string) ([]App, error)
	// ListByProjectPage 是 ListByProject 的分页读面（ADR-0026 after_* + limit；
	// 游标轴 = seq 创建序升序——既有响应序不变）。limit<=0 或 >200 回落/
	// 钳制缺省 50。
	ListByProjectPage(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]App, error)
	// CountByProject 返回 Project 内活跃 App 行数（ADR-0017 附录 A.1 配额
	// 口径；受理位在事务内读）。
	CountByProject(ctx context.Context, run state.Runner, projectID string) (int, error)
	// List 返回全部活跃 App（ADR-0022 启动基线重放的枚举面）。
	List(ctx context.Context, run state.Runner) ([]App, error)
	// SoftDelete 落 tombstone（tombstone 件）。命中 0 行（不存在或已删）返回
	// ErrNotFound——与 Get 口径一致："已删"对调用方即"不存在"（再删 404）。
	SoftDelete(ctx context.Context, run state.Runner, id string) error
}
