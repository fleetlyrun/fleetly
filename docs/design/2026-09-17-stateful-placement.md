# edgesets stateful 放置（节点约束）设计

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案 | 2026-09-17 | [平台架构设计](2026-09-17-architecture.md) §2.4/§2.5/§2.6（应用模型 = Compose 规范）、D18 对标纪律；[Swarm 底座评估](../research/2026-09-17-swarm-substrate-assessment.md) §3/§4/§6；[控制面状态模型](2026-09-17-state-model.md)；来源：独立设计×交叉验证（§8），身份模型为用户裁决，机制面经 D18 精简；画像复核轮（2026-09-17）：drain 维护语义入 §2.6 与 V6b（2 台为生产基线）；S3 卷被否方案落档（§5 风险行 + §7 明确不做）；2026-09-17 审核裁决轮：§7 增执行中继例外条款（架构 D19） |

## 1. 现状与问题

- Swarm local 卷按节点各自创建，任务被重调度到其他节点会得到**空卷**（数据不跟随、数据风险）；删服务不删卷；bind mount 必须预先存在于目标节点。
- 节点消失（15–16.5s 判定 DOWN）后 manager 会在其他节点重建任务；未加约束的有卷服务因此可能静默产生空卷。**「有卷就不迁移」不成立，必须主动钉住。**
- 自研 spec 已废止（应用模型 = Compose 规范，D14）；compose 原生没有平台放置语义——放置意图由 label `edgesets.placement.node` 承载；节点身份只有 hostname（可变、可重名）。
- 已验证能力边界：**manager 无法枚举/删除远端节点的 local 卷**——卷删除/校验由用户按文档在节点上执行，平台不建维护作业。

## 2. 目标设计

### 2.1 概念模型（意图 / 绑定 / 执行）

| 层 | 载体 | 归属 | 变更规则 |
|---|---|---|---|
| 意图 | 服务 label `edgesets.placement.node`（显示名或节点 ID，可省略） | 用户 | 改 compose 文件 |
| 绑定 | `placements` 记录（**平台节点 ID 为锚**） | 平台 | 仅经首次自动选点、显式换点（破坏性确认）、备份恢复迁移、人工重绑四类操作 |
| 执行 | 适配器编译为节点 label 约束（`edgesets.node-id`）；**cron job 服务继承 app 绑定**（同约束下发，任务型 job 落数据节点） | 适配器 | 随绑定自动下发 |

不变量：**绑定优先于 label 的缺失**（用户删除 pin 不触发迁移）；有卷应用不存在「无绑定」的合法运行态；绑定节点不可用时**不迁移、不换点**。

### 2.2 节点身份

- **领域身份 = 平台节点 ID**（`n_<ULID>`，永不复用，用户裁决），写入节点 label `edgesets.node-id`；服务约束引用它。
- **显示名**=Swarm hostname（展示用）；label 可写名或 ID，名→ID 解析失败 → 422 + 候选清单。
- Swarm node ID 仅存在于适配器映射（`runtime_node_refs`）；**节点重入/重建后的人工重绑**：`edgesets placement rebind <app> --node <新节点> --data-restored|--discard`，不做 adopt 流程、不做 machine-id 自动判定（D18 砍单）。

### 2.3 label 与 API

```yaml
services:
  web:
    labels:
      edgesets.placement.node: srv-01   # 可选：hostname 或 n_<ULID>；省略 = 平台自动选点并持久保持
```

- 粒度 app 级（一个任务挂 app 全部卷，只能落单节点）；不做 process/卷级、不做标签选择器 DSL（硬钉住是唯一语义）。
- 有命名卷/宿主绑定 → **强制钉住**（平台自动，无需用户声明）；无卷应用默认不钉，显式 pin 时出计划警告（失去自动重调度）。
- 用户写 `deploy.placement.constraints` 时仅允许 `node.labels.edgesets.*` 命名空间，其余 → `E_COMPOSE_UNSUPPORTED`。
- 校验：`volumes` 非空时 `replicas` 必须为 1（本地卷不能多副本共享）。
- label 一致性裁决：同 app 多服务 label 指向不同节点 → 422 `E_PLACEMENT_LABEL_CONFLICT`；label 指定节点与当前绑定不一致 → 不生效，部署前 409 `E_PLACEMENT_MOVE_REQUIRES_ACK`（换点走 `PUT /v1/apps/{app}/placement` 破坏性确认）；label 缺失或与绑定一致 → 正常（绑定优先于 label 缺失）。

