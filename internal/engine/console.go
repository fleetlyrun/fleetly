package engine

// Console 面板公网发布面（ADR-0061）：受管 traefik 前置一条 Host 路由指回
// REST gateway——Console SPA 与 /v1 REST API 同端口同源服务
//（internal/console.Mount），一条 Host 路由即整面。

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
