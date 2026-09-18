package main

import (
	"time"

	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/secrets"
)

// defaultHTTPAddr / defaultGRPCAddr 分别是 HTTP 面与 gRPC 面的缺省监听
// 地址：默认只绑回环，避免控制面未配置时暴露公网（架构 §4.2 安全默认
// 基线）。HTTP 可用 -addr 覆盖，或经 -c config.yaml 中的 addr 键覆盖
// （显式 flag 优先于配置文件）；gRPC 经 config.yaml 的 grpc.addr 配置。
const (
	defaultHTTPAddr = "127.0.0.1:8420"
	defaultGRPCAddr = "127.0.0.1:8421"
	// defaultDBPath 是状态库缺省路径（config state.db_path 的回落值）。
	defaultDBPath = "./fleetly.db"
)

// AppConfig 应用配置，与 config.yaml 及 flags 对应（lynx Config，默认
// Viper 适配）。随阶段推进只增字段，不回收键名。
type AppConfig struct {
	// Addr 是 HTTP 面（框架 healthz 端点 + grpc-gateway 挂载的 REST /v1/**）
	// 的监听地址。
	Addr string `mapstructure:"addr"`
	// GRPC 是 gRPC 面配置（server.v1 服务承载于此，gateway 反向代理目标）。
	GRPC GRPCConfig `mapstructure:"grpc"`
	// State 是状态层配置（config 键 state.*）。
	State StateConfig `mapstructure:"state"`
	// Secrets 是平台密钥配置（config 键 secrets.*，architecture §2.3：
	// envelope 主密钥存控制面主机文件、权限保护、与备份数据分离）。
	Secrets SecretsConfig `mapstructure:"secrets"`
	// Build 是构建管线配置（config 键 build.*，T2.8）。
	Build BuildConfig `mapstructure:"build"`
	// Engine 是发布引擎配置（config 键 engine.*，T2.10/T2.11；治理参数
	// v0.1 平台默认——文件缺省即文档默认，见 engine.Config.Normalize）。
	Engine EngineConfig `mapstructure:"engine"`
	// Ingress 是入口/证书配置节（config 键 ingress.*，T2.15/T2.16；
	// Traefik 部署 + 配置端点 + 集中 ACME——缺省值经 ingress.Config.
	// Normalize 回落，单一事实源在 internal/ingress）。
	Ingress IngressConfig `mapstructure:"ingress"`
}

// EngineConfig 是发布引擎配置节（config 键 engine.*）。默认值与
// release-semantics §2.8 治理参数表一致（releaseTimeout=300s、观察窗 60s、
// 水位判定 10s、轮询 2s），经 engine.Config.Normalize 回落。
type EngineConfig struct {
	// ReleaseTimeoutSeconds 是 L2 发布看门狗秒数（engine.release_timeout_seconds；
	// 缺省 300——含 PENDING/停滞，有效值 ≥ health 预算）。
	ReleaseTimeoutSeconds int `mapstructure:"release_timeout_seconds"`
	// ObserveSeconds 是 L3 观察窗秒数（engine.observe_seconds；缺省 60）。
	ObserveSeconds int `mapstructure:"observe_seconds"`
	// ReplicasBelowSeconds 是观察窗副本水位不足判定的持续秒数
	//（engine.replicas_below_seconds；缺省 10）。
	ReplicasBelowSeconds int `mapstructure:"replicas_below_seconds"`
	// PollSeconds 是引擎轮询周期秒数（engine.poll_seconds；缺省 2）。
	PollSeconds int `mapstructure:"poll_seconds"`
	// DriftIntervalSeconds 是运行域漂移检测扫描周期秒数
	//（engine.drift_interval_seconds；缺省 30，T2.13）。
	DriftIntervalSeconds int `mapstructure:"drift_interval_seconds"`
}

// EngineSettings 把 engine.* 配置节翻译为引擎核心配置（engine.Config，
// 缺省值经 Normalize 回落——单一事实源在 internal/engine）。
func (c *AppConfig) EngineSettings() engine.Config {
	return engine.Config{
		ReleaseTimeout:   time.Duration(c.Engine.ReleaseTimeoutSeconds) * time.Second,
		ObserveWindow:    time.Duration(c.Engine.ObserveSeconds) * time.Second,
		ReplicasBelowFor: time.Duration(c.Engine.ReplicasBelowSeconds) * time.Second,
		PollInterval:     time.Duration(c.Engine.PollSeconds) * time.Second,
		DriftInterval:    time.Duration(c.Engine.DriftIntervalSeconds) * time.Second,
	}
}

