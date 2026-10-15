// Package role 是 Role 聚合的领域面（CONTEXT.md Role 词条：命名的 Scope
// 集合，在 Team 内授予 User 或 Token）。scopes 列是 JSON 数组（colon 形态
// ——Scope 语言解析/蕴含在 identity 种子包）。行类型与 Repo 接口在此单源，
// SQLite 实现住 infrastructure/persistence/role（ADR-0075 Repo 接口层）。
package role

// Role 是聚合行（Builtin 行 team_id 为空——平台级模板）。
type Role struct {
	ID        string
	Seq       int64
	TeamID    string
	Name      string
	Builtin   bool
	Scopes    []string
	CreatedAt string
}
