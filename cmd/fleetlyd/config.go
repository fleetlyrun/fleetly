package main

import (
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
}

// GRPCConfig 是 gRPC 面的配置节（config 键 grpc.*）。
type GRPCConfig struct {
	// Addr 是 gRPC 监听地址（grpc.addr）。
	Addr string `mapstructure:"addr"`
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
