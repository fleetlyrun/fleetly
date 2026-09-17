# edgesets 控制面状态模型设计（Swarm 底座）

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案 | 2026-09-17 | [平台架构设计](2026-09-17-architecture.md) §2.3/§2.6/§2.8（应用模型 = Compose 规范）、D18 对标纪律；[Swarm 底座评估](../research/2026-09-17-swarm-substrate-assessment.md)；[放置设计](2026-09-17-stateful-placement.md)；来源：独立设计×交叉验证（§8），机制面经 D18 精简 |

## 1. 现状与问题

- 草案 `nodes` 表曾含「最后心跳」与「资源水位」，但 Swarm 是成员与任务的唯一权威，且**不暴露心跳时间戳**——镜像运行态进 SQLite 会制造第二条真源与陈旧数据。
- 「实际态可从节点 label 重建」表述不精确：真正可反建的是服务/卷 label + 少量身份信息，且有明确边界（不能恢复秘密与历史）。
- 控制面与 swarm raft 是两个备份源，备份时间点必然不同；恢复顺序与「恢复期是否自动收敛」未定义（自动收敛在 DB 较旧时会静默回退部署）。

## 2. 目标设计

### 2.1 分层判据与权威归属

三条判据（顺序裁决）：**单一写者**（平台创造的进 SQLite，Swarm 创造的实时读）、**可重建性**（能重算的只做缓存、禁入决策路径）、**历史性**（过去时事实必须自存）。

| 类别 | 内容 | 权威 | 平台存储 |
|---|---|---|---|
| 期望态 | app/compose 期望态/env 声明/domains/placement/治理策略 | SQLite | apps/revisions/domains/env_vars/placements |
| 平台身份与凭证 | token 哈希、主密钥、settings、备份策略 | SQLite + 密钥文件 | tokens/meta/密钥 |
| 历史与叙事 | deployment/revision/事件/审计/构建日志引用/定时任务运行记录 | SQLite/文件 | deployments/events/audit_log/cron_runs/日志目录 |
| 运行态事实 | 节点/服务/任务/卷/实际镜像 | **Swarm/Engine** | 观测缓存（nodes 等），带 `observed_at/stale` |
| 证书材料 | ACME 账户与证书链（`acme.json`） | 文件（Traefik 卷） | 独立备份（避免重签触发配额） |
| 构建产物 | 镜像、日志、Railpack plan | 镜像存储/registry；平台文件 | 文件 + 引用 |

### 2.2 派生缓存与读契约

- `nodes` 表定位 = **观测缓存**（节点状态历史在 `events`，见 §2.9；平台不建 drain 工作流，用 `docker node` 原生），标注「派生自 Docker/Swarm，禁止用于决策」。
- 刷新：事件驱动（node/service/task 变更 1s 内）+ **统一 30s 全量 resync**；启动先全量同步再服务；底座不可达 → 指数退避，全部行置 `stale=true`。
- **不承诺「最后心跳」**：字段用 `last_seen_at`（平台观测语义），文案与 API 不得称「心跳」。
- 水位：`reserved`（服务 reservation 求和，永远可得）；`used` 由 v0.2 Metrics 端口提供，缺失时显式 `null` + `usage_source:"unavailable"`，不显示陈旧数字。
- 读契约（极简）：观测数据带 `observed_at/stale` 两个字段；**写操作先直读底座**，冲突 → `E_STATE_VERSION_CONFLICT`（409）。

### 2.3 节点身份与重建

- **领域身份 = 平台节点 ID**（`n_<ULID>`，用户裁决，见放置设计 §2.2）；写入节点 label `edgesets.node-id`；显示名=Swarm hostname。
- Swarm node ID 仅存适配器映射 `runtime_node_refs(platform_id, swarm_node_id)`（**用途：身份 label 被删改后对账器自动重放的依据**；映射本身可从 label 反建）；换机/重建后走**人工 rebind**（放置设计 §2.2）。
- 平台不制造节点健康语义：state/availability 逐字镜像 Swarm，外加平台观测时间。

### 2.4 对象标记契约（最小集）

