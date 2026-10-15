// Package route 是 Route 聚合的领域面（CONTEXT.md Route 词条：host/path
// → Process 端口的映射，附协议与 TLS 模式。RoutesService 虽住 proxy
// proto，资源归属 structure 上下文——结构面附件，发布是 Proxy 上下文的
// 消费视角，2026-10-13 归属裁决）。行类型与 Repo 接口在此单源，SQLite
// 实现住 infrastructure/persistence/route（ADR-0075 Repo 接口层）。
package route

import (
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Route 是聚合行（软删 tombstone）。
type Route struct {
	ID        string
	Seq       int64
	ProjectID string
	Host      string
	Path      string // 空 = /
	AppID     string
	Process   string
	Port      int32
	Protocol  capability.Protocol
	TLSMode   string // auto | none
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (r *Route) Deleted() bool { return r.DeletedAt != "" }
