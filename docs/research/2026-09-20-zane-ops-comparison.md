# 对照调研：zane-ops（Python/Django + Temporal + Swarm）

| 状态 | 日期 | 关联 |
|---|---|---|
| 已完成 | 2026-09-20 | 本地源码对照（D:/Codes/paas/zane-ops，调研时 HEAD 含 `Remove celery dependency (#194)`→`Try temporalio (#189)` 全史）；对照基准 = [平台架构](../design/2026-09-17-architecture.md)（已实现 v0.1 切面）；结论输入 [v0.2 规划](../plan/2026-09-20-v0.2-plan.md) §借鉴与 §避坑 |

**一句话结论**：zane-ops 与 fleetly 同用 Docker Swarm 底座、同走「一部署一快照、健康通过才切流量」的发布语义，但它是**重生态路线**（Django+Postgres+Valkey+Temporal+fluentd+Loki，控制面自身 10 容器、内存 limit 合计 ≈7GB），fleetly 是**轻单核路线**（单二进制+SQLite，idle ≈160MB）。两条路线在部署编排的核心语义上**多处独立收敛**（互相印证），其重量代价与若干机制细节（请求归因访问日志、DB↔Swarm 周期对账、webshell 安全三件套）分别构成 v0.2 的**避坑清单与借鉴清单**。

## 1. 项目画像与技术选型对照

zane-ops：自托管 PaaS，Heroku/Railway/Render 的开源替代，支持镜像/Git 构建（Dockerfile/静态目录/Nixpacks/Railpack 四种 builder）/compose stack 三类部署，Django 5.2 + DRF、React 19 + React Router 7、Caddy 代理、Temporal.io 编排、PostgreSQL 16 + Valkey（Redis）、fluentd→Loki 日志、open-core（`backend/ee/` 商业授权）。

| 维度 | zane-ops | fleetly v0.1 | 评注 |
|---|---|---|---|
| 底座 | Docker Swarm | Docker Swarm | **同**——对照价值最高的前提 |
| 控制面形态 | 10 容器 stack（proxy/app/temporal-server/×2 worker/valkey/db/pgbouncer/fluentd/loki，`docker/docker-stack.prod.yaml`） | 单二进制 fleetlyd + Traefik + containerd | 差 1~2 个数量级 |
| 控制面内存 | limit 合计 ≈7GB（app 1G + temporal 500M + worker×1G + valkey 500M + db 1G + pgbouncer 1G + fluentd 200M + loki 1G） | 实测 idle ≈160.7MB（[校准报告](2026-09-19-resource-calibration.md)） | 我们 <200MB 预算的对照物 |
| 编排引擎 | Temporal server（workflow history 持久化、activity 重试/心跳/信号量） | 自研状态机 + CAS + janitor + 重启分类恢复 | §2 |
| 状态库 | Postgres 16（zane_api 已 355 个 migration）+ PgBouncer | SQLite（goose 少量迁移） | |
| 代理 | Caddy Admin API(:2019) JSON 增量 PATCH + ETag 乐观锁 | Traefik provider 文件原子下发 | §3 |
| 日志 | 容器 fluentd driver→fluentd→HTTP 回灌 API→Loki(运行)/Postgres(访问) | 控制面直读 ContainerLogs（hub/ring/disk）+ SSE | §6 |
| API/前端 | DRF→OpenAPI→生成 TS client（`frontend/app/api/v1.ts`）；WebSocket(Channels) | proto→REST/gRPC→openapi-typescript 生成 `schema.d.ts`（D4 门禁）；SSE | 同代同构 |
| 安装 | `curl … install.sh \| sudo bash`，304 行 bash，安装期需 ROOT_DOMAIN+APP_DOMAIN 双域名 | install.sh 单命令 + 引擎门禁；平台域名属 v0.2 E1 前置 | 双域名口径与我们 E1 前置一致 |
| 测试 | Django 单测（CI matrix 分 app 并行 + service containers 真 Postgres/Loki/Caddy）；**前端零测试、无 Swarm E2E** | 21 包单测 + vitest + dind 全链三件套（journey/upgrade/nightly） | 我们占优，保持 |

