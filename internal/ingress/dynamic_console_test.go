package ingress

// console 免端口直访段（2026-09-24）的合成面测试：BackendURL 覆写（宿主
// 进程后端不走「键:端口」swarm DNS 公式）+ RootRedirect 根路径 302 中间件
// （双入口）+ 覆写 serversTransport（TLS IP 后端跳过服务器认证）+ Validate
// 的悬空中间件引用拒绝（Spike B 纪律同 services 面）。

import (
	"strings"
	"testing"
	"time"
)

func TestSynthesizeConsoleDirectRoute(t *testing.T) {
	certPEM, keyPEM, _ := selfSignedTestCert(t, "console.example.test")
	routes := []Route{{
		App:  platformCertApp,
		Service: "console",
		Domains:  []string{"console.example.test"},
		Name:     consoleRouterName,
		BackendURL:   "https://10.124.0.3:8420",
		Transport:    consoleTransportName,
		RootRedirect: "/ui/",
		Cert:         &CertificateRef{App: platformCertApp, SHA256: "aa", NotAfter: time.Now().Add(24 * time.Hour).UnixNano(), CertPEM: certPEM, KeyPEM: keyPEM},
	}}
	cfg := Synthesize(routes)

	// 后端覆写 + transport 引用与定义。
	svc := cfg.HTTP.Services[consoleRouterName]
	if svc == nil || svc.LoadBalancer == nil || len(svc.LoadBalancer.Servers) != 1 {
		t.Fatalf("console service missing: %+v", cfg.HTTP.Services)
	}
	if got := svc.LoadBalancer.Servers[0].URL; got != "https://10.124.0.3:8420" {
		t.Fatalf("console backend url = %s, want explicit BackendURL", got)
	}
	if got := svc.LoadBalancer.ServersTransport; got != consoleTransportName+"@http" {
		t.Fatalf("console serversTransport ref = %s", got)
	}
	st := cfg.HTTP.ServersTransports[consoleTransportName]
	if st == nil || !st.InsecureSkipVerify {
		t.Fatalf("insecure console transport missing: %+v", cfg.HTTP.ServersTransports)
	}

	// 根路径重定向：双入口 router + Path(`/`) 精确匹配 + 中间件引用与载荷。
	root := cfg.HTTP.Routers[consoleRouterName+"-root-websecure"]
	if root == nil || root.TLS == nil || !strings.Contains(root.Rule, "Path(`/`)") ||
		!strings.Contains(root.Rule, "Host(`console.example.test`)") {
		t.Fatalf("root redirect router missing/malformed: %+v", root)
	}
	if len(root.Middlewares) != 1 || root.Middlewares[0] != consoleRouterName+"-root-redirect" {
		t.Fatalf("root router middlewares = %v", root.Middlewares)
	}
	if cfg.HTTP.Routers[consoleRouterName+"-root-web"] == nil {
		t.Fatalf("root redirect web-entrypoint router missing")
	}
	mw := cfg.HTTP.Middlewares[consoleRouterName+"-root-redirect"]
	if mw == nil || mw.RedirectRegex == nil {
		t.Fatalf("root redirect middleware missing: %+v", cfg.HTTP.Middlewares)
	}
	if mw.RedirectRegex.Regex != `^(https?://[^/]+)/?$` || mw.RedirectRegex.Replacement != "${1}/ui/" {
		t.Fatalf("redirect payload = %+v", mw.RedirectRegex)
	}
	if mw.RedirectRegex.Permanent {
		t.Fatalf("redirect must be 302 (non-permanent), got permanent")
	}

	// 主路由（Host-only）与根路由并存；整体过 Validate。
	if cfg.HTTP.Routers[consoleRouterName+"-web"] == nil || cfg.HTTP.Routers[consoleRouterName+"-websecure"] == nil {
		t.Fatalf("console host routers missing: %+v", cfg.HTTP.Routers)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// 悬空中间件引用拒绝（坏 router 会被 Traefik 连同整份配置应用——校验
	// 责任在控制面，Spike B 纪律的 services 同款扩展）。
	cfg.HTTP.Routers[consoleRouterName+"-root-web"].Middlewares = []string{"nonexistent"}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "nonexistent middleware") {
		t.Fatalf("dangling middleware reference must be rejected, got: %v", err)
	}
}
