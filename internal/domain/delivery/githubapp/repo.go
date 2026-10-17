package githubapp

import (
	"context"
)

// Repo 是 GitHub App 接入材料聚合存取。方法集机械平移自既有 repo 导出
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Upsert 写入（受理即验证后调用：调用方持封装材料与已解析属主）。同
	// 作用域同属主的活跃行覆盖更新（私钥轮换 = 重放 put，fingerprint 变更
	// 即新钥）；异属主落新行。
	Upsert(ctx context.Context, a *App) error
	// Get 按 id 直读（含密文——轮换/维护路径；回显面走 List 族）。
	Get(ctx context.Context, id string) (*App, error)
	// GetByProjectAndOwner 构建期解析（Project 级，遮蔽 Global）：含密文
	// ——仅 clone 材料路径调用；回显面走 List 族。
	GetByProjectAndOwner(ctx context.Context, projectID, login string) (*App, error)
	// GetGlobalByOwner 构建期解析（平台级回落）：含密文，同上仅材料路径。
	GetGlobalByOwner(ctx context.Context, login string) (*App, error)
	// ListByProject 返回 Project 活跃行的指纹面（回显契约：密文 SQL 层
	// NULL 投影，值永不出现）。account_login 字典序升序。
	ListByProject(ctx context.Context, projectID string) ([]App, error)
	// ListGlobal 返回平台级活跃行的指纹面（同上回显契约）。
	ListGlobal(ctx context.Context) ([]App, error)
	// ListAll 读全表行（含 tombstone）：离线维护面（KEK 重封）专用——
	// 墓碑行的密文同样要可解，含密文列故禁止回显面消费。
	ListAll(ctx context.Context) ([]App, error)
	// UpdatePrivateKeyCiphertext 重写密文（KEK 重封专用：fingerprint 是
	// 明文的函数，重封不变）。行不存在不报错——调用方（rewrap）的行列表
	// 与本写同窗口，无并发删除面。
	UpdatePrivateKeyCiphertext(ctx context.Context, id string, ciphertext []byte) error
	// SoftDelete 落 tombstone（幂等：已删行归 nil；行不存在 =
	// state.ErrNotFound）。
	SoftDelete(ctx context.Context, id string) error
}
