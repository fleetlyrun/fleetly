# edgesets 发布失败与回滚语义设计

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案 | 2026-09-17 | [平台架构设计](2026-09-17-architecture.md) §2.5；[Swarm 底座评估](../research/2026-09-17-swarm-substrate-assessment.md) §1/§6；[交付流水线](2026-09-17-delivery-pipeline.md) §6；来源：独立设计×交叉验证（§8），用户裁决见 §3 D-REL-6 |

## 1. 现状与问题

- 草案自相矛盾：不变量写 `start-first + failure-action=pause`，默认参数表写「失败动作 rollback」；「回滚两层」（Swarm 单版自动回滚 + 平台 5 版本重放）职责不清，且 Swarm 自动回滚会清空唯一 `PreviousSpec`（源码级）。
- 源码级事实：`start-first` 下新任务 RUNNING 即移除旧任务（monitor 窗**不保护已切流流量**）；从未 RUNNING 的失败不受 5s 窗限制、必触发失败动作；新任务无法调度时 updater**没有超时**（源码 TODO）；`UpdateService` 会清空 `UpdateStatus` 并把 `PreviousSpec` 覆写为当前 spec。
- 平台承诺「失败 = 不切流量」需要覆盖全路径：启动期失败、切流后崩溃、PENDING/停滞、控制面重启、stop-first 降级、镜像不可得。

## 2. 目标设计

### 2.1 语义与不变量（四条）

1. **失败不改变有效版本**：active revision 只在观察窗通过后前移；任何失败都不移动它。
2. **Swarm 侧固定 `pause`**：新任务失败即冻结更新、旧任务继续服务；平台是唯一回滚决策者，**永不调用 Swarm 原生回滚**（不使用 `PreviousSpec`、不依赖 `--rollback`）。
3. **回滚/归位 = 按完整版本快照重放**（单层实现）：自动与手动共用同一原语，确定性、可审计、幂等、支持任意历史版本。
4. **观察窗默认只告警**（`update.onUnstable=alert`），`rollback` 为 per-app opt-in；**stop-first 失败强制恢复**（不可关闭：不回滚 = 永久宕机）。

### 2.2 窗口模型（职责分工）

| 层 | 参数（默认） | 覆盖范围 | 信号 | 平台动作 |
|---|---|---|---|---|
| L1 Swarm 更新器 | `update-monitor=5s`（平台固定，不暴露） | 任务 RUNNING 后 5s 内失败；**任何从未 RUNNING 的失败（无时限）** | `UpdateStatus.State=paused` + 任务 `Status.Err` | 按错误码分类；未切流 → 归位重放 |
| L2 平台发布看门狗 | `update.releaseTimeout=300s`（可配） | 整个 releasing 阶段 | PENDING 无进展、deadline、引擎无响应 | 确定性预检优先；超时 → 失败分类 + 归位 |
| L3 平台观察窗 | `update.observe=60s`（可配；0=关闭并发警告） | 从切流（首个目标实例健康）起 | 崩溃循环（≥2 次退出）、窗末未恢复、副本不足 ≥10s | 默认告警 + `app=degraded`；`onUnstable=rollback` → 新 deployment（kind=rollback） |
| L4 运行期稳定性 | 3 次退出 / 10min | 观察窗之后 | 退出计数 | 只告警（升级 critical），建议手动回滚；不自动 |

- Swarm monitor 保持 5s **不放大**：放大会把「已切流后崩溃」误判为更新失败（旧任务已不在，冻结语义失真）。
- 观察窗判定细则：单次退出且窗末自愈 → 警告通过（`W_DEPLOY_INSTABILITY`）；窗末 unhealthy 或崩溃循环 → 不合格。

### 2.3 发布状态机

```
queued → preparing → building → releasing → observing → succeeded
   │         │          │          │            │
   └─────────┴──────────┴──────────┴────────────┴─→ failed / cancelled
```

