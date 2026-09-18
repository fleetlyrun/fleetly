package ingress

// 管理器/端点/证书窗口测试：发布收敛（假 docker 客户端）、token 鉴权
// 负面测试、空视图不换视图（Spike B「先校验后写」）、续期窗口判定、
// 证书落盘/解析。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// fakeDocker 是 dockerClient 的假实现（服务/网络/卷/seed 容器内存态 +
// 调用记录）。
type fakeDocker struct {
	services   map[string]ingressServiceState
	networks   map[string]bool
	creates    []string
	updates    []string
	netEns     []string
	volumes    map[string]bool
	seedID     string
	copiedDirs []string
	info       swarmInfo
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		services: map[string]ingressServiceState{},
		networks: map[string]bool{},
		volumes:  map[string]bool{},
		info:     swarmInfo{SwarmActive: true, NodeAddr: "127.0.0.1"},
	}
}

func (f *fakeDocker) Info(context.Context) (swarmInfo, error) { return f.info, nil }

func (f *fakeDocker) ServiceInspect(_ context.Context, name string) (ingressServiceState, error) {
	return f.services[name], nil
}

func (f *fakeDocker) ServiceCreate(_ context.Context, spec swarm.ServiceSpec) error {
	f.creates = append(f.creates, spec.Name)
	f.services[spec.Name] = ingressServiceState{
		Exists:  true,
		Version: 1,
		Image:   spec.TaskTemplate.ContainerSpec.Image,
		Args:    append([]string{}, spec.TaskTemplate.ContainerSpec.Args...),
		Ports:   append([]swarm.PortConfig{}, spec.EndpointSpec.Ports...),
		Mounts:  append([]mount.Mount{}, spec.TaskTemplate.ContainerSpec.Mounts...),
		HealthTest: func() []string {
			if spec.TaskTemplate.ContainerSpec.Healthcheck != nil {
				return spec.TaskTemplate.ContainerSpec.Healthcheck.Test
			}
			return nil
		}(),
	}
	return nil
}

func (f *fakeDocker) ServiceUpdate(_ context.Context, name string, _ uint64, spec swarm.ServiceSpec) error {
	f.updates = append(f.updates, name)
	cur := f.services[name]
	cur.Version++
	// 同构底座语义：TaskTemplate.Networks 是整组替换（非追加）。
	cur.Networks = netTargets(spec)
	f.services[name] = cur
	return nil
}

func netTargets(spec swarm.ServiceSpec) []string {
	out := []string{}
	for _, n := range spec.TaskTemplate.Networks {
		out = append(out, n.Target)
	}
	return out
}

func (f *fakeDocker) NetworkEnsure(_ context.Context, name string) error {
	f.netEns = append(f.netEns, name)
	f.networks[name] = true
	return nil
}

func (f *fakeDocker) NetworkID(_ context.Context, name string) (string, error) {
	// 同构底座语义：ID 形态与名字不同（swarm 归一），幂等判据走 ID。
	return "netid-" + name, nil
}

func (f *fakeDocker) VolumeEnsure(_ context.Context, name string) error {
	f.volumes[name] = true
	return nil
}

func (f *fakeDocker) SeedContainerEnsure(_ context.Context, _, _, _, _ string) (string, error) {
	if f.seedID == "" {
		f.seedID = "seed-ctr-1"
	}
	return f.seedID, nil
}

func (f *fakeDocker) CopyToDir(_ context.Context, _, dir string, _ []FileEntry) error {
	f.copiedDirs = append(f.copiedDirs, dir)
	return nil
}

// newTestManager 构造 ACME 关闭的测试管理器（独立 token/cert 目录）。
func newTestManager(t *testing.T) (*Manager, *fakeDocker, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dc := newFakeDocker()
	enabled := false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		TokenFile:         filepath.Join(dir, "ingress.token"),
		CertDir:           filepath.Join(dir, "certs"),
		ConfigAdvertiseIP: "127.0.0.1",
		ACME:              ACMEConfig{Enabled: &enabled, CADirURL: "http://unused.test/dir"},
	}
	m := NewManagerWithDocker(cfg, st, dc, log)
	return m, dc, st
}

