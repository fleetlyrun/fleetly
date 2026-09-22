package api

// NotificationsService 的 API 面单测（E6 W5-S4，observability §5）：
// 逐 RPC 覆盖——创建（secret 明文一次性 + 指纹/密文入库）、名字冲突 409、
// 不存在 404、模式白名单 422、启停翻转、轮换换指纹、TestWebhook 真实
// HMAC 验签（服务端用独立实现重算——与 e2e 同口径）、台账读面过滤。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

func newNotificationsTestEnv(t *testing.T) (*state.Store, *secrets.Box, serverv1.NotificationsServiceClient, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), dir+"/test.db")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(dir + "/test.key")
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	auth := NewAuthenticator(st)
	srv := newAuthServer(auth)
	serverv1.RegisterNotificationsServiceServer(srv, NewNotificationsService(st, box))
	conn := serveBufconn(t, srv)
	token := seedTokenPlain(t, st, "admin")
	return st, box, serverv1.NewNotificationsServiceClient(conn), token
}

func mustCreateEndpoint(t *testing.T, cl serverv1.NotificationsServiceClient, token, name, url string, patterns ...string) *serverv1.CreateWebhookEndpointResponse {
	t.Helper()
	enabled := true
	resp, err := cl.CreateWebhookEndpoint(authCtx(context.Background(), token), &serverv1.CreateWebhookEndpointRequest{
		Name: name, Url: url, EventPatterns: patterns, Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("create endpoint %s: %v", name, err)
	}
	return resp
}

func TestWebhookCreateReturnsSecretOnceAndStoresCipher(t *testing.T) {
	_, _, cl, token := newNotificationsTestEnv(t)
	ctx := authCtx(context.Background(), token)
	resp := mustCreateEndpoint(t, cl, token, "ops", "https://ops.example.test/hook", "deployment.*")

	if resp.GetSecret() == "" {
		t.Fatal("create must return the plaintext secret once")
	}
	if len(resp.GetEndpoint().GetSecretFingerprint()) != 16 {
		t.Fatalf("fingerprint form: %q", resp.GetEndpoint().GetSecretFingerprint())
	}
	// 读面零敏感投影：List/Get 不出现明文，只出指纹。
	list, err := cl.ListWebhookEndpoints(ctx, &serverv1.ListWebhookEndpointsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.GetEndpoints()) != 1 {
		t.Fatalf("endpoints: %d", len(list.GetEndpoints()))
	}
	view := list.GetEndpoints()[0]
	if strings.Contains(view.String(), resp.GetSecret()) {
		t.Fatal("list must never carry the plaintext secret")
	}
	if view.GetSecretFingerprint() != resp.GetEndpoint().GetSecretFingerprint() {
		t.Fatal("fingerprint must round-trip")
	}
}

func TestWebhookConflictNotFoundAndPatternValidation(t *testing.T) {
	_, _, cl, token := newNotificationsTestEnv(t)
	ctx := authCtx(context.Background(), token)
	mustCreateEndpoint(t, cl, token, "ops", "https://ops.example.test/hook", "deployment.*")

	// 重名 → E_WEBHOOK_NAME_CONFLICT（注册表码 409 信封）。
	enabled := true
	_, err := cl.CreateWebhookEndpoint(ctx, &serverv1.CreateWebhookEndpointRequest{
		Name: "ops", Url: "https://other.example.test", EventPatterns: []string{"*"}, Enabled: &enabled,
	})
	appErr, ok := apperr.FromError(err)
	if !ok || appErr.Code() != "E_WEBHOOK_NAME_CONFLICT" {
		t.Fatalf("name conflict: err = %v", err)
	}

	// 不存在 → E_WEBHOOK_NOT_FOUND（404 信封）。
	_, err = cl.GetWebhookEndpoint(ctx, &serverv1.GetWebhookEndpointRequest{Id: "01NOPE"})
	appErr, ok = apperr.FromError(err)
	if !ok || appErr.Code() != "E_WEBHOOK_NOT_FOUND" {
		t.Fatalf("not found: err = %v", err)
	}

	// 模式白名单 → E_WEBHOOK_PATTERN_INVALID（422）。
	_, err = cl.CreateWebhookEndpoint(ctx, &serverv1.CreateWebhookEndpointRequest{
		Name: "bad", Url: "https://x.example.test", EventPatterns: []string{"BAD;drop"},
	})
	appErr, ok = apperr.FromError(err)
	if !ok || appErr.Code() != "E_WEBHOOK_PATTERN_INVALID" {
		t.Fatalf("pattern invalid: err = %v", err)
	}

	// URL 形状 → 400 退化信封（不占用注册表码）。
	_, err = cl.CreateWebhookEndpoint(ctx, &serverv1.CreateWebhookEndpointRequest{
		Name: "badurl", Url: "ftp://x", EventPatterns: []string{"*"},
	})
	if status.Convert(err).Code() != codes.InvalidArgument {
		t.Fatalf("bad url: code = %s", status.Convert(err).Code())
	}
}

func TestWebhookUpdateEnableDisableAndRotate(t *testing.T) {
	_, _, cl, token := newNotificationsTestEnv(t)
	ctx := authCtx(context.Background(), token)
	created := mustCreateEndpoint(t, cl, token, "ops", "https://ops.example.test/hook", "deployment.*")

	// 停用。
	disabled := false
	upd, err := cl.UpdateWebhookEndpoint(ctx, &serverv1.UpdateWebhookEndpointRequest{
		Id: created.GetEndpoint().GetId(), Enabled: &disabled,
	})
	if err != nil || upd.GetEndpoint().GetEnabled() {
		t.Fatalf("disable: %v (%v)", err, upd.GetEndpoint().GetEnabled())
	}
	// 启用。
	enabled := true
	upd, err = cl.UpdateWebhookEndpoint(ctx, &serverv1.UpdateWebhookEndpointRequest{
		Id: created.GetEndpoint().GetId(), Enabled: &enabled,
	})
	if err != nil || !upd.GetEndpoint().GetEnabled() {
		t.Fatalf("enable: %v", err)
	}
	// 改模式集（整体替换）。
	upd, err = cl.UpdateWebhookEndpoint(ctx, &serverv1.UpdateWebhookEndpointRequest{
		Id: created.GetEndpoint().GetId(), EventPatterns: []string{"cron.failed", "app.*"},
	})
	if err != nil || len(upd.GetEndpoint().GetEventPatterns()) != 2 {
		t.Fatalf("set-patterns: %v (%v)", err, upd.GetEndpoint().GetEventPatterns())
	}
	// 轮换：新明文 ≠ 旧明文，指纹变化。
	rot, err := cl.RotateWebhookSecret(ctx, &serverv1.RotateWebhookSecretRequest{Id: created.GetEndpoint().GetId()})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rot.GetSecret() == "" || rot.GetSecret() == created.GetSecret() {
		t.Fatal("rotation must produce a fresh plaintext secret")
	}
	if rot.GetSecretFingerprint() == created.GetEndpoint().GetSecretFingerprint() {
		t.Fatal("fingerprint must change on rotation")
	}
	// 删除后读取 404。
	if _, err := cl.DeleteWebhookEndpoint(ctx, &serverv1.DeleteWebhookEndpointRequest{Id: created.GetEndpoint().GetId()}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cl.GetWebhookEndpoint(ctx, &serverv1.GetWebhookEndpointRequest{Id: created.GetEndpoint().GetId()}); err == nil {
		t.Fatal("deleted endpoint must 404")
	}
}

// TestWebhookTestEndpointSignsCorrectly：TestWebhook 用库内解密的密钥按契
// 算法签名——接收侧（测试服务器）用创建响应的明文独立重算 HMAC 验签
// （真实重算，非 grep 签名存在——与 e2e 同口径）。
func TestWebhookTestEndpointSignsCorrectly(t *testing.T) {
	_, _, cl, token := newNotificationsTestEnv(t)

	var mu sync.Mutex
	var gotBody []byte
	var gotTS, gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		mu.Lock()
		gotBody, gotTS, gotSig = body, r.Header.Get("X-Fleetly-Timestamp"), r.Header.Get("X-Fleetly-Signature")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	created := mustCreateEndpoint(t, cl, token, "ops", srv.URL, "deployment.*")
	resp, err := cl.TestWebhook(authCtx(context.Background(), token), &serverv1.TestWebhookRequest{Id: created.GetEndpoint().GetId()})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !resp.GetOk() || resp.GetStatusCode() != 200 {
		t.Fatalf("test delivery: %+v (%s)", resp, resp.GetError())
	}
	// 独立重算：sha256=hex(HMAC-SHA256(secret, ts + "." + body))——测试侧
	// 用标准库直算，不 import notify 包（算法契约的双实现互证）。
	mac := hmac.New(sha256.New, []byte(created.GetSecret()))
	mac.Write([]byte(gotTS))
	mac.Write([]byte("."))
	mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if want != gotSig {
		t.Fatalf("HMAC recompute failed: want %q got %q (body=%s)", want, gotSig, gotBody)
	}
	// type=test 标位（设计 §5.2：结构同真实事件）。
	if !strings.Contains(string(gotBody), `"type":"test"`) {
		t.Fatalf("test payload type missing: %s", gotBody)
	}
}

// TestWebhookDeliveriesReadFace：台账读面 + 状态过滤（数据由 state 层直写
// ——api 面只投影）。
func TestWebhookDeliveriesReadFace(t *testing.T) {
	st, _, cl, token := newNotificationsTestEnv(t)
	ctx := authCtx(context.Background(), token)
	created := mustCreateEndpoint(t, cl, token, "ops", "https://ops.example.test/hook", "*")
	rows, err := st.CreateWebhookDeliveriesAndAdvance(ctx, 1, []string{created.GetEndpoint().GetId()})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.RecordWebhookAttempt(ctx, rows[0].ID, state.WebhookAttemptResult{
		OK: false, ResponseCode: 500, ErrText: "boom", NextRetryAt: time.Now().UTC().Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("attempt: %v", err)
	}
	all, err := cl.ListWebhookDeliveries(ctx, &serverv1.ListWebhookDeliveriesRequest{})
	if err != nil || len(all.GetDeliveries()) != 1 {
		t.Fatalf("list: %v (n=%d)", err, len(all.GetDeliveries()))
	}
	d := all.GetDeliveries()[0]
	if d.GetStatus() != "pending" || d.GetAttempts() != 1 || d.ResponseCode == nil || d.GetResponseCode() != 500 {
		t.Fatalf("delivery view: %+v", d)
	}
	ok, err := cl.ListWebhookDeliveries(ctx, &serverv1.ListWebhookDeliveriesRequest{Status: "ok"})
	if err != nil || len(ok.GetDeliveries()) != 0 {
		t.Fatalf("status filter: %v (n=%d)", err, len(ok.GetDeliveries()))
	}
}
