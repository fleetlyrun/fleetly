package main

// Console 静态托管测试（T2.21 验收）：生产同构装配（gRPC 拦截链 + gateway
// mux + /ui/ 静态分派）下——
//   - /ui/ 返回 index.html（200，text/html）；
//   - 静态资源 /ui/assets/** 原样托管（200）；
//   - SPA 深链 /ui/apps/xyz 回退 index.html（200）；
//   - 目录穿越形态（/ui/%2e%2e/...）404，目录外不可达；
//   - 豁免精确到 /ui/ 前缀：/v1/apps 无 token 仍 401（信封形态）；
//   - static_dir 缺 index.html → NewHTTPServer fail-fast；
//   - static_dir 缺省（未启用）→ /ui/ 不分派（退回 gateway mux 404 形态）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"

	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// startConsoleHarness 起 gRPC（完整拦截链）+ gateway mux + /ui/ 静态分派
// 的生产同构 HTTP 面，返回 base URL。
func startConsoleHarness(t *testing.T, staticDir string) string {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(t.TempDir(), "test.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}
	auth := api.NewAuthenticator(st)
	gs := lynxgrpc.NewServer(
		lynxgrpc.WithAddr("127.0.0.1:0"),
		lynxgrpc.WithLogger(discardLogger()),
		lynxgrpc.WithHealthCheckers(noCheckers),
		lynxgrpc.WithInterceptors(
			auth.UnaryAuthInterceptor(),
			validateUnaryInterceptor(validator),
		),
	)
	g := gs.GetServer()
	serverv1.RegisterSystemServiceServer(g, api.NewSystemService("dev", st,
		func() []api.SystemComponent { return nil }, nil))
	serverv1.RegisterAppsServiceServer(g, api.NewAppsService(st, box, "127.0.0.1:8424"))
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
	var consoleUI http.Handler
	if staticDir != "" {
		consoleUI, err = newConsoleUIHandler(staticDir)
		if err != nil {
			t.Fatalf("newConsoleUIHandler: %v", err)
		}
	}
	return startRootHTTP(t, newRootHandler(nil, consoleUI, mux))
}

func writeConsoleDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html":         "<!doctype html><html><head><title>fleetly console</title></head><body>app-shell</body></html>",
		"assets/app-abc.js":  "console.log('fleetly-console-bundle')",
		"assets/app-abc.css": "body{color:teal}",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGatewayConsoleStaticHosting(t *testing.T) {
	dir := writeConsoleDist(t)
	base := startConsoleHarness(t, dir)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // 不跟随重定向：逐跳断言
	}}

	get := func(path string) (int, string, string) {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			b.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		return resp.StatusCode, b.String(), resp.Header.Get("Content-Type")
	}

	const indexHTML = "<!doctype html><html><head><title>fleetly console</title></head><body>app-shell</body></html>"

	// 面 1：/ui/ 返回 index.html。
	code, body, ctype := get("/ui/")
	if code != 200 || body != indexHTML || !strings.Contains(ctype, "text/html") {
		t.Fatalf("GET /ui/ = %d %q (%s)", code, body, ctype)
	}

	// 面 2：静态资源原样托管（Content-Type 按扩展名）。
	code, body, ctype = get("/ui/assets/app-abc.js")
	if code != 200 || body != "console.log('fleetly-console-bundle')" || !strings.Contains(ctype, "javascript") {
		t.Fatalf("GET /ui/assets/app-abc.js = %d %q (%s)", code, body, ctype)
	}
	code, _, ctype = get("/ui/assets/app-abc.css")
	if code != 200 || !strings.Contains(ctype, "text/css") {
		t.Fatalf("GET /ui/assets/app-abc.css = %d (%s)", code, ctype)
	}

	// 面 3：SPA 深链回退 index.html。
	code, body, _ = get("/ui/apps/demo")
	if code != 200 || body != indexHTML {
		t.Fatalf("GET /ui/apps/demo = %d %q, want 200 index.html (SPA fallback)", code, body)
	}

	// 面 4：目录穿越 404（fs.ValidPath 拒绝 ..）。
	const traversalPath = "/ui/%2e%2e/%2e%2e/fleetly.key"
	code, _, _ = get(traversalPath)
	if code != 404 {
		t.Fatalf("GET %s = %d, want 404", traversalPath, code)
	}

	// 面 5：豁免精确到 /ui/ 前缀——/v1/apps 无 token 仍 401 退化信封。
	code, body, _ = get("/v1/apps")
	if code != 401 {
		t.Fatalf("GET /v1/apps (no token) = %d, want 401 (static hosting must not widen the exemption)", code)
	}
	if !strings.Contains(body, `"message"`) {
		t.Fatalf("GET /v1/apps 401 body = %q, want envelope form", body)
	}
	// 近似前缀不分派（/ui2、/ui/.. 之外的收敛形态由 mux 兜底 404）。
	code, _, _ = get("/uix")
	if code == 200 {
		t.Fatalf("GET /uix = 200, want non-static dispatch")
	}
}

// TestConsoleStaticDirValidation 验证装配期校验：缺 index.html fail-fast；
// 未启用（空串）时 /ui/ 不分派（落回 gateway mux 404）。
func TestConsoleStaticDirValidation(t *testing.T) {
	empty := t.TempDir()
	if _, err := newConsoleUIHandler(empty); err == nil {
		t.Fatal("newConsoleUIHandler(empty dir) succeeded, want fail-fast (no index.html)")
	}

	// 未启用形态：consoleUI = nil，/ui/ 交回 gateway mux（404）。用桩
	// fallback 断言分派面收敛（不经真 mux，聚焦分派语义）。
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	root := newRootHandler(nil, nil, fallback)
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != 404 {
		t.Fatalf("disabled console: GET /ui/ = %d, want 404 (fallback reached)", rec.Code)
	}

	// webhook 分派不受第三个 handler 影响（既有豁免面不变）。
	hit := false
	webhook := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true })
	root = newRootHandler(webhook, nil, fallback)
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/apps/demo/webhooks/github", nil))
	if !hit {
		t.Fatal("webhook path not dispatched after console integration")
	}
}
