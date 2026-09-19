package gitserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// webhook 入口测试（T2.19 验收断言面；D1 后契约更新）：验签正/负、
// delivery ID TTL 防重放、自定义时间戳窗、(app, sha) 幂等去重（部署计数
// 不变）、分支过滤、未配置即未启用（404）、provider 白名单。
// D1（S17 类 D）契约：受理路径回 202 {status:"accepted"}——拉源+入队在
// 后台 worker 执行，部署行经 waitIdle 同步点后可见；worker 失败披露走
// 事件 app.webhook_fetch_failed + 撤坑。

// startWebhookWorker 是测试侧 worker 生命周期（D1）：受理后的异步执行面
// 随测试启动；清理时取消 ctx 并限时等待排空（Cleanup LIFO——先于
// newTestSource 注册的 store 关闭执行）。
func startWebhookWorker(t *testing.T, src *GitTriggers) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	src.StartWebhookWorker(ctx)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := src.StopWebhookWorker(stopCtx); err != nil {
			t.Logf("webhook worker stop: %v", err)
		}
	})
}

// webhookEventSeen 断言事件流中已出现指定事件（D1 失败披露断言面）。
func webhookEventSeen(t *testing.T, st *state.Store, name string) bool {
	t.Helper()
	events, err := st.EventsSince(context.Background(), 0, 200)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, ev := range events {
		if ev.Name == name {
			return true
		}
	}
	return false
}

// sign 计算投递签名（X-Hub-Signature-256 同形）。
func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// signTimestamped 计算携带 X-Fleetly-Timestamp 投递的签名（E7①：材料
// = ts+"."+body——时间戳参与签名，剥离/替换即破坏签名）。
func signTimestamped(secret []byte, ts string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// pushBody 构造 push 投递体。
func pushBody(ref, after string) []byte {
	b, _ := json.Marshal(pushPayload{Ref: ref, After: after})
	return b
}

// setAppWebhookSecret 以密文形态播种 webhook secret（api 写面的测试等价
// 形态——加密在测试内完成，语义一致：库中只落密文）。
func setAppWebhookSecret(t *testing.T, st *state.Store, box *secrets.Box, appID, secret string) {
	t.Helper()
	cipher, err := box.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	if err := st.SetAppWebhookSecret(context.Background(), appID, string(cipher), ""); err != nil {
		t.Fatalf("SetAppWebhookSecret: %v", err)
	}
}

// setAppSource 播种拉源配置（auth none；url 为本地仓库路径）。
func setAppSource(t *testing.T, st *state.Store, appID, url, branch string) {
	t.Helper()
	if err := st.SetAppSource(context.Background(), appID, state.AppSourceWrite{
		URL: url, Branch: branch, AuthKind: state.SourceAuthNone,
	}); err != nil {
		t.Fatalf("SetAppSource: %v", err)
	}
}

// ── 验签 ────────────────────────────────────────────────────────────────────

func TestVerifySignature(t *testing.T) {
	secret := []byte("a-secret-at-least-16ch")
	body := []byte(`{"x":1}`)
	if !verifySignature(sign(secret, body), secret, body) {
		t.Fatal("valid signature rejected")
	}
	if verifySignature(sign([]byte("another-secret-16cha"), body), secret, body) {
		t.Fatal("wrong secret accepted")
	}
	if verifySignature("sha256=deadbeef", secret, body) {
		t.Fatal("garbage digest accepted")
	}
	if verifySignature("md5=abc", secret, body) {
		t.Fatal("non-sha256 scheme accepted")
	}
	if verifySignature("", secret, body) {
		t.Fatal("missing header accepted")
	}
	// hex 大写形态等价（前缀词法固定小写，只归一摘要位——与 GitHub 原文
	// 行为一致：签名头恒 "sha256=<hex>"）。
	sig := sign(secret, body)
	up := "sha256=" + strings.ToUpper(strings.TrimPrefix(sig, "sha256="))
	if !verifySignature(up, secret, body) {
		t.Fatal("uppercase hex rejected")
	}
}

// ── 时间戳窗（自定义投递方）─────────────────────────────────────────────────

func TestTimestampInWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if !timestampInWindow("1800000000", now) {
		t.Fatal("current timestamp rejected")
	}
	if timestampInWindow("1799999400", now) { // 10 分钟前
		t.Fatal("stale timestamp accepted")
	}
	if !timestampInWindow("1799999700", now) { // 5 分钟前（边界内）
		t.Fatal("boundary timestamp rejected")
	}
	if timestampInWindow("not-a-number", now) {
		t.Fatal("garbage timestamp accepted")
	}
}

