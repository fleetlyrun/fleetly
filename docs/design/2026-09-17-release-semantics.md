# fleetly 发布失败与回滚语义设计

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案 | 2026-09-17 | [平台架构设计](2026-09-17-architecture.md) §2.5（应用模型 = Compose 规范）；[Swarm 底座评估](../research/2026-09-17-swarm-substrate-assessment.md) §1/§6；[交付流水线](2026-09-17-delivery-pipeline.md) §6；来源：独立设计×交叉验证（§8），用户裁决见 §3 D-REL-6；2026-09-17 审核裁决轮：env 快照按三层合并结果（架构 §2.4）、`blocked_waiting` 入状态机、`deployment.cancelled` 事件命名对齐；2026-09-17 Spike B 回写：B2/B3 关闭、归位禁 `--force`、stop-first 停机实测 10–12s |

## 1. 现状与问题

- 草案自相矛盾：不变量写 `start-first + failure-action=pause`，默认参数表写「失败动作 rollback」；「回滚两层」（Swarm 单版自动回滚 + 平台 5 版本重放）职责不清，且 Swarm 自动回滚会清空唯一 `PreviousSpec`（源码级）。
- 源码级事实：`start-first` 下新任务 RUNNING 即移除旧任务（monitor 窗**不保护已切流流量**）；从未 RUNNING 的失败不受 5s 窗限制、必触发失败动作；新任务无法调度时 updater**没有超时**（源码 TODO）；`UpdateService` 会清空 `UpdateStatus` 并把 `PreviousSpec` 覆写为当前 spec。
- 平台承诺「失败 = 不切流量」需要覆盖全路径：启动期失败、切流后崩溃、PENDING/停滞、控制面重启、stop-first 降级、镜像不可得。

## 2. 目标设计

### 2.1 语义与不变量（四条）

1. **失败不改变有效版本**：active revision 只在观察窗通过后前移；任何失败都不移动它。
2. **Swarm 侧固定 `pause`**：新任务失败即冻结更新、旧任务继续服务；平台是唯一回滚决策者，**永不调用 Swarm 原生回滚**（不使用 `PreviousSpec`、不依赖 `--rollback`）。
3. **回滚/归位 = 按完整版本快照重放**（单层实现）：自动与手动共用同一原语，确定性、可审计、幂等、支持任意历史版本。
4. **观察窗默认只告警**，`rollback` 为平台侧 per-app opt-in（v0.1 无文件字段）；**stop-first 失败强制恢复**（不可关闭：不回滚 = 永久宕机）。

### 2.2 窗口模型（职责分工）

| 层 | 参数（默认） | 覆盖范围 | 信号 | 平台动作 |
|---|---|---|---|---|
| L1 Swarm 更新器 | `update-monitor=5s`（平台固定，不暴露） | 任务 RUNNING 后 5s 内失败；**任何从未 RUNNING 的失败（无时限）** | `UpdateStatus.State=paused` + 任务 `Status.Err` | 按错误码分类；未切流 → 归位重放 |
| L2 平台发布看门狗 | `releaseTimeout=300s`（平台默认） | 整个 releasing 阶段（**平台已知的绑定节点不可用除外**：进入 `blocked_waiting` 暂停计时，恢复后重新起算，见放置专项） | PENDING 无进展、deadline、引擎无响应 | 平台自身对象的确定性预检（绑定节点/镜像/secret/端口）；超时 → 失败分类 + 归位 |
| L3 平台观察窗 | `observe=60s`（平台默认；v0.1 无文件配置） | 从切流（首个目标实例健康）起 | 崩溃循环（≥2 次退出）、窗末未恢复、副本不足 ≥10s | 默认告警 + `app=degraded`；平台侧 opt-in `rollback` → 新 deployment（kind=rollback） |
| L4 运行期稳定性 | 无计数机制 | 观察窗之后 | 任务失败/退出事件 | 只告警一次（不做 3 次/10min 计数与升级规则）；建议手动回滚 |

