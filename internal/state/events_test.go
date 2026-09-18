package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/edgesets/edgefleet/internal/apperr"
	"github.com/edgesets/edgefleet/internal/eventcode"
)

// appendN 在事务外按序追加 n 条事件（独立事务）。
func appendN(t *testing.T, st *Store, n int, at time.Time) []int64 {
	t.Helper()
	seqs := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		var seq int64
		err := st.InTx(context.Background(), func(tx *Tx) error {
			var err error
			seq, err = tx.AppendEvent(context.Background(), Event{
				Name: "node.joined", Subject: "node:x", At: at,
			})
			return err
		})
		if err != nil {
			t.Fatalf("append event: %v", err)
		}
		seqs = append(seqs, seq)
	}
	return seqs
}

// TestEventSeqMonotonicAndNeverReused 验证 seq 单调且清理后不复用
// （state-model §2.9：SSE 游标依赖 seq 永不回退）。
func TestEventSeqMonotonicAndNeverReused(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	seqs := appendN(t, st, 5, time.Now().UTC())
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq not monotonic: %v", seqs)
		}
	}

	// 全部清理后追加：seq 继续前进（AUTOINCREMENT 不复用历史值）。
	if _, err := st.PruneExpiredEvents(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	after := appendN(t, st, 1, time.Now().UTC())
	if after[0] <= seqs[len(seqs)-1] {
		t.Fatalf("seq reused after prune: before max %d, after %d", seqs[len(seqs)-1], after[0])
	}
}

// TestEventsSinceWindows 验证窗口查询：since 过滤、limit 截断、空表语义。
func TestEventsSinceWindows(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 空表：任意游标返回空页、无错误（无从判断断档）。
	evs, err := st.EventsSince(ctx, 0, 10)
	if err != nil || len(evs) != 0 {
		t.Fatalf("empty store: events=%v err=%v, want empty nil-error", evs, err)
	}

	seqs := appendN(t, st, 5, time.Now().UTC())

	// 从头拉全量。
	evs, err = st.EventsSince(ctx, 0, 10)
	if err != nil {
		t.Fatalf("since=0: %v", err)
	}
	if len(evs) != 5 {
		t.Fatalf("since=0 got %d events, want 5", len(evs))
	}
	if evs[0].Seq != seqs[0] || evs[4].Seq != seqs[4] {
		t.Fatalf("events order mismatch: %v vs %v", evs, seqs)
	}

	// since 游标。
	evs, err = st.EventsSince(ctx, seqs[2], 10)
	if err != nil {
		t.Fatalf("since cursor: %v", err)
	}
	if len(evs) != 2 || evs[0].Seq != seqs[3] {
		t.Fatalf("since cursor window wrong: %+v", evs)
	}

	// limit 截断。
	evs, err = st.EventsSince(ctx, 0, 2)
	if err != nil {
		t.Fatalf("limit: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("limit window wrong: %d", len(evs))
	}

	// 游标超前（无新事件）：空页、无错误。
	evs, err = st.EventsSince(ctx, seqs[4], 10)
	if err != nil || len(evs) != 0 {
		t.Fatalf("ahead cursor: events=%v err=%v, want empty nil-error", evs, err)
	}
}

// TestEventsCursorExpired410 验收事件 410：保留窗清理后，早于窗起点
// 的游标查询返回 E_EVENT_CURSOR_EXPIRED（HTTP 410），context 附
// oldest_seq（state-model §2.9 显式断档）。
func TestEventsCursorExpired410(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 前 3 条早于清理线（1 小时前），后 2 条晚于清理线（10 分钟前）——
	// 保留清理按时间裁剪，构造出「窗内 + 窗外」混合。
	cutoff := time.Now().UTC().Add(-30 * time.Minute)
	var seqs []int64
	for _, at := range []time.Time{
		cutoff.Add(-30 * time.Minute), cutoff.Add(-20 * time.Minute), cutoff.Add(-10 * time.Minute),
		cutoff.Add(10 * time.Minute), cutoff.Add(20 * time.Minute),
	} {
		seqs = append(seqs, appendN(t, st, 1, at)...)
	}

	// 清理掉前 3 条（保留窗起点推进到 seqs[3]）。
	n, err := st.PruneExpiredEvents(ctx, cutoff)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 3 {
		t.Fatalf("pruned %d events, want 3", n)
	}

	gotOldest, ok, err := st.OldestSeq(ctx)
	if err != nil || !ok {
		t.Fatalf("oldest seq: %v %v", ok, err)
	}
	if gotOldest != seqs[3] {
		t.Fatalf("oldest = %d, want %d", gotOldest, seqs[3])
	}

	// 游标停在已清理区段（since = seqs[1]）→ 410。
	_, err = st.EventsSince(ctx, seqs[1], 10)
	if err == nil {
		t.Fatal("expired cursor must fail")
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("want *apperr.Error, got %T: %v", err, err)
	}
	if appErr.Code() != "E_EVENT_CURSOR_EXPIRED" {
		t.Fatalf("code = %s, want E_EVENT_CURSOR_EXPIRED", appErr.Code())
	}
	if appErr.HTTPStatus() != 410 {
		t.Fatalf("http status = %d, want 410", appErr.HTTPStatus())
	}
	if got := appErr.Context()["oldest_seq"]; got == "" {
		t.Fatalf("context.oldest_seq missing: %+v", appErr.Context())
	}

	// 游标恰在保留窗起点（since+1 == oldest）→ 不算断档，正常返回。
	evs, err := st.EventsSince(ctx, seqs[3]-1, 10)
	if err != nil {
		t.Fatalf("cursor at window edge must not 410: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("edge window: got %d events, want 2", len(evs))
	}
}

// TestAppendEventRejectsUnregisteredName 验证事件名注册表纪律（与
// errcode fail-fast 同源）：未注册事件名 panic，不允许清单外事件。
func TestAppendEventRejectsUnregisteredName(t *testing.T) {
	if _, ok := eventcode.Get("node.joined"); !ok {
		t.Fatal("eventcode registry must contain node.joined (test premise)")
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("unregistered event name must panic")
		}
	}()
	st := newTestStore(t)
	_ = st.InTx(context.Background(), func(tx *Tx) error {
		_, err := tx.AppendEvent(context.Background(), Event{Name: "not.registered_event"})
		return err
	})
}
