# fleetly

fleetly 是一个轻量 PaaS：把源码或镜像变成运行在可插拔运行时上的工作负载，并提供流量接入、托管数据服务与观测。人类与 AI Agent 同为一等用户。

- 词汇（唯一真源，冻结）：[CONTEXT.md](CONTEXT.md)
- 架构 / 领域模型 / 选型：`docs/design/`
- 决策记录：`docs/adr/`
- v1 功能清单与批次：`docs/plan/2026-09-30-feature-checklist.md`

## 安装（F0.1，宿主 Linux）

```sh
curl -fsSL https://fleetly.dev/install.sh | sudo sh
# 脚本：Docker 检测/安装 → swarm init → fleetlyd（systemd）→ 健康等待
# 下一步：cat /var/lib/fleetly/bootstrap-token && fleetly init --token <...> admin
```

发布通道是 GitHub Releases（首个 tag 前该 URL 404，从源码构建走下方开发流）；
本地二进制安装形态 `FLEETLY_BIN_DIR=... sh install.sh`（e2e 冒烟同款）。

## 开发

```sh
mise install          # 钉版工具链（go/buf/protoc/protoc-gen-go/golangci-lint）
mise run generate:all # proto + config + wire 生成
mise run dev:server   # 启动 fleetlyd（gRPC :9080 / REST :9081）
go run ./cmd/fleetly version
```

约定见 [AGENTS.md](AGENTS.md)。
