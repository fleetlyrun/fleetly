package ingress

// 管理器/端点/证书窗口测试：发布收敛（假 docker 客户端）、token 鉴权
// 负面测试、空视图落 noop 兜底（H9 合法空态 + Spike B「先校验后写」）、
// 路由撤销链路、续期窗口判定、证书落盘/解析。

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
	services    map[string]ingressServiceState
	networks    map[string]bool
	creates     []string
	updates     []string
	updateSpecs []swarm.ServiceSpec
	netEns      []string
	volumes     map[string]bool
	seedID      string
	copiedDirs  []string
	info        swarmInfo
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
	// 记录提交的整份 spec 载荷（网络保留断言用——收敛更新是否保留既有
	// app 挂载只能在载荷上断言）。
	f.updateSpecs = append(f.updateSpecs, spec)
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
	snap, _ := m.vw.snapshot()
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

// TestPublishEmptyViewWithdrawsToFallback 是 H9 前身
// TestPublishEmptyViewKeepsPreviousConfig 的语义反转：最后一个路由移除
// （空声明集）不再是「拒绝换视图、Traefik 保留旧路由（502 残留）」，
// 而是空视图 = noop 兜底形态真实下发——旧路由不在、兜底路由在、台账
// 已清（撤销生效）。
func TestPublishEmptyViewWithdrawsToFallback(t *testing.T) {
	m, _, st := newTestManager(t)
	ctx := context.Background()
	app, _ := st.CreateApp(ctx, "", "solo")
	in := PublishInput{AppID: app.ID, AppName: "solo", Services: []ServiceRoutes{
		{Service: "web", Port: "80", Domains: []string{"solo.example.test"}},
	}}
	if err := m.PublishRoutes(ctx, in); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if snap, _ := m.vw.snapshot(); snap.HTTP.Routers["fleetly-solo-web-web"] == nil {
		t.Fatal("route should be in view after publish")
	}

	// 移除声明（服务删除的发布路径）：空声明集 → 台账清空 → 合成落
	// 兜底 → 发布成功（不再拒绝）。
	err := m.PublishRoutes(ctx, PublishInput{AppID: app.ID, AppName: "solo", Services: []ServiceRoutes{}})
	if err != nil {
		t.Fatalf("publishing an empty route view must succeed via fallback (H9): %v", err)
	}
	snap, _ := m.vw.snapshot()
	if snap.HTTP.Routers["fleetly-solo-web-web"] != nil {
		t.Fatal("previous route must be withdrawn (absent from view)")
	}
	if snap.HTTP.Routers[fallbackRouterName] == nil {
		t.Fatalf("fallback router must be served on empty view: %+v", snap.HTTP.Routers)
	}
	rows, _ := st.ListAppDomains(ctx, app.ID)
	if len(rows) != 0 {
		t.Fatalf("ledger should reflect the (empty) declared set: %+v", rows)
	}
}