// ── delivery ID TTL 缓存 ────────────────────────────────────────────────────

func TestDeliveryCacheTTL(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cur := now
	c := newDeliveryCache(time.Minute, func() time.Time { return cur })
	// Claim 原子查+占：首占成功、重占被拒。
	if !c.Claim("d1") {
		t.Fatal("first claim rejected")
	}
	if c.Claim("d1") {
		t.Fatal("duplicate claim allowed")
	}
	if !c.Seen("d1") {
		t.Fatal("claimed id not seen")
	}
	// Unmark 撤坑（5xx 失败路径）：撤坑后同 id 可重新占坑。
	c.Unmark("d1")
	if c.Seen("d1") {
		t.Fatal("unmarked id still seen")
	}
	if !c.Claim("d1") {
		t.Fatal("claim after unmark rejected")
	}
	// TTL 过期：条目自然失效，可重新占坑。
	cur = now.Add(2 * time.Minute)
	if c.Seen("d1") {
		t.Fatal("expired delivery reported seen")
	}
	if !c.Claim("d1") {
		t.Fatal("claim after expiry rejected")
	}
}

// TestDeliveryCacheCapacityBound 容量上限（R1 纵深②）：seen 集体量以
// capacity 为硬界，超限占坑淘汰最旧条目——有界内存，不随攻击流量增长。
func TestDeliveryCacheCapacityBound(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tick := now
	c := newDeliveryCache(time.Hour, func() time.Time { return tick })
	c.capacity = 3
	for _, id := range []string{"a", "b", "c"} {
		if !c.Claim(id) {
			t.Fatalf("claim %s rejected", id)
		}
		tick = tick.Add(time.Second) // 占坑时刻递增，淘汰序 = 占坑序
	}
	// 超限占坑仍成功（淘汰最旧 a 让位）。
	if !c.Claim("d") {
		t.Fatal("over-capacity claim rejected (must evict oldest instead)")
	}
	if c.Seen("a") {
		t.Error("oldest entry a not evicted")
	}
	for _, id := range []string{"b", "c", "d"} {
		if !c.Seen(id) {
			t.Errorf("entry %s missing after eviction", id)
		}
	}
	// 界面持续成立：继续占坑不超 capacity。
	c.Claim("e") // 淘汰 b
	for _, id := range []string{"a", "b"} {
		if c.Seen(id) {
			t.Errorf("evicted entry %s reported seen", id)
		}
	}
	for _, id := range []string{"c", "d", "e"} {
		if !c.Seen(id) {
			t.Errorf("entry %s missing after second eviction", id)
		}
	}
}

// TestValidDeliveryID 词形守卫（R1 纵深①）：长度上界与控制字符。
func TestValidDeliveryID(t *testing.T) {
	if !validDeliveryID("0123456789abcdef-uuid-like") {
		t.Fatal("normal id rejected")
	}
	long := strings.Repeat("a", maxDeliveryIDLen)
	if !validDeliveryID(long) {
		t.Fatal("id at length bound rejected")
	}
	if validDeliveryID(long + "x") {
		t.Fatal("oversized id accepted")
	}
	if validDeliveryID("bad\nid") || validDeliveryID("bad\tid") || validDeliveryID("bad\x00id") || validDeliveryID("bad\x7fid") {
		t.Fatal("control-char id accepted")
	}
}

// ── E7①/②（S19）：时间戳参与签名 + 方法守卫 ────────────────────────────────

