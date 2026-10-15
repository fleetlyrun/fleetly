// Package secret 是 Secret 聚合的领域面（CONTEXT.md Secret 词条：Project
// 级敏感值实体，age 信封加密存储，值永不回显只回指纹；注入 App/Task/
// Database）。密文由 internal/material 加解密，repo 只管存取。行类型与
// Repo 接口在此单源，SQLite 实现住 infrastructure/persistence/secret
// （ADR-0075 Repo 接口层路线）。
package secret

// Secret 是聚合行（ciphertext 是 age 信封；fingerprint 供回显对账）。
type Secret struct {
	ID          string
	Seq         int64
	ProjectID   string
	Name        string
	Ciphertext  []byte
	Fingerprint string
	CreatedAt   string
	UpdatedAt   string
	DeletedAt   string
}

// Deleted 报告 tombstone 状态。
func (s *Secret) Deleted() bool { return s.DeletedAt != "" }
