# fleetly

[English](README.md) | **简体中文**

fleetly 是一个轻量级自托管 PaaS：把镜像或源码变成运行在可插拔运行时上的工作负载，再围绕它们接好流量入口、托管数据服务与观测。人类与 AI Agent 同为一等用户——CLI、API、Console、文档的每个面，都同时为「人在终端敲命令」与「Agent 自主驱动」两种形态而设计。

## 为什么是 fleetly

- **宿主单二进制，零外部依赖。** 控制面 `fleetlyd` 内嵌 SQLite（WAL）存储，并自宿它需要的全部五个受管组件：zot 镜像仓、traefik Proxy、VictoriaLogs 日志库、VictoriaMetrics 指标库、每节点 cAdvisor 采集端。一台全新 Linux 机器，一条命令到公网可访问的 HTTPS 应用。
- **声明式内核。** 受理面把一切来源——compose 文件、镜像引用、spec——归一化为不可变的 Revision（冻结 Spec）；引擎持续把现实收敛到声明、检出漂移、必要时重放最后的好状态。回滚就是一次对旧 Revision 的 Replay。
- **deploy 级变更有预览与票据。** 此类动词先产出变更预览（spec diff、逐 Workload 后果判定、结构化警告）；交互终端以 y/N 确认，非交互调用传 `--yes`，或走 `--preview` / `--ticket` 两调用流。
- **每个动词都有机器契约。** 全命令 `--json`（protojson snake_case）、稳定退出码（`0`/`1`/`2`/`64`）、errcode + 处置提示 + 文档链接的错误信封、自描述面（`fleetly schema`、`fleetly explain`）；`skills/` 内置现成的 Agent 技能。
- **架构由测试承载。** 冻结词汇（[CONTEXT.md](CONTEXT.md)——更名即设计缺陷）、决策全走 ADR、契约测试执法分层与禁词与注册表。

## 功能

### 部署与运行

- 部署来源四种：镜像引用、源码树（`dockerfile` / `railpack` / `static` 构建器）、上传产物、git push（私有仓走 GitHub App 一等接入，或原生 webhook）。
- Compose 受理面 = 官方 compose-spec 语法 + `x-fleetly-*` 扩展键，由 compose-go 解析并做 schema 校验；归一化原文冻结进 Revision。
- Process 级部署策略：rolling（健康门内逐代替换，缺省）与 blue-green。
- Variable 三级合成（Project 共享、源声明、App 级）冻结进 Revision；Secret 落盘加密、永不回显——读面只见指纹。
- Task 双形态：one-shot 一次性执行与 resident 常驻实例池（属主租约 + 专属网络组）；Schedule 是带时区的 cron，按点铸造 Run。
- 执行会话（`fleetly exec`、`fleetly shell`、Web 终端）经节点出站中继送达——节点零入站端口。

### 托管数据服务

- **Database**——postgres、pgvector、mysql、mongo、redis。
- **PubSub**——nats、kafka、redpanda（kafka 方言兼容在目录中互示），带 Topic / Stream 子资源。
- **Bucket Store**——silo，S3 兼容存储，per-Project 访问密钥。
- 引擎目录（Engine Catalog）自描述引擎、钉版镜像、缺省与弃用版本、旋钮 schema；`databases upgrade` 以可断点重入的向导走主版本升级。
- 每实例双车道配置：static 旋钮滚动重启生效，dynamic 旋钮免重启即时生效。
- 备份逐引擎 verify 与恢复演练；Platform Backup（restic）保护控制面自身。

### 流量与网络

- Route 把 `host/path → Process 端口` 映射出去，协议 http / h2c / tcp，TLS 逐路由（ACME 托管或上传证书）。
- Project 级网络，可跨 Project peer 互挂，可声明 `egress:none` 隔离（k3s 上由 NetworkPolicy 执法）。

### 集群

- 同一 Runtime 能力端口后可插拔：Docker Swarm 缺省，k3s 第二运行时。
- 节点凭加入材料接入（`fleetly nodes enroll`），可 drain / cordon；节点上零平台安装物——节点视图只是观测缓存。
- Volume 缺省钉住节点；Placement 约束显式声明。

### 观测与治理

