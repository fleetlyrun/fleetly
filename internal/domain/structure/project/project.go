// Package project 是 Project 聚合的领域面（CONTEXT.md Project 词条：归属
// 与网络隔离轴，属于一个 Team；资源名只在 Project 内唯一）：行类型与
// Repo 接口在此单源，SQLite 实现住 infrastructure/persistence/project
// （ADR-0075 Repo 接口层路线）。
package project

// Project 是聚合行（软删 tombstone：deleted_at 非空即已删）。
type Project struct {
	ID        string
	Seq       int64
	Name      string
	TeamID    string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (p *Project) Deleted() bool { return p.DeletedAt != "" }
