package cmd

// 端口暴露自证单测（ADR-0036）：主机分类与探测裁决全部纯函数 + 注入
// 探针——不拨真网，Windows 本机确定性。

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyHost(t *testing.T) {
	cases := []struct {
		host string
		want exposeClass
	}{
		{"", exposeWildcard},
		{"0.0.0.0", exposeWildcard},
		{"::", exposeWildcard},
		{"127.0.0.1", exposeLoopback},
		{"::1", exposeLoopback},
		{"localhost", exposeLoopback},
		{"10.124.0.3", exposePrivate},
		{"172.16.1.5", exposePrivate},
		{"192.168.1.1", exposePrivate},
		{"100.64.0.1", exposePrivate},   // CGNAT（云 VPC 形态）
		{"169.254.10.9", exposePrivate}, // 链路本地
		{"fd00::1", exposePrivate},      // ULA
		{"fe80::1", exposePrivate},      // 链路本地 v6
		{"8.8.8.8", exposePublic},
		{"1.2.3.4", exposePublic},
		{"zot.example.com", exposePublic}, // 域名按最暴露假设
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, classifyHost(tc.host), "host %q", tc.host)
	}
}

func TestParseEndpointTarget(t *testing.T) {
	tgt := parseEndpointTarget("http://10.124.0.3:9082/edge/config")
	assert.Equal(t, "10.124.0.3", tgt.host)
	assert.Equal(t, "9082", tgt.port)
	assert.False(t, tgt.parseErr)

	// 缺端口回退既知监听端口。
	tgt = parseEndpointTarget("http://10.124.0.3/edge/config")
	assert.Equal(t, "10.124.0.3", tgt.host)
	assert.Equal(t, "9082", tgt.port)

	// 裸 host:port 宽容兼容。
	tgt = parseEndpointTarget("10.0.0.5:9082")
	assert.Equal(t, "10.0.0.5", tgt.host)
	assert.Equal(t, "9082", tgt.port)

	// 非法形态不探测。
	assert.True(t, parseEndpointTarget("http://").parseErr)
}

func TestParseRegistryTarget(t *testing.T) {
	tgt := parseRegistryTarget("10.124.0.3:5000")
	assert.Equal(t, "10.124.0.3", tgt.host)
	assert.Equal(t, "5000", tgt.port)

	// 纯主机宽容回退既知仓库端口。
	tgt = parseRegistryTarget("10.124.0.3")
	assert.Equal(t, "10.124.0.3", tgt.host)
	assert.Equal(t, "5000", tgt.port)

	assert.True(t, parseRegistryTarget("").parseErr)
}

func TestEdgeConfigExposure(t *testing.T) {
	listening := func(string) error { return nil }
	refusing := func(string) error { return errors.New("connection refused") }

	// 未配置 = 面停用（现状语义），ok 不探测。
	c := edgeConfigExposure("", listening)
	assert.Equal(t, checkOK, c.Status)
	assert.Contains(t, c.Detail, "not configured")

	// 公网可达 = fail（无认证端点，暴露即全量路由可改写）。
	c = edgeConfigExposure("http://8.8.8.8:9082/edge/config", listening)
	assert.Equal(t, checkFail, c.Status)
	assert.Contains(t, c.Detail, "publicly reachable at 8.8.8.8:9082")

	// 公网不可达 = 自证不可达（本检查的核心价值）。
	c = edgeConfigExposure("http://8.8.8.8:9082/edge/config", refusing)
	assert.Equal(t, checkOK, c.Status)
	assert.Contains(t, c.Detail, "self-certified")

	// 私网可达 = ok（边界 = 云防火墙）；不可达 = warn 未监听。
	c = edgeConfigExposure("http://10.124.0.3:9082/edge/config", listening)
	assert.Equal(t, checkOK, c.Status)
	assert.Contains(t, c.Detail, "non-public address")
	c = edgeConfigExposure("http://10.124.0.3:9082/edge/config", refusing)
	assert.Equal(t, checkWarn, c.Status)
	assert.Contains(t, c.Detail, "not listening")

	// 通配目标 = 无法自证。
	c = edgeConfigExposure("http://0.0.0.0:9082/edge/config", listening)
	assert.Equal(t, checkWarn, c.Status)
	assert.Contains(t, c.Detail, "wildcard")
}

func TestRegistryExposure(t *testing.T) {
	listening := func(string) error { return nil }
	refusing := func(string) error { return errors.New("connection refused") }

	// 未配置 = 受管仓库停用。
	c := registryExposure("", listening)
	assert.Equal(t, checkOK, c.Status)

	// 公网可达 = warn（明文 + 单一平台凭证，烈度低于无认证面）。
	c = registryExposure("8.8.8.8:5000", listening)
	assert.Equal(t, checkWarn, c.Status)
	assert.Contains(t, c.Detail, "publicly reachable at 8.8.8.8:5000")

	// 公网不可达 = 自证不可达。
	c = registryExposure("8.8.8.8:5000", refusing)
	assert.Equal(t, checkOK, c.Status)
	assert.Contains(t, c.Detail, "self-certified")

	// 私网可达 = ok。
	c = registryExposure("10.124.0.3:5000", listening)
	assert.Equal(t, checkOK, c.Status)
	assert.Contains(t, c.Detail, "non-public address")
}

func TestBindSurfaceCheck(t *testing.T) {
	// 缺省三面皆通配绑定 → 显式 warn + config 键点名。
	c := bindSurfaceCheck([]struct{ name, addr, key string }{
		{"grpc", ":9080", "server.grpc.addr"},
		{"gateway http", ":9081", "server.http.addr"},
		{"edge config", ":9082", "server.edge_config.addr"},
	})
	assert.Equal(t, checkWarn, c.Status)
	assert.Contains(t, c.Detail, "(wildcard: server.grpc.addr, server.http.addr, server.edge_config.addr)")
	assert.NotEmpty(t, c.Advice)

	// 全部钉定 → ok（pinned）。
	c = bindSurfaceCheck([]struct{ name, addr, key string }{
		{"grpc", "127.0.0.1:9080", "server.grpc.addr"},
		{"gateway http", "10.124.0.3:9081", "server.http.addr"},
		{"edge config", "10.124.0.3:9082", "server.edge_config.addr"},
	})
	assert.Equal(t, checkOK, c.Status)
	assert.Contains(t, c.Detail, "(pinned)")

	// 混合形态 → warn 只点名通配键。
	c = bindSurfaceCheck([]struct{ name, addr, key string }{
		{"grpc", "127.0.0.1:9080", "server.grpc.addr"},
		{"gateway http", ":9081", "server.http.addr"},
		{"edge config", "10.124.0.3:9082", "server.edge_config.addr"},
	})
	assert.Equal(t, checkWarn, c.Status)
	assert.Contains(t, c.Detail, "(wildcard: server.http.addr)")
}

// runExposureChecks 固定三条、顺序稳定（golden 对账前提）。
func TestRunExposureChecksShape(t *testing.T) {
	checks := runExposureChecks(exposureTargets{}, func(string) error { return nil })
	assert.Len(t, checks, 3)
	assert.Equal(t, "edge config exposure", checks[0].Name)
	assert.Equal(t, "registry exposure", checks[1].Name)
	assert.Equal(t, "bind surface", checks[2].Name)
}