- `kind ∈ {deploy, rollback}`：仅**已切流回滚**创建新 deployment（`kind=rollback`，用户或 `onUnstable=rollback` 触发，走完整健康门 + 观察窗）。**归位（含 stop-first 强制恢复）不创建新 deployment**——作为失败 deployment 的 `recovery=restore` 记录在同一条目内。
- **失败分流唯一判据 = `first_healthy_at`（是否曾切流）**：
  - 未切流（null）：`recovery=restore` 同记录归位——重放最后有效 revision；`start-first` 下内容深度相等通常零任务变动（待 Spike B2 验证）；stop-first 下为重建旧实例（强制、不可取消；停机持续，`downtime_ms` 如实累计）。
  - 已切流：观察窗判定，deployment 终态为 `failed`（`verdict=unstable`）；默认 `app=degraded` + 告警（不创建新 deployment）；`onUnstable=rollback` 时额外创建 `kind=rollback` deployment（目标 `last_stable`）。
- 首次部署失败（无有效版本）：`scale=0` 保留 service、revision 与日志，`app=runtime_state=down`，事件 `deployment.substrate_halted`。
- 恢复失败（含回滚失败）：`critical`，**不再二次自动回滚**；对账对该 app 只检测不收敛。
- 引擎不可达：`recovery=blocked`，退避重试（5s/15s/60s，上限 5min），持久化可续跑。
- 控制面重启：启动扫描非终态 deployment——健康 → 重开完整观察窗；`paused/failed` → 分类 + 归位；无法判定 → 失败 + 人工。
- cancel：`queued/preparing/building/releasing(未健康)` 可 cancel（先归位再落 cancelled）；曾健康不可 cancel（409，建议改用 rollback）。

### 2.4 版本快照与任意版本重放

- **`revisions` 表**（每 app 每个产物一条）：`number`、`image_digest`、`resolved_spec`（完整物化快照：processes/command/port/health/resources/replicas/mounts/placement/非密钥 env/路由目标，不含任何底座字段名）、`spec_hash`、`source_kind/source_ref`、`status ∈ {candidate|active|superseded|retired}`。
- **保留与可重放集合**：`update.keepVersions=5`（1..20）；仅 `active|superseded`（曾完整通过观察窗）可重放；超限 → `retired`（显式可读，不静默消失）；回滚目标越界 → `E_DEPLOY_TARGET_RETIRED`。
- **重放语义**：workload 字段按快照回放；**非密钥 env 随快照回滚**（env 变更必须经部署固化为新版本，天然形成可回退点）；治理参数（observe/onUnstable/keepVersions）取**当前** spec（前瞻设置不随版本回退）；**secret 值永远取当前**（回滚不撤销密钥轮换，文档明示）；卷数据、DB 迁移、DNS 不回滚。
- **preflight**：镜像可得性（v0.1 本地 inspect / v0.2 registry HEAD）、约束可满足、secret 存在、spec 合法——任一失败在动底座之前失败。v0.1 无 registry 时镜像被清理 → `E_IMAGE_UNAVAILABLE` + `W_ROLLBACK_IMAGE_RISK`（提示保留镜像或重建）。
- 归位零成本：`IsTaskDirty` 按 task spec 深度相等判断，同内容重放不重建任务（**Spike B2 断言 task id 不变**，未验证前标注「待验证」）。

### 2.5 场景行为矩阵

| # | 场景 | 判定码 | 曾切流 | 动作 | 流量影响 |
|---|---|---|---|---|---|
| 1 | spec 校验失败 | `E_SPEC_INVALID` | — | 不动底座 | 无 |
| 2 | 构建失败 | `E_BUILD_FAILED` | — | 不动底座 | 无 |
| 3 | 镜像拉取失败/本地缺失 | `E_IMAGE_PULL_FAILED` | 否 | 归位（通常零动作） | 无 |
| 4 | 约束 0 节点匹配（确定性预检） | `E_SCHEDULER_NO_FIT` | 否 | 部署前快速失败 | 无 |
| 5 | PENDING 至 `releaseTimeout` | `E_SCHEDULER_PENDING_TIMEOUT` | 否 | 归位 | 无（start-first） |
| 6 | 容器启动即崩 | `E_TASK_START_FAILED` | 否 | Swarm 已 pause → 归位 | 无 |
| 7 | health 永不通过 | `E_HEALTH_TIMEOUT` | 否 | 同上（附 health 配置与探测输出） | 无 |
| 8 | 观察窗崩溃循环（≥2 次退出） | `E_OBSERVE_CRASH_LOOP` | 是 | 默认告警；opt-in 回滚 | 已在新版本；回滚 = 二次切换 |
| 9 | 观察窗窗末 unhealthy | `E_OBSERVE_UNHEALTHY` | 是 | 同上 | 同上 |
| 10 | 单次退出且自愈 | `W_DEPLOY_INSTABILITY` | 是 | 警告通过，记不稳定计数 | 无 |
| 11 | 观察窗后崩溃（3/10min） | `E_DEPLOY_POST_WINDOW_UNSTABLE`（warning→critical） | 是 | 只告警 + 建议 `edgesets rollback` | 由 Swarm 重启自愈，间歇失败 |
| 12 | 控制面重启 | `E_DEPLOY_INTERRUPTED` | 视评估 | 分类恢复（2.3） | 视现场 |
| 13 | 引擎不可达 | `E_RUNTIME_UNAVAILABLE` | 视现场 | recovery blocked + 退避重试 | 无法保证，critical 告警 |
| 14 | stop-first 失败 | `E_DEPLOY_DOWNTIME_FAILED`（恢复失败 `E_DEPLOY_DOWNTIME_RECOVERY_FAILED`） | 否 | **强制归位**，停机持续 | 停机窗口 = 判定 + 恢复，如实告知 |
| 15 | 首发失败 | `deployment.substrate_halted` | — | scale=0 保留现场 | 应用本就不可用 |