## 2. 部署编排：他们走过的 Celery→Temporal 迁移，就是我们自研状态机的「平行世界」

**演进史（git 实证）**：zane-ops 最初用 Celery 跑部署，`Remove celery dependency and models (#194)` → `Try temporalio for background jobs (#189)` → `Temporal works now` ——部署编排天然是**可持久化执行（durable execution）**问题，Celery 的 at-most-once 任务模型扛不住「构建 20 分钟、控制面中途重启、取消要补偿」的需求；他们的解法是引入 Temporal server（连带 Postgres schema、双 worker 进程、独立 UI），而不是自研。

**他们的部署主流程**（`backend/temporal/workflows/services.py` `DeployDockerServiceWorkflow`）：同 service 部署经 Redis 分布式信号量天然串行 → 建卷/configs →（有 host 端口/卷冲突时先把旧生产部署 scale=0 释放端口）→ pull → 建 swarm service（蓝绿 slot，一部署一 service）→ 健康检查通过 → Caddy 切路由 → 清理旧部署（删监控 schedule、scale down、删 configs/volumes）→ 失败则 scale back 旧部署 + 落终态。取消走 signal + **步骤枚举逆向补偿**（`DockerDeploymentStep` 每步配对应回退）。控制面重启后由 Temporal history 重放续跑；每次部署收尾跑 `cleanup_previous_unclean_deployments` 做 **DB↔Swarm 对账**（DB 说在跑但 swarm service 没了 → 批量标 REMOVED）。

**对照与裁决**：

- **印证（我们已同构）**：一部署一记录 + **不可变 JSON 快照**（他们 `Deployment.service_snapshot`；我们归一化 compose 快照 + env 三层合并结果）；**回滚 = 旧快照重放成新部署**而非流量切回（他们 `diff_service_snapshots` redeploy；我们 `desired_hash ≡ v1` 快照重放，journey J6 实证）；健康通过才切流量、失败恢复旧版（我们观察窗 + 首发失败 scale=0）；「外部副作用前状态先落定」（他们 `transaction.on_commit`；我们写前直读 + CAS + S18-A6 单事务收口）；部署串行化（我们 Queue+worker 单进程更简单）。
- **借鉴（R2，进 v0.2 引擎加固）**：**运行期 DB↔Swarm 对账**——他们不只靠重启分类恢复，每次部署收尾都对账一次。我们已有非终态超龄扫描（S18-A10），但「derived_state 与 swarm 实况的周期 reconcile」仍是缺口：外部 `docker service rm` 后我们的 app 视图会长期停在 running。落法：janitor 扩一项「声称 running 但 service/task 缺失 → 事件 + 状态修正」。
- **避坑（A1）**：**为单机/两节点场景引入通用编排引擎不划算**。Temporal 换来的重试/重放/心跳，我们用「事件溯源语义的状态机 + 单写点 + 对账」以 1/40 的资源代价覆盖了 v0.1 全部需求。v0.2 E1 多节点时守住「fleetlyd 单 manager 写点」纪律即可继续免引擎；若未来真需要跨进程并发部署再加**带 TTL 的信号量**（他们的 Redis TTL 自愈语义可抄，R6）。
- **借鉴（R3）**：`DeploymentChange` 字段级 old/new 变更日志 + 快照 diff 函数，服务「这次部署改了什么」的 UI 展示。我们快照已齐，缺 diff 视图——Console 部署详情页的低成本增强。

## 3. 代理与 TLS：ETag 乐观锁与请求归因标记值得抄，on-demand TLS 不学

