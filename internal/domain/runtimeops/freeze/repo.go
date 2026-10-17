package freeze

import (
	"context"
)

// Repo 是 Change Freeze 聚合存取。方法集机械平移自既有 repo 导出面；
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一条活跃冻结；同 scope 已有活跃行 →
	// state.ErrAlreadyExists（部分唯一索引把守——Set 面映射 E_CONFLICT，
	// 先 lift 再 set）。
	Create(ctx context.Context, f *Freeze) error
	// Get 按 ID 读行（含历史行——lift 幂等面需要区分"已 lift"与"不
	// 存在"）。
	Get(ctx context.Context, id string) (*Freeze, error)
	// ActiveForTeam 返回命中该 Team 的活跃冻结（team_id ∈ {'', teamID}）；
	// Team 特定行排前（拒绝信封优先报更具体的原因）。
	ActiveForTeam(ctx context.Context, teamID string) ([]Freeze, error)
	// Lift 落 lifted_at（CAS：仅活跃行）。0 行回查：行不存在 →
	// state.ErrNotFound；已 lift → nil（幂等——Lift 面对历史行直接成功）。
	Lift(ctx context.Context, id string) error
	// List 返回全部冻结行（新→旧 + after 游标；ADR-0026 after_* + limit
	// 惯例）。
	List(ctx context.Context, afterSeq int64, limit int) ([]Freeze, error)
}
