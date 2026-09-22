package notify

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// notify 包测试（E6 W5-S4，observability §5.2）：matcher 精确/前缀/全通配
// + 非法拒绝；签名正负向（篡改即失败）；重试状态机（退避与终态、非 2xx 记
// 码）；游标消费（顺序/间隙跳过/首启不回放）；停机 drain；system 组件判红。

func newTestEnv(t *testing.T) (*Manager, *state.Store, *secrets.Box) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), dir+"/state.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(dir + "/test.key")
	if err != nil {
		t.Fatalf("ensure key: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := Config{
		PollInterval: 10 * time.Millisecond,
		Backoff:      []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond},
		Workers:      2,
	}
	return NewManager(st, box, cfg, logger), st, box
}

func appendEvent(t *testing.T, st *state.Store, name string) int64 {
	t.Helper()
	return appendEventAt(t, st, name, time.Now().UTC())
}

func appendEventAt(t *testing.T, st *state.Store, name string, at time.Time) int64 {
	t.Helper()
	var seq int64
	err := st.InTx(context.Background(), func(tx *state.Tx) error {
		var err error
		seq, err = tx.AppendEvent(context.Background(), state.Event{Name: name, Subject: "app:t", At: at})
		return err
	})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	return seq
}

func createEndpointWithPatterns(t *testing.T, st *state.Store, box *secrets.Box, name, url string, patterns []string) state.WebhookEndpoint {
	t.Helper()
	plaintext := "secret-for-" + name
	cipher, err := box.Encrypt([]byte(plaintext))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	e, err := st.CreateWebhookEndpoint(context.Background(), state.WebhookEndpointWrite{
		Name:              name,
		URL:               url,
		SecretCipher:      string(cipher),
		SecretFingerprint: "0123456789abcdef",
		EventPatterns:     patterns,
		Enabled:           true,
	})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	return e
}

// ── matcher ─────────────────────────────────────────────────────────────

func TestMatcherForms(t *testing.T) {
	cases := []struct {
		pattern string
		match   []string
		miss    []string
	}{
		{"deployment.*",
			[]string{"deployment.queued", "deployment.succeeded", "deployment.a.b"},
			[]string{"deployment", "app.degraded", "xdeployment.a"}},
		{"cron.run_failed",
			[]string{"cron.run_failed"},
			[]string{"cron.run_failedX", "cron.failed", "x.cron.run_failed"}},
		{"*",
			[]string{"deployment.queued", "app.deleted", "logs.ingest_degraded"},
			nil},
		{"*.substrate_missing",
			[]string{"app.substrate_missing"},
			[]string{"app.substrate_missingX"}},
	}
	for _, tc := range cases {
		m, err := NewEndpointMatcher([]string{tc.pattern})
		if err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		for _, hit := range tc.match {
			if !m.Match(hit) {
				t.Errorf("pattern %q must match %q", tc.pattern, hit)
			}
		}
		for _, out := range tc.miss {
			if m.Match(out) {
				t.Errorf("pattern %q must not match %q", tc.pattern, out)
			}
		}
	}
}

