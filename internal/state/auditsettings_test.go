package state

// audit.* 设置面单测（v0.3 W3-S1，rbac-teams §6 裁决 D-W0-6）：未设置态
//（RetentionDays=0、Set=false——生效值回落链在消费方：config > 缺省 90）、
// 保存校验、审计 audit.retention_changed 同事务、不落事件（设计 §6 事件
// 注册表不含本键）、存储值畸形 loud-fail。

import (
	"context"
	"strings"
	"testing"
)

// TestLoadAuditSettingsDefault 空库/未设置 → 未设置态（RetentionDays=0，
// Set=false）——本层不投影数值，回落链在消费方（janitor auditRetentionFor）。
func TestLoadAuditSettingsDefault(t *testing.T) {
	st := newSettingsStore(t)
	in, err := st.LoadAuditSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadAuditSettings: %v", err)
	}
	if in.RetentionDays != 0 || in.Set {
		t.Fatalf("default = %+v, want {RetentionDays=0, Set=false}", in)
	}
}

// TestSaveAuditSettingsRoundTrip 保存回读：显式天数 → RetentionDays 命中
// 且 Set=true；UpdatedAt 随保存推进；覆盖保存生效。
func TestSaveAuditSettingsRoundTrip(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	if err := st.SaveRetentionDays(ctx, 30, AuditSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save 30: %v", err)
	}
	in, err := st.LoadAuditSettings(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if in.RetentionDays != 30 || !in.Set || in.UpdatedAt.IsZero() {
		t.Fatalf("settings = %+v, want {30, Set=true, UpdatedAt set}", in)
	}

	if err := st.SaveRetentionDays(ctx, 180, AuditSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save 180: %v", err)
	}
	in, err = st.LoadAuditSettings(ctx)
	if err != nil {
		t.Fatalf("load 2: %v", err)
	}
	if in.RetentionDays != 180 || !in.Set {
		t.Fatalf("settings = %+v, want {180, Set=true}", in)
	}
}

// TestSaveAuditSettingsAuditNoEvent 保存同事务落审计（action=
// audit.retention_changed，diff 只带天数；_changed 后缀 = authsettings 的
// 只审计形态）且**不落事件**（不发明新事件）。
func TestSaveAuditSettingsAuditNoEvent(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	if err := st.SaveRetentionDays(ctx, 30, AuditSaveOptions{Actor: "user:01ADMIN", ActorTokenID: "flt_test"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "audit.retention_changed" {
			found = true
			if !strings.Contains(a.DiffSummary, "30") {
				t.Fatalf("audit diff = %q, want the day count", a.DiffSummary)
			}
			if a.Actor != "user:01ADMIN" || a.Target != "platform:audit" || a.Result != "ok" {
				t.Fatalf("audit row = %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("audit row audit.retention_changed not written")
	}
	evs, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, ev := range evs {
		if ev.Name == "audit.retention_changed" {
			t.Fatal("retention save must not emit an event (no new event names)")
		}
	}
}

// TestSaveAuditSettingsValidation 值域校验：0/负数显式拒绝（不落库、不落
// 审计——fail-closed；把「关掉清理」伪装成合法设置比拒绝更糟）。
func TestSaveAuditSettingsValidation(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	for _, days := range []int{0, -1, -90} {
		if err := st.SaveRetentionDays(ctx, days, AuditSaveOptions{Actor: "human"}); err == nil {
			t.Fatalf("save %d must fail", days)
		}
	}
	in, err := st.LoadAuditSettings(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if in.Set {
		t.Fatalf("rejected saves leaked a row: %+v", in)
	}
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	for _, a := range audits {
		if a.Action == "audit.retention_changed" {
			t.Fatal("rejected save must not write the audit row (fail-closed)")
		}
	}
}

// TestLoadAuditSettingsMalformedStored 存储值畸形（非整数 / 越值域）→
// loud-fail（设置损坏显式报错，不静默回落——与 logs.backend 同口径）。
func TestLoadAuditSettingsMalformedStored(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"non-integer", "ninety"},
		{"zero", "0"},
		{"negative", "-5"},
	} {
		if err := st.InTx(ctx, func(tx *Tx) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)`,
				AuditKeyRetentionDays, tc.value, 1)
			return err
		}); err != nil {
			t.Fatalf("seed %s: %v", tc.name, err)
		}
		if _, err := st.LoadAuditSettings(ctx); err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("%s: LoadAuditSettings err = %v, want invalid-value loud fail", tc.name, err)
		}
		if err := st.InTx(ctx, func(tx *Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM platform_settings WHERE key = ?`, AuditKeyRetentionDays)
			return err
		}); err != nil {
			t.Fatalf("cleanup %s: %v", tc.name, err)
		}
	}
}