- Swarm monitor 保持 5s **不放大**：放大会把「已切流后崩溃」误判为更新失败（旧任务已不在，冻结语义失真）。
- 多副本：切流起点保持「首个目标实例健康」；观察窗副本水位按 compose `deploy.replicas`（desired）判定。`replicas` 字段 v0.1 即照用（无卷/无固定端口服务），缩放 UI/指标水位等管理面属 v0.2。
- 观察窗判定细则：单次退出且窗末自愈 → 警告通过（`W_DEPLOY_INSTABILITY`）；窗末 unhealthy 或崩溃循环 → 不合格。
- **UX 补偿（默认不改）**：`app=degraded`（对应 deployment `verdict=unstable`）为一等状态——UI 横幅 + 一键回滚按钮 + 通知，使可感知度对齐 Dokploy 的自动回滚体验。

### 2.3 发布状态机

```
queued → preparing → building → releasing → observing → succeeded
   │         │          │          │            │
   └─────────┴──────────┴──────────┴────────────┴─→ failed / cancelled
```

- `releasing` 含子状态 `blocked_waiting`：发布中绑定节点 DOWN → 看门狗暂停计时、可 cancel，节点恢复续跑并重新起算（场景 15）；绑定节点 REMOVED → 直接失败，不进 `blocked_waiting`（场景 16）。
- `kind ∈ {deploy, rollback}`：仅**已切流回滚**创建新 deployment（`kind=rollback`，用户或平台侧 opt-in rollback 触发，走完整健康门 + 观察窗）。**归位（含 stop-first 强制恢复）不创建新 deployment**——作为失败 deployment 的 `recovery=restore` 记录在同一条目内。
- **失败分流唯一判据 = `first_healthy_at`（是否曾切流）**：
  - 未切流（null）：`recovery=restore` 同记录归位——重放最后有效 revision；`start-first` 下内容深度相等通常零任务变动（待 Spike B2 验证）；stop-first 下为重建旧实例（强制、不可取消；停机持续，`downtime_ms` 如实累计）。
  - 已切流：观察窗判定，deployment 终态为 `failed`（`verdict=unstable`）；默认 `app=degraded` + 告警（不创建新 deployment）；平台侧 opt-in rollback 时额外创建 `kind=rollback` deployment（目标 `last_stable`）。
- 首次部署失败（无有效版本）：`scale=0` 保留 service、revision 与日志，`app.state=down`，事件 `deployment.substrate_halted`。
- 恢复失败（含回滚失败）：`critical`，**不再二次自动回滚**；对账对该 app 只检测不收敛。
- 引擎不可达：`recovery=blocked`，退避重试（5s/15s/60s，上限 5min），持久化可续跑。
- 控制面重启：启动扫描非终态 deployment——健康 → 重开完整观察窗；`paused/failed` → 分类 + 归位；无法判定 → 失败 + 人工。
- cancel：`queued/preparing/building/releasing(未健康)` 可 cancel（先归位再落 cancelled）；曾健康不可 cancel（409，建议改用 rollback）。

### 2.4 版本快照与回滚（最近 5 版）

- **`revisions` 表**（每 app 每次成功部署一条）：`number`、`compose_normalized`（归一化 compose：受控子集内的服务与卷定义；**env 按三层合并结果快照**〔`key:sha256` + 来源标注，链式规则见架构 §2.4 变量合并〕，按字面值、无插值）、平台覆盖层（镜像 digest、secret 引用、路由绑定、节点绑定）、`spec_hash`（按合并结果计算）、`source_kind/source_ref`、`status ∈ {candidate|active|superseded}`。
- **保留与可重放集合**：固定保留最近 5 次**成功**部署；列表即选项（列不出来的不可回滚，没有额外的状态机与错误码）；回滚目标越界 → `E_ROLLBACK_NO_TARGET`。
- **重放语义**：compose 字段按快照回放（**非密钥 env 随快照回滚**；env 变更必须经部署固化为新版本，天然形成可回退点）；治理参数（observe/onUnstable/keepVersions）取**当前平台配置**（前瞻设置不随版本回退）；**secret 值永远取当前**（回滚不撤销密钥轮换，文档明示）；卷数据、DB 迁移、DNS 不回滚。
- **preflight**：镜像可得性（v0.1 本地 inspect / v0.2 registry HEAD）、约束可满足、secret 存在、compose 合法（受控子集/受管字段）——任一失败在动底座之前失败。v0.1 无 registry 时镜像被清理 → `E_IMAGE_UNAVAILABLE` + `W_ROLLBACK_IMAGE_RISK`（提示保留镜像或重建）。
- 归位零成本：`IsTaskDirty` 按 task spec 深度相等判断，同内容重放不重建任务——**2026-09-17 Spike B2 已实测验证**：旧 task id 跨「失败 + 同内容重放」不变、零新增任务；字段脏检矩阵：container-label/env/restart-policy 变更与 `--force` 触发任务重建，service-label/update-config 不触发。**平台纪律：归位重放禁用 `--force`**。