// TestWithdrawAppRoutes 撤销链路（app 删除管线的入口，H9）：发布域名
// 路由 → WithdrawAppRoutes → 台账清空 + 视图落兜底；幂等；sweep 的全量
// 重发布（republishAll）对空集成功（不再产生永久 warn 的拒绝路径）。
func TestWithdrawAppRoutes(t *testing.T) {
	m, _, st := newTestManager(t)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "gone")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := m.PublishRoutes(ctx, PublishInput{AppID: app.ID, AppName: "gone", Services: []ServiceRoutes{
		{Service: "web", Port: "80", Domains: []string{"gone.example.test"}},
	}}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if snap, _ := m.vw.snapshot(); snap.HTTP.Routers["fleetly-gone-web-web"] == nil {
		t.Fatal("route should be in view before withdraw")
	}

	// 撤销：台账清 + 视图落兜底。
	if err := m.WithdrawAppRoutes(ctx, app.ID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	rows, _ := st.ListAppDomains(ctx, app.ID)
	if len(rows) != 0 {
		t.Fatalf("ledger rows must be deleted on withdraw: %+v", rows)
	}
	snap, _ := m.vw.snapshot()
	if snap.HTTP.Routers["fleetly-gone-web-web"] != nil {
		t.Fatal("route must be withdrawn from view")
	}
	if snap.HTTP.Routers[fallbackRouterName] == nil {
		t.Fatalf("view must fall back to noop router after withdraw: %+v", snap.HTTP.Routers)
	}

	// 幂等：重复撤销无副作用。
	if err := m.WithdrawAppRoutes(ctx, app.ID); err != nil {
		t.Fatalf("withdraw must be idempotent: %v", err)
	}
	// sweep 路径（republishAll）对空路由集成功——空视图不再被 Validate
	// 拒绝（12h sweep 不再永久 warn）。
	if err := m.republishAll(ctx); err != nil {
		t.Fatalf("republishAll on empty ledger must succeed via fallback: %v", err)
	}
	// 空输入拒绝。
	if err := m.WithdrawAppRoutes(ctx, ""); err == nil {
		t.Fatal("withdraw with empty app id must fail")
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

// TestCertStoreSaveAtomicReplaceAndNoResidue E5：tmp+rename 原子写语义——
// 同名重写换入新内容（rename 覆盖既有目标，Windows 同样成立）、目录内
// 不残留 .tmp 中间文件（半写形态不可能被 Traefik/Load 观测）。
func TestCertStoreSaveAtomicReplaceAndNoResidue(t *testing.T) {
	dir := t.TempDir()
	store := newCertStore(dir)
	certPEM, keyPEM, _ := selfSignedTestCert(t, "first.test")
	pair, err := ParsePair("app-x", []string{"first.test"}, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse pair: %v", err)
	}
	if err := store.Save(pair); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 重写（续期形态）：同名换入新内容。
	certPEM2, keyPEM2, _ := selfSignedTestCert(t, "second.test")
	pair2, err := ParsePair("app-x", []string{"second.test"}, certPEM2, keyPEM2)
	if err != nil {
		t.Fatalf("parse pair2: %v", err)
	}
	if err := store.Save(pair2); err != nil {
		t.Fatalf("re-save (rename over existing): %v", err)
	}
	got, err := store.Load("app-x")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.SHA256 != pair2.SHA256 || !sameDomainSet(got.Domains, pair2.Domains) {
		t.Fatalf("content after re-save = %+v, want the second pair (rename must replace)", got)
	}
	// 无 .tmp 残留。
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temp file residue after atomic save: %v", leftovers)
	}
}

// TestCertStoreSaveDetectsTampering E5 损坏注入：回读内容与内存值 sha256
// 不一致 → Save 显式报错（兑现注释承诺；调用方按既有重试语义承接）。
func TestCertStoreSaveDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	store := newCertStore(dir)
	// 回读出口注入篡改（磁盘/写路径损坏形态）。
	store.readFile = func(path string) ([]byte, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		tampered := append([]byte{}, raw...)
		tampered[0] ^= 0xff
		return tampered, nil
	}
	certPEM, keyPEM, _ := selfSignedTestCert(t, "tamper.test")
	pair, err := ParsePair("tamper", []string{"tamper.test"}, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse pair: %v", err)
	}
	err = store.Save(pair)
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("tampered read-back must fail Save with verification error, got %v", err)
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

// TestEnsureTraefikUpdatePreservesAttachedNetworks 收敛更新保留全部既有
// app 网络挂载（架构评审 H8）：spec 漂移（如镜像钉版变更）触发的更新不得
// 清空 TaskTemplate.Networks——Traefik 必须同时挂在所有 app 网络上才能
// 反代各 app 容器；一次漂移收敛断网 = 全路由 502，只有各 app 再部署才逐个
// 恢复的实机回归钉死。
func TestEnsureTraefikUpdatePreservesAttachedNetworks(t *testing.T) {
	m, dc, st := newTestManager(t)
	ctx := context.Background()
	// 预置：两个 app 各自接入网络（经真实发布路径建起多 app 挂载态）。
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
	// 制造漂移：实况镜像与期望钉版不一致（如镜像升级后的收敛场景）。
	svc := dc.services[IngressServiceName]
	svc.Image = "traefik:v3.4"
	dc.services[IngressServiceName] = svc
	if err := m.EnsureTraefik(ctx); err != nil {
		t.Fatalf("converge traefik: %v", err)
	}
	// 更新已提交，且载荷是期望镜像（证明这是漂移收敛更新而非 attach）。
	last := dc.updateSpecs[len(dc.updateSpecs)-1]
	if last.TaskTemplate.ContainerSpec.Image != DefaultTraefikImage {
		t.Fatalf("update payload image = %s, want %s", last.TaskTemplate.ContainerSpec.Image, DefaultTraefikImage)
	}
	// 核心断言：两个既有网络仍在提交载荷里、且无新增（以实况为基准合并）。
	got := map[string]bool{}
	for _, n := range last.TaskTemplate.Networks {
		got[n.Target] = true
	}
	if len(got) != 2 || !got["netid-fleetly-app1-net"] || !got["netid-fleetly-app2-net"] {
		t.Fatalf("update payload networks = %v, want exactly both attached app nets", last.TaskTemplate.Networks)
	}
	// 服务实况同构：整组替换后两网络仍在（底座视角未断网）。
	if nets := dc.services[IngressServiceName].Networks; len(nets) != 2 {
		t.Fatalf("service networks after converge = %v, want both preserved", nets)
	}
}

// TestEnsureTraefikUpdateWithoutNetworks 对照位：实况无任何挂载时收敛
// 更新不引入 Networks 字段（首次创建后未 attach 的行为不变位）。
func TestEnsureTraefikUpdateWithoutNetworks(t *testing.T) {
	m, dc, _ := newTestManager(t)
	ctx := context.Background()
	if err := m.EnsureTraefik(ctx); err != nil {
		t.Fatalf("create traefik: %v", err)
	}
	// 制造漂移：实况镜像与期望钉版不一致。
	svc := dc.services[IngressServiceName]
	svc.Image = "traefik:v3.4"
	dc.services[IngressServiceName] = svc
	if err := m.EnsureTraefik(ctx); err != nil {
		t.Fatalf("converge traefik: %v", err)
	}
	last := dc.updateSpecs[len(dc.updateSpecs)-1]
	if len(last.TaskTemplate.Networks) != 0 {
		t.Fatalf("update payload networks = %v, want none when service has no attachments", last.TaskTemplate.Networks)
	}
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
