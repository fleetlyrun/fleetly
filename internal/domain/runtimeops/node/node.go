// Package node 是 Node 观测缓存聚合的领域面（架构 §5，ADR-0075 Repo
// 接口层）：nodes 表只是观测缓存，非权威——权威归属判定永远查平台表；
// Provider Watch 流的 node.joined/leave 事实在此落行，节点 ID 永不复用。
// 行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/node。
//
// 节点个体 relay 凭证（H10）住独立表 node_relay_credentials：材质纪律与
// domain/identity 的 Token 同款（前缀可扫描、只落 sha256、明文只出现
// 一次），但生命周期轴是“节点身份”而非用户/团队身份——不复用 Token 表
// 与其 team/user/role 外键面。domain 零 I/O 的材质例外有 identity 先例。
package node

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// RelayCredentialPrefix 是节点 relay 凭证的前缀（泄露扫描识别锚；与
// Token 族的 flt_/fltinv_ 分立——节点材料不进用户 Token 语义面）。
const RelayCredentialPrefix = "fltrelay_"

// relayCredentialBytes 是随机字节数（256-bit 熵；base64url 后 43 字符）。
const relayCredentialBytes = 32

// RelayCredentialMaterial 是一次 relay 凭证生成结果（Secret 明文只在此
// 存在；调用方负责落库前经 Repo 只写 SHA256）。
type RelayCredentialMaterial struct {
	Secret string // fltrelay_ + base64url(32B)；只在铸造响应出现一次
	SHA256 string // hex；存储与查询键
	Prefix string // 前 12 字符；审计/日志
}

// NewRelayCredential 生成节点个体 relay 凭证材质（crypto/rand）。
func NewRelayCredential() (RelayCredentialMaterial, error) {
	raw := make([]byte, relayCredentialBytes)
	if _, err := rand.Read(raw); err != nil {
		return RelayCredentialMaterial{}, fmt.Errorf("node relay credential: entropy source: %w", err)
	}
	secret := RelayCredentialPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return RelayCredentialMaterial{
		Secret: secret,
		SHA256: HashRelayCredential(secret),
		Prefix: secret[:min(len(secret), 12)],
	}, nil
}

// HashRelayCredential 返回明文的 sha256 hex（存储与查询键）。
func HashRelayCredential(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

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

// RelayCredential 是一行节点个体 relay 凭证（库内只落 hash；读面不返回
// 明文——明文的唯一存在点是铸造响应）。
type RelayCredential struct {
	// PlatformID 是平台节点 ID（主键 = 单节点单现役凭证；重铸即替换）。
	PlatformID string
	// CarrierID 是铸造时锚定的载体节点 ID（握手绑定的第二轴）。
	CarrierID string
	// SHA256 是明文的 sha256 hex（查询键；绝不入审计/日志）。
	SHA256 string
	// CreatedAt 是铸造时刻（RFC3339）。
	CreatedAt string
}
