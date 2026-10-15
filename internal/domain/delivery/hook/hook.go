// Package hook 是 per-App Git 触发配置聚合的领域面（F0.13，ADR-0075
// Repo 接口层）：URL token 与 GitHub webhook secret 同源——token_sha256
// 是 URL 查找键，secret_ciphertext 是 age 信封（HMAC 验签需原串，纯摘要
// 不可逆）；明文只在铸造响应出现一次。行类型与 Repo 接口在此单源，
// SQLite 实现住 infrastructure/persistence/hook（驱动与加密机械留实现侧）。
package hook

// Hook 是聚合行（WatchPaths 以 JSON 数组落库；SecretCiphertext 是 age 信封）。
type Hook struct {
	AppID            string
	Repo             string
	Branch           string
	Dockerfile       string
	WatchPaths       []string
	TokenSHA256      string
	TokenPrefix      string
	SecretCiphertext []byte
	CreatedAt        string
	UpdatedAt        string
}