命名空间 `edgesets.*`（保留前缀，用户占用 → 422 `E_LABEL_RESERVED`）；写者唯一 = 适配器 Marker 端口；密钥/payload 永不入 label；保留未知 `edgesets.*` 键不改写。

| 对象 | 键 | 用途 |
|---|---|---|
| Service | `managed=true`、`app`、`process`、`deployment`、`cron`（job 的 schedule 名） | 归属判定、孤儿检测、删除保护、堆栈对账辅助、cron 运行归属 |
| Container | `app` | 人工排障时识别归属（不参与决策） |
| Node | `node-id`（平台 ID） | 放置锚与重绑 |
| Volume | 无 label，使用命名约定 `edgesets-<app>-<key>-<appid8>`（约束来源：卷由服务 spec 在各节点惰性创建，label 传递能力待 Spike 验证——`VolumeOptions.Labels` 生效则可收敛到 label 体系） | 卷归属与防代际静默复用 |

- 平台约定 label（compose 原生字段承载）：`edgesets.domains`（路由域名）、`edgesets.placement.node`（放置意图）〔v0.1 契约〕；`edgesets.cron` / `edgesets.cron.timezone`（定时任务，v0.2 契约；带该 label 的服务不按长驻部署）。
- ~~锚点文档 / schema 版本化 / 溢写~~：经 D18 砍除（无硬承诺需要；DB + label 足够）。

### 2.5 漂移判定

- `desired-hash = sha256(canonical_json(期望态规范化))`；期望态 = compose 受控子集字段（镜像/命令/env〔以 `key:sha256(value)` 参与〕/mounts/replicas/labels/约束/resources/health/restart/stop/网络）+ 平台覆盖层（镜像 digest、secret 引用、路由与节点绑定）。
- 判定只用 hash；字段级深比仅用于 diff 报告，env 只报键名与 `key:hash`。
- 外部操作（含手动 `docker service update --rollback`）→ 识别为漂移 → 事件 `reconcile.drift_detected`；收敛 per-app opt-in（D11）。

### 2.6 对账器、孤儿保护与恢复观察

- 安全不对称：**「DB 无记录」不是「对象该删」的证据**。孤儿（`managed=true` 但 DB 无 app）→ 登记 `orphans`，**永不自动删除**；处理走人工（重新 init 接管，或用户在底层自行清理）；平台不建 adopt/purge 流程。
- **恢复观察模式**：恢复流程完成后控制面进入只读观察（一个开关 + UI 横幅）：只观测与列差异（复用 drift 输出），不自动收敛；人工处理后显式退出。
- 删除 = tombstone-first（`deleting → deleted` + 保留期），恢复时不复活已删应用。

### 2.7 备份、等序原则与恢复

**等序不变量：SQLite 允许比 raft 新，绝不允许比 raft 旧**（DB 新 → 收敛补齐；DB 旧 → 孤儿待决）。

- 热备：SQLite 一致快照（`VACUUM INTO`）+ sha256 回读校验；**每次成功部署后 + 每日**；不碰 raft。
- 冷备：host 侧 helper，停 Engine → tar `/var/lib/docker/swarm`（含 raft 与 autolock key）→ DB 快照 → `acme.json` → 校验上传；**平台升级前强制** + 手动。
- 密钥（主密钥）独立路径、不同介质保存；备份失败红色告警；`state_backups` 台账记录 `verify_status`。
- **恢复顺序（固定）**：① 停控制面与 Engine ② 校验备份集（校验和 + 密钥指纹，不匹配 → `E_BACKUP_KEY_MISSING`，拒绝半恢复）③ raft 回填 → Engine 启动（必要时 `--force-new-cluster`）④ SQLite + acme 回填 ⑤ 启动控制面 → **只读观察** ⑥ 人工按差异清单处理 → 退出观察 ⑦ 事件 `restore.completed`。
- **恢复阶梯 L1/L2**：L1 = raft+DB（全保真，应用不中断）；L2 = 仅 DB（新集群 + 人工重绑，运行态重建）。**L3 场景（仅容器存活）runbook 化、不建机制**。
- **明确边界**：单节点（v0.1）整机磁盘丢失 = 应用与数据同时丢失，控制面 DR 不覆盖（写入用户文档）。

