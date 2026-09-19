package ingress

// Traefik 动态配置（HTTP provider 载荷）的合成、非空校验与序列化。
//
// Spike B 四态实测（spike/b/README.md §6，硬约束）：
//   - 裸 {} 载荷会清空 Traefik 全部路由（实测 404）；
//   - 显式空 map {"http":{"routers":{},"services":{}}} 被 Traefik 拒绝
//     （decode 错误）并保留旧配置；
//   - 坏 router 会被连同整份配置一起应用（Traefik 不替平台拦截）。
//
// 因此「非空 + 引用一致」校验的责任在控制面：Validate 拒绝键缺失/合成
// 结果为空/悬空引用；发布路径（Manager.publish）校验不过不换视图——
// Traefik 端永远只能取到通过校验的全量配置。
//
// 「合法空态」的载体（H9，方案 docs/design/2026-09-20-remediation-complete.md
// §1/S13）：Spike B 两拒（裸 {} 清空、显式空 map 被拒保留旧配置）之外，
// 空路由集的撤销语义由常驻兜底 router 承载——合成器在路由集为空时产出
// 单条指向 noop@internal 的兜底路由，使「空合成」恒为 Traefik 可接受的
// 非空配置（旧路由被真实撤销，而非 502 残留）。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DynamicConfig 是 Traefik 动态配置载荷（只承载平台产出的子集；字段名
// 对齐 Traefik v3 动态配置 JSON 契约）。
type DynamicConfig struct {
	HTTP *HTTPDynamic `json:"http,omitempty"`
	TLS  *TLSDynamic  `json:"tls,omitempty"`
}

// HTTPDynamic 是 http 段（routers/services 键恒在——空 map 也显式写出，
// 让「缺键」形态在载荷层面不可能出现）。
type HTTPDynamic struct {
	Routers           map[string]*Router           `json:"routers"`
	Services          map[string]*DynamicService   `json:"services"`
	ServersTransports map[string]*ServersTransport `json:"serversTransports,omitempty"`
}

// Router 是一条路由规则。
type Router struct {
	Rule        string   `json:"rule"`
	EntryPoints []string `json:"entryPoints"`
	Service     string   `json:"service"`
	// Priority 显式优先级（ACME 挑战路由用——压过 host 路由的默认
	// 规则长度优先级）。
	Priority int        `json:"priority,omitempty"`
	TLS      *RouterTLS `json:"tls,omitempty"`
}

// RouterTLS 非 nil 即启用该路由的 TLS（443 入口路由携带）。
type RouterTLS struct{}

// DynamicService 是一个负载均衡服务（VIP 形态：单 server 指向
// fleetly-<app>-<service> 的 overlay VIP + expose 端口）。
type DynamicService struct {
	LoadBalancer *LoadBalancer `json:"loadBalancer"`
}

// LoadBalancer 是 LB 配置（serversTransport 恒引用平台默认连接治理参数，
// V4 依据见 config.go）。
type LoadBalancer struct {
	Servers          []Server `json:"servers"`
	ServersTransport string   `json:"serversTransport,omitempty"`
}

// Server 是一个后端。
type Server struct {
	URL string `json:"url"`
}

// ServersTransport 是连接治理参数集（V4 辅助治理；缺省
// fleetly-default 由平台写死，不暴露配置面）。
type ServersTransport struct {
	MaxIdleConnsPerHost int                 `json:"maxIdleConnsPerHost,omitempty"`
	ForwardingTimeouts  *ForwardingTimeouts `json:"forwardingTimeouts,omitempty"`
}

// ForwardingTimeouts 是转发超时（字符串时长形态——Spike B 脚本同款，
// Traefik v3 HTTP provider 实测接受）。
type ForwardingTimeouts struct {
	DialTimeout     string `json:"dialTimeout,omitempty"`
	IdleConnTimeout string `json:"idleConnTimeout,omitempty"`
}

// TLSDynamic 是 tls 段：证书集中下发（tls.certificates 文件引用形态，
// 文件经证书目录只读挂载到达 Traefik）。
type TLSDynamic struct {
	Certificates []TLSCertificate `json:"certificates,omitempty"`
}

// TLSCertificate 是一张证书的文件引用（容器内路径）。
type TLSCertificate struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