### 2.5 场景行为矩阵

| # | 场景 | 判定码 | 曾切流 | 动作 | 流量影响 |
|---|---|---|---|---|---|
| 1 | compose 校验失败（子集/受管字段/策略冲突） | `E_COMPOSE_UNSUPPORTED` / `E_COMPOSE_MANAGED_FIELD` / `E_COMPOSE_UNSAFE_STRATEGY` | — | 不动底座 | 无 |
| 2 | 构建失败 | `E_BUILD_FAILED` | — | 不动底座 | 无 |
| 3 | 镜像拉取失败/本地缺失 | `E_IMAGE_PULL_FAILED` | 否 | 归位（通常零动作） | 无 |
| 4 | PENDING 至 `releaseTimeout`（含无法调度/资源不足；**平台已知的绑定节点不可用除外，见 15/16**） | `E_SCHEDULER_PENDING_TIMEOUT` | 否 | 归位 | 无（start-first） |
| 5 | 容器启动即崩 | `E_TASK_START_FAILED` | 否 | Swarm 已 pause → 归位 | 无 |
| 6 | health 永不通过 | `E_HEALTH_TIMEOUT` | 否 | 同上（附 health 配置与探测输出） | 无 |
| 7 | 观察窗崩溃循环（≥2 次退出） | `E_OBSERVE_CRASH_LOOP` | 是 | 默认告警；opt-in 回滚 | 已在新版本；回滚 = 二次切换 |
| 8 | 观察窗窗末 unhealthy | `E_OBSERVE_UNHEALTHY` | 是 | 同上 | 同上 |
| 9 | 单次退出且自愈 | `W_DEPLOY_INSTABILITY` | 是 | 警告通过（不计数升级） | 无 |
| 10 | 观察窗后崩溃 | `E_DEPLOY_POST_WINDOW_UNSTABLE` | 是 | 只告警一次 + 建议 `fleetly rollback`；不计数升级 | 由 Swarm 重启自愈，间歇失败 |
| 11 | 控制面重启 | `E_DEPLOY_INTERRUPTED` | 视评估 | 分类恢复（2.3） | 视现场 |
| 12 | 引擎不可达 | `E_RUNTIME_UNAVAILABLE` | 视现场 | recovery blocked + 退避重试 | 无法保证，critical 告警 |
| 13 | stop-first 失败 | `E_DEPLOY_DOWNTIME_FAILED` | 否 | **强制归位**，停机持续；恢复失败 = 同一码 critical | 停机窗口 = 判定 + 恢复，如实告知 |
| 14 | 首发失败 | `deployment.substrate_halted` | — | scale=0 保留现场 | 应用本就不可用 |
| 15 | 发布中绑定节点 DOWN | — | 否 | 部署 `blocked_waiting`（L2 计时暂停、可 cancel）；节点恢复续跑并重新起算；应用 `blocked` | 停机（唯一合法节点不可用） |
| 16 | 发布中绑定节点 REMOVED | `E_PLACEMENT_NODE_GONE` | 否 | 部署失败；应用保持 `blocked(node_gone)` 等人工 rebind | 停机（数据安全优先） |

### 2.6 stop-first 专表（有卷/固定 host 端口/global）