- 日志：实时流 + 持久化检索；构建日志流式输出，出口单点脱敏。
- 指标走 PromQL；per-App 告警规则；通知通道（webhook、telegram）。
- 事件携单调序号（SSE follow），与只增不裁的审计台账并行。
- 读时计算的诊断引擎把观测变成 Finding，Fault Class 回答「往哪看」——平台的错永远不会被讲成你的错；Platform Incident 讲清波及面。
- 身份与权限：Team / Role / Scope（`resource:action`，write 蕴含 read）、可吊销 Token、一次性邀请；Change Freeze 可全局或按 Team 开变更冻结窗。

### 使用面

- **Console**——内嵌于 daemon 的 React SPA：部署、日志、Web 终端、quickstart 向导、模板库、按需数据库浏览会话（pgweb / redis-commander / Adminer / Mongoku）、审计与设置。
- **CLI**——`fleetly`，同一 API 之上的瘦客户端；每个动词同时服务人类与机器两种渲染。
- **API 与 SDK**——同一 proto 契约出 gRPC `:9080` 与 REST `:9081`；类型化 Go SDK 在 `sdk/go`。

## 架构一览

```
fleetly CLI · Go SDK · AI Agent · Console（Web）
        │   gRPC :9080 · REST :9081——同一 proto 契约
        ▼
fleetlyd——控制面单二进制（systemd 优先）
  ├─ 内嵌 SQLite（WAL）+ goose 迁移
  ├─ engine：单写者 + 可插拔应用模块的收敛环
  ├─ 五件受管自宿组件：
  │    zot 镜像仓 :5000 · traefik Proxy · VictoriaLogs :9428
  │    VictoriaMetrics :8428 · cAdvisor :8080（每节点）
  ▼  Runtime 能力端口——Provider 模型（Docker Swarm 缺省 · k3s）
节点——零平台安装物，凭加入材料接入，exec 经出站中继
```

- 七类能力端口（Runtime、Builder、Registry、Proxy、Logging、Metrics、ObjectStore）；每类同期恰有一个在册 Provider，由配置选定。
- Clean 四环分层——`domain` → `application` → `infrastructure` → `interfaces`，加 `assembly` 组合根——依赖方向由契约测试执法，不靠自觉。
- 深入阅读：[docs/design/2026-09-30-architecture.md](docs/design/2026-09-30-architecture.md)。

## 快速开始

Linux，amd64 或 arm64：

```sh
# 1) 一行安装：OS/arch 检测 → 二进制 → Docker 检测/安装 → swarm init
#    → fleetlyd（systemd）→ 健康等待
curl -fsSL https://fleetly.sh/install.sh | sudo sh

# 2) 消费 bootstrap token 完成首个管理员初始化
cat /var/lib/fleetly/bootstrap-token
fleetly init --token <token> admin

# 3) 样例应用一条龙：project/app → 镜像部署 → sslip.io Route
#    → 等待 succeeded → 输出访问 URL（零 DNS 配置；--tls auto 走 ACME）
fleetly quickstart
```

发布通道是 GitHub Releases；首个 tag 发布前该通道诚实地 404——在那之前请从源码构建。`FLEETLY_BIN_DIR=... sh install.sh` 改装本地二进制（e2e 冒烟同款形态）；数据根、advertise 地址、受管组件端点等均可经环境变量调整，见 [install.sh](install.sh) 头注释。

日常部署：

```sh
fleetly projects create shop
fleetly apps create --project <project-id> demo
fleetly deploy --app demo --image nginx:1.29               # 镜像直投
fleetly deploy --app demo --from-dir . --builder railpack  # 源码构建
fleetly deployments wait --deployment <deployment-id>
fleetly rollback --app demo --yes                          # 重放最近一次 succeeded 的 Revision
```

## CLI 一览

每组无参运行即列出子动词（`fleetly <group>`）：

| 上下文 | 命令组 |
| --- | --- |
| 身份与访问 | `init` `login` `whoami` `users` `teams` `roles` `tokens` `audit` |
| 结构 | `projects` `apps` `secrets` `configs` `vars` `shared-variables` `volumes` `networks` |
| 交付 | `deploy` `deployments` `revisions` `rollback` `builds` `uploads` `templates` `hooks` `github-apps` `registry` |
| 托管数据服务 | `databases` `pubsubs` `buckets` `engines` `topics` `streams` `bucket` |
| 自动化 | `tasks` `runs` `schedules` `exec` `shell` |
| 流量与集群 | `routes` `nodes` |
| 观测 | `logs`（`--text` 走持久化检索）`metrics` `alerts` `rules` `channels` `events` `findings` |
| 平台 | `platform` `components` `freeze` `doctor` `status` `schema` `explain` `version` |

