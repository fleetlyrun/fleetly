# fleetly 技术选型（2026-09-30）

定调：**骨架照 torchwood 模式，领域依赖沿用归档仓已验证版本**。本期裁定：Console 沿用归档栈（React 19 + Vite + TS + Tailwind 4 + Radix + TanStack）；Go 三 module 从 N0 起步（主 + genproto + sdk/go，真实 tag）。

## 1. 选型总表

| 领域 | 选型 | 版本基线 | 理由与出处 |
|---|---|---|---|
| 服务骨架 | `lynx-go/lynx` + `contrib/zap` | v1.17.0（两包同版对齐） | torchwood 现行线：WithLoggerProvider/slog/RunE/DrainTimeout/OnPostStop + bootkit 共享装配 + lynxtest L2 装配测试；不复刻归档 v1.11 旧 SetLogger API |
| 网关/拦截器 | `lynx-go/grpcapi` | v0.1.0 | gateway.Dial 共享连接、NewErrorHandler+ErrorBodyBuilder、NewValidate（protovalidate）、**AssertAllRegisteredHavePolicy authz fail-closed**——与 Scope token 注解模型天然契合 |
| RPC | grpc + grpc-gateway/v2 + protobuf | 1.83.x / 2.29+ / 1.36.x | 独立端口（gRPC 与 HTTP 分开）；REST 走 grpcapi 网关机制非手写循环 |
| proto 工具链 | buf remote 插件 | go v1.36.10 / gateway v2.27.4 / grpc v1.6.0 / openapiv2 v2.27.3 | 两仓逐字相同配置已验证；buf breaking FILE 进 CI；protovalidate 引入 |
| CLI | `lynx-go/commands` | v0.3.0 | 归档验证：退出码四态（0/1/2/64）、protojson `--json`、render 单点、golden + `-update`；不引入 cobra |
| DI | `google/wire` | v0.7.0 | torchwood 模式：wire.go(wireinject)+提交 wire_gen.go+provides.go；GOWORK=off 任务见坑表 |
| 存储 | `modernc.org/sqlite` + `pressly/goose/v3` + 原生 `database/sql` | 1.59.0 / 3.28.0 | 纯 Go 零 cgo（交叉编译/容器形态友好）；**不用 ORM**——CAS/四件一拍要显式 SQL |
| 容器客户端 | `moby/moby/api` + `moby/moby/client`（新拆分 module） | 1.56.0 / 0.6.0 | 归档已验证；比 docker/docker 旧路径现代 |
| 构建 | `moby/buildkit` + `railpack`（钉版）+ `compose-go/v2`（受控子集归一化） | 0.32.2 / 0.39.0 / 2.15.0 | 归档验证；railpack 钉版防 plan 漂移（旧 spike） |
| ACME / 加密 / cron / ID | `go-acme/lego/v4`、`filippo.io/age`、`robfig/cron/v3`（仅解析）、`oklog/ulid/v2` | 4.35.2 / 1.3.2 / 3.0.1 / 2.1.1 | 归档验证 |
| 对象存储客户端 | `minio-go/v7` | 7.3.0 | ObjectStore Provider 用 |
| 配置 | viper 经 lynx ConfigSource + **config.proto 生成结构**（单一事实源） | viper 1.21 | torchwood 模式：EnvPrefix FLEETLY、`.`→`_` 替换 |
| 日志 | zap 经 contrib 作 slog provider；业务只见 `*slog.Logger` | zap 1.28 | torchwood 模式 |
| 测试 | testify（采纳）+ 自制 golden + hermetic（真 SQLite+假底座+假时钟） | 1.12.1 | torchwood 有 testify、归档无——统一采纳 |
| Console | React 19 + Vite + TS + Tailwind 4 + Radix + TanStack Query + xterm | node 24 / pnpm | 沿用归档栈（本期裁定）；openapi-typescript 生成链 + schema 漂移门禁沿用 |
| e2e | dind shell 套件 + nightly 拓扑 | — | 归档验证 |
| 工具链 | mise 钉版：go/buf/protoc/protoc-gen-go/golangci-lint/node/pnpm | golangci 2.12+ | torchwood 同款模板；golangci 取 torchwood 全集（standard + bodyclose/gosec/noctx/sqlclosecheck，max-issues=0） |

Go 版本：1.26 线（现 go.mod 的 1.25.0 需升；mise 钉最新补丁，与 torchwood/archived 对齐）。

## 2. module 与目录策略（三 module 从 N0）

```
/            github.com/fleetlyrun/fleetly        主 module：cmd/fleetlyd、cmd/fleetly、internal/*（架构文档 §2 树不变）
/genproto    github.com/fleetlyrun/fleetly/genproto   proto 生成物，独立 module，真实 tag（v0.1.0 起）
/sdk/go      github.com/fleetlyrun/fleetly/sdk/go     类型化 gRPC 客户端，真实 tag
go.work      三 module 组织
```

- 架构文档 §2 的 `internal/spec` 定位微调：**原始生成类型住 genproto module**，`internal/spec` 承接校验、归一化与包装。
- **tag 纪律**：首个可用切面即打真实 tag；主 module 禁止伪版本消费 sdk/genproto（归档教训直接导致 torchwood vendored fork）。
- wire 生成任务带 `GOWORK=off`（见坑表）。

## 3. 明确不引入

Postgres/外部数据库（单二进制零依赖部署）、bun/ORM（CAS 显式 SQL）、Redis/消息队列（进程内 admission + Outbox 足够）、Temporal 级工作流引擎（zane-ops 反面）、cobra（commands 统一）、logrus（仅 buildkit 进度残留允许，目标消除）、多容器观测重栈（fluentd/loki 全家桶——轻量红线）。

## 4. 已知坑与对策（两仓实证）

1. **wire v0.7 + go.work 不兼容**：wire 内嵌 `-mod=mod` 在 go.work 下非法——generate 任务固定 `env = { GOWORK = "off" }`（归档存档解法）。
2. **lynx 与 contrib/zap 必须同版**：归档 v1.11+v1.7 错位用了废弃 API；新仓锁同版（v1.17 线）。
3. **protojson 字段名策略全局一致**：gateway `NewMarshaler(UseProtoNames)` 与 CLI `--json` 同策略，防 Console 验收 camelCase/snake_case 割裂（归档两次踩坑）。
4. **gateway TLS 回拨**：gateway→gRPC 的 dial 必须带 TLS 断言，否则明文 dial 断全量 REST（归档 W5-S5）。
5. **commands SubDispatch 丢错误类型**：自写 subDispatchUsage 收口（归档经验）。
6. **CLI 不自动加载 cwd .env**：防恶意仓库劫持 `FLEETLY_CLI_ENDPOINT`（torchwood 守卫测试模式）。
