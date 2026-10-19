package config

// 受管 Proxy 配置端点暴露面启动门（R09-M8/R14-M6 → S14-c9，v1 必做）：
// 多租户形态（teams>1）下端点不得无认证；单租户/回环形态保持现状（收窄
// 是显式动作，升级零扰动——ADR-0036）。本文件只放纯配置判定（可单测），
// 团队数探测与启动接线在 assembly（薄壳）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateProxyConfigExposureMultiTeam：多租户 + 无 token 时监听
// 形态决定生死——通配/非回环 fail-fast，回环放行，token 放行。
func TestValidateProxyConfigExposureMultiTeam(t *testing.T) {
	for _, tc := range []struct {
		name    string
		addr    string
		token   string
		wantErr bool
	}{
		{"wildcard without token", ":9082", "", true},
		{"explicit all-interfaces", "0.0.0.0:9082", "", true},
		{"ipv6 wildcard", "[::]:9082", "", true},
		{"vpc bind without token", "10.124.0.3:9082", "", true},
		{"loopback v4 without token", "127.0.0.1:9082", "", false},
		{"loopback v6 without token", "[::1]:9082", "", false},
		{"localhost without token", "localhost:9082", "", false},
		{"wildcard with token", ":9082", "proxy-secret", false},
		{"vpc bind with token", "10.124.0.3:9082", "proxy-secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := WithDefaults(&AppConfig{Server: &Server{ProxyConfig: &ProxyConfig{Addr: tc.addr, AuthToken: tc.token}}})
			err := c.ValidateProxyConfigExposure(true)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "server.proxy_config.auth_token")
			assert.Contains(t, err.Error(), "FLEETLY_SERVER_PROXY_CONFIG_AUTH_TOKEN")
			assert.Contains(t, err.Error(), tc.addr, "the refusal names the offending listen address")
		})
	}
}

// TestValidateProxyConfigExposureSingleTeam：单队形态保持现状——缺省
// 通配绑定 + 无 token 仍是合法部署（staging/单机不受升级影响；多租户启用
// 前必须先置 token 或改回环）。
func TestValidateProxyConfigExposureSingleTeam(t *testing.T) {
	c := WithDefaults(&AppConfig{})
	require.NoError(t, c.ValidateProxyConfigExposure(false))
	// nil 配置链：按缺省解读（多租户下仍是拒绝态——fail-closed，不 panic）。
	require.Error(t, (*AppConfig)(nil).ValidateProxyConfigExposure(true))
	require.NoError(t, (*AppConfig)(nil).ValidateProxyConfigExposure(false))
}

// TestLoopbackListen：监听地址回环判定（形态不明按非回环 fail-closed）。
func TestLoopbackListen(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:9082":  true,
		"127.0.0.2:9082":  true,
		"[::1]:9082":      true,
		"localhost:9082":  true,
		":9082":           false,
		"0.0.0.0:9082":    false,
		"[::]:9082":       false,
		"10.124.0.3:9082": false,
		"not-an-address":  false,
		"":                false,
	} {
		assert.Equal(t, want, loopbackListen(addr), addr)
	}
}
