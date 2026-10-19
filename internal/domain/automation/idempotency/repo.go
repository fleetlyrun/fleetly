package idempotency

import (
	"context"
	"time"
)

// Repo 是幂等记录聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
//
// 键空间按 (scope, key) 收窄：scope 是调用方主体作用域（application/idem
// 的 callerScope 计算：token:<TokenID> 优先，其次 user:<UserID>，无身份
// anonymous）。同键同体只在同一作用域内重放，跨 Token/跨 Team 互不可见
// （H11 / R14-H4：不得取出他队创建响应）。
type Repo interface {
	// Get 在作用域内按键直读（含过期行——过期判定与惰性清理在调用方，见
	// application/idem）。作用域外同键不可见。
	Get(ctx context.Context, scope, key string) (*Record, error)
	// Claim 在作用域内落一条 inflight 认领（(scope, key) 不存在时）。同
	// 作用域同键已被认领/完成返回 state.ErrAlreadyExists——调用方据此进入
	// 重放/冲突分诊，不覆盖任何在册行；异作用域同键各自独立。
	Claim(ctx context.Context, scope, key, method, fingerprint string, claimTTL time.Duration) error
	// Complete 把作用域内的 inflight 行落为完成态：携带响应引用（proto 全
	// 名 + 确定性序列化体）与 24h 保留窗。行不存在（已被清理/释放）返回
	// state.ErrNotFound——幂等承诺随行消失，调用方按无记录处理（不复活）。
	Complete(ctx context.Context, scope, key, responseType string, body []byte, retention time.Duration) error
	// Release 删除作用域内的认领行（handler 失败路径：允许同键干净重试——
	// 失败请求无响应可重放）。作用域外同键不受影响。
	Release(ctx context.Context, scope, key string) error
	// Sweep 清理保留窗外的一切行（janitor 周期执行；返回清理行数供观测）。
	// 全局过期清理，不区分作用域（含存量 '' 隔离行）。
	Sweep(ctx context.Context) (int64, error)
}
