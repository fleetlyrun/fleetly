// Package ingress 是入口与证书适配器（T2.15/T2.16；架构 §2.6 入口真源）：
// 平台自管 Traefik（global service 每节点，v0.1 单机 1 实例）、路由与证书
// 经 HTTP provider 由控制面集中下发、控制面内嵌 ACME（lego）集中签发。
//
// 纪律（Spike B 实测，spike/b/README.md §6——硬约束）：
//   - Traefik 只拒绝「显式空 map」与解析失败，**裸 {} 会清空全部路由**；
//     因此控制面合成配置必须保证 http.routers/services 键存在且非空，
//     先校验后写（Validate），坏配置不换入内存视图、Traefik 端永远取
//     不到残缺配置；
//   - 配置服务不可达时 Traefik 保留上一份成功配置（原生兜底，实测）；
//   - 路由发布严格晚于健康门（release-semantics §2.5 不变量）——本包的
//     PublishRoutes 由发布引擎在首健康→切流之后调用。
//
// 核心不出现第三方概念的一般化纪律在本包的反向落实：Traefik/lego 类型
// 只存在于本包内部，出口是 Route/DynamicConfig 等本包类型；发布引擎经
// engine.RoutePublisher 端口（本包 Manager 隐式实现）消费，不感知
// Traefik 存在。
package ingress

import (
	"fmt"
	"time"
)

// 平台钉版与保守缺省（architecture §2.6 入口行 + §2.5 连接治理行）。
const (
	// DefaultTraefikImage 是 Traefik 钉版镜像（Spike B 实测版本 v3.5；
	// 升级走镜像钉版变更 + 回归，不追 latest）。
	DefaultTraefikImage = "traefik:v3.5"
	// TraefikCertMountPath 是证书目录在 Traefik 容器内的只读挂载点
	//（tls.certificates 的 certFile/keyFile 以该路径书写）。
	TraefikCertMountPath = "/fleetly-certs"
	// DefaultServersTransportIdleTimeout 是 serversTransport 空闲连接
	// 回收时长。依据 V4 实测（architecture §2.5 连接治理行）：keep-alive
	// 连接池会复用已退出的任务，治理主键是应用侧优雅退出，
	// serversTransport 降为辅助——只治理空闲池（90s 默认 → 15s 保守值），
	// 对 in-flight 请求无效。
	DefaultServersTransportIdleTimeout = 15 * time.Second
	// DefaultServersTransportDialTimeout 是后端拨号超时（保守值）。
	DefaultServersTransportDialTimeout = 5 * time.Second
	// DefaultRenewBefore 是证书续期窗口（到期前 30 天，T2.16 交付物）。
	DefaultRenewBefore = 30 * 24 * time.Hour
	// DefaultRenewScanInterval 是续期扫描周期。
	DefaultRenewScanInterval = 12 * time.Hour
	// DefaultACMECADirURL 是 LE production 目录端点（测试用 Pebble：
	// 配置 ingress.acme.ca_dir_url 指向本地 pebble /dir）。
	DefaultACMECADirURL = "https://acme-v02.api.letsencrypt.org/directory"
	// acmeChallengePathPrefix 是 HTTP-01 挑战路径前缀（ACME 契约常量；
	// 各节点 Traefik 把它反代到控制面挑战应答端点，架构 §2.6 证书行）。
	acmeChallengePathPrefix = "/.well-known/acme-challenge/"
)