func TestPublishRoutesConvergesTraefikAndView(t *testing.T) {
	m, dc, st := newTestManager(t)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "demo")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	in := PublishInput{AppID: app.ID, AppName: "demo", Services: []ServiceRoutes{
		{Service: "web", Port: "8080", Domains: []string{"test.example.internal"}},
	}}
	if err := m.PublishRoutes(ctx, in); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Traefik 收敛：一次创建、参数含 HTTP provider 端点与 token header。
	if len(dc.creates) != 1 || dc.creates[0] != IngressServiceName {
		t.Fatalf("traefik create calls = %v", dc.creates)
	}
	svc := dc.services[IngressServiceName]
	if svc.Image != DefaultTraefikImage {
		t.Fatalf("traefik image = %s, want pinned %s", svc.Image, DefaultTraefikImage)
	}
	endpointArg := ""
	for _, a := range svc.Args {
		if strings.HasPrefix(a, "--providers.http.endpoint=") {
			endpointArg = a
		}
	}
	if !strings.Contains(endpointArg, "http://127.0.0.1:8422/configs") {
		t.Fatalf("provider endpoint arg = %q", endpointArg)
	}
	authArg := ""
	for _, a := range svc.Args {
		if strings.HasPrefix(a, "--providers.http.headers.Authorization=") {
			authArg = a
		}
	}
	if !strings.HasPrefix(authArg, "--providers.http.headers.Authorization=Bearer ") {
		t.Fatalf("token header arg = %q", authArg)
	}
	// host 80/443 端口形态。
	if len(svc.Ports) != 2 {
		t.Fatalf("traefik ports = %+v", svc.Ports)
	}
	// 证书卷挂载（volume 形态、只读、挂到 /fleetly-certs）+ 证书卷与
	// seed 容器已收敛。
	if len(svc.Mounts) != 1 || svc.Mounts[0].Source != "fleetly-ingress-certs" ||
		svc.Mounts[0].Target != TraefikCertMountPath || !svc.Mounts[0].ReadOnly {
		t.Fatalf("traefik cert mount wrong: %+v", svc.Mounts)
	}
	if !dc.volumes["fleetly-ingress-certs"] {
		t.Fatal("cert volume not ensured")
	}

	// app 网络接入（一次 update）。
	if len(dc.updates) != 1 {
		t.Fatalf("expected 1 network-attach update, got %v", dc.updates)
	}

	// 视图：路由已合成（带 port）。
	snap := m.vw.snapshot()
	if snap.HTTP.Routers["fleetly-demo-web-web"] == nil {
		t.Fatalf("route not in view: %+v", snap.HTTP.Routers)
	}
	if got := snap.HTTP.Services["fleetly-demo-web"].LoadBalancer.Servers[0].URL; got != "http://fleetly-demo-web:8080" {
		t.Fatalf("server url = %s", got)
	}

	// 台账已同步。
	rows, _ := st.ListAppDomains(ctx, app.ID)
	if len(rows) != 1 || rows[0].Domain != "test.example.internal" || rows[0].Port != "8080" {
		t.Fatalf("ledger rows = %+v", rows)
	}

	// 幂等重发布：无新建、无多余更新。
	before := len(dc.creates) + len(dc.updates)
	if err := m.PublishRoutes(ctx, in); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if after := len(dc.creates) + len(dc.updates); after != before {
		t.Fatalf("idempotent republish changed calls: %d -> %d", before, after)
	}
}

// TestPublishEmptyViewKeepsPreviousConfig 复现 Spike B 纪律：最后一个
// 路由移除时合成结果为空 → 拒绝换视图（Traefik 侧保留旧配置；空配置
// 不落库/不出控制面）。
func TestPublishEmptyViewKeepsPreviousConfig(t *testing.T) {
	m, _, st := newTestManager(t)
	ctx := context.Background()
	app, _ := st.CreateApp(ctx, "", "solo")
	in := PublishInput{AppID: app.ID, AppName: "solo", Services: []ServiceRoutes{
		{Service: "web", Port: "80", Domains: []string{"solo.example.test"}},
	}}
	if err := m.PublishRoutes(ctx, in); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if m.vw.snapshot().HTTP.Routers["fleetly-solo-web-web"] == nil {
		t.Fatal("route should be in view after publish")
	}

	// 移除声明（服务删除的发布路径）：空声明集 → 台账清空 → 合成空 →
	// 校验拒绝 → 视图保持上一份好配置。
	err := m.PublishRoutes(ctx, PublishInput{AppID: app.ID, AppName: "solo", Services: []ServiceRoutes{}})
	if err == nil {
		t.Fatal("publishing an empty route view must fail (Spike B: keep last good config)")
	}
	snap := m.vw.snapshot()
	if snap.HTTP.Routers["fleetly-solo-web-web"] == nil {
		t.Fatal("previous good config must be retained after rejected empty publish")
	}
	rows, _ := st.ListAppDomains(ctx, app.ID)
	if len(rows) != 0 {
		t.Fatalf("ledger should reflect the (empty) declared set: %+v", rows)
	}
}

