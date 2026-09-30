# fleetly

fleetly 是一个轻量 PaaS：把源码或镜像变成运行在可插拔运行时上的工作负载，并提供流量接入、托管数据服务与观测。人类与 AI Agent 同为一等用户。

- 词汇（唯一真源，冻结）：[CONTEXT.md](CONTEXT.md)
- 架构 / 领域模型 / 选型：`docs/design/`
- 决策记录：`docs/adr/`
- v1 功能清单与批次：`docs/plan/2026-09-30-feature-checklist.md`

## 开发

```sh
mise install          # 钉版工具链（go/buf/protoc/protoc-gen-go/golangci-lint）
mise run generate:all # proto + config + wire 生成
mise run dev:server   # 启动 fleetlyd（gRPC :9080 / REST :9081）
go run ./cmd/fleetly version
```

约定见 [AGENTS.md](AGENTS.md)。