| API | 语义 |
|---|---|
| `GET /v1/apps/{app}/placement` | source/node/state/reason/volumes |
| `PUT /v1/apps/{app}/placement` | `{node:"srv-01"\|null, dataAck:"restored"\|"discarded", confirm}`；换点 = 破坏性 |
| `GET /v1/nodes` | 只读节点列表（含平台 ID/显示名/状态/已钉应用）；变更用 `docker node` |
| `GET /v1/volumes` | 卷与孤儿清单（删除指引到节点上手动执行） |

### 2.4 状态模型增量（SQLite，只加不减）

```sql
placements(app PRIMARY KEY, node_id, source /* platform|label */, label_ref,
           state /* ok|blocked|unresolved */, reason, pinned_at, updated_at, etag)
volumes(id, app, key, kind /* named|bind */, node_id, docker_name, mount_path,
        host_path, status /* active|orphaned */, created_at, UNIQUE(app, key))
-- nodes：平台 ID/显示名/观测字段（见状态模型专项）
-- 适配器私有：runtime_node_refs(platform_id, swarm_node_id)
-- 用途：身份 label 被删改后对账器自动重放的依据（映射本身可从 label 反建）
```

### 2.5 选点与前哨（确定性、可解释）

```
resolve_placement(app):
  label pin → 解析（名或 ID）；失败 → E_PLACEMENT_NODE_INVALID / NOT_FOUND
  已有绑定 → 保持（绑定优先）
  无绑定且无卷 → 不钉（自由调度）
  无绑定且有卷 → 自动选点：候选 = 节点 ready；
    评分 = 数据引力（卷所在节点） > 已钉应用数少 > 名称序
    无候选 → E_PLACEMENT_NO_ELIGIBLE_NODE

deploy_preflight(app):
  绑定节点非 ready → E_PLACEMENT_NODE_UNAVAILABLE（快速失败，不排队）
  卷数据节点 ≠ 目标节点 → 需 dataAck：
    "restored"  → 目标节点上任一绑定卷已由恢复流程重建（用户声明 + 审计）
    "discarded" → admin + confirm，旧卷置 orphaned，事件 volume.discarded
    缺省      → E_VOLUME_NODE_MISMATCH（数据安全前哨，把空卷事故变成 409）
```

### 2.6 生命周期与漂移矩阵（精简版）

| 漂移 | 判定 | 动作 |
|---|---|---|
| 绑定节点 DOWN | 观测（心跳 15s 量级） | 应用 `blocked`（UI 横幅）；任务 PENDING；进行中部署 `blocked_waiting`（发布看门狗暂停计时、可 cancel；节点恢复续跑并重新起算）；新部署快速失败 |
| 绑定节点 drain（主动维护） | `docker node update --availability drain` 后观测 | 同「DOWN」：应用 `blocked`、任务 PENDING；回岗（active）后自动回绑、本地卷数据不丢（维护窗口语义见架构 §2.6） |
| 绑定节点恢复 | 观测 | 自动回到绑定（任务落回唯一合法节点），事件 `placement.recovered` |
| 绑定节点移除 | `docker node rm` 后观测 | `blocked(node_gone)`；进行中部署以 `E_PLACEMENT_NODE_GONE` 失败；人工二选一：恢复数据后 `rebind --data-restored` / `rebind --discard` |
| 节点身份 label 被删改 | 对账器 | 自动重放 label + 事件（安全不变量） |
| 卷位置 ≠ 绑定（手工移动/残留） | 对账器 + 前哨 | 部署 409 `E_VOLUME_NODE_MISMATCH`；不自动修数据 |
| 无状态应用新增卷 | 部署时 | **钉住当时运行节点**（数据诞生点）；未运行则按选点算法 |
| DR 后无法判定绑定 | 恢复流程 | `unresolved`，要求显式放置；不猜测 |
| 节点改名/重装换机 | 按平台 ID 绑定，不受影响 | 换机 = 节点移除路径；重装后重新 join 并人工重绑 |

### 2.7 节点变更与数据恢复路径

- **节点变更**一律用 `docker node` 原生命令（drain/rm/update）；平台只读展示并在对账时反映结果，不建生命周期 API。
- **换点/迁移**：唯一受支持路径 = **备份恢复迁移**（restic 还原到目标节点 → 用户声明 `--data-restored` → 改绑定 → 发布；旧卷转孤儿）。空卷路径（`--discard`）需 admin + confirm + 审计。
- **卷生命周期**：删除应用默认保留卷（`orphaned`，可发现）；运行中卷拒绝删除；**远端卷删除/校验由用户按文档在节点上手动执行**（`docker volume rm`），平台不做 docker.sock 维护作业。
- **孤儿卷**：`GET /v1/volumes` 可见；清理 = 节点上手动删除 + 平台对账消失。