// TestProviderEndpointTokenAuth 配置端点鉴权负面测试（验收 3：带错
// token 401）+ 挑战应答面 + /healthz。
func TestProviderEndpointTokenAuth(t *testing.T) {
	m, _, _ := newTestManager(t)
	v := m.vw
	v.setRoutes([]Route{{App: "demo", Service: "web", Port: "80", Domains: []string{"d.test"}}})
	handler := newProviderHandler(v, "secret-token")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// 无 token → 401。
	resp, err := http.Get(srv.URL + "/configs")
	if err != nil {
		t.Fatalf("get /configs: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token /configs = %d, want 401", resp.StatusCode)
	}
	// 错 token → 401。
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/configs", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /configs wrong token: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token /configs = %d, want 401", resp.StatusCode)
	}
	// 对 token → 200 + 合法动态配置 JSON。
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/configs", nil)
	req2.Header.Set("Authorization", "Bearer secret-token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("get /configs ok token: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("ok-token /configs = %d, want 200", resp2.StatusCode)
	}
	raw, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(raw), `"routers"`) || !strings.Contains(string(raw), `"services"`) {
		t.Fatalf("config payload missing keys: %s", raw)
	}
	// /healthz 无鉴权 200。
	resp3, err := http.Get(srv.URL + "/healthz")
	if err != nil || resp3.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %v/%d", err, resp3.StatusCode)
	}
	_ = resp3.Body.Close()
	// 挑战应答：在途 token 命中、未知 404。
	v.addChallenge("challenge-token", "challenge-token.thumbprint")
	resp4, err := http.Get(srv.URL + "/.well-known/acme-challenge/challenge-token")
	if err != nil {
		t.Fatalf("challenge fetch: %v", err)
	}
	body, _ := io.ReadAll(resp4.Body)
	_ = resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK || string(body) != "challenge-token.thumbprint" {
		t.Fatalf("challenge response = %d %q", resp4.StatusCode, body)
	}
	resp5, err := http.Get(srv.URL + "/.well-known/acme-challenge/unknown")
	if err != nil {
		t.Fatalf("unknown challenge: %v", err)
	}
	_ = resp5.Body.Close()
	if resp5.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown challenge = %d, want 404", resp5.StatusCode)
	}
}

// TestNeedsRenewalWindow 续期窗口判定（30 天窗）：缺失/域名集变化/临期
// 重签，健康证书跳过。
func TestNeedsRenewalWindow(t *testing.T) {
	now := time.Now().UTC()
	if !needsRenewal(nil, []string{"a.test"}, now, 30*24*time.Hour) {
		t.Fatal("nil cert needs issuance")
	}
	// 域名集变化（多 SAN 单证书契约）。
	pair := &CertificatePair{Domains: []string{"a.test"}, NotAfter: now.Add(90 * 24 * time.Hour)}
	if !needsRenewal(pair, []string{"a.test", "b.test"}, now, 30*24*time.Hour) {
		t.Fatal("domain set change must trigger re-issue")
	}
	// 进入窗口（还剩 10 天 < 30 天窗）。
	pair2 := &CertificatePair{Domains: []string{"a.test"}, NotAfter: now.Add(10 * 24 * time.Hour)}
	if !needsRenewal(pair2, []string{"a.test"}, now, 30*24*time.Hour) {
		t.Fatal("cert within renewal window must be renewed")
	}
	// 窗外（还剩 40 天）。
	pair3 := &CertificatePair{Domains: []string{"a.test"}, NotAfter: now.Add(40 * 24 * time.Hour)}
	if needsRenewal(pair3, []string{"a.test"}, now, 30*24*time.Hour) {
		t.Fatal("healthy cert outside window must NOT be renewed")
	}
}

// TestCertStoreRoundTripAndParse 证书库落盘/回读/解析（合成叶证书）。
func TestCertStoreRoundTripAndParse(t *testing.T) {
	dir := t.TempDir()
	store := newCertStore(dir)
	certPEM, keyPEM, notAfter := selfSignedTestCert(t, "round.test")
	pair, err := ParsePair("round", []string{"round.test"}, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse pair: %v", err)
	}
	if pair.SHA256 == "" || len(pair.SHA256) != 64 {
		t.Fatalf("sha256 malformed: %q", pair.SHA256)
	}
	if !pair.NotAfter.Equal(notAfter) {
		t.Fatalf("not after = %v, want %v", pair.NotAfter, notAfter)
	}
	if err := store.Save(pair); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load("round")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.SHA256 != pair.SHA256 || !sameDomainSet(got.Domains, pair.Domains) {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if _, err := store.Load("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cert should surface os.ErrNotExist, got %v", err)
	}
}

