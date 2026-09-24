package state

// git.hostkey_fingerprint 指纹台账的 state 层断言（FZ-12 D-W0-8 的写入通
// 道单点）：首启静默建账 / 同值幂等 / 台账不同（改台账模拟换钥后装载）
// → 事件 + 审计同事务落库。端到端装载链在 internal/gitserver hostkey_test。

import (
	"context"
	"strings"
	"testing"
)

func newHostKeySettingStore(t *testing.T) *Store {
	t.Helper()
	return newSettingsStore(t)
}

func TestGitHostKeyFingerprintReconcile(t *testing.T) {
	st := newHostKeySettingStore(t)
	ctx := context.Background()

	// 无台账装载（首启）：建账、changed=false、零事件零审计。
	changed, err := st.ReconcileGitHostKeyFingerprint(ctx, "SHA256:AAAA")
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if changed {
		t.Fatal("first reconcile must not report changed")
	}
	if v, err := st.LoadGitHostKeyFingerprint(ctx); err != nil || v != "SHA256:AAAA" {
		t.Fatalf("ledger after first reconcile = %q err=%v, want SHA256:AAAA", v, err)
	}
	assertNoHostKeyChangedRecords(t, st, "first boot")

	// 同值装载：幂等零记录。
	if changed, err = st.ReconcileGitHostKeyFingerprint(ctx, "SHA256:AAAA"); err != nil || changed {
		t.Fatalf("same-value reconcile = %v err=%v, want false/nil", changed, err)
	}
	assertNoHostKeyChangedRecords(t, st, "same-value reload")

	// 台账不同（模拟换钥后装载）：changed=true + 事件 + 审计，diff 带新旧。
	changed, err = st.ReconcileGitHostKeyFingerprint(ctx, "SHA256:BBBB")
	if err != nil {
		t.Fatalf("changed reconcile: %v", err)
	}
	if !changed {
		t.Fatal("differing reconcile must report changed")
	}
	events, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	eventHits := 0
	for _, e := range events {
		if e.Name == "git.hostkey_changed" {
			eventHits++
			if e.Subject != "platform:git" {
				t.Fatalf("event subject = %q, want platform:git", e.Subject)
			}
			for _, want := range []string{"SHA256:AAAA", "SHA256:BBBB"} {
				if !strings.Contains(e.Payload, want) {
					t.Fatalf("event payload %s missing %s", e.Payload, want)
				}
			}
		}
	}
	if eventHits != 1 {
		t.Fatalf("git.hostkey_changed event count = %d, want 1", eventHits)
	}
	audits, _, err := st.ListAudits(ctx, AuditQuery{Action: "git.hostkey_changed"})
	if err != nil {
		t.Fatalf("ListAudits: %v", err)
	}
	if len(audits) != 1 {
		t.Fatalf("git.hostkey_changed audit count = %d, want 1", len(audits))
	}
	if audits[0].Actor != "system" {
		t.Fatalf("audit actor = %q, want system", audits[0].Actor)
	}
	for _, want := range []string{"SHA256:AAAA", "SHA256:BBBB"} {
		if !strings.Contains(audits[0].DiffSummary, want) {
			t.Fatalf("audit diff %s missing %s", audits[0].DiffSummary, want)
		}
	}
}

func assertNoHostKeyChangedRecords(t *testing.T, st *Store, scene string) {
	t.Helper()
	ctx := context.Background()
	events, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, e := range events {
		if e.Name == "git.hostkey_changed" {
			t.Fatalf("[%s] unexpected git.hostkey_changed event", scene)
		}
	}
	audits, _, err := st.ListAudits(ctx, AuditQuery{Action: "git.hostkey_changed"})
	if err != nil {
		t.Fatalf("ListAudits: %v", err)
	}
	if len(audits) != 0 {
		t.Fatalf("[%s] unexpected git.hostkey_changed audit rows = %d", scene, len(audits))
	}
}