### 2.6 stop-first 专表（有卷/固定 host 端口/global）

- 硬 preflight（镜像/约束/secret/端口）**在停机之前**执行，失败零停机。
- 失败 = 强制归位重放（不受 `onUnstable` 影响），停机持续到恢复完成；`downtime_ms` 与 `deployment.downtime_started/ended` 事件如实记录。
- `update.onUnstable` 对 stop-first 无意义（归位是恢复不是策略），不对用户暴露该组合的歧义文案。
- 对外口径：文档与 UI 明示「有卷/固定端口应用不承诺零停机」（既有降级表）。

### 2.7 错误码、事件、审计、guarantees

- **错误码**（注册表只增不复用，格式 `E_<域>_<条件>`）：`E_SPEC_INVALID`、`E_BUILD_FAILED`、`E_IMAGE_PULL_FAILED`、`E_IMAGE_UNAVAILABLE`、`E_SCHEDULER_NO_FIT`、`E_SCHEDULER_PENDING_TIMEOUT`、`E_TASK_START_FAILED`、`E_HEALTH_TIMEOUT`、`E_RELEASE_STALLED`、`E_OBSERVE_CRASH_LOOP`、`E_OBSERVE_UNHEALTHY`、`E_DEPLOY_INTERRUPTED`、`E_DEPLOY_POST_WINDOW_UNSTABLE`、`E_DEPLOY_DOWNTIME_FAILED`、`E_DEPLOY_DOWNTIME_RECOVERY_FAILED`、`E_ROLLBACK_FAILED`、`E_ROLLBACK_TARGET_RETIRED`、`E_ROLLBACK_NO_TARGET`、`E_RUNTIME_UNAVAILABLE`。警告码：`W_DEPLOY_INSTABILITY`、`W_DEPLOY_NO_HEALTHCHECK`、`W_DEPLOY_OBSERVE_DISABLED`、`W_ROLLBACK_IMAGE_RISK`。
- **事件**：`deployment.{queued,release_started,healthy,switched,observe_started,succeeded,failed,warning,aborted,rollback_started,rollback_finished,rollback_failed,recovery_scheduled,recovery_blocked,substrate_halted,superseded}`、`app.{degraded,instability_detected,recovered}`。
- **审计**：`deployment.create/cancel/rollback`（human/agent）、`deployment.auto_abort/auto_rollback/recovery_retry`（system + reason=错误码）；自动动作必入审计。
- **`guarantees` 块**（deployment 资源机读字段）：`health_gate`（spec/镜像/none）、`traffic_switch`（zero_downtime/downtime_window）、`observation`（on/off）、`auto_rollback`（on/off）——所有降级透明可机读。
- 错误信封统一：`{code, message, phase, deployment_id, suggestion, context{raw/log_tail/exit_code/...}, docs}`；部署失败是资源终态而非 HTTP 错误。

### 2.8 spec 字段（edgesets.yaml v1alpha 增量）

