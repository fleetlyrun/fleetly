# fleetly

fleetly 是一个轻量 PaaS：把源码或镜像变成运行在可插拔运行时上的工作负载，并提供流量接入、托管数据服务与观测。人类与 AI Agent 同为一等用户。

- 词汇（唯一真源，冻结）：[CONTEXT.md](CONTEXT.md)
- 架构 / 领域模型 / 选型：`docs/design/`
- 决策记录：`docs/adr/`
- v1 功能清单与批次：`docs/plan/2026-09-30-feature-checklist.md`

## 形态

- **宿主 Linux 二进制**：`fleetlyd`（daemon，systemd 优先）+ `fleetly`（CLI）。控制面 gRPC `:9080` / REST `:9081`。
- **运行时**：Docker Swarm（单节点 `swarm init` 起步，`fleetly nodes enroll` 扩到多节点）。
- **受管自宿**（零外部依赖起步）：zot 镜像仓 `:5000`、traefik Edge、VictoriaLogs `:9428`、VictoriaMetrics + cadvisor `:8428`。

## 快速开始

```sh
# 1) 一行安装：OS/arch 检测 → 二进制 → Docker 检测/安装 → swarm init
#    → fleetlyd（systemd）→ 健康等待
curl -fsSL https://fleetly.dev/install.sh | sudo sh

# 2) 消费 bootstrap token 完成初始化
cat /var/lib/fleetly/bootstrap-token
fleetly init --token <token> admin

# 3) 样例应用一条龙：project/app → 镜像部署 → sslip.io Route → 等待
#    succeeded → 输出访问 URL（零 DNS；--tls auto 走 LE ACME）
fleetly quickstart
```

发布通道是 GitHub Releases（**首个 tag 发布前该 URL 404**——诚实失败；从源码构建走下方开发流）。本地二进制安装形态：`FLEETLY_BIN_DIR=... sh install.sh`（e2e 冒烟同款）；数据根/advertise/受管服务地址等环境变量见脚本头注释。

日常部署（镜像直投或源码构建、可回滚）：

```sh
fleetly projects create shop
fleetly apps create --project <project-id> demo
fleetly deploy --app demo --image nginx:1.29              # 镜像直投
fleetly deploy --app demo --from-dir . --builder railpack # 源码构建（dockerfile/railpack/static）
fleetly deployments wait --deployment <deployment-id>
fleetly rollback --app demo                              # 默认回滚到上一成功基线
```

## CLI 一览

按上下文分组（每组的子动词 `fleetly <group>` 无参即列）：

| 上下文 | 命令组 |
| --- | --- |
| 身份与访问 | `init` `login` `whoami` `tokens` `users` `roles` `teams` `audit` |
| 结构 | `projects` `apps` `secrets` `configs` `volumes` `networks`（含跨 Project peer） |
| 交付 | `deploy` `deployments`（wait/cancel）`rollback` `revisions` `builds`（logs 流式）`uploads`（内容寻址）`hooks` |
| 托管数据服务 | `databases`（postgres/pgvector/redis/mysql/mongo 模板；backup/verify/恢复） |
| 自动化 | `tasks`（one-shot 与 resident 池）`runs`（wait）`schedules`（时区 cron） |
| 边缘与集群 | `routes` `nodes`（enroll/drain/cordon） |
| 观测 | `logs`（`--text` 走受管日志检索）`metrics`（PromQL）`alerts` `channels` `events`（follow SSE） |
| 平台 | `platform`（升级前 Platform Backup）`doctor` `status` `schema` `explain` |

机器契约（Agent 面，全部命令一致）：

- `--json` 全命令覆盖，protojson snake_case；golden 双形态钉死。
- 稳定退出码：`0` 成功/无变化、`1` 错误、`2` 有变化（diff 类）、`64` 用法错误。
- 错误信封：errcode + 处置提示 + docs 链接，人类与 Agent 共用同一 stderr。
- 能力自描述：`fleetly schema`（资源与字段）与 `fleetly explain`（零文档发现面）；仓内 `skills/` 提供 Agent 操作技能。

## 开发

```sh
mise install            # 钉版工具链（go/buf/protoc/protoc-gen-go/golangci-lint）
mise run generate:all   # 基线生成通道：protoc 插件 → buf generate → config.pb.go → wire
mise run generate:verify # 再生成 + 零漂移断言
mise run test           # go test -race 三 module
mise run lint           # go vet/gofmt + golangci + buf lint & breaking
mise run dev:server     # 启动 fleetlyd
go run ./cmd/fleetly version
```

e2e dind 套件：`e2e:dind` / `e2e:h2c` / `e2e:twonode` / `e2e:upgrade`（升级零扰动矩阵）/ `e2e:backup`（四引擎恢复演练）。

工程约定（生成物纪律、分层与依赖、守卫文化、提交纪律）见 [AGENTS.md](AGENTS.md)。

## 状态

pre-release（未发 tag）。N0（单节点心脏）/ N1（Agent 面 + torchwood dogfooding）/ N2 数据观测主线已落地并在 staging 双节点真机持续验收；排期中：ObjectStore 外置目标、只读 Console（N3）、k3s 第二运行时试点（N4）。真机操作记录见 `docs/runbooks/staging-fleetly.md`。
