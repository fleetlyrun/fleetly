package config

// 受管 Proxy 配置端点暴露面启动门（R09-M8/R14-M6 → S14-c9，v1 必做）。
//
// 裁决形态（台账 §5 #4）：多租户启用前，受管端点不得无认证——团队数 > 1
// 且 `server.proxy_config.auth_token` 为空且监听非回环 → 拒绝启动。单租户
// 形态保持现状（ADR-0036：收窄是显式动作、升级零扰动；缺省 ":9082" 通配
// 绑定是既成部署形态，staging 单队不受影响）。回环监听不另设门：他租户
// 进程不可达，且 managed traefik 走网络拉取时本就要求非回环绑址。
//
// 调用面：assembly 的 pre-start 钩子探测团队数后调本判定（fail-fast 先于
// 服务启动）；本函数是纯配置判定（可单测、无 I/O、不泄漏内部地址——
// 错误只回显运维自己配置的监听地址与修复键）。

import (
	"fmt"
	"net"
)

// ValidateProxyConfigExposure 校验受管 Proxy 配置端点的暴露面。
// moreThanOneTeam 由调用方按团队数判定（teams>1）。
func (c *AppConfig) ValidateProxyConfigExposure(moreThanOneTeam bool) error {
	if c.ProxyConfigAuthToken() != "" {
		return nil // 显式认证形态：任何绑定/租户数都受保护
	}
	if !moreThanOneTeam {
		return nil // 单租户：现状零扰动（ADR-0036 显式动作原则）
	}
	addr := c.ProxyConfigAddr()
	if loopbackListen(addr) {
		return nil // 回环：他租户进程不可达
	}
	return fmt.Errorf("refusing to start: the managed proxy config endpoint listens on %q without authentication while the platform holds more than one team; set server.proxy_config.auth_token (env FLEETLY_SERVER_PROXY_CONFIG_AUTH_TOKEN) or bind server.proxy_config.addr to a loopback address", addr)
}

// loopbackListen 报告监听地址是否只绑回环。形态不明按非回环处理
// （fail-closed：宁可拒绝启动也不静默放行）。
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false // 通配绑定（":9082" / "0.0.0.0:9082" 归一后 host 空）
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
