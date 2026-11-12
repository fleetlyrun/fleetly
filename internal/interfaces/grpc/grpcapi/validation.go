package grpcapi

import (
	"net"
	"net/url"
	"strings"
	"unicode"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// RejectPrivateHost 拒绝指向环回/私网/链路本地/未指定段的 URL host
// （P0-7.3，api_base 与 repo 的单源 SSRF 防线）：两者都以控制面身份向 URL
// host 发起出站请求——写权者可配 https://10.x 形态让控制面探测内网且上游
// message 原样回显。只判字面 IP 与 localhost 名（校验位不做 DNS 解析——
// 解析即 TOCTOU，且受理面无解析依赖）；DNS 名形态的内网指向不在此列。
func RejectPrivateHost(field, host string) error {
	h := strings.ToLower(host)
	if h == "" {
		return apperr.New("E_INVALID_ARGUMENT", "%s: URL host must not be empty", field)
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return apperr.New("E_INVALID_ARGUMENT",
			"%s: must not point at localhost (got %q)", field, host)
	}
	ip := net.ParseIP(h)
	if ip == nil {
		// 纯数字/点字面（"2130706433"、"0177.0.0.1"）不是合法 IP 词法但
		// libc 解析器可能按十进制/八进制 IPv4 拨号——字面数字面一律拒。
		if !strings.ContainsAny(h, ":") && strings.Trim(h, "0123456789.") == "" {
			return apperr.New("E_INVALID_ARGUMENT",
				"%s: numeric host forms are not accepted (got %q); use a DNS name or a dotted IPv4 literal", field, host)
		}
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return apperr.New("E_INVALID_ARGUMENT",
			"%s: must not point at loopback, private, link-local or unspecified addresses (got %q)", field, host)
	}
	return nil
}

// ValidateGitRepo 校验 hook repo 形态（https-only、无内嵌材料、无空白/
// 控制字符、私网段拒绝）。repo 会冻结进 Revision 并由控制面以 root 拉取。
func ValidateGitRepo(repo string) error {
	r := strings.TrimSpace(repo)
	if !strings.HasPrefix(r, "https://") {
		return apperr.New("E_INVALID_ARGUMENT",
			"repo: only https:// direct repository URLs are supported (got %q); http, git, ssh, file and ext transports are rejected", r)
	}
	// 内嵌访问材料拒绝（ADR-0073 阶段 A 决策 6）：repo 冻结进 Revision 并
	// 出现在读面——token 形态 URL 会把材料明文泄入四个读面。访问材料走
	// GitHub App 或 git:<host> Project Secret（构建期解析）。
	if u, perr := url.Parse(r); perr == nil && u.User != nil {
		return apperr.New("E_INVALID_ARGUMENT",
			"repo: must not embed access material in the URL — it freezes into revisions and shows up in read faces; configure a github app or a git:<host> project secret instead")
	}
	for i := 0; i < len(r); i++ {
		if c := rune(r[i]); unicode.IsSpace(c) || unicode.IsControl(c) {
			return apperr.New("E_INVALID_ARGUMENT",
				"repo: must not contain whitespace or control characters (byte offset %d)", i)
		}
	}
	// 私网/环回/链路本地段拒绝（RejectPrivateHost 单源，P0-7.3）：repo 由
	// 控制面以 root 拉取——与 api_base 同一 SSRF 防线。
	if u, perr := url.Parse(r); perr == nil {
		if err := RejectPrivateHost("repo", u.Hostname()); err != nil {
			return err
		}
	}
	return nil
}
