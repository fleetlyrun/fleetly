# edgesets 控制面状态模型设计（Swarm 底座）

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案 | 2026-09-17 | [平台架构设计](2026-09-17-architecture.md) §2.3/§2.6/§2.8（应用模型 = Compose 规范）；[Swarm 底座评估](../research/2026-09-17-swarm-substrate-assessment.md)；[放置设计](2026-09-17-stateful-placement.md)；来源：独立设计×交叉验证（§8） |

## 1. 现状与问题

- 草案 `nodes` 表含「最后心跳」与「资源水位」，但 Swarm 是成员与任务的唯一权威，且**不暴露心跳时间戳**——镜像运行态进 SQLite 会制造第二条真源与陈旧数据。
- 「实际态可从节点 label 重建」表述不精确：节点 label 只承载身份/别名；真正可反建的是**服务/容器/卷 label** + 锚点文档，且有明确边界（不能恢复秘密与历史）。
- 控制面与 swarm raft 是两个备份源，备份时间点必然不同；恢复顺序与「恢复期是否自动收敛」未定义（自动收敛在 DB 较旧时会静默回退部署）。
- 缓存数据无新鲜度契约时，用户与 AI Agent 会把观测快照当实时事实。

## 2. 目标设计

### 2.1 分层判据与权威归属

三条判据（顺序裁决）：

1. **单一写者**：谁创造谁持有——平台创造的（意图/历史/凭证）进 SQLite；Swarm 创造的（成员/任务/服务定义）不落为权威。
2. **可重建性**：能由权威源重算的只允许以观测缓存存在，且**不得进入决策路径**（写判断先实时直读）。
3. **历史性**：过去时事实（deployment/event/audit/revision）任何当前态源都无法恢复 → 必须自存。

| 类别 | 内容 | 权威 | 平台存储 | 可重建 |
|---|---|---|---|---|
| 期望态 | app/compose 期望态/env 声明/domains/placement/治理策略 | SQLite | apps/revisions/domains/env_vars/placements | 仓库 compose 文件 + 锚点（repo/ref/spec_digest） |
| 平台身份与凭证 | token 哈希、主密钥、settings、备份策略 | SQLite + 密钥文件 | tokens/meta/密钥 | 不可（重发/重配，显式告知） |
| 历史与叙事 | deployment/revision/事件/审计/构建日志引用 | SQLite/文件 | deployments/events/audit_log/日志目录 | 不可 |
| 运行态事实 | 节点/服务/任务/卷/实际镜像 | **Swarm/Engine** | 观测缓存（nodes 等），带 `observed_at/stale` | 重读即可 |
| 平台写入的真实态 | 服务/容器/节点/卷 label + 锚点文件 | 底座对象自身 + 锚点 | 适配器写 label；锚点目录 | 用于反建映射（2.4） |
| 证书材料 | ACME 账户与证书链（`acme.json`） | 文件（Traefik 卷） | 独立备份（避免重签触发配额） | 丢失需重签 |
| 构建产物 | 镜像、日志、Railpack plan | 镜像存储/registry；平台文件 | 文件 + 引用 | 镜像可重建；日志/plan 不可 |

禁止：把运行态快照当权威；对未登记对象做自动删除（2.6）。

### 2.2 派生缓存与读契约

- `nodes` 表定位 = **观测缓存 + 平台工作流载体**（显示名、drain 意图、历史锚点），表注释标注「派生自 Docker/Swarm，禁止用于决策」。
- 刷新：事件驱动（node/service/task 变更 1s 内）+ 周期全量 resync（nodes 30s / services 30s / volumes 5min）；启动先全量同步再服务；底座不可达 → 指数退避，全部行置 `stale=true`。
- **不承诺「最后心跳」**：Swarm 不暴露心跳时间戳，字段用 `last_seen_at/status_changed_at`（平台观测语义），文案与 API 不得称「心跳」。
- 水位双概念（禁止混算）：`reserved`（服务 reservation 求和，永远可得）/ `used`（v0.2 Metrics 端口；v0.1 无 → `used:null` + `usage_source:"unavailable"` + `E_METRICS_UNAVAILABLE` 警告）；不显示陈旧数字。
- 读契约：列表/详情带 `meta{source: desired|cached|live, observed_at, stale, resync_epoch, warnings[], partial}`；`freshness=live` 失败返回 504，**不得静默回退缓存**（显式 `fallback=stale` 才降级）。
- 写前直读：所有写操作先直读底座，以对象版本作乐观令牌；冲突 → `E_STATE_VERSION_CONFLICT`（409，携带当前版本）。

### 2.3 节点身份与重建