```yaml
update:
  strategy: auto          # auto=按能力选择（有卷/固定端口/global→stop-first 并告知）；显式 start-first 与卷/端口冲突→E_SPEC_UNSAFE_STRATEGY；显式 stop-first 允许
  observe: 60s            # 0..600s；0=关闭（警告 + guarantees）
  onUnstable: alert       # alert（默认）| rollback（per-app opt-in）
  keepVersions: 5         # 1..20；1=无回滚目标（警告）
  releaseTimeout: 300s    # 30s..3600s；有效值 ≥ health 预算
  stopGrace: 60s          # 5s..600s
health:
  path: /healthz          # 与 cmd 互斥
  cmd: ""                 # 逃生舱：镜像无 HTTP 客户端时
  interval: 5s / timeout: 3s / retries: 3 / startPeriod: 10s
```

健康门解析优先级：spec `health` > 镜像 `HEALTHCHECK` > none（`W_DEPLOY_NO_HEALTHCHECK` + `guarantees.health_gate=none`）。

### 2.9 系统性失败处理

- v0.1：跨 app 同签名失败（10min ≥3 个，如 registry/节点/构建器故障）→ 事件 `system.failure_suspected` + 页面告警（只告警）。
- v0.2：升级为熔断——暂停新部署入队与自动回滚链（放行归位/恢复类操作），5min 无新增后自动解除；事件 `system.breaker_opened/closed` + 审计。

## 3. 关键决策及理由

| # | 决策 | 理由 | 被否选项及原因 | 来源 |
|---|---|---|---|---|
| D-REL-1 | `failure-action=pause` 固定，不使用 Swarm 原生回滚 | 自动回滚清空唯一 PreviousSpec、不覆盖 PENDING、首发卡 `UPDATING`；pause 保留旧任务与 spec，恢复动作幂等可审计 | `rollback`（历史槽与 PENDING 缺陷）；`none`（静默带病） | 独立收敛 |
| D-REL-2 | 回滚/归位 = 完整快照重放（单层） | 只回 digest 会把坏 env/health 留在原地；自动与手动共用原语，支持任意 N | Swarm `--rollback`（语义不可控：Spec/PreviousSpec 互换） | 独立收敛 |
| D-REL-3 | 窗口三层分工（2.2） | monitor 从 RUNNING 起算且只判启动期；PENDING 无超时需看门狗；切流后归观察窗 | 放大 monitor（语义失真）；只靠轮询（丢失任务级失败原因） | 独立收敛 |
| D-REL-4 | 失败分流判据 = `first_healthy_at` | 健康前后是「流量是否已切」的物理分界；状态标签有竞态 | 按阶段标签分流（竞态下误判） | 独立收敛 |
| D-REL-5 | 首发失败 scale=0 保留现场 | 停掉崩溃循环且保留诊断；重发零迁移成本 | 保持 crash-loop（噪音）；删 service（丢诊断） | 裁决（A 优于 B） |
| D-REL-6 | **观察窗默认只告警**；`onUnstable=rollback` 为 opt-in；stop-first 强制恢复 | **用户裁决**：与 D11「检测开、收敛 opt-in」哲学一致；对外部依赖故障无效回滚会震荡。严格判据（崩溃循环/窗末未恢复）在 opt-in 时生效 | 默认自动回滚（静默改变运行版本，与不静默原则张力） | 用户裁决 |
| D-REL-7 | 未切流归位为同记录 recovery；已切流回滚为新 deployment | 未切流常为零动作、不应污染版本历史；已切流回滚需可观察/可审计/可再回滚 | 全部新 deployment（噪音）；全部同记录（历史不可读） | 裁决（合并） |
| D-REL-8 | stop-first 失败强制归位、`downtime_ms` 诚实累计 | 不回滚=永久宕机，无正当场景；停机必须如实告知 | 尊重 `onUnstable=alert`（服务中断时袖手旁观） | 独立收敛 |
| D-REL-9 | 治理参数取当前 spec、secret 值取当前、非密钥 env 随版本回滚 | 前瞻设置不随版本回退；密钥回滚是安全事故；非密钥 env 属于 workload，不回退会让「完整快照」名不副实（env 变更必须经部署固化，天然形成可回退点） | 全量快照含治理参数/密钥（静默回退与安全事故）；env 取当前（回滚半到位） | 独立收敛 |
| D-REL-10 | 首发/回滚失败不再二次自动回滚 | 断链防震荡；恢复失败是 critical 人工介入场景 | 自动重试（叠加失败与掩盖根因） | 独立收敛 |
| D-REL-11 | 系统性失败 v0.1 告警、v0.2 熔断 | 防集体回滚震荡并暴露根因；避免 v0.1 机制过重 | 只做 app 级（无法区分单应用与平台故障） | 独有（B） |