// Config 是入口适配器配置（config 键 ingress.*；缺省值经 Normalize 回落，
// 单一事实源在本包）。
type Config struct {
	// TraefikImage 是入口镜像（钉版；traefik_image）。
	TraefikImage string
	// HTTPPort / HTTPSPort 是宿主发布端口（host 模式 80/443；http_port/
	// https_port）。
	HTTPPort  int
	HTTPSPort int
	// ConfigAddr 是控制面配置端点监听地址（config_addr）。默认 0.0.0.0
	// ——Traefik 任务（容器 netns）须经宿主 IP 访问；鉴权 token 强制，
	// 非回环绑定的暴露面由 token 承担（取舍见 provider.go 注释）。
	ConfigAddr string
	// ConfigAdvertiseIP 是下发给 Traefik 的控制面可达 IP
	//（config_advertise_ip；空 = 自动探测：Swarm advertise addr 优先，
	// 出口本地地址兜底）。Docker Desktop 形态 advertise addr 是 VM 内部
	// IP，须显式配置为宿主可达地址。
	ConfigAdvertiseIP string
	// TokenFile 是配置端点 bearer token 的持久化文件（token_file；首启
	// 生成，与 Traefik 静态配置 --providers.http.headers 同步传递）。
	TokenFile string
	// CertDir 是证书存储根目录（cert_dir；控制面侧明文 PEM，文件权限
	// 0600）。state-model §2.1：证书材料属控制面、独立备份目录。Traefik
	// 经平台自管命名卷（CertVolume）消费证书——控制面签发后经 seed 容器
	// 复制进卷（swarm 不接受 Windows 宿主路径 bind 挂载，实机验证结论；
	// Linux 单机 bind 亦被卷方案统一替代——一条代码路径）。
	CertDir string
	// CertVolume 是 Traefik 证书只读挂载的命名卷（cert_volume）。
	CertVolume string
	// CertSeedImage 是证书 seed 容器镜像（cert_seed_image；stopped 容器 +
	// docker API 拷贝，卷的持久化让 Traefik 重启不丢证书）。
	CertSeedImage string
	// ACME 是集中签发器配置。
	ACME ACMEConfig
	// RenewBefore 是续期窗口（到期前；renew_before_days）。
	RenewBefore time.Duration
	// RenewScanInterval 是续期扫描周期（renew_scan_seconds）。
	RenewScanInterval time.Duration
	// PollInterval 是 Traefik 拉取配置的轮询周期（静态配置参数；供收敛
	// 等待逻辑对齐节奏）。
	PollInterval time.Duration
}

// ACMEConfig 是集中 ACME 配置（config 键 ingress.acme.*）。
type ACMEConfig struct {
	// CADirURL 是 ACME 目录端点（ca_dir_url；默认 LE production，测试用
	// Pebble URL）。
	CADirURL string
	// Email 是 ACME 账号邮箱（email；空 = 无 contact 注册）。
	Email string
	// CAPoolFile 是 CA 根证书池 PEM（ca_pool_file；Pebble/私有 CA 场景
	// 信任自定义根）。空 = 系统信任池。
	CAPoolFile string
	// AccountKeyFile 是 ACME 账号私钥文件（account_key_file；空 =
	// <CertDir>/acme-account.key）。
	AccountKeyFile string
	// Enabled 报告是否启用集中签发（enabled；false = 只发布路由不下发
	// TLS 证书——无域名/离线环境的显式关闭位）。nil 缺省 = true。
	Enabled *bool
}

// ACMEEnabled 报告集中签发是否启用。
func (c ACMEConfig) ACMEEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Normalize 回落文档缺省值。
func (c Config) Normalize() Config {
	if c.TraefikImage == "" {
		c.TraefikImage = DefaultTraefikImage
	}
	if c.HTTPPort == 0 {
		c.HTTPPort = 80
	}
	if c.HTTPSPort == 0 {
		c.HTTPSPort = 443
	}
	if c.ConfigAddr == "" {
		c.ConfigAddr = "0.0.0.0:8422"
	}
	if c.TokenFile == "" {
		c.TokenFile = "fleetly-ingress.token"
	}
	if c.CertDir == "" {
		c.CertDir = "fleetly-certs"
	}
	if c.CertVolume == "" {
		c.CertVolume = "fleetly-ingress-certs"
	}
	if c.CertSeedImage == "" {
		c.CertSeedImage = "alpine:3.20"
	}
	if c.ACME.CADirURL == "" {
		c.ACME.CADirURL = DefaultACMECADirURL
	}
	if c.ACME.AccountKeyFile == "" {
		c.ACME.AccountKeyFile = ""
	}
	if c.RenewBefore <= 0 {
		c.RenewBefore = DefaultRenewBefore
	}
	if c.RenewScanInterval <= 0 {
		c.RenewScanInterval = DefaultRenewScanInterval
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	return c
}

// Validate 校验配置不变量（fail-fast 于装配期）。
func (c Config) Validate() error {
	n := c.Normalize()
	if n.HTTPPort <= 0 || n.HTTPSPort <= 0 {
		return fmt.Errorf("ingress: http/https ports must be positive")
	}
	if n.ACME.ACMEEnabled() && n.ACME.CADirURL == "" {
		return fmt.Errorf("ingress: acme.ca_dir_url is required when acme enabled")
	}
	return nil
}
