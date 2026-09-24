package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// W4 通道扩展测试（observability 设计 §8，D-W4-4/D-W4-5，W4-S3）：三通道
// 消息渲染形态、slack 无签名 {"text"} POST、email 真 SMTP 会话（本地假
// sink——net.Listen 250 应答，零第三方）、台账语义（email = success bool +
// detail，response_code 不承载 SMTP 语义）。

// fakeSMTPSink 是测试用迷你 SMTP 服务器：canned 应答（220/250/354/221），
// 会话全文（命令 + DATA 正文）记入 transcript 供断言。零第三方依赖。
type fakeSMTPSink struct {
	ln         net.Listener
	mu         sync.Mutex
	transcript string
	addr       string
}

func newFakeSMTPSink(t *testing.T) *fakeSMTPSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtp sink listen: %v", err)
	}
	s := &fakeSMTPSink{ln: ln, addr: ln.Addr().String()}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTPSink) write(conn net.Conn, line string) {
	_, _ = conn.Write([]byte(line + "\r\n"))
}

func (s *fakeSMTPSink) record(line string) {
	s.mu.Lock()
	s.transcript += line + "\n"
	s.mu.Unlock()
}

func (s *fakeSMTPSink) serve() {
	// 多连接接受循环（探针/投递各自建连——每次会话独立处理）。
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.handle(conn)
	}
}

func (s *fakeSMTPSink) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	s.write(conn, "220 sink.test ESMTP fake")
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	inData := false
	for sc.Scan() {
		line := sc.Text()
		s.record(line)
		upper := strings.ToUpper(line)
		switch {
		case inData:
			if line == "." {
				inData = false
				s.write(conn, "250 OK queued as sink-1")
			}
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			s.write(conn, "250-sink.test")
			s.write(conn, "250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM"):
			s.write(conn, "250 OK")
		case strings.HasPrefix(upper, "RCPT TO"):
			s.write(conn, "250 OK")
		case strings.HasPrefix(upper, "DATA"):
			inData = true
			s.write(conn, "354 end data with <CR><LF>.<CR><LF>")
		case strings.HasPrefix(upper, "QUIT"):
			s.write(conn, "221 bye")
			return
		default:
			s.write(conn, "250 OK")
		}
	}
}

// transcriptSnapshot 返回会话全文（无并发写时读）。
func (s *fakeSMTPSink) transcriptSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript
}

// samplePayload 是渲染断言的固定事件投影。
func samplePayload() Payload {
	return NewEventPayload(42, 1727184000, "deployment.succeeded", "app:web", `{"revision":3,"actor":"alice"}`)
}

func TestRenderChannelForms(t *testing.T) {
	p := samplePayload()

	// slack：单行头 + 详情行（D-W4-5 原文形态）。
	want := "[fleetly] deployment.succeeded — app:web\n" +
		"seq: 42\n" +
		"at: 2024-09-24T13:20:00Z\n" +
		"payload: actor: \"alice\"\nrevision: 3"
	if got := RenderSlackText(p); got != want {
		t.Fatalf("slack text:\n got %q\nwant %q", got, want)
	}

	// email：主题 + 键值正文（纯文本，不做 HTML）。
	if got, want := RenderEmailSubject(p), "[fleetly] deployment.succeeded"; got != want {
		t.Fatalf("email subject: got %q want %q", got, want)
	}
	wantBody := "name: deployment.succeeded\n" +
		"subject: app:web\n" +
		"seq: 42\n" +
		"at: 2024-09-24T13:20:00Z\n" +
		"payload: actor: \"alice\"\nrevision: 3"
	if got := RenderEmailBody(p); got != wantBody {
		t.Fatalf("email body:\n got %q\nwant %q", got, wantBody)
	}

	// 空 payload：详情行收敛（无 payload 行）。
	bare := NewEventPayload(1, 1727184000, "cron.failed", "cron:nightly", "{}")
	if strings.Contains(RenderSlackText(bare), "payload:") {
		t.Fatalf("bare payload must not render a payload line: %q", RenderSlackText(bare))
	}
}

