# E6 观测（VictoriaLogs 日志库 + 统一检索 + metrics opt-in + 通知）专项设计

| 状态 | 日期 | 说明 |
|---|---|---|
| **实现中（W5-S1 日志库核心已落地，2026-09-22）** | 2026-09-22 | 裁决输入：[v0.2 规划 §2 W5 行](../plan/2026-09-20-v0.2-plan.md)、V2-1（VictoriaLogs 默认捆绑，用户直裁 2026-09-20）、V2-6（通知 Webhook 首发）、R4 载体修订（访问日志随 VL 入日志库，规划 §7 修订 4）；本票新增裁决：**D-W5-2**（metrics opt-in 管理组件，用户直裁 2026-09-22——依据：staging 429MB 基线 + VL 后 ≈455MB，三件套 +110~165MB 默认捆绑 ≈589MB 踩线无余量）、**D-W5-4**（受管服务宿主可达性 = host-mode 回环发布）。同轮关联裁决：**D-W5-1**（E2 MCP 暂缓出 v0.2，用户直裁「CLI 目前基本够用」→ E2 整票移 v0.3，规划 §5/§6 已回填）。W5 其余两票见 [web-terminal 专项](2026-09-22-web-terminal.md)（D-W5-3 通道修订 + 控制面 TLS）。**S1 落地注记（三处实现偏离，均真机取证）**：①D-W5-4 字面「PortConfig.HostIP=127.0.0.1」不可实现——Engine API 对 swarm 服务端口 HostIp 静默丢弃（docker 29.8.1 dind 实证，moby v1.56 无该字段），落地 = **host 网络任务 + VL 原生 `-httpListenAddr=127.0.0.1:9428`**（进程级回环绑定，零公网面不变量比设计字面更严；附带：host 网络任务不可挂 overlay，`fleetly-victorialogs-net` 留位网络不创建——未来容器消费方的访问面另裁）；②镜像 repo 更正 `victoriametrics/victoria-logs`（设计字面 `victorialogs/victoria-logs` 在 Docker Hub 不存在），钉 **v1.52.0** index digest；③ES bulk 端点实测 `/insert/elasticsearch/_bulk`（`/insert/elasticbulk` 在 v1.52 无此端点） |

## 1. 现状与问题

1. **检索缺失**：日志链路 = hub 内存环（直播+回放）+ JSONL 落盘（7 天保留）+ `History()` 全量扫日文件——无关键词/时间窗过滤。V2-1 易用性直裁的核心兑现项就是「Console 统一检索」。
2. **访问日志零采集**：traefik（服务名 `fleetly-ingress`）静态参数无 `--accesslog`，R4 的输入面不存在。
3. **metrics 零采集**：无 cAdvisor/VM/exporter 任何面；预算复测全靠 VmRSS/docker stats 手工脚本（runbook 台账）。
4. **通知零通路**：cron 失败、备份上传失败、对账降级——全部只进 events 表，用户不打开 Console 就不知道。
5. **宿主不可达 overlay（结构性，E3-5 实证）**：hub 与 fleetlyd 都是宿主进程，overlay 网络内的服务（VL/VM）对宿主不可路由——受管服务的宿主可达路径必须显式设计（D-W5-4）。

## 2. VictoriaLogs 默认捆绑（V2-1）

### 2.1 组件与部署形态（internal/victorialogs，rustfs duty 同款）

- **服务**：swarm service `fleetly-victorialogs`，单副本**钉住 manager**（日志采集单写点 = hub 在 manager 经 Docker API 拉全集群服务日志，数据重力同控制面；不引入跨节点写路径，SQLite 单写点纪律同源）。卷 `fleetly-victorialogs-data`。专用内部网络 `fleetly-victorialogs-net`（今日无容器消费方，为后续受管消费方留位；**不对应用开放挂载**——应用要读自己的日志走平台 API）。
- **镜像**：`victorialogs/victoria-logs:<实现时点最新稳定>` 钉 digest（Go 常量 + image-prepull 台账双锚，zot/restfs 同款纪律）。单二进制、无采集中间层（A1 红线不涉）。
- **宿主可达（D-W5-4）**：host-mode 端口发布 `TargetPort 9428 / PublishedPort 9428 / PublishMode host / HostIP 127.0.0.1`（moby `swarm.PortConfig.HostIP`，Docker ≥25 / API 1.44；staging 29.8.1 与 e2e dind 均满足；traefik 80/443 host-mode 发布是既有先例，本处仅多绑回环）。**回环绑定 = 零公网面**：hub 直推与 SearchLogs 查询都走 `127.0.0.1:9428`，VL 对外不可达。
- **参数**：`-storageDataPath=<卷>`；`-retentionPeriod` 对齐 `logs.retention_days`（缺省 7d，一窗两载体同口径）；内存限额起步 128MB（实测校准门 FZ-9 纪律：先测 idle 再定）。 LogsQL 无需额外 flag。
- **duty**：`internal/victorialogs` 镜像 rustfs manager（`internal/rustfs`）：设置驱动、幂等比对、漂移收敛、backoff 重试、diff 事件 `logs.victorialogs_deployed` / `logs.victorialogs_removed`（事件注册表只增）。

