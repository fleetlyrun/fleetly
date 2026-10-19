package github

// NewClient 注入分支的安全面补装接线测试（评审 P3 跟进）：注入客户端的
// 零值面（CheckRedirect/Timeout/Transport）必须补齐出站约束——nil
// Transport 是常见注入形态（&http.Client{}），缺补装即静默失去 IP 门禁。
// 约束的行为面（跨源拒绝/私网拒拨/DNS 重绑定封死）在 outbound_test 以假
// resolver 覆盖，本文件只钉构造层接线（白盒读 unexported http 字段）。

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientInjectionInstallsSecureTransport(t *testing.T) {
	c := NewClient(&http.Client{}, nil)
	require.NotNil(t, c.http.Transport, "a zero-value injected client must receive the secure transport (IP dial gate)")
	tr, ok := c.http.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotNil(t, tr.DialContext, "the installed transport must carry the guarded dialer")
	assert.Nil(t, c.http.Transport.(*http.Transport).Proxy, "proxy must stay disabled (proxy-side resolution bypasses the IP gate)")
	assert.Equal(t, DefaultOutboundTimeout, c.http.Timeout)
	assert.NotNil(t, c.http.CheckRedirect, "the redirect gate must be installed on a zero-value injected client")

	// 显式自带 Transport 的注入保持豁免形态（httptest TLS 等测试自定义传
	// 输面）——零值才补，调用方显式值优先。
	custom := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
	c2 := NewClient(custom, nil)
	assert.Same(t, custom.Transport, c2.http.Transport)
	assert.Equal(t, time.Second, c2.http.Timeout)
}
