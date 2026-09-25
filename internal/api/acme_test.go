package api

// ACME DNS-01 设置面集成测试（B 线 W5 设计 §3，W5-S3）：走与生产同构的鉴
// 权链（bufconn + seed token），断言 admin scope 把门、凭证只写不读（读面
// 指纹）、envelope 密文落库、api_token 留空保留语义（SMTP 密码同款先例）、
// 联动校验门（wildcard 无 provider 422 / 无 base_domain 409）、探针正/负
// 路径（插件构造缝注入假件——E_ACME_DNS_TEST_FAILED 信封 context 带失败步）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/acmedns"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// newAcmeTestEnv 起一个带鉴权链 + SystemService（注入 box、base_domain 与
// 假插件构造缝）的 bufconn server，返回 client、store、admin/read token。
func newAcmeTestEnv(t *testing.T, baseDomain string, factory DNSProviderFactory) (serverv1.SystemServiceClient, *state.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), dir+"/acme.db")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(dir + "/acme.key")
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	admin := seedTokenPlain(t, st, "admin")
	read := seedTokenPlain(t, st, "read")

	svc := NewSystemService("dev", st, nil, nil, nil).
		WithJoinGuide(baseDomain, nil).
		WithSecretsBox(box)
	if factory != nil {
		svc = svc.WithDNSProviderFactory(factory)
	}
	srv := newAuthServer(NewAuthenticator(st))
	serverv1.RegisterSystemServiceServer(srv, svc)
	conn := serveBufconn(t, srv)
	return serverv1.NewSystemServiceClient(conn), st, admin, read
}

// recordFakeProvider 是探针测试用的内存假件（记录调用序列；可注入失败）。
type recordFakeProvider struct {
	presentErr error
	cleanupErr error
	calls      []string
}

func (p *recordFakeProvider) Present(_ context.Context, fqdn, _ string) error {
	p.calls = append(p.calls, "present:"+fqdn)
	return p.presentErr
}

func (p *recordFakeProvider) CleanUp(_ context.Context, fqdn, _ string) error {
	p.calls = append(p.calls, "cleanup:"+fqdn)
	return p.cleanupErr
}

// 编译期断言：假件满足插件接口。
var _ acmedns.Provider = (*recordFakeProvider)(nil)

