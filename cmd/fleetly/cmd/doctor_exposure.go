package cmd

// doctor 端口暴露自证（ADR-0036）：对配置的 edge config 端点与受管仓库
// 地址做主机分类 + TCP 拨号（公网可达即 fail/warn，自证不可达即 ok），
// 并汇报三面生效绑址（通配绑定显式警示）。分类与裁决是纯函数、探测经
// 参数注入——单测不拨真网（Windows 本机确定性）；真机行为由 dind smoke
// 锚定。

import (
	"net"
	"net/url"
	"strings"
)

// exposeClass 是主机暴露分类（探测裁决的输入）。
type exposeClass int

const (
	exposeWildcard exposeClass = iota // "" / 0.0.0.0 / ::（通配——自证无意义）
	exposeLoopback                    // 回环（外部不可达由构造保证）
	exposePrivate                     // 私网（RFC1918/CGNAT/链路本地/ULA——VPC 形态）
	exposePublic                      // 公网 IP 或域名（域名按最暴露假设处理）
)

// classifyHost 把地址主机部归入暴露分类。解析不了的视为公网（最暴露
// 假设——自证检查宁误报勿漏报）。
func classifyHost(host string) exposeClass {
	switch host {
	case "", "0.0.0.0", "::":
		return exposeWildcard
	case "localhost":
		return exposeLoopback
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return exposePublic
	}
	if ip.IsLoopback() {
		return exposeLoopback
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return exposePrivate
	}
	// CGNAT 100.64/10（云 VPC 常见形态）不在 IsPrivate 内，显式补。
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] < 128 {
		return exposePrivate
	}
	return exposePublic
}

// exposureTarget 是一次暴露自证的解析后目标。
type exposureTarget struct {
	host     string
	port     string
	parseErr bool // 目标形态不合法（裸报 warn，不探测）
}

// parseEndpointTarget 解析 edge config 端点 URL（http://host[:9082]/path；
// 裸 host:port 宽容兼容，缺端口回退既知监听端口）。
func parseEndpointTarget(raw string) exposureTarget {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if host, port, serr := net.SplitHostPort(raw); serr == nil && host != "" && validPort(port) {
			return exposureTarget{host: host, port: port}
		}
		return exposureTarget{parseErr: true}
	}
	port := u.Port()
	if port == "" {
		port = configEdgeConfigPort
	}
	return exposureTarget{host: u.Hostname(), port: port}
}

// parseRegistryTarget 解析受管仓库地址（host:port；纯主机宽容回退既知
// 仓库端口）。
func parseRegistryTarget(raw string) exposureTarget {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || !validPort(port) {
		host = strings.Trim(raw, "[]")
		port = configRegistryPort
		if host == "" {
			return exposureTarget{parseErr: true}
		}
	}
	return exposureTarget{host: host, port: port}
}

// validPort 校验端口部是 1-5 位数字（SplitHostPort 不验数字——"http://"
// 会被拆成 host=http port=/，裸形态兜底必须自证端口合法）。
func validPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// probeFn 是端口探测接缝（doctor.go 的 probePort 同形；参数注入保单测
// hermetic）。
type probeFn func(addr string) error

// exposureCheck 对单一面对配置地址做"公网可达性自证"（ADR-0036 决策 2）：
// 公网可达 → publicStatus（edge config=fail / registry=warn）；公网不可达
// → ok（self-certified，本检查的核心价值）；私网/回环可达 → ok（边界 =
// 云防火墙，如实声明）；未监听 → warn；未配置/通配/非法 → 相应豁免态。
func exposureCheck(name, rawTarget string, tgt exposureTarget, unsetDetail, publicStatus string, probe probeFn) doctorCheck {
	if rawTarget == "" {
		return doctorCheck{Name: name, Status: checkOK, Detail: unsetDetail}
	}
	if tgt.parseErr {
		return doctorCheck{
			Name: name, Status: checkWarn, Detail: "invalid target " + rawTarget,
			Advice: "pass a host:port address (registry) or an http URL (edge config endpoint)",
		}
	}
	target := net.JoinHostPort(tgt.host, tgt.port)
	switch classifyHost(tgt.host) {
	case exposeWildcard:
		return doctorCheck{
			Name: name, Status: checkWarn, Detail: "wildcard target " + target,
			Advice: "pin a concrete loopback/VPC address to enable reachability self-certification",
		}
	case exposePublic:
		if err := probe(target); err == nil {
			return doctorCheck{
				Name: name, Status: publicStatus,
				Detail: "publicly reachable at " + target,
				Advice: "block public access at the cloud firewall/security group, or pin the address to a VPC-private one (runbook: port exposure matrix)",
			}
		}
		return doctorCheck{
			Name: name, Status: checkOK,
			Detail: "not publicly reachable at " + target + " (self-certified)",
		}
	}
	// 回环/私网：探测监听态（不可达即 face 未起——安装预检口径同族）。
	if err := probe(target); err != nil {
		return doctorCheck{
			Name: name, Status: checkWarn, Detail: "not listening on " + target,
			Advice: "expected on machines without fleetlyd; if this face should be up, check the daemon config and logs",
		}
	}
	return doctorCheck{
		Name: name, Status: checkOK,
		Detail: "listening on " + target + " (non-public address; the cloud firewall is the exposure boundary)",
	}
}