- 硬 preflight（镜像/约束/secret/端口）**在停机之前**执行，失败零停机。
- 失败 = 强制归位重放（不受 `onUnstable` 影响），停机持续到恢复完成（Spike B 失败矩阵实测：stop-first 失败停机 10–12s 量级）；`downtime_ms` 与 `deployment.downtime_started/ended` 事件如实记录。
- 观察窗策略（`onUnstable`）对 stop-first 无意义（归位是恢复不是策略），不对用户暴露该组合的歧义文案。
- 对外口径：文档与 UI 明示「有卷/固定端口应用不承诺零停机」（既有降级表）。

### 2.7 错误码、事件、审计

- **错误码**（注册表只增不复用，格式 `E_<域>_<条件>`）：`E_COMPOSE_UNSUPPORTED`、`E_COMPOSE_MANAGED_FIELD`、`E_COMPOSE_UNSAFE_STRATEGY`、`E_BUILD_FAILED`、`E_IMAGE_PULL_FAILED`、`E_IMAGE_UNAVAILABLE`、`E_SCHEDULER_PENDING_TIMEOUT`、`E_TASK_START_FAILED`、`E_HEALTH_TIMEOUT`、`E_OBSERVE_CRASH_LOOP`、`E_OBSERVE_UNHEALTHY`、`E_DEPLOY_INTERRUPTED`、`E_DEPLOY_POST_WINDOW_UNSTABLE`、`E_DEPLOY_DOWNTIME_FAILED`、`E_ROLLBACK_FAILED`、`E_ROLLBACK_NO_TARGET`、`E_RUNTIME_UNAVAILABLE`。警告码：`W_DEPLOY_INSTABILITY`、`W_DEPLOY_NO_HEALTHCHECK`、`W_ROLLBACK_IMAGE_RISK`。
- **事件**：`deployment.{queued,release_started,healthy,switched,observe_started,succeeded,failed,warning,cancelled,rollback_started,rollback_finished,rollback_failed,recovery_scheduled,recovery_blocked,substrate_halted,superseded}`、`app.{degraded,instability_detected,recovered}`。
- **审计**：`deployment.create/cancel/rollback`（human/ai_agent）、`deployment.auto_abort/auto_rollback/recovery_retry`（system + reason=错误码）；自动动作必入审计。
- 错误信封统一：`{code, message, phase, deployment_id, suggestion, context{raw/log_tail/exit_code/...}, docs}`；部署失败是资源终态而非 HTTP 错误。

### 2.8 治理参数与 compose 受管字段

**平台治理参数**（v0.1 全部平台默认、文件不可配置；后续以平台 API 开放 per-app 覆盖）：

| 参数 | 默认 | 说明 |
|---|---|---|
| observe | 60s | 观察窗时长 |
| onUnstable | alert | `rollback` 为平台侧 opt-in |
| keepVersions | 5 | 可重放 compose revision 数（v0.1 固定；范围语义随 v0.2 开放 per-app 覆盖时定义） |
| releaseTimeout | 300s | 发布看门狗（含 PENDING/停滞），有效值 ≥ health 预算 |
| stopGrace | 60s | 缺省值；compose `stop_grace_period` 可覆盖 |

**compose 受管字段政策**（校验拒绝而非静默覆盖；违反 → `E_COMPOSE_MANAGED_FIELD`）：
- `deploy.update_config.failure_action` 必须为 `pause`（或省略）
- `deploy.update_config.monitor` 必须省略或 5s
- 更新顺序：`deploy.update_config.order` 照用（有卷服务强制 stop-first；显式 start-first 冲突 → `E_COMPOSE_UNSAFE_STRATEGY`）
- 其余 `deploy.*` 照用（parallelism/delay/restart_policy/resources/replicas/placement 限 `fleetly.*` 标签）

健康门解析：服务 `healthcheck`（未写子字段取平台默认 5s/3s/3/10s）> 无（`health_gate=none` + `W_DEPLOY_NO_HEALTHCHECK`）。

### 2.9 系统性失败处理