- **领域身份 = 平台节点 ID**（`n_<ULID>`，用户裁决，见放置设计 §2.2）；写入节点 label `edgesets.node-id`；显示名唯一、可改，写入 label `edgesets.node-name`。
- Swarm node ID 仅存适配器映射 `runtime_node_refs(platform_id, swarm_node_id, synced_at)`；L2（全新建集群）后新 ID 通过显式 adopt 重绑。
- hostname 仅展示；同名节点不自动合并，歧义 → 警告或人工裁决。
- 平台不制造节点健康语义：state/availability 逐字镜像 Swarm，外加平台观测时间。

### 2.4 对象标记契约（label schema v1）

命名空间 `edgesets.*`（保留前缀，用户占用 → 422 `E_LABEL_RESERVED`）；写者唯一 = 适配器 Marker 端口；密钥/payload 永不入 label。

| 对象 | 键 | 用途 |
|---|---|---|
| Service | `schema=1`、`managed=true`、`role`（app/control-plane/ingress/build）、`app-id`、`app`、`process`、`deployment`、`desired-hash`、`anchor\|anchor-ref` | 归属、漂移比对、DR 反建、删除保护 |
| Container | `schema`、`app-id`、`app`、`process` | **身份四元组**：随节点磁盘存活，是 raft 丢失后唯一在线归属证据（不做可变字段，避免每次部署 churn） |
| Node | `schema`、`node-id`（平台 ID）、`node-name` | 放置锚与重建 |
| Volume | `schema`、`app-id`、`volume`（best-effort） | 卷归属；主机制是命名约定 `edgesets-<app>-<key>-<appid8>`（防代际静默复用） |

- 平台约定 label（v0.1 契约，compose 原生字段承载）：`edgesets.domains`（路由域名）、`edgesets.port`（路由端口）、`edgesets.placement.node`（放置意图）——与内部 label 同属 `edgesets.*` 命名空间、同样版本化。
- 锚点文档：紧凑 JSON（app/repo/ref/spec_digest/deployment/image/processes/volumes/domains/env_keys/created_by）≤2KB 存 label；溢出 → `/var/lib/edgesets/anchors/<app>.json` + label 存 `anchor-ref=sha256`。
- 版本化：读到 `schema>1` → 只读（409 `E_OBJECT_SCHEMA_NEWER`，提示升级）；保留未知 `edgesets.*` 键；契约只增不改语义，快照测试。

### 2.5 漂移判定

- `desired-hash = sha256(canonical_json(期望态规范化))`；期望态 = compose 受控子集字段（镜像/命令/env〔以 `key:sha256(value)` 参与〕/mounts/replicas/labels/约束/resources/health/restart/stop/网络）+ 平台覆盖层（镜像 digest、secret 引用、路由与节点绑定）。
- 判定只用 hash；字段级深比仅用于 diff 报告，env 只报键名与 `key:hash`。
- 外部操作（含手动 `docker service update --rollback`）→ 识别为漂移 → 事件 `reconcile.drift_detected`；收敛 per-app opt-in（D11）。

### 2.6 对账器、孤儿保护与恢复模式

- 安全不对称：**「DB 无记录」不是「对象该删」的证据**。孤儿（`managed=true` 但 DB 无 app）→ 登记 `orphans`，**永不自动删除**；adopt（按锚点重建记录为 pending_redeploy）或 purge（admin+confirm+审计）显式处理。
- **恢复模式**：启动检测到恢复标记或「DB app 数 < 已登记服务数」→ 进入 recovery：只观测与登记，暂停期望态收敛；输出 `GET /v1/recovery/plan`（dry-run）→ 人工 adopt/apply 后退出。
- 删除 = tombstone-first（`deleting → deleted` + 保留期），恢复时不复活已删应用（防 label 反建复活）。

### 2.7 备份、等序原则与恢复阶梯

**等序不变量：SQLite 允许比 raft 新，绝不允许比 raft 旧**（DB 新 → 收敛补齐；DB 旧 → 孤儿待决）。

- 热备：SQLite 一致快照（`VACUUM INTO`/backup API）+ sha256 回读校验，每次成功部署后 + 每 6h；**不碰 raft**。
- 冷备：host 侧 helper，停 Engine → tar `/var/lib/docker/swarm`（含 raft 与 autolock key）→ DB 快照 → `acme.json` + anchors → 校验上传（每周 + **平台升级前强制**）；manifest 记 `state_seq/created_at/校验和`。
- 密钥（主密钥）独立路径、不同介质保存；备份失败红色告警；备份台账 `state_backups` 记录 `verify_status`。
- **恢复顺序（固定）**：① 停控制面与 Engine ② 校验备份集（校验和 + 密钥指纹，不匹配 → `E_BACKUP_KEY_MISSING`，拒绝半恢复）③ raft 回填 → Engine 启动（必要时 `--force-new-cluster`）④ SQLite + acme 回填 ⑤ 启动控制面 → recovery 模式 → plan ⑥ 人工 apply/purge/adopt ⑦ 事件 `restore.completed`。
- **恢复阶梯**：L1 raft+DB（全保真，应用不中断）；L2 仅 DB（新集群 + 逐台 adopt，运行态重建）；L3 仅容器清单（离线采集：在每台节点执行 `docker ps --filter label=edgesets.app-id`，`edgesets recover import` 汇总——v0.2 不引入节点侧 agent，有损、需人工确认）。
- **明确边界**：单节点（v0.1）整机磁盘丢失 = 应用与数据同时丢失，控制面 DR 不覆盖（写入用户文档）。

