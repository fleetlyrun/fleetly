// Package node 是 Node 观测缓存聚合的领域面（架构 §5，ADR-0075 Repo
// 接口层）：nodes 表只是观测缓存，非权威——权威归属判定永远查平台表；
// Provider Watch 流的 node.joined/leave 事实在此落行，节点 ID 永不复用。
// 行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/node。
package node

// Node 是一行观测缓存。
type Node struct {
	PlatformID  string
	Seq         int64
	CarrierID   string
	Hostname    string
	Role        string
	Available   bool
	FirstSeenAt string
	LastSeenAt  string
}
