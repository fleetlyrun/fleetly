package state

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/imageregistry"
)

// registry.* 运行期设置（IMPL-T1-2/DT-2 设计冻结；platform_settings KV
// 表——s3settings/acmesettings 同型，**无新迁移**），供部署路径的外部
// registry 凭证下发（tag→digest 解析带凭证 + service create/update 的
// X-Registry-Auth 编码）。
//
// 键面（词表只增；键名常量为本包唯一登记点）：
//   - registry.host                 外部 registry host（ghcr.io 形态；唯一
//     必填项，空 = 未配置/清除——解析腿回落匿名 + 本机 inspect）；
//   - registry.username             Basic Auth 用户名（可空 = 仅密码形态
//     由 registry 裁决）；
//   - registry.password_cipher     密码 envelope 密文（internal/secrets）——
//     **state 层存密文不解释**（s3.secret_access_key 同款纪律）：加密边界
//     在调用方（internal/api），明文绝不进日志/事件/审计/错误；
//   - registry.password_fingerprint 密码指纹（明文 sha256 前 8 hex，读面
//     脱敏投影）——保存时与密文同事务写入，读面免解密即出指纹。
//
// 语义（冻结口径）：host 为空 = 清除全部四键；host 非空且密码留空 =
// 保留已存密码（ACME api_token 先例——改用户名/主机不强制重录）；密码
// 非空 = 覆盖 + 新指纹。
//
// 事件面：registry.updated（只带 host 与指纹，凭据材料零出现——负面测试
// 钉死；eventcode 注册表只增登记）。

// registry.* 设置键词表（只增）。
const (
	RegistryKeyHost                = "registry.host"
	RegistryKeyUsername            = "registry.username"
	RegistryKeyPasswordCipher      = "registry.password_cipher" //nolint:gosec // G101：设置键名字面量，非凭据材料
	RegistryKeyPasswordFingerprint = "registry.password_fingerprint"
)

// registrySettingsKeys 是保存时全量落库的键清单（PUT 语义：每次保存写全
// 四键，清除形态落空值——避免「换 host 残留旧凭证」的静默状态）。
var registrySettingsKeys = []string{
	RegistryKeyHost, RegistryKeyUsername, RegistryKeyPasswordCipher, RegistryKeyPasswordFingerprint,
}

// RegistrySettings 是 registry.* 设置的 typed 视图。PasswordCipher 为
// **存储形态**（envelope 密文，由调用方加密/解密）；Host 为空 = 未配置。
type RegistrySettings struct {
	// Host 是外部 registry host（归一形态：小写、无 scheme、docker.io →
	// registry-1.docker.io）。
	Host string
	// Username 是 Basic Auth 用户名。
	Username string
	// PasswordCipher 是密码密文（存储形态）；空 = 未设置密码。
	PasswordCipher string
	// PasswordFingerprint 是密码指纹（sha256 前 8 hex）；空 = 未设置。
	PasswordFingerprint string
	// UpdatedAt 是各设置行 updated_at 的最大值（只读投影）。
	UpdatedAt time.Time
}

// CredentialsSet 报告密码是否已设置（密文非空）。
func (s RegistrySettings) CredentialsSet() bool { return s.PasswordCipher != "" }

// NormalizeRegistryHost 把用户输入的 registry host 归一为引用可匹配的
// 形态（去空白/小写/去 scheme/去尾斜杠；docker.io 家族 → registry-1.
// docker.io）——与 imageregistry.Parse 的 host 归一同一函数（单一事实源）。
func NormalizeRegistryHost(host string) string { return imageregistry.NormalizeHost(host) }

// ValidateRegistrySettings 是保存前的存储形态校验（fail-fast）：
//   - 密文与指纹必须同生共死（任一有值另一为空 = 状态损坏，loud-fail）；
//   - host 为空 = 合法清除形态（不约束其余字段——保存侧统一清空）；
//   - host 非空：不得含空白/斜杠（scheme 与路径在归一后仍残留者显式拒绝
//     ——API 面预校验，此处兜底内部编码错误）。
func ValidateRegistrySettings(in RegistrySettings) error {
	if (in.PasswordCipher == "") != (in.PasswordFingerprint == "") {
		return fmt.Errorf("state: registry password cipher and fingerprint must be set together")
	}
	host := NormalizeRegistryHost(in.Host)
	if host == "" {
		return nil
	}
	if strings.ContainsAny(host, " \t/") {
		return fmt.Errorf("state: registry host %q is not a bare host[:port] form", in.Host)
	}
	return nil
}

