// Package app 是 App 聚合的领域面（CONTEXT.md App 词条：长运行可部署单元，
// 由一个或多个 Process 组成；Process 形态住 Spec/Revision，App 行只承载
// 骨架与命名）：行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/app（ADR-0075 Repo 接口层路线）。
package app

// App 是聚合行（软删 tombstone）。
type App struct {
	ID        string
	Seq       int64
	ProjectID string
	Name      string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (a *App) Deleted() bool { return a.DeletedAt != "" }