### 2.8 错误码与事件（精简）

- 错误码：`E_PLACEMENT_NODE_INVALID`、`E_PLACEMENT_NODE_NOT_FOUND`、`E_PLACEMENT_NODE_UNAVAILABLE`、`E_PLACEMENT_NODE_GONE`、`E_PLACEMENT_NO_ELIGIBLE_NODE`、`E_PLACEMENT_MOVE_REQUIRES_ACK`、`E_PLACEMENT_LABEL_CONFLICT`、`E_VOLUME_NODE_MISMATCH`。警告：`W_PLACEMENT_STATELESS_PIN`。
- 事件：`placement.{bound,changed,blocked,recovered,unresolved}`、`node.{joined,down,up,removed}`、`volume.{created,detached,orphaned,discarded}`。
- UI/CLI：应用详情「运行位置」卡片（节点、来源、原因、状态）；`edgesets nodes ls`、`apps placement`、`apps placement rebind`、`volumes ls --orphaned`；破坏性操作统一 `--confirm-destructive` + 回显。

### 2.9 v0.1 单节点

- 同一字段、同一代码路径（单节点 = 候选集只有一项）；label 可写本机名；未声明则自动绑定本机。
- 多节点操作返回 `E_CAPABILITY_REQUIRES_MULTI_NODE`，不静默成功；v0.1 → v0.2 零迁移。

## 3. 关键决策及理由

| # | 决策 | 理由 | 被否选项及原因 | 来源 |
|---|---|---|---|---|
| D-PLC-1 | 有卷应用默认自动钉住 + 绑定持久保持 | 数据安全必须是默认行为；用户/AI Agent 不应被迫理解节点概念 | 要求显式 pin（摩擦 + 易漏）；不钉（空卷事故） | 独立收敛 |
| D-PLC-2 | 平台节点 ID 为绑定锚，显示名仅供读写 | 重名机器不得静默接管有状态应用；重建/换机可控 | hostname 作身份（重名风险）；直接用 Swarm node ID（L2 重建即失效） | **用户裁决** |
| D-PLC-3 | 卷-节点归属一等状态 + 部署前哨 409 | 约束满足 ≠ 数据在位；把静默事故变成显式错误 | 只依赖约束（无数据校验） | 独立收敛 |
| D-PLC-4 | 节点消失不迁移、不换点；blocked 可 cancel | 数据唯一安全动作是不动；回滚目标同样不可调度（空操作） | 自动换点重建（空卷）；统一超时失败（误导用户） | 独立收敛 |
| D-PLC-5 | 数据迁移唯一路径 = 备份恢复；空卷路径需 admin+confirm | 两种真实意图必须在 API 层区分 | 单一 `--force`（把丢失与迁移混同）；在线卷迁移（无共享存储） | 独立收敛 |
| D-PLC-6 | 重入/换机走**人工 rebind**，不做 adopt 流程与 machine-id 自动判定 | ≤10 台场景换机是低频手工事件；自动化流程（marker 校验/身份冲突消解/adopt）复杂度远超收益（D18） | adopt + 卷内 marker + machine-id 自动再关联（过度设计，砍单） | 裁决（D18 砍单） |
| D-PLC-7 | 选点用三因子（数据引力 > 已钉数 > 名称序） | 可解释、可测试；多因子评分（worker 优先/内存/计数）在 ≤10 台无实测收益 | 多因子加权评分（提前优化） | 裁决（D18 精简） |
| D-PLC-8 | 不做 rebalance 机制 | Swarm 无原生 rebalance，重排 = 重启；小集群人工用 `docker node` 处理即可 | rebalance plan/apply（Dokploy 亦无，过度设计） | 裁决（D18 砍单） |

## 4. 分步实施

