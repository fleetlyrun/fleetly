// Package idempotency 是通用幂等记录聚合的领域面（ADR-0024 F1.1，
// ADR-0075 Repo 接口层）：存储面只管行事实（claim/complete/release/
// sweep），执法语义（指纹计算、重放、冲突分诊）在 application/idem
// 拦截器收口。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/idempotency。
package idempotency

import "time"

// 行状态。
const (
	StateInflight  = "inflight"
	StateCompleted = "completed"
)

// Record 是幂等记录行。
type Record struct {
	Key          string
	Method       string
	Fingerprint  string
	State        string
	ResponseType string
	ResponseBody []byte
	CreatedAt    time.Time
	ExpiresAt    time.Time
}
