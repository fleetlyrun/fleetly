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

// webhook 入口测试（T2.19 验收断言面）：验签正/负、delivery ID TTL 防重放、
// 自定义时间戳窗、(app, sha) 幂等去重（部署计数不变）、分支过滤、
// 未配置即未启用（404）、provider 白名单。

// sign 计算投递签名（X-Hub-Signature-256 同形）。
func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
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

// ── HTTP 全链（验签/防重放/去重/分支过滤/未配置语义）────────────────────────

func TestWebhookHandlerFullChain(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
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

	// 3. 正确签名 → enqueued（真实拉源：fetch file 路径 remote）。
	resp, raw = post(hdr("d1", mainBody), mainBody)
	if resp.StatusCode != 200 {
		t.Fatalf("valid signature status = %d (%s)", resp.StatusCode, raw)
	}
	var rec webhookReceipt
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Status != "enqueued" || rec.DeploymentID == "" {
		t.Fatalf("receipt = %s err=%v", raw, err)
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

// ── 失败-重投链（对抗审查整改①的验收链路）─────────────────────────────────
//
// 真实链路断言：拉源瞬态失败 → 5xx（不占重放缓存）→ 官方重投（同一
// delivery ID）且拉源恢复 → 第二次成功入队；成功后同 ID 重放仍 409
// （既有防重放语义不回退）。
func TestWebhookFetchFailureDoesNotPoisonReplayCache(t *testing.T) {
	src, st, box, _ := newTestSource(t, time.Minute)
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

	// 1. 拉源失败 → 5xx（E_RUNTIME_UNAVAILABLE 信封）。
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", hdr("d-redelivery"), body)
	if resp.StatusCode < 500 || resp.StatusCode > 599 {
		t.Fatalf("fetch-failure status = %d, want 5xx (%s)", resp.StatusCode, raw)
	}
	// 2. 同一 delivery ID 立即重投（拉源仍坏）→ 仍是 5xx 而非 409——
	//    失败投递没有占坑，官方重投可达处理链。
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", hdr("d-redelivery"), body)
	if resp.StatusCode < 500 || resp.StatusCode > 599 {
		t.Fatalf("immediate redelivery status = %d, want 5xx not 409 (%s)", resp.StatusCode, raw)
	}
	// 3. 拉源恢复（源 URL 指回真实仓库）→ 同一 delivery ID 重投成功入队。
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")
	resp, raw = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", hdr("d-redelivery"), body)
	if resp.StatusCode != 200 {
		t.Fatalf("recovered redelivery status = %d, want 200 (%s)", resp.StatusCode, raw)
	}
	var rec webhookReceipt
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Status != "enqueued" || rec.DeploymentID == "" {
		t.Fatalf("recovered redelivery receipt = %s err=%v", raw, err)
	}
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
	// 同 delivery ID 换正确签名重投：未被 401 占坑 → 正常入队。
	goodHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-bruteforce",
	}
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", goodHeaders, body)
	if resp.StatusCode != 200 {
		t.Fatalf("same-id after bad signature status = %d, want 200 (401 must not poison cache: %s)", resp.StatusCode, raw)
	}
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments for sha = %d err=%v, want 1", n, err)
	}

	// 时间窗外 401 同样不占坑：过期 X-Fleetly-Timestamp 被拒后，同 ID 带
	// 合法时间戳头可正常处理（同 sha 已入队 → duplicate 终局）。
	staleHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-stale-ts",
		"X-Fleetly-Timestamp": "1000000000", // 远在窗口外
	}
	resp, _ = postRaw(t, srv, "/v1/apps/my-api/webhooks/github", staleHeaders, body)
	if resp.StatusCode != 401 {
		t.Fatalf("stale timestamp status = %d, want 401", resp.StatusCode)
	}
	freshHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-stale-ts",
		"X-Fleetly-Timestamp": fmt.Sprintf("%d", time.Now().Unix()),
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
	// 守卫先于缓存：bad ID 未占坑，同投递换合法 ID 正常入队。
	resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
		"Content-Type":        "application/json",
		"X-Hub-Signature-256": sign([]byte(secret), body),
		"X-GitHub-Delivery":   "d-valid-after-guard",
	}, body)
	if resp.StatusCode != 200 {
		t.Fatalf("valid id after guard rejections status = %d, want 200 (%s)", resp.StatusCode, raw)
	}
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments for sha = %d err=%v, want 1", n, err)
	}
}

// TestWebhookConcurrentSameDeliverySingleDeploy 并发同 ID（R2）：进坑为
// 原子「查 + 占」——首个请求拉源/入队期间，同 ID 并发投递全部在 Claim
// 处 409，不再放大到 fetch 时长；终态恰好一份部署。
func TestWebhookConcurrentSameDeliverySingleDeploy(t *testing.T) {
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
		case 200:
			okCount++
		case 409:
			conflictCount++
		default:
			t.Fatalf("unexpected status %d in concurrent same-id race", r.status)
		}
	}
	if okCount != 1 || conflictCount != k-1 {
		t.Fatalf("concurrent same-id: 200=%d 409=%d, want exactly 1 ok and %d conflicts", okCount, conflictCount, k-1)
	}
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments for sha = %d err=%v, want exactly 1", n, err)
	}
}
