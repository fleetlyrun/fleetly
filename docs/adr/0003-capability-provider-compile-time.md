# Capability/Provider 统一插件模型：编译期注册，进程外协议延后

运行时、日志、观测、存储、镜像仓库、构建、接入七类一律建模为 Capability（端口）+ Provider（实现），统一注册表与配置选定。机制取舍：先做编译期 Go interface 注册（单二进制、零 RPC 开销），**不做** porter 式 gRPC 进程外插件协议——当前没有第三方插件作者，跨进程协议的复杂度过不了 deletion test（继承归档 ADR-0016/0017/0018 文化）。但契约类型全部定义在 proto，未来抽进程外插件是机械翻译，不需改语义。

## Consequences

- 新 Provider = `internal/providers/` 下一个包 + init 注册 + 配置一行，不触碰引擎。
- 每个 Capability 同期恰有一个在册 Provider；多实例并行属于未来的编排面，不是插件面。
