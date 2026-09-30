package traefik

import (
	"encoding/json"
	"fmt"
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

// routeKey 生成稳定 router/service 键（host-path 归一）。
func routeKey(r capability.Route) string {
	key := r.Host
	if r.Path != "" && r.Path != "/" {
		key += "-" + strings.Trim(r.Path, "/")
	}
	return strings.NewReplacer(".", "-", "*", "-", "/", "-", "_", "-").Replace(key)
}