// LoadRegistrySettings 读取全部 registry.* 设置（每次现读，不缓存长驻；
// 部署路径每次解析与取名现读——保存即对下一次部署生效）。空库/无行 →
// 零值（未配置）。存储值畸形显式报错：设置损坏 loud-fail，不静默回落。
func (s *Store) LoadRegistrySettings(ctx context.Context) (RegistrySettings, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value, updated_at FROM platform_settings WHERE key LIKE 'registry.%'`)
	if err != nil {
		return RegistrySettings{}, fmt.Errorf("state: query registry settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	vals := map[string]string{}
	var latest int64
	for rows.Next() {
		var key, value string
		var updatedAt int64
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			return RegistrySettings{}, fmt.Errorf("state: scan registry setting: %w", err)
		}
		vals[key] = value
		if updatedAt > latest {
			latest = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return RegistrySettings{}, fmt.Errorf("state: iterate registry settings: %w", err)
	}
	out := RegistrySettings{
		Host:                NormalizeRegistryHost(vals[RegistryKeyHost]),
		Username:            vals[RegistryKeyUsername],
		PasswordCipher:      vals[RegistryKeyPasswordCipher],
		PasswordFingerprint: vals[RegistryKeyPasswordFingerprint],
	}
	if err := ValidateRegistrySettings(out); err != nil {
		return RegistrySettings{}, fmt.Errorf("state: stored registry settings invalid: %w", err)
	}
	if out.Host == "" && (out.Username != "" || out.PasswordCipher != "" || out.PasswordFingerprint != "") {
		return RegistrySettings{}, fmt.Errorf("state: stored registry settings carry credentials without a host (clear must remove all four keys)")
	}
	out.UpdatedAt = time.Unix(0, latest).UTC()
	return out, nil
}

// RegistrySaveOptions 是保存的上下文选项：actor/actorTokenID 进审计（API
// 面传调用者身份，同事务 fail-closed）。
type RegistrySaveOptions struct {
	Actor        string // "human"（API 面）——审计 actor 词表不扩
	ActorTokenID string
}

// SaveRegistrySettings 全量保存 registry.* 设置 + 审计 + 事件
// registry.updated（同一事务，fail-closed）。PasswordCipher 必须已由调用
// 方 envelope 加密（密文入参——本层不解释密文）；审计 diff 与事件 payload
// 均不含任何凭据材料（事件只带 host 与指纹）。
func (s *Store) SaveRegistrySettings(ctx context.Context, in RegistrySettings, opts RegistrySaveOptions) error {
	in.Host = NormalizeRegistryHost(in.Host)
	if err := ValidateRegistrySettings(in); err != nil {
		return err
	}
	if in.Host == "" {
		// 清除形态：四键齐清（不留半截凭证）。
		in.Username = ""
		in.PasswordCipher = ""
		in.PasswordFingerprint = ""
	}
	values := map[string]string{
		RegistryKeyHost:                in.Host,
		RegistryKeyUsername:            in.Username,
		RegistryKeyPasswordCipher:      in.PasswordCipher,
		RegistryKeyPasswordFingerprint: in.PasswordFingerprint,
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		for _, key := range registrySettingsKeys {
			const q = `INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
			if _, err := tx.ExecContext(ctx, q, key, values[key], now); err != nil {
				return fmt.Errorf("state: upsert registry setting %s: %w", key, err)
			}
		}
		// 审计：diff 摘要只含 host/username 与密码有无——密码明文/密文零出现。
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        opts.Actor,
			ActorTokenID: opts.ActorTokenID,
			Action:       "registry.updated",
			Target:       "platform:registry",
			Result:       "ok",
			DiffSummary:  DiffSummary("host", in.Host, "username", in.Username, "password_set", in.CredentialsSet()),
		}); err != nil {
			return err
		}
		// 事件 registry.updated（只带 host 与指纹；与业务写同事务 = Outbox）。
		if _, err := tx.AppendEvent(ctx, Event{
			Name:    "registry.updated",
			Subject: "platform:registry",
			Payload: DiffSummary("host", in.Host, "password_fingerprint", in.PasswordFingerprint),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("state: save registry settings: %w", err)
	}
	return nil
}
