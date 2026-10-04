# ADR-0040: VictoriaLogs Logging Provider——集中采集环、双径检索、build 日志出口脱敏

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-04 | CONTEXT.md Logging/Metrics/Secret/Build 词条、ADR-0004（受管自宿唯一 reconciler）、ADR-0014（材料纪律）、ADR-0019 附录 B（受管自宿范式真源）、ADR-0021（钉版基线）、ADR-0022（Generation/指纹）、F2.4（checklist）、单机假设审计发现 A（StreamLogs 本节点视角，docs/reviews/2026-10-03）、提案 P11（错误只打一次+进程级脱敏）、ADR-0036 E28（材料字节稳定） |

## 背景

F2.4 三个到期件：①Logging Capability 的受管自宿 Provider（VictoriaLogs，
ADR-0021 选型基线）——日志持久化、检索（时间/文本/容器过滤）、脱敏；
②单机假设审计**发现 A**：`fleetly logs` 的容器发现走 manager 本节点
`ContainerList`，worker 节点容器日志静默缺失（zane 同款病灶），修复归宿
钉在本批；③提案 **P11**：build log 出口单点脱敏（Secret 值零出现锚）+
错误链 CLI 只打一次核对。

现状：`LogsService.StreamLogs` 经 `FacesOf(Runtime).Logs` 直读 docker
（仅实时 + 最近缓冲）；build 日志只有进程内 500 帧环形缓冲（末 8 个
build），不落库不落盘。

## 决策

1. **受管自宿 = VictoriaLogs 单节点（zot 范式逐条核对）**：
   - 镜像钉版 `victoriametrics/victoria-logs:v1.52.0`（2026-07-16 发布，
     三个月场龄；v1.25.0 起 tag 无 `-victorialogs` 后缀——同义镜像。
     入口 `/victoria-logs-prod` 绝对路径，镜像 config 实证）。升级走
     Platform 升级序（ADR-0015）。
   - 受管 Workload `fleetly-logging-victorialogs`（域 `fleetly/system/
     logging`）：端口/发布 9428（routing mesh——daemon 与采集面经节点
     地址可达）；数据卷 `fleetly-logging-victorialogs` → `-storageDataPath`
     （带卷 → reconciler 自动钉控制面节点，F2.3 语义免费继承）；
     `StopGrace 60s`（数据面宽限惯例）。
   - **发布端口暴露 = 必须认证**：routing mesh 上任何集群容器可路由到
     `<node>:9428`，而 VL 持全集群日志（跨 Project 敏感面）——VL 原生
     `-httpAuth.username/-httpAuth.password` Basic Auth 关死（username
     非空即启用）。密码首启随机生成（24B hex），持久化
     `<DataRoot>/keys/victorialogs.json`（0o600 tmp+rename，zot
     `loadOrGenerateCredential` 同款；字节稳定 = 载体指纹稳定，E28）。
     密码经材料文件注入（`-httpAuth.password=file:///run/secrets/…`，
     VL 原生旗标语义）——**绝不进 argv**（swarm service argv 集群可
     inspect）。
   - 保留窗 `-retentionPeriod=<logging.retention_days>d`（config 缺省
     30d；变更 = Workload 指纹变更 = 一次受管滚动）。VL 单租户、无
     per-Project 面板：**域隔离由平台查询构造执法**（见决策 4），VL
     本身只认平台凭证。
   - `config.logging.addr` 空 = Logging 面停用（zot 同款诚实降级：
     `fleetly logs` 回退 Runtime 实时路径，build 日志回退环形缓冲——
     升级零扰动）。install.sh 与 registry.addr 同源物化
     `<advertise>:9428`。

