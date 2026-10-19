package traefik

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// traefik HTTP provider 动态配置 schema（v3；字段名与 traefik 官方动态
// 配置一致——这份 JSON 是 Provider 私有翻译产物，不进 IR）。

type dynamicConfig struct {
	HTTP *httpConfig `json:"http,omitempty"`
	TCP  *tcpConfig  `json:"tcp,omitempty"`
}

type httpConfig struct {
	Routers     map[string]httpRouter     `json:"routers,omitempty"`
	Services    map[string]httpService    `json:"services,omitempty"`
	Middlewares map[string]httpMiddleware `json:"middlewares,omitempty"`
}

type httpRouter struct {
	Rule        string     `json:"rule"`
	Service     string     `json:"service"`
	Middlewares []string   `json:"middlewares,omitempty"`
	TLS         *routerTLS `json:"tls,omitempty"`
}

// httpMiddleware 是 traefik 中间件声明的受管子集（ADR-0051 forwardAuth
// ——browse 会话路由的请求门禁；ADR-0061 rateLimit——console 登录限速；
// 其余形态不入受管面）。
type httpMiddleware struct {
	ForwardAuth *forwardAuth `json:"forwardAuth,omitempty"`
	RateLimit   *rateLimit   `json:"rateLimit,omitempty"`
}

// forwardAuth 指向平台 authorize 端点（每请求校验会话 cookie；traefik
// 透传原始请求头——含 Cookie，2026-10-07 v3.6 实证）。
type forwardAuth struct {
	Address string `json:"address"`
}

