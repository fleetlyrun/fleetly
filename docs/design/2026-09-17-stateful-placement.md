# edgesets stateful 放置（节点约束）设计

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案 | 2026-09-17 | [平台架构设计](2026-09-17-architecture.md) §2.4/§2.5/§2.6；[Swarm 底座评估](../research/2026-09-17-swarm-substrate-assessment.md) §3/§4/§6；[控制面状态模型](2026-09-17-state-model.md)；来源：独立设计×交叉验证（§8），身份模型为用户裁决 |

## 1. 现状与问题

- Swarm local 卷按节点各自创建，任务被重调度到其他节点会得到**空卷**（数据不跟随、数据风险）；删服务不删卷；bind mount 必须预先存在于目标节点。
- 节点消失（15–16.5s 判定 DOWN）后 manager 会在其他节点重建任务；未加约束的有卷服务因此可能静默产生空卷。**「有卷就不迁移」不成立，必须主动钉住。**
- 草案 spec 没有任何约束/placement 字段；节点身份只有 hostname（可变、可重名）；谁在何时选定节点、节点更换后如何恢复均未定义。
- 已验证能力边界：**manager 无法枚举/删除远端节点的 local 卷**（维护需一次性作业经 docker.sock，Spike V8 验证）。

## 2. 目标设计

### 2.1 概念模型（意图 / 绑定 / 执行）

| 层 | 载体 | 归属 | 变更规则 |
|---|---|---|---|
| 意图 | spec `placement.node`（显示名或节点 ID，可省略） | 用户 | 改 spec |
| 绑定 | `placements` 记录（**平台节点 ID 为锚**） | 平台 | 仅经首次自动选点、显式换点（破坏性确认）、备份恢复迁移、重入 adopt 四类操作 |
| 执行 | 适配器编译为节点 label 约束（`io.edgesets.node-id`） | 适配器 | 随绑定自动下发 |

不变量：**绑定优先于 spec 的缺失**（用户删除 pin 不触发迁移）；有卷应用不存在「无绑定」的合法运行态；绑定节点不可用时**不迁移、不换点**。

### 2.2 节点身份（用户裁决：平台 ID 为锚）

- **领域身份 = 平台节点 ID**（`n_<ULID>`，永不复用），写入节点 label `io.edgesets.node-id`；服务约束引用它。
- **显示名**唯一（默认 hostname slug，可改），仅供人/Agent 读写；spec 可写名或 ID，名→ID 解析失败 → 422 + 候选清单。
- Swarm node ID 仅存在于适配器映射（`runtime_node_refs`），不进入核心契约；**重入 = 新 Swarm 成员 → 显式 adopt**（校验数据后把平台 ID 重新关联），不依赖 hostname/machine-id 自动判定。
- 灾后全新建集群（L2）：平台 ID 保留在 DB/导出物，节点逐台 adopt 重绑；未 adopt 的绑定应用进入 blocked，部署 409，绝不猜测。

### 2.3 spec 与 API

```yaml
placement:
  node: srv-01     # 可选：显示名或 n_<ULID>；省略 = 平台自动选点并持久保持
```

- 粒度 app 级（一个任务挂 app 全部卷，只能落单节点）；不做 process/卷级、不做标签选择器 DSL（硬钉住是唯一语义）。
- 有命名卷/宿主绑定 → **强制钉住**（平台自动，无需用户声明）；无卷应用默认不钉，显式 pin 时出计划警告（失去自动重调度）。
- 校验：`volumes` 非空时 `replicas` 必须为 1（本地卷不能多副本共享）。

| API | 语义 |
|---|---|
| `GET /v1/apps/{app}/placement` | source/node/state/reason/volumes/hints |
| `PUT /v1/apps/{app}/placement` | `{node:"srv-01"\|null, dataAck:"restored"\|"discarded", confirm}`；换点 = 破坏性 |
| `GET/PATCH/DELETE /v1/nodes/*`、`/v1/nodes/{id}/adopt`、`/v1/nodes/{id}/actions{drain\|resume\|remove}` | 节点生命周期 |
| `GET/DELETE /v1/volumes*`、`POST /v1/volumes/{id}/verify` | 卷清单/孤儿/校验 |
| `POST /v1/rebalance {dryRun}`（+ apply） | 仅无状态、手动 |

### 2.4 状态模型增量（SQLite，只加不减）