客户端以 `FLEETLY_ADDR` + `FLEETLY_TOKEN`（或凭据文件）连接——CLI 在你所在的任何地方运行，不必登上服务器。SDK 拨同一批地址，另支持 `grpc://` / `grpcs://` scheme。

## Agent 面

机器契约在所有动词上一致，Agent 因此可以驱动整个平台：

- 全命令 `--json`——protojson、snake_case——golden 双形态钉死。
- 稳定退出码：`0` 成功/无变化、`1` 错误、`2` 有变化（diff 类动词）、`64` 用法错误。
- 同一错误信封——errcode、处置提示、文档链接——人类与 Agent 共用一份 stderr。
- 平台自描述：`fleetly schema` 枚举资源与字段；`fleetly explain` 就地讲解任意资源。
- `skills/` 承载仓内 Agent 技能（部署诊断、Database / PubSub / Bucket Store 开通、Task 池），行使与 CLI/API 相同的能力面；其中散文与代码同受契约测试执法。

## 开发

```sh
mise install               # 钉版工具链（go/buf/protoc/protoc-gen-go/golangci-lint/node/pnpm）
mise run generate:all      # 基线生成通道：钉装本地插件 → buf → config.pb.go → wire
mise run generate:verify   # 再生成 + 零漂移断言
mise run test              # go test -race 三 module
mise run lint              # go vet/gofmt + golangci + buf lint + buf breaking（对 origin/main）
mise run dev:server        # 启动 fleetlyd
go run ./cmd/fleetly version
```

Console（React 19 + Vite + Tailwind 4；Node 24 / pnpm 钉版）：`console:install` · `console:gen`（openapi-typescript 从 genproto swagger 再生成）· `console:build`（产物提交进 `internal/interfaces/console/dist`）· `console:verify`。

e2e 套件跑 Docker-in-Docker：`e2e:dind`（单节点冒烟）· `e2e:h2c`（受管 Proxy Route）· `e2e:twonode`（节点接入 + 卷钉住）· `e2e:upgrade`（升级零扰动矩阵）· `e2e:backup`（跨引擎恢复演练）。

工程约定——生成物纪律、分层与依赖、守卫文化、提交纪律——见 [AGENTS.md](AGENTS.md)，对人与 Agent 同一生效。

## 仓库结构

```
cmd/fleetlyd, cmd/fleetly    daemon 与 CLI 入口
proto/                       唯一契约源（buf lint + breaking 门禁）
genproto/                    proto 生成物 module——手改即错
sdk/go                       类型化 Go 客户端 module
internal/domain              领域层：spec IR、模型、能力端口、聚合
internal/application         engine 内核 + 应用模块、authn、compose 受理
internal/infrastructure      SQLite 仓储；providers（swarm、k3s、traefik、zot…）
internal/interfaces          gRPC handler、admin、内嵌 console
internal/assembly, config    组合根
console/                     Web console 源码
skills/                      面向 Agent 的技能
docs/                        设计、ADR、计划、runbook
e2e/                         DinD 端到端套件
```

## 文档

- 词汇真源（冻结）：[CONTEXT.md](CONTEXT.md)
- 架构、领域模型、选型：[docs/design/](docs/design/)
- 决策记录：[docs/adr/](docs/adr/)
- v1 功能清单与批次：[docs/plan/2026-09-30-feature-checklist.md](docs/plan/2026-09-30-feature-checklist.md)
- staging 真机操作实录：[docs/runbooks/staging-fleetly.md](docs/runbooks/staging-fleetly.md)

## 状态

pre-release——尚未发布 tag。v1 功能清单基本收官（单节点心脏、Agent 面 + dogfooding、数据与观测信任、Console 体验全量、k3s 第二运行时、托管数据服务重设计），平台现役于双节点 staging 集群承载真实 dogfood 工作负载。挂账项在清单内跟踪：备份 manifest 形、GitHub App staging 真机锚；GitLab/Gitea webhook 等有需求再做。