### 2.8 导出 / 导入合同

- `edgesets.export/v1`：语义 JSON（apps/revisions/env 密文/domains/deployments/images）+ manifest（`format_version/bundle_id/逐文件 sha256/excluded[] 含原因`）+ README（人读恢复指引与不承诺清单）；异步任务 + 保留期。
- **不含**：token（仅元数据参考）、主密钥、证书私钥（`--include-acme` 显式危险开关）；密文可导（无主密钥不可解，无害）；`--include-plaintext-secrets` 需 admin + 交互确认 + 审计。
- 导入：保留 app_id，env 用目标平台主密钥重加密；名称冲突 409（不自动改名）；镜像需可达或重建；**不承诺免重建**（备份=原地恢复，导出=搬家）。

### 2.9 审计与事件自存

- `audit_log`（1 年）：actor（human/ai_agent/system + token）、action、target、result、error_code、request_id、diff 摘要；**破坏性/管理操作与业务写同事务，审计失败即操作失败（fail-closed）**；系统自动动作（对账收敛/自动绑定/恢复 apply）必入审计。
- `events`（30 天）：`seq` 单调（SSE 游标），Outbox 模式（与业务写同事务）；`since_seq` 早于保留期 → 410 `E_EVENT_CURSOR_EXPIRED` + `oldest_seq`（显式断档）。
- 底座事件流只作缓存失效信号，不作产品事件来源（无 actor、保留不可控）；secret 值禁止进入事件/审计/日志（只允许键名与 `key:hash`）。

## 3. 关键决策及理由

| # | 决策 | 理由 | 被否选项及原因 | 来源 |
|---|---|---|---|---|
| D-STM-1 | 三层状态（权威/派生缓存/实时直读）+ 三判据 | 双状态源无法消灭只能明确属主；缓存进决策路径是陈旧数据误操作的唯一通路 | 全量镜像运行态入权威（双写者）；不落缓存（打爆底座 API、无降级读） | 独立收敛 |
| D-STM-2 | `nodes` 降级为观测缓存 + 工作流载体；不承诺心跳时间戳 | Swarm 是成员唯一权威；不暴露心跳，伪造字段是信息黑盒的另一种形式 | 保留为权威（争真源）；删表（别名/历史/降级读无家可归） | 独立收敛 |
| D-STM-3 | 水位双概念 reserved/used，缺失显式 null | 不静默降级；v0.1 无指标栈也能回答「预留是否超卖」 | 容量冒充用量（谎报） | 独立收敛 |
| D-STM-4 | 标签双层（service 承载元数据 / container 承载身份四元组） | service label 随 raft 存亡；container label 随节点磁盘存活（raft 丢失后唯一在线证据） | 只用 service label（raft 丢即全盲）；container 承载可变字段（churn + 不稳定身份） | 独立收敛 |
| D-STM-5 | desired-hash 判定漂移，深比仅报告 | 廉价、稳定、不泄露 env；Swarm 自动写入字段的深比会永久误报 | 逐字段深比判定；时间戳比较 | 独立收敛 |
| D-STM-6 | 孤儿只登记不自动删除；恢复期禁止自动收敛 | DB 滞后于底座是 DR 的必然状态；自动删除是数据丢失级事故；恢复期收敛会静默回退部署 | 按 DB 反向清理；恢复即收敛 | 独立收敛 |
| D-STM-7 | 备份等序原则 + 固定恢复顺序 + 密钥独立 | 「DB ≥ raft」使两种恢复方向都有安全路径；先 raft 才有可观察底座 | 运行中 tar raft（撕裂）；先 DB 后 raft（系统性孤儿）；恢复即自动收敛 | 独立收敛 |
| D-STM-8 | 领域节点身份 = 平台 ID（用户裁决），Swarm node ID 仅适配器映射 | 抗重名/重建，数据安全优先；映射细节不进核心契约 | hostname 作身份；Swarm node ID 作领域身份（L2 失效） | **用户裁决** |
| D-STM-9 | 导出为语义 JSON 合同（非 SQLite 直拷），不承诺免重建 | DB schema 是私有实现；一包含密钥=一包全泄 | 直拷 SQLite（版本耦合）；含主密钥（违反基线） | 独立收敛 |
| D-STM-10 | 审计 fail-closed + 事件游标显式断档 | 自动收敛若无审计即为黑盒改配置；静默跳号是不可诊断 | 读操作写审计（噪音）；事件仅内存广播（断号） | 独立收敛 |