```sql
placements(app PRIMARY KEY, node_id, source /* platform|spec */, spec_ref,
           state /* ok|blocked|unresolved|conflict */, reason, pinned_at, updated_at, etag)
volumes(id, app, key, kind /* named|bind */, node_id, docker_name, mount_path,
        host_path, status /* active|orphaned|unverified|abandoned */, verified_at,
        UNIQUE(app, key))
-- nodes：平台 ID/显示名/观测字段/工作流字段（见状态模型专项）
-- 适配器私有：runtime_node_refs(platform_id, swarm_node_id), runtime_volume_refs
```

### 2.5 选点算法与部署前哨（确定性、可解释）

```
resolve_placement(app):
  spec pin → 解析（名或 ID）；失败 → E_PLACEMENT_NODE_INVALID/ NOT_FOUND
  已有绑定 → 保持（绑定优先）
  无绑定且无卷 → 不钉（自由调度）
  无绑定且有卷 → 自动选点：
    候选 = ready ∧ schedulable ∧ 架构匹配 ∧ 资源可容纳
    评分 = 数据引力（卷所在节点） > worker 优先 > 已钉应用数少 > 卷数少 > 内存余量 > 名称/ID 序
    无候选 → E_PLACEMENT_NO_ELIGIBLE_NODE（不进入调度等待）

deploy_preflight(app):
  绑定节点非 ready → E_PLACEMENT_NODE_UNAVAILABLE（快速失败，不排队）
  卷数据节点 ≠ 目标节点 → 需 dataAck：
    "restored"  → 校验目标卷 verified（marker 校验），否则 E_PLACEMENT_TARGET_NOT_VERIFIED
    "discarded" → admin + confirm，旧卷置 abandoned（保留记录），事件 volume.discarded
    缺省      → E_VOLUME_NODE_MISMATCH（数据安全前哨，把空卷事故变成 409）
```

### 2.6 生命周期与漂移矩阵

| 漂移 | 判定 | 动作 |
|---|---|---|
| 绑定节点 DOWN / drain | 观测（心跳 15s 量级） | 应用 `blocked`（UI 红条）；任务 PENDING；进行中部署 `blocked_waiting`（无超时、可 cancel、不回滚）；新部署快速失败 |
| 绑定节点移除（remove） | 移除流程 | `blocked(node_gone)`；24h 后升级提醒；恢复仅两条路径（见 2.7） |
| 节点重命名 | API | 默认 409（列出被 spec 按名引用的应用）；`--force` 后相关应用 `unresolved` |
| 节点重入（同机 rejoin） | 显式 adopt | 校验卷数据（marker/verify）后重绑平台 ID；未验证 → `unverified`，部署被拒 |
| 节点身份 label 被删改 | 对账器 | 自动重放 label + 事件（安全不变量，per-app opt-in 的收窄例外） |
| 身份冲突（同 ID 两节点） | 对账器 | 拒绝自动处理；涉及应用拒绝调度 + 409；人工裁决 |
| 卷位置 ≠ 绑定（手工移动/残留） | 对账器 + 前哨 | 部署 409 `E_VOLUME_NODE_MISMATCH`；不自动修数据 |
| 带外出现同名卷 | 扫描 | `volume.duplicate_detected` + 部署拒绝；不自动删除 |
| 无状态应用新增卷 | 部署时 | **钉住当时运行节点**（数据诞生点）；未运行则按选点算法 |
| DR 后无法判定绑定 | 恢复模式 | `unresolved`，要求显式放置；不猜测 |

### 2.7 节点操作与数据恢复路径

- **join**：`docker swarm join` 后平台观测 → 分配平台 ID/显示名 → 写 label。
- **adopt**（重入）：`POST /v1/nodes/{id}/adopt`，确认旧身份无活动节点 → 校验目标节点各卷 marker/人工 verify → 重绑平台 ID → force update 被钉任务。
- **drain**：有钉住应用 → 409（列出清单），`--force` 才允许并显式告知「这些应用将停机且不会迁走」；移除节点同理。
- **换点/迁移**：唯一受支持路径 = **备份恢复迁移**（restic 还原到目标节点 → verify → 改绑定 → 发布；旧卷转孤儿）。空卷路径（`dataAck=discarded`）需 admin + confirm + 审计。
- **卷生命周期**：删除应用默认保留卷（`orphaned`，可发现）；删除仅显式 `--delete-volumes`/孤儿清理且需确认；运行中卷拒绝删除；**远端卷删除/校验经一次性维护作业**（能力边界 + Spike V8；不可行则降级「清空 + 人工回收」并如实显示）。

