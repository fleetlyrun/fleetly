// Package token 是 Token 聚合的领域面（CONTEXT.md Token 词条：携带 Scope
// 的凭证——Scope 经 role 间接持有；sha256 存储，明文只在创建响应出现
// 一次。材质生成与前缀族在 identity 种子包）。行类型与 Repo 接口在此
// 单源，SQLite 实现住 infrastructure/persistence/token（ADR-0075 Repo
// 接口层）。
package token

// Token 是聚合行（SHA256 是明文的 hex 摘要查询键；LastUsedAt 空串=从未
// 使用；ExpiresAt 空串=永不过期）。
type Token struct {
	ID         string
	Seq        int64
	Name       string
	TeamID     string
	UserID     string
	RoleID     string
	SHA256     string
	Prefix     string
	Revoked    bool
	LastUsedAt string
	CreatedAt  string
	// ExpiresAt 是过期时刻（UTC RFC3339 秒精度；'' = 永不过期）。只有
	// Login 铸造的密码会话 Token 带值（S18-B / R14-M1）；bootstrap、
	// tokens create、CLI init 铸造的 Agent/CI Token 保持 ''——authn 拦截器
	// 逐请求判定，过期即 E_UNAUTHENTICATED。
	ExpiresAt string
}