// BuildConfig 是构建管线配置节（config 键 build.*）。缺省值经
// build.Config.Normalize 回落（并发 2、限额 1GiB/1.5CPU、缓存命名卷）。
type BuildConfig struct {
	// BuildkitHost 是 buildkit 端点（build.buildkit_host）；空 = 缺省
	// docker-container://fleetly-buildkit（平台自管容器）。指向外部
	// buildkitd（tcp://…）时应把 manage_daemon 置 false。
	BuildkitHost string `mapstructure:"buildkit_host"`
	// ManageDaemon 报告平台是否自管 buildkitd 容器（build.manage_daemon，
	// 缺省 true；外部端点形态置 false）。
	ManageDaemon *bool `mapstructure:"manage_daemon"`
	// DaemonContainerName 是自管 buildkitd 容器名（build.daemon_container_name）。
	DaemonContainerName string `mapstructure:"daemon_container_name"`
	// CacheVolume 是 buildkitd 内部工作缓存的持久化命名卷
	// （build.cache_volume，挂 /var/lib/buildkit）。
	CacheVolume string `mapstructure:"cache_volume"`
	// CacheDir 是 local cache 导入/导出的宿主目录（build.cache_dir；
	// 客户端侧数据根，缺省 ./build-cache）。
	CacheDir string `mapstructure:"cache_dir"`
	// ArtifactsDir 是 plan JSON / 构建日志归档根目录（build.artifacts_dir）。
	ArtifactsDir string `mapstructure:"artifacts_dir"`
	// Concurrency 是构建队列并发上限（build.concurrency；缺省 2，天花板 8）。
	Concurrency int `mapstructure:"concurrency"`
	// MemoryBytes 是 buildkitd 容器内存限额（build.memory_bytes；缺省 1GiB）。
	MemoryBytes int64 `mapstructure:"memory_bytes"`
	// CPUS 是 buildkitd 容器 CPU 限额（build.cpus；缺省 1.5）。
	CPUS float64 `mapstructure:"cpus"`
	// PollSeconds 是队列扫描周期秒数（build.poll_seconds；缺省 2）。
	PollSeconds int `mapstructure:"poll_seconds"`
}

// GRPCConfig 是 gRPC 面的配置节（config 键 grpc.*）。
type GRPCConfig struct {
	// Addr 是 gRPC 监听地址（grpc.addr）。
	Addr string `mapstructure:"addr"`
}

// IngressConfig 是入口/证书配置节（config 键 ingress.*）。字段与
// internal/ingress.Config 一一对应；缺省值在 ingress.Config.Normalize
// （traefik:v3.5 钉版、host 80/443、配置端点 :8422、LE production ACME、
// 续期窗口 30 天）。
type IngressConfig struct {
	// TraefikImage 是入口镜像（traefik_image；钉版，升级 = 改配置 +
	// 回归，不追 latest）。
	TraefikImage string `mapstructure:"traefik_image"`
	// HTTPPort / HTTPSPort 是宿主发布端口（host 模式）。
	HTTPPort  int `mapstructure:"http_port"`
	HTTPSPort int `mapstructure:"https_port"`
	// ConfigAddr 是控制面配置端点监听地址（config_addr；默认 0.0.0.0:8422
	// ——Traefik 任务经宿主 IP 访问，鉴权 token 强制）。
	ConfigAddr string `mapstructure:"config_addr"`
	// ConfigAdvertiseIP 是下发给 Traefik 的控制面可达 IP
	//（config_advertise_ip；空 = 自动探测。Docker Desktop 形态 advertise
	// addr 是 VM 内部 IP，须显式配置宿主可达地址）。
	ConfigAdvertiseIP string `mapstructure:"config_advertise_ip"`
	// TokenFile 是配置端点 bearer token 文件（token_file；首启生成）。
	TokenFile string `mapstructure:"token_file"`
	// CertDir 是证书存储根目录（cert_dir；控制面侧明文 PEM，独立备份目录
	// ——state-model §2.1；bind 挂载进 Traefik 只读）。
	CertDir string `mapstructure:"cert_dir"`
	// ACME 是集中签发器配置。
	ACME IngressACMEConfig `mapstructure:"acme"`
	// RenewBeforeDays 是续期窗口天数（renew_before_days；缺省 30）。
	RenewBeforeDays int `mapstructure:"renew_before_days"`
	// RenewScanSeconds 是续期扫描周期秒数（renew_scan_seconds；缺省 12h）。
	RenewScanSeconds int `mapstructure:"renew_scan_seconds"`
}

// IngressACMEConfig 是集中 ACME 配置（config 键 ingress.acme.*）。
type IngressACMEConfig struct {
	// Enabled 报告是否启用集中签发（enabled；缺省 true。false = 只发布
	// HTTP 路由——无域名/离线环境的显式关闭位）。
	Enabled *bool `mapstructure:"enabled"`
	// CADirURL 是 ACME 目录端点（ca_dir_url；默认 LE production，测试用
	// Pebble URL）。
	CADirURL string `mapstructure:"ca_dir_url"`
	// Email 是 ACME 账号邮箱（email）。
	Email string `mapstructure:"email"`
	// CAPoolFile 是 CA 根证书池 PEM（ca_pool_file；Pebble/私有 CA 信任）。
	CAPoolFile string `mapstructure:"ca_pool_file"`
	// AccountKeyFile 是 ACME 账号私钥文件（account_key_file；空 =
	// <cert_dir>/acme-account.key）。
	AccountKeyFile string `mapstructure:"account_key_file"`
}

