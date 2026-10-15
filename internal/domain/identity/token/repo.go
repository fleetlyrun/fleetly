package token

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Token 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行（UserID 空串落 NULL——无属主用户的 Token：Agent/CI/
	// bootstrap）。
	Create(ctx context.Context, run state.Runner, t *Token) error
	// GetBySHA256 按明文摘要查行（authn 拦截器的查询键）。
	GetBySHA256(ctx context.Context, run state.Runner, sha string) (*Token, error)
	// Get 按 ID 直读。
	Get(ctx context.Context, run state.Runner, id string) (*Token, error)
	// HasAliveBootstrap 报告是否存在未吊销的 bootstrap Token（首启去重
	// 判定；journal 之外的库内权威锚）。
	HasAliveBootstrap(ctx context.Context, run state.Runner) (bool, error)
	// List 返回全部 Token（ID 序稳定；含已吊销——吊销状态是可见事实）。
	List(ctx context.Context, run state.Runner) ([]Token, error)
	// Revoke 落吊销位（幂等；重复吊销返回当前行）。验收锚：吊销后进行中
	// 请求的下一个调用即 401（authn 拦截器逐请求查表）。
	Revoke(ctx context.Context, run state.Runner, id string) (*Token, error)
	// TouchLastUsed 记录最近使用时刻（authn 拦截器经内存节流调用——默认
	// 60s 内同名 Token 不重复写库；空串首次必写）。
	TouchLastUsed(ctx context.Context, run state.Runner, id string) error
}
