package capability

// Route host/path 白名单校验（安全批共享真源）：host/path 会原样内插进
// traefik 路由规则的反引号定界符内（Host(`%s`) / PathPrefix(`%s`)）——
// 反引号等规则元字符可注入/劫持路由；host 又是平台级命名空间（跨项目
// 同 host 双路由会让 Proxy 同名 router 互覆）。API 受理面与 Proxy Provider
// 纵深面共用本函数（单真源，两处不漂移）。非法形态一律拒绝，不做清洗
// 改写（清洗=语义漂移，fail-closed）。

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// routeHostRe 是 host 白名单：可选 `*.` 通配前缀 + 点分 DNS 标签（每段
// 字母数字开头结尾、中间可带连字符、≤63 字节；单标签与单标签 IP 形态
// 天然可过——sslip.io 调试域与 traefik 单标签主机不受影响）。
var routeHostRe = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// ValidateRouteHost 校验 Route host（总长 ≤253；白名单外——反引号、空白、
// 控制字符、下划线、协议前缀等——一律拒绝）。
func ValidateRouteHost(host string) error {
	if host == "" || len(host) > 253 || !routeHostRe.MatchString(host) {
		return fmt.Errorf("host: must be a DNS hostname (optionally with a leading *., labels of letters/digits/hyphen, max 253 bytes)")
	}
	return nil
}

// routePathAllowed 是 path 白名单字符集（RFC 3986 pchar 加 `/` 与 `%`；
// 刻意不含反引号——traefik 规则的定界符，出现即注入面）。
const routePathAllowed = "/._~!$&'()*+,;=:@%-"

// ValidateRoutePath 校验 Route path：空 = 根；否则以 `/` 开头、仅含白名单
// 字符、长度 ≤1024。
func ValidateRoutePath(path string) error {
	if path == "" {
		return nil
	}
	if len(path) > 1024 || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path: must be empty or a path starting with '/' (max 1024 bytes)")
	}
	for i := 0; i < len(path); i++ {
		c := path[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte(routePathAllowed, c) >= 0
		if !ok {
			return fmt.Errorf("path: character %q is not allowed; use letters, digits and one of %s", string(c), routePathAllowed)
		}
	}
	return nil
}

// ValidateRouteAuthAddress 校验 Route 门禁地址（ADR-0051）：ForwardAuth
// 目标必须是 http/https 绝对 URL（platform authorize 端点）——编排器把它
// 原样递给 Proxy，非 URL 形态即配置错误，fail-closed。
func ValidateRouteAuthAddress(address string) error {
	if address == "" {
		return fmt.Errorf("auth address: must not be empty")
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("auth address: must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("auth address: scheme must be http or https")
	}
	return nil
}
