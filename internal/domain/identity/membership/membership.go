// Package membership 是 membership 关联聚合的领域面（User 在 Team 内经
// Role 获权的关联行；一 Team 一 Role，UNIQUE(user_id, team_id)——RPC 面
// 无专属 Service，词汇归属 identity.proto 分组）。行类型与 Repo 接口在
// 此单源，SQLite 实现住 infrastructure/persistence/membership（ADR-0075
// Repo 接口层）。
package membership

// Membership 是关联行。
type Membership struct {
	ID        string
	Seq       int64
	UserID    string
	TeamID    string
	RoleID    string
	CreatedAt string
}
