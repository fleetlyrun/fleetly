package ingress

// 动态配置合成与非空校验测试（Spike B 四态纪律的回归——硬约束）：
//   - 裸 {} 会清空 Traefik 全部路由（实测 404）→ 合成器与发布路径双重
//     保证「routers 键存在且非空」（空路由集由 noop 兜底路由承载，H9）；
//   - 显式空 map 被 Traefik 拒绝（保留旧配置）→ 视图未发布时端点应答
//     该形态（等价「不动」语义）；
//   - 坏 router 会被连同整份配置应用 → 悬空引用先校验拒绝
//     （Traefik 内建 @internal 引用豁免）。

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestSynthesizeMapsRoutesDeterministically(t *testing.T) {
	routes := []Route{
		{App: "demo", Service: "web", Port: "8080", Domains: []string{"b.example.test", "a.example.test"}},
	}
	cfg := Synthesize(routes)
	if cfg.HTTP == nil || cfg.HTTP.Routers == nil || cfg.HTTP.Services == nil {
		t.Fatalf("routers/services keys must always exist (Spike B discipline)")
	}
	router := cfg.HTTP.Routers["fleetly-demo-web-web"]
	if router == nil {
		t.Fatalf("web router missing: %+v", cfg.HTTP.Routers)
	}
	// 域名集合按归一化形态进 Host 规则（compose 层已排序——这里不再重排，
	// 声明顺序即规则顺序）。
	if !strings.Contains(router.Rule, "Host(`a.example.test`)") ||
		!strings.Contains(router.Rule, "Host(`b.example.test`)") {
		t.Fatalf("host rule malformed: %s", router.Rule)
	}
	if router.EntryPoints[0] != "web" {
		t.Fatalf("web router entrypoint = %v", router.EntryPoints)
	}
	// 服务映射：VIP + expose 端口 + 平台默认 serversTransport（V4 依据）。
	svc := cfg.HTTP.Services["fleetly-demo-web"]
	if svc == nil || svc.LoadBalancer == nil || len(svc.LoadBalancer.Servers) != 1 {
		t.Fatalf("service mapping missing: %+v", cfg.HTTP.Services)
	}
	if got := svc.LoadBalancer.Servers[0].URL; got != "http://fleetly-demo-web:8080" {
		t.Fatalf("server url = %s, want http://fleetly-demo-web:8080", got)
	}
	if svc.LoadBalancer.ServersTransport != defaultServersTransportName+"@http" {
		t.Fatalf("serversTransport ref = %s", svc.LoadBalancer.ServersTransport)
	}
	st := cfg.HTTP.ServersTransports[defaultServersTransportName]
	if st == nil || st.ForwardingTimeouts == nil ||
		st.ForwardingTimeouts.IdleConnTimeout != (15*time.Second).String() {
		t.Fatalf("default serversTransport (idleConnTimeout=15s, V4) missing: %+v", st)
	}
}

