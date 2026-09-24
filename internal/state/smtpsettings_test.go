package state

import (
	"context"
	"strings"
	"testing"
)

// notify.smtp.* 设置面测试（observability 设计 §8.3，W4-S3）：加密往返
// （密文原样存取——state 层不解释）、PUT 全量语义、校验面、审计零泄漏
// （密文/明文材料零出现在审计 diff；只落审计不落事件——红线延伸）。

func TestSmtpSettingsRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 空库缺省态。
	got, err := st.LoadSmtpSettings(ctx)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if got.Host != "" || got.Port != 0 || got.PasswordCipher != "" {
		t.Fatalf("empty settings must be all-zero: %+v", got)
	}

	// 保存（密文入参——本层不解释）+ 读回。
	err = st.SaveSmtpSettings(ctx, SmtpSettings{
		Host: "smtp.example.test", Port: 587,
		Username: "fleetly", PasswordCipher: "CIPHER-MATERIAL",
		From: "fleetly@example.test",
	}, SmtpSaveOptions{Actor: "human"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = st.LoadSmtpSettings(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Host != "smtp.example.test" || got.Port != 587 ||
		got.Username != "fleetly" || got.PasswordCipher != "CIPHER-MATERIAL" ||
		got.From != "fleetly@example.test" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("updated_at must be projected")
	}

	// PUT 全量语义：第二次保存不提供密码 → 密码位清空（不残留旧值）。
	err = st.SaveSmtpSettings(ctx, SmtpSettings{
		Host: "smtp2.example.test", Port: 25, From: "fleetly@example.test",
	}, SmtpSaveOptions{Actor: "human"})
	if err != nil {
		t.Fatalf("save 2: %v", err)
	}
	got, _ = st.LoadSmtpSettings(ctx)
	if got.Host != "smtp2.example.test" || got.Port != 25 || got.PasswordCipher != "" || got.Username != "" {
		t.Fatalf("PUT semantics must clear absent keys: %+v", got)
	}
}

func TestSmtpSettingsValidation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	bad := []SmtpSettings{
		{Port: 587, From: "f@example.test"},                              // host 缺
		{Host: "smtp.example.test", From: "f@example.test"},              // port 0
		{Host: "smtp.example.test", Port: 70000, From: "f@example.test"}, // port 超界
		{Host: "smtp.example.test", Port: 587},                           // from 缺
		{Host: "smtp.example.test", Port: 587, From: "not a mailbox"},    // from 非法
	}
	for i, in := range bad {
		if err := st.SaveSmtpSettings(ctx, in, SmtpSaveOptions{Actor: "human"}); err == nil {
			t.Fatalf("case %d must be rejected: %+v", i, in)
		}
	}
}

// TestSmtpSettingsNeverInAudit：密码密文/明文、用户名值零出现在审计
// diff（state-model §2.9；§8.3 红线——密码零泄漏入审计/日志/事件）；
// 同时断言不落事件（通知配置面自身零事件——§5.2 红线延伸）。
func TestSmtpSettingsNeverInAudit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	err := st.SaveSmtpSettings(ctx, SmtpSettings{
		Host: "smtp.example.test", Port: 587,
		Username: "secret-user", PasswordCipher: "TOP-SECRET-CIPHER",
		From: "fleetly@example.test",
	}, SmtpSaveOptions{Actor: "human"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action != "notify.smtp_changed" {
			t.Fatalf("unexpected audit action %q on smtp settings save", a.Action)
		}
		found = true
		for _, banned := range []string{"TOP-SECRET-CIPHER", "secret-user", "CIPHER"} {
			if strings.Contains(a.DiffSummary, banned) {
				t.Fatalf("audit diff leaks %q: %s", banned, a.DiffSummary)
			}
		}
	}
	if !found {
		t.Fatal("notify.smtp_changed audit row missing")
	}

	// 零事件断言：事件流不得出现任何 notify.* / smtp 事件。
	rows, err := st.db.QueryContext(ctx, `SELECT name FROM events`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		if strings.HasPrefix(name, "notify.") || strings.Contains(name, "smtp") {
			t.Fatalf("smtp settings must not emit events (zero-event red line), got %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate events: %v", err)
	}
}