### 2.8 导出

- `edgesets export --out bundle.tar.gz`：compose 文件 + SQLite 一致性导出 + 卷 tar（可选）+ README（含恢复指引与不承诺清单）。
- **不含**：token、主密钥、证书私钥。**不建格式合同与自动导入**（D18 砍单）；跨平台搬家 = 按 README 人工恢复。

### 2.9 审计与事件自存

- `audit_log`（1 年）：actor（human/ai_agent/system + token）、action、target、result、error_code、request_id、diff 摘要；**破坏性/管理操作与业务写同事务，审计失败即操作失败（fail-closed）**；系统自动动作必入审计。
- `events`（30 天）：`seq` 单调（SSE 游标，Outbox 模式与业务写同事务）；`since_seq` 早于保留期 → 410 `E_EVENT_CURSOR_EXPIRED` + `oldest_seq`（显式断档）。
- 底座事件流只作缓存失效信号，不作产品事件来源；secret 值禁止进入事件/审计/日志。

### 2.10 应用状态机（派生视图）

- `app.state ∈ {running, degraded, blocked, down}`，由部署记录与观测派生，优先级裁决：`down > blocked > degraded > running`。
  - `down`：无有效版本或首发失败 `scale=0`（没有任何期望实例）
  - `blocked`：平台侧无合法动作可执行——绑定节点不可用/已移除（`placement.state ∈ {blocked, unresolved}`）
  - `degraded`：运行中但有不合格判定——观察窗失败（deployment `verdict=unstable`）、窗后不稳定、`W_DEPLOY_INSTABILITY`
  - `running`：以上皆否
- `unstable` 仅为 **deployment verdict**，不进入 app 状态词表；`placement.state` 与最近 deployment 的 verdict 作为正交细节随应用详情返回。
- 事件映射：进入 degraded → `app.degraded`；消除 → `app.recovered`；blocked 由 `placement.blocked` / `placement.recovered` 驱动。

## 3. 关键决策及理由

| # | 决策 | 理由 | 被否选项及原因 | 来源 |
|---|---|---|---|---|
| D-STM-1 | 三层状态（权威/派生缓存/实时直读）+ 三判据 | 双状态源无法消灭只能明确属主；缓存进决策路径是误操作唯一通路 | 全量镜像运行态入权威（双写者）；不落缓存（打爆底座 API） | 独立收敛 |
| D-STM-2 | `nodes` 降级为观测缓存 + 工作流载体；不承诺心跳时间戳 | Swarm 是成员唯一权威且不暴露心跳 | 保留为权威（争真源）；删表（历史/降级读无家可归） | 独立收敛 |
| D-STM-3 | 水位 reserved/used 双概念，缺失显式 null | 不静默降级；v0.1 也能回答「预留是否超卖」 | 容量冒充用量（谎报） | 独立收敛 |
| D-STM-4 | label 最小集（managed/app/process/deployment + 节点身份），无 schema 仪式/锚点 | ≤10 台无跨版本对象共存场景；锚点反建依赖 L3（已砍） | 完整 label 契约 + 版本化 + 锚点溢写（过度设计，D18） | 裁决（D18 砍单） |
| D-STM-5 | desired-hash 判定漂移，深比仅报告 | 廉价、稳定、不泄露 env | 逐字段深比判定；时间戳比较 | 独立收敛 |
| D-STM-6 | 孤儿只登记不删除；恢复后只读观察 | DB 滞后于底座是 DR 的必然状态；恢复期自动收敛会静默回退部署 | 按 DB 反向清理；恢复即收敛 | 独立收敛 |
| D-STM-7 | 备份等序 + 固定恢复顺序 + 密钥独立 | 「DB ≥ raft」使两种恢复方向都有安全路径 | 运行中 tar raft（撕裂）；恢复即自动收敛 | 独立收敛 |
| D-STM-8 | 领域节点身份 = 平台 ID（用户裁决） | 抗重名/重建，数据安全优先 | hostname 作身份；Swarm node ID 作领域身份 | **用户裁决** |
| D-STM-9 | 导出为简单 tar（不是格式合同），不承诺免重建 | 小团队的导出是「带走数据」承诺，不是迁移流水线 | edgesets.export/v1 合同 + import 自动化（过度设计，D18） | 裁决（D18 砍单） |
| D-STM-10 | 审计 fail-closed + 事件游标显式断档 | 自动收敛无审计即黑盒改配置；静默跳号不可诊断 | 读操作写审计（噪音）；事件仅内存广播（断号） | 独立收敛 |
| D-STM-11 | 不做 recovery plan 资源与两阶段 apply；只读观察开关替代 | 一个 flag 覆盖「恢复期禁收敛」，plan 状态机是仪式 | recovery_plans 表 + findings + apply 两段式（过度设计，D18） | 裁决（D18 砍单） |

