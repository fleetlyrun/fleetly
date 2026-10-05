# ADR-0041: Metrics Provider——cadvisor 全局采集、VictoriaMetrics 存储、engine 原生阈值告警

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-04 | CONTEXT.md Metrics/Notification 词条（新增）、ADR-0004（受管自宿唯一 reconciler）、ADR-0014（材料纪律/信封）、ADR-0021（victoria 系选型）、ADR-0035（行级授权）、ADR-0036 E28（材料字节稳定）、ADR-0039（PlatformBackup.S3 面——doctor 挂账消化）、ADR-0040（F2.4 采集环/受管范式直系先例）、F2.5（checklist）、F2.6（Console 图表消费方） |

## 背景

F2.5 三件：①victoria 系受管自宿指标存储；②基础图表数据面（容器 CPU/内存——图表本体在 F2.6
Console，本批交付查询 API）；③阈值告警 + 通知通道（webhook/telegram 起步）。随批消化
F2.2 挂账：**「未配异地持续告警」**（platform_backup.s3 未配置时应有持续告警面）。
核心约束差：**docker 无集群面 stats API**（ServiceLogs 的日志面先例不适用于 stats——
`/containers/{id}/stats` 只在本节点 daemon），多节点容器指标必须每节点有采集端点。

## 决策

1. **存储 = VictoriaMetrics 单节点受管自宿（ADR-0040 范式直系复用）**：
   - 镜像钉版 `victoriametrics/victoria-metrics:v1.152.0`（2026-09-14 发布三周场龄；
     入口 `/victoria-metrics-prod` 绝对路径，镜像 config 实证 2026-10-04）。升级走
     Platform 升级序。
   - 受管 Workload `fleetly-metrics-victoriametrics`（域 `fleetly/system/metrics`）：
     8428 mesh 发布（daemon 查询/推送经节点地址可达）；数据卷
     `fleetly-metrics-victoriametrics` → `-storageDataPath`（带卷 → 钉控制面节点）；
     `StopGrace 60s`；`-retentionPeriod` 来自 `config.metrics.retention_days`（缺省
     30d）。
   - 认证与 VL 同款：`-httpAuth.username/-httpAuth.password`（密码首启随机落
     `<DataRoot>/keys/victoriametrics.json`，0o600 tmp+rename，E28 字节稳定）经材料
     `file://` 注入——**绝不进 argv**。VM 同样是平台单租户：查询隔离由平台查询构造
     执法（Console/API 面按 App 域字段过滤）。
   - `config.metrics.addr` 空 = Metrics 面停用（零扰动降级：无采集/无告警/查询精确
     失败）。install.sh 与 registry/logging 同源物化 `<advertise>:8428`。

2. **采集 = cadvisor 全局服务（每节点）+ engine 集中 scrape（第 10 收敛环）**：
   - **docker stats 无集群 API 是硬约束**——F2.4 的"控制面集中采集"形态在此只完成
     一半：cadvisor `gcr.io/cadvisor/cadvisor:v0.55.1` 以 **global 模式**跑每节点
     （受管域 `fleetly/system/metrics` 第二个 Workload；host 发布 8080 = 每节点端点），
     engine 集中 scrape（与日志采集同构——ADR-0040 决策 2 的"global 采集 Workload
     升级路径"在 stats 面被硬约束前置兑现）。
   - **Runtime IR 三扩展**（通用概念，k4s 各有自然映射）：
     - `Workload.Global bool`（swarm `Mode.Global`；k8s=DaemonSet）——Replicas 与
       Global 互斥，Global 优先；
     - `Workload.HostBinds []HostBind{Source, Target, ReadOnly}`（swarm bind mount；
       k8s=hostPath）——受管域专用声明面，用户 Spec 不暴露（投影面白名单不进）；
     - `PortPublish.Mode`（`ingress`（缺省=routing mesh，现行为零扰动）| `host`
       （宿主网络栈，每节点一个端点））。
   - cadvisor 声明：host binds 只读 `/→/rootfs`、`/var/run→/var/run`、`/sys→/sys`、
     `/var/lib/docker→/var/lib/docker`（官方 run 形态；`/var/run` 含 docker.sock 供
     容器元数据）。**不用 privileged**（只读挂载面够 cpu/mem 主链；e2e/staging 实证
     兜底）。
   - engine `metricsStep`（节拍 15s，环内自持粗节拍——loop.go ticker 单源纪律，节拍
     锚仿 platformBackupDue）：`DescribeCluster` 取节点集（**NodeView 扩 `Addr`**——
     swarm `node.Status.Addr` 现成搬运，观测缓存不落库）→ 每可用节点
     `GET http://<addr>:8080/metrics`（10s 上界）→ **原文透传** VM
     `/api/v1/import/prometheus?extra_label=job=fleetly-cadvisor&extra_label=node=<平台节点ID>`
     （平台节点归因进每条序列；cadvisor 自带 container_label_fleetly_ns_* 序列标签
     ——per-App 过滤的锚）→ 同一遍在手法**评估告警规则**（见决策 3——存储与评估
     同一数据面，评估零查询依赖）。节点抓取失败告警日志退避（同采集环形态）；
     **节点下线 = 该节点序列停写（VM 断口诚实呈现），不补偿**。
   - **诚实边界（记档不遮掩）**：cadvisor 无认证面——`<node>:8080` 对能路由到节点
     IP 的任何一方暴露全节点容器元数据与用量（含容器名/标签 = 跨 Project 可见性）。
     v1 接受（与 edge_config 无认证同类：VPC-only 部署边界，runbook 端口表入册）；
     **多租户启用前的挂账**（收窄形态届时裁决：前置认证代理或节点防火墙）。

