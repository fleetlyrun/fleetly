package engine

import (
	"net/url"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Console 面板公网发布面（ADR-0061）：受管 traefik 前置一条 Host 路由指回
// REST gateway——Console SPA 与 /v1 REST API 同端口同源服务
//（internal/console.Mount），一条 Host 路由即整面。

// ConsoleLoginPath 是登录端点的 REST 路径——proto/fleetly/identity/v1/
// identity.proto Login 注解的同源物化（apitest 漂移守卫钉死：注解改路径，
// 本常量与限速路由必须同批改）。
const ConsoleLoginPath = "/v1/auth/login"

// console 登录限速缺省（ADR-0061 决策 3）：每源 IP 每分钟均值 10、突发
// 20——人类登录充裕，bcrypt 在线爆破被压到不可行量级。
const (
	consoleLoginRateAverage = 10
	consoleLoginRateBurst   = 20
)

// ConsoleConfig 是 Console 面板公网发布面配置（ADR-0061；config.console
// 三字段直通）。零值 = 面停用（不发布任何路由，现状）。
type ConsoleConfig struct {
	// Host 是面板公网主机名（如 console.dev.fleetly.run）。
	Host string
	// GatewayURL 是 traefik 容器可达的 REST gateway 基址（browse.gateway_url
	// 同文化——traefik 容器能拉到的地址即正确地址）。
	GatewayURL string
	// TLSMode 是路由 TLS 模式：auto（缺省口径，空串随渲染层视为 auto）| none。
	TLSMode string
}

// Configured 报告 Console 公网发布面是否装配可用。
func (c ConsoleConfig) Configured() bool { return c.Host != "" && c.GatewayURL != "" }

// consoleCapabilityRoutes 铸 Console 公网发布双路由（publishRoutes 合并
// 面；ADR-0061 决策 2）：
//
//	① 主路由 Host-only → gateway（Console SPA + /v1 REST API 整面）；
//	② 登录路由 Host && PathPrefix（规则更长优先级更高——browse entry 先例）
//	   带 rateLimit：PUBLIC 端点公网可达前的唯一限速面。
//
// 路由指向控制面自身而非租户载体——Target/Process/Port 零值（发布面只消费
// host/path/protocol/tls/backend/auth/ratelimit）；gateway_url 解析失败
// 返回 nil（装配错误诚实降级为不发布，错误日志留痕）。
func (e *Engine) consoleCapabilityRoutes() []capability.Route {
	if !e.opts.Console.Configured() {
		return nil
	}
	gwURL, err := url.Parse(e.opts.Console.GatewayURL)
	if err != nil || gwURL.Host == "" {
		e.log.Error("console route: gateway url malformed", "url", e.opts.Console.GatewayURL)
		return nil
	}
	main := capability.Route{
		Host:        e.opts.Console.Host,
		Protocol:    capability.ProtocolHTTP,
		TLS:         e.opts.Console.TLSMode,
		BackendAddr: gwURL.Host,
	}
	login := main
	login.Path = ConsoleLoginPath
	login.RateLimit = &capability.RouteRateLimit{
		Average: consoleLoginRateAverage,
		Burst:   consoleLoginRateBurst,
	}
	return []capability.Route{main, login}
}
