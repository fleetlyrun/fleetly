// Package network 是 Network 聚合的领域面（CONTEXT.md Network 词条：
// Project 级互通附件；成员为 Project 进程、Task 网络组挂靠或显式跨
// Project 引用；可声明 egress:none）。行类型与 Repo 接口在此单源，
// SQLite 实现住 infrastructure/persistence/network（ADR-0075 Repo 接口
// 层；实现侧包名 networkrepo 避开与内部网络实现的混淆）。
package network

// Network 是聚合行。
type Network struct {
	ID         string
	Seq        int64
	ProjectID  string
	Name       string
	EgressNone bool
	CreatedAt  string
	DeletedAt  string
}

// Deleted 报告 tombstone 状态。
func (n *Network) Deleted() bool { return n.DeletedAt != "" }
