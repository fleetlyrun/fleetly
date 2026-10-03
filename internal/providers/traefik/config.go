package traefik

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// traefik HTTP provider 动态配置 schema（v3；字段名与 traefik 官方动态
// 配置一致——这份 JSON 是 Provider 私有翻译产物，不进 IR）。

type dynamicConfig struct {
	HTTP *httpConfig `json:"http,omitempty"`
	TCP  *tcpConfig  `json:"tcp,omitempty"`
}

type httpConfig struct {
	Routers  map[string]httpRouter  `json:"routers,omitempty"`
	Services map[string]httpService `json:"services,omitempty"`
}

type httpRouter struct {
	Rule    string     `json:"rule"`
	Service string     `json:"service"`
	TLS     *routerTLS `json:"tls,omitempty"`
}

type routerTLS struct {
	CertResolver string      `json:"certResolver,omitempty"`
	Domains      []tlsDomain `json:"domains,omitempty"`
}

type tlsDomain struct {
	Main string `json:"main"`
}

type httpService struct {
	LoadBalancer loadBalancer `json:"loadBalancer"`
}

type loadBalancer struct {
	Servers []server `json:"servers"`
}

type server struct {
	URL string `json:"url"`
}

type tcpConfig struct {
	Routers  map[string]tcpRouter  `json:"routers,omitempty"`
	Services map[string]tcpService `json:"services,omitempty"`
}

type tcpRouter struct {
	Rule    string    `json:"rule"`
	Service string    `json:"service"`
	TLS     *struct{} `json:"tls,omitempty"`
}

type tcpService struct {
	LoadBalancer loadBalancer `json:"loadBalancer"`
}

// buildDynamicConfig 把全量 Route 集翻译为 traefik 动态配置（确定性输出：
// 键排序，golden 钉死）。
func buildDynamicConfig(routes []capability.Route) ([]byte, error) {
	cfg := &dynamicConfig{}
	for _, r := range routes {
		if r.BackendAddr == "" {
			return nil, fmt.Errorf("traefik config: route %s%s has no resolved backend address", r.Host, r.Path)
		}
		// 纵深防线（安全批 P0）：host/path 会原样内插进反引号定界的规则串
		//（Host(`%s`) / PathPrefix(`%s`) / HostSNI(`%s`)），白名单外的形态
		//（反引号/空白/控制字符）可注入规则。存量行绕过受理面校验的兜底：
		// 非法路由跳过并留错误日志（engine publishRoutes 的"backend
		// unresolved, skipping route"同款先例），不阻断其余路由发布。
		if err := capability.ValidateRouteHost(r.Host); err != nil {
			slog.Error("traefik config: invalid route host, skipping route", "host", r.Host, "err", err)
			continue
		}
		if err := capability.ValidateRoutePath(r.Path); err != nil {
			slog.Error("traefik config: invalid route path, skipping route", "host", r.Host, "path", r.Path, "err", err)
			continue
		}
		name := routeKey(r)
		switch r.Protocol {
		case capability.ProtocolTCP:
			if cfg.TCP == nil {
				cfg.TCP = &tcpConfig{Routers: map[string]tcpRouter{}, Services: map[string]tcpService{}}
			}
			cfg.TCP.Routers[name] = tcpRouter{
				Rule:    fmt.Sprintf("HostSNI(`%s`)", r.Host),
				Service: name,
				TLS:     &struct{}{},
			}
			cfg.TCP.Services[name] = tcpService{LoadBalancer: loadBalancer{
				Servers: []server{{URL: "tcp://" + r.BackendAddr}},
			}}
		default:
			if cfg.HTTP == nil {
				cfg.HTTP = &httpConfig{Routers: map[string]httpRouter{}, Services: map[string]httpService{}}
			}
			rule := fmt.Sprintf("Host(`%s`)", r.Host)
			if r.Path != "" && r.Path != "/" {
				rule += fmt.Sprintf(" && PathPrefix(`%s`)", r.Path)
			}
			router := httpRouter{Rule: rule, Service: name}
			if r.TLS == "auto" || r.TLS == "" {
				router.TLS = &routerTLS{CertResolver: "le", Domains: []tlsDomain{{Main: r.Host}}}
			}
			cfg.HTTP.Routers[name] = router
			// 后端协议：h2c = 明文 HTTP/2（messageloop 形态），http 同为
			// 明文；两态均经集群内网，无需 https 后端。
			scheme := "http"
			if r.Protocol == capability.ProtocolH2C {
				scheme = "h2c"
			}
			cfg.HTTP.Services[name] = httpService{LoadBalancer: loadBalancer{
				Servers: []server{{URL: scheme + "://" + r.BackendAddr}},
			}}
		}
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // traefik 规则里的 & 不转义（可读性；JSON 等价）
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// emptyDynamicConfig 是空集形态（不含任何 router/service；仍为全量配置
// 对象——拉取端点永不返回裸 {}，防配置清空事故）。
func emptyDynamicConfig() []byte {
	return []byte("{\n  \"http\": {\n    \"routers\": {},\n    \"services\": {}\n  }\n}\n")
}

// routeKey 生成稳定 router/service 键（host-path 归一 + 原始键短哈希）。
//
// 单射守卫（Q-10）：归一化清洗会把不同 (host,path) 撞成同键——如
// ("a.b","/c") 与 ("a.b-c","") 都清洗为 "a-b-c"——traefik 的 router/service
// 是同名 map，撞键即静默互覆（后路由吃掉前路由）。恒追加原始 (host,path)
// 的 FNV-64a 低 48 位十六进制后缀：不同输入必不同键（哈希撞仅在 2^24 量级
// 路由数下需要考虑），键仍落在 traefik 名字的 DNS 安全字符集
// [a-z0-9-] 内，且纯函数确定性（同输入同键，重启/重发布不漂）。
func routeKey(r capability.Route) string {
	key := r.Host
	if r.Path != "" && r.Path != "/" {
		key += "-" + strings.Trim(r.Path, "/")
	}
	normalized := strings.NewReplacer(".", "-", "*", "-", "/", "-", "_", "-").Replace(key)
	// hash.Hash 约定 Write 永不返回错（fnv 实现恒 nil），检错无处置面。
	h := fnv.New64a()
	_, _ = h.Write([]byte(r.Host))
	_, _ = h.Write([]byte{0}) // 分隔符：("a","b/c") 与 ("a/b","c") 不共哈希
	_, _ = h.Write([]byte(r.Path))
	return fmt.Sprintf("%s-%012x", normalized, h.Sum64()&0xFFFFFFFFFFFF)
}