// Route 是一条应用路由（app 的一个入口服务；domains 已归一化）。
type Route struct {
	App     string
	Service string
	// Port 是后端目标端口（compose expose 首端口）。
	Port string
	// Domains 是该服务的域名集（归一化 compose spec 产出）。
	Domains []string
	// Cert 标记该 app 证书是否已就绪（就绪则 443 路由 + tls.certificates
	// 下发；未就绪只发 80 路由）。
	Cert *CertificateRef
}

// CertificateRef 是一张已落库证书的引用（文件名 = app 名；sha256 与到期
// 时间随台账登记）。
type CertificateRef struct {
	App      string
	SHA256   string
	NotAfter int64 // UnixNano
}

// defaultServersTransportName 是平台默认 serversTransport 键。
const defaultServersTransportName = "fleetly-default"

// 兜底路由常量（H9 合法空态）：路由集为空时的常驻路由——
//   - host 取 .invalid 保留 TLD（RFC 2606，永不可能被真实域名命中）；
//   - service 引用 Traefik v3 内建 noop@internal（无外部端点依赖、零流量
//     命中；以 @internal 限定名引用即 Traefik 官方的内建元素引用形态，
//     不进 services map）。
const (
	fallbackRouterName = "fleetly-fallback"
	fallbackHost       = "fleetly.invalid"
	noopServiceRef     = "noop@internal"
)

// RouterName 返回 app 服务对应的 router/service 键（fleetly-<app>-<service>
// ——与 Swarm 服务名同形，键空间唯一且可读）。
func RouterName(app, service string) string {
	return "fleetly-" + app + "-" + service
}

// Synthesize 从全量路由集合成动态配置（确定性：路由按 app/service 字典
// 序；域名已由 compose 层归一化排序）。routers/services 键恒存在；
// serversTransport 平台默认恒注入（V4 连接治理参数）。
func Synthesize(routes []Route) *DynamicConfig {
	cfg := &DynamicConfig{HTTP: &HTTPDynamic{
		Routers:  map[string]*Router{},
		Services: map[string]*DynamicService{},
		ServersTransports: map[string]*ServersTransport{
			defaultServersTransportName: {
				MaxIdleConnsPerHost: 8,
				ForwardingTimeouts: &ForwardingTimeouts{
					DialTimeout:     durationString(DefaultServersTransportDialTimeout),
					IdleConnTimeout: durationString(DefaultServersTransportIdleTimeout),
				},
			},
		},
	}}
	certs := map[string]CertificateRef{}
	for _, r := range routes {
		if r.Cert != nil {
			certs[r.App] = *r.Cert
		}
	}
	for _, r := range routes {
		name := RouterName(r.App, r.Service)
		rule := hostRuleOf(r.Domains)
		// 80 入口（web）：无证书时的唯一入口；有证书时与 443 并存
		//（HTTP/HTTPS 同服，重定向不做——v0.1 不发明设计外行为）。
		cfg.HTTP.Routers[name+"-web"] = &Router{
			Rule:        rule,
			EntryPoints: []string{"web"},
			Service:     name,
		}
		if r.Cert != nil {
			cfg.HTTP.Routers[name+"-websecure"] = &Router{
				Rule:        rule,
				EntryPoints: []string{"websecure"},
				Service:     name,
				TLS:         &RouterTLS{},
			}
		}
		backend := name
		if r.Port != "" {
			backend = name + ":" + r.Port
		}
		cfg.HTTP.Services[name] = &DynamicService{
			LoadBalancer: &LoadBalancer{
				Servers:          []Server{{URL: "http://" + backend}},
				ServersTransport: defaultServersTransportName + "@http",
			},
		}
	}
	if len(certs) > 0 {
		tls := &TLSDynamic{}
		for _, app := range sortedCertApps(certs) {
			tls.Certificates = append(tls.Certificates, TLSCertificate{
				CertFile: TraefikCertMountPath + "/" + app + ".crt",
				KeyFile:  TraefikCertMountPath + "/" + app + ".key",
			})
		}
		cfg.TLS = tls
	}
	// 空路由集 → 常驻兜底路由（H9）：services 留空 map（键恒在），routers
	// 携带唯一一条 noop@internal 引用——Traefik 收到的是合法非空配置，旧
	// 路由被真实撤销（对比：显式空 map 被拒 → 旧路由 502 残留）。挂全部
	// 入口（web/websecure）与平台路由形态一致，命中率为零。
	if len(routes) == 0 {
		cfg.HTTP.Routers[fallbackRouterName] = &Router{
			Rule:        hostRuleOf([]string{fallbackHost}),
			EntryPoints: []string{"web", "websecure"},
			Service:     noopServiceRef,
		}
	}
	return cfg
}