### 2.2 设置面：`logs.backend`

- platform_settings 新键 `logs.backend` ∈ {`victorialogs`, `jsonl`}，**缺省 `victorialogs`（V2-1 默认捆绑）**——存量安装升级后同样落入缺省（未显式设置过的，duty 即部署 VL）；显式设过 `jsonl` 的不动。
- CLI `fleetly logs backend show|set <mode>` + Console Settings 卡；切回 `jsonl` → duty 移除服务、**卷保留**（rustfs 禁用同型数据安全语义）。
- **降级条款（V2-1 原文延续）**：600MB 实测门不过 → 缺省改 `jsonl` + 发布说明；FTS5 兜底（jsonl 模式 + SQLite FTS5 索引日文件）为设计附录 §6.A，**不实现除非门失败**。

### 2.3 hub 直推（ES bulk，无采集中间容器）

- **入口**：hub（`internal/logs` manager）在既有「入口脱敏」之后挂批量器——**进 VL 的行全部已脱敏**（env 秘密值/凭证不落 VL，与 ring/disk 同一纪律）。
- **协议**：`POST 127.0.0.1:9428/insert/elasticbulk`（规划原文 ES bulk）；行字段：`_time`、`app`、`service`、`source`（`container|build|access`）、`stderr`、`_msg`=行内容；访问日志附加字段见 §3.2。流字段 `_stream_fields={app,service,source}`。
- **批量纪律**：2s 或 512 行触发 flush；内存队列上限 8192 行，溢出丢最旧 + 计数（**直播面零影响**——ring/fan-out 不经过批量器，VL 故障不破坏 FollowLogs，A3 直读不动条款的结构性兑现）。
- **失败诚实面**：VL 不可达 → streak 进入/退出各发一事件（`logs.ingest_degraded` / `logs.ingest_recovered`，事件去抖）+ system status 新组件 `victorialogs` 红 + 丢弃计数常驻 `logs backend status` 输出。不逐行红、不静默。
- **JSONL 边界**：`victorialogs` 模式下**停止 JSONL 落盘**（双写不留，磁盘不翻倍）；切换前的 JSONL 文件按既有 prune 自然老化，**检索不跨界**（切换点之前的日志不在新检索面里——诚实边界，Console 文案与 CLI 同口径）。
- **build 日志**：与今日写入/读取同一咽喉点接入批量器（`source=build`），S2 断言「构建日志可检索」。
- **跨节点日志聚合实测点**：hub 经 manager Docker API 拉全集群服务日志，该路径是否受 7946/UDP 过滤影响未在 W2/W3 显式断言过——S1 e2e（本地 dind 单机不覆盖）+ staging 演练各记一笔（W3-F2 放行后 worker 任务日志应可采）。

### 2.4 SSE 直读不动（A3）

FollowLogs（ring 回放+直播）零改动；VL 只承接**检索面**。两条路径职责分离：VL 故障 = 检索降级，直播照常。

### 2.5 数据边界

- **VL 数据不进 state_backups**（运营数据，L1 备份清单不含——规划 W5 行原文）：备份 manifest 与 runbook 明示；VL 卷损 = 丢失 7 天日志检索面，不损平台状态。
- VL 卷与 rustfs/zot 卷同属「受管组件卷」，app 删除/迁移扫描不涉及。

### 2.6 预算门

S1 e2e 实测 VL idle 增量；staging 演练全栈复测（rustfs 启用态 429MB 基线 + VL）；>600MB → 触发 §2.2 降级条款（缺省翻 `jsonl`，本波内完成翻default+文档）。

## 3. 统一检索与访问日志

### 3.1 SearchLogs API（LogsService 扩展，proto 加法）

- RPC `SearchLogs {apps?, keyword?, time_start/time_end, services?, sources?, limit, cursor}` → fleetlyd → `127.0.0.1:9428/select/logsql/query`；结果行带 `{at, app, service, source, stderr, msg}` + 访问字段透传。
- **注入安全（硬性）**：keyword 构造为 LogsQL 短语过滤（转义规则实现 + **负向测试钉住**——用户输入永不裸拼进查询串）；时间窗/服务名经白名单校验（服务名沿用 `^[a-z0-9-]+$` 类既有约束）。
- 权限：read scope（与 FollowLogs 同级——能看直播就能看检索）。
- CLI：`fleetly logs search <app> --keyword <kw> --since 1h --source access`。
- 空结果与 VL 不可达区分（`E_LOGS_BACKEND_UNAVAILABLE` 诚实报错，不返回空列表冒充）。