// TestSynthesizeTLSSegmentWhenCertReady E1-2 内联分发：证书就绪的 app 产
// 出 443 路由 + tls.certificates 段，certFile/keyFile 键携带内联 PEM 全文
// （V-MN 实测修正后的 FileOrContent 契约；设计原 certContent/keyContent
// 键已证伪）。指纹断言：载荷中的证书与输入证书 DER 指纹一致（Traefik 443
// 所服证书由此载荷决定——spike 证据的测试面等价形态）。
func TestSynthesizeTLSSegmentWhenCertReady(t *testing.T) {
	certPEM, keyPEM, _ := selfSignedTestCert(t, "shop.example.test")
	routes := []Route{
		{App: "shop", Service: "web", Port: "80", Domains: []string{"shop.example.test"},
			Cert: &CertificateRef{App: "shop", SHA256: "aa", NotAfter: 1, CertPEM: certPEM, KeyPEM: keyPEM}},
	}
	cfg := Synthesize(routes)
	if cfg.HTTP.Routers["fleetly-shop-web-websecure"] == nil {
		t.Fatalf("443 router missing when cert ready")
	}
	if cfg.HTTP.Routers["fleetly-shop-web-websecure"].TLS == nil {
		t.Fatalf("443 router must enable tls")
	}
	if cfg.TLS == nil || len(cfg.TLS.Certificates) != 1 {
		t.Fatalf("tls.certificates segment missing: %+v", cfg.TLS)
	}
	cert := cfg.TLS.Certificates[0]
	if cert.CertFile != string(certPEM) {
		t.Fatalf("certFile must be the inline PEM verbatim, got %.80q", cert.CertFile)
	}
	if cert.KeyFile != string(keyPEM) {
		t.Fatalf("keyFile must be the inline PEM verbatim, got %.40q", cert.KeyFile)
	}
	// 指纹级比对（spike 证据形态）：certFile 值解析出的证书 DER sha256 ==
	// 输入证书链首块 DER sha256。
	block, _ := pem.Decode([]byte(cert.CertFile))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("certFile must decode to a CERTIFICATE PEM block")
	}
	gotSum := sha256.Sum256(block.Bytes)
	wantBlock, _ := pem.Decode(certPEM)
	if wantBlock == nil {
		t.Fatalf("input cert PEM must decode")
	}
	wantSum := sha256.Sum256(wantBlock.Bytes)
	if gotSum != wantSum {
		t.Fatalf("inline cert fingerprint mismatch: got %x want %x", gotSum, wantSum)
	}
	// HTTP 路由并存（无重定向设计外行为）。
	if cfg.HTTP.Routers["fleetly-shop-web-web"] == nil {
		t.Fatalf("80 router missing when cert ready")
	}
	// 序列化键名契约钉死（FileOrContent 修正项——V-MN 实测：未知键使
	// 整份动态配置 decode 被拒）。
	raw, err := json.Marshal(cfg.TLS)
	if err != nil {
		t.Fatalf("marshal tls segment: %v", err)
	}
	if !strings.Contains(string(raw), `"certFile"`) || !strings.Contains(string(raw), `"keyFile"`) {
		t.Fatalf("tls payload keys must be certFile/keyFile: %s", raw)
	}
}

// TestSynthesizeSkipsCertRefWithoutPEM 防御位：Cert 引用缺 PEM（不可能形
// 态：发布路径只对证书库 Load 成功的 app 挂 Cert）不产出半张证书的 TLS
// 段——半张证书比整份配置被 Traefik 拒绝更糟。
func TestSynthesizeSkipsCertRefWithoutPEM(t *testing.T) {
	routes := []Route{
		{App: "ghost", Service: "web", Port: "80", Domains: []string{"ghost.example.test"},
			Cert: &CertificateRef{App: "ghost", SHA256: "aa", NotAfter: 1}},
	}
	cfg := Synthesize(routes)
	if cfg.TLS != nil {
		t.Fatalf("cert ref without PEM must not enter the TLS segment: %+v", cfg.TLS)
	}
	if cfg.HTTP.Routers["fleetly-ghost-web-websecure"] != nil {
		t.Fatal("443 router must not be emitted without distributable cert material")
	}
}

// TestValidateRejectsEmpty 裸 {} 回归（Spike B b2：合法空载荷清空全部路由）。
func TestValidateRejectsEmpty(t *testing.T) {
	// 形态 1：完全空载荷（裸 {}）。
	bare := &DynamicConfig{}
	if err := Validate(bare); err == nil {
		t.Fatal("bare {} payload must be rejected")
	}
	// 形态 2：反序列化自字面 {}。
	var decoded DynamicConfig
	if err := json.Unmarshal([]byte(`{}`), &decoded); err != nil {
		t.Fatalf("decode bare {}: %v", err)
	}
	if err := Validate(&decoded); err == nil {
		t.Fatal("decoded bare {} must be rejected")
	}
	// 形态 3：键在值空（Traefik 保留语义的形态，但平台发布路径同样拒绝
	// ——「空配置不落盘/不换视图」）。
	empty := EmptyConfig()
	if err := Validate(empty); err == nil {
		t.Fatal("explicit empty routers/services must be rejected on publish path")
	}
	// 形态 4：零路由集的合成结果（最后一个服务移除）——语义已反转（H9）：
	// 合成器产出 noop 兜底路由，「空合成」恒过 Validate（撤销即真实下发
	// 空态，而非拒绝后残留旧路由）。形态钉死见
	// TestSynthesizeEmptyRouteSetEmitsNoopFallback。
	synthesized := Synthesize(nil)
	if err := Validate(synthesized); err != nil {
		t.Fatalf("synthesized empty route set must pass via fallback router: %v", err)
	}
	// 且合成结果永远不是裸 {}（键恒存在——http 段非 nil）。
	if synthesized.HTTP == nil || synthesized.HTTP.Routers == nil || synthesized.HTTP.Services == nil {
		t.Fatal("synthesizer must always emit http.routers/services keys")
	}
}

