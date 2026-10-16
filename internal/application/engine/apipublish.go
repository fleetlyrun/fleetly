package engine

import (
	"net/url"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// 平台 API（gRPC 控制面）公网发布面（ADR-0079）：受管 traefik 前置一条
// h2c Host 路由指回 fleetlyd gRPC 监听——CLI/SDK 从公网经 TLS 直连的通道
//（console 面同款形态；CLI 面为纯 gRPC，一条路由即整面）。

// APILoginMethodPath 是登录端点的 gRPC method 路径——proto/fleetly/
// identity/v1/identity.proto UsersService.Login 的同源物化（apitest 漂移
// 守卫锚到生成码 ServiceDesc：proto 改包名/服务名而常量未同批改的漂移
// = 限速路由指空路径，登录端点公网无限速裸奔）。
const APILoginMethodPath = "/fleetly.identity.v1.UsersService/Login"

// ApiConfig 是平台 API 公网发布面配置（ADR-0079；config.api 三字段直通）。
// 零值 = 面停用（不发布任何路由，现状）。
type ApiConfig struct {
	// Host 是 API 公网主机名（如 api.dev.fleetly.run）。
	Host string
	// GRPCURL 是 traefik 容器可达的 fleetlyd gRPC 基址（console.
	// gateway_url 同文化——traefik 容器能拉到的地址即正确地址）。
	GRPCURL string
	// TLSMode 是路由 TLS 模式：auto（缺省口径，空串随渲染层视为 auto）| none。
	TLSMode string
}

// Configured 报告 API 公网发布面是否装配可用。
func (c ApiConfig) Configured() bool { return c.Host != "" && c.GRPCURL != "" }

// apiCapabilityRoutes 铸 API 公网发布双路由（publishRoutes 合并面；
// ADR-0079 决策 2）：
//
//	① 主路由 Host-only + h2c → fleetlyd gRPC（整面：非 PUBLIC 动词仍需
//	   Token，authn 拦截链语义零变化）；
//	② 登录路由 Host && PathPrefix（gRPC method 路径）带 rateLimit——
//	   CLI login 的密码形态走 gRPC Login，与 console REST /v1/auth/login
//	   同一后端方法；不限速则公网 gRPC 面是绕过 console 登录限速的爆破
//	   通道（ADR-0061 决策 3 同值：每源 IP 均值 10、突发 20）。
//
// 路由指向控制面自身而非租户载体——Target/Process/Port 零值；grpc_url
// 解析失败返回 nil（装配错误诚实降级为不发布，错误日志留痕）。
func (e *Engine) apiCapabilityRoutes() []capability.Route {
	if !e.opts.Data.Api.Configured() {
		return nil
	}
	grpcURL, err := url.Parse(e.opts.Data.Api.GRPCURL)
	if err != nil || grpcURL.Host == "" {
		e.log.Error("api route: grpc url malformed", "url", e.opts.Data.Api.GRPCURL)
		return nil
	}
	main := capability.Route{
		Host:        e.opts.Data.Api.Host,
		Protocol:    capability.ProtocolH2C,
		TLS:         e.opts.Data.Api.TLSMode,
		BackendAddr: grpcURL.Host,
	}
	login := main
	login.Path = APILoginMethodPath
	login.RateLimit = &capability.RouteRateLimit{
		Average: consoleLoginRateAverage,
		Burst:   consoleLoginRateBurst,
	}
	return []capability.Route{main, login}
}