- **v0.1**：`edgesets.placement.node` label 解析与校验、自动绑定（单节点同路径）、卷注册表、前哨 409、删除应用保留卷。
- **v0.2**：多节点绑定与选点、`GET /v1/nodes` 只读列表、孤儿卷可见性与手动清理指引、备份恢复迁移（restic）+ `rebind` CLI。
- **v0.3**：模板目录中带卷模板的放置指引。

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| 带点 label 约束解析、身份 label 被改动 | 钉住失效 | Spike V6a 实测；对账器自动重放 + 事件（安全不变量） |
| 无指标阶段选点质量 | 选到繁忙节点 | 显式 pin 可覆盖；指标落地后校准；不宣传智能调度 |
| 用户期待「跟着应用走」 | 认知落差 | UI 徽标与事件解释「为什么在这」；用户访谈验证 |
| 用户要求用 S3（兼容）卷承载数据库、或期待借此获得有状态自动迁移 | 数据损坏 / 性能崩塌 / 信任受损 | §7 明确不做并公开理由；S3 定位（备份目标 + 对象存储端点）写入文档；真实共享 POSIX 需求 → 退出预案（k3s + CSI/JuiceFS）评估，不在 Swarm 上叠 S3-FS |
| 远端卷只能手动删除 | 误删/漏删 | 文档化指引 + 孤儿清单可见；不做高危 docker.sock 机制 |
| 重绑依赖用户声明数据已恢复 | 声明不实时数据未恢复 | `--data-restored` 需 admin + 审计；恢复流程回读校验沿用备份体系 |
| 24h ORPHANED 语义未实测 | 承诺偏差 | 不对外承诺定时升级；文档用「节点长期不可达时提示」保守措辞 |

## 6. 测试与验收

- **V6a**（卷与绑定）：有卷无约束迁移得空卷（复现并文档化）；加绑定后任务钉住且不迁移。
- **V6b**（绑定保持与基本漂移）：down→blocked→恢复自动回绑（进行中部署续跑）；remove→blocked→人工 rebind 两条路径（进行中部署以 `E_PLACEMENT_NODE_GONE` 失败）；label 被删自动重放；卷位置不匹配 409；**drain 维护**：无状态节点零停机、有状态节点 drain→回岗自动回绑（数据不丢）。
- 单元：选点确定性（三因子）、绑定保持、前哨分支（restored/discarded/缺省）、校验规则。
- 卷 label 传递（Spike 待验证）：服务 spec 创建卷时 `VolumeOptions.Labels` 是否生效；生效则卷身份收敛到 label（消掉名称解析与 `appid8` 生成）。

## 7. 明确不做

- 不做 stateful 自动迁移 / 自动换点 / 自动 failback
- 不做标签/偏好式调度 DSL（只做节点级硬钉住）
- 不做 rebalance 机制（自动与手动 plan/apply 皆不建）
- 不做节点 adopt 流程 / 身份自动消解 / 卷内 marker / machine-id 自动再关联
- 不做节点生命周期 API（drain/remove/rename 用 `docker node`）
- 不做远端卷维护作业（docker.sock 挂载）；远端卷删除/校验由用户在节点上执行。**唯一例外（2026-09-17 审核裁决，架构 D19）**：执行中继 `edgesets-exec`（global service，每节点挂本机 docker.sock）——API 面仅 exec、仅平台标记容器、仅 overlay 内可达；不做卷/镜像/节点操作，例外范围不随功能扩张
- 不做跨节点卷在线迁移（备份恢复是唯一路径）
- 不做卷数据自动删除
- 不做 S3（兼容存储）/ FUSE / CSI-S3 作为数据卷，亦不做「把数据库放 S3 上换取自动迁移」的方案：S3 无 POSIX 语义（无可靠 fsync、无文件锁、对象不可变、延迟高 3~4 个数量级），与 PG/MySQL/Redis/Mongo 的崩溃恢复模型不可调和；JuiceFS 类方案要引入元数据引擎（Redis/PG）与每节点客户端，与 1~2h/周 维护预算冲突。S3 只做备份目标（restic）与应用对象存储端点（凭证注入）；跨节点共享 POSIX 需求出现时走退出预案（k3s + CSI）评估

## 8. 来源与验证（独立设计×交叉验证 + D18 精简）

- **独立收敛**：placement 可选+自动；有卷强制/自动钉；label+约束（适配器内）；卷-节点归属一等 + 前哨拦截；数据引力选点；节点消失不迁移；跨点双路径 + 显式 ack；数据不自动删；单节点同路径；拒绝选择器 DSL。
- **裁决**：绑定模型合并（单记录 + 「绑定优先」规则）；marker 校验改为用户显式声明（D18）；进行中部署 blocked / 新部署快速失败。
- **用户裁决**：平台节点 ID 为锚（D-PLC-2）。
- **独有但经 D18 砍除**：远端卷能力与维护作业、卷内 marker、adopt/身份消解、24h 升级提醒、rebalance——记录于此以防遗忘，真实需求出现时再评估。
- **开放问题**：PENDING 重评估行为；无指标选点质量。