- **配置下发**：他们完全走 Caddy Admin API 增量 PATCH，读-改-写带 **ETag（If-Match）乐观锁**，412 重试 3 次，且在客户端**复刻 Caddy 官方路由排序算法**保证追加位置正确（`backend/temporal/proxy.py`）。我们走 Traefik provider 文件原子写 + 单写点 publisher，并发安全等价且更简单——**不迁移，但「代理配置变更必须有并发防线 + 顺序语义」这一裁决被印证**。
- **TLS**：Caddy `on_demand` ACME + 许可回调（代理收到未知域名 TLS 握手 → 反问 API 该域名是否登记过，`views/proxy.py CheckCertificatesAPIView`）。优点是零配置签发，代价是**先打到代理再说**的暴露面。我们「域名台账先行 + 控制面集中签发（HTTP-01 经 Traefik 反代）」更严——**保持，不抄**（A2）。
- **借鉴（R4，E6 观测的访问日志输入）**：他们在 Caddy 路由上 `log_append` 注入 `zane_deployment_blue/green_hash`、request id，访问日志解析成结构化行入 Postgres（method/status/时长/UA/IP，带权限过滤查询）。**「这条请求打了哪个 deployment」可归因**，排障价值极高（蓝绿期间尤其）。我们的等价物：Traefik access log（已有文件面）+ 中间件注入 deployment 标记 + 解析入 SQLite（janitor 保留窗）。这比引入任何日志栈都便宜。
- **蓝绿 slot 的取舍**：他们一部署一 swarm service + DNS 别名带 slot（蓝绿两个 service 并存，随时可指回）。我们单 service 滚动更新（start-first + Traefik 切换）在 v0.1 语义下更省资源；**快速指回**能力我们用「快照重放回滚（≈18s）」覆盖，验收达标——不跟。E1 多节点大流量场景再评估。

## 4. 应用模型与 compose 处理：三层编译器结构可参考，校验路线各走各的

他们的 compose 处理（`backend/compose/processor.py`）是「用户 YAML → 受管 spec → 可部署 YAML」两阶段编译：先 `docker compose config` **子进程**做官方语法校验，再做自定义约束（禁 build、bind 卷绝对路径、configs 只准 content）；路由靠**约定标签**（`zane.http.routes.N.domain/...`）声明；平台注入受管字段——服务改名 `{hash}_{name}` 防共享网络 DNS 冲突、强制 fluentd 日志配置、注入 `update_config{start-first, rollback}`、打 `zane-managed` 标签族；环境变量支持 `{{ generate_password|uuid|domain|... }}` 模板函数，**生成的 secret 存进 stack env_overrides 反馈给用户**；configs 内容变更自动升版本 `_v{N}`（绕过 swarm config 不可变）。

- **印证**：受管字段注入 + 平台标签族 + 「保留用户原文、部署时编译」与我们归一化快照路线同构。
- **借鉴（R5，E4 数据库托管输入）**：`generate_*` 模板函数 = FZ-1「自动连接串以 `source=system` 平台 env 注入」的实现细节同构——首次部署生成凭据、存平台层、对用户只读可见。`_v{N}` 内容版本化技巧在 v0.2 开放 compose `configs` 时备用。
- **避坑（A4）**：**服务端跑 `docker compose config` 子进程做校验**——子进程依赖面（compose 插件版本漂移）、性能、注入风险都归我们 S 系评审反复敲过的点。我们受控子集 + 自研校验 + golden 的路线**保持不变**；他们「校验完丢给 docker stack deploy 全量语义」导致支持面必须收窄到 stack 语义（例如不支持 build 也是被迫的）。
- **注意**：他们 DNS 防冲突用的是「hash 前缀改名 + depends_on 同步改名」；我们用 naming 规约 + 网络别名，语义等价，不改。

## 5. 资源与部署形态：重量代价的实证（v0.2 观测栈的镜鉴）

生产 stack 逐容器（`docker/docker-stack.prod.yaml`，全部钉 manager 节点）：proxy(Caddy，未设限) / app(2C-1G) / temporal-server(0.5C-500M) / temporal-admin-tools(replicated-job 0.5C-500M) / main-worker(1C-1G) / schedule-worker(1C-1G) / valkey(0.5C-500M) / postgres(1C-1G) / pgbouncer(1C-1G) / fluentd(0.5C-200M) / loki(1C-1G)。**控制面自身内存 limit 合计 ≈7GB**；另有可选 otel(tempo) 与 temporal-ui stack。

