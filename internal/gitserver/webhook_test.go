package gitserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	if c.Seen("d1") {
		t.Fatal("first sight reported dup")
	}
	if !c.Seen("d1") {
		t.Fatal("second sight not detected")
	}
	cur = now.Add(2 * time.Minute) // TTL 过期
	if c.Seen("d1") {
		t.Fatal("expired delivery reported dup")
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
	setAppSource(t, st, appRow.ID, sourceDir, "main")

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