// TestSynthesizeEmptyRouteSetEmitsNoopFallback 钉死空路由集的合成产物
// JSON 形态（H9 合法空态）：单条 fleetly-fallback router（.invalid 保留
// TLD + noop@internal 内建引用），services 键在值空，serversTransport
// 平台默认恒注入。该形态使 Traefik 收到合法非空配置 → 旧路由被真实撤销。
func TestSynthesizeEmptyRouteSetEmitsNoopFallback(t *testing.T) {
	cfg := Synthesize(nil)
	if len(cfg.HTTP.Routers) != 1 {
		t.Fatalf("empty route set must synthesize exactly the fallback router: %+v", cfg.HTTP.Routers)
	}
	fb := cfg.HTTP.Routers[fallbackRouterName]
	if fb == nil {
		t.Fatalf("fallback router missing: %+v", cfg.HTTP.Routers)
	}
	if fb.Rule != "Host(`fleetly.invalid`)" {
		t.Fatalf("fallback rule = %q, want Host(`fleetly.invalid`)", fb.Rule)
	}
	if fb.Service != noopServiceRef {
		t.Fatalf("fallback service = %q, want %s", fb.Service, noopServiceRef)
	}
	if len(cfg.HTTP.Services) != 0 || cfg.HTTP.Services == nil {
		t.Fatalf("fallback services must be present-but-empty map: %+v", cfg.HTTP.Services)
	}
	// 序列化形态钉死（Traefik HTTP provider 消费的载荷契约）。
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal fallback config: %v", err)
	}
	want := `{"http":{"routers":{"fleetly-fallback":{"rule":"Host(` + "`fleetly.invalid`" + `)",` +
		`"entryPoints":["web","websecure"],"service":"noop@internal"}},` +
		`"services":{},"serversTransports":{"fleetly-default":{"maxIdleConnsPerHost":8,` +
		`"forwardingTimeouts":{"dialTimeout":"5s","idleConnTimeout":"15s"}}}}}`
	if string(raw) != want {
		t.Fatalf("fallback payload mismatch:\n got: %s\nwant: %s", raw, want)
	}
	// Validate 恒过（发布路径不拒绝空合成）。
	if err := Validate(cfg); err != nil {
		t.Fatalf("fallback config must pass Validate: %v", err)
	}
}

func TestValidateRejectsDanglingRouterRef(t *testing.T) {
	// Spike B c1：坏 router 会被连同整份配置应用——悬空引用先校验拒绝。
	cfg := &DynamicConfig{HTTP: &HTTPDynamic{
		Routers: map[string]*Router{
			"r1": {Rule: "Host(`x.test`)", EntryPoints: []string{"web"}, Service: "ghost"},
		},
		Services: map[string]*DynamicService{
			"s1": {LoadBalancer: &LoadBalancer{Servers: []Server{{URL: "http://x:80"}}}},
		},
	}}
	if err := Validate(cfg); err == nil {
		t.Fatal("dangling router→service ref must be rejected")
	}
	// 空 LB server URL。
	cfg2 := &DynamicConfig{HTTP: &HTTPDynamic{
		Routers: map[string]*Router{
			"r1": {Rule: "Host(`x.test`)", EntryPoints: []string{"web"}, Service: "s1"},
		},
		Services: map[string]*DynamicService{
			"s1": {LoadBalancer: &LoadBalancer{Servers: []Server{{URL: ""}}}},
		},
	}}
	if err := Validate(cfg2); err == nil {
		t.Fatal("empty server URL must be rejected")
	}
}

