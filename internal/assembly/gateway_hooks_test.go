package assembly

// webhook 原生入口 adapter 单测：token/头/体的搬运、路径归拒、protojson
// 归一输出、错误信封出口。假客户端锚定 adapter 行为（服务面场景在
// internal/apitest，HTTP 端到端在 e2e dind smoke）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
)

// fakeHookClient 记录最近一次请求并回放配置的响应（窄接口满足
// hooksReceiver——adapter 只消费 ReceiveWebhook）。
type fakeHookClient struct {
	got  *deliveryv1.ReceiveWebhookRequest
	resp *deliveryv1.ReceiveWebhookResponse
	err  error
}

func (f *fakeHookClient) ReceiveWebhook(_ context.Context, req *deliveryv1.ReceiveWebhookRequest, _ ...grpc.CallOption) (*deliveryv1.ReceiveWebhookResponse, error) {
	f.got = req
	return f.resp, f.err
}

var _ hooksReceiver = (*fakeHookClient)(nil)

func post(h http.Handler, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHooksHandlerForwardsGitHubShape(t *testing.T) {
	fake := &fakeHookClient{resp: &deliveryv1.ReceiveWebhookResponse{Status: "accepted", DeploymentId: "01DEP"}}
	h := newHooksHandler(fake)

	rec := post(h, fleetlygrpc.HooksURLPrefix+"flthook_abc123", `{"ref":"refs/heads/main"}`, map[string]string{
		"X-GitHub-Event":      "push",
		"X-GitHub-Delivery":   "d-1",
		"X-Hub-Signature-256": "sha256=deadbeef",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// 请求面逐字段搬运（体是原始字节）。
	assert.Equal(t, "flthook_abc123", fake.got.GetToken())
	assert.Equal(t, "push", fake.got.GetEvent())
	assert.Equal(t, "d-1", fake.got.GetDelivery())
	assert.Equal(t, "sha256=deadbeef", fake.got.GetSignature())
	assert.Equal(t, `{"ref":"refs/heads/main"}`, string(fake.got.GetPayload()))
	// 响应是紧凑 protojson snake_case。
	assert.JSONEq(t, `{"status":"accepted","deployment_id":"01DEP"}`, rec.Body.String())
}

func TestHooksHandlerRejectsBadShape(t *testing.T) {
	fake := &fakeHookClient{resp: &deliveryv1.ReceiveWebhookResponse{Status: "pong"}}
	h := newHooksHandler(fake)

	// 非 POST / 空 token / 多段路径全拒，且不打到 gRPC 面。
	for _, tc := range []struct {
		method, path string
		wantCode     int
	}{
		{http.MethodGet, fleetlygrpc.HooksURLPrefix + "flthook_x", http.StatusMethodNotAllowed},
		{http.MethodPost, fleetlygrpc.HooksURLPrefix, http.StatusNotFound},
		{http.MethodPost, fleetlygrpc.HooksURLPrefix + "a/b", http.StatusNotFound},
	} {
		req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, tc.wantCode, rec.Code, "%s %s", tc.method, tc.path)
	}
	assert.Nil(t, fake.got)
}

func TestHooksHandlerErrorEnvelope(t *testing.T) {
	fake := &fakeHookClient{err: status.Error(codes.Unauthenticated, "invalid hook token")}
	h := newHooksHandler(fake)

	rec := post(h, fleetlygrpc.HooksURLPrefix+"flthook_wrong", `{}`, nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	// 信封形态（apperr 退化面：code + message）；token 不回显。
	assert.Contains(t, rec.Body.String(), `"message"`)
	assert.NotContains(t, rec.Body.String(), "flthook_wrong")
}

func TestHooksHandlerRejectsOversizePayload(t *testing.T) {
	fake := &fakeHookClient{resp: &deliveryv1.ReceiveWebhookResponse{Status: "accepted"}}
	h := newHooksHandler(fake)

	// 超过 hookPayloadLimit（25MiB）的请求体：设计内 413，且不打到 gRPC 面
	//（Q-11：大 payload 的拒绝语义由本层拥有，不依赖传输层兜底）。
	body := strings.Repeat("x", hookPayloadLimit+1)
	rec := post(h, fleetlygrpc.HooksURLPrefix+"flthook_t", body, nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Nil(t, fake.got, "oversize payload must be rejected before the gRPC hop")
}

func TestMountHooksRouting(t *testing.T) {
	gw := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // gateway 面的哨兵码
	})
	hooks := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := mountHooks(gw, hooks)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, fleetlygrpc.HooksURLPrefix+"tok", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/projects", nil))
	assert.Equal(t, http.StatusTeapot, rec.Code, "non-hook paths must fall through to the grpc-gateway mux")
}