// TestAcmeSettingsFace 设置面主链：scope 把门 → 保存（token 加密落库）→
// 读面只出指纹 → 留空保留语义 → provider=none 清空。
func TestAcmeSettingsFace(t *testing.T) {
	cl, st, admin, read := newAcmeTestEnv(t, "example.test", nil)
	ctx := context.Background()
	const plaintext = "acme-TOKEN-PLAINTEXT-MARKER"

	// scope 把门：read token → PermissionDenied（三面同门）。
	_, err := cl.GetAcmeSettings(authCtx(ctx, read), &serverv1.GetAcmeSettingsRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token on get = %v, want PermissionDenied", status.Code(err))
	}
	_, err = cl.UpdateAcmeSettings(authCtx(ctx, read), &serverv1.UpdateAcmeSettingsRequest{DnsProvider: "dnspod"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token on update = %v, want PermissionDenied", status.Code(err))
	}
	_, err = cl.TestDnsProvider(authCtx(ctx, read), &serverv1.TestDnsProviderRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token on test = %v, want PermissionDenied", status.Code(err))
	}

	// 未配置缺省态：provider=none、无指纹。
	got, err := cl.GetAcmeSettings(authCtx(ctx, admin), &serverv1.GetAcmeSettingsRequest{})
	if err != nil {
		t.Fatalf("GetAcmeSettings (empty): %v", err)
	}
	s := got.GetSettings()
	if s.GetDnsProvider() != state.AcmeDNSProviderNone || s.GetCredentialsFingerprint() != "" || s.GetWildcard() {
		t.Fatalf("empty settings = %+v, want none defaults", s)
	}

	// 保存 dnspod + token：指纹回显、响应无明文、密文落库。
	up, err := cl.UpdateAcmeSettings(authCtx(ctx, admin), &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderDNSPod,
		ApiToken:    plaintext,
	})
	if err != nil {
		t.Fatalf("UpdateAcmeSettings: %v", err)
	}
	sum := sha256.Sum256([]byte(plaintext))
	wantFP := hex.EncodeToString(sum[:8])
	if up.GetSettings().GetCredentialsFingerprint() != wantFP {
		t.Fatalf("fingerprint = %s, want %s", up.GetSettings().GetCredentialsFingerprint(), wantFP)
	}
	if strings.Contains(up.String(), plaintext) {
		t.Fatal("update response must not contain the token plaintext")
	}
	var stored string
	if err := st.InTx(ctx, func(tx *state.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT value FROM platform_settings WHERE key = ?`, state.AcmeKeyDNSCredentials).Scan(&stored)
	}); err != nil {
		t.Fatalf("read stored cipher: %v", err)
	}
	if stored == plaintext || !strings.HasPrefix(stored, "age-encryption.org/v1") {
		t.Fatalf("stored token must be age ciphertext, got prefix %.40q", stored)
	}

	// 留空保留语义：切 provider 保凭证——token 留空 → 指纹不变（密文沿用）。
	up2, err := cl.UpdateAcmeSettings(authCtx(ctx, admin), &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderCloudflare,
	})
	if err != nil {
		t.Fatalf("UpdateAcmeSettings (keep): %v", err)
	}
	if up2.GetSettings().GetCredentialsFingerprint() != wantFP {
		t.Fatalf("blank-token save must keep stored credentials, fingerprint = %s, want %s",
			up2.GetSettings().GetCredentialsFingerprint(), wantFP)
	}

	// provider=none 恒清空凭证。
	if _, err := cl.UpdateAcmeSettings(authCtx(ctx, admin), &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderNone,
	}); err != nil {
		t.Fatalf("UpdateAcmeSettings (none): %v", err)
	}
	got, err = cl.GetAcmeSettings(authCtx(ctx, admin), &serverv1.GetAcmeSettingsRequest{})
	if err != nil {
		t.Fatalf("GetAcmeSettings (after none): %v", err)
	}
	if got.GetSettings().GetCredentialsFingerprint() != "" {
		t.Fatalf("provider=none must clear credentials, fingerprint = %s", got.GetSettings().GetCredentialsFingerprint())
	}

	// 审计脱敏：acme.settings_changed 行零明文（含密文形态零出现）。
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action != "acme.settings_changed" {
			continue
		}
		found = true
		if strings.Contains(a.DiffSummary, plaintext) || strings.Contains(a.DiffSummary, "age-encryption.org") {
			t.Fatalf("audit leaks credential material: %s", a.DiffSummary)
		}
	}
	if !found {
		t.Fatal("audit action acme.settings_changed missing")
	}
}

// TestAcmeSettingsWildcardGateAndDomains wildcard 联动门 + 期望域名集派生：
// wildcard=true 无 provider → 422 E_ACME_WILDCARD_REQUIRES_PROVIDER；无
// base_domain → 409 E_ACME_WILDCARD_REQUIRES_BASE_DOMAIN；就位后读面下发
// 服务端派生的四元域名集。
func TestAcmeSettingsWildcardGateAndDomains(t *testing.T) {
	// ① 单节点形态（base_domain 空）：wildcard+provider → 409。
	clNoBase, _, adminNoBase, _ := newAcmeTestEnv(t, "", nil)
	ctxNoBase := authCtx(context.Background(), adminNoBase)
	_, err := clNoBase.UpdateAcmeSettings(ctxNoBase, &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderDNSPod, ApiToken: "1,tok", Wildcard: true,
	})
	ae, ok := apperr.FromGRPCStatus(status.Convert(err))
	if !ok || ae.Code() != "E_ACME_WILDCARD_REQUIRES_BASE_DOMAIN" {
		t.Fatalf("wildcard without base_domain = %v (ok=%v), want E_ACME_WILDCARD_REQUIRES_BASE_DOMAIN", err, ok)
	}

	// ② 有 base_domain：wildcard 无 provider → 422。
	cl, _, admin, _ := newAcmeTestEnv(t, "example.test", nil)
	ctx := authCtx(context.Background(), admin)
	_, err = cl.UpdateAcmeSettings(ctx, &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderNone, Wildcard: true,
	})
	ae, ok = apperr.FromGRPCStatus(status.Convert(err))
	if !ok || ae.Code() != "E_ACME_WILDCARD_REQUIRES_PROVIDER" {
		t.Fatalf("wildcard without provider = %v (ok=%v), want E_ACME_WILDCARD_REQUIRES_PROVIDER", err, ok)
	}

	// ③ 就位形态：wildcard=true + provider + token → 读面派生四元域名集。
	if _, err := cl.UpdateAcmeSettings(ctx, &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderDNSPod, ApiToken: "1,tok", Wildcard: true,
	}); err != nil {
		t.Fatalf("wildcard+provider save: %v", err)
	}
	got, err := cl.GetAcmeSettings(ctx, &serverv1.GetAcmeSettingsRequest{})
	if err != nil {
		t.Fatalf("GetAcmeSettings: %v", err)
	}
	v := got.GetSettings()
	if !v.GetWildcard() || len(v.GetWildcardDomains()) != 4 {
		t.Fatalf("wildcard view = %+v, want wildcard with 4 derived domains", v)
	}
	want := []string{"*.example.test", "console.example.test", "ctrl.example.test", "registry.example.test"}
	for i, d := range want {
		if v.GetWildcardDomains()[i] != d {
			t.Fatalf("wildcard_domains = %v, want %v", v.GetWildcardDomains(), want)
		}
	}
	if v.GetBaseDomain() != "example.test" {
		t.Fatalf("base_domain = %q, want example.test", v.GetBaseDomain())
	}

	// ④ 关闭开关：派生集清空。
	if _, err := cl.UpdateAcmeSettings(ctx, &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderDNSPod, Wildcard: false,
	}); err != nil {
		t.Fatalf("wildcard off save: %v", err)
	}
	got, err = cl.GetAcmeSettings(ctx, &serverv1.GetAcmeSettingsRequest{})
	if err != nil {
		t.Fatalf("GetAcmeSettings (off): %v", err)
	}
	if got.GetSettings().GetWildcard() || len(got.GetSettings().GetWildcardDomains()) != 0 {
		t.Fatalf("wildcard off view = %+v, want no wildcard/no domains", got.GetSettings())
	}
}

// TestDnsProviderProbe 探针面：无 base_domain 拒绝；无候选且无已存拒绝；
// 候选形态正路径（steps 全 ok，假件收到 create→delete 两步、记录名 =
// _acme-challenge-test.<base>）；create 失败 → E_ACME_DNS_TEST_FAILED 且
// context 带失败步；已存凭证路径。
func TestDnsProviderProbe(t *testing.T) {
	ctx := context.Background()

	// 无 base_domain → E_MULTI_NODE_REQUIRES_BASE_DOMAIN（探针记录名由
	// base_domain 派生，单节点形态显式拒绝；factory 在该分支前不生效）。
	clNoBase, _, adminNoBase, _ := newAcmeTestEnv(t, "", nil)
	_, err := clNoBase.TestDnsProvider(authCtx(ctx, adminNoBase), &serverv1.TestDnsProviderRequest{
		DnsProvider: "dnspod", ApiToken: "1,tok",
	})
	ae, ok := apperr.FromGRPCStatus(status.Convert(err))
	if !ok || ae.Code() != "E_MULTI_NODE_REQUIRES_BASE_DOMAIN" {
		t.Fatalf("probe without base_domain = %v, want E_MULTI_NODE_REQUIRES_BASE_DOMAIN", err)
	}

	fake := &recordFakeProvider{}
	cl, _, admin, read := newAcmeTestEnv(t, "example.test", func(string, []byte) (acmedns.Provider, error) {
		return fake, nil
	})
	actx := authCtx(ctx, admin)

	// 无候选且无已存 → InvalidArgument（形状拒绝）。
	_, err = cl.TestDnsProvider(actx, &serverv1.TestDnsProviderRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("probe with nothing = %v, want InvalidArgument", status.Code(err))
	}
	// 候选形状不齐（有 provider 无 token）→ InvalidArgument。
	_, err = cl.TestDnsProvider(actx, &serverv1.TestDnsProviderRequest{DnsProvider: "dnspod"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("half candidate = %v, want InvalidArgument", status.Code(err))
	}

	// 候选正路径：两步全绿，记录名进结果与假件调用序列。
	res, err := cl.TestDnsProvider(actx, &serverv1.TestDnsProviderRequest{
		DnsProvider: "dnspod", ApiToken: "1,tok",
	})
	if err != nil {
		t.Fatalf("TestDnsProvider (candidate): %v", err)
	}
	r := res.GetResult()
	if !r.GetOk() || r.GetDnsProvider() != "dnspod" || r.GetRecordName() != "_acme-challenge-test.example.test" {
		t.Fatalf("probe result = %+v", r)
	}
	if len(r.GetSteps()) != 2 || r.GetSteps()[0].GetStep() != "create" || r.GetSteps()[1].GetStep() != "delete" {
		t.Fatalf("probe steps = %+v, want create then delete", r.GetSteps())
	}
	if len(fake.calls) != 2 || fake.calls[0] != "present:_acme-challenge-test.example.test" ||
		fake.calls[1] != "cleanup:_acme-challenge-test.example.test" {
		t.Fatalf("fake provider calls = %v, want present+cleanup at the probe record", fake.calls)
	}

	// 负路径：create 失败 → E_ACME_DNS_TEST_FAILED + context（失败步短路，
	// 无记录可删——假件恒收 1 次调用）。
	fakeFail := &recordFakeProvider{presentErr: errors.New("provider boom (fake)")}
	cl2, _, admin2, _ := newAcmeTestEnv(t, "example.test", func(string, []byte) (acmedns.Provider, error) {
		return fakeFail, nil
	})
	_, err = cl2.TestDnsProvider(authCtx(ctx, admin2), &serverv1.TestDnsProviderRequest{
		DnsProvider: "cloudflare", ApiToken: "tok",
	})
	if err == nil {
		t.Fatal("failing probe should error")
	}
	ae, ok = apperr.FromGRPCStatus(status.Convert(err))
	if !ok || ae.Code() != "E_ACME_DNS_TEST_FAILED" {
		t.Fatalf("envelope = %v (ok=%v), want E_ACME_DNS_TEST_FAILED", err, ok)
	}
	if ae.Context()["failed_step"] != "create" || ae.Context()["provider"] != "cloudflare" {
		t.Fatalf("context = %v, want failed_step=create provider=cloudflare", ae.Context())
	}

	// 已存凭证路径：先保存（构造缝返回假件——与探针同件），空候选即测已存。
	if _, err := cl.UpdateAcmeSettings(actx, &serverv1.UpdateAcmeSettingsRequest{
		DnsProvider: state.AcmeDNSProviderDNSPod, ApiToken: "1,tok",
	}); err != nil {
		t.Fatalf("UpdateAcmeSettings: %v", err)
	}
	fake.calls = nil
	res, err = cl.TestDnsProvider(actx, &serverv1.TestDnsProviderRequest{})
	if err != nil {
		t.Fatalf("TestDnsProvider (stored): %v", err)
	}
	if !res.GetResult().GetOk() {
		t.Fatalf("stored probe = %+v, want ok", res.GetResult())
	}

	// scope 把门复述：read token 探针 → PermissionDenied。
	if _, err := cl.TestDnsProvider(authCtx(ctx, read), &serverv1.TestDnsProviderRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token probe = %v, want PermissionDenied", status.Code(err))
	}
}
