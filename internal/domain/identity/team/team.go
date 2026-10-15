// Package team 是 Team 聚合的领域面（CONTEXT.md Team 词条：权限轴，User
// 与 Token 通过 Team 内的角色获得资源访问权；Project 归属 Team）。行类型
// 与 Repo 接口在此单源，SQLite 实现住 infrastructure/persistence/team
// （ADR-0075 Repo 接口层）。
package team

// Team 是聚合行。
type Team struct {
	ID        string
	Seq       int64
	Name      string
	CreatedAt string
}
