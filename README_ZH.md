# fleetly

[English](README.md) | [简体中文](README_ZH.md)

> Dokku 的资源占用，Railway 的 API，AI Agent 优先的操作方式。

fleetly 是面向小团队的极轻量级开源 PaaS：把 `compose.yaml` 应用部署到 1~10 台服务器的集群上，获得零停机发布、快照回滚、漂移检测，以及一套同时为人类与 AI Agent 设计的 API 面——不需要 Kubernetes。

**当前状态：早期开发中。** 设计已定稿并通过评审；T0 地基（仓库、CI 门禁、proto 契约链、错误码注册表、dind E2E 骨架）已落地。v0.1 尚未发布——见[路线图](#路线图)。曾用名 *edgesets* 与 *edgefleet*。

## 安装

干净 Linux VPS（amd64/arm64，root）上一条命令装出可运行平台——引擎门禁（Docker ≥ 29.8.1 + iptables 后端）、隐式 `docker swarm init`、systemd 开机自启，安装报告含端口暴露面提示：

```sh
curl -fsSL https://fleetly.dev/install.sh | sudo sh -            # 最新 stable
curl -fsSL https://fleetly.dev/install.sh | sudo sh - --version v0.1.0
sudo sh install.sh --bin-dir ./dist                              # 离线 / 开发形态
```

首启日志会**只打印一次** bootstrap admin token。卸载默认保留应用数据（`--purge` 才删）。控制面升级一条命令、自带升级前快照与失败自动回退（`sudo sh upgrade.sh --version vX.Y.Z`）；Engine/主机升级是另一条冷备轨——见 [`docs/runbooks/upgrade.md`](docs/runbooks/upgrade.md)。三形态、门禁清单、端口面表与 dind 验收见 [`deploy/README.md`](deploy/README.md)。（release 制品链随发布流水线落地；在那之前离线 `--bin-dir` 形态是可用路径。）

## 为什么是 fleetly

- **为没有运维的团队而建。** ≤5 名开发、无专职运维、1~3 台服务器起步、每周 1~2 小时的维护预算。一切可自动化的（证书、备份、升级、巡检）都自动化且可验证。
- **Compose 是唯一应用模型。** 没有私有 spec。受控的 Compose 规范子集 + 最小 `fleetly.*` label 约定；子集之外一律结构化报错拒绝，绝不静默忽略。
- **Docker Swarm 作底座。** 成员管理、调度、健康门更新由引擎内置——不自研分布式核心。v0.1 单节点本身就是（对用户透明的）单节点 Swarm，加第二台是 `docker swarm join`，不是重构。
- **API 优先，proto 即契约。** gRPC + REST（grpc-gateway）由同一份 protobuf 派生；CLI、Console 与（v0.2 的）MCP 都是同一契约的消费者。没有 API 的功能不准进产品。
- **信任是地板。** 原子化自升级（预拉镜像 + 快照 + 失败自动回退）、带回读校验的备份、错误信息即产品（稳定错误码 + 上下文 + 修复建议）——同时服务人类与 AI Agent。

## 能力地图（规划）

| 领域 | 行为 | 版本 |
|---|---|---|
| 部署 | git push / webhook / API → Railpack 或 Dockerfile 构建 → 零停机上线 → 观察窗 | v0.1 |
| 发布安全 | Swarm `failure-action=pause` + 平台快照重放（最近 5 个已验证版本）；不用 Swarm 原生回滚 | v0.1 |
| 路由 / TLS | 每节点 Traefik，路由与证书由控制面下发；集中 ACME（HTTP-01）、多 SAN 域名列表 | v0.1 |
| 状态 | SQLite 控制面状态，三层模型（权威 / 观测缓存 / 实时直读） | v0.1 |
| 漂移检测 | 期望态 hash 对现实；检测默认开、自动收敛 per-app opt-in | v0.1 |
| 多节点 | `docker swarm join`、镜像仓库（zot）、有状态钉住、诚实的 HA 边界 | v0.2 |
| AI Agent | MCP server，精选工具面（≤30 工具）、scope token、破坏性操作两段式确认 | v0.2 |
| 数据服务 | 托管 Postgres/Redis 模板 + 逐引擎备份适配器 + 连接串注入 | v0.2 |
| 其他 | Cron（Swarm job）、指标（VictoriaMetrics）、Web 终端（执行中继，D19） | v0.2+ |

## 诚实的边界

我们明确说清楚不做什么：不做跨节点共享存储（卷本地；有状态服务钉住节点、永不自动迁移——数据移动只走备份恢复）；**2 台 ≠ 全面 HA**（你得到的是无状态进程级 HA，不是管理面或有状态 HA——安装器会明说）；不做 CI 引擎（测试归 Git 托管方，fleetly 以 webhook 状态做发布门禁）；不做 Kubernetes 后端（k3s 是退出预案，不是功能）。

## 架构

```
CLI (fleetly) / Console / MCP (v0.2) / gRPC / REST / git push (SSH) / Webhook
                 │
   fleetlyd —— 运行于 Swarm manager 的 Go 单二进制
     API：gRPC + grpc-gateway（proto = 唯一契约真源）
     发布状态机 · 对账器 · 构建管线（Railpack/BuildKit）
     状态：SQLite (WAL) · 密钥：envelope 加密（age）· TLS：集中 ACME
                 │  Docker API（本地 socket 管理全集群）
   Docker Engine（Swarm mode）—— 服务 · overlay 网络 · 调度
   Traefik（global，每节点）—— 路由与证书由控制面下发
```

基础栈：[lynx](https://github.com/lynx-go/lynx) + [google/wire](https://github.com/google/wire)（D20），buf + [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway/v2)（D21）。领域代码零框架类型依赖——核心/适配器边界是硬纪律（D13）。

## 仓库结构

```
cmd/fleetlyd/   控制面守护进程
cmd/fleetly/    CLI
proto/            API 契约（fleetly.{server,client,console,shared}.v1）
genproto/         生成代码 + OpenAPI（openapiv2）——已提交
sdk/go/           Go SDK（gRPC client）
internal/         errcode / eventcode 注册表、应用错误信封
e2e/              dind 冒烟骨架（CI 与 Spike 复用）
docs/             设计文档、调研报告、实施规划
console/          Console 前端（React + Vite + shadcn/ui，随 T2.21 落地）
deploy/           安装器与 systemd unit（随 T2.1 落地）
```

## CLI

CLI 只经 gRPC（SDK）与守护进程通信——没有任何直开数据库或直连 Docker 的路径。所有触达平台的动词都带 `--addr`（默认 `127.0.0.1:8421`，env `FLEETLY_ADDR`）与 `--token`（env `FLEETLY_TOKEN`）；bootstrap admin token 在 fleetlyd 首启日志中**只打印一次**，后续 token 由 `fleetly tokens create` 签发。全部动词支持 `--json`；退出码 `0` 成功/无变化、`1` 错误、`2` 有变化（仅 `plan`/`diff`）、`64` 用法错误（未知动词/flag 或参数违规，EX_USAGE 惯例）。一元 RPC 带缺省 30s deadline；流式动词（`logs follow`、`events watch`）与等待动词（`deploy`、`build`、`rollback`）上 Ctrl-C 干净退出（退出码 0）。

```bash
fleetlyd &                                  # 控制面（gRPC :8421，HTTP :8420，git SSH :8424）
export FLEETLY_ADDR=127.0.0.1:8421
export FLEETLY_TOKEN=<bootstrap admin token>

fleetly validate compose.yaml               # 受控子集校验（本地）
fleetly plan compose.yaml                   # 经 API 与最近版本快照比对；退出 2 = 有变化
fleetly deploy compose.yaml                 # 入队并等待终态
fleetly apps list && fleetly deployments list my-api
fleetly logs follow my-api --service web    # 实时流（--json 为 JSONL）
fleetly env set my-api KEY value            # 随下次部署生效
fleetly rollback my-api                     # 快照重放（最近 5 版）
fleetly drift show my-api                   # 期望态 vs 实况
fleetly tokens create --scopes deploy --note CI   # 明文仅此一次显示
```

### 通过 `git push`（SSH）部署

守护进程内嵌 SSH git 端点（默认 `127.0.0.1:8424`——安全默认只绑回环；VPS 上对外时改 `git.addr` 并配防火墙）。注册公钥后向应用 bare 仓库推送：仓库根的 `compose.yaml`/`compose.yml` 即部署单元，推送到应用配置分支（默认 `main`）触发部署。

```bash
fleetly git keys add ~/.ssh/id_ed25519.pub --note laptop   # admin scope；库内只落指纹
git remote add fleetly ssh://git@127.0.0.1:8424/my-api.git
git push fleetly main                                      # → 构建 → 零停机上线
fleetly git keys list && fleetly git keys rm <id>
```

### 通过 Webhook（GitHub / Gitea）部署

先配置 per-app 签名密钥（设置后不再回显），再在 Git 托管方把 webhook 指向控制面（`POST /v1/apps/<app>/webhooks/github` 或 `/gitea`）。守护进程强制校验 HMAC-SHA256 签名、按 delivery ID 防重放（15 分钟窗口）、按 commit 幂等去重，随后拉源并入队部署。

```bash
fleetly apps webhook set-secret my-api <secret>            # ≥16 字符；admin scope
fleetly apps webhook set-source my-api https://github.com/acme/web.git \
    --branch main --auth-kind none                          # 或 https_token / ssh_key
fleetly apps webhook show my-api                           # 无敏感投影
```

完整 flag 列表见 `fleetly help <动词>`。

### Console 端（Web UI）

React SPA（Vite + Tailwind + shadcn/ui），只经带鉴权的 REST API 消费平台。构建后把产物目录配给 daemon 即可在 `/ui/` 前缀访问（静态资源不要求 token；数据仍全部走 Bearer 鉴权的 `/v1`）：

```bash
cd console && pnpm install && pnpm build      # → console/dist
fleetlyd -c config.yaml                       # 配置 console.static_dir: "./console/dist"
# 打开 http://127.0.0.1:8420/ui/  → 粘贴 API token 登录
```

覆盖：应用列表/详情（派生状态徽章）、部署（跟踪到终态）与回滚、实时日志（NDJSON 跟随 + 历史检索）、env 管理（pending 变更独立分组「待下次部署生效」）、域名管理与 verify、系统健康、平台事件流。详见 [console/README.md](console/README.md)。

## 文档

全部文档在 [`docs/`](docs/README.md)（中文，设计先行的工作流）：

- [架构设计](docs/design/2026-09-17-architecture.md)——定位、技术栈、21 项关键决策（D1–D21）、路线图
- 专项设计：[发布语义](docs/design/2026-09-17-release-semantics.md) · [stateful 放置](docs/design/2026-09-17-stateful-placement.md) · [控制面状态模型](docs/design/2026-09-17-state-model.md) · [交付流水线](docs/design/2026-09-17-delivery-pipeline.md)
- 调研：[竞品全景](docs/research/2026-09-17-competitive-landscape.md) · [Swarm 底座评估](docs/research/2026-09-17-swarm-substrate-assessment.md)
- 实施：[任务分解](docs/plan/2026-09-17-task-breakdown.md) · [v0.1 切面冻结清单](docs/plan/2026-09-17-v0.1-scope-freeze.md)

## 路线图

| 阶段 | 范围 | 状态 |
|---|---|---|
| T0 地基 | 仓库、CI 门禁、proto 契约链、错误/事件注册表、dind E2E 骨架 | ✅ 完成 |
| Spike A/B/C | 构建、发布与路由、底座风险验证（V1–V7） | 下一步 |
| v0.1 | 单节点 8 项范围 GA（部署闭环、TLS、回滚、信任闭环演练） | 开发中 |
| v0.2 | 多节点、MCP、S3 端点、托管数据库、cron、指标、Web 终端 | 规划中 |
| v0.3 | 预览环境、模板目录、RBAC、Compose 子集扩展、Tunnel 接入 | 规划中 |

## 开发

前置：Go ≥ 1.26.6（`GOTOOLCHAIN=auto` 可用）、buf CLI、Docker（e2e 用）。

```bash
go build ./...
go test ./... ./sdk/go/... -race
buf lint && buf generate          # 生成物已提交，不得漂移
golangci-lint run
```

冒烟 E2E（在 `docker:29.8.1-dind` 内运行 fleetlyd）：见 [`e2e/README.md`](e2e/README.md)。

贡献纪律：本项目设计先行——行为变更先落文档（走评审轮），再按任务分解的垂直切片落地。错误码与事件是只增注册表。

## 许可证

Apache-2.0——见 [LICENSE](LICENSE)。默认发行包不含 AGPL/DSAL 组件。
