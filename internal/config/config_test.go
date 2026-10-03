package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 绑面字段的缺省语义（ADR-0036）：不配置 = 原行为——edge config 仍
// ":9082"（现状硬编码平移为缺省），registry 仍停用（未设 env 的现状）；
// 钉址是显式动作，平台不静默改绑。
func TestWithDefaultsBindSurface(t *testing.T) {
	c := WithDefaults(&AppConfig{})
	assert.Equal(t, ":9080", c.GRPCAddr())
	assert.Equal(t, ":9081", c.HTTPAddr())
	assert.Equal(t, ":9082", c.EdgeConfigAddr())
	assert.Empty(t, c.RegistryAddr(), "registry addr unset = managed registry stays disabled")
}

// 显式配置的绑址原样保留（缺省只回填空值，不覆盖）。
func TestWithDefaultsPreservesConfiguredBinds(t *testing.T) {
	c := WithDefaults(&AppConfig{
		Server: &Server{
			Grpc:       &GRPC{Addr: "10.124.0.3:9080"},
			Http:       &HTTP{Addr: "127.0.0.1:9081"},
			EdgeConfig: &EdgeConfig{Addr: "10.124.0.3:9082"},
		},
		Registry: &Registry{Addr: "10.124.0.3:5000"},
	})
	assert.Equal(t, "10.124.0.3:9080", c.GRPCAddr())
	assert.Equal(t, "127.0.0.1:9081", c.HTTPAddr())
	assert.Equal(t, "10.124.0.3:9082", c.EdgeConfigAddr())
	assert.Equal(t, "10.124.0.3:5000", c.RegistryAddr())
}

// 访问器容忍 nil 链（装配侧存在只取单字段的消费点）。
func TestBindAccessorsTolerateNil(t *testing.T) {
	c := &AppConfig{}
	assert.Equal(t, DefaultGRPCAddr, c.GRPCAddr())
	assert.Equal(t, DefaultHTTPAddr, c.HTTPAddr())
	assert.Equal(t, DefaultEdgeConfigAddr, c.EdgeConfigAddr())
	assert.Empty(t, c.RegistryAddr())
	assert.Equal(t, DefaultEdgeConfigAddr, (*AppConfig)(nil).EdgeConfigAddr())
	assert.Empty(t, (*AppConfig)(nil).RegistryAddr())
}