// TestDeliverSlackPostsTextWithoutSignature：slack 通道 POST {"text"} 形态
// 且**无平台签名头**（Slack 端自带鉴权——URL 即凭据，设计 §8.1）。
func TestDeliverSlackPostsTextWithoutSignature(t *testing.T) {
	var mu sync.Mutex
	var gotBody []byte
	var gotSig, gotTS, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = readFull(r, body)
		mu.Lock()
		gotBody, gotSig, gotTS, gotCT = body, r.Header.Get(HeaderSignature), r.Header.Get(HeaderTimestamp), r.Header.Get("Content-Type")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ep := Endpoint{Type: ChannelSlack, URL: srv.URL, Secret: []byte("should-not-be-used")}
	ok, code, errText := deliver(context.Background(), &http.Client{}, ep, samplePayload(), nil, DefaultAttemptTimeout)
	if !ok || code != 200 || errText != "" {
		t.Fatalf("slack delivery: ok=%v code=%d err=%s", ok, code, errText)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotSig != "" || gotTS != "" {
		t.Fatalf("slack deliveries must not carry platform signature headers: sig=%q ts=%q", gotSig, gotTS)
	}
	if gotCT != HeaderContentType {
		t.Fatalf("content type = %q", gotCT)
	}
	// {"text": ...} 单键形态（httpbin 式接收器的断言口径——e2e 同款）。
	var m map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &m); err != nil {
		t.Fatalf("body not a JSON object: %s", gotBody)
	}
	if len(m) != 1 {
		t.Fatalf("slack body must carry exactly one key (text), got %s", gotBody)
	}
	if _, ok := m["text"]; !ok {
		t.Fatalf("slack body missing the text key: %s", gotBody)
	}
	var text string
	if err := json.Unmarshal(m["text"], &text); err != nil {
		t.Fatalf("text not a string: %s", gotBody)
	}
	if !strings.HasPrefix(text, "[fleetly] deployment.succeeded — app:web\n") {
		t.Fatalf("slack text header line missing: %q", text)
	}
}

// TestDeliverEmailSMTPSession：email 通道真 SMTP 会话（本地假 sink）——
// MAIL/RCPT/DATA 全链、信封与头零密码材料。（认证路径不对本 sink 施测：
// sink 不宣告 STARTTLS，net/smtp PlainAuth 会诚实地拒绝明文送凭据——该
// 拒绝本身就是安全语义，单独的负向断言见 TestSendTestEmailAuthRefused。）
func TestDeliverEmailSMTPSession(t *testing.T) {
	sink := newFakeSMTPSink(t)
	cfg := SmtpConfig{
		Host: "127.0.0.1", Port: sinkPort(t, sink),
		From: "fleetly@example.test",
	}
	ep := Endpoint{Type: ChannelEmail, Target: "ops@example.test"}
	ok, code, errText := deliver(context.Background(), &http.Client{}, ep, samplePayload(), &cfg, DefaultAttemptTimeout)
	if !ok || errText != "" {
		t.Fatalf("email delivery failed: ok=%v err=%s", ok, errText)
	}
	_ = code // 台账面不消费（success bool + detail 语义——见 Manager 级测试）
	tr := sink.transcriptSnapshot()
	for _, want := range []string{
		"MAIL FROM:<fleetly@example.test>",
		"RCPT TO:<ops@example.test>",
		"Subject: [fleetly] deployment.succeeded",
		"name: deployment.succeeded",
		"revision: 3",
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("smtp transcript missing %q:\n%s", want, tr)
		}
	}
}

// TestSendTestEmailAuthRefusedOnPlaintext：sink 不宣告 STARTTLS 时，配置
// 了用户名的会话必须被 net/smtp 诚实拒绝（明文链路不送凭据——不静默降级）。
func TestSendTestEmailAuthRefusedOnPlaintext(t *testing.T) {
	sink := newFakeSMTPSink(t)
	cfg := SmtpConfig{
		Host: "127.0.0.1", Port: sinkPort(t, sink),
		Username: "fleetly", Password: "pw", From: "fleetly@example.test",
	}
	ok, _, errText := SendTestEmail(context.Background(), cfg, "probe@example.test")
	if ok {
		t.Fatal("plaintext auth delivery must not succeed against a non-TLS relay")
	}
	if !strings.Contains(errText, "smtp auth failed") {
		t.Fatalf("error must name the auth step (honest detail): %q", errText)
	}
}

