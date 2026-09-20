package main

// B5 批次评审整改的 fleetlyd 装配面回归：
//   - H7 REST 面请求体上限（root handler 最外层，鉴权/解码前置 413）；
//   - M4-2 GitKeysService 的 gateway 注册（GET /v1/git/keys 走通而非 404）；
//   - M4-3 lynxhttp WriteTimeout 调优钩子（流式端点 15min，读侧保持紧）。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	gohttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"

	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestGatewayRequestBodyLimit H7：超限请求体在分派（webhook/console/
// gateway 解码与鉴权）之前被 413 拦截——无 token 亦然（防线先于鉴权），
// 且后端 fallback handler 从未被触达；声明 Content-Length 超限走预检短路，
// 谎报/缺省长度（chunked）经 MaxBytesReader 在读取时截断。
func TestGatewayRequestBodyLimit(t *testing.T) {
	reached := false
	fallback := gohttp.HandlerFunc(func(w gohttp.ResponseWriter, r *gohttp.Request) {
		reached = true
		// 模拟 gateway 面读 body 的行为：MaxBytesReader 超限在 Read 处报错
		//（真实链路由 marshaler 解码时消费——此处断言读取面被截断即可）。
		buf := make([]byte, 64)
		if _, err := r.Body.Read(buf); err == nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		w.WriteHeader(gohttp.StatusOK)
	})
	root := newRootHandler(gohttp.NotFoundHandler(), nil, fallback)

	// 面 1：声明超限 Content-Length 的 POST /v1/**（无 token）→ 413，先于
	// fallback（鉴权与解码都在其后）。
	oversized := bytes.Repeat([]byte("x"), maxRequestBodyBytes+1)
	req := httptest.NewRequest(gohttp.MethodPost, "/v1/apps", bytes.NewReader(oversized))
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	if rec.Code != gohttp.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", rec.Code)
	}
	if reached {
		t.Fatal("oversized request must be rejected before reaching gateway handler (H7 鉴权前置)")
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("413 envelope not JSON: %s", rec.Body.String())
	}
	if _, has := env["message"]; !has {
		t.Fatalf("413 envelope missing message: %s", rec.Body.String())
	}

	// 面 2：chunked（无 Content-Length）超限体——进入 fallback（分派正常），
	// 但 body 读取被 MaxBytesReader 截断（错误而非吞完 33MiB）。
	reached = false
	var readErr error
	fallback2 := gohttp.HandlerFunc(func(w gohttp.ResponseWriter, r *gohttp.Request) {
		reached = true
		_, readErr = io.Copy(io.Discard, r.Body)
		w.WriteHeader(gohttp.StatusOK)
	})
	root2 := newRootHandler(gohttp.NotFoundHandler(), nil, fallback2)
	req2 := httptest.NewRequest(gohttp.MethodPost, "/v1/apps", io.MultiReader(
		bytes.NewReader(oversized), // 超限体
		strings.NewReader("tail"))) // 确保总长 > 上限
	req2.ContentLength = -1 // httptest 对未知 reader 类型缺省即此（chunked 形态）
	rec2 := httptest.NewRecorder()
	root2.ServeHTTP(rec2, req2)
	if !reached {
		t.Fatal("chunked request should dispatch to gateway (预检不拦未声明长度)")
	}
	if readErr == nil || !strings.Contains(readErr.Error(), "too large") {
		t.Fatalf("chunked oversized body read err = %v, want MaxBytesReader truncation", readErr)
	}

	// 面 3：合法小请求体不受影响（透传 fallback）。
	reached = false
	req3 := httptest.NewRequest(gohttp.MethodPost, "/v1/apps", strings.NewReader(`{"compose":"x"}`))
	rec3 := httptest.NewRecorder()
	root2.ServeHTTP(rec3, req3)
	if rec3.Code != gohttp.StatusOK || !reached {
		t.Fatalf("normal body status = %d reached=%v, want 200/true", rec3.Code, reached)
	}
}

// TestGatewayGitKeysRegistered M4-2：GitKeysService 挂 gateway——GET
// /v1/git/keys 带 admin token 走通（200）而非 404；无 token 401（路由在
// 注册面内才会进鉴权链，404 形态即未注册的旧缺陷）。
func TestGatewayGitKeysRegistered(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	adminTok := seedScopedToken(t, st, "admin")

	auth := api.NewAuthenticator(st)
	gs := lynxgrpc.NewServer(
		lynxgrpc.WithAddr("127.0.0.1:0"),
		lynxgrpc.WithLogger(discardLogger()),
		lynxgrpc.WithHealthCheckers(noCheckers),
		lynxgrpc.WithInterceptors(auth.UnaryAuthInterceptor()),
	)
	g := gs.GetServer()
	serverv1.RegisterGitKeysServiceServer(g, api.NewGitKeysService(st))
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

	mux, err := newGatewayMux(gs.Addr())
	if err != nil {
		t.Fatalf("newGatewayMux: %v", err)
	}
	root := newRootHandler(gohttp.NotFoundHandler(), nil, mux)

	get := func(token string) (int, string) {
		req := httptest.NewRequest(gohttp.MethodGet, "/v1/git/keys", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// 无 token：401（路由存在、进鉴权链——未注册形态是 404）。
	if code, body := get(""); code != 401 {
		t.Fatalf("no-token GET /v1/git/keys = %d (%s), want 401 (路由已注册)", code, body)
	}
	// admin token：200 走通（M4-2）。
	if code, body := get(adminTok); code != 200 {
		t.Fatalf("admin GET /v1/git/keys = %d (%s), want 200 (M4-2 gateway 注册)", code, body)
	}
}

// TestTuneHTTPServerWriteTimeout M4-3：调优钩子只放宽 WriteTimeout 到
// 15min（lynx 缺省 60s 会静默掐断 /v1/events/stream 与 logs stream），读侧
// （ReadHeaderTimeout/ReadTimeout）保持 lynx 缺省紧口径不动。lynxhttp 在
// 内部超时之后应用 ServerOptions（server.go Start），本用例按同序构造后
// 断言终态。
func TestTuneHTTPServerWriteTimeout(t *testing.T) {
	srv := &gohttp.Server{
		ReadHeaderTimeout: 60 * time.Second, // lynx DefaultTimeout 缺省形态
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	tuneHTTPServer(srv)
	if srv.WriteTimeout < 15*time.Minute {
		t.Fatalf("WriteTimeout = %v, want >= 15min (M4-3 流式端点)", srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout != 60*time.Second || srv.ReadTimeout != 60*time.Second {
		t.Fatalf("read-side timeouts must stay tight: header=%v read=%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
}