## 4. 分步实施

- **v0.1**（单节点）：三层原则、观测缓存（单机同路径）、最小 label 集、`revisions`、tombstone、`state_backups` 台账（热备 + sha256 回读校验 + 失败红色告警）、审计 fail-closed、事件 seq/游标。
- **v0.2**：完整 resync 刷新器、`orphans` 表、`cron_runs` 运行记录、恢复演练（L1/L2）、导出 tar、raft 冷备 helper、多节点 nodes 列表接入。
- 交付流水线映射：V5/V5b/V6a/V6b 与状态回归项（见 §6）。

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| 缓存陈旧被误当事实 | 用户/Agent 误判 | `observed_at/stale` + 写前直读；决策路径禁读缓存 |
| 恢复后孤儿容器命运（raft 回退到旧点） | 残留任务/端口冲突 | V5b 验证；若被 GC 则提高热备频率并明确口径 |
| 冷备维护窗口实际不执行 | 灾备假可用 | 升级前强制冷备（天然有窗口）；冷备占比监控 |
| env 明文随 raft 备份 | 「密钥独立于备份」边界击穿 | 如实文档化 + 备份介质加密；v0.2 评估 secrets/tmpfs |
| 只读观察开关被忽略/忘记退出 | 漂移不收敛 | UI 横幅 + 事件提醒；API 拒绝收敛类写操作直到退出 |

## 6. 测试与验收

- **V5b**：raft 回退后孤儿容器命运（0/5/30min 观察）。
- **V6a/V6b**：见[放置设计](2026-09-17-stateful-placement.md) §6。
- **状态回归**：写前直读冲突（`E_STATE_VERSION_CONFLICT`）、孤儿只登记不删除、备份顺序与密钥指纹校验、导出 tar 不含 token/密钥、审计 fail-closed、事件游标 410、只读观察开关。
- 单元/契约：`desired-hash` 稳定性、tombstone 不复活。

## 7. 明确不做

- 不做「运行态事实写为权威」；不做对未知对象的自动删除
- 不自研节点心跳/节点状态机；不承诺 `last heartbeat` 时间戳
- 不做 recovery plan 资源、两阶段 apply、恢复模式启发式
- 不做 DR L3 机制（runbook 化）
- 不做导出格式合同与自动导入
- 不做锚点文档/label schema 版本化仪式
- 不把容器 label 当第二数据库

## 8. 来源与验证（独立设计×交叉验证 + D18 精简）

- **独立收敛（两份几乎同构）**：三层状态；`nodes` 降级 + 不承诺心跳；水位双概念；hash 漂移判定；孤儿保护；等序原则 + 恢复顺序 + 恢复禁自动收敛；审计/事件自存 + fail-closed；v0.1 统一起步。
- **裁决**：恢复顺序采纳「raft → DB → 启动控制面 → 人工处理」（避免运行中换 DB）；热备频率简化为「部署后 + 每日」；D18 砍除恢复计划资源/L3/导出合同/锚点与 label 仪式/读契约协商面。
- **用户裁决**：平台节点 ID 为领域身份（D-STM-8）。
- **独有但经 D18 砍除**：container label 身份四元组作为 L3 第三锚点、离线清单采集、导出格式合同、新鲜度协商（freshness/fallback/resync_epoch）——记录于此，真实需求出现时再评估。
- **开放问题**：raft 回退后孤儿容器命运（V5b）；`VACUUM INTO` 并发正确性。
