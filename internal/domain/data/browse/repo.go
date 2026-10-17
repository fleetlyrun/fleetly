package browse

import (
	"context"
)

// Repo 是 browse 会话台账存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行台账（受理事务内——与事件/审计同拍）。
	Create(ctx context.Context, s *Session) error
	// List 返回全部在册行（到期判定在引擎域——repo 只做存取；id 升序
	// 稳定）。
	List(ctx context.Context) ([]*Session, error)
	// CountByProject 对在册行按 project 计数（quota 判定输入）。
	CountByProject(ctx context.Context, projectID string) (int, error)
	// Delete 删一行（回收收口；不存在是幂等成功）。
	Delete(ctx context.Context, id string) error
}
