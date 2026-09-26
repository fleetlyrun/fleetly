package ingress

// E1/E3 验收测试（S19）：
//   - E1：签发互斥——并发 ensureCertificate 恒单次 Obtain（假签发器计数），
//     -race 覆盖 m.user 缓存的锁纪律；
//   - E3：续期扫描的 appID 解析失败（路由集快照与解析之间 app 被删的竞态
//     终态）跳过该 app，不空串下传。

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/registration"

	"github.com/fleetlyrun/fleetly/internal/state"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// newACMETestManager 构造 ACME 启用 + 假签发器（obtainFn 注入缝，E1）的
// 测试管理器：注册 sidecar 预置（ensureAccount 不发起注册网络调用）、
// RenewBefore=1h（24h 自签证书落盘后即健康——第二轮调用必须命中缓存而
// 非再次签发）。返回值 calls 是 Obtain 计数器。
func newACMETestManager(t *testing.T) (*Manager, *state.Store, *atomic.Int64) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	enabled := true
	cfg := Config{
		TokenFile:         filepath.Join(dir, "ingress.token"),
		CertDir:           filepath.Join(dir, "certs"),
		ConfigAdvertiseIP: "127.0.0.1",
		ACME:              ACMEConfig{Enabled: &enabled, CADirURL: "http://unused.test/dir"},
		RenewBefore:       time.Hour,
	}
	m := NewManagerWithDocker(cfg, st, newFakeDocker(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	// 注册 sidecar 预置：reg URI 非空 → ensureAccount 跳过注册（不触网）。
	// 词形按读取面（json.Unmarshal 进 registration.Resource——URI 是顶层
	// 字段）。
	if err := os.MkdirAll(filepath.Join(dir, "certs"), 0o750); err != nil {
		t.Fatalf("mkdir cert dir: %v", err)
	}
	sidecar := filepath.Join(dir, "certs", "acme-account.key.json")
	if err := os.WriteFile(sidecar, []byte(`{"uri":"https://ca.test/reg/1"}`), 0o600); err != nil {
		t.Fatalf("seed account sidecar: %v", err)
	}

	certPEM, keyPEM, _ := selfSignedTestCert(t, "acme.test")
	var calls atomic.Int64
	m.obtainFn = func(_ context.Context, _ string, _ registration.User, _ []string) ([]byte, []byte, error) {
		calls.Add(1)
		return certPEM, keyPEM, nil
	}
	return m, st, &calls
}

// TestEnsureCertificateConcurrentSingleObtain E1：并发 ensureCertificate
// （engine PublishRoutes 与 Run sweep 两路调用方的并发形态）——签发互斥下
// 恰好一次 Obtain，其余调用方重读证书库命中刚落盘的证书（LE duplicate
// 限额不被打）；-race 同时覆盖 m.user 读写的锁纪律。
func TestEnsureCertificateConcurrentSingleObtain(t *testing.T) {
	m, st, calls := newACMETestManager(t)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "demo")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := st.CreateAppDomain(ctx, app.ID, state.DomainInput{
		Domain: "demo.example.test", Service: "web", Port: "80", Protocol: "http", CertMode: "http01",
	}); err != nil {
		t.Fatalf("seed domains: %v", err)
	}

	const k = 4
	var wg sync.WaitGroup
	type result struct {
		ref *CertificateRef
		err error
	}
	out := make(chan result, k)
	for i := 0; i < k; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := m.ensureCertificate(ctx, app.ID, "demo", []string{"demo.example.test"}, false)
			out <- result{ref: ref, err: err}
		}()
	}
	wg.Wait()
	close(out)
	for r := range out {
		if r.err != nil {
			t.Fatalf("ensureCertificate: %v", r.err)
		}
		if r.ref == nil || r.ref.SHA256 == "" {
			t.Fatalf("nil or empty ref: %+v", r.ref)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("obtain calls = %d, want exactly 1 (issuance mutex must serialize; losers must hit the fresh cert)", n)
	}
	// 台账登记恰好发生（首个调用方的 SetDomainCert 成功路径；E3 语义的
	// 对照面：appID 非 empty 时不落空）。
	rows, err := st.ListAppDomains(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListAppDomains: %v", err)
	}
	if len(rows) != 1 || rows[0].CertSHA256 == "" {
		t.Fatalf("ledger cert not registered: %+v", rows)
	}
}

// TestRenewDueSkipsUnresolvableApp E3：appID 解析失败（renewDue 的路由集
// 快照与 GetAppByName 之间 app 被删——以快照终态直接驱动 renewDueByApp）
// → 跳过该 app（不再空串 appID 下传：SetDomainCert 落空 + 每域名误
// warn）；可解析 app 正常续期。
func TestRenewDueSkipsUnresolvableApp(t *testing.T) {
	m, st, calls := newACMETestManager(t)
	ctx := context.Background()
	live, err := testsupport.SeedAppE(t, st, "live")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := st.CreateAppDomain(ctx, live.ID, state.DomainInput{
		Domain: "live.example.test", Service: "web", Port: "80", Protocol: "http", CertMode: "http01",
	}); err != nil {
		t.Fatalf("seed domains: %v", err)
	}

	// ghost：域名在快照内、名称已不可解析（app 行已删的竞态终态）。
	if err := m.renewDueByApp(ctx, map[string][]string{
		"ghost": {"ghost.example.test"},
		"live":  {"live.example.test"},
	}); err != nil {
		t.Fatalf("renewDueByApp: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("obtain calls = %d, want exactly 1 (unresolvable app must be skipped, not issued with empty id)", n)
	}
	rows, err := st.ListAppDomains(ctx, live.ID)
	if err != nil {
		t.Fatalf("ListAppDomains: %v", err)
	}
	if len(rows) != 1 || rows[0].CertSHA256 == "" {
		t.Fatalf("live app cert not registered: %+v", rows)
	}
}

// TestConvergeAppDomainsIssuesHTTP01CertificateForAPICreatedDomain 守卫⑤的
// 代码链取证（IMPL-T1-1；staging 真机复验单列）：API 形态写入的域名行走
// ConvergeAppDomains → app 证书签发的 HTTP-01 SAN 集 = state 行域名（路径
// 不回退），签发后行上登记 cert_sha256。
func TestConvergeAppDomainsIssuesHTTP01CertificateForAPICreatedDomain(t *testing.T) {
	m, st, _ := newACMETestManager(t)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "acmeapi")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	const domain = "api.acmeapi.example.test"
	if _, err := st.CreateAppDomain(ctx, app.ID, state.DomainInput{
		Domain: domain, Service: "web", Port: "8080", Protocol: "http", CertMode: "http01",
	}); err != nil {
		t.Fatalf("create domain: %v", err)
	}
	certPEM, keyPEM, _ := selfSignedTestCert(t, domain)
	var issued []string
	m.obtainFn = func(_ context.Context, _ string, _ registration.User, domains []string) ([]byte, []byte, error) {
		issued = append([]string{}, domains...)
		return certPEM, keyPEM, nil
	}
	if err := m.ConvergeAppDomains(ctx, app.ID); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if len(issued) != 1 || issued[0] != domain {
		t.Fatalf("HTTP-01 SAN set = %v, want [%s]", issued, domain)
	}
	rows, err := st.ListAppDomains(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListAppDomains: %v", err)
	}
	if len(rows) != 1 || rows[0].CertSHA256 == "" {
		t.Fatalf("cert ledger row not stamped after issuance: %+v", rows)
	}
}
