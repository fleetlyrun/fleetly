package hook

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Git 触发配置聚合存取。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。
type Repo interface {
	// Create 落一行（首配铸造路径：Token 材料随行落）。双唯一键（app_id
	// 主键 / token_sha256）命中归一为 state.ErrAlreadyExists：SetGitHook
	// 的 Get 先行是读面判定，并发交错（首配竞态）落此处——调用方重试即走
	// UpdateConfig 臂。githubapp 同款形态。
	Create(ctx context.Context, run state.Runner, h *Hook) error
	// UpdateConfig 只改配置字段（已配置过的 Set 路径：不换 Token 材料）。
	UpdateConfig(ctx context.Context, run state.Runner, h *Hook) error
	// Get 按 App 直读。
	Get(ctx context.Context, run state.Runner, appID string) (*Hook, error)
	// GetByTokenSHA256 按 URL token 查找（接收面的入口查询键）。
	GetByTokenSHA256(ctx context.Context, run state.Runner, sha string) (*Hook, error)
	// RotateToken 双换 Token 材料（URL token 与 webhook secret 同源；旧行
	// 即刻失效——查询键已换）。
	RotateToken(ctx context.Context, run state.Runner, appID, sha, prefix string, ciphertext []byte) error
	// DeleteByApp 随 App tombstone 级联移除整个聚合（hook 行 + 重投去重
	// 行，与 tombstone 同事务）：孤儿 hook 会让配置面对已删 App 继续成功、
	// 接收面却按活跃行拒绝——两面语义分裂（ADR-0072 批真机咬出：删除 App
	// 后 rotate 仍成功、webhook 401）。去重行不清会撞 FK（hook_deliveries
	// REFERENCES app_hooks）令删除本身失败（FB-1：配置过 GitHook 的 App
	// 删除报 FOREIGN KEY constraint failed）。
	DeleteByApp(ctx context.Context, run state.Runner, appID string) error
	// ListAll 读全表行：离线维护面（KEK 重封）专用（含 webhook secret
	// 信封列，禁止回显面消费）。
	ListAll(ctx context.Context, run state.Runner) ([]Hook, error)
	// UpdateSecretCiphertext 重封 webhook secret 信封（KEK 重封专用；URL
	// token 材料与信封无关，不动）。
	UpdateSecretCiphertext(ctx context.Context, run state.Runner, appID string, ciphertext []byte) error
	// RecordDelivery 落一行重投去重锚；返回 true = 该 delivery 已出现过
	// （重投）。顺带清理保留窗外的旧行（窗宽是实现细节）。
	RecordDelivery(ctx context.Context, run state.Runner, appID, delivery string) (bool, error)
}
