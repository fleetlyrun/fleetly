package github

// GitHub 出站面的安全收口（R07-M4）：受理面 validateAPIBase 只做字符串层
// 私网拒绝，302 重定向与 DNS 重绑定可绕过——本文件在客户端构造层补齐
// 三道约束（生产装配 NewClient(nil, nil) 即得）：
//  1. CheckRedirect 只允许同源（scheme+host 与原请求一致）重定向，并限制
//     跳数；跨 host 一律拒绝（错误文本不回显目标地址）；
//  2. 客户端总超时（DefaultOutboundTimeout）+ 请求 ctx 传递（调用方
//     deadline 更早时以调用方为准）；
//  3. DialContext 在拨号前解析 host 并拒绝私网/环回/链路本地/组播/保留
//     段 IP；用解析结果直接拨号避免二次解析（DNS 重绑定 TOCTOU）。解析
//     结果中任一地址非公网即整体拒绝（混注不放行）。
//
// 自定义 api_base（企业 GHES）能力保留：约束按"每个请求的 host"执行，
// 不写死 GitHub 域名。出站代理不参与（Proxy=nil）：代理会代做解析，使
// 第 3 条校验看不到真实目标 IP。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultOutboundTimeout 是出站请求的总超时（连接 + 重定向 + 响应体读取）。
const DefaultOutboundTimeout = 15 * time.Second

// maxRedirects 是允许的重定向跳数上限（默认 10 跳对 API 客户端过宽）。
const maxRedirects = 5

// IPResolver 是 DNS 解析接缝（生产 = net.DefaultResolver；测试注入假
// resolver 验证私网拒绝与 DNS 混注，不依赖真实网络）。
type IPResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

var (
	// errNonPublicAddress 是不泄漏 IP 的拒绝文案（SSRF 场景错误信息只述
	// 原因，不回显解析结果/内部地址）。
	errNonPublicAddress = errors.New("github: refusing outbound connection to a non-public address")
	// errRedirectRefused 同理不回显重定向目标（URL 可能指向内网/元数据
	// 端点；client.Do 的 url.Error 会把目标 URL 拼进错误文本，do 层据此
	// 哨兵重包为无地址文案）。
	errRedirectRefused = errors.New("github: refusing an outbound redirect (cross-origin or too many hops)")
)

// NewSecureHTTPClient 构造带出站约束的 HTTP 客户端（resolver 为 nil 用
// 系统解析器）。生产装配经 NewClient(nil, ...) 使用本形态。
func NewSecureHTTPClient(resolver IPResolver) *http.Client {
	return newSecureHTTPClient(resolver, DefaultOutboundTimeout)
}

// newSecureHTTPClient 是超时可注入的同形构造（测试用小超时验证挂起上界）。
func newSecureHTTPClient(resolver IPResolver, timeout time.Duration) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tr := base.Clone()
	tr.Proxy = nil // 见文件头：代理解析会绕过 IP 门禁
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tr.DialContext = guardedDialContext(resolver, dialer)
	return &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: refuseCrossOriginRedirect,
	}
}

// refuseCrossOriginRedirect 限制重定向到与原请求同源（scheme + host，
// host 比较大小写不敏感）。跨 host = 拒绝（302/307/308 均走本判据；
// Go 的 client 对 307/308 会保留方法与 body，正是盲 SSRF 的形态）。
func refuseCrossOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= maxRedirects {
		return errRedirectRefused
	}
	origin := via[0].URL
	if req.URL.Scheme != origin.Scheme || !strings.EqualFold(req.URL.Host, origin.Host) {
		return errRedirectRefused
	}
	return nil
}

// guardedDialContext 返回"解析 → 全量 IP 公网校验 → 按解析结果直拨"的
// DialContext。直拨解析结果而非让 dialer 二次解析，封死 DNS 重绑定窗口。
func guardedDialContext(resolver IPResolver, dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("github: invalid outbound address")
		}
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("github: resolve outbound host: %w", err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("github: outbound host resolved to no addresses")
		}
		for _, ip := range ips {
			if !isPublicIP(ip.IP) {
				return nil, errNonPublicAddress
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if derr == nil {
				return conn, nil
			}
			lastErr = derr
		}
		return nil, lastErr
	}
}

// isPublicIP 报告地址是否可作为出站目标：私网/环回/链路本地/组播/未指定
// 及 IANA 保留段（CGNAT、文档、基准、240/4）一律拒绝。
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0: // 0.0.0.0/8
			return false
		case v4[0] == 100 && v4[1]&0xc0 == 64: // 100.64.0.0/10（CGNAT）
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 2: // 192.0.2.0/24（文档）
			return false
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // 198.18.0.0/15（基准）
			return false
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100: // 198.51.100.0/24（文档）
			return false
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113: // 203.0.113.0/24（文档）
			return false
		case v4[0] >= 240: // 240.0.0.0/4（保留，含广播）
			return false
		}
		return true
	}
	// IPv6 文档前缀 2001:db8::/32（其余非公网段已由上面的 net.IP 判据覆盖）。
	if len(ip) == net.IPv6len && ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
		return false
	}
	return true
}