### 2.8 rebalance

- 仅无状态应用、手动触发、`dryRun` 默认；有状态应用显式排除并列原因（`REBALANCE_STATEFUL_SKIPPED`）。
- 护栏：进行中部署冲突、无 quorum 拒绝、plan etag 防 stale、逐应用串行（maxMoves 默认 10）。
- **best-effort 如实报告**：调度器可能把任务放回原节点；重试上限后标记未达成；节点恢复后倾斜超阈值只发建议事件，不自动执行。

### 2.9 错误码与事件

- 错误码：`E_PLACEMENT_NODE_INVALID`、`E_PLACEMENT_NODE_NOT_FOUND`、`E_PLACEMENT_NODE_UNAVAILABLE`、`E_PLACEMENT_NODE_GONE`、`E_PLACEMENT_NO_ELIGIBLE_NODE`、`E_PLACEMENT_SPEC_OWNED`、`E_PLACEMENT_MOVE_REQUIRES_ACK`、`E_PLACEMENT_TARGET_NOT_VERIFIED`、`E_PLACEMENT_IDENTITY_CONFLICT`、`E_VOLUME_NODE_MISMATCH`、`E_VOLUME_IN_USE`、`E_VOLUME_DATA_UNVERIFIED`、`E_NODE_DRAIN_BLOCKED_PINNED_APPS`、`E_NODE_REMOVE_BLOCKED_PINNED_APPS`、`E_NODE_RENAME_BLOCKED_BY_PLACEMENT`、`E_BIND_PATH_MISSING`。警告：`W_PLACEMENT_STATELESS_PIN`、`W_PLACEMENT_MULTI_REPLICA`、`W_REBALANCE_STATEFUL_SKIPPED`。
- 事件：`placement.{bound,changed,blocked,recovered,unresolved,released}`、`node.{joined,adopted,down,up,removed,renamed,config_restored}`、`volume.{created,detached,orphaned,adopted,verified,discarded,duplicate_detected}`、`rebalance.{planned,applied,completed,failed}`。
- UI/CLI：应用详情「运行位置」卡片（节点、来源、原因、状态）；`edgesets nodes ls/drain/adopt`、`apps placement`、`volumes ls --orphaned`；破坏性操作统一 `--confirm-destructive` + 回显。

### 2.10 v0.1 单节点

- 同一字段、同一代码路径（单节点 = 候选集只有一项）；`placement.node` 可写该节点名；未声明则自动绑定本机。
- 不可用操作（drain/adopt/rebind/rebalance）返回 `E_CAPABILITY_REQUIRES_MULTI_NODE`，不静默成功。
- UI 显示「本机」，不引入节点概念负担；v0.1 → v0.2 零迁移。

## 3. 关键决策及理由

| # | 决策 | 理由 | 被否选项及原因 | 来源 |
|---|---|---|---|---|
| D-PLC-1 | 有卷应用默认自动钉住 + 绑定持久保持 | 数据安全必须是默认行为；用户/AI Agent 不应被迫理解节点概念 | 要求显式 pin（摩擦 + 易漏）；不钉（空卷事故） | 独立收敛 |
| D-PLC-2 | 平台节点 ID 为绑定锚，显示名仅供读写 | 重名机器不得静默接管有状态应用；重建/adopt 可控 | hostname/别名作身份（重名风险）；直接用 Swarm node ID（L2 重建即失效） | **用户裁决**（②B 模型） |
| D-PLC-3 | 卷-节点归属一等状态 + 部署前哨 409 | 约束满足 ≠ 数据在位（adopt/带外操作后）；把静默事故变成显式错误 | 只依赖约束（无数据校验） | 独立收敛 |
| D-PLC-4 | 节点消失不迁移、不换点；blocked 可 cancel；资源型 PENDING 仍走 300s | 数据唯一安全动作是不动；回滚目标同样不可调度（空操作） | 自动换点重建（空卷）；统一超时失败（误导用户）；自动 failback（未验证） | 独立收敛 |
| D-PLC-5 | 数据迁移唯一路径 = 备份恢复；空卷路径需 admin+confirm | 两种真实意图必须在 API 层区分；防「以为恢复了其实没有」 | 单一 `--force`（把丢失与迁移混同）；在线卷迁移（无共享存储，做不到） | 独立收敛 |
| D-PLC-6 | 重入用显式 adopt + 数据校验，不用 machine-id 自动判定 | 换盘/重装可能保留 machine-id，自动路径会误判数据在位（A 自报风险）；显式更安全 | 引擎标签 machine-id 自动再关联（复杂度 + 误判风险；列为后续增强） | 裁决 |
| D-PLC-7 | 选点确定性评分（数据引力优先） | 可解释、可测试、AI Agent 可复算；有卷应用唯一正确解是数据节点 | 随机/轮询（不可解释）；bin-pack（故障半径集中） | 独立收敛 |
| D-PLC-8 | rebalance 仅无状态、手动、best-effort 如实报告 | 无共享存储下有状态搬运=数据事故；不承诺均衡只承诺动作与结果 | 自动 rebalance（重启与震荡）；含 stateful（事故） | 独立收敛 |

