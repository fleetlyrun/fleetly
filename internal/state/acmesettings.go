package state

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// acme.* 运行期设置（B 线 W5 设计 §3，D-V3W5-3/D-V3W5-4，v0.3 W5-S3）：
// platform_settings 库内设置（KV 表——s3settings 同型，**无新迁移**），
// 读取点每次现读不缓存长驻；保存 = 校验 + 审计同事务 fail-closed。
//
// 键面（词表只增；键名常量为本包唯一登记点）：
//   - acme.dns.provider          DNS-01 服务商（none | dnspod | cloudflare；
//     缺省 none——未配置。词表与 internal/acmedns 注册表对齐，一致性由
//     api 面测试互钉）；
//   - acme.dns.credentials_cipher 凭证 envelope 密文（internal/secrets）——
//     **state 层存密文不解释**（s3.secret_access_key 同款纪律）：加密边界
//     在调用方（internal/api），明文绝不进日志/事件/审计/错误（本文件
//     负面测试钉死）；
//   - acme.wildcard              通配证书 opt-in 开关（缺省 false）。true
//     时平台证书期望域名集变 [*.base, console/ctrl/registry.<base>]，签发
//     走 DNS-01（internal/ingress 消费）。
//
// 联动校验（实现票口径）：wildcard=true 须 provider≠none（否则 422 带指
// 引——通配签发没有 DNS 凭据无处应答 challenge），且须 base_domain 非空
//（否则 409——期望域名集由 base_domain 派生，与 E_S3_PUBLIC_REQUIRES_
// BASE_DOMAIN 同构门）。
//
// 事件面：**零新增事件**——本设置族只落审计行 acme.settings_changed
// （实现票口径；证书签发/续期沿用 ingress.cert_* 审计面，eventcode 注册表
// 不扩）。

// acme.* 设置键词表（只增）。
const (
	AcmeKeyDNSProvider    = "acme.dns.provider"
	AcmeKeyDNSCredentials = "acme.dns.credentials_cipher" //nolint:gosec // G101：设置键名字面量，非凭据材料
	AcmeKeyWildcard       = "acme.wildcard"
)

// acme.dns.provider 词表：缺省 none（none 恒在词表——显式「无 DNS 服务商」
// 形态，关闭 wildcard 或撤销凭证时保存）。
const (
	AcmeDNSProviderNone       = "none"
	AcmeDNSProviderDNSPod     = "dnspod"
	AcmeDNSProviderCloudflare = "cloudflare"
)

// AcmeSettings 是 acme.* 设置的 typed 视图。CredentialsCipher 为**存储
// 形态**（envelope 密文，由调用方加密/解密）；DNSProvider 缺省 none。
type AcmeSettings struct {
	// DNSProvider 是 DNS-01 服务商（none | dnspod | cloudflare）。
	DNSProvider string
	// CredentialsCipher 是凭证密文（存储形态）；空 = 未设置凭证。
	CredentialsCipher string
	// Wildcard 是通配证书 opt-in 开关。
	Wildcard bool
	// UpdatedAt 是各设置行 updated_at 的最大值（只读投影）。
	UpdatedAt time.Time
}

// CredentialsSet 报告 DNS 凭证是否已设置（密文非空——调用方以 provider
// 判定可用性时与本函数联用）。
func (s AcmeSettings) CredentialsSet() bool { return s.CredentialsCipher != "" }

// NormalizeDNSProvider 把空 provider 归一为 none（proto 请求缺省语义）。
func NormalizeDNSProvider(provider string) string {
	if strings.TrimSpace(provider) == "" {
		return AcmeDNSProviderNone
	}
	return provider
}

// ValidateDNSProvider 校验 provider 词表（保存与读侧共用；非法值显式拒绝
// ——设置面 loud-fail，不静默回落）。
func ValidateDNSProvider(provider string) error {
	switch provider {
	case AcmeDNSProviderNone, AcmeDNSProviderDNSPod, AcmeDNSProviderCloudflare:
		return nil
	default:
		return fmt.Errorf("state: acme.dns.provider %q not in {none, dnspod, cloudflare}", provider)
	}
}