2. **采集面 = engine Logging 环（第 9 收敛环）· 控制面集中采集**——
   发现 A 的根治不是"每节点采集代理"而是**集群面 API**：
   - swarm `StreamLogs` 重做：容器发现从本节点 `ContainerList` 改为
     `ServiceList`（fleetly.ns 标签全等，集群对象）+ `ServiceLogs`
     逐服务读流（manager API 聚合**所有节点** task 日志）。`Details:true`
     携带 `com.docker.swarm.{node,service,task}.id` 行属性（k=v 逗号
     连接、URL query 转义——自写解析器，不引 docker/cli 内部包）。
     `LogFrame.Container` 语义更新为 swarm task ID（容器 ID 的集群稳
     定等价物；proto 字段名不变——"容器过滤"词汇以 process/WorkloadID
     表达，本字段是归因信息）。
   - **实时路径与采集面同修同源**：`fleetly logs`（follow 形态）与
     采集环消费**同一个** `StreamLogs`——重做即根治，无双路径漂移。
   - engine `loggingStep`（logging 环）：从权威表枚举活跃隔离域
     （apps 全量 / active 态 tasks / 非 tombstone databases / 受管域
     三件），每域一条 Follow 流 goroutine（生命周期跨 step，域消失即
     cancel），帧 → 批汇（≤1s 或 256 帧_flush）→ `Logging.Ingest`。
   - **游标与断流自愈**：`log_cursors` 表（域主键 + last_ts）**只在
     Ingest 成功后推进**。VL 不可达 → 游标冻结 → 帧缓冲有界（2000 帧
     满则丢最旧 + 计数告警）；VL 恢复 → Since=游标 从 docker json-file
     缓冲重放补窗。daemon 重启同款补窗。诚实边界：**补窗深度以 docker
     日志文件在场为界**（json-file 无轮转配置时近乎全量；轮转/任务载
     体已删则该窗永久缺失）。e2e 与 runbook 均以此口径记锚。
   - **为什么不每节点采集代理**（logspout/vector global service 形态）：
     ①Runtime IR 需新增全局调度模式 + 宿主 bind（docker.sock）两个新
     概念，受管面复杂度跃升；②worker 节点零安装物原则（staging node2
     现状）不被突破；③控制面节点单机假设 = ADR-0019 构建/工具容器同款
     已裁决合法类目（审计发现 D/E 同源）；④P11 出口单点在 daemon 内
     天然成立。代价（诚实记入）：全集群日志量经控制面 daemon——小微
     团队目标规模（ADR-0008 定位）可承载；触发条件（日志量压迫控制面
     /独立采集机诉求）出现时升级为 global 采集 Workload，端口契约
     （Logging.Ingest）不变。
   - **运行时容器日志不做脱敏**（P11 范围 = build log 出口）：用户
     自己的 stdout 属用户自己的域；Secret 注入面（env/文件）在平台
     侧，容器自己打印的值在它自己的日志里不构成跨域泄漏。词汇边界
     随本 ADR 记入 CONTEXT.md。

3. **build 日志持久化 + P11 出口单点脱敏**：
   - builder 面向用户的日志出口唯一点是 engine 构造的 writer
     （`builder.Build(ctx, input, w)` 调用点）——本批把该 writer 升级
     为链：**redactWriter（脱敏）→ 环形缓冲（既有 live 面）+ VL ingest
     （kind=build）**。所有产生点（BuildKit SolveStatus 翻译/推送流/
     railpack 合成帧）不变，脱敏天然单点。
   - **脱敏表** = 该次构建在场 Secret 值集：`PushCred.Secret`（推送
     凭证——构建上下文中唯一真实流动的 Secret）+ `SecretFiles` 值
     （预留面首次消费）。替换形态 `secret:<sha256 前 8 hex>`
     （指纹短形态——不可逆、同值同形、可 grep 对账）。值 <8 字节不进
     表（短值误替换率高、泄漏面小，诚实阈值）。
   - VL 侧 build 日志 = `_stream{kind=build, fleetly_build=<id>}`：
     `fleetly builds logs` 读序：环形缓冲（live + 末 8 build）→ 不在
     场 → VL 回读（历史 build 全保留窗）。同一 RPC，无新面。
   - **验收锚（P11 占位锚的正式化）**：注入已知 Secret 值 → build log
     全量帧断言原始值零出现 + `secret:<fp>` 在场。
   - 错误只打一次（P11 ②）：CLI 错误信封单点（main 统一渲染 errcode +
     suggestion）核对流式动词（logs/events/builds）错误链无双打；发现
     即修，同批落 golden。

