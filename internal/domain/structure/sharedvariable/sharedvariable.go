// Package sharedvariable 是 SharedVariable 聚合的领域面（CONTEXT.md
// Variable 词条：Project 级共享变量，归一化期合成进 AppSpec——Project 层
// 在下、App 层 env 覆盖，ADR-0027/0043）。值是明文非敏感面（敏感值走
// age 信封的 Secret 聚合）；软删 tombstone 与 secrets 同款。行类型与
// Repo 接口在此单源，SQLite 实现住 infrastructure/persistence/
// sharedvariable（ADR-0075 Repo 接口层）。
package sharedvariable

// SharedVariable 是聚合行。
type SharedVariable struct {
	ID        string
	Seq       int64
	ProjectID string
	Name      string
	Value     string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (v *SharedVariable) Deleted() bool { return v.DeletedAt != "" }
