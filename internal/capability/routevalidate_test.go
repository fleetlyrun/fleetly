package capability

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Route host/path 白名单（安全批共享真源）：API 受理面与 traefik Provider
// 纵深面共用，这里钉死放行/拒绝边界。
func TestValidateRouteHost(t *testing.T) {
	t.Run("legal", func(t *testing.T) {
		for _, h := range []string{
			"shop.127.0.0.1.sslip.io", // sslip.io 调试域（quickstart 形态）
			"n0.dev.fleetly.run",
			"single",        // 单标签
			"192.168.1.10",  // 单标签 IP 形态
			"*.fleetly.run", // 通配前缀
			"a-b.c.d",       // 标签内连字符
			"xn--bcher-kva.example",
			strings.Repeat("a", 63) + ".example", // 63 字节单标签（上限）
		} {
			require.NoError(t, ValidateRouteHost(h), "host %q must be legal", h)
		}
	})

	t.Run("illegal", func(t *testing.T) {
		for _, h := range []string{
			"",                               // 空
			"`evil.sslip.io`",                // 反引号注入
			"un_der.sslip.io",                // 下划线
			"spa ce.sslip.io",                // 内嵌空白
			"host\ninjection",                // 换行
			"tab\tx",                         // 制表符
			"https://evil.example",           // scheme 前缀
			"-leading.sslip.io",              // 标签连字符开头
			"trailing-.sslip.io",             // 标签连字符结尾
			"do..uble.example",               // 空标签
			"*evil.example",                  // 通配符不成标签
			"**.example",                     // 双通配
			"*.",                             // 裸通配
			strings.Repeat("a", 64),          // 单标签超 63
			strings.Repeat("ab.", 128) + "c", // 总长超 253
		} {
			assert.Error(t, ValidateRouteHost(h), "host %q must be rejected", h)
		}
	})

	// 长度边界：253 字节点分域名合法，254 拒。
	ok253 := strings.Join([]string{
		strings.Repeat("a", 63), strings.Repeat("a", 63),
		strings.Repeat("a", 63), strings.Repeat("a", 61),
	}, ".")
	require.Len(t, ok253, 253)
	assert.NoError(t, ValidateRouteHost(ok253))
	assert.Error(t, ValidateRouteHost(ok253+"b"))
}

func TestValidateRoutePath(t *testing.T) {
	for _, p := range []string{"", "/", "/api", "/v1/users", "/a.b/c~d$&'()*+,;=:@%-"} {
		assert.NoError(t, ValidateRoutePath(p), "path %q must be legal", p)
	}
	for _, p := range []string{
		"no-leading-slash",
		"/bad`tick",                     // 反引号（traefik 规则定界符）
		"/bad space",                    // 空白
		"/bad\x00nul",                   // 控制字符
		"/bad\n",                        // 换行
		"/bad\\slash",                   // 反斜杠
		"/bad\"quote",                   // 双引号
		"/" + strings.Repeat("a", 1025), // 超长
	} {
		assert.Error(t, ValidateRoutePath(p), "path %q must be rejected", p)
	}
}
