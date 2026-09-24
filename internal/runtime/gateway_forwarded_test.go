package runtime

import (
	"context"
	"fmt"
	"io"
	gohttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"
	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// W3-S4 XFF 覆写验收（W1-S2 披露的信任语义收口）：gateway 面入向
// X-Forwarded-For 头在进 mux 前被 sanitizeForwardedFor 清除，grpc-gateway
// 以真实 TCP 对端地址回填 x-forwarded-for metadata——api 面 IP 键限流按
// 真实对端分桶，伪造链首值不能稀释桶。

// TestSanitizeForwardedForStripsHeader 是覆写中间件的单元断言：XFF 删除、
// 其余头原样透传（分派面 = 覆写面，不做额外改写）。
func TestSanitizeForwardedForStripsHeader(t *testing.T) {
	var gotXFF, gotXReq, gotMethod string
	h := sanitizeForwardedFor(gohttp.HandlerFunc(func(w gohttp.ResponseWriter, r *gohttp.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotXReq = r.Header.Get("X-Requested-With")
		gotMethod = r.Method
	}))
	req := httptest.NewRequest(gohttp.MethodPost, "/v1/auth/register", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 7.7.7.7")
	req.Header.Set("X-Requested-With", "probe")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotXFF != "" {
		t.Fatalf("X-Forwarded-For after sanitize = %q, want empty", gotXFF)
	}
	if gotXReq != "probe" || gotMethod != gohttp.MethodPost {
		t.Fatalf("other headers/method must pass through untouched: %q %q", gotXReq, gotMethod)
	}
}

// TestGatewayForwardedForForgeryCannotDiluteRateLimit 是 gateway 覆写的
// 行为断言（生产同构装配：gRPC 环回 + newGatewayMux 含清洗层）：注册限流
// 的 IP 桶按真实 HTTP 对端（本测试全部请求同源 127.0.0.1）计满——每次
// 携带全新伪造 XFF 的第 11 次注册被 429。若伪造值参与分桶（旧语义），每
// 次都拿到新桶、恒为 403 E_REGISTRATION_CLOSED，本断言不可能通过。
func TestGatewayForwardedForForgeryCannotDiluteRateLimit(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gs := lynxgrpc.NewServer(
		lynxgrpc.WithAddr("127.0.0.1:0"),
		lynxgrpc.WithLogger(discardLogger()),
		lynxgrpc.WithHealthCheckers(noCheckers),
		lynxgrpc.WithInterceptors(api.NewAuthenticator(st).UnaryAuthInterceptor()),
	)
	g := gs.GetServer()
	serverv1.RegisterAuthServiceServer(g, api.NewAuthService(st))
	if err := gs.Init(nil); err != nil {
		t.Fatalf("grpc Init: %v", err)
	}
	gsErr := make(chan error, 1)
	go func() { gsErr <- gs.Start(context.Background()) }()
	t.Cleanup(func() {
		_ = gs.Stop(context.Background())
		<-gsErr
	})
	select {
	case <-gs.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("grpc server not ready within 5s")
	}

	gw, err := newGatewayMux(gs.Addr())
	if err != nil {
		t.Fatalf("newGatewayMux: %v", err)
	}
	hs := lynxhttp.NewServer(gw,
		lynxhttp.WithAddr("127.0.0.1:0"),
		lynxhttp.WithHealthCheckers(noCheckers),
		lynxhttp.WithLogger(discardLogger()),
	)
	if err := hs.Init(nil); err != nil {
		t.Fatalf("http Init: %v", err)
	}
	hsErr := make(chan error, 1)
	go func() { hsErr <- hs.Start(context.Background()) }()
	t.Cleanup(func() {
		_ = hs.Stop(context.Background())
		<-hsErr
	})
	select {
	case <-hs.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("http server not ready within 5s")
	}
	base := "http://" + hs.Addr()
	client := &gohttp.Client{Timeout: 5 * time.Second}

	register := func(email, forgedXFF string) (int, string) {
		payload := fmt.Sprintf(`{"email":%q,"password":"pw-register-1"}`, email)
		req, err := gohttp.NewRequest(gohttp.MethodPost, base+"/v1/auth/register", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if forgedXFF != "" {
			req.Header.Set("X-Forwarded-For", forgedXFF)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/auth/register: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// 首用户注册（无伪造头）→ 200：窗口随后缺省关闭。
	if code, body := register("founder@example.com", ""); code != 200 {
		t.Fatalf("first register = %d body %s", code, body)
	}
	// 关窗后的 9 次注册：每次全新 email + 全新伪造 XFF → 全部 403
	//（业务拒绝；同时 IP 桶 2..10/10 计满——403 不喂 A3 失败限速）。
	for i := 1; i <= 9; i++ {
		email := fmt.Sprintf("user%d@example.com", i)
		code, body := register(email, fmt.Sprintf("10.%d.%d.%d", i, i, i))
		if code != 403 || !strings.Contains(body, "E_REGISTRATION_CLOSED") {
			t.Fatalf("forged-XFF register %d = %d body %s, want 403 E_REGISTRATION_CLOSED", i, code, body)
		}
	}
	// 第 11 次（又是全新 email + 全新伪造 XFF）→ 429：桶按真实对端计满，
	// 伪造值未稀释任何桶。若伪造生效，此处应是 403 而非 429。
	code, body := register("user10@example.com", "10.10.10.10")
	if code != 429 || !strings.Contains(body, "rate limit exceeded for registration") {
		t.Fatalf("11th forged-XFF register = %d body %s, want 429 rate-limit envelope (forgery must not dilute the IP bucket)", code, body)
	}
}
