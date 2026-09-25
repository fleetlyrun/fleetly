package runtime

// 根路径引导页测试：GET / 不再落 gateway 404，而是返回静态引导页——
//   - GET / → 200 text/html，含 /ui/ 与 /v1 指引，携带 landing CSP；
//   - Console 未启用（consoleUI = nil）时引导页照常可用（无依赖）；
//   - 豁免精确到 GET/HEAD 的根路径：POST / 与未知路径仍进 gateway 面；
//   - 生产同构装配下 GET / 与 GET /v1/apps（无 token 401）形态不变。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLandingPageDispatch(t *testing.T) {
	fallbackHits := 0
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		http.NotFound(w, r)
	})
	root := newRootHandler(nil, nil, nil, nil, fallback)

	// 面 1：GET / → 200 引导页（Console 未启用也照常），含三处入口指引。
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 {
		t.Fatalf("GET / = %d, want 200 landing page", rec.Code)
	}
	ctype := rec.Header().Get("Content-Type")
	if !strings.Contains(ctype, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html", ctype)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != landingCSP {
		t.Fatalf("GET / Content-Security-Policy = %q, want %q", got, landingCSP)
	}
	body := rec.Body.String()
	for _, want := range []string{`href="/ui/"`, `href="/v1/apps"`, `href="/healthz/liveness"`, `href="/healthz/readiness"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET / body missing %q — landing must link all entry points", want)
		}
	}

	// 面 2：豁免面收敛在根路径——POST / 与未知路径进 gateway 面（此处以
	// 404 fallback 桩代表），引导页不得吞掉它们。
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/", nil),
		httptest.NewRequest(http.MethodDelete, "/", nil),
		httptest.NewRequest(http.MethodGet, "/nonexistent", nil),
		httptest.NewRequest(http.MethodGet, "//", nil),
	} {
		fallbackHits = 0
		rec = httptest.NewRecorder()
		root.ServeHTTP(rec, req)
		if fallbackHits != 1 {
			t.Fatalf("%s %s dispatched to landing (want gateway fallback)", req.Method, req.URL.Path)
		}
	}

	// 面 3：HEAD / 同样分派（真实 net/http 服务器自行抑制 body；httptest
	// Recorder 不模拟该行为，这里只钉分派与状态面）。
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != 200 {
		t.Fatalf("HEAD / = %d, want 200", rec.Code)
	}
}

// TestLandingPageOnProductionHarness 生产同构装配（gRPC 拦截链 + gateway
// mux + Console 托管）下 GET / 返回引导页、/v1 鉴权面不受影响。
func TestLandingPageOnProductionHarness(t *testing.T) {
	base := startConsoleHarness(t, writeConsoleDist(t))
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / = %d, want 200 landing page", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html", resp.Header.Get("Content-Type"))
	}

	// 无 token 的 /v1 面形态不变（401 信封——引导页不放宽豁免面）。
	resp2, err := client.Get(base + "/v1/apps")
	if err != nil {
		t.Fatalf("GET /v1/apps: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /v1/apps (no token) = %d, want 401 unchanged", resp2.StatusCode)
	}
}
