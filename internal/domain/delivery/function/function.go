// Package function 是 Function 聚合的领域面（ADR-0077 一等实体；设计真源
// docs/design/2026-10-16-functions-api.md v3；实施 ADR-0102）。行类型与
// Repo 接口在此单源（ADR-0075 决策 3：domain 限界上下文持行 + 接口，
// persistence 实现）。任务①（proto 批）只立锚缝所需最小面——anchor 归属
// 解析一跳；列面与方法集随任务②（两表迁移）同批扩全。
package function

import "context"

// Function 是聚合行（任务①最小面 = 归属解析所需列；任务②扩全：name/
// state/status/容量三列/exposure 两列/current_revision_id/generation/
// spec_fingerprint/时间戳——设计 v3 §4.1）。
type Function struct {
	ID        string
	ProjectID string
}

// Repo 是 Function 聚合存取（任务①最小面；任务②随 functions/
// function_revisions 两表扩全：Create/Get/List/换轨 CAS/容量 CAS/
// Prune（ADR-0101 保留窗）/DeleteAll 等）。
type Repo interface {
	// Get 按 ID 直读（活跃档；tombstone 不可见——冻结/授权沿活跃行口径，
	// App 同款）。
	Get(ctx context.Context, id string) (*Function, error)
}
