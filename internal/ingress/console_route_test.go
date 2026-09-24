package ingress

// platformConsoleRoute 的门控与形态测试（2026-09-24）：base_domain 门 /
// advertise 未定缺席 / 明文与 TLS 两态后端 / withPlatformRoutes 集成。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPlatformConsoleRoute(t *testing.T) {
	m, _, _ := newTestManager(t)
	m.SetConfigPort(8420)

	// base_domain 空：直访段缺席（ConfigTLSEnEnabled 门——单节点 v0.1 形态
	// 零变化）。
	if _, ok := m.platformConsoleRoute(); ok {
		t.Fatal("console route must be absent without base_domain")
	}
	if got := m.withPlatformRoutes(context.Background(), nil); len(got) != 0 {
		t.Fatalf("platform routes without base_domain = %v, want none", got)
	}

	m.cfg.BaseDomain = "example.test"
	// advertise 未定（EnsureTraefik 未跑过）：无地址可指，不追加——duty
	// 收敛链在 EnsureTraefik 之后重发布，最终一致。
	if _, ok := m.platformConsoleRoute(); ok {
		t.Fatal("console route must be absent before advertise is known")
	}

	m.advertiseIP = "10.124.0.3"
	r, ok := m.platformConsoleRoute()
	if !ok || r.Name != consoleRouterName || r.Service != "console" {
		t.Fatalf("console route missing: %+v ok=%v", r, ok)
	}
	// 单节点明文形态：http 后端、无 transport 覆写、根路径 302 到 /ui/。
	if r.BackendURL != "http://10.124.0.3:8420" || r.Transport != "" || r.RootRedirect != "/ui/" {
		t.Fatalf("plaintext form = %+v", r)
	}
	if r.App != platformCertApp {
		t.Fatalf("console route app = %q, want platform cert reserved name", r.App)
	}

	// 平台证书在盘：https 后端 + 跳过服务器认证的专用 transport（IP 端点
	// 无 SAN——providerEndpoint F9 修订二同口径）。
	certPEM, keyPEM, _ := selfSignedTestCert(t, "console.example.test")
	if err := m.certs.Save(&CertificatePair{
		App:      platformCertApp,
		Domains:  m.PlatformDomains(),
		CertPEM:  certPEM,
		KeyPEM:   keyPEM,
		NotAfter: time.Now().Add(24 * time.Hour).UTC(),
	}); err != nil {
		t.Fatalf("seed platform cert: %v", err)
	}
	r, _ = m.platformConsoleRoute()
	if r.BackendURL != "https://10.124.0.3:8420" || r.Transport != consoleTransportName {
		t.Fatalf("tls form = %+v", r)
	}

	// withPlatformRoutes 集成：registry 与 console 两段都在（s3 关闭态）。
	got := m.withPlatformRoutes(context.Background(), nil)
	names := make([]string, 0, len(got))
	for _, r := range got {
		names = append(names, r.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, RegistryServiceName) || !strings.Contains(joined, consoleRouterName) {
		t.Fatalf("platform routes = %v, want registry + console", names)
	}
}