// TestWebhookTimestampBindsSignature E7①：携带 X-Fleetly-Timestamp 的投递
// 签名材料必须是 ts+"."+body（时间戳参与签名——剥离后重签 body 不可通过）
// 且时间窗强制；无该头（GitHub/Gitea 官方形态）退回 body-only 验签。
// 防线边界（如实）：provider 白名单只有 github|gitea 官方路径——官方投递
// 不携带该头时时间窗防线不存在（见 webhook.go 文件头注释 2）。
func TestWebhookTimestampBindsSignature(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	// 分支不跟踪的载荷 → 200 ignored（同步终局；不启动 worker、不配 source，
	// 验签面隔离观测）。
	body := pushBody("refs/heads/other", "1111111111111111111111111111111111111111")
	freshTS := fmt.Sprintf("%d", time.Now().Unix())
	post := func(sig, ts, delivery string) int {
		headers := map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": sig,
			"X-GitHub-Delivery":   delivery,
		}
		if ts != "" {
			headers["X-Fleetly-Timestamp"] = ts
		}
		resp, _ := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", headers, body)
		return resp.StatusCode
	}

	// ①携带时间戳 + 签名覆盖 ts+body → 通过（ignored 终局）。
	if got := post(signTimestamped([]byte(secret), freshTS, body), freshTS, "ts-ok"); got != 200 {
		t.Fatalf("timestamped signature status = %d, want 200", got)
	}
	// ②携带时间戳但签名只覆盖 body → 401（时间戳必须参与签名——不可剥离）。
	if got := post(sign([]byte(secret), body), freshTS, "ts-stripped"); got != 401 {
		t.Fatalf("body-only signature with timestamp header status = %d, want 401", got)
	}
	// ③时间戳被替换（签名用的是另一个 ts）→ 401。
	otherTS := fmt.Sprintf("%d", time.Now().Add(-time.Minute).Unix())
	if got := post(signTimestamped([]byte(secret), otherTS, body), freshTS, "ts-swapped"); got != 401 {
		t.Fatalf("swapped timestamp status = %d, want 401", got)
	}
	// ④窗口外的 ts（签名正确覆盖 ts+body）→ 401（时间窗强制）。
	staleTS := "1000000000"
	if got := post(signTimestamped([]byte(secret), staleTS, body), staleTS, "ts-stale"); got != 401 {
		t.Fatalf("stale timestamp status = %d, want 401", got)
	}
	// ⑤无时间戳头（官方形态）→ body-only 签名照常通过（既有契约不变）。
	if got := post(sign([]byte(secret), body), "", "no-ts"); got != 200 {
		t.Fatalf("official body-only signature status = %d, want 200", got)
	}
}

// TestWebhookMethodNotAllowed E7②：webhook 面只收 POST——路径匹配后
// GET/OPTIONS → 405 + Allow 头（未知 provider 仍 404，豁免面不放宽）。
func TestWebhookMethodNotAllowed(t *testing.T) {
	src, _, _, _ := newTestSource(t, time.Minute)
	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	for _, method := range []string{http.MethodGet, http.MethodOptions, http.MethodPut} {
		req, err := http.NewRequest(method, srv.URL+"/v1/apps/my-api/webhooks/github", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405 (%s)", method, resp.StatusCode, raw)
		}
		if resp.Header.Get("Allow") != http.MethodPost {
			t.Fatalf("%s Allow = %q, want POST", method, resp.Header.Get("Allow"))
		}
	}
	// 未知 provider 仍 404（方法守卫不放宽豁免面）。
	resp, err := http.Get(srv.URL + "/v1/apps/my-api/webhooks/evil")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d, want 404", resp.StatusCode)
	}
}

// ── HTTP 全链（验签/防重放/去重/分支过滤/未配置语义）────────────────────────