// TestManagerEmailDeliveryLedger：Manager 级 email 投递落台账——ok 行
// response_code 保持 NULL（success bool + detail 语义，既有列兼容），last_error 空。
func TestManagerEmailDeliveryLedger(t *testing.T) {
	m, st, _ := newTestEnv(t)
	ctx := context.Background()
	sink := newFakeSMTPSink(t)

	if err := st.SaveSmtpSettings(ctx, state.SmtpSettings{
		Host: "127.0.0.1", Port: sinkPort(t, sink),
		From: "fleetly@example.test",
	}, state.SmtpSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save smtp settings: %v", err)
	}
	seq := appendEvent(t, st, "cron.failed")
	ep, err := st.CreateWebhookEndpoint(ctx, state.WebhookEndpointWrite{
		Name: "mail", Type: state.WebhookChannelEmail, Target: "ops@example.test",
		SecretCipher: "unused", SecretFingerprint: "0123456789abcdef",
		EventPatterns: []string{"cron.*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create email endpoint: %v", err)
	}
	rows, err := st.CreateWebhookDeliveriesAndAdvance(ctx, seq, []string{ep.ID})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	m.attempt(ctx, deliveryJob{deliveryID: rows[0].ID, endpointID: ep.ID})

	d, err := st.GetWebhookDelivery(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if d.Status != state.WebhookDeliveryOK || d.LastError != "" {
		t.Fatalf("email ok row: %+v", d)
	}
	if d.ResponseCode != nil {
		t.Fatalf("email ok row must not carry a response code (SMTP semantics stay out of the column): %+v", d.ResponseCode)
	}
	// sink 真收到了（RCPT 是端点 target）。
	if tr := sink.transcriptSnapshot(); !strings.Contains(tr, "RCPT TO:<ops@example.test>") {
		t.Fatalf("sink transcript missing the endpoint target:\n%s", tr)
	}
}

// TestManagerEmailWithoutSettingsFailsHonestly：email 端点但 SMTP 设置未
// 配置 → 按失败计尝试（detail 进 last_error），不 panic 不静默。
func TestManagerEmailWithoutSettingsFailsHonestly(t *testing.T) {
	m, st, _ := newTestEnv(t)
	ctx := context.Background()
	seq := appendEvent(t, st, "cron.failed")
	ep, err := st.CreateWebhookEndpoint(ctx, state.WebhookEndpointWrite{
		Name: "mail", Type: state.WebhookChannelEmail, Target: "ops@example.test",
		SecretCipher: "unused", SecretFingerprint: "0123456789abcdef",
		EventPatterns: []string{"cron.*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, err := st.CreateWebhookDeliveriesAndAdvance(ctx, seq, []string{ep.ID})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	m.attempt(ctx, deliveryJob{deliveryID: rows[0].ID, endpointID: ep.ID})
	d, err := st.GetWebhookDelivery(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if d.Attempts != 1 || d.LastError == "" || !strings.Contains(d.LastError, "smtp settings") {
		t.Fatalf("failed attempt must record the honest detail: %+v", d)
	}
}

// TestSendTestEmailProbe：TestSmtp 探针执行体——主题 [fleetly] test、
// 收件 = 指定 to、DATA 应答码上报（探针面口径）。
func TestSendTestEmailProbe(t *testing.T) {
	sink := newFakeSMTPSink(t)
	cfg := SmtpConfig{Host: "127.0.0.1", Port: sinkPort(t, sink), From: "fleetly@example.test"}
	ok, code, errText := SendTestEmail(context.Background(), cfg, "probe@example.test")
	if !ok || code != 250 || errText != "" {
		t.Fatalf("probe: ok=%v code=%d err=%s", ok, code, errText)
	}
	tr := sink.transcriptSnapshot()
	for _, want := range []string{"RCPT TO:<probe@example.test>", "Subject: [fleetly] test"} {
		if !strings.Contains(tr, want) {
			t.Errorf("probe transcript missing %q:\n%s", want, tr)
		}
	}
}

// sinkPort 取 sink 的数值端口（DialContext JoinHostPort 消费形态）。
func sinkPort(t *testing.T, s *fakeSMTPSink) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatalf("sink addr %q: %v", s.addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("sink port %q: %v", portStr, err)
	}
	return port
}