3. **告警 = engine 原生阈值评估（不引 vmalert/Alertmanager）**：
   - 评估在 scrape 遍内完成（内存最新样本 × 规则表）：`cpu_percent`（cadvisor
     `container_cpu_usage_seconds_total` 增量率 / `machine_cpu_cores` ×100，per-container
     率经 15s 窗差分，进程内保存上次值）与 `memory_working_set_bytes`（gauge 直取）；
     规则按 App 聚合（该 App 全部容器取 max——最热副本代表）。
   - 规则行 `alert_rules`（迁移 00022）：per-App、metric 值域、threshold、
     `for_secs` 持续窗（缺省 0=立即）、enabled、状态机 `ok|firing` + `state_since`
     （连续越限 ≥for_secs → firing；回落 → ok）。**通知只在状态迁移沿触发**
     （fired/resolved 各一次，不逐拍轰炸）。
   - **系统内置规则 `platform-offsite-backup`（F2.2 挂账消化）**：非用户行——
     代码内评估（`PlatformBackupConfig.S3 == nil` 持续 24h → firing），ListAlertStates
     以系统行呈现、不可删；doctor 新检查同源（见决策 5）。
   - 事件：`alert.fired` / `alert.resolved`（eventcode +2，outbox 三链惯例）+ 审计。

4. **通知通道 `notification_channels`（迁移 00022 同批）**：
   - kind = `webhook`（POST JSON 载荷：rule/app/metric/value/threshold/state/
     fired_at——HMAC 不做，通道 URL 即凭证）| `telegram`（sendMessage API；bot_token
     + chat_id）。配置 age 信封（ADR-0014——URL/token 是凭证材料）。
   - 派发：engine 侧迁移沿触发、全通道并行、单通道失败记行不阻断（
     `last_failure` 诊断列）；`test` 动词即时发一条测试载荷（验收锚）。
   - CLI：`channels create/list/delete/test` + `alerts rules create/list/delete` +
     `alerts list`（现行状态）；`metrics query`（PromQL 透传，诊断/图表地基）。

5. **API 面（proto 先行）**：telemetry 上下文 +`MetricsService`（`QueryMetrics`：
   PromQL 透传 + start/end/step → 序列点集；Console F2.6 图表的唯一数据源；
   `metrics:read`）+ `AlertingService`（channels/rules/states CRUD，
   `channels:write|read` + `alerts:write|read` scope 新资源喂食 + freeze 桶 +
   idem 面 + 行级授权（规则 per-App 链、通道平台级））。doctor 两件：①s3 未配
   AND 无启用通道 → warn（可行动文本）；②内置规则状态进 status 诊断行。

## 后果

- 受管域从三件到五件（+VM、+cadvisor 全局）；reconciler 指纹集扩——cadvisor 全局
  任务的 spec 漂移面（bind 路径不存在等）在 Ensure 失败面呈现。
- staging 换装预期：VM 新起 + cadvisor 全局起服（每节点一 task）；用户域零扰动。
- Platform Backup 口径：VM 卷同 VL 卷（显式不纳管——手工卷 tar 纪律；指标可重采，
  丢失只损历史图表）。
- 评估窗口 15s + for_secs 语义是**采样近似**（非精确持续窗——15s 节拍采样序列的
  连续判定；诚实记入规则文档）。