- **不做熔断**：多 app 同时失败（registry/节点/构建器故障）通过部署失败率异常的事件/页面告警暴露，人工判断处置。Dokploy 无此机制也达成头部体验；仅当真实事故证明需要时再评估。

## 3. 关键决策及理由

| # | 决策 | 理由 | 被否选项及原因 | 来源 |
|---|---|---|---|---|
| D-REL-1 | `failure-action=pause` 固定，不使用 Swarm 原生回滚 | 自动回滚清空唯一 PreviousSpec、不覆盖 PENDING、首发卡 `UPDATING`；pause 保留旧任务与 spec，恢复动作幂等可审计 | `rollback`（历史槽与 PENDING 缺陷）；`none`（静默带病） | 独立收敛 |
| D-REL-2 | 回滚/归位 = 完整快照重放（单层） | 只回 digest 会把坏 env/health 留在原地；自动与手动共用原语，支持任意 N | Swarm `--rollback`（语义不可控：Spec/PreviousSpec 互换） | 独立收敛 |
| D-REL-3 | 窗口三层分工（2.2） | monitor 从 RUNNING 起算且只判启动期；PENDING 无超时需看门狗；切流后归观察窗 | 放大 monitor（语义失真）；只靠轮询（丢失任务级失败原因） | 独立收敛 |
| D-REL-4 | 失败分流判据 = `first_healthy_at` | 健康前后是「流量是否已切」的物理分界；状态标签有竞态 | 按阶段标签分流（竞态下误判） | 独立收敛 |
| D-REL-5 | 首发失败 scale=0 保留现场 | 停掉崩溃循环且保留诊断；重发零迁移成本 | 保持 crash-loop（噪音）；删 service（丢诊断） | 裁决（A 优于 B） |
| D-REL-6 | **观察窗默认只告警**；rollback 为平台侧 opt-in（v0.1 无文件字段）；stop-first 强制恢复 | **用户裁决**：与 D11「检测开、收敛 opt-in」哲学一致；对外部依赖故障无效回滚会震荡。严格判据（崩溃循环/窗末未恢复）在 opt-in 时生效 | 默认自动回滚（静默改变运行版本，与不静默原则张力） | 用户裁决 |
| D-REL-7 | 未切流归位为同记录 recovery；已切流回滚为新 deployment | 未切流常为零动作、不应污染版本历史；已切流回滚需可观察/可审计/可再回滚 | 全部新 deployment（噪音）；全部同记录（历史不可读） | 裁决（合并） |
| D-REL-8 | stop-first 失败强制归位、`downtime_ms` 诚实累计 | 不回滚=永久宕机，无正当场景；停机必须如实告知 | 尊重默认告警（服务中断时袖手旁观） | 独立收敛 |
| D-REL-9 | 治理参数取当前平台配置、secret 值取当前、非密钥 env 随快照回滚（三层合并结果，见架构 §2.4） | 前瞻设置不随版本回退；密钥回滚是安全事故；非密钥 env 属于 workload，不回退会让「完整快照」名不副实（env 变更必须经部署固化，天然形成可回退点） | 快照含治理参数/密钥（静默回退与安全事故）；env 取当前（回滚半到位） | 独立收敛 |
| D-REL-10 | 首发/回滚失败不再二次自动回滚 | 断链防震荡；恢复失败是 critical 人工介入场景 | 自动重试（叠加失败与掩盖根因） | 独立收敛 |
| D-REL-11 | 系统性失败只告警，不做熔断 | 小团队/小集群爆炸半径有限；Dokploy 无熔断也达成头部体验；机制复杂度不构成 UX（D18） | 熔断状态机（过度设计，无真实事故证据） | 裁决（砍单） |

## 4. 分步实施