## 4. 分步实施

- **v0.1**（单节点）：pause 固定、错误码/事件/guarantees、`update.*` 解析与校验、revisions 快照与任意版本重放、首发 scale=0、控制面重启分类恢复、`W_ROLLBACK_IMAGE_RISK`。
- **v0.2**：多副本部分切流细则、系统性失败熔断、MCP 只读查询（`get_deployment`/`list_revisions`/`rollback` 两段式确认）。
- 与交付流水线映射见 §6。

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| start-first 重发布期 LB 端点可能早于 health 加入（开放问题） | 「失败=不切流量」在重发布场景失真（少量 5xx） | 最高优先级 Spike B 验证；若成立：观察窗起点前移 + 对外口径降级为「新连接零失败、切换期少量 5xx」；技术预案（若口径不可接受）：路由由 VIP 改为平台自管 task 级注册（`tasks.<service>` + 健康过滤） |
| 归位零成本依赖 `IsTaskDirty` 深度相等（未验证） | 归位多做一次滚动（功能仍正确） | Spike B2 断言 task id 不变；不成立则接受一次滚动并修正文档 |
| 冻结→归位竞态（窗口内旧任务节点 DOWN） | 按失败 spec 重建错误版本任务 | 归位 p95 <2s；故障注入测试；错误任务不会通过 health 接管流量 |
| 观察窗阈值（60s/10s/重启判定）为拍值 | 误报或漏报 | dogfood + 3 类真实应用校准；平台默认可调 |
| v0.1 无 registry，回滚镜像被清理 | 回滚目标不可用 | preflight + `W_ROLLBACK_IMAGE_RISK` + 文档；不自动清理镜像 |
| 恢复动作覆写 `PreviousSpec` | 运维手动 `--rollback` 滚向失败版本 | DR 注记 + CLI 警告 |

## 6. 测试与验收

- **Spike B 扩展**（作为通过标准）：B1 health 失败 → 更新 paused、旧任务不中断；B2 pause 后同内容重放任务零替换；B3 start-period 内任务是否进 LB 端点（最高优先级）；B4 失败矩阵逐条错误码断言；B5 stop-first 强制归位与停机账。
- **nightly**：场景矩阵 1-15 的 E2E（含控制面重启、引擎不可达恢复 blocked→恢复）；错误码注册表快照；回滚（快照重放）一分钟内完成。
- **release**：上一版本 → 新版本升级 E2E 中的非终态 deployment 恢复。

## 7. 明确不做

- 不使用 Swarm 原生回滚 / `PreviousSpec` 迁移路径
- 观察窗默认不自动回滚；窗口后不自动回滚
- 不自动重试失败发布；不做 dry-run 预启动（假阴性）
- 不版本化 secret 值（回滚不撤销轮换）
- 不承诺「切换期绝对零失败」（以 Spike B3 结论校准口径）

## 8. 来源与验证（独立设计×交叉验证）

- **独立收敛（两份设计分别得出）**：pause 固定；单层快照重放；三层窗口；窗后只告警；stop-first 强制恢复；失败分流按是否切流；数据/迁移/secret 不回滚；治理字段进 spec、substrate 参数不进 spec；错误码/事件/审计/guarantees。
- **裁决**：首发 scale=0（A 论证硬）；恢复建模同记录/新 deployment 二分（合并）；PENDING 只做确定性预判（B 更安全，A 自报近似判定风险）；系统性熔断降级为 v0.2（B 独有，先告警）。
- **用户裁决**：观察窗默认告警（D-REL-6）。
- **独有并验证后并入**：`IsTaskDirty` 归位零成本（B，待 Spike）；LB 端点早于 health（B，开放问题）；guarantees 块、`W_ROLLBACK_IMAGE_RISK`、`downtime_ms` 诚实累计（A）；恢复覆写 PreviousSpec 注记（A）。
- **开放问题（禁当承诺）**：LB 端点时机；归位零成本；冻结→归位竞态；观察窗阈值校准。