// IngressSettings 把 ingress.* 配置节翻译为入口适配器核心配置（ingress.
// Config，缺省值经 Normalize 回落——单一事实源在 internal/ingress）。
func (c *AppConfig) IngressSettings() ingress.Config {
	return ingress.Config{
		TraefikImage:      c.Ingress.TraefikImage,
		HTTPPort:          c.Ingress.HTTPPort,
		HTTPSPort:         c.Ingress.HTTPSPort,
		ConfigAddr:        c.Ingress.ConfigAddr,
		ConfigAdvertiseIP: c.Ingress.ConfigAdvertiseIP,
		TokenFile:         c.Ingress.TokenFile,
		CertDir:           c.Ingress.CertDir,
		ACME: ingress.ACMEConfig{
			Enabled:        c.Ingress.ACME.Enabled,
			CADirURL:       c.Ingress.ACME.CADirURL,
			Email:          c.Ingress.ACME.Email,
			CAPoolFile:     c.Ingress.ACME.CAPoolFile,
			AccountKeyFile: c.Ingress.ACME.AccountKeyFile,
		},
		RenewBefore:       time.Duration(c.Ingress.RenewBeforeDays) * 24 * time.Hour,
		RenewScanInterval: time.Duration(c.Ingress.RenewScanSeconds) * time.Second,
	}
}

// StateConfig 是状态层配置节（config 键 state.*）。保留期天数取非正值
// 时回落注册默认（事件 30 天 / 审计 365 天——保留期是契约默认，不允许
// 误配成 0 静默关闭清理）。
type StateConfig struct {
	// DBPath 是 SQLite 状态库文件路径（state.db_path）。
	DBPath string `mapstructure:"db_path"`
	// EventRetentionDays 是事件保留天数（state.event_retention_days）。
	EventRetentionDays int `mapstructure:"event_retention_days"`
	// AuditRetentionDays 是审计保留天数（state.audit_retention_days）。
	AuditRetentionDays int `mapstructure:"audit_retention_days"`
	// DockerHost 是底座连接地址（state.docker_host）；空 = DOCKER_HOST
	// 环境变量，再缺省本机套接字。
	DockerHost string `mapstructure:"docker_host"`
}

// SecretsConfig 是平台密钥配置节（config 键 secrets.*）。主密钥文件与
// 备份数据分离保存（architecture §2.3）；权限过宽 fail-fast 拒绝启动
// （internal/secrets ErrWeakPermissions，POSIX 面）。
type SecretsConfig struct {
	// KeyPath 是 envelope 主密钥文件路径（secrets.key_path）；空 = 缺省
	// ./fleetly.key。首启不存在则生成并日志提示妥善保存。
	KeyPath string `mapstructure:"key_path"`
}

// GRPCAddr 返回 gRPC 监听地址，未配置时回落缺省值。
func (c *AppConfig) GRPCAddr() string {
	if c.GRPC.Addr == "" {
		return defaultGRPCAddr
	}
	return c.GRPC.Addr
}

// DBPath 返回状态库路径，未配置时回落缺省值。
func (c *AppConfig) DBPath() string {
	if c.State.DBPath == "" {
		return defaultDBPath
	}
	return c.State.DBPath
}

// KeyPath 返回主密钥文件路径，未配置时回落缺省值（单一事实源 =
// secrets.DefaultKeyPath）。
func (c *AppConfig) KeyPath() string {
	if c.Secrets.KeyPath == "" {
		return secrets.DefaultKeyPath
	}
	return c.Secrets.KeyPath
}

// BuildSettings 把 build.* 配置节翻译为构建管线核心配置（build.Config，
// 缺省值经 Normalize 回落——单一事实源在 internal/build）。
func (c *AppConfig) BuildSettings() build.Config {
	cfg := build.Config{
		BuildkitHost:        c.Build.BuildkitHost,
		DaemonContainerName: c.Build.DaemonContainerName,
		CacheVolume:         c.Build.CacheVolume,
		CacheDir:            c.Build.CacheDir,
		ArtifactsDir:        c.Build.ArtifactsDir,
		Concurrency:         c.Build.Concurrency,
		MemoryBytes:         c.Build.MemoryBytes,
		NanoCPUs:            int64(c.Build.CPUS * 1e9),
		PollInterval:        time.Duration(c.Build.PollSeconds) * time.Second,
		ManageDaemon:        c.Build.ManageDaemon == nil || *c.Build.ManageDaemon,
	}
	return cfg.Normalize()
}