- **裁决（A1 续）**：这不是「他们做错了」，而是「Temporal+Loki 生态的自然重量」。对我们的直接约束：**v0.2 E6 metrics/通知严禁引入 fluentd/Loki/Grafana 级依赖**（单 Loki limit 即 1G，等于我们全部预算的 5 倍）——按既定口径用 VictoriaMetrics/cAdvisor 轻核并重测 <400MB 总预算（FZ-9 已把 containerd ≈44-50MB 计入）。
- **供应链反面教材（A6）**：其 CI（`.github/workflows/pytests.yaml`）使用维护者个人 fork 镜像 `fredkiss3/grafana-loki` 与可变 tag `ghcr.io/zane-ops/proxy:canary`——正是我们 S20-F1 钉 SHA 要防的形态。**纪律扩展（R7）**：我们 CI/部署脚本里引用的容器镜像一律钉 digest（当前 deploy 脚本镜像走 release 清单，补一条 lint 即可）。

## 6. 日志与可观测：双存储分工有启发，无流式是退步

他们：容器日志 driver=fluentd（unix socket、non-blocking、带 service 元数据 tag）→ fluentd 批量 POST 回平台 API → 运行/构建日志入 Loki（label 检索、保留期由 Loki 自管）、Caddy 访问日志解析入 Postgres（含 GeoIP 国家码）；查询全部 REST 轮询 + 游标分页，**没有实时流**。

- **借鉴（R4 续）**：「访问日志强一致入库（可按 app 权限过滤查询）/运行日志走廉价检索层」的双轨分工值得要——SQLite 场景下简化为「访问日志入 SQLite + 运行日志维持现有 ring/disk」。
- **避坑（A3）**：无实时日志流（纯轮询）是明显退步——我们的 SSE 直读（journey J5）保持。
- **印证**：fluentd non-blocking driver 的动机（采集器挂了不能阻塞业务容器 stdout）说明「日志采集链路必须与业务容器解耦」；我们控制面直读 API 天然解耦，无此风险。

## 7. webshell 与多租户：E7 的直接设计输入

他们的 webshell（`backend/webshell/`）：Django Channels WebSocket → 服务端 PTY（`pty.openpty` + SIGWINCH resize）→ `docker exec -it` 进**最新 running task 的容器**；安全 = 三层（workspace 角色门槛 / shell 白名单仅 6 种 / 资源归属校验），**无空闲超时、无审计日志**（disconnect 才清理进程）。

- **借鉴（R1，E7 落地细则）**：PTY+exec+shell 白名单是标准做法照抄；**补齐他们缺的**：空闲超时（复用 substrate 30s per-call 超时纪律）、**审计事件**（谁在哪个容器何时开 shell——我们审计面现成，`webshell.opened` 入事件注册表走只增流程）、连接数上限（对齐 WatchEvents 的 per-token 流上限 A5）。
- **参照**：他们 preview 环境 basic auth（bcrypt）+ PR 评论自动更新（GitHub/GitLab）是 v0.2「PR 预览环境」的成熟先例（R8，排 E1 后）；workspace/RBAC/邀请制属多用户面，我们 v0.2 仍单操作员，**不跟进**（与 FZ-12 单操作员口径一致）。

## 8. 汇总裁决表