## 4. 分步实施

- **v0.1**：`placement.node` 解析与校验、自动绑定（单节点同路径）、卷注册表、前哨 409、label 契约写入、删除应用保留卷。
- **v0.2**：节点生命周期 API（drain/remove/adopt/rename）、孤儿卷管理与维护作业（Spike V8）、多节点选点与漂移矩阵（Spike V6b）、rebalance（dry-run 优先）、备份恢复迁移（restic）。
- **v0.3**：模板目录/受控 stack 中带卷模板的放置指引；machine-id 自动重入评估。

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| 远端卷不可枚举/删除（维护作业未实测） | 卷管理与校验受限 | Spike V8 实测维护作业；不可行则降级「清空 + 人工回收」，UI 如实显示 |
| 带点 label 约束解析、身份 label 被改动 | 钉住失效 | Spike V8 实测两条命名；对账器自动重放 + 事件（安全不变量） |
| adopt 校验依赖平台镜像可达（离线节点） | 重入受阻 | 失败路径 `E_VOLUME_DATA_UNVERIFIED` 停住等人工确认，不静默 |
| 无指标阶段选点质量 | 选到繁忙节点 | 显式 pin 可覆盖；指标落地后校准；不宣传智能调度 |
| 用户期待「跟着应用走」 | 认知落差 | UI 徽标与事件解释「为什么在这」；用户访谈验证 |
| 24h ORPHANED 语义未实测 | 承诺偏差 | 文案保守为「升级提醒」；V6b 长时观察 |

## 6. 测试与验收

- **V6a**（单机/多机基础）：有卷无约束迁移得空卷（复现并文档化）；加绑定后任务钉住且不迁移。
- **V6b**（漂移矩阵）：down→blocked→rejoin→adopt→恢复；remove→gone；rename 阻断；label 被删自动重放；身份冲突拒绝；同名卷检测。
- **V8**（远端能力）：probe/maintenance 作业写读 marker、远端卷删除、label 约束两条命名。
- **V9**（重入 adopt）：全链路（含数据未验证时的拒绝路径）。
- 单元：选点确定性、绑定保持、前哨分支（restored/discarded/缺省）、校验规则。

## 7. 明确不做

- 不做 stateful 自动迁移 / 自动换点 / 自动 failback
- 不做标签/偏好式调度 DSL（只做节点级硬钉住）
- 不做跨节点卷在线迁移（备份恢复是唯一路径）
- v0.2 不做自动 rebalance
- 不做卷数据自动删除；不做未验证数据的自动接管
- 不做自研节点协议（身份机制一律落在节点 label + 显式 adopt）

## 8. 来源与验证（独立设计×交叉验证）

- **独立收敛**：placement 可选+自动；有卷强制/自动钉；label+约束（适配器内）；卷-节点归属一等 + 前哨拦截；数据引力确定性选点；节点消失不迁移；跨点双路径 + 显式 ack；数据不自动删；rebalance 仅无状态手动；单节点同路径；拒绝选择器 DSL。
- **裁决**：绑定模型合并（单记录 + 「绑定优先」规则）；重入显式 adopt（弃 machine-id 自动）；marker 有则校验、无则显式确认；进行中部署 blocked / 新部署快速失败。
- **用户裁决**：平台节点 ID 为锚（D-PLC-2）。
- **独有并验证后并入**：远端卷能力边界与维护作业（B，进 Spike V8）；卷内 marker 校验（B）；24h 升级提醒、bind 路径检查、dataAck 双路径 API 形态（A）。
- **开放问题**：维护作业安全性；PENDING 重评估行为；adopt 在离线网络下的可用性；无指标选点质量。
