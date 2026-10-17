package membership

import (
	"context"
)

// Repo 是 membership 存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Create 落一行；同 User 同 Team 重复授权返回 state.ErrAlreadyExists。
	Create(ctx context.Context, m *Membership) error
	// GetByUser 返回 User 在指定 Team 的 membership。
	GetByUser(ctx context.Context, userID, teamID string) (*Membership, error)
	// DeleteByUser 解绑 User 在 Team 的授权（删用户前解引用）。
	DeleteByUser(ctx context.Context, userID, teamID string) error
	// DeleteAllForUser 解绑 User 的全部 membership 行（删用户级联；ADR-0080
	// 批把此前的接口裸 SQL 收进 repo——Stores 零执行器词汇）。
	DeleteAllForUser(ctx context.Context, userID string) (int64, error)
	// ListByUser 返回用户的全部 membership（Login 密码会话铸 Token 的
	// team/role 解析源；C6 第一期——单 membership 取首行，多队诚实取 ID 序
	// 首个，跨队会话面随多队裁决扩展）。
	ListByUser(ctx context.Context, userID string) ([]Membership, error)
}