## 4. 分步实施

- **v0.1**（单节点）：三层原则与监控字段、`meta` 读契约、label 契约（service + container，schema=1）、`nodes` 观测缓存（单机同路径）、`app_revisions`、tombstone、备份 manifest + 回读校验、审计 fail-closed、事件 seq/游标。
- **v0.2**：完整 resync 刷新器、`orphans`/`recovery_plans`/`state_backups` 表、恢复阶梯 L1/L2/L3 与演练、导出导入 v1、raft 冷备 helper、placement/nodes API 接入。
- 交付流水线映射：V5/V5b/V6/V8/V9 与新增 V8a-V8f（见 §6）。

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| 容器 label 在 raft 丢失后的存活性未实测（L3 根基） | 反建证据失效 | Spike C：停 manager 后 worker 上 `docker inspect` 断言；失败则 L3 降级为「仅本机容器 + 人工」 |
| `Node.UpdatedAt` 语义未验证 | 状态时刻不可信 | Spike C 观察；不可用则只用平台 `observed_at` |
| label 体积上限未文档化 | 锚点写失败 | 自设 2KB/8KB 上限 + `anchor-ref` 溢写；Spike A 极端应用验证 |
| 缓存陈旧被误当事实 | 用户/Agent 误判 | `meta` 信封 + stale 标记 + freshness 契约 + E2E 断言 |
| 恢复后孤儿容器命运（raft 回退到旧点） | 残留任务/端口冲突 | V5b 验证；若被 GC 则提高热备频率并明确口径 |
| 冷备维护窗口实际不执行 | 灾备假可用 | 升级前强制冷备（天然有窗口）；冷备占比监控 |
| env 明文随 raft 备份 | 「密钥独立于备份」边界击穿 | 如实文档化；备份介质加密；v0.2 评估 secrets/tmpfs |
| 恢复模式启发式误触发 | 合法手工操作被阻断 | 显式覆盖开关 + 场景化测试 |

## 6. 测试与验收

- **V5b**：raft 回退后孤儿容器命运（0/5/30min 观察）。
- **V8a**：删 DB 后从 raft + label + 锚点重建，对照边界表逐行核对。
- **V8b**：孤儿保护（手工 create 带 label 服务 / 直接 rm → 只登记不删；adopt/purge 语义）。
- **V8c**：备份顺序与恢复流程（热备 DB≥raft；冷备；密钥指纹不匹配必须拒绝）。
- **V8d**：缓存语义（live 失败不外退、stale、resync_epoch、`E_STATE_VERSION_CONFLICT`）。
- **V8e**：导出导入（env 重加密、token 不迁移、冲突码、manifest 校验）。
- **V8f**：审计 fail-closed + 事件游标 410。
- 单元/契约：label codec 快照、`desired-hash` 稳定性、tombstone 不复活。

## 7. 明确不做

- 不做「运行态事实写为权威」；不做对未知对象的自动删除
- 不自研节点心跳/节点状态机；不承诺 `last heartbeat` 时间戳
- v0.2 不引入节点侧 inventory agent
- 不把容器 label 当第二数据库（只承载身份四元组）
- 不承诺导出免重建；不承诺单节点整机丢失的应用与数据恢复

## 8. 来源与验证（独立设计×交叉验证）

- **独立收敛（两份几乎同构）**：三层状态；`nodes` 降级 + 不承诺心跳；水位双概念；hash 漂移判定；孤儿保护；等序原则 + 恢复顺序 + 恢复禁自动收敛；导出语义合同；审计/事件自存 + fail-closed；`meta` 读契约 + live 不回退；v0.1 统一起步。
- **裁决**：恢复顺序采纳「raft → DB → 启动控制面 → plan → 人工」（避免运行中换 DB）；`nodes` 保留表名并标注字段分类；热备频率「每次成功部署后 + 周期」；冷备现实性进风险表 + 升级前强制。
- **用户裁决**：平台节点 ID 为领域身份（D-STM-8，覆盖两份设计原「别名/底座 ID」取向）。
- **独有并验证后并入**：container label 身份四元组、L3 离线采集、「单节点整机丢失不保」（B）；锚点溢写、水位契约、V8a-f 清单（A）。
- **开放问题**：container label 存活性；`Node.UpdatedAt`；label 体积；VACUUM INTO 并发正确性；etag 冲突率。