4. **检索面 = 双径路由（词汇即语义）**：
   - **无 `--text` = 实时路径**（Runtime StreamLogs，含 follow/since/
     tail——现行为字节级保持，升级零扰动；集群化修好的多节点覆盖免
     费生效）。
   - **`--text` = 检索路径**（Logging.Query → VL `/select/logsql/query`：
     LogsQL `{fleetly_project=…,fleetly_app=…[,fleetly_workload=…]} |
     ~ "<text>"` + start/end/limit（limit 即 tail 语义——VL 返回最大
     `_time` 的 N 条）；`--text --follow` → VL `/select/logsql/tail`
     端点（官方实时尾随；诚实标注 ≥5s 批汇延迟）。
   - 行级隔离：查询构造只携带请求 App 的 project/app 字段（logs:read
     scope + Team 行级授权既有链不动）；VL 单租户凭证不离开 daemon。
   - proto only-add：`StreamLogsRequest.text = 7`。CLI `fleetly logs
     --text`（golden 双形态）。`LogsService` 注记从"仅实时+最近缓冲"
     更新为双径。

5. **capability/config/装配**：
   - `capability.Logging` 端口既有（Ingest/Query）不变；Query 契约注记
     更新（检索路径语义；Follow 经 tail 端点承载）。装配
     `WithLoggingAddr`（config.logging.addr 唯一契约源，zot 同款 ctx
     注入；无 env 旧通道——本批首生即带 config 键）。
   - `Engine.Deps.Logging`（可空 = 采集/检索面停用）；reconciler
     `managedProviders()` + logging 声明（不挂项目网——发布端口可达，
     zot 同款分叉）。
   - `config.Logging{addr, retention_days}`（AppConfig 字段 6）；
     `logging.addr` 缺省空 = 停用，`retention_days` 缺省 30。

## 后果

- staging 换装预期一次受管滚动（VL 新起 + 无用户域扰动）；`fleetly
  logs` 行为零变化（无 --text 时）。运维新面：VL 数据卷纳入 Platform
  Backup 数据根清单核对（卷是节点本地卷——Platform Backup 需显式纳管
  或诚实标注不纳管，见验收锚）。
- 日志检索能力（跨任务替换/跨重启的历史 + 文本过滤）首次在位；build
  日志首次有保留窗持久化。
- 采集环是常驻 goroutine 集（每活跃域一条）——域表增长有界（配额 +
  活跃态过滤），StreamLogs 空域是 ServiceList 空结果 no-op。
- VL 不可达窗口的日志补窗深度受 docker json-file 在场限制（决策 2
  诚实边界）；日志轮转策略（daemon.json 默认无界）单列卫生挂账，不
  随本批。

## 验收锚

- [ ] 双节点 e2e：`dind-two-node.sh` 日志断言升级为两节点全覆盖（发现
  A 现状下限断言退役）+ `--text` 检索断言（VL 经采集环落地后可查）
- [ ] VL 受管域起服后：`--text` 检索命中跨重启/跨任务替换的历史行；
  mesh 端点无凭证 401（auth 执法锚）
- [ ] build log 注入已知 Secret 后全量帧零出现（P11 锚）+ `secret:<fp>`
  指纹形态在场
- [ ] VL 受管 Workload 材料指纹跨进程重启稳定（E28：无逐重启滚动）
- [ ] daemon 重启采集补窗：重启窗内产生的日志在恢复后可查（docker
  缓冲在场时）
- [ ] `fleetly logs`（无 --text）输出与升级前形态一致（实时路径零扰动）
- [ ] CLI 错误链无双打（流式动词错误路径核对）
- [ ] Platform Backup 对 VL 数据卷的口径已裁决并记 runbook（纳管或
  显式不纳管 + 恢复语义）
