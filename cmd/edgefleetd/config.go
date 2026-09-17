package main

// defaultHTTPAddr 是 HTTP 面的缺省监听地址：默认只绑回环，避免控制面
// 未配置时暴露公网（架构 §4.2 安全默认基线）。可用 -addr 覆盖，或经
// -c config.yaml 中的 addr 键覆盖（显式 flag 优先于配置文件）。
const defaultHTTPAddr = "127.0.0.1:8420"

// AppConfig 应用配置，与 config.yaml 及 flags 对应（lynx Config，默认
// Viper 适配）。随阶段推进只增字段，不回收键名。
type AppConfig struct {
	// Addr 是 HTTP 面（当前仅框架 healthz 端点）的监听地址。
	Addr string `mapstructure:"addr"`
}
