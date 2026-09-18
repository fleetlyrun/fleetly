package state

import (
	"context"
	"testing"
	"time"
)

// TestJanitorPrunesExpired 保留期清理：事件与审计按各自保留期清理，
// 窗口内记录不受影响；被清理区段的游标查询显式 410（断档契约联动）。
func TestJanitorPrunesExpired(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 旧事件（40 天前）+ 新事件（1 天前）；旧审计（400 天前）+ 新审计（10 天前）。
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-24 * time.Hour)
	for _, at := range []time.Time{old, old.Add(time.Second), recent} {
		if err := st.InTx(ctx, func(tx *Tx) error {
			_, err := tx.AppendEvent(ctx, Event{Name: "node.up", At: at})
			return err
		}); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	for _, at := range []time.Time{now.Add(-400 * 24 * time.Hour), now.Add(-10 * 24 * time.Hour)} {
		if err := st.InTx(ctx, func(tx *Tx) error {
			return tx.WriteAudit(ctx, AuditEntry{Actor: "system", Action: "app.create", Result: "ok", At: at})
		}); err != nil {
			t.Fatalf("write audit: %v", err)
		}
	}

	jr := NewJanitor(st, DefaultEventRetentionDays, DefaultAuditRetentionDays, testLogger())
	eventsPruned, auditsPruned, err := jr.PruneOnce(ctx, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if eventsPruned != 2 {
		t.Fatalf("events pruned = %d, want 2 (40 天前的两条)", eventsPruned)
	}
	if auditsPruned != 1 {
		t.Fatalf("audits pruned = %d, want 1 (400 天前的一条)", auditsPruned)
	}

	// 窗口内记录保持：从头游标因断档 410（见下方断言），从清理边界后
	// 的游标（since=2）查询只剩 1 条。
	gotOldest, ok, err := st.OldestSeq(ctx)
	if err != nil || !ok {
		t.Fatalf("oldest seq after prune: %v %v", ok, err)
	}
	evs, err := st.EventsSince(ctx, gotOldest-1, 10)
	if err != nil {
		t.Fatalf("events after prune: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("remaining events = %d, want 1", len(evs))
	}
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("audits after prune: %v", err)
	}
	if len(audits) != 1 {
		t.Fatalf("remaining audits = %d, want 1", len(audits))
	}

	// 清理制造了断档：从头游标显式 410（不静默跳号）。
	if _, err := st.EventsSince(ctx, 0, 10); err == nil {
		t.Fatal("cursor 0 after head prune must return E_EVENT_CURSOR_EXPIRED")
	}
}

// TestJanitorRetentionDefaults 非正值保留期回落默认（不允许配置关闭清理）。
func TestJanitorRetentionDefaults(t *testing.T) {
	jr := NewJanitor(nil, 0, -5, testLogger())
	if jr.EventRetention() != DefaultEventRetentionDays*24*time.Hour {
		t.Fatalf("event retention = %v, want default %d days", jr.EventRetention(), DefaultEventRetentionDays)
	}
	if jr.AuditRetention() != DefaultAuditRetentionDays*24*time.Hour {
		t.Fatalf("audit retention = %v, want default %d days", jr.AuditRetention(), DefaultAuditRetentionDays)
	}
	jr2 := NewJanitor(nil, 7, 90, testLogger())
	if jr2.EventRetention() != 7*24*time.Hour || jr2.AuditRetention() != 90*24*time.Hour {
		t.Fatalf("configured retentions wrong: %v / %v", jr2.EventRetention(), jr2.AuditRetention())
	}
}

// TestJanitorServiceLoop Start/Stop 生命周期：启动先清一拍，Stop 退出。
func TestJanitorServiceLoop(t *testing.T) {
	st := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 先放一条已过期事件。
	if err := st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.AppendEvent(ctx, Event{Name: "node.up", At: time.Now().UTC().Add(-48 * time.Hour)})
		return err
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	jr := NewJanitor(st, 1, DefaultAuditRetentionDays, testLogger()) // 1 天保留
	if err := jr.Start(ctx); err != nil {
		t.Fatalf("start janitor: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := st.EventsSince(ctx, 0, 10)
		if len(evs) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	evs, err := st.EventsSince(ctx, 0, 10)
	if err != nil {
		t.Fatalf("query after janitor: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("janitor must prune expired events on start tick, got %d", len(evs))
	}
	if err := jr.Stop(ctx); err != nil {
		t.Fatalf("stop janitor: %v", err)
	}
}
