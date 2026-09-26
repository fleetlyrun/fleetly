package logs

// 入湖批量器的 hermetic 单测（E6 观测专项设计 §2.3，W5-S1）：
//   - flush 触发（行数阈值 / wake 信号；2s ticker 路径为同一 flushTick）；
//   - 溢出丢最旧 + 计数；
//   - streak 去抖（进入/退出各发一事件；失败批回灌重试）；
//   - victorialogs 模式下 JSONL 落盘停止、build 行接入（脱敏先于入湖）；
//   - VL 故障时直播面零影响（ring/Follow 不经过批量器——A3 不动条款）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// fakeBackend 是 IngestBackend 假件：可编程成败 + 批次捕获。
type fakeBackend struct {
	mu      sync.Mutex
	batches [][]Entry
	err     error
	calls   int
}

func (f *fakeBackend) IngestBulk(_ context.Context, entries []Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	cp := make([]Entry, len(entries))
	copy(cp, entries)
	f.batches = append(f.batches, cp)
	return nil
}

func (f *fakeBackend) snapshot() ([][]Entry, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]Entry, len(f.batches))
	copy(out, f.batches)
	return out, f.calls
}

func newTestIngester(t *testing.T, st *state.Store, fb *fakeBackend) (*Ingester, *[]capturedEvent) {
	t.Helper()
	ing := NewIngester(fb, st, discardLogger())
	// 事件缝替换为内存捕获（事件名与 payload 形态仍由生产路径钉住）。
	events := &[]capturedEvent{}
	ing.eventFn = func(_ context.Context, name string, payload map[string]string) {
		*events = append(*events, capturedEvent{name: name, payload: payload})
	}
	return ing, events
}

// capturedEvent 是 eventFn 捕获的事件。
type capturedEvent struct {
	name    string
	payload map[string]string
}

func newIngesterHarness(t *testing.T) (*Ingester, *fakeBackend, *[]capturedEvent) {
	t.Helper()
	st := openStateStub(t)
	fb := &fakeBackend{}
	ing, events := newTestIngester(t, st, fb)
	return ing, fb, events
}

// TestIngesterFlushOnLineThreshold 行数阈值触发：第 512 行入队即唤醒
// flush；已脱敏行原样透传（批量器不做二次脱敏——单一管线纪律）。
func TestIngesterFlushOnLineThreshold(t *testing.T) {
	ing, fb, _ := newIngesterHarness(t)
	for i := 0; i < flushLines; i++ {
		ing.Add(Entry{App: "app", Service: "web", At: time.Unix(int64(i), 0).UTC(),
			Line: fmt.Sprintf("redacted-%d", i), Source: SourceContainer})
	}
	// Add 只入队 + 发信号；flush 由循环承载——这里直接驱动一拍（与
	// ticker/wake 路径同函数）。
	ing.flushTick(context.Background())
	batches, calls := fb.snapshot()
	if calls != 1 || len(batches) != 1 || len(batches[0]) != flushLines {
		t.Fatalf("batches=%d calls=%d, want one batch of %d", len(batches), calls, flushLines)
	}
	if batches[0][0].Line != "redacted-0" || batches[0][flushLines-1].Line != fmt.Sprintf("redacted-%d", flushLines-1) {
		t.Fatalf("batch order broken: first=%q last=%q", batches[0][0].Line, batches[0][flushLines-1].Line)
	}
	if ing.Pending() != 0 {
		t.Fatalf("pending = %d after flush, want 0", ing.Pending())
	}
}

// TestIngesterFlushOnWakeAndSkipEmpty wake 信号（阈值未达时由外部/下拍
// 驱动）也能及时 flush；空队列 tick 为 no-op（不发空 bulk）。
func TestIngesterFlushOnWakeAndSkipEmpty(t *testing.T) {
	ing, fb, _ := newIngesterHarness(t)
	ing.Add(Entry{App: "a", Service: "s", Line: "one", Source: SourceContainer})
	ing.flushTick(context.Background())
	batches, calls := fb.snapshot()
	if calls != 1 || len(batches[0]) != 1 {
		t.Fatalf("calls=%d batches=%v, want single-line flush", calls, batches)
	}
	ing.flushTick(context.Background())
	_, calls = fb.snapshot()
	if calls != 1 {
		t.Fatalf("empty tick re-sent: calls=%d, want 1", calls)
	}
}

