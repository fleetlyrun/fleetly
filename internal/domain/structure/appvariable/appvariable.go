// Package appvariable 是 AppVariable 聚合的领域面（CONTEXT.md Variable
// 词条：App 级平台变量——三级合成的顶层，冻结咽喉合成进 AppSpec，同键
// 覆盖源声明 env 与 SharedVariable，ADR-0072）。值是明文非敏感面（敏感值
// 走 age 信封的 Secret 聚合）；软删 tombstone 与 shared_variables 同款
// （聚合镜像先例）。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/appvariable（ADR-0075 Repo 接口层）。
package appvariable

// AppVariable 是聚合行。
type AppVariable struct {
	ID        string
	Seq       int64
	AppID     string
	Name      string
	Value     string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (v *AppVariable) Deleted() bool { return v.DeletedAt != "" }