func TestWebhookHandlerFullChain(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	startWebhookWorker(t, src)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"

	// 源仓库（file 路径形态的 remote）与 app 播种。
	sourceDir, sha := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	post := func(headers map[string]string, body []byte) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/apps/my-api/webhooks/github", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, raw
	}

	hdr := func(delivery string, body []byte) map[string]string {
		return map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": sign([]byte(secret), body),
			"X-GitHub-Delivery":   delivery,
		}
	}

	// 1. 未配置 secret 的 app → 404（未配置即未启用）。
	mainBody := pushBody("refs/heads/main", sha)
	if resp, _ := postRaw(t, srv, "/v1/apps/no-such-app/webhooks/github", hdr("d0", mainBody), mainBody); resp.StatusCode != 404 {
		t.Fatalf("unknown app status = %d, want 404", resp.StatusCode)
	}

	// 2. 错签名 → 401 + 退化信封（code 空）。
	resp, raw := post(map[string]string{"Content-Type": "application/json", "X-GitHub-Delivery": "d-badsig"}, mainBody)
	if resp.StatusCode != 401 {
		t.Fatalf("bad signature status = %d, want 401 (%s)", resp.StatusCode, raw)
	}
	var env map[string]any
	_ = json.Unmarshal(raw, &env)
	if c, _ := env["code"].(string); c != "" {
		t.Fatalf("bad signature envelope code = %q, want empty (FZ-2)", c)
	}

	// 3. 正确签名 → 202 受理（D1：拉源+入队在 worker；真实拉源 = fetch
	//    file 路径 remote）。waitIdle 后部署行落库可见。
	resp, raw = post(hdr("d1", mainBody), mainBody)
	if resp.StatusCode != 202 {
		t.Fatalf("valid signature status = %d (%s)", resp.StatusCode, raw)
	}
	var rec webhookReceipt
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Status != "accepted" {
		t.Fatalf("receipt = %s err=%v", raw, err)
	}
	src.hooks.waitIdle()
	if n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha); err != nil || n != 1 {
		t.Fatalf("deployments for sha after worker = %d err=%v, want 1", n, err)
	}

	// 4. TTL 内重放同 delivery ID → 409 且部署计数不变。
	before, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw = post(hdr("d1", mainBody), mainBody)
	if resp.StatusCode != 409 {
		t.Fatalf("replay status = %d, want 409 (%s)", resp.StatusCode, raw)
	}
	after, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || before != after {
		t.Fatalf("replay changed deployment count: %d -> %d (err=%v)", before, after, err)
	}

	// 5. 同 sha 重投（新 delivery ID）→ duplicate 回执、不建新部署。
	resp, raw = post(hdr("d2", mainBody), mainBody)
	if resp.StatusCode != 200 {
		t.Fatalf("duplicate status = %d (%s)", resp.StatusCode, raw)
	}
	rec = webhookReceipt{}
	_ = json.Unmarshal(raw, &rec)
	if rec.Status != "duplicate" {
		t.Fatalf("same-sha redelivery status = %q, want duplicate", rec.Status)
	}
	after, err = st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || after != before {
		t.Fatalf("duplicate redelivery changed count: %d -> %d (err=%v)", before, after, err)
	}

	// 6. 分支过滤：非配置分支只收不发（200 ignored）。
	sourceDir2, sha2 := newSourceRepo(t, composeFixture) // 独立 sha
	_ = sourceDir2
	featBody := pushBody("refs/heads/feature-x", sha2)
	resp, raw = post(hdr("d3", featBody), featBody)
	if resp.StatusCode != 200 {
		t.Fatalf("other-branch status = %d (%s)", resp.StatusCode, raw)
	}
	rec = webhookReceipt{}
	_ = json.Unmarshal(raw, &rec)
	if rec.Status != "ignored" {
		t.Fatalf("other-branch receipt = %s", raw)
	}

	// 7. 未知 provider → 404（豁免面不放宽）。
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/apps/my-api/webhooks/evil", strings.NewReader("{}"))
	resp2, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("unknown provider status = %d, want 404", resp2.StatusCode)
	}
}

// postRaw 是不封装的 POST（未知 app 路径用）。
func postRaw(t *testing.T, srv *httptest.Server, path string, headers map[string]string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, raw
}