// TestIngesterOverflowDropsOldest 溢出丢最旧：超过 8192 行后队头被挤出，
// dropped 计数恒等挤出量；flush 只送队列上限内的最新行。
func TestIngesterOverflowDropsOldest(t *testing.T) {
	ing, fb, _ := newIngesterHarness(t)
	total := queueCapacity + 100
	for i := 0; i < total; i++ {
		ing.Add(Entry{App: "app", Service: "web", Line: fmt.Sprintf("line-%d", i), Source: SourceContainer})
	}
	if got := ing.DroppedTotal(); got != 100 {
		t.Fatalf("dropped = %d, want 100", got)
	}
	ing.flushTick(context.Background())
	batches, _ := fb.snapshot()
	if len(batches) != 1 || len(batches[0]) != queueCapacity {
		t.Fatalf("batch size = %d, want %d", len(batches[0]), queueCapacity)
	}
	if batches[0][0].Line != "line-100" {
		t.Fatalf("oldest retained = %q, want line-100 (drop-oldest)", batches[0][0].Line)
	}
	if last := batches[0][queueCapacity-1].Line; last != fmt.Sprintf("line-%d", total-1) {
		t.Fatalf("newest retained = %q, want line-%d", last, total-1)
	}
}

// TestIngesterStreakDebounce streak 去抖：失败进入沿发一次
// logs.ingest_degraded（payload 带丢弃计数）；持续失败不再发；恢复沿发
// 一次 logs.ingest_recovered；失败批回灌队头不丢失。
func TestIngesterStreakDebounce(t *testing.T) {
	ing, fb, events := newIngesterHarness(t)
	boom := errors.New("connection refused")
	fb.mu.Lock()
	fb.err = boom
	fb.mu.Unlock()

	ing.Add(Entry{App: "a", Service: "s", Line: "l1", Source: SourceContainer})
	ing.flushTick(context.Background()) // 进入沿
	ing.flushTick(context.Background()) // 持续失败（不再发事件）
	ing.Add(Entry{App: "a", Service: "s", Line: "l2", Source: SourceContainer})
	ing.flushTick(context.Background()) // 仍失败
	if !ing.Degraded() {
		t.Fatal("streak should be failing")
	}
	degraded := 0
	var degradedPayload map[string]string
	for _, ev := range *events {
		if ev.name == "logs.ingest_degraded" {
			degraded++
			degradedPayload = ev.payload
		}
	}
	if degraded != 1 {
		t.Fatalf("degraded events = %d, want exactly 1 (debounce)", degraded)
	}
	if degradedPayload == nil {
		t.Fatal("degraded payload missing")
	}
	if _, ok := degradedPayload["dropped_total"]; !ok {
		t.Fatalf("degraded payload missing dropped_total: %v", degradedPayload)
	}
	if _, ok := degradedPayload["error"]; !ok {
		t.Fatalf("degraded payload missing error summary: %v", degradedPayload)
	}

	// 恢复：backend 转绿 → 下一拍送出回灌批 + recovered 一次。
	fb.mu.Lock()
	fb.err = nil
	fb.mu.Unlock()
	ing.flushTick(context.Background())
	if ing.Degraded() {
		t.Fatal("streak should have exited")
	}
	batches, _ := fb.snapshot()
	if len(batches) != 1 {
		t.Fatalf("recovery batches = %d, want 1", len(batches))
	}
	lines := map[string]bool{}
	for _, e := range batches[0] {
		lines[e.Line] = true
	}
	if !lines["l1"] || !lines["l2"] {
		t.Fatalf("requeued lines lost: %v", batches[0])
	}
	recovered := 0
	for _, ev := range *events {
		if ev.name == "logs.ingest_recovered" {
			recovered++
		}
	}
	if recovered != 1 {
		t.Fatalf("recovered events = %d, want exactly 1", recovered)
	}
	// 恢复后成功拍不再发事件。
	ing.flushTick(context.Background())
	for _, ev := range *events {
		if ev.name == "logs.ingest_recovered" && recovered > 1 {
			t.Fatal("recovered re-emitted on healthy tick")
		}
	}
}