// ValidateAcmeSettings 是保存前联动校验（fail-fast，实现票口径）：
//   - provider 词表校验（none|dnspod|cloudflare）；
//   - wildcard=true 时 provider 必须非 none（422 带指引——DNS-01 无凭据
//     无处应答 challenge）；
//   - wildcard=true 时 base_domain 必须非空（409——期望域名集
//     [*.base, console/ctrl/registry.<base>] 由 base_domain 派生）。
//
// wildcard=false 不约束 provider/凭证形态（关闭开关是合法的「凭证仍在位」
// 形态——重开免重录）。
func ValidateAcmeSettings(in AcmeSettings, baseDomain string) error {
	provider := NormalizeDNSProvider(in.DNSProvider)
	if err := ValidateDNSProvider(provider); err != nil {
		return err
	}
	if in.Wildcard {
		if provider == AcmeDNSProviderNone {
			return apperr.New("E_ACME_WILDCARD_REQUIRES_PROVIDER",
				"acme.wildcard=true requires acme.dns.provider to be dnspod or cloudflare (a wildcard SAN can only be validated via a DNS-01 challenge, which needs provider credentials)")
		}
		if strings.TrimSpace(baseDomain) == "" {
			return apperr.New("E_ACME_WILDCARD_REQUIRES_BASE_DOMAIN",
				"acme.wildcard=true requires a platform base domain (the wildcard domain set *.base with console/ctrl/registry.<base> derives from it)")
		}
	}
	return nil
}

// LoadAcmeSettings 读取全部 acme.* 设置（每次现读，不缓存长驻）。空库/
// 无行 → 缺省态（provider=none，wildcard=false）。存储值畸形显式报错：
// 设置损坏 loud-fail，不静默回落。
func (s *Store) LoadAcmeSettings(ctx context.Context) (AcmeSettings, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value, updated_at FROM platform_settings WHERE key LIKE 'acme.%'`)
	if err != nil {
		return AcmeSettings{}, fmt.Errorf("state: query acme settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	vals := map[string]string{}
	var latest int64
	for rows.Next() {
		var key, value string
		var updatedAt int64
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			return AcmeSettings{}, fmt.Errorf("state: scan acme setting: %w", err)
		}
		vals[key] = value
		if updatedAt > latest {
			latest = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return AcmeSettings{}, fmt.Errorf("state: iterate acme settings: %w", err)
	}
	out := AcmeSettings{
		DNSProvider:       NormalizeDNSProvider(vals[AcmeKeyDNSProvider]),
		CredentialsCipher: vals[AcmeKeyDNSCredentials], // 密文原样（本层不解释）
	}
	if err := ValidateDNSProvider(out.DNSProvider); err != nil {
		return AcmeSettings{}, fmt.Errorf("state: stored acme setting invalid: %w", err)
	}
	if out.Wildcard, err = parseBoolSetting(AcmeKeyWildcard, vals[AcmeKeyWildcard]); err != nil {
		return AcmeSettings{}, err
	}
	out.UpdatedAt = time.Unix(0, latest).UTC()
	return out, nil
}

// AcmeSaveOptions 是保存的上下文选项：baseDomain 是 wildcard 门禁判定面
// （daemon 配置；空 = 单节点形态）；actor/actorTokenID 进审计（API 面传
// 调用者身份，同事务 fail-closed）。
type AcmeSaveOptions struct {
	BaseDomain   string
	Actor        string // "human"（API 面）——审计 actor 词表不扩
	ActorTokenID string
}

// SaveAcmeSettings 全量保存 acme.* 设置（PUT 语义）+ 审计（同一事务，
// fail-closed）。CredentialsCipher 必须已由调用方 envelope 加密（密文入参
// ——本层不解释密文，也绝不把任何设置值拼进错误/审计：审计 diff 只带
// provider 与 wildcard 开关，凭证材料零出现）。
//
// 事件面（实现票口径）：本族只落审计行，不落事件（eventcode 注册表零
// 新增）——审计 action = acme.settings_changed。
func (s *Store) SaveAcmeSettings(ctx context.Context, in AcmeSettings, opts AcmeSaveOptions) error {
	in.DNSProvider = NormalizeDNSProvider(in.DNSProvider)
	if err := ValidateAcmeSettings(in, opts.BaseDomain); err != nil {
		return err
	}
	values := map[string]string{
		AcmeKeyDNSProvider:    in.DNSProvider,
		AcmeKeyDNSCredentials: in.CredentialsCipher,
		AcmeKeyWildcard:       strconv.FormatBool(in.Wildcard),
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		for _, key := range []string{AcmeKeyDNSProvider, AcmeKeyDNSCredentials, AcmeKeyWildcard} {
			const q = `INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
			if _, err := tx.ExecContext(ctx, q, key, values[key], now); err != nil {
				return fmt.Errorf("state: upsert acme setting %s: %w", key, err)
			}
		}
		// 审计（实现票：acme.settings_changed 零明文）——diff 摘要只含
		// provider 与 wildcard 开关，凭证材料（明文/密文/指纹）零出现。
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        opts.Actor,
			ActorTokenID: opts.ActorTokenID,
			Action:       "acme.settings_changed",
			Target:       "platform:acme",
			Result:       "ok",
			DiffSummary:  DiffSummary("dns_provider", in.DNSProvider, "wildcard", in.Wildcard,
				"credentials_set", in.CredentialsSet()),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("state: save acme settings: %w", err)
	}
	return nil
}
