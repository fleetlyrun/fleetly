package freeze

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Change Freeze 聚合存取。方法集机械平移自既有 repo 导出面；
// Runner 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可
// 组合进四件一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一条活跃冻结；同 scope 已有活跃行 →
	// state.ErrAlreadyExists（部分唯一索引把守——Set 面映射 E_CONFLICT，
	// 先 lift 再 set）。
	Create(ctx context.Context, run state.Runner, f *Freeze) error
	// Get 按 ID 读行（含历史行——lift 幂等面需要区分"已 lift"与"不
	// 存在"）。
	Get(ctx context.Context, run state.Runner, id string) (*Freeze, error)
	// ActiveForTeam 返回命中该 Team 的活跃冻结（team_id ∈ {'', teamID}）；
	// Team 特定行排前（拒绝信封优先报更具体的原因）。
	ActiveForTeam(ctx context.Context, run state.Runner, teamID string) ([]Freeze, error)
	// Lift 落 lifted_at（CAS：仅活跃行）。0 行回查：行不存在 →
	// state.ErrNotFound；已 lift → nil（幂等——Lift 面对历史行直接成功）。
	Lift(ctx context.Context, run state.Runner, id string) error
	// List 返回全部冻结行（新→旧 + after 游标；ADR-0026 after_* + limit
	// 惯例）。
	List(ctx context.Context, run state.Runner, afterSeq int64, limit int) ([]Freeze, error)
}