func TestViewChallengeOverlayAndServedRevision(t *testing.T) {
	v := newView("http://10.0.0.5:8422")
	v.setRoutes([]Route{{App: "demo", Service: "web", Port: "80", Domains: []string{"d.test"}}})

	// 无挑战：快照 = 纯路由。
	if snap, _ := v.snapshot(); snap.HTTP.Routers[acmeChallengeRouterName] != nil {
		t.Fatal("challenge router must be absent without in-flight challenge")
	}
	// 注入挑战：挑战路由叠加 + 优先级压过 host 路由 + 应答端点指向控制面。
	rev := v.addChallenge("tok-1", "token.thumbprint")
	if v.currentRevision() < rev {
		t.Fatalf("revision must advance on challenge add: %d < %d", v.currentRevision(), rev)
	}
	snap, _ := v.snapshot()
	cr := snap.HTTP.Routers[acmeChallengeRouterName]
	if cr == nil || cr.Priority != 1000 {
		t.Fatalf("challenge router missing or priority wrong: %+v", cr)
	}
	cs := snap.HTTP.Services[acmeChallengeServiceName]
	if cs == nil || cs.LoadBalancer.Servers[0].URL != "http://10.0.0.5:8422" {
		t.Fatalf("challenge responder URL wrong: %+v", cs)
	}
	// 清理挑战：路由消失。
	v.removeChallenge("tok-1")
	if snap, _ := v.snapshot(); snap.HTTP.Routers[acmeChallengeRouterName] != nil {
		t.Fatal("challenge router must be removed after CleanUp")
	}
	// awaitServed：servedRevision 追上后收敛（E2：markServed 以快照 rev 入参）。
	_, rev2 := v.snapshot()
	v.markServed(rev2)
	if !v.awaitServed(rev2, 10*1000*1000) {
		t.Fatal("awaitServed should return immediately once served >= rev")
	}
}

// TestViewUnpublishedServesExplicitEmpty 未发布视图（routes 为 nil——控制面
// 重启后的首拍窗口）应答显式空 map：Traefik 拒绝并保留其侧既有配置，
// 重启本身不制造路由撤销窗口（Spike B 实测语义；区别于已发布的空集）。
func TestViewUnpublishedServesExplicitEmpty(t *testing.T) {
	v := newView("")
	snap, _ := v.snapshot()
	if snap.HTTP == nil || snap.HTTP.Routers == nil || len(snap.HTTP.Routers) != 0 {
		t.Fatalf("unpublished view must serve explicit empty map: %+v", snap.HTTP)
	}
	if snap.HTTP.Routers[fallbackRouterName] != nil {
		t.Fatal("unpublished view must NOT serve the fallback (never published = keep-last-good)")
	}
}

// TestViewEmptyRoutesMergesChallenge 空视图（已发布的合法空态，H9）与
// ACME 挑战路由共存：兜底路由与挑战路由/服务同盘（最后一个域名撤销期间
// 仍有 app 在签证书的合法场景）。
func TestViewEmptyRoutesMergesChallenge(t *testing.T) {
	v := newView("http://10.0.0.5:8422")
	v.setRoutes([]Route{}) // 已发布的空路由集（合法空态）
	v.addChallenge("tok-1", "token.thumbprint")
	snap, _ := v.snapshot()
	if snap.HTTP.Routers[fallbackRouterName] == nil {
		t.Fatalf("fallback router missing on published empty view: %+v", snap.HTTP.Routers)
	}
	if snap.HTTP.Routers[acmeChallengeRouterName] == nil {
		t.Fatalf("challenge router missing alongside fallback: %+v", snap.HTTP.Routers)
	}
	cs := snap.HTTP.Services[acmeChallengeServiceName]
	if cs == nil || cs.LoadBalancer.Servers[0].URL != "http://10.0.0.5:8422" {
		t.Fatalf("challenge responder service wrong: %+v", cs)
	}
}