// ── 失败-重投链（对抗审查整改①的验收链路；D1 后契约迁移到 worker 侧）───
//
// 真实链路断言：拉源瞬态失败 → 受理 202（失败在 worker 披露：事件
// app.webhook_fetch_failed + 审计 + 撤坑）→ 官方重投（同一 delivery ID）
// 不被 409 挡（撤坑语义）且拉源恢复 → 第二次成功入队；成功后同 ID 重放
// 仍 409（既有防重放语义不回退）。
func TestWebhookFetchFailureDoesNotPoisonReplayCache(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	startWebhookWorker(t, src)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"

	sourceDir, sha := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	// 第一投：源 URL 指向不存在的仓库路径 → git fetch 失败（瞬态形态）。
	setAppSource(t, st, appRow.ID, fileURL(sourceDir)+"/does-not-exist", "main")

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	body := pushBody("refs/heads/main", sha)
	hdr := func(delivery string) map[string]string {
		return map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": sign([]byte(secret), body),
			"X-GitHub-Delivery":   delivery,
		}
	}

	// 1. 拉源失败：受理期仍 202（D1）；worker 落 fetch_failed 事件并撤坑。
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", hdr("d-redelivery"), body)
	if resp.StatusCode != 202 {
		t.Fatalf("fetch-failure accept status = %d, want 202 (%s)", resp.StatusCode, raw)
	}
	src.hooks.waitIdle()
	if !webhookEventSeen(t, st, "app.webhook_fetch_failed") {
		t.Fatal("app.webhook_fetch_failed event missing after worker fetch failure")
	}
	// 2. 同一 delivery ID 立即重投（拉源仍坏）→ 受理可达（202 而非 409）
	//    ——失败投递已撤坑，官方重投链保持通。
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", hdr("d-redelivery"), body)
	if resp.StatusCode != 202 {
		t.Fatalf("immediate redelivery status = %d, want 202 not 409 (%s)", resp.StatusCode, raw)
	}
	src.hooks.waitIdle()
	// 3. 拉源恢复（源 URL 指回真实仓库）→ 同一 delivery ID 重投成功入队。
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", hdr("d-redelivery"), body)
	if resp.StatusCode != 202 {
		t.Fatalf("recovered redelivery status = %d, want 202 (%s)", resp.StatusCode, raw)
	}
	var rec webhookReceipt
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Status != "accepted" {
		t.Fatalf("recovered redelivery receipt = %s err=%v", raw, err)
	}
	src.hooks.waitIdle()
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments for sha = %d err=%v, want exactly 1", n, err)
	}
	// 4. 成功（终局占坑）后同 ID 重放 → 409（防重放语义不回退）。
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", hdr("d-redelivery"), body)
	if resp.StatusCode != 409 {
		t.Fatalf("post-success replay status = %d, want 409 (%s)", resp.StatusCode, raw)
	}
}

// 401 形态不占坑（二轮 R1）：错签/时间窗外的重投路径绝不触碰缓存——拒绝
// 一个错签重试与查缓存开销相当，占坑只产生公网无凭据可填的无界 map。
// 断言：同 delivery ID 错签 401 后换正确签名重投 → 正常入队（非 409）。
func TestWebhookBadSignatureDoesNotPoisonCache(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	startWebhookWorker(t, src)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"
	sourceDir, sha := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	body := pushBody("refs/heads/main", sha)
	badHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": "sha256=deadbeef",
		"X-GitHub-Delivery":   "d-bruteforce",
	}
	resp, _ := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", badHeaders, body)
	if resp.StatusCode != 401 {
		t.Fatalf("bad signature status = %d, want 401", resp.StatusCode)
	}
	// 同 delivery ID 换正确签名重投：未被 401 占坑 → 正常受理（D1 后为
	// 202；部署行经 worker 落库）。
	goodHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-bruteforce",
	}
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", goodHeaders, body)
	if resp.StatusCode != 202 {
		t.Fatalf("same-id after bad signature status = %d, want 202 (401 must not poison cache: %s)", resp.StatusCode, raw)
	}
	src.hooks.waitIdle()
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments for sha = %d err=%v, want 1", n, err)
	}

	// 时间窗外 401 同样不占坑：过期 X-Fleetly-Timestamp（E7①：签名材料
	// = ts+"."+body）被拒后，同 ID 带合法时间戳头可正常处理（同 sha 已
	// 入队 → duplicate 终局）。
	staleTS := "1000000000" // 远在窗口外
	staleHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": signTimestamped([]byte(secret), staleTS, body),
		"X-GitHub-Delivery":   "d-stale-ts",
		"X-Fleetly-Timestamp": staleTS,
	}
	resp, _ = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", staleHeaders, body)
	if resp.StatusCode != 401 {
		t.Fatalf("stale timestamp status = %d, want 401", resp.StatusCode)
	}
	freshTS := fmt.Sprintf("%d", time.Now().Unix())
	freshHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": signTimestamped([]byte(secret), freshTS, body),
		"X-GitHub-Delivery":   "d-stale-ts",
		"X-Fleetly-Timestamp": freshTS,
	}
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", freshHeaders, body)
	if resp.StatusCode != 200 {
		t.Fatalf("same-id after stale timestamp status = %d, want 200 (401 must not poison cache: %s)", resp.StatusCode, raw)
	}
	var rec webhookReceipt
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Status != "duplicate" {
		t.Fatalf("receipt = %s err=%v (same sha already enqueued → duplicate)", raw, err)
	}
}