// TestAttachNetworkKeepsPreviousApps 多 app 网络回归：逐个接入两个 app
// 的网络后，实况网络集必须同时包含两者（不得互相覆盖——实机验证发现的
// 回归，T2.15 修复钉死）。
func TestAttachNetworkKeepsPreviousApps(t *testing.T) {
	m, dc, st := newTestManager(t)
	ctx := context.Background()
	for _, name := range []string{"app1", "app2"} {
		appRow, err := st.CreateApp(ctx, "", name)
		if err != nil {
			t.Fatalf("create app: %v", err)
		}
		if err := m.PublishRoutes(ctx, PublishInput{AppID: appRow.ID, AppName: name, Services: []ServiceRoutes{
			{Service: "web", Port: "80", Domains: []string{name + ".example.test"}},
		}}); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
	}
	nets := dc.services[IngressServiceName].Networks
	has := func(s string) bool {
		for _, n := range nets {
			if n == s {
				return true
			}
		}
		return false
	}
	// 底座把 attach 目标归一为 ID（fake 同构：netid-<name>）——按 ID 断言。
	if !has("netid-fleetly-app1-net") || !has("netid-fleetly-app2-net") {
		t.Fatalf("networks after two app attaches = %v, want both app nets present", nets)
	}
	// 幂等重发布（同一 app）：attach 以 ID 判等——不再产生更新/重复项。
	before := len(dc.updates)
	if err := m.PublishRoutes(ctx, PublishInput{AppID: mustApp1(t, st).ID, AppName: "app1", Services: []ServiceRoutes{
		{Service: "web", Port: "80", Domains: []string{"app1.example.test"}},
	}}); err != nil {
		t.Fatalf("republish app1: %v", err)
	}
	if after := len(dc.updates); after != before {
		t.Fatalf("idempotent attach produced %d extra updates", after-before)
	}
	// 网络目标无重复项（ID 判等防重复 attach，实机回归钉死）。
	seen := map[string]int{}
	for _, n := range dc.services[IngressServiceName].Networks {
		seen[n]++
		if seen[n] > 1 {
			t.Fatalf("duplicate network attachment %s (attach must be idempotent)", n)
		}
	}
}

// mustApp1 是测试内 app 行取回（幂等重发布段的输入）。
func mustApp1(t *testing.T, st *state.Store) state.App {
	t.Helper()
	app, err := st.GetAppByName(context.Background(), "app1")
	if err != nil {
		t.Fatalf("get app1: %v", err)
	}
	return app
}

// TestSyncCertToVolume 证书卷同步：crt/key 经 seed 容器拷入挂载目录。
func TestSyncCertToVolume(t *testing.T) {
	m, dc, _ := newTestManager(t)
	certPEM, keyPEM, _ := selfSignedTestCert(t, "sync.test")
	pair, err := ParsePair("sync", []string{"sync.test"}, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse pair: %v", err)
	}
	if err := m.syncCertToVolume(context.Background(), pair); err != nil {
		t.Fatalf("sync cert to volume: %v", err)
	}
	if dc.seedID == "" {
		t.Fatal("seed container not ensured")
	}
	if len(dc.copiedDirs) != 1 || dc.copiedDirs[0] != TraefikCertMountPath {
		t.Fatalf("copy destinations = %v, want [%s]", dc.copiedDirs, TraefikCertMountPath)
	}
}

// TestTokenLoadOrGenerateStable token 持久化：首启生成、重启复用同值。
func TestTokenLoadOrGenerateStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ingress.token")
	first, created, err := tokenLoadOrGenerate(path)
	if err != nil || !created {
		t.Fatalf("first load: created=%t err=%v", created, err)
	}
	second, created2, err := tokenLoadOrGenerate(path)
	if err != nil || created2 {
		t.Fatalf("second load should reuse: created=%t err=%v", created2, err)
	}
	if first != second || len(first) < 32 {
		t.Fatalf("token not stable/generated: %q vs %q", first, second)
	}
}

// selfSignedTestCert 生成自签测试证书（返回 PEM 与 NotAfter）。
func selfSignedTestCert(t *testing.T, domain string) ([]byte, []byte, time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, tmpl.NotAfter
}
