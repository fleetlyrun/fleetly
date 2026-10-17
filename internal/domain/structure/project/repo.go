package project

import (
	"context"
)

// Repo 是 Project 聚合存取（无门面：仅本聚合查询）。方法集机械平移自
// 既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行；同 Team 同名活跃 Project 冲突返回 state.ErrAlreadyExists
	// （ADR-0028：唯一性口径 (team_id, name)——同名项目跨 Team 并存）。
	// ID 空则装配 "<name>-<seq>"（ADR-0070）；Seq 零值则事务内取 MAX+1（铸造
	// 单源在 repo 行装配处）。
	Create(ctx context.Context, p *Project) error
	// Get 按 ID 直读（含已删行——tombstone 是事实，不是秘密）。
	Get(ctx context.Context, id string) (*Project, error)
	// GetByNameInTeam 在 Team 内按名读活跃行（ADR-0028：名字唯一性只在
	// Team 内成立——跨 Team 同名并存，裸名查询语义不再成立）。
	GetByNameInTeam(ctx context.Context, teamID, name string) (*Project, error)
	// List 返回全部活跃行（全量面——seq 序 = 创建序；授权解析与引擎枚举
	// 消费；API 分页读面走 ListPage）。
	List(ctx context.Context) ([]Project, error)
	// ListPage 是 List 的分页读面（ADR-0026 after_* + limit；游标轴 = seq
	// 创建序升序——既有响应序不变）。limit<=0 或 >200 回落/钳制缺省 50。
	ListPage(ctx context.Context, afterSeq int64, limit int) ([]Project, error)
	// ListByTeam 返回 Team 内全部活跃行（ADR-0035 行级授权的 List 面过滤锚
	// 与 authz 解析面——全量；非 owner 的 API 分页读面走 ListByTeamPage）。
	ListByTeam(ctx context.Context, teamID string) ([]Project, error)
	// ListByTeamPage 是 ListByTeam 的分页读面（ADR-0026 after_* + limit；
	// Team 过滤与游标分页叠加）。
	ListByTeamPage(ctx context.Context, teamID string, afterSeq int64, limit int) ([]Project, error)
	// SoftDelete 落 tombstone（四件一拍的 tombstone 件；Outbox/审计由调用方
	// 同事务组合）。幂等：已删行不报错。
	SoftDelete(ctx context.Context, id string) error
}
