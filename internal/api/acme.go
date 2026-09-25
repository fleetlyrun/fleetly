package api

// ACME DNS-01 设置面（B 线 W5 设计 §3，D-V3W5-3/D-V3W5-4，v0.3 W5-S3）：
// SystemService 增三 RPC——GetAcmeSettings/UpdateAcmeSettings/TestDnsProvider
//（platform_settings acme.* 设置，S3 设置面同型先例）。整体 admin scope
// + requirePlatformWriteFace（平台凭据面，W2-S4 收口口径）。
//
// 凭证纪律：明文只在写入请求与服务端内存瞬时出现（envelope 加密落库）；
// 读面只回指纹；探针错误文案只带 provider 状态码/message，凭证材料零出现。
// api_token 留空 = 保留已存凭证（SMTP 密码同款先例——wildcard 开关切换不
// 要求重录）；dns_provider=none 恒清空凭证。

import (
	"context"
	"fmt"
	"strings"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/acmedns"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DNSProviderFactory 是 DNS-01 插件构造缝（acmedns.New 的注入形态——测试
// 指 httptest 假端点；生产零注入）。
type DNSProviderFactory func(name string, credentials []byte) (acmedns.Provider, error)

// WithDNSProviderFactory 注入插件构造缝（链式装配；nil 合法——回落
// acmedns.New 官方端点形态）。
func (s *SystemService) WithDNSProviderFactory(fn DNSProviderFactory) *SystemService {
	s.dnsProviderFactory = fn
	return s
}

// newDNSProvider 按 provider 名构造插件实例（构造缝优先，官方端点兜底）。
func (s *SystemService) newDNSProvider(name string, credentials []byte) (acmedns.Provider, error) {
	if s.dnsProviderFactory != nil {
		return s.dnsProviderFactory(name, credentials)
	}
	return acmedns.New(name, credentials)
}

// GetAcmeSettings ACME DNS-01 设置只读面：凭证只回指纹；wildcard=true 且
// base_domain 非空时下发服务端派生的通配期望域名集（派生公式与签发面同源
// ——ingress 的期望集函数不跨包暴露，api 侧以同一字面集投影，一致性由
// ingress 侧测试互钉）。
func (s *SystemService) GetAcmeSettings(ctx context.Context, _ *serverv1.GetAcmeSettingsRequest) (*serverv1.GetAcmeSettingsResponse, error) {
	// 平台面敏感读门（W2-S4）：ACME 设置 = 平台凭据面（S3 设置同门）。
	if err := requirePlatformWriteFace(ctx, s.st); err != nil {
		return nil, err
	}
	if s.box == nil {
		return nil, status.Error(codes.Unavailable, "secrets box unavailable (not assembled)")
	}
	in, err := s.st.LoadAcmeSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &serverv1.GetAcmeSettingsResponse{Settings: s.acmeSettingsView(ctx, in)}, nil
}

// UpdateAcmeSettings 保存 ACME DNS-01 设置（provider + 凭证 + wildcard）。
// 互斥/联动校验 fail-fast 在 state 层（E_ACME_WILDCARD_REQUIRES_PROVIDER /
// E_ACME_WILDCARD_REQUIRES_BASE_DOMAIN，信封原样透传）；保存 + 审计
// acme.settings_changed 同事务（零明文；本设置族零事件——实现票口径）。
func (s *SystemService) UpdateAcmeSettings(ctx context.Context, req *serverv1.UpdateAcmeSettingsRequest) (*serverv1.UpdateAcmeSettingsResponse, error) {
	// 平台面写门（W2-S4）：ACME 设置保存 = 平台管理员。
	if err := requirePlatformWriteFace(ctx, s.st); err != nil {
		return nil, err
	}
	if s.box == nil {
		return nil, status.Error(codes.Unavailable, "secrets box unavailable (not assembled)")
	}
	provider := state.NormalizeDNSProvider(req.GetDnsProvider())
	credentialsCT, err := s.resolveStoredOrEncryptedCredentials(ctx, provider, req.GetApiToken())
	if err != nil {
		return nil, err
	}
	in := state.AcmeSettings{
		DNSProvider:       provider,
		CredentialsCipher: credentialsCT,
		Wildcard:          req.GetWildcard(),
	}
	opts := state.AcmeSaveOptions{BaseDomain: s.baseDomain, Actor: "human"}
	if p, ok := PrincipalFromContext(ctx); ok {
		opts.ActorTokenID = p.TokenID
	}
	if err := s.st.SaveAcmeSettings(ctx, in, opts); err != nil {
		return nil, err
	}
	return &serverv1.UpdateAcmeSettingsResponse{Settings: s.acmeSettingsView(ctx, in)}, nil
}

// TestDnsProvider DNS 服务商探针：候选凭证（未保存也能测）或已存凭证
// （两字段全空）。在 _acme-challenge-test.<base_domain> 建删真实 TXT——
// 通过 = 能认证/能写/能删（zone 解析在 create 步隐含执行）。失败以
// E_ACME_DNS_TEST_FAILED 报错：失败步与 provider 错误摘要进 context。
func (s *SystemService) TestDnsProvider(ctx context.Context, req *serverv1.TestDnsProviderRequest) (*serverv1.TestDnsProviderResponse, error) {
	// 平台面写门（W2-S4）：探针消耗平台凭据 = admin 级信任面。
	if err := requirePlatformWriteFace(ctx, s.st); err != nil {
		return nil, err
	}
	if s.box == nil {
		return nil, status.Error(codes.Unavailable, "secrets box unavailable (not assembled)")
	}
	if strings.TrimSpace(s.baseDomain) == "" {
		return nil, apperr.New("E_MULTI_NODE_REQUIRES_BASE_DOMAIN",
			"the DNS provider probe requires a platform base domain (the test TXT record _acme-challenge-test.<base_domain> derives from it)")
	}
	providerName := strings.TrimSpace(req.GetDnsProvider())
	tokenPlain := req.GetApiToken()
	if providerName == "" && tokenPlain == "" {
		// 无候选凭证 → 测已存凭证（每次现读）。
		in, err := s.st.LoadAcmeSettings(ctx)
		if err != nil {
			return nil, err
		}
		if in.DNSProvider == state.AcmeDNSProviderNone || !in.CredentialsSet() {
			return nil, statusInvalidArgument(
				"no DNS provider configured yet (acme.dns.provider=none): pass a candidate provider and api token to test")
		}
		providerName = in.DNSProvider
		plain, derr := s.box.Decrypt([]byte(in.CredentialsCipher))
		if derr != nil {
			return nil, fmt.Errorf("decrypt stored acme dns credentials: %w", derr)
		}
		tokenPlain = string(plain)
	} else {
		// 候选形态：provider 与 token 必须齐备（无 token 无从认证）。
		if providerName == "" || tokenPlain == "" {
			return nil, statusInvalidArgument(
				"candidate probe requires both dns_provider and api_token (omit both to test the stored credentials)")
		}
	}
	if providerName != state.AcmeDNSProviderDNSPod && providerName != state.AcmeDNSProviderCloudflare {
		return nil, statusInvalidArgument(fmt.Sprintf(
			"dns_provider %q not in {dnspod, cloudflare}", providerName))
	}

	// 候选凭证同走信封包装（与保存路径同词形归一——探针测的就是将存进
	// 平台的凭证形态；W5-S4 门上修复，见 resolveStoredOrEncryptedCredentials）。
	envelope, werr := acmedns.CredentialsEnvelope(tokenPlain)
	if werr != nil {
		return nil, statusInvalidArgument(werr.Error())
	}
	provider, err := s.newDNSProvider(providerName, envelope)
	if err != nil {
		return nil, statusInvalidArgument(err.Error())
	}
	result, err := s.runDNSProbe(ctx, providerName, provider)
	if err != nil {
		return nil, err
	}
	return &serverv1.TestDnsProviderResponse{Result: result}, nil
}

// runDNSProbe 执行探针并把失败信封化（E_ACME_DNS_TEST_FAILED——失败步 +
// provider 错误摘要进 context；凭证材料零出现）。
func (s *SystemService) runDNSProbe(ctx context.Context, providerName string, provider acmedns.Provider) (*serverv1.DnsProviderTestResult, error) {
	recordName := acmedns.ProbeRecordName(s.baseDomain)
	res := acmedns.Probe(ctx, provider, recordName)
	out := &serverv1.DnsProviderTestResult{
		Ok:          res.OK,
		DnsProvider: providerName,
		RecordName:  recordName,
		Steps:       make([]*serverv1.DnsProbeStep, 0, len(res.Steps)),
	}
	for _, st := range res.Steps {
		out.Steps = append(out.Steps, &serverv1.DnsProbeStep{
			Step:       st.Step,
			Ok:         st.OK,
			DurationMs: st.Cost.Milliseconds(),
			Error:      st.ErrMsg,
		})
	}
	if !res.OK {
		lastErr := ""
		for _, st := range res.Steps {
			if !st.OK {
				lastErr = st.ErrMsg
			}
		}
		return nil, apperr.New("E_ACME_DNS_TEST_FAILED",
			"dns provider test failed at step %s: %s", res.FailedStep, lastErr).
			WithContext("provider", providerName).
			WithContext("record_name", recordName).
			WithContext("failed_step", res.FailedStep)
	}
	return out, nil
}

// resolveStoredOrEncryptedCredentials 计算保存形态的凭证密文：
//   - provider=none → 恒清空（""）；
//   - api_token 非空 → 插件凭证信封包装（CredentialsEnvelope——W5-S4 门上
//     修复：API 面词形是裸 token，插件解析面只认 JSON 信封）后 envelope
//     加密落库；
//   - api_token 留空 → 保留已存密文（SMTP 密码同款先例；已存密文不可读
//     明文再加密——密文原样沿用）。
func (s *SystemService) resolveStoredOrEncryptedCredentials(ctx context.Context, provider, tokenPlain string) (string, error) {
	if provider == state.AcmeDNSProviderNone {
		return "", nil
	}
	if tokenPlain != "" {
		envelope, err := acmedns.CredentialsEnvelope(tokenPlain)
		if err != nil {
			return "", fmt.Errorf("wrap acme dns credentials: %w", err)
		}
		ct, err := s.box.Encrypt(envelope)
		if err != nil {
			return "", fmt.Errorf("encrypt acme dns credentials: %w", err)
		}
		return string(ct), nil
	}
	// 留空 = 保留已存（无已存 = 仍为空——wildcard 门在 state 层兜住
	// provider≠none 但凭证缺失的形态？不——校验只查 provider；凭证缺失时
	// 签发期插件构造失败，诚实失败。此处如实保留空形态）。
	stored, err := s.st.LoadAcmeSettings(ctx)
	if err != nil {
		return "", err
	}
	return stored.CredentialsCipher, nil
}

// acmeSettingsView 把设置构造为脱敏读面投影（凭证解密出指纹，明文不出
// 服务端边界；wildcard 域名集服务端派生）。
func (s *SystemService) acmeSettingsView(ctx context.Context, in state.AcmeSettings) *serverv1.AcmeSettingsView {
	v := &serverv1.AcmeSettingsView{
		DnsProvider: state.NormalizeDNSProvider(in.DNSProvider),
		Wildcard:    in.Wildcard,
		UpdatedAt:   tstamp(in.UpdatedAt),
		BaseDomain:  s.baseDomain,
	}
	if in.Wildcard && strings.TrimSpace(s.baseDomain) != "" {
		base := s.baseDomain
		v.WildcardDomains = []string{
			"*." + base,
			"console." + base,
			"ctrl." + base,
			"registry." + base,
		}
	}
	if in.CredentialsCipher != "" {
		plain, err := s.box.Decrypt([]byte(in.CredentialsCipher))
		if err != nil {
			// 指纹面解密失败 = 平台密钥面故障：字段留空并如实降级（不
			// 500 整个读面——设置事实仍可读；解密故障在 GetSystemStatus
			// 的 state.secrets 组件红面可见）。
			return v
		}
		v.CredentialsFingerprint = secretFingerprint(plain)
	}
	return v
}
