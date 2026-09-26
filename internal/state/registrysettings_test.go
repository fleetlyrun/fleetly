package state

import (
	"context"
	"strings"
	"testing"
)

// registry.* 设置面测试（IMPL-T1-2/DT-2）：往返与归一、清除语义、密文/
// 指纹同生共死（保存拒绝 + 存储损坏 loud-fail）、审计与事件脱敏（凭据
// 材料零出现——事件只带 host 与指纹）。

// TestRegistrySettingsRoundtrip 保存 → 读取逐字段一致（含 host 归一）；
// 空库缺省态 = 零值（未配置）。
func TestRegistrySettingsRoundtrip(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	got, err := st.LoadRegistrySettings(ctx)
	if err != nil {
		t.Fatalf("LoadRegistrySettings (empty): %v", err)
	}
	if got.Host != "" || got.Username != "" || got.CredentialsSet() {
		t.Fatalf("empty-db settings = %+v, want zero defaults", got)
	}

	// storedForm 是测试用「存储形态」标记值——state 层存密文不解释，
	// 这里只验证原样存取（envelope 加解密边界测试在 api 面）。
	storedForm := "ciphertext-not-plaintext"
	in := RegistrySettings{
		Host:                "https://GHCR.io/",
		Username:            "robot",
		PasswordCipher:      storedForm,
		PasswordFingerprint: "1234abcd",
	}
	if err := st.SaveRegistrySettings(ctx, in, RegistrySaveOptions{Actor: "human", ActorTokenID: "tok_1"}); err != nil {
		t.Fatalf("SaveRegistrySettings: %v", err)
	}
	got, err = st.LoadRegistrySettings(ctx)
	if err != nil {
		t.Fatalf("LoadRegistrySettings: %v", err)
	}
	want := RegistrySettings{
		Host:                "ghcr.io", // scheme/大小写/尾斜杠归一
		Username:            "robot",
		PasswordCipher:      storedForm,
		PasswordFingerprint: "1234abcd",
		UpdatedAt:           got.UpdatedAt,
	}
	if got != want {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be set after save")
	}
	if !got.CredentialsSet() {
		t.Fatal("CredentialsSet should be true with stored cipher")
	}

	// docker.io 家族归一到 registry-1.docker.io（引用匹配单一事实源）。
	if err := st.SaveRegistrySettings(ctx, RegistrySettings{
		Host: "docker.io", Username: "u", PasswordCipher: "ct", PasswordFingerprint: "abcd1234",
	}, RegistrySaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("SaveRegistrySettings (docker.io): %v", err)
	}
	got, err = st.LoadRegistrySettings(ctx)
	if err != nil {
		t.Fatalf("LoadRegistrySettings (docker.io): %v", err)
	}
	if got.Host != "registry-1.docker.io" {
		t.Fatalf("host = %q, want registry-1.docker.io (Docker Hub normalization)", got.Host)
	}
}

// TestRegistrySettingsClear 清除语义：host 空 = 四键齐清（不留半截凭证）。
func TestRegistrySettingsClear(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	if err := st.SaveRegistrySettings(ctx, RegistrySettings{
		Host: "ghcr.io", Username: "robot", PasswordCipher: "ct", PasswordFingerprint: "abcd1234",
	}, RegistrySaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("SaveRegistrySettings: %v", err)
	}
	if err := st.SaveRegistrySettings(ctx, RegistrySettings{}, RegistrySaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("SaveRegistrySettings (clear): %v", err)
	}
	got, err := st.LoadRegistrySettings(ctx)
	if err != nil {
		t.Fatalf("LoadRegistrySettings after clear: %v", err)
	}
	if got.Host != "" || got.Username != "" || got.PasswordCipher != "" || got.PasswordFingerprint != "" {
		t.Fatalf("settings after clear = %+v, want all four keys empty", got)
	}
}

