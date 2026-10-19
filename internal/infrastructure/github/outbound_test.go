package github_test

// GitHub 出站面约束（R07-M4）：跨源重定向拒绝 / 解析 IP 私网门禁（含
// DNS 混注）/ 总超时上界 / 同源重定向放行 / 生产构建 NewClient(nil) 确实
// 拿到约束。全部经假 resolver + 假 RoundTripper 承载，不依赖真实网络；
// 正常 200 正例由既有 client_test.go 的 httptest 用例覆盖（注入 Transport
// 的形态保留自定义传输面）。

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
)

// staticResolver 返回固定解析结果（DNS 混注由多条目承载）。
type staticResolver []net.IPAddr

func (s staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return s, nil
}

// roundTripFunc 是 http.RoundTripper 函数缝（无网络假应答）。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeResponse(req *http.Request, status int, location, body string) *http.Response {
	h := http.Header{}
	if location != "" {
		h.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status, Header: h,
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: req,
	}
}

// 302 到异源（含异 host / 降级 scheme）拒绝；经 Client 的 do 出口后错误
// 文本不回显重定向目标（目标可能是内网/元数据端点）。
func TestGitHubClientRejectsCrossOriginRedirect(t *testing.T) {
	hc := github.NewSecureHTTPClient(staticResolver{{IP: net.ParseIP("93.184.216.34")}})
	hc.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return fakeResponse(req, http.StatusFound, "https://evil.example/internal", ""), nil
	})
	client := github.NewClient(hc, nil)

	_, err := client.ConvertManifestCode(context.Background(), "https://api.example", "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing an outbound redirect")
	assert.NotContains(t, err.Error(), "evil.example", "the redirect target must not leak into the error")
	assert.NotContains(t, err.Error(), "169.254", "no internal address may ride the error text")

	// 同 host 但 scheme 降级（https → http）同样拒绝：端口/协议变更都
	// 可能把请求导向非出站约束面。
	hc2 := github.NewSecureHTTPClient(staticResolver{{IP: net.ParseIP("93.184.216.34")}})
	hc2.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return fakeResponse(req, http.StatusFound, "http://api.example/plain", ""), nil
	})
	_, err = github.NewClient(hc2, nil).ConvertManifestCode(context.Background(), "https://api.example", "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing an outbound redirect")
}

// 解析到私网/环回/链路本地/组播/保留段的地址一律拒绝，且在拨号前拒绝
// （不产生实际连接）；错误文本不回显 IP。
func TestSecureClientRejectsNonPublicResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		ip   string
	}{
		{"rfc1918", "10.0.0.0"},
		{"loopback", "127.0.0.1"},
		{"cloud-metadata", "169.254.169.254"},
		{"multicast", "224.0.0.1"},
		{"unspecified", "0.0.0.0"},
		{"cgnat", "100.64.0.1"},
		{"ipv6-loopback", "::1"},
		{"ipv6-ula", "fd00::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			require.NotNil(t, ip)
			client := github.NewSecureHTTPClient(staticResolver{{IP: ip}})
			_, err := client.Get("https://api.example/")
			require.Error(t, err, "a resolution to %s must be refused", tc.ip)
			assert.Contains(t, err.Error(), "non-public address")
			assert.NotContains(t, err.Error(), tc.ip, "the resolved address must not leak into the error")
		})
	}
}

// DNS 混注（公网 + 私网同答）不放行：任一解析结果非公网即整体拒绝。
func TestSecureClientRejectsMixedResolution(t *testing.T) {
	client := github.NewSecureHTTPClient(staticResolver{
		{IP: net.ParseIP("93.184.216.34")},
		{IP: net.ParseIP("10.1.2.3")},
	})
	_, err := client.Get("https://api.example/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public address")
}

// 同源（scheme + host 一致）重定向放行：企业 GHES 的尾斜杠/路径跳转不被
// 误伤。
func TestSecureClientAllowsSameOriginRedirect(t *testing.T) {
	calls := 0
	client := github.NewSecureHTTPClient(staticResolver{{IP: net.ParseIP("93.184.216.34")}})
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return fakeResponse(req, http.StatusFound, "https://api.example/next", ""), nil
		}
		return fakeResponse(req, http.StatusOK, "", `{"ok":true}`), nil
	})
	resp, err := client.Get("https://api.example/")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // 假应答体
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, calls)
}

// 挂起上游的总超时上界：Transport 阻塞到请求 ctx 到期，客户端必须在
// 配置的总超时量级内返回 timeout 错误（不是无界挂起）。
func TestSecureClientTotalTimeout(t *testing.T) {
	client := github.NewSecureHTTPClient(nil)
	client.Timeout = 60 * time.Millisecond
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	start := time.Now()
	_, err := client.Get("https://api.example/")
	require.Error(t, err)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout(), "the hung upstream must surface as a timeout")
	assert.Less(t, time.Since(start), 5*time.Second, "the client timeout must bound the request")
}

// 请求 ctx 的上界同样生效：调用方 deadline 更早时以调用方为准（客户端总
// 超时是兜底，不覆盖调用方更紧的 ctx）。
func TestClientHonorsRequestContextDeadline(t *testing.T) {
	hc := github.NewSecureHTTPClient(nil)
	hc.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	c := github.NewClient(hc, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := c.ConvertManifestCode(ctx, "https://api.example", "code")
	require.Error(t, err)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout(), "the request context deadline must bound the call")
}

// 生产构建形态（NewClient(nil, nil)）确实装上出站约束：以环回 api_base
// 直连会被解析 IP 门禁拦下（错误是"non-public address"而非 connection
// refused——证明请求没有真的拨号）。
func TestNewClientWithoutInjectionIsHardened(t *testing.T) {
	c := github.NewClient(nil, nil)
	_, err := c.ConvertManifestCode(context.Background(), "http://127.0.0.1:1", "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public address")
	assert.NotContains(t, err.Error(), "connection refused")
	assert.NotContains(t, err.Error(), "127.0.0.1", "the internal address must not leak into the error")
}

// 默认构造面参数：总超时/重定向闸/传输层三者都在场。
func TestSecureClientDefaults(t *testing.T) {
	client := github.NewSecureHTTPClient(nil)
	assert.Equal(t, github.DefaultOutboundTimeout, client.Timeout)
	assert.NotNil(t, client.CheckRedirect)
	assert.NotNil(t, client.Transport)
}