// edgeConfigExposure 组装 edge config 面检查（公网可达 = fail：无认证
// 端点，公网可达即任意人可改写全量路由）。
func edgeConfigExposure(raw string, probe probeFn) doctorCheck {
	return exposureCheck("edge config exposure", raw, parseEndpointTarget(raw),
		"not configured (edge config endpoint stays disabled)", checkFail, probe)
}

// registryExposure 组装受管仓库面检查（公网可达 = warn：明文 HTTP + 单一
// 平台凭证，authenticated 暴露烈度低于无认证的 edge config 面）。
func registryExposure(raw string, probe probeFn) doctorCheck {
	return exposureCheck("registry exposure", raw, parseRegistryTarget(raw),
		"not configured (managed registry stays disabled)", checkWarn, probe)
}

// bindSurfaceCheck 汇报三面生效绑址（ADR-0036 决策 2②）：通配绑定
// （0.0.0.0/::/:port）显式 warn + 钉址/防火墙双通道处置建议。缺省配置即
// 通配——如实呈报，不粉饰。
func bindSurfaceCheck(binds []struct{ name, addr, key string }) doctorCheck {
	detail := make([]string, 0, len(binds))
	var wildcards []string
	for _, b := range binds {
		detail = append(detail, b.name+" "+b.addr)
		if classifyHost(hostPart(b.addr)) == exposeWildcard {
			wildcards = append(wildcards, b.key)
		}
	}
	c := doctorCheck{
		Name:   "bind surface",
		Detail: strings.Join(detail, ", "),
	}
	if len(wildcards) > 0 {
		c.Status = checkWarn
		c.Detail += " (wildcard: " + strings.Join(wildcards, ", ") + ")"
		c.Advice = "wildcard binds listen on every interface; pin the listed config keys to a loopback/VPC address, or enforce the VPC firewall boundary (runbook: port exposure matrix)"
		return c
	}
	c.Status = checkOK
	c.Detail += " (pinned)"
	return c
}

// hostPart 剥监听地址的主机部（":9080" → ""；"127.0.0.1:9080" → 原样）。
// 无端口裸地址按主机处理（分类兜底）。
func hostPart(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// doctorExposureEnv 是 doctor 消费的 daemon 侧 env（与 install.sh 注入
// unit 的同一组——自证地址来源与 daemon 配置同键，ADR-0036）。
const (
	envEdgeConfigEndpoint = "FLEETLY_EDGE_CONFIG_ENDPOINT"
	envRegistryAddr       = "FLEETLY_REGISTRY_ADDR"
	// 探测缺省端口（目标缺端口时的宽容回退——两个面的既知监听端口）。
	configEdgeConfigPort = "9082"
	configRegistryPort   = "5000"
)

// exposureTargets 是 doctor 的暴露自证输入集（旗标解析产物；测试直接
// 构造以保 hermetic）。exposure 两面的地址旗标缺省时回退 daemon env
// （同键文化）；bind 三面缺省 = config 缺省（config 包常量单一真源）。
type exposureTargets struct {
	edgeEndpoint string
	registryAddr string
	bindGRPC     string
	bindHTTP     string
	bindEdge     string
}

// resolved 补齐 env 兜底（旗标显式传入优先）。
func (f exposureTargets) resolved() exposureTargets {
	t := f
	if t.edgeEndpoint == "" {
		t.edgeEndpoint = envOr(envEdgeConfigEndpoint, "")
	}
	if t.registryAddr == "" {
		t.registryAddr = envOr(envRegistryAddr, "")
	}
	return t
}

// runExposureChecks 跑三条暴露自证检查（edge config/registry/bind surface）。
func runExposureChecks(ex exposureTargets, probe probeFn) []doctorCheck {
	return []doctorCheck{
		edgeConfigExposure(ex.edgeEndpoint, probe),
		registryExposure(ex.registryAddr, probe),
		bindSurfaceCheck([]struct{ name, addr, key string }{
			{"grpc", ex.bindGRPC, "server.grpc.addr"},
			{"gateway http", ex.bindHTTP, "server.http.addr"},
			{"edge config", ex.bindEdge, "server.edge_config.addr"},
		}),
	}
}
