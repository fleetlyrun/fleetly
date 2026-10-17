package revision

import (
	"context"
)

// Repo 是 Revision 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行（digest 由 spec 派生；同 App 序号唯一、同 App 同内容唯一
	// ——重复冻结同内容 → state.ErrAlreadyExists，调用方应改走 FindByDigest
	// 复用）。ID 空则由 app id + per-App 序号派生（web-42-r7，ADR-0070 派生
	// 类——per-App 修订号即 UI 面既有语义，确定性无随机）。
	Create(ctx context.Context, rev *Revision) error
	// FindByDigest 按 App + 内容指纹找既有冻结体（内容寻址复用：无变化重
	// 部署复用 Rn 而非新冻结 Rn+1）。
	FindByDigest(ctx context.Context, appID, digest string) (*Revision, error)
	// Get 按 ID 直读。
	Get(ctx context.Context, id string) (*Revision, error)
	// GetBySeq 按 App 内序号读（R1..Rn 对外形态）。
	GetBySeq(ctx context.Context, appID string, seq int64) (*Revision, error)
	// Latest 返回 App 最新 Revision（无 → state.ErrNotFound；"上一成功"由
	// 调用方结合 Deployment 终态判定，本 repo 不持部署语义）。
	Latest(ctx context.Context, appID string) (*Revision, error)
	// NextSeq 返回 App 下一序号（首条 = 1；单调推进）。
	NextSeq(ctx context.Context, appID string) (int64, error)
	// ListByApp 返回 App 全部 Revision（R1..Rn 序号升序 + after 游标；
	// ADR-0026 after_* + limit 惯例——游标轴 = App 内单调 seq，升序即既有
	// 响应序，events 面同款形态）。limit<=0 或 >200 回落/钳制缺省 50。
	ListByApp(ctx context.Context, appID string, afterSeq int64, limit int) ([]Revision, error)
}