// TestManagerVLSkipsDiskAndFollowsUnaffected Manager 级端到端（钉住设计
// 两条款）：① victorialogs 模式下 JSONL 落盘停止（History container 来源
// 为空——磁盘不翻倍）；② VL 不可达（批量器降级）时直播面不受影响
//（Follow 照常收行——ring/fan-out 不经过批量器）。
func TestManagerVLSkipsDiskAndFollowsUnaffected(t *testing.T) {
	mg, port, st, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fb := &fakeBackend{}
	fb.mu.Lock()
	fb.err = errors.New("dial 127.0.0.1:9428: connection refused")
	fb.mu.Unlock()
	mg.WithIngestBackend(fb)
	// 缺省设置（未显式保存）= victorialogs 生效——门每拍现读即开。
	if err := st.SaveLogsSettings(ctx, "victorialogs", state.LogsSaveOptions{Actor: "system"}); err != nil {
		t.Fatalf("save backend: %v", err)
	}
	mg.refreshBackendGate(ctx)
	if !mg.vlEnabled() {
		t.Fatal("vl gate should be on with default backend")
	}

	app, _ := testsupport.SeedAppE(t, st, "vlapp")
	port.setApp(app.QualifiedName(), "web")
	base := time.Now().Add(-time.Hour)
	mg.WithClock(func() time.Time { return base })
	port.emit("fleetly-"+app.TeamSlug+"-"+app.ProjectSlug+"-vlapp-web", substrate.LogLine{At: base.Add(time.Millisecond), Line: "live line"})

	// 直播订阅先行（VL 故障下的存活面——本测试的钉子）。订阅键 = 三段限定形。
	ch, stop := mg.Follow(ctx, app.QualifiedName(), "web")
	defer stop()

	mg.scanOnce(ctx)
	select {
	case e := <-ch:
		if e.Line != "live line" {
			t.Fatalf("follower got %q", e.Line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower received nothing while the ingest backend is down")
	}

	// 行进入了批量器（pending 未 flush——backend 不可达），且 JSONL 落盘
	// 停止：History container 来源为空。
	if mg.ing.Pending() != 1 {
		t.Fatalf("pending = %d, want 1 (queued for retry)", mg.ing.Pending())
	}
	// 驱动一拍 flush（Run 循环的 tick/wake 同函数）：失败 → streak 进入。
	mg.ing.flushTick(ctx)
	if mg.ing.Pending() != 1 {
		t.Fatalf("pending after failed flush = %d, want 1 (requeued)", mg.ing.Pending())
	}
	if mg.ing.DroppedTotal() != 0 {
		t.Fatalf("dropped = %d before overflow, want 0", mg.ing.DroppedTotal())
	}
	rows, err := mg.History(ctx, HistoryQuery{App: "vlapp", Source: SourceContainer})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("history rows = %d, want 0 (disk writes skipped in victorialogs mode)", len(rows))
	}
	if !mg.IngestDegraded() {
		t.Fatal("ingest should report degraded while the backend is down")
	}
}

// TestIngestBuildLineRedactedAndGated 构建行接入：过该 app 脱敏值集、
// source=build、只进批量器（ring 不加噪——FollowLogs 零改动）；jsonl 模
// 式（门关）静默 no-op。
func TestIngestBuildLineRedactedAndGated(t *testing.T) {
	mg, _, st, box := newTestManager(t)
	ctx := context.Background()

	fb := &fakeBackend{}
	mg.WithIngestBackend(fb)
	if err := st.SaveLogsSettings(ctx, "victorialogs", state.LogsSaveOptions{Actor: "system"}); err != nil {
		t.Fatalf("save backend: %v", err)
	}
	mg.refreshBackendGate(ctx)

	if _, err := testsupport.SeedAppE(t, st, "builder"); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	appRow, err := st.GetAppByName(ctx, "builder")
	if err != nil {
		t.Fatalf("GetAppByName: %v", err)
	}
	const secret = "build-token-987654"
	ciphertext, err := box.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := st.SetAppEnv(ctx, appRow.ID, "TOKEN", string(ciphertext), "platform", "human"); err != nil {
		t.Fatalf("SetAppEnv: %v", err)
	}

	// 直播面订阅：构建行绝不进 ring（FollowLogs 零改动的结构保证）。
	ch, stop := mg.Follow(ctx, "builder", "web")
	defer stop()

	mg.IngestBuildLine(appRow.ID, "builder", "web", time.Unix(100, 0).UTC(), "pulling build-token-987654")
	mg.ing.flushTick(ctx)

	batches, _ := fb.snapshot()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("batches = %v, want single build row", batches)
	}
	row := batches[0][0]
	if row.Source != SourceBuild || row.App != "builder" || row.Service != "web" {
		t.Fatalf("row = %+v, want source=build app/service preserved", row)
	}
	if strings.Contains(row.Line, secret) {
		t.Fatalf("build line entered the store unredacted: %q", row.Line)
	}
	if !strings.Contains(row.Line, "***") {
		t.Fatalf("build line = %q, want redacted marker", row.Line)
	}
	select {
	case e := <-ch:
		t.Fatalf("build line leaked into the ring: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}

	// 门关（jsonl）→ no-op。
	if err := st.SaveLogsSettings(ctx, "jsonl", state.LogsSaveOptions{Actor: "system"}); err != nil {
		t.Fatalf("save jsonl: %v", err)
	}
	mg.refreshBackendGate(ctx)
	mg.IngestBuildLine(appRow.ID, "builder", "web", time.Unix(101, 0).UTC(), "ignored")
	if _, calls := fb.snapshot(); calls != 1 {
		t.Fatalf("calls after gated build line = %d, want 1 (no-op)", calls)
	}
}