### 3.2 访问日志（R4 载体 = VL）

- **开启采集**：traefik 静态参数增 `--accesslog=true --accesslog.format=json`（JSON 行含 RouterName）；spec 变更走 ingress ensure 既有收敛。
- **hub 增平台服务采集**：poll `fleetly-ingress` 服务日志 → JSON 解析 → RouterName 反解出 app/service（路由命名是平台自己的确定性公式）→ Entry{App, Service, Source: `access`, Msg: 紧凑摘要（method status host path duration）} + 结构化字段（`method/status/host/route/duration_ms/client_ip`）→ 同一 ES bulk 批量器入 VL。**不进 ring**（量级与用途不同，直播面不加噪）。
- **部署归因**：入口时查该 app 当前 active deployment 记 `deployment_id` 字段（±滚动窗近似，诚实标注）；**精确到 task 的蓝绿归因是 v0.3 候选**（traefik 中间件注头方案留档不实现）。
- **入口脱敏适用性**：访问日志行经同一 redactor（URL query 可能带凭证的既有纪律覆盖）。

### 3.3 Console 统一检索

- **AppLogsPage 升级**：检索栏（关键词输入 + 时间窗快捷项 + 服务/来源 chips）→ SearchLogs；结果虚拟滚动 + 时间高亮；保留现有直播视图（两态切换：直播 / 检索）。
- 全局跨应用检索页：v0.2.x（本波只做 app 级——单操作员模型下先兑现高频路径）。
- **degraded 一等 UI（挂账收口，规划 §6）**：应用列表/详情 degraded 态常驻解释卡（对账降级因由 + 关联 `app.substrate_missing` 事件链接），不再是只有 badge 的二等态。

## 4. metrics opt-in（D-W5-2）

### 4.1 设置与组件

- platform_settings 新键 `metrics.mode` ∈ {`unset`（缺省）, `on`}——**opt-in**（用户直裁 2026-09-22；同 V2-2 rustfs 模式：默认关、一键启用、启用时计入 600MB 预算复测）。
- `on` → duty（`internal/metrics`，rustfs 同款）部署三件，全部钉 digest 双锚（docker.io 系，预拉台账同步）：
  - `fleetly-victoriametrics`：victoria-metrics 单机版，单副本钉 manager，卷 `fleetly-victoriametrics-data`，**回环发布 8428**（D-W5-4 同款），`-retentionPeriod` 对齐新 config 键 `metrics.retention_days`（缺省 14d）；
  - `fleetly-cadvisor`：**global**（每节点一任务），host 只读挂载（`/:/rootfs:ro`、`/var/run:/var/run:ro`、`/sys:/sys:ro`、`/var/lib/docker:/var/lib/docker:ro`），挂 `fleetly-metrics-net`；
  - `fleetly-node-exporter`：global，host 只读挂载（`/proc`、`/sys`）。
- **抓取**：VM `-prometheus.config` 静态 targets `tasks.fleetly-cadvisor:8080` + `tasks.fleetly-node-exporter:9100`（DNS RR 解析全部任务 IP，Swarm 原生成员发现）。**跨节点抓取依赖 overlay 数据面**：W3-F2 放行前 worker 节点指标缺席、manager 节点指标不受影响——Console 与 `metrics status` 诚实标注（不红、不谎报 0）。
- 版本钉版：实现时点取最新稳定（VM v1.1xx 系 / cAdvisor v0.5x 系 / node_exporter v1.x 系），digest 双锚进 image-prepull 台账。

### 4.2 查询面与图表

- RPC `SearchMetrics {query（PromQL 透传）, time_start/time_end, step}`（新 MetricsService，read scope）→ fleetlyd → `127.0.0.1:8428` → 序列点集。**诚实口径**：操作员工具、PromQL 透传、不声称查询沙箱（文档明示）。
- **Console 图表**：引入 **uPlot**（≈10KB gzip，MIT，canvas 时序图——当前零图表依赖，不引入 recharts 级重库）：AppOverview 增资源卡（容器 CPU/内存/网络，服务分解）+ 节点卡（node_exporter 面）。
- **多副本水位显示（挂账收敛，规划 §6）**：服务 replicas 常驻显示 + 每副本资源水位；**自动扩缩明确 v0.3**（需指标历史+策略面）。缩放路径 = compose `replicas` 编辑 → 正常部署（不可变纪律；**不做旁路 scale API**——旁路即状态漂移，EnterPhase 单写点红线）。
- `metrics.mode=on` 而组件未就绪 → 图表卡诚实「采集中/未就绪」态，不画空线。

### 4.3 预算复测

