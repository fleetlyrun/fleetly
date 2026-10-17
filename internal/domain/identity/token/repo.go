package token

import (
	"context"
)

// Repo 是 Token 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行（UserID 空串落 NULL——无属主用户的 Token：Agent/CI/
	// bootstrap）。
	Create(ctx context.Context, t *Token) error
	// GetBySHA256 按明文摘要查行（authn 拦截器的查询键）。
	GetBySHA256(ctx context.Context, sha string) (*Token, error)
	// Get 按 ID 直读。
	Get(ctx context.Context, id string) (*Token, error)
	// HasAliveBootstrap 报告是否存在未吊销的 bootstrap Token（首启去重
	// 判定；journal 之外的库内权威锚）。
	HasAliveBootstrap(ctx context.Context) (bool, error)
	// List 返回全部 Token（ID 序稳定；含已吊销——吊销状态是可见事实）。
	List(ctx context.Context) ([]Token, error)
	// Revoke 落吊销位（幂等；重复吊销返回当前行）。验收锚：吊销后进行中
	// 请求的下一个调用即 401（authn 拦截器逐请求查表）。
	Revoke(ctx context.Context, id string) (*Token, error)
	// TouchLastUsed 记录最近使用时刻（authn 拦截器经内存节流调用——默认
	// 60s 内同名 Token 不重复写库；空串首次必写）。
	TouchLastUsed(ctx context.Context, id string) error
}