// TestRegistrySettingsValidation 存储形态校验：密文/指纹必须同生共死；
// host 带路径段拒绝；host 非空无密码 = 合法匿名形态。
func TestRegistrySettingsValidation(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		in      RegistrySettings
		wantErr string
	}{
		{
			name:    "cipher without fingerprint",
			in:      RegistrySettings{Host: "ghcr.io", PasswordCipher: "ct"},
			wantErr: "cipher and fingerprint",
		},
		{
			name:    "fingerprint without cipher",
			in:      RegistrySettings{Host: "ghcr.io", PasswordFingerprint: "abcd1234"},
			wantErr: "cipher and fingerprint",
		},
		{
			name:    "host with path segment",
			in:      RegistrySettings{Host: "ghcr.io/owner"},
			wantErr: "bare host",
		},
		{
			name:    "host with whitespace",
			in:      RegistrySettings{Host: "ghcr io"},
			wantErr: "bare host",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.SaveRegistrySettings(ctx, tc.in, RegistrySaveOptions{Actor: "human"})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}

	// 正路径：host 非空、无密码 = 合法（匿名访问形态；匹配只影响不注入）。
	if err := st.SaveRegistrySettings(ctx, RegistrySettings{Host: "ghcr.io", Username: ""},
		RegistrySaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("anonymous host-only settings should save: %v", err)
	}
}

// TestRegistrySettingsStoredCorruptionFailsLoudly 存储损坏 loud-fail：
// 绕过保存路径直写半截形态（密文无指纹 / 无 host 残留凭证），读取显式报错。
func TestRegistrySettingsStoredCorruptionFailsLoudly(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	corrupt := `INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, 1)`
	if err := st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, corrupt, RegistryKeyHost, "ghcr.io")
		return err
	}); err != nil {
		t.Fatalf("seed host row: %v", err)
	}
	if err := st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, corrupt, RegistryKeyPasswordCipher, "ct-without-fingerprint")
		return err
	}); err != nil {
		t.Fatalf("seed cipher row: %v", err)
	}
	if _, err := st.LoadRegistrySettings(ctx); err == nil ||
		!strings.Contains(err.Error(), "cipher and fingerprint") {
		t.Fatalf("LoadRegistrySettings = %v, want loud cipher/fingerprint corruption failure", err)
	}

	// 无 host 残留凭证的形态同样 loud-fail。
	st2 := newSettingsStore(t)
	if err := st2.InTx(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, corrupt, RegistryKeyUsername, "robot")
		return err
	}); err != nil {
		t.Fatalf("seed username row: %v", err)
	}
	if _, err := st2.LoadRegistrySettings(ctx); err == nil ||
		!strings.Contains(err.Error(), "without a host") {
		t.Fatalf("LoadRegistrySettings = %v, want loud credentials-without-host failure", err)
	}
}

// TestRegistrySettingsAuditAndEventRedacted 审计 + 事件同事务落库且脱敏：
// 密码密文零出现；事件只带 host 与指纹（注册表只增口径的 payload 契约）。
func TestRegistrySettingsAuditAndEventRedacted(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	const cipherMarker = "CIPHERTEXT-MARKER-9f8e7d6c"
	in := RegistrySettings{
		Host:                "ghcr.io",
		Username:            "robot",
		PasswordCipher:      cipherMarker,
		PasswordFingerprint: "deadbeef",
	}
	if err := st.SaveRegistrySettings(ctx, in, RegistrySaveOptions{Actor: "human", ActorTokenID: "tok_e"}); err != nil {
		t.Fatalf("SaveRegistrySettings: %v", err)
	}

	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var auditSeen bool
	for _, a := range audits {
		if a.Action != "registry.updated" {
			continue
		}
		auditSeen = true
		if strings.Contains(a.DiffSummary, cipherMarker) {
			t.Fatalf("audit diff leaks the password cipher: %s", a.DiffSummary)
		}
		if !strings.Contains(a.DiffSummary, "ghcr.io") || !strings.Contains(a.DiffSummary, "true") {
			t.Fatalf("audit diff should carry host and password_set: %s", a.DiffSummary)
		}
	}
	if !auditSeen {
		t.Fatal("audit action registry.updated missing")
	}

	events, err := st.EventsSince(ctx, 0, 10)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	var eventSeen bool
	for _, ev := range events {
		if ev.Name != "registry.updated" {
			continue
		}
		eventSeen = true
		if strings.Contains(ev.Payload, cipherMarker) {
			t.Fatalf("event payload leaks the password cipher: %s", ev.Payload)
		}
		if !strings.Contains(ev.Payload, "ghcr.io") || !strings.Contains(ev.Payload, "deadbeef") {
			t.Fatalf("event payload should carry host and fingerprint: %s", ev.Payload)
		}
	}
	if !eventSeen {
		t.Fatal("event registry.updated missing")
	}
}
