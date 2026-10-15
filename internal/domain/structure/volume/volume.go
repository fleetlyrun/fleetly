// Package volume 是 Volume 聚合的领域面（CONTEXT.md Volume 词条：持久
// 存储附件；无显式 Placement 时默认钉住节点——pinned_node_id 是平台节点
// ID 锚，节点 ID 永不复用）。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/volume（ADR-0075 Repo 接口层）。
package volume

// Volume 是聚合行。
type Volume struct {
	ID           string
	Seq          int64
	ProjectID    string
	Name         string
	PinnedNodeID string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    string
}

// Deleted 报告 tombstone 状态。
func (v *Volume) Deleted() bool { return v.DeletedAt != "" }