func TestMatcherMultiplePatternsAreOR(t *testing.T) {
	m, err := NewEndpointMatcher([]string{"deployment.healthy", "cron.failed"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !m.Match("deployment.healthy") || !m.Match("cron.failed") {
		t.Fatal("OR semantics broken")
	}
	if m.Match("cron.succeeded") {
		t.Fatal("unrelated event must not match")
	}
}

// ── 签名 ────────────────────────────────────────────────────────────────

func TestSignatureRoundTripAndTamper(t *testing.T) {
	secret := []byte("test-secret-material")
	body := []byte(`{"type":"event","seq":1}`)
	ts := int64(1720000000)
	header := SignatureHeaderValue(secret, ts, body)
	if !strings.HasPrefix(header, "sha256=") {
		t.Fatalf("header form: %q", header)
	}
	if !VerifySignature(secret, ts, body, header) {
		t.Fatal("valid signature must verify")
	}
	// 篡改 body → 失败。
	if VerifySignature(secret, ts, []byte(`{"type":"event","seq":2}`), header) {
		t.Fatal("tampered body must fail verification")
	}
	// 篡改 timestamp（重放窗外的签名不可移植）→ 失败。
	if VerifySignature(secret, ts+1, body, header) {
		t.Fatal("tampered timestamp must fail verification")
	}
	// 错误密钥 → 失败。
	if VerifySignature([]byte("other"), ts, body, header) {
		t.Fatal("wrong secret must fail verification")
	}
}

func TestPayloadMarshalsStable(t *testing.T) {
	p := NewEventPayload(7, 1720000000, "app.deleted", "app:01ABC", `{"k":"v"}`)
	b1, err := MarshalPayload(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b2, _ := MarshalPayload(p)
	if string(b1) != string(b2) {
		t.Fatal("payload marshal must be deterministic (signing depends on it)")
	}
	for _, want := range []string{`"type":"event"`, `"seq":7`, `"name":"app.deleted"`, `"payload":{"k":"v"}`} {
		if !strings.Contains(string(b1), want) {
			t.Errorf("body missing %s: %s", want, b1)
		}
	}
	test := NewTestPayload("01ENDPOINT", 1720000000)
	if test.Type != PayloadTypeTest {
		t.Fatalf("test payload type: %q", test.Type)
	}
}

// ── 重试状态机 ──────────────────────────────────────────────────────────

func TestRetryDelaySchedule(t *testing.T) {
	def := Config{}
	if got := def.retryDelayFor(1); got != retryBackoffSchedule[0] {
		t.Fatalf("first retry delay = %v", got)
	}
	if got := def.retryDelayFor(2); got != retryBackoffSchedule[1] {
		t.Fatalf("second retry delay = %v", got)
	}
	// 第三档按表取值（3 次尝试语义下终态先于本档生效——生产不可达；
	// 保留断言钉住退避表字面，设计复核若改「首发 + 3 次重试」语义随
	// MaxDeliveryAttempts 升 4 时本档即真实可达）。
	if got := def.retryDelayFor(3); got != retryBackoffSchedule[2] {
		t.Fatalf("third retry delay = %v", got)
	}
	if got := def.retryDelayFor(4); got != DefaultNextRetryDelay {
		t.Fatalf("beyond the table falls back: %v", got)
	}
	custom := Config{Backoff: []time.Duration{time.Millisecond, 2 * time.Millisecond}}
	if got := custom.retryDelayFor(2); got != 2*time.Millisecond {
		t.Fatalf("injected table: %v", got)
	}
}

// TestDeliverRetryLadderToTerminal：非 2xx（500）与传输失败都按退避表重试，
// MaxDeliveryAttempts 次后终态 failed；每尝试 attempts 恒增、非 2xx 记响应码。
func TestDeliverRetryLadderToTerminal(t *testing.T) {
	m, st, box := newTestEnv(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	appendEvent(t, st, "app.degraded")
	ep := createEndpointWithPatterns(t, st, box, "failing", srv.URL, []string{"app.*"})
	rows, err := st.CreateWebhookDeliveriesAndAdvance(ctx, 1, []string{ep.ID})
	if err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
	d := rows[0]
	for i := 0; i < m.cfg.maxAttempts(); i++ {
		m.attempt(ctx, deliveryJob{deliveryID: d.ID, endpointID: ep.ID})
	}
	got, err := st.GetWebhookDelivery(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != state.WebhookDeliveryFailed {
		t.Fatalf("status = %s, want failed after %d attempts (got %+v)", got.Status, m.cfg.maxAttempts(), got)
	}
	if got.Attempts != m.cfg.maxAttempts() {
		t.Fatalf("attempts = %d, want %d", got.Attempts, m.cfg.maxAttempts())
	}
	if got.ResponseCode == nil || *got.ResponseCode != 500 {
		t.Fatalf("response code must record the 500: %+v", got.ResponseCode)
	}
	if got.NextRetryAt != nil {
		t.Fatal("terminal row carries no retry time")
	}
	if code := int(hits.Load()); code != m.cfg.maxAttempts() {
		t.Fatalf("receiver hits = %d, want %d", hits.Load(), m.cfg.maxAttempts())
	}
	// 终态红灯数据源（system notifications 组件）。
	latest, err := st.LatestWebhookTerminalDeliveries(ctx)
	if err != nil || latest[ep.ID].Status != state.WebhookDeliveryFailed {
		t.Fatalf("terminal ledger must surface: %v", err)
	}
	if err := m.CheckHealth(); err == nil || !strings.Contains(err.Error(), "failing") {
		t.Fatalf("component must be red with failing endpoints: %v", err)
	}
}

// TestDeliverSuccessSignsAndRecords：2xx → ok + 指纹头可被独立复算（真实
// HMAC 重算，非 grep 签名存在——e2e 同口径）。
func TestDeliverSuccessSignsAndRecords(t *testing.T) {
	m, st, box := newTestEnv(t)
	ctx := context.Background()

	type captured struct {
		body      []byte
		ts        string
		signature string
	}
	var got captured
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = readFull(r, body)
		got = captured{
			body:      body,
			ts:        r.Header.Get(HeaderTimestamp),
			signature: r.Header.Get(HeaderSignature),
		}
		w.WriteHeader(http.StatusOK)
		close(ready)
	}))
	t.Cleanup(srv.Close)

	seq := appendEvent(t, st, "deployment.succeeded")
	plaintext := "receiver-secret-material"
	cipher, err := box.Encrypt([]byte(plaintext))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	fp := "deadbeef00000000"
	ep, err := st.CreateWebhookEndpoint(ctx, state.WebhookEndpointWrite{
		Name: "ops", URL: srv.URL, SecretCipher: string(cipher),
		SecretFingerprint: fp, EventPatterns: []string{"deployment.*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, err := st.CreateWebhookDeliveriesAndAdvance(ctx, seq, []string{ep.ID})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	m.attempt(ctx, deliveryJob{deliveryID: rows[0].ID, endpointID: ep.ID})

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver never saw the POST")
	}
	if got.ts == "" || got.signature == "" {
		t.Fatalf("signature headers missing: %+v", got)
	}
	// 独立重算 HMAC（宿主/测试侧不共享实现细节——算法契约面）。
	ts, err := strconv.ParseInt(got.ts, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q: %v", got.ts, err)
	}
	if !VerifySignature([]byte(plaintext), ts, got.body, got.signature) {
		t.Fatalf("HMAC recompute with the plaintext secret must pass: sig=%q body=%s", got.signature, got.body)
	}
	// 载荷携带事件全字段。
	for _, want := range []string{`"type":"event"`, `"name":"deployment.succeeded"`, `"seq":` + strconv.FormatInt(seq, 10)} {
		if !strings.Contains(string(got.body), want) {
			t.Errorf("body missing %s: %s", want, got.body)
		}
	}
	d, err := st.GetWebhookDelivery(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if d.Status != state.WebhookDeliveryOK || d.Attempts != 1 || d.ResponseCode == nil || *d.ResponseCode != 200 {
		t.Fatalf("ok ledger row: %+v", d)
	}
	// 恢复绿：组件从 failed 转 ok 后恒绿。
	if err := m.CheckHealth(); err != nil {
		t.Fatalf("component must be green after success: %v", err)
	}
}

// TestConsumeCursorOrderAndGapSkip：顺序消费（每事件一拍、逐事件推进游标，
// 不匹配事件也推进消费位）。
func TestConsumeCursorOrderAndGapSkip(t *testing.T) {
	m, st, box := newTestEnv(t)
	ctx := context.Background()

	ep := createEndpointWithPatterns(t, st, box, "ops", "http://127.0.0.1:1/hook", []string{"deployment.*"})
	for _, name := range []string{"deployment.queued", "deployment.succeeded", "deployment.failed"} {
		appendEvent(t, st, name)
	}
	// 不匹配事件：消费位照样越过（无端点行落账）。
	appendEvent(t, st, "cron.failed")
	m.consume(ctx)

	rows, err := st.ListWebhookDeliveries(ctx, ep.ID, "", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("deliveries = %d, want 3 (only pattern matches)", len(rows))
	}
	seen := map[int64]bool{}
	for _, r := range rows {
		seen[r.EventSeq] = true
	}
	for seq := int64(1); seq <= 3; seq++ {
		if !seen[seq] {
			t.Fatalf("event seq %d missing from the ledger (rows: %v)", seq, rows)
		}
	}
	cursor, err := st.GetWebhookCursor(ctx)
	if err != nil || cursor != 4 {
		t.Fatalf("cursor = %d (err %v), want 4 (advanced past the non-match)", cursor, err)
	}
}

// TestConsumeGapSkipsPrunedWindow：间隙诚实跳过——游标与现存最旧事件之间
// 的区段被保留期清掉（E_EVENT_CURSOR_EXPIRED）时，消费位对齐当前最大 seq
// 继续；间隙事件零合成台账行、零新事件（warn 日志是唯一披露面）。
func TestConsumeGapSkipsPrunedWindow(t *testing.T) {
	m, st, box := newTestEnv(t)
	ctx := context.Background()

	ep := createEndpointWithPatterns(t, st, box, "ops", "http://127.0.0.1:1/hook", []string{"deployment.*"})
	// seq1（旧）：首拍消费 → 游标=1、台账一行。
	appendEventAt(t, st, "deployment.queued", time.Now().UTC().Add(-10*24*time.Hour))
	m.consume(ctx)
	cursor, err := st.GetWebhookCursor(ctx)
	if err != nil || cursor != 1 {
		t.Fatalf("cursor after first consume = %d (err %v), want 1", cursor, err)
	}
	// seq2（旧，未消费）+ seq3（新鲜，未消费）。
	appendEventAt(t, st, "deployment.succeeded", time.Now().UTC().Add(-10*24*time.Hour))
	appendEvent(t, st, "deployment.failed")
	// 保留期清理：seq1/seq2（10 天前）被删，seq3 存活 → oldest=3。
	if _, err := st.PruneExpiredEvents(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	// 消费：EventsSince(1) 断档（1+1=2 < oldest=3）→ 对齐 maxSeq=3，
	// 间隙事件（seq2）零台账行。
	m.consume(ctx)
	cursor, _ = st.GetWebhookCursor(ctx)
	if cursor != 3 {
		t.Fatalf("cursor after gap = %d, want 3 (aligned to the current max seq)", cursor)
	}
	rows, _ := st.ListWebhookDeliveries(ctx, ep.ID, "", 100)
	if len(rows) != 1 || rows[0].EventSeq != 1 {
		t.Fatalf("gap must not synthesize delivery rows: %+v", rows)
	}
	// 间隙后消费照常恢复：新事件正常落账。
	appendEvent(t, st, "deployment.cancelled")
	m.consume(ctx)
	rows, _ = st.ListWebhookDeliveries(ctx, ep.ID, "", 100)
	if len(rows) != 2 {
		t.Fatalf("delivery must resume after the gap reset: %d rows", len(rows))
	}
}

// TestConsumeFirstRunDoesNotReplayHistory：游标未初始化时首拍对齐当前最大
// seq——订阅从现在开始，历史事件零补投（首启不轰炸接收方）。
func TestConsumeFirstRunDoesNotReplayHistory(t *testing.T) {
	m, st, box := newTestEnv(t)
	appendEvent(t, st, "deployment.queued")
	appendEvent(t, st, "deployment.succeeded")
	ep := createEndpointWithPatterns(t, st, box, "ops", "http://127.0.0.1:1/hook", []string{"deployment.*"})
	if err := m.initializeCursor(context.Background()); err != nil {
		t.Fatalf("init cursor: %v", err)
	}
	m.consume(context.Background())
	rows, _ := st.ListWebhookDeliveries(context.Background(), ep.ID, "", 100)
	if len(rows) != 0 {
		t.Fatalf("history must not be replayed: %d rows", len(rows))
	}
	// 新事件照常消费。
	appendEvent(t, st, "deployment.failed")
	m.consume(context.Background())
	rows, _ = st.ListWebhookDeliveries(context.Background(), ep.ID, "", 100)
	if len(rows) != 1 {
		t.Fatalf("new events must still deliver: %d rows", len(rows))
	}
}

// TestDisabledEndpointSkipsConsumptionAndPostpones：停用端点在消费拍不建行；
// 已建行在停用期间被推迟且不烧尝试预算。
func TestDisabledEndpointSkipsConsumptionAndPostpones(t *testing.T) {
	m, st, box := newTestEnv(t)
	ctx := context.Background()
	ep := createEndpointWithPatterns(t, st, box, "ops", "http://127.0.0.1:1/hook", []string{"deployment.*"})
	disabled := false
	if _, err := st.UpdateWebhookEndpoint(ctx, ep.ID, state.WebhookEndpointUpdate{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	appendEvent(t, st, "deployment.queued")
	m.consume(ctx)
	rows, _ := st.ListWebhookDeliveries(ctx, ep.ID, "", 100)
	if len(rows) != 0 {
		t.Fatalf("disabled endpoint must not consume: %d rows", len(rows))
	}
	// 已存在的 pending 行（停用前建的）：attempt 只推迟不计尝试。
	enabled := true
	if _, err := st.UpdateWebhookEndpoint(ctx, ep.ID, state.WebhookEndpointUpdate{Enabled: &enabled}); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	rows, _ = st.CreateWebhookDeliveriesAndAdvance(ctx, 99, []string{ep.ID})
	disabled = false
	if _, err := st.UpdateWebhookEndpoint(ctx, ep.ID, state.WebhookEndpointUpdate{Enabled: &disabled}); err != nil {
		t.Fatalf("disable again: %v", err)
	}
	m.attempt(ctx, deliveryJob{deliveryID: rows[0].ID, endpointID: ep.ID})
	d, _ := st.GetWebhookDelivery(ctx, rows[0].ID)
	if d.Attempts != 0 || d.Status != state.WebhookDeliveryPending || d.NextRetryAt == nil {
		t.Fatalf("disabled skip must postpone without burning attempts: %+v", d)
	}
}

// TestRunDrainsInFlightOnStop：停机 drain——ctx 取消后 Run 等在途尝试完成
// 簿记收口（台账行终见 attempts=1 的记账）才返回，不把在途写半途丢弃。
// handler 进入即发信号（确定性——worker 确实在途），再取消。
func TestRunDrainsInFlightOnStop(t *testing.T) {
	m, st, box := newTestEnv(t)
	m.cfg.Workers = 1

	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 进入即确认在途；慢一拍返回（给 cancel 留出落在在途窗口内的时间
		// ——cancel 会让 Do 以 canceled 失败，簿记仍须完成）。
		select {
		case <-entered:
		default:
			close(entered)
		}
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	plaintext := "drain-secret"
	cipher, _ := box.Encrypt([]byte(plaintext))
	ep, err := st.CreateWebhookEndpoint(context.Background(), state.WebhookEndpointWrite{
		Name: "drain", URL: srv.URL, SecretCipher: string(cipher),
		SecretFingerprint: "0123456789abcdef", EventPatterns: []string{"*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 游标先于事件追加完成初始化（对齐 0——空事件平台形态）；随后同步
	// 消费一拍：台账行先于 Run 创建（确定性——无轮询竞态），任务入队。
	if initialized, err := st.InitializeWebhookCursorIfEmpty(context.Background(), 0); err != nil || !initialized {
		t.Fatalf("pre-initialize cursor: %v (%v)", err, initialized)
	}
	appendEvent(t, st, "app.recovered")
	m.consume(context.Background())
	rows, err := st.ListWebhookDeliveries(context.Background(), ep.ID, state.WebhookDeliveryPending, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("synchronous consume must create exactly one pending row: %v (n=%d)", err, len(rows))
	}
	d := rows[0]

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = m.Run(runCtx); close(done) }()

	// handler 进入 = worker 在途。此刻取消：在途 HTTP 随父 ctx 中止，
	// 但 Run 必须等簿记（RecordWebhookAttempt）落账后才返回。
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("worker never attempted the delivery")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after ctx cancel (drain hung)")
	}
	got, err := st.GetWebhookDelivery(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// 簿记收口证据：在途尝试已被记账（attempts ≥ 1、updated_at 已推进
	// 到创建之后）——半途进程退出不会留下这种可观察的收尾。
	if got.Attempts < 1 || !got.UpdatedAt.After(got.CreatedAt) {
		t.Fatalf("in-flight attempt must be drained to a recorded state: %+v", got)
	}
}

// TestCheckHealthGreenWhenNothingFailing：无端点/无终败 = 恒绿（无所欠）。
func TestCheckHealthGreenWhenNothingFailing(t *testing.T) {
	m, st, box := newTestEnv(t)
	if err := m.CheckHealth(); err != nil {
		t.Fatalf("empty platform must be green: %v", err)
	}
	createEndpointWithPatterns(t, st, box, "ops", "http://127.0.0.1:1/hook", []string{"*"})
	if err := m.CheckHealth(); err != nil {
		t.Fatalf("endpoint without terminal failure must be green: %v", err)
	}
}

// ── 小工具 ──────────────────────────────────────────────────────────────

func readFull(r *http.Request, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Body.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