| # | 主题 | zane-ops 做法 | 裁决 | 去向 |
|---|---|---|---|---|
| V1 | 部署=不可变快照+回滚=快照重放 | service_snapshot JSON + diff redeploy | **印证**（我们同构） | — |
| V2 | 健康过才切流量/失败恢复旧版/串行部署 | workflow 内三步 + 信号量 | **印证** | — |
| V3 | 副作用前状态先落定 | transaction.on_commit | **印证**（我们 CAS+单写点） | — |
| V4 | OpenAPI→生成客户端 | spectacular→v1.ts | **印证**（D4 同构刚落地） | — |
| R1 | webshell 安全三件套+超时+审计 | WS+PTY+exec+白名单（缺后两样） | **借鉴**：抄白名单、补超时/审计/连接上限 | v0.2 E7 |
| R2 | 运行期 DB↔Swarm 对账 | 每次部署收尾 cleanup unclean | **借鉴**：janitor 扩 reconcile | v0.2 引擎加固（前置重构同期） |
| R3 | 字段级变更日志+快照 diff 视图 | DeploymentChange old/new | **借鉴**：Console 部署详情 diff | v0.2 Console 线 |
| R4 | 访问日志带 deployment 归因入库 | Caddy log_append 蓝绿 hash→Postgres | **借鉴**：Traefik access log→SQLite | v0.2 E6 轻量化路径 |
| R5 | 生成型 secret/connection string | generate_* 模板+env_overrides 反馈 | **借鉴**：FZ-1 source=system 实现细节 | v0.2 E4 |
| R6 | 带 TTL 的部署并发信号量 | Redis lock+counter+TTL 自愈 | **借鉴**：多写点出现时才启用 | v0.2 E1 备用 |
| R7 | CI 镜像钉 digest | （反面：fork 镜像+canary tag） | **借鉴**（反面教材） | v0.2 交付面小项 |
| R8 | PR 预览+评论反馈+basic auth | preview 环境 + PR comment | **借鉴**：先例参考 | v0.2 PR 预览（E1 后） |
| A1 | 重生态依赖 | Temporal/Postgres/Loki 全家桶 ≈7GB | **避坑**：单机/两节点坚持轻单核；E6 禁重依赖 | v0.2 全程约束 |
| A2 | on-demand TLS 许可回调 | 未知域名先握手再问 API | **避坑**：保持台账先行+集中签发 | — |
| A3 | 日志纯轮询无流 | REST cursor 分页 | **避坑**：保持 SSE | — |
| A4 | compose 官方子进程校验 | `docker compose config` | **避坑**：保持自研白名单+golden | — |
| A5 | webshell 无超时无审计 | 仅断开清理 | **避坑**（随 R1 补齐） | v0.2 E7 |
| A6 | CI 可变镜像引用 | fork+canary | **避坑**（随 R7） | — |

## 9. 对 v0.2 规划的输入（R/A 编号被规划文档引用）

1. E7 webshell 按 R1 细则直接出票；E4 按 R5 细化 FZ-1 落地；E6 按 R4 走 Traefik access log→SQLite 轻路径并守住 A1 资源红线；引擎加固（EnterPhase 前置重构）合并 R2 对账项。
2. 平台双域名安装（ROOT_DOMAIN/APP_DOMAIN）印证 E1 前置「平台基础域名安装项」的必要性。
3. 本报告只反映调研时点的 zane-ops HEAD；其 notes/（`api-tokens-plan.md` 记录的 deploy_token 权限漏洞、compose 卷 reconcile 未完成）说明重栈路线的工程债同样存在，不构成改换路线的理由。
4. **修订（2026-09-20，V2-1 裁决后）**：用户裁决「易用性与轻量同等重要，可适当提高内存限额」——[v0.2 规划](../plan/2026-09-20-v0.2-plan.md) 红线 1 由 idle <400MB 上调至 **<600MB**，VictoriaLogs 改为**默认捆绑**（单二进制 + 控制面直推、无采集中间容器，600MB 实测门约束）。随之：R4 的访问日志载体由 SQLite 改为随 VictoriaLogs 统一入日志库；A1 的红线对象明确为「多容器重栈」（fluentd+Loki+Grafana 三件套、Temporal 级），VictoriaLogs 类轻单核组件不属此列。本报告其余结论不变（调研为时点快照，裁决演进以规划文档为准）。