// TestWebhookOversizedDeliveryIDRejected 词形守卫负路径（R1 纵深①）：
// 超长 ID 与控制字符 ID 直接 400，且发生在读任何缓存之前（不产生 seen
// 占坑，重投同 ID 词形合法后可正常处理）。
func TestWebhookOversizedDeliveryIDRejected(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	startWebhookWorker(t, src)
	sourceDir, sha := newSourceRepo(t, composeFixture)
	ctx := context.Background()
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	secret := "hook-secret-at-least-16"
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	body := pushBody("refs/heads/main", sha)
	post := func(delivery string) int {
		resp, _ := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": sign([]byte(secret), body),
			"X-GitHub-Delivery":   delivery,
		}, body)
		return resp.StatusCode
	}
	if got := post(strings.Repeat("x", maxDeliveryIDLen+1)); got != 400 {
		t.Fatalf("oversized delivery id status = %d, want 400", got)
	}
	// 控制字符词形在单元层钉死（TestValidDeliveryID）——net/http 客户端
	// 拒绝发送含控制字符的头值，传输面到不了这里。
	// 守卫先于缓存：bad ID 未占坑，同投递换合法 ID 正常受理（D1 后 202；
	// 部署行经 worker 落库）。
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-valid-after-guard",
	}, body)
	if resp.StatusCode != 202 {
		t.Fatalf("valid id after guard rejections status = %d, want 202 (%s)", resp.StatusCode, raw)
	}
	src.hooks.waitIdle()
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments for sha = %d err=%v, want 1", n, err)
	}
}

// TestWebhookConcurrentSameDeliverySingleDeploy 并发同 ID（R2）：进坑为
// 原子「查 + 占」——首个请求受理期间，同 ID 并发投递全部在 Claim 处
// 409（D1 后坑位在受理段即闭环，不再放大到 fetch 时长）；终态恰好一份
// 部署。
func TestWebhookConcurrentSameDeliverySingleDeploy(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	startWebhookWorker(t, src)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"
	sourceDir, sha := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	body := pushBody("refs/heads/main", sha)
	const k = 8
	type result struct {
		status int
		err    error
	}
	results := make(chan result, k)
	for i := 0; i < k; i++ {
		go func() {
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/apps/my-api/webhooks/github",
				strings.NewReader(string(body)))
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Hub-Signature-256", sign([]byte(secret), body))
			req.Header.Set("X-GitHub-Delivery", "d-concurrent")
			resp, err := srv.Client().Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer func() { _ = resp.Body.Close() }()
			results <- result{status: resp.StatusCode}
		}()
	}
	okCount, conflictCount := 0, 0
	for i := 0; i < k; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent post: %v", r.err)
		}
		switch r.status {
		case 202:
			okCount++
		case 409:
			conflictCount++
		default:
			t.Fatalf("unexpected status %d in concurrent same-id race", r.status)
		}
	}
	if okCount != 1 || conflictCount != k-1 {
		t.Fatalf("concurrent same-id: 202=%d 409=%d, want exactly 1 accepted and %d conflicts", okCount, conflictCount, k-1)
	}
	src.hooks.waitIdle()
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments for sha = %d err=%v, want exactly 1", n, err)
	}
}

// ── D1（S17 类 D）新增：异步化验收面 ────────────────────────────────────────

// TestWebhookSlowFetchDoesNotBlockResponse 慢拉源不阻塞响应（D1 核心验收）：
// fetch 挂起（门闸注入，模拟大仓库 20s+ 拉源）期间受理响应已在远早于
// GitHub 10s 投递超时的时刻返回 202；部署行只在 fetch 完成后落库。
func TestWebhookSlowFetchDoesNotBlockResponse(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	startWebhookWorker(t, src)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"

	sourceDir, sha := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	// 慢拉源注入（fake 20s+——门闸形态：阻塞至放行后执行真实拉源，避免
	// 真实 sleep 的测试时长与竞态）。
	fetchStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	src.fetchFn = func(ctx context.Context, plan fetchPlan) error {
		fetchStarted <- struct{}{}
		<-release
		return runFetch(ctx, plan)
	}

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	body := pushBody("refs/heads/main", sha)
	// 受理响应时延断言：远小于 fetch 挂起时长（此处门闸恒阻塞——任何
	// ≥2s 的响应时延都意味着同步执行回归，GitHub 10s 投递超时会被掐断）。
	start := time.Now()
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-slow-fetch",
	}, body)
	elapsed := time.Since(start)
	if resp.StatusCode != 202 {
		t.Fatalf("slow-fetch accept status = %d (%s)", resp.StatusCode, raw)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("response blocked on fetch: %v (must return well before GitHub 10s delivery timeout)", elapsed)
	}
	// fetch 在 worker 侧执行中（未完成），部署行必然不存在。
	select {
	case <-fetchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start fetch within 5s")
	}
	if n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha); err != nil || n != 0 {
		t.Fatalf("deployments for sha during fetch = %d err=%v, want 0", n, err)
	}
	// 放行 fetch → worker 完成入队。
	close(release)
	src.hooks.waitIdle()
	if n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha); err != nil || n != 1 {
		t.Fatalf("deployments for sha after release = %d err=%v, want 1", n, err)
	}
}

