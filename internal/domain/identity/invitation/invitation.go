// Package invitation 是 Invitation 聚合的领域面（F0.5 邀请流：一次性、
// 短时窗、绑 team+role；sha256 存储，明文只在创建响应出现一次——材质
// 生成在 identity 种子包）。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/invitation（ADR-0075 Repo 接口层）。
package invitation

// Invitation 是聚合行（ConsumedAt 空串=未使用；ExpiresAt 是 RFC3339）。
type Invitation struct {
	ID          string
	Seq         int64
	TokenSHA256 string
	TeamID      string
	RoleID      string
	CreatedBy   string
	ExpiresAt   string
	ConsumedAt  string
	CreatedAt   string
}
