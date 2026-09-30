package identity

// Token 材质（F0.6）：前缀化随机串——`flt_` 前缀使泄露可被扫描识别；
// 存储侧只落 sha256；明文只在创建响应出现一次。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Token 前缀族：平台 Token `flt_`、邀请 Token `fltinv_`、Git 触发 Token
// `flthook_`（URL token 与 GitHub webhook secret 同源；泄露扫描的识别锚）。
const (
	TokenPrefix       = "flt_"
	InvitationPrefix  = "fltinv_"
	HookPrefix        = "flthook_"
	secretAlphabetLen = 32 // 随机字节数（base64url 后 43 字符；256-bit 熵）
)

// GeneratedToken 是一次材质生成结果（Secret 明文只在此存在）。
type GeneratedToken struct {
	Secret string // 完整明文（前缀 + base64url 随机段）
	SHA256 string // hex 摘要（存储形态）
	Prefix string // 明文前缀（列表展示；前 12 字符）
}

// NewToken 生成平台 Token 材质。
func NewToken() (GeneratedToken, error) { return generate(TokenPrefix) }

// NewInvitation 生成邀请 Token 材质。
func NewInvitation() (GeneratedToken, error) { return generate(InvitationPrefix) }

// NewHookToken 生成 Git 触发 Token 材质（URL 段与 webhook secret 同串）。
func NewHookToken() (GeneratedToken, error) { return generate(HookPrefix) }

func generate(prefix string) (GeneratedToken, error) {
	raw := make([]byte, secretAlphabetLen)
	if _, err := rand.Read(raw); err != nil {
		return GeneratedToken{}, fmt.Errorf("identity: entropy source: %w", err)
	}
	secret := prefix + base64.RawURLEncoding.EncodeToString(raw)
	return GeneratedToken{
		Secret: secret,
		SHA256: HashToken(secret),
		Prefix: secret[:min(len(secret), 12)],
	}, nil
}

// HashToken 返回明文的 sha256 hex（存储与查询键）。
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// TokenKind 报告明文的前缀归属（platform/invitation/unknown）。
func TokenKind(secret string) string {
	switch {
	case strings.HasPrefix(secret, TokenPrefix):
		return "platform"
	case strings.HasPrefix(secret, InvitationPrefix):
		return "invitation"
	default:
		return "unknown"
	}
}