// Validate 执行 Spike B 纪律校验（先校验后写）：
//  1. http.routers 键必须存在且非空（裸 {} 与显式空 map 双拒——空合成
//     由兜底路由承载，恒过本条）；
//  2. http.services 键必须存在（nil map 序列化为 null，同样是缺键形态；
//     值可以为空 map——只有内建引用的兜底形态合法，见 isInternalService）；
//  3. 每个 router 引用的 service 必须存在（悬空引用拒绝——坏 router 会被
//     Traefik 连同整份配置应用；Traefik 内建服务 @internal 限定名豁免）；
//  4. 每个 service 必须有非空 server URL。
func Validate(cfg *DynamicConfig) error {
	if cfg == nil || cfg.HTTP == nil {
		return fmt.Errorf("ingress: 动态配置缺 http 段（拒绝下发，Spike B 纪律）")
	}
	if len(cfg.HTTP.Routers) == 0 {
		// 键缺失（nil map）与空 map 同判——Spike B 纪律：键必须存在且
		// 合成非空（nil map 序列化为 null，同样是 Traefik 无法接受的形态）。
		return fmt.Errorf("ingress: http.routers 缺失或为空（拒绝下发；空配置会清空 Traefik 全部路由）")
	}
	if cfg.HTTP.Services == nil {
		return fmt.Errorf("ingress: http.services 键缺失（nil 序列化为 null，拒绝下发）")
	}
	for name, r := range cfg.HTTP.Routers {
		if r == nil || r.Rule == "" || r.Service == "" {
			return fmt.Errorf("ingress: router %s 形态不完整（rule/service 必填）", name)
		}
		if isInternalService(r.Service) {
			continue // Traefik 内建服务（noop@internal）：不由平台 services map 承载
		}
		if _, ok := cfg.HTTP.Services[r.Service]; !ok {
			return fmt.Errorf("ingress: router %s 引用不存在的 service %s（悬空引用拒绝）", name, r.Service)
		}
	}
	for name, s := range cfg.HTTP.Services {
		if s == nil || s.LoadBalancer == nil || len(s.LoadBalancer.Servers) == 0 {
			return fmt.Errorf("ingress: service %s 无后端（空 LB 拒绝）", name)
		}
		for i, srv := range s.LoadBalancer.Servers {
			if srv.URL == "" {
				return fmt.Errorf("ingress: service %s 第 %d 个后端 URL 为空", name, i)
			}
		}
	}
	return nil
}

// MarshalJSONBytes 序列化载荷（缩进关闭——Traefik 轮询高频路径的最小形）。
func MarshalJSONBytes(cfg *DynamicConfig) ([]byte, error) {
	return json.Marshal(cfg)
}

// hostRuleOf 合成 Host 匹配规则：Host(`d1`) || Host(`d2`)。
func hostRuleOf(domains []string) string {
	parts := make([]string, 0, len(domains))
	for _, d := range domains {
		parts = append(parts, "Host(`"+d+"`)")
	}
	return strings.Join(parts, " || ")
}

// isInternalService 判定是否 Traefik 内建服务引用（@internal 限定名——
// Traefik 官方的内建元素引用形态，如 noop@internal；不进平台 services
// map，悬空引用校验豁免）。
func isInternalService(service string) bool {
	return strings.HasSuffix(service, "@internal")
}

// sortedCertApps 返回证书 app 名的字典序（tls.certificates 确定性）。
func sortedCertApps(certs map[string]CertificateRef) []string {
	apps := make([]string, 0, len(certs))
	for app := range certs {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	return apps
}

// durationString 是 Traefik 动态配置的时长序列化形态（Go duration 字符串，
// Spike B 实测接受）。
func durationString(d time.Duration) string {
	return d.String()
}