- **v0.1**（单节点）：pause 固定、错误码/事件、compose 受管字段校验（`E_COMPOSE_MANAGED_FIELD`）、归一化 compose 快照与最近 5 版回滚、首发 scale=0、控制面重启分类恢复、`W_ROLLBACK_IMAGE_RISK`。
- **v0.2**：多副本水位粗判、MCP 只读查询（`get_deployment`/`list_revisions`/`rollback` 两段式确认）、degraded/不稳定的一等 UI（横幅 + 一键回滚 + 通知）。
- 与交付流水线映射见 §6。

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| start-first 重发布期 LB 端点时机 | ~~开放问题~~ **2026-09-17 Spike B3 已关闭**：带 healthcheck 端点入集晚于 healthy 45–87ms、542 样本 0 失真，「失败=不切流量」成立、对外口径不降级；health_gate=none 的暴露（27 连败/6s 预热）归降级表 | 证据 spike/b/README.md §3；技术预案（路由改平台自管 task 级注册）不再需要 |
| 归位零成本依赖 `IsTaskDirty` 深度相等 | ~~未验证~~ **2026-09-17 Spike B2 已实测验证**（task id 不变；`--force` 必重建→归位禁用） | 见 §2.4；spike/b/README.md §2 |
| 冻结→归位竞态（窗口内旧任务节点 DOWN） | 按失败 spec 重建错误版本任务 | 归位 p95 <2s；故障注入测试；错误任务不会通过 health 接管流量 |
| 观察窗阈值（60s/10s/重启判定）为拍值 | 误报或漏报 | dogfood + 3 类真实应用校准；平台默认可调 |
| v0.1 无 registry，回滚镜像被清理 | 回滚目标不可用 | preflight + `W_ROLLBACK_IMAGE_RISK` + 文档；不自动清理镜像 |
| 恢复动作覆写 `PreviousSpec` | 运维手动 `--rollback` 滚向失败版本 | DR 注记 + CLI 警告 |

## 6. 测试与验收

- **Spike B 扩展**（作为通过标准）：B1 health 失败 → 更新 paused、旧任务不中断；B2 pause 后同内容重放任务零替换；B3 start-period 内任务是否进 LB 端点（最高优先级）；B4 失败矩阵逐条错误码断言；B5 stop-first 强制归位与停机账。
- **nightly**：场景矩阵 1-16 的 E2E（含控制面重启、引擎不可达恢复 blocked→恢复、绑定节点 DOWN/REMOVED 的部署续跑与失败、多副本 pause 后新旧混合归位）；错误码注册表快照；回滚（快照重放）一分钟内完成。
- **release**：上一版本 → 新版本升级 E2E 中的非终态 deployment 恢复。

## 7. 明确不做

- 不使用 Swarm 原生回滚 / `PreviousSpec` 迁移路径
- 观察窗默认不自动回滚；窗口后不自动回滚
- 不自动重试失败发布；不做 dry-run 预启动（假阴性）
- 不版本化 secret 值（回滚不撤销轮换）
- 不承诺「切换期绝对零失败」（以 Spike B3 结论校准口径）

## 8. 来源与验证（独立设计×交叉验证）

- **独立收敛（两份设计分别得出）**：pause 固定；单层快照重放；三层窗口；窗后只告警；stop-first 强制恢复；失败分流按是否切流；数据/迁移/secret 不回滚；治理参数平台管理、substrate 参数不进用户配置；错误码/事件/审计。
- **裁决**：首发 scale=0（A 论证硬）；恢复建模同记录/新 deployment 二分（合并）；PENDING 不做 Swarm 调度约束预检，仅保留平台自身对象预检（绑定节点/镜像/secret/端口；B 更安全，A 自报近似判定风险）；系统性熔断砍掉（D18 对标纪律）。
- **用户裁决**：观察窗默认告警（D-REL-6）。
- **独有并验证后并入**：`IsTaskDirty` 归位零成本（B，待 Spike）；LB 端点早于 health（B，开放问题）；`W_ROLLBACK_IMAGE_RISK`、`downtime_ms` 诚实累计（A）；恢复覆写 PreviousSpec 注记（A）。guarantees 块与系统熔断经 D18 对标纪律砍除。
- **开放问题（禁当承诺）**：~~LB 端点时机~~与~~归位零成本~~已于 2026-09-17 Spike 实测关闭（B3/B2，见架构 §5 与 §2.4）；仍开放：冻结→归位竞态；观察窗阈值校准。