启用态全栈实测进 runbook（§10 演练）；>600MB → 收紧组件限额复测；仍超 → 回设计方案裁组件（cAdvisor 首裁——最大头，裁后容器指标降级为 manager 本地）。

## 5. 通知（V2-6：Webhook 首发）

### 5.1 端点与订阅

- 新表 `webhook_endpoints`（迁移随票）：`{id ULID, name, url, secret_cipher（envelope）, event_patterns JSON, enabled, created_at, updated_at}`。订阅 = 事件名 glob 模式列表（`deployment.*`、`cron.run_failed`、`app.substrate_missing`、`*`）。
- CRUD API（admin scope）+ 审计（`webhook.created/updated/deleted`）+ `fleetly notifications endpoint create|list|rm` CLI + Console Settings 卡。
- 校验：URL scheme http/https；https 强烈建议、http 允许（内网 receiver）+ Console 警示文案。**单操作员信任模型，无 SSRF 过滤——文档诚实声明**（收件地址是操作员自己配的）。

### 5.2 投递器（lynx service：internal/notify）

- **游标轮询**：自有游标（`webhook_state` 表）经 `EventsSince` 消费（WatchEvents 同路径，不建进程内总线）→ 模式匹配 → 每端点并发 ≤2 的投递 worker（全局 ≤8）。
- **载荷**：POST JSON（事件全字段）+ `X-Fleetly-Timestamp` + `X-Fleetly-Signature: sha256=HMAC(secret, timestamp + "." + body)`（接收方可验签 + 5min 窗防重放；secret 平台生成、可轮换）。
- **重试**：失败 3 次退避 30s / 5m / 30m（当日了结——W3-F3 同精神，不跨日滚雪球）；台账 `webhook_deliveries` `{event_seq, endpoint_id, status(pending|ok|failed), attempts, response_code, last_error, next_retry_at}` + janitor prune（7d）。
- **通知自身零事件（防自激励环）**：投递失败不 emitted 事件——失败可见面 = 台账 + system status `notifications` 组件红（端点连续终败时）+ Console 卡。订阅 `*` 不会把自己套进回环。
- `TestWebhook` RPC：发送 `type=test` 载荷（结构同真实事件）验证连通与验签配置。
- **既有事件面零改动即订阅可达**：cron 失败、备份上传失败、对账降级、库 degraded——事件本就存在，通知是纯消费者（设计优势，无侵入）。

## 6. 挂账与不做

- **§6.A FTS5 兜底（设计附录，条件实现）**：600MB 门失败时——jsonl 模式 + FTS5 虚表索引 JSONL 日文件 + SearchLogs 后端切换开关（`logs.search_backend`）；平时不实现。
- 全局跨应用日志检索页：v0.2.x。
- Slack/Email 通道：随真实需求（V2-6 原文）；告警规则引擎（VM alerting）：v0.3+ 评估。
- 自动扩缩、指标水位触发：v0.3。
- VL 集群版/跨节点日志副本：不做（单写点纪律）。
- 访问日志精确到 task 的蓝绿归因：v0.3 候选（中间件注头方案留档）。
- MCP 只读查询工具：随 E2 整体在 v0.3（D-W5-1）。

## 7. 验收锚定

| 面 | 验收 |
|---|---|
| S1 e2e（`e2e/logs-victorialogs.sh`，s3-rustfs 模板） | duty 收敛（部署/移除/漂移）→ 回环可达（宿主 curl VL health）→ 入湖可查（部署应用→SearchLogs 命中）→ keyword 过滤 → **注入负向**（恶意 keyword 不逃逸）→ 降级 streak（停 VL → degraded 事件 + 直播不受影响）→ backend 切换（jsonl 行为回退+卷保留） |
| S2 | 访问日志字段（访问应用 → access 行带 method/status/route/deployment_id）→ build 日志可检索 → Console 检索面（锚点不破坏 + 新锚点）→ degraded 解释卡 |
| S3 e2e | mode on → 三件套收敛 → SearchMetrics 有数（容器/节点两路）→ 水位显示 → 预算记录入台账 |
| S4 e2e | 订阅 `deployment.*` → 触发部署 → receiver 收到 + HMAC 验签通过 → 重试路径（receiver 500→退避→台账）→ `*` 订阅无回环 → test 载荷 |
| 契约 | 事件码只增（`logs.*` 5 项、组件健康面）；错误码注册表全量设计按需消费（`E_LOGS_BACKEND_UNAVAILABLE`、`E_METRICS_NOT_ENABLED`、`E_WEBHOOK_*` 族）；proto breaking 门禁；Console 锚点清单只增 |
| staging 演练 | runbook §10：全链 + 预算复测（429+VL / metrics on 复测）+ 跨节点日志聚合实测点（§2.3）+ worker 指标（视 UDP 放行状态诚实记录） |
