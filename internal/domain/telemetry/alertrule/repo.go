package alertrule

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 alert_rules 表的仓储。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一条规则（初始态 ok）。
	Create(ctx context.Context, run state.Runner, rule *Rule) error
	// Get 按 ID 读行。
	Get(ctx context.Context, run state.Runner, id string) (*Rule, error)
	// ListEnabled 列全部启用规则（app 升序）。
	ListEnabled(ctx context.Context, run state.Runner) ([]Rule, error)
	// ListByApp 列某 App 的规则（含禁用行；id 升序）。
	ListByApp(ctx context.Context, run state.Runner, appID string) ([]Rule, error)
	// ListAll 列全部规则（alerts list 面）。
	ListAll(ctx context.Context, run state.Runner) ([]Rule, error)
	// Observe 落最近评估观测（值 + 时刻；不迁态）。
	Observe(ctx context.Context, run state.Runner, id string, value float64, at string) error
	// Transition 迁移评估态（fired/resolved 通知沿的持久锚）。
	Transition(ctx context.Context, run state.Runner, id, to, at string) error
	// Delete 删行（幂等：不存在 state.ErrNotFound 由调用面映射 404）。
	Delete(ctx context.Context, run state.Runner, id string) error
}
