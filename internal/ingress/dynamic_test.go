package ingress

// 动态配置合成与非空校验测试（Spike B 四态纪律的回归——硬约束）：
//   - 裸 {} 会清空 Traefik 全部路由（实测 404）→ 合成器与发布路径双重
//     保证「routers/services 键存在且非空」；
//   - 显式空 map 被 Traefik 拒绝（保留旧配置）→ 视图未发布时端点应答
//     该形态（等价「不动」语义）；
//   - 坏 router 会被连同整份配置应用 → 悬空引用先校验拒绝。

import (
	"encoding/json"
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

func TestSynthesizeTLSSegmentWhenCertReady(t *testing.T) {
	routes := []Route{
		{App: "shop", Service: "web", Port: "80", Domains: []string{"shop.example.test"},
			Cert: &CertificateRef{App: "shop", SHA256: "aa", NotAfter: 1}},
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
	if cfg.TLS.Certificates[0].CertFile != TraefikCertMountPath+"/shop.crt" ||
		cfg.TLS.Certificates[0].KeyFile != TraefikCertMountPath+"/shop.key" {
		t.Fatalf("cert file refs wrong: %+v", cfg.TLS.Certificates[0])
	}
	// HTTP 路由并存（无重定向设计外行为）。
	if cfg.HTTP.Routers["fleetly-shop-web-web"] == nil {
		t.Fatalf("80 router missing when cert ready")
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
	// 形态 4：零路由集的合成结果（最后一个服务移除）。
	synthesized := Synthesize(nil)
	if err := Validate(synthesized); err == nil {
		t.Fatal("synthesized empty route set must be rejected")
	}
	// 且合成结果永远不是裸 {}（键恒存在——http 段非 nil）。
	if synthesized.HTTP == nil || synthesized.HTTP.Routers == nil || synthesized.HTTP.Services == nil {
		t.Fatal("synthesizer must always emit http.routers/services keys")
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
	if v.snapshot().HTTP.Routers[acmeChallengeRouterName] != nil {
		t.Fatal("challenge router must be absent without in-flight challenge")
	}
	// 注入挑战：挑战路由叠加 + 优先级压过 host 路由 + 应答端点指向控制面。
	rev := v.addChallenge("tok-1", "token.thumbprint")
	if v.currentRevision() < rev {
		t.Fatalf("revision must advance on challenge add: %d < %d", v.currentRevision(), rev)
	}
	snap := v.snapshot()
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
	if v.snapshot().HTTP.Routers[acmeChallengeRouterName] != nil {
		t.Fatal("challenge router must be removed after CleanUp")
	}
	// awaitServed：servedRevision 追上后收敛。
	v.markServed()
	if !v.awaitServed(v.currentRevision(), 10*1000*1000) {
		t.Fatal("awaitServed should return immediately once served >= rev")
	}
}
