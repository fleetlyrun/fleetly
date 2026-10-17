package idempotency

import (
	"context"
	"time"
)

// Repo 是幂等记录聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Get 按键直读（含过期行——过期判定与惰性清理在调用方，见
	// application/idem）。
	Get(ctx context.Context, key string) (*Record, error)
	// Claim 落一条 inflight 认领（键不存在时）。键已被认领/完成返回
	// state.ErrAlreadyExists——调用方据此进入重放/冲突分诊，不覆盖任何
	// 在册行。
	Claim(ctx context.Context, key, method, fingerprint string, claimTTL time.Duration) error
	// Complete 把 inflight 行落为完成态：携带响应引用（proto 全名 +
	// 确定性序列化体）与 24h 保留窗。行不存在（已被清理/释放）返回
	// state.ErrNotFound——幂等承诺随行消失，调用方按无记录处理（不复活）。
	Complete(ctx context.Context, key, responseType string, body []byte, retention time.Duration) error
	// Release 删除认领行（handler 失败路径：允许同键干净重试——失败请求
	// 无响应可重放）。
	Release(ctx context.Context, key string) error
	// Sweep 清理保留窗外的一切行（janitor 周期执行；返回清理行数供观测）。
	Sweep(ctx context.Context) (int64, error)
}
