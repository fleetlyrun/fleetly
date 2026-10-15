package membership

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 membership 存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落一行；同 User 同 Team 重复授权返回 state.ErrAlreadyExists。
	Create(ctx context.Context, run state.Runner, m *Membership) error
	// GetByUser 返回 User 在指定 Team 的 membership。
	GetByUser(ctx context.Context, run state.Runner, userID, teamID string) (*Membership, error)
	// DeleteByUser 解绑 User 在 Team 的授权（删用户前解引用）。
	DeleteByUser(ctx context.Context, run state.Runner, userID, teamID string) error
	// ListByUser 返回用户的全部 membership（Login 密码会话铸 Token 的
	// team/role 解析源；C6 第一期——单 membership 取首行，多队诚实取 ID 序
	// 首个，跨队会话面随多队裁决扩展）。
	ListByUser(ctx context.Context, run state.Runner, userID string) ([]Membership, error)
}
