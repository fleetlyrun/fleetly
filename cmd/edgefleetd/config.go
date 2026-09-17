package main

// defaultHTTPAddr / defaultGRPCAddr 分别是 HTTP 面与 gRPC 面的缺省监听
// 地址：默认只绑回环，避免控制面未配置时暴露公网（架构 §4.2 安全默认
// 基线）。HTTP 可用 -addr 覆盖，或经 -c config.yaml 中的 addr 键覆盖
// （显式 flag 优先于配置文件）；gRPC 经 config.yaml 的 grpc.addr 配置。
const (
	defaultHTTPAddr = "127.0.0.1:8420"
	defaultGRPCAddr = "127.0.0.1:8421"
)

// AppConfig 应用配置，与 config.yaml 及 flags 对应（lynx Config，默认
// Viper 适配）。随阶段推进只增字段，不回收键名。
type AppConfig struct {
	// Addr 是 HTTP 面（框架 healthz 端点 + grpc-gateway 挂载的 REST /v1/**）
	// 的监听地址。
	Addr string `mapstructure:"addr"`
	// GRPC 是 gRPC 面配置（server.v1 服务承载于此，gateway 反向代理目标）。
	GRPC GRPCConfig `mapstructure:"grpc"`
}

// GRPCConfig 是 gRPC 面的配置节（config 键 grpc.*）。
type GRPCConfig struct {
	// Addr 是 gRPC 监听地址（grpc.addr）。
	Addr string `mapstructure:"addr"`
}

// GRPCAddr 返回 gRPC 监听地址，未配置时回落缺省值。
func (c *AppConfig) GRPCAddr() string {
	if c.GRPC.Addr == "" {
		return defaultGRPCAddr
	}
	return c.GRPC.Addr
}
