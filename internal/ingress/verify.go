package ingress

// 域名验证（fleetly domains verify；架构 §2.6 DNS 契约行）：解析域名 →
// 对入口发布端口探测（本机视角；E4，S19：端口经参数传入——与 ingress
// 配置的 http_port/https_port 同源，非默认端口部署不再探测 80/443 假
// 目标）→ 如实报告当前解析 IP 与端口可达性。不算 DNS 传播（无法也不应
// 猜测全网视角）——契约（A 记录指向全部节点、TTL ≤300s）的核验材料 =
// 当前观测，判断权在操作者。

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// verifyTimeout 是单域名探测预算（解析 + 80 + 443）。
const verifyTimeout = 5 * time.Second

// DomainCheck 是单域名的验证报告。HTTP80/HTTPS443 字段名沿用既有
// http_80/https_443 契约键（proto 契约面），E4 后承载的是「配置端口」的
// 探测结果（非默认端口部署下即该端口，不再恒为 80/443）。
type DomainCheck struct {
	Domain string   `json:"domain"`
	IPs    []string `json:"ips"`
	// Resolved 报告本机是否解析得到至少一个地址。
	Resolved bool `json:"resolved"`
	// HTTP80 是配置 HTTP 端口的探测结果（"" = 不可达；否则记录响应状态行）。
	HTTP80 string `json:"http_80,omitempty"`
	// HTTPS443 是配置 HTTPS 端口的 TLS 握手结果（"" = 不可达）。
	HTTPS443 string `json:"https_443,omitempty"`
	// CertSubject / CertNotAfter 是 443 端口实收证书的诚实记录
	//（不经信任判定——私有 CA/临期证书照实呈现）。
	CertSubject  string   `json:"cert_subject,omitempty"`
	CertDNSNames []string `json:"cert_dns_names,omitempty"`
	CertNotAfter string   `json:"cert_not_after,omitempty"`
	Err          string   `json:"error,omitempty"`
}

// VerifyDomains 对域名集执行本机视角验证（并发 1 串行——探测量 ≤ 每服务
// 5 × 每应用 10 的契约上界，串行足够）。探测端口 httpPort/httpsPort 与
// 入口发布端口同源（调用方传 Manager 配置；E4，S19）。
func VerifyDomains(ctx context.Context, domains []string, httpPort, httpsPort int) []DomainCheck {
	out := make([]DomainCheck, 0, len(domains))
	for _, d := range domains {
		out = append(out, verifyOne(ctx, d, httpPort, httpsPort))
	}
	return out
}

// verifyOne 验证单域名（httpPort/httpsPort 为入口发布端口，E4）。
func verifyOne(ctx context.Context, domain string, httpPort, httpsPort int) DomainCheck {
	check := DomainCheck{Domain: domain}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	ips, err := net.DefaultResolver.LookupHost(vctx, domain)
	sort.Strings(ips)
	check.IPs = ips
	check.Resolved = len(ips) > 0
	if err != nil {
		check.Err = fmt.Sprintf("resolve: %v", err)
		return check
	}
	ip := ips[0] // 探测取首个解析地址（A 记录多节点形态下逐一探测是 v0.2 面）

	// HTTP：拨向配置端口、带 Host 头的 GET（经 Traefik 的路由语义即请求头
	// 驱动；Host 判定不含端口，URL 不带端口不影响路由匹配）。
	client := &http.Client{
		Timeout: verifyTimeout,
		Transport: &http.Transport{
			DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(dctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(httpPort)))
			},
		},
	}
	req, err := http.NewRequestWithContext(vctx, http.MethodGet, "http://"+domain+"/", nil)
	if err == nil {
		resp, err := client.Do(req)
		if err != nil {
			check.HTTP80 = "unreachable: " + errMessage(err)
		} else {
			defer func() { _ = resp.Body.Close() }()
			check.HTTP80 = resp.Status
		}
	}

	// HTTPS：配置端口上的 TLS 握手（SNI = 域名；证书链照实记录，不做信任
	// 判定）。
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: verifyTimeout}, Config: &tls.Config{
		ServerName: domain,
		// InsecureSkipVerify 是「诚实记录」语义的一部分：verify 的目标是
		// 报告节点实际提供的证书（SAN/到期），不是做信任裁判——信任判定
		// 属客户端/curl --cacert 场景。
		InsecureSkipVerify: true, //nolint:gosec // 探测面如实记录证书，不做信任判定
	}}
	conn, err := dialer.DialContext(vctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(httpsPort)))
	if err != nil {
		check.HTTPS443 = "unreachable: " + errMessage(err)
		return check
	}
	tlsConn := conn.(*tls.Conn)
	state := tlsConn.ConnectionState()
	defer func() { _ = tlsConn.Close() }()
	check.HTTPS443 = "handshake ok (tls " + tlsVersionName(state.Version) + ")"
	if len(state.PeerCertificates) > 0 {
		leaf := state.PeerCertificates[0]
		check.CertSubject = leaf.Subject.String()
		check.CertDNSNames = leaf.DNSNames
		check.CertNotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
	}
	return check
}

// errMessage 提取错误的单行文案。
func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// tlsVersionName 是 TLS 版本的可读名。
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "1.0"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS13:
		return "1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}
