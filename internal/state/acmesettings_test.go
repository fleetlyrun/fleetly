package state

// acme.* 设置面测试（B 线 W5 设计 §3，W5-S3）：往返、联动校验门两负路径
// （wildcard 无 provider → 422；wildcard 无 base_domain → 409）、审计零明
// 文（凭证材料零出现）、零事件（事件面收敛为审计行——实现票口径）。

import (
	"context"
	"strings"
	"testing"
)

// TestAcmeSettingsRoundtrip 保存 → 读取逐字段一致；空库缺省态 = none/false。
func TestAcmeSettingsRoundtrip(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	got, err := st.LoadAcmeSettings(ctx)
	if err != nil {
		t.Fatalf("LoadAcmeSettings (empty): %v", err)
	}
	if got.DNSProvider != AcmeDNSProviderNone || got.CredentialsSet() || got.Wildcard {
		t.Fatalf("empty-db settings = %+v, want none defaults", got)
	}

	// storedForm 是测试用「存储形态」标记值——state 层存密文不解释（与
	// s3 同款；envelope 加解密边界测试在 api 面）。
	storedForm := "ciphertext-not-plaintext"
	in := AcmeSettings{
		DNSProvider:       AcmeDNSProviderDNSPod,
		CredentialsCipher: storedForm,
		Wildcard:          false, // 无 base_domain 的测试库形态：只验凭证面往返
	}
	if err := st.SaveAcmeSettings(ctx, in, AcmeSaveOptions{BaseDomain: "example.test", Actor: "human", ActorTokenID: "tok_1"}); err != nil {
		t.Fatalf("SaveAcmeSettings: %v", err)
	}
	got, err = st.LoadAcmeSettings(ctx)
	if err != nil {
		t.Fatalf("LoadAcmeSettings: %v", err)
	}
	in.UpdatedAt = got.UpdatedAt // 时间列是保存侧分配的，不参与字段比对
	if got != in {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, in)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be set after save")
	}
	if !got.CredentialsSet() {
		t.Fatal("CredentialsSet should be true with stored cipher")
	}
}

// TestAcmeSettingsValidation 联动校验（实现票口径）：wildcard 无 provider
// → E_ACME_WILDCARD_REQUIRES_PROVIDER（422）；wildcard 无 base_domain →
// E_ACME_WILDCARD_REQUIRES_BASE_DOMAIN（409）；词表外 provider 拒绝；
// 关闭开关不约束凭证形态。
func TestAcmeSettingsValidation(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		in         AcmeSettings
		baseDomain string
		wantCode   string
		wantHTTP   string
	}{
		{
			name:     "wildcard without provider",
			in:       AcmeSettings{DNSProvider: AcmeDNSProviderNone, Wildcard: true},
			wantCode: "E_ACME_WILDCARD_REQUIRES_PROVIDER", wantHTTP: "422",
		},
		{
			name:     "wildcard without base domain",
			in:       AcmeSettings{DNSProvider: AcmeDNSProviderDNSPod, Wildcard: true},
			wantCode: "E_ACME_WILDCARD_REQUIRES_BASE_DOMAIN", wantHTTP: "409",
		},
		{
			name:     "unknown provider vocabulary",
			in:       AcmeSettings{DNSProvider: "aliyun", Wildcard: false},
			wantCode: "", wantHTTP: "",
		},
	}
	for _, tc := range cases {
		base := "example.test"
		if tc.name == "wildcard without base domain" {
			base = "" // 显式空——门禁判定面
		}
		err := st.SaveAcmeSettings(ctx, tc.in, AcmeSaveOptions{BaseDomain: base, Actor: "human"})
		if tc.wantCode == "" {
			if err == nil || !strings.Contains(err.Error(), "not in {none, dnspod, cloudflare}") {
				t.Fatalf("case %s: want vocabulary error, got %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantCode) {
			t.Fatalf("case %s: want %s, got %v", tc.name, tc.wantCode, err)
		}
		// HTTP 映射断言在 api 信封化测试（apperr.HTTP 透传）。
	}
}

// TestAcmeSettingsGatePositivePath 正路径：wildcard=true + provider 就绪 +
// base_domain 在位 → 保存成功（联动门全绿形态）。
func TestAcmeSettingsGatePositivePath(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	in := AcmeSettings{DNSProvider: AcmeDNSProviderCloudflare, CredentialsCipher: "ct", Wildcard: true}
	if err := st.SaveAcmeSettings(ctx, in, AcmeSaveOptions{BaseDomain: "example.test", Actor: "human"}); err != nil {
		t.Fatalf("wildcard+provider+base_domain must save, got %v", err)
	}
}

// TestAcmeSettingsAuditRedacted 审计脱敏：acme.settings_changed 行的 diff
// 只含 provider/wildcard/凭证有无——凭证材料（明文与密文）零出现。
func TestAcmeSettingsAuditRedacted(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	const plainMarker = "super-secret-plaintext"
	const cipherMarker = "ciphertext-marker-xyz"
	in := AcmeSettings{DNSProvider: AcmeDNSProviderDNSPod, CredentialsCipher: cipherMarker, Wildcard: true}
	if err := st.SaveAcmeSettings(ctx, in, AcmeSaveOptions{BaseDomain: "example.test", Actor: "human", ActorTokenID: "tok_9"}); err != nil {
		t.Fatalf("SaveAcmeSettings: %v", err)
	}
	entries, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action != "acme.settings_changed" {
			if strings.Contains(e.DiffSummary, plainMarker) || strings.Contains(e.DiffSummary, cipherMarker) {
				t.Fatalf("credential material leaked into unrelated audit row %s: %s", e.Action, e.DiffSummary)
			}
			continue
		}
		found = true
		if strings.Contains(e.DiffSummary, plainMarker) || strings.Contains(e.DiffSummary, cipherMarker) {
			t.Fatalf("credential material leaked into acme audit diff: %s", e.DiffSummary)
		}
		for _, want := range []string{"dnspod", "wildcard", "credentials_set"} {
			if !strings.Contains(e.DiffSummary, want) {
				t.Fatalf("audit diff missing %q: %s", want, e.DiffSummary)
			}
		}
		if e.Target != "platform:acme" || e.Actor != "human" {
			t.Fatalf("audit row identity fields wrong: %+v", e)
		}
	}
	if !found {
		t.Fatal("acme.settings_changed audit row not found")
	}
}

// TestAcmeSettingsNoEvent 零事件口径：保存 acme 设置不落任何事件行（事件
// 面收敛为审计行；eventcode 注册表零新增——实现票口径）。
func TestAcmeSettingsNoEvent(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	before := countEvents(t, st)
	in := AcmeSettings{DNSProvider: AcmeDNSProviderCloudflare, CredentialsCipher: "ct", Wildcard: false}
	if err := st.SaveAcmeSettings(ctx, in, AcmeSaveOptions{BaseDomain: "example.test", Actor: "human"}); err != nil {
		t.Fatalf("SaveAcmeSettings: %v", err)
	}
	if after := countEvents(t, st); after != before {
		t.Fatalf("event rows = %d, want unchanged (%d) — acme settings must not emit events", after, before)
	}
}

// countEvents 统计事件表行数（EventsSince 全量拉取的计数形态）。
func countEvents(t *testing.T, st *Store) int {
	t.Helper()
	events, err := st.EventsSince(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	return len(events)
}