// TestWebhookQueueFullReturns503 队列满背压（D1）：带界队列 32——不启动
// worker（受理项滞留驱动队满），前 32 个受理 202，第 33 个 503 + 撤坑
// （同 ID 重投不被 409 挡——重投链保持通）。
func TestWebhookQueueFullReturns503(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"

	sourceDir, _ := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	// 不同 sha（各自通过幂等去重）× 队列容量 → 全部受理滞留（worker 未
	// 启动，受理项只积累不消费——驱动队满路径）。
	for i := 0; i < webhookQueueCapacity; i++ {
		distinct := fmt.Sprintf("%040x", i+1)
		body := pushBody("refs/heads/main", distinct)
		resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": sign([]byte(secret), body),
			"X-GitHub-Delivery":   fmt.Sprintf("d-full-%d", i),
		}, body)
		if resp.StatusCode != 202 {
			t.Fatalf("queue item %d status = %d, want 202 (%s)", i, resp.StatusCode, raw)
		}
	}
	// 第 33 个 → 503（背压可见）。
	overflow := fmt.Sprintf("%040x", webhookQueueCapacity+1)
	overflowBody := pushBody("refs/heads/main", overflow)
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), overflowBody),
		"X-GitHub-Delivery":   "d-overflow",
	}, overflowBody)
	if resp.StatusCode != 503 {
		t.Fatalf("overflow status = %d, want 503 (%s)", resp.StatusCode, raw)
	}
	// 503 已撤坑：同 ID 重投（队列仍满）→ 仍 503 而非 409（重投链保持通）。
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), overflowBody),
		"X-GitHub-Delivery":   "d-overflow",
	}, overflowBody)
	if resp.StatusCode != 503 {
		t.Fatalf("overflow redelivery status = %d, want 503 not 409 (%s)", resp.StatusCode, raw)
	}
	// 无 worker：受理项全部滞留，库内无部署行。
	if n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, fmt.Sprintf("%040x", 1)); err != nil || n != 0 {
		t.Fatalf("deployments while queue stalled = %d err=%v, want 0", n, err)
	}
}

// TestWebhookWorkerDrainsOnStop 停机排空（D1 生命周期）：受理后不等执行
// 直接停机——Stop 等待队列排空退出（在处理项不被取消打断），排空完成后
// 部署行可见；排水位立起后新投递 503。
func TestWebhookWorkerDrainsOnStop(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"

	sourceDir, sha := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	runCtx, cancel := context.WithCancel(context.Background())
	src.StartWebhookWorker(runCtx)

	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	body := pushBody("refs/heads/main", sha)
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-drain",
	}, body)
	if resp.StatusCode != 202 {
		t.Fatalf("accept status = %d (%s)", resp.StatusCode, raw)
	}
	// 不等 waitIdle：直接停机——Stop（无限等待形）返回即排空完成。
	cancel()
	if err := src.StopWebhookWorker(context.Background()); err != nil {
		t.Fatalf("worker stop: %v", err)
	}
	if n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha); err != nil || n != 1 {
		t.Fatalf("deployments for sha after drain = %d err=%v, want 1 (queue must drain on stop)", n, err)
	}
	// 排水位已立：停机后的新投递 503（不进无人消费的队列）。sha 取独立
	// 值——已部署 sha 会在上游幂等去重处 200 duplicate，到不了入队段。
	afterStopSHA := fmt.Sprintf("%040x", 0xbeef)
	afterStopBody := pushBody("refs/heads/main", afterStopSHA)
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), afterStopBody),
		"X-GitHub-Delivery":   "d-after-stop",
	}, afterStopBody)
	if resp.StatusCode != 503 {
		t.Fatalf("post-stop accept status = %d, want 503 (%s)", resp.StatusCode, raw)
	}
}
