package state

// metrics.* 设置面单测（E6 观测专项设计 §4.1，W5-S3；logsettings_test 同型）：
// 缺省语义（未显式设置 = unset 生效、Set=false——D-W5-2 opt-in）、保存校验、
// 审计+事件同事务、存储值畸形 loud-fail。

import (
	"context"
	"strings"
	"testing"
)

// TestLoadMetricsSettingsDefault 空库/未设置 → 缺省态（Mode=unset，
// Set=false）——D-W5-2 opt-in（默认关、零新增常驻）的语义锚。
func TestLoadMetricsSettingsDefault(t *testing.T) {
	st := newSettingsStore(t)
	in, err := st.LoadMetricsSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadMetricsSettings: %v", err)
	}
	if in.Mode != MetricsModeUnset || in.Set {
		t.Fatalf("default = %+v, want {unset, Set=false}", in)
	}
}

// TestSaveMetricsSettingsRoundTrip 保存回读：显式 on → Mode=on 且 Set=true
//（「未设置」与「显式 on」的区分）；显式 unset 同样 Set=true（用户明确关
// 过——与缺省态区分，Console/CLI 诚实展示）；UpdatedAt 随保存推进。
func TestSaveMetricsSettingsRoundTrip(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	if err := st.SaveMetricsSettings(ctx, MetricsModeOn, MetricsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save on: %v", err)
	}
	in, err := st.LoadMetricsSettings(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if in.Mode != MetricsModeOn || !in.Set || in.UpdatedAt.IsZero() {
		t.Fatalf("settings = %+v, want {on, Set=true, UpdatedAt set}", in)
	}

	if err := st.SaveMetricsSettings(ctx, MetricsModeUnset, MetricsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save unset: %v", err)
	}
	in, err = st.LoadMetricsSettings(ctx)
	if err != nil {
		t.Fatalf("load 2: %v", err)
	}
	if in.Mode != MetricsModeUnset || !in.Set {
		t.Fatalf("settings = %+v, want {unset, Set=true}", in)
	}
}

// TestSaveMetricsSettingsAuditAndEvent 保存同事务落审计（action=
// metrics.mode_updated，diff 只带 mode）与事件（metrics.mode_updated，
// Outbox）——logsettings 同型。
func TestSaveMetricsSettingsAuditAndEvent(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	if err := st.SaveMetricsSettings(ctx, MetricsModeOn, MetricsSaveOptions{Actor: "human", ActorTokenID: "flt_test"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	audit, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	foundAudit := false
	for _, a := range audit {
		if a.Action == "metrics.mode_updated" {
			foundAudit = true
			if !strings.Contains(a.DiffSummary, "on") {
				t.Fatalf("audit diff = %q, want mode value", a.DiffSummary)
			}
			if a.Actor != "human" {
				t.Fatalf("audit actor = %q", a.Actor)
			}
		}
	}
	if !foundAudit {
		t.Fatal("audit row metrics.mode_updated not written")
	}
	evs, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	foundEvent := false
	for _, ev := range evs {
		if ev.Name == "metrics.mode_updated" {
			foundEvent = true
			if !strings.Contains(ev.Payload, "on") {
				t.Fatalf("event payload = %q, want mode value", ev.Payload)
			}
		}
	}
	if !foundEvent {
		t.Fatal("event metrics.mode_updated not appended")
	}
}

// TestSaveMetricsSettingsValidation 值域校验：非法值显式拒绝（不落库、
// 不落审计/事件——fail-closed）。
func TestSaveMetricsSettingsValidation(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	if err := st.SaveMetricsSettings(ctx, "prometheus", MetricsSaveOptions{Actor: "human"}); err == nil {
		t.Fatal("save prometheus must fail")
	}
	if err := st.SaveMetricsSettings(ctx, "", MetricsSaveOptions{Actor: "human"}); err == nil {
		t.Fatal("save empty must fail")
	}
	evs, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, ev := range evs {
		if ev.Name == "metrics.mode_updated" {
			t.Fatal("rejected save must not emit the event (fail-closed)")
		}
	}
	// 库内仍是缺省态。
	in, err := st.LoadMetricsSettings(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if in.Set {
		t.Fatalf("rejected save leaked a row: %+v", in)
	}
}

// TestLoadMetricsSettingsMalformedStored 存储值畸形（越词表）→ loud-fail
//（设置损坏显式报错，不静默回落缺省）。
func TestLoadMetricsSettingsMalformedStored(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	if err := st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)`,
			MetricsKeyMode, "off", 1)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.LoadMetricsSettings(ctx); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("LoadMetricsSettings err = %v, want invalid-value loud fail", err)
	}
}