// rateLimit 是 traefik rateLimit 中间件的受管形态（ADR-0061）：每源 IP
// 固定窗限速，窗恒 1 分钟。
type rateLimit struct {
	Average int    `json:"average"`
	Period  string `json:"period"`
	Burst   int    `json:"burst"`
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
		// host 统一小写（R07-M2）：traefik Host 匹配按小写请求 host，大写
		// 规则值永不命中；构造面归一同时覆盖存量大写行（不迁移数据）。
		host := capability.NormalizeRouteHost(r.Host)
		name := routeKey(r)
		switch r.Protocol {
		case capability.ProtocolTCP:
			if cfg.TCP == nil {
				cfg.TCP = &tcpConfig{Routers: map[string]tcpRouter{}, Services: map[string]tcpService{}}
			}
			cfg.TCP.Routers[name] = tcpRouter{
				Rule:    fmt.Sprintf("HostSNI(`%s`)", host),
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
			rule := fmt.Sprintf("Host(`%s`)", host)
			if r.Path != "" && r.Path != "/" {
				rule += fmt.Sprintf(" && PathPrefix(`%s`)", r.Path)
			}
			router := httpRouter{Rule: rule, Service: name}
			if r.TLS == "auto" || r.TLS == "" {
				router.TLS = &routerTLS{CertResolver: "le", Domains: []tlsDomain{{Main: host}}}
			}
			var mws []string
			if r.Auth != nil {
				// ForwardAuth 门禁（ADR-0051 决策 5）：browse 会话路由每请求
				// 校验会话 cookie；中间件与 router 同键派生（单射守卫同源）。
				if err := capability.ValidateRouteAuthAddress(r.Auth.Address); err != nil {
					slog.Error("traefik config: invalid route auth address, skipping route",
						"host", r.Host, "path", r.Path, "err", err)
					continue
				}
				mwName := name + "-fw"
				mws = append(mws, mwName)
				if cfg.HTTP.Middlewares == nil {
					cfg.HTTP.Middlewares = map[string]httpMiddleware{}
				}
				cfg.HTTP.Middlewares[mwName] = httpMiddleware{ForwardAuth: &forwardAuth{Address: r.Auth.Address}}
			}
			if r.RateLimit != nil {
				// 登录限速（ADR-0061 决策 3）：每源 IP 固定窗；声明非法同
				// Auth 口径——跳过该路由并留错误日志，不阻断其余发布。
				if err := capability.ValidateRouteRateLimit(r.RateLimit); err != nil {
					slog.Error("traefik config: invalid route rate limit, skipping route",
						"host", r.Host, "path", r.Path, "err", err)
					continue
				}
				mwName := name + "-rl"
				mws = append(mws, mwName)
				if cfg.HTTP.Middlewares == nil {
					cfg.HTTP.Middlewares = map[string]httpMiddleware{}
				}
				cfg.HTTP.Middlewares[mwName] = httpMiddleware{RateLimit: &rateLimit{
					Average: r.RateLimit.Average,
					Period:  "1m",
					Burst:   r.RateLimit.Burst,
				}}
			}
			router.Middlewares = mws
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
// 路由数下需要考虑），且纯函数确定性（同输入同键，重启/重发布不漂）。
//
// R07-M2：键名恒落在 [a-z0-9-]（dnsSafeName 折叠——'@' 等 path 白名单
// 字符不再原样进 traefik 对象名）；host 按小写归一后再入键与哈希，使
// 大写/小写两种写法命中同一 router（path 仍大小写敏感，只有对象名折叠）。
func routeKey(r capability.Route) string {
	host := capability.NormalizeRouteHost(r.Host)
	key := host
	if r.Path != "" && r.Path != "/" {
		key += "-" + strings.Trim(r.Path, "/")
	}
	normalized := dnsSafeName(key)
	// hash.Hash 约定 Write 永不返回错（fnv 实现恒 nil），检错无处置面。
	h := fnv.New64a()
	_, _ = h.Write([]byte(host))
	_, _ = h.Write([]byte{0}) // 分隔符：("a","b/c") 与 ("a/b","c") 不共哈希
	_, _ = h.Write([]byte(r.Path))
	return fmt.Sprintf("%s-%012x", normalized, h.Sum64()&0xFFFFFFFFFFFF)
}

// dnsSafeName 把任意键折叠为 traefik 对象名允许的 DNS 安全字符集
// [a-z0-9-]：大写折小写，其余字符折 '-'，首尾 '-' 去掉（DNS label 不以
// 连字符起止）。折叠只作用于对象名——规则串里的 path 保持原字节。
func dnsSafeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteByte(c)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// httpRuleRe 是生成器产出的 HTTP 规则语法全集（P9 预检的 schema 面）：
// Host(`h`) 可选 && PathPrefix(`p`)；定界段内禁反引号（注入面）。
var httpRuleRe = regexp.MustCompile(`^Host\(` + "`" + `[^` + "`" + `]+` + "`" + `\)( && PathPrefix\(` + "`" + `[^` + "`" + `]+` + "`" + `\))?$`)

// tcpRuleRe 是 TCP 规则语法全集：HostSNI(`h`)。
var tcpRuleRe = regexp.MustCompile(`^HostSNI\(` + "`" + `[^` + "`" + `]+` + "`" + `\)$`)

// traefikNameRe 是受管 router/service/middleware 名的字符集（DNS label
// 形态：小写字母数字与连字符，不以连字符起止）。traefik 明示 router 名
// 不允许 '@'（v3.5 Rules & Priority），而 path 白名单本身允许 '@'——名字
// 折叠（routeKey/dnsSafeName）之外再在预检面 fail-closed 兜底生成器漏网。
var traefikNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// validateTraefikName 校验一个 traefik 对象名落在受管字符集内。
func validateTraefikName(kind, name string) error {
	if !traefikNameRe.MatchString(name) {
		return fmt.Errorf("traefik precheck: %s name %q is outside the DNS-safe charset [a-z0-9-]", kind, name)
	}
	return nil
}

// validateDynamicConfig 是发布前 schema 级预检（P9；裁决=降级轨道）：
// traefik 无配置校验面（真机核对 2026-10-04：v3.5.6 子命令仅 healthcheck/
// version、API 全只读、POST /api/validate 404），拒载行为=整快照丢弃 +
// last-known-good 继续服务且零日志——控制面在发布侧提前闭合同一语义：
// 生成物不过预检 → 整快照拒绝、旧快照继续服务（5s poll 窗口与静默面
// 一并消除）。校验内容（生成器不变量，红=生成器 bug 或漏网输入）：
//  1. JSON 严格回读（未知字段 = 形态漂移）；
//  2. router.service 引用闭合（HTTP/TCP 各自面内）；
//  3. 规则串精确落在生成语法内（Host/PathPrefix/HostSNI + 反引号定界）；
//  4. servers[].url 可解析且 scheme 合面（http/h2c；tcp://）。
func validateDynamicConfig(schema []byte) error {
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.DisallowUnknownFields()
	var cfg dynamicConfig
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("traefik precheck: snapshot is not valid dynamic config: %w", err)
	}
	if cfg.HTTP != nil {
		for name, r := range cfg.HTTP.Routers {
			if err := validateTraefikName("http router", name); err != nil {
				return err
			}
			if !httpRuleRe.MatchString(r.Rule) {
				return fmt.Errorf("traefik precheck: http router %s rule is outside the generated grammar: %s", name, r.Rule)
			}
			if _, ok := cfg.HTTP.Services[r.Service]; !ok {
				return fmt.Errorf("traefik precheck: http router %s references missing service %s", name, r.Service)
			}
		}
		for name, s := range cfg.HTTP.Services {
			if err := validateTraefikName("http service", name); err != nil {
				return err
			}
			if err := validateServerURLs(name, s.LoadBalancer.Servers, map[string]bool{"http": true, "h2c": true}); err != nil {
				return err
			}
		}
		for name := range cfg.HTTP.Middlewares {
			if err := validateTraefikName("http middleware", name); err != nil {
				return err
			}
		}
	}
	if cfg.TCP != nil {
		for name, r := range cfg.TCP.Routers {
			if err := validateTraefikName("tcp router", name); err != nil {
				return err
			}
			if !tcpRuleRe.MatchString(r.Rule) {
				return fmt.Errorf("traefik precheck: tcp router %s rule is outside the generated grammar: %s", name, r.Rule)
			}
			if _, ok := cfg.TCP.Services[r.Service]; !ok {
				return fmt.Errorf("traefik precheck: tcp router %s references missing service %s", name, r.Service)
			}
		}
		for name, s := range cfg.TCP.Services {
			if err := validateTraefikName("tcp service", name); err != nil {
				return err
			}
			if err := validateServerURLs(name, s.LoadBalancer.Servers, map[string]bool{"tcp": true}); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateServerURLs 校验 loadBalancer.servers 的 URL 形态（scheme 白名单
// + host:port 非空）。
func validateServerURLs(service string, servers []server, allowed map[string]bool) error {
	for i, srv := range servers {
		u, err := url.Parse(srv.URL)
		if err != nil || !allowed[u.Scheme] || u.Host == "" {
			return fmt.Errorf("traefik precheck: service %s server[%d] has an invalid url %q (want scheme %v with host:port)",
				service, i, srv.URL, keysOf(allowed))
		}
	}
	return nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
