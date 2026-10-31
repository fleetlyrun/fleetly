package alertrule

import (
	"context"
)

// Repo 是 alert_rules 表的仓储。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一条规则（初始态 ok）。
	Create(ctx context.Context, rule *Rule) error
	// Get 按 ID 读行。
	Get(ctx context.Context, id string) (*Rule, error)
	// ListEnabled 列全部启用规则（app 升序）。
	ListEnabled(ctx context.Context) ([]Rule, error)
	// ListByApp 列某 App 的规则（含禁用行；id 升序）。
	ListByApp(ctx context.Context, appID string) ([]Rule, error)
	// ListAll 列全部规则（alerts list 面）。
	ListAll(ctx context.Context) ([]Rule, error)
	// Observe 落最近评估观测（值 + 时刻；不迁态）。
	Observe(ctx context.Context, id string, value float64, at string) error
	// BeginPending 落越限首见的起窗时刻（pending_since 列直写——B2a W1b
	// 直接改列形态，无双写；窗内推进不重写，重启续窗的持久锚）。
	BeginPending(ctx context.Context, id, at string) error
	// ClearPending 清 pending 窗（回落归位；幂等——无窗行清写无效果）。
	ClearPending(ctx context.Context, id string) error
	// Transition 迁移评估态（fired/resolved 通知沿的持久锚；迁移即离窗，
	// pending_since 随迁移清列——firing 与 ok 都是窗的终点）。
	Transition(ctx context.Context, id, to, at string) error
	// Delete 删行（幂等：不存在 state.ErrNotFound 由调用面映射 404）。
	Delete(ctx context.Context, id string) error
}