- 通道出站依赖节点出网（telegram 需公网出站；webhook 同）——staging 有，dind e2e
  走假 webhook 接收器（e2e 既有 h2cserver 形态复用）。

## 验收锚

（2026-10-05 评审批 P1-3/P3-2 收口勾稽：七锚逐条核实后勾——CI 锚指向
e2e/dind-smoke.sh 与 e2e/dind-two-node.sh 的断言块，单测锚指向本仓测试。）

- [x] e2e（dind 两节点）：cadvisor 全局每节点一 task + VM 1/1；`metrics query` 返回
  双节点容器序列（node 标签双值）；内存序列非空
  〔e2e/dind-two-node.sh 尾段：VM 1/1 + cadvisor **2/2**（全局每节点一 task）+
  从 `nodes list` 取两平台 ID 逐节点查 `max(container_cpu_usage_seconds_total{job="fleetly-cadvisor",node=<id>})`
  非空（node 标签双值）；两 dind 补 `--cgroupns=host`（14abbae 的 smoke 修正
  同款——嵌套 cgroup 可见面，容器样本才不缺席）；staging 真机同款 2/2 在册
  （runbook 2026-10-04 记录·四）〕
- [x] 阈值规则全链：规则（memory > 极小值）→ firing → webhook 接收器收到
  alert.fired 载荷 → 规则删除/回落收到 resolved
  〔e2e/dind-smoke.sh F2.5 段（能力门控语义对齐 0f8d4da——CI dind 门控在场
  时全链）：`e2e/webhookrecv` 假接收器（dind 内 loopback）收 fired 载荷并按
  type/rule_id/app_id/metric/state 真值断言；**resolved 走回落形态**——拆 App
  使容器样本消失 → firing→ok 迁移沿派发（规则行删除本身不派发，行删除即
  无评估面——engine evaluateRules 语义，勾稽时核实现场事实）；单测双锚
  engine/metrics_test.go TestEvaluateRulesStateMachine（httptest 派发面）〕
- [x] 通知通道 test 动词：webhook 接收器收到测试载荷；telegram 未配出网环境跳过
  （e2e 只测 webhook）
  〔e2e/dind-smoke.sh 通道链双面：可达端点（接收器 /hook）→ `channels test`
  delivered=true + 接收器收 alert_test 载荷（字段真值断言）；不可达端点 →
  delivered=false 诚实失败（既有断言保留）；telegram 维持 e2e 只测 webhook〕
- [x] doctor：s3 未配 + 无通道 → warn 文本可行动；配通道后消警
  〔单测 cmd/fleetly/cmd/doctor_golden_test.go TestDoctorAlertingChecks 三面：
  无通道 warn 带可行动建议 / 内置规则 firing warn 带 platform_backup.s3 处置
  （24h 持续窗不进 e2e——单测是唯一锚）/ 通道在场消警 ok；e2e dind-smoke
  同款双面（3c 无通道 warn + F2.5 配通道后 ok）〕
- [x] VM 材料指纹跨进程重启稳定（E28）；8428 无凭证 401
  〔E28：internal/providers/victoriametrics/provider_test.go
  TestMaterialsStableAcrossRestarts（50 次跨构造字节稳定）+
  TestCredentialPersistRoundTrip（幂等/权限/fail loud）；8428 四面锚：CI
  e2e/dind-two-node.sh 无凭证 401 + 平台凭证 200（busybox wget --header），
  staging 真机 401（runbook 2026-10-04 记录·四）〕
- [ ] staging 真机：双节点序列 + 一条告警真发（webhook 到 requestbin 类端点或
  staging 本机接收器）+ runbook 回写
  〔**偏差注（不整条勾）**：staging 双节点序列 + 阈值 firing + 8428 401 已真机
  （runbook 2026-10-04 记录·四）；告警**真发到外部端点**（requestbin 类或
  staging 本机接收器）未做——真投递由本批 CI 假接收器锚承载
  （alert.fired/resolved 字段真值断言）；staging 外投真机随换装批补〕
- [x] Metrics 面停用（addr 空）升级零扰动：无新受管服务、CLI 精确失败
  〔装配面 internal/assembly/provides_test.go TestMetricsFaceDisabledWhenAddrEmpty：
  工厂在册的同一夹具下 addr 空 → nil Provider（受管声明不进 reconciler 集 =
  无新受管服务，addr 在场正形态对照）；查询精确失败 apitest/alerting_test.go
  TestMetricsQueryDisabledFace（E_INTERNAL 信封——夹具未配 metrics.addr）〕
