# fleetly E4 数据库托管专项设计

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案（待裁决轮） | 2026-09-20 | [v0.2 规划 §2 W4 / §5 E4 行 / §3 V2-5、V2-2](../plan/2026-09-20-v0.2-plan.md)；[架构 §2.3/§2.4 应用模型数据库行 / §4.3 v0.2 路线](2026-09-17-architecture.md)；[发布专项 §2.4 快照与回滚](2026-09-17-release-semantics.md)；[放置专项（卷钉住复用）](2026-09-17-stateful-placement.md)；[状态模型专项（表/台账纪律）](2026-09-17-state-model.md)；[zane-ops 调研 R5（generate_* 先例）](../research/2026-09-20-zane-ops-comparison.md)；[v0.1 冻结清单 FZ-1（source=system）](../plan/2026-09-17-v0.1-scope-freeze.md)；现状代码：internal/envlayer、internal/secrets、internal/state/{volume,env,apps,deployments}.go、internal/compose/validate.go、internal/engine/{ports,planner,rollback}.go、internal/naming |

## 1. 现状与问题

v0.1 收口后，「托管数据服务机制」（架构 §1.2/§2.4：模板 + 卷钉住 + 备份/恢复适配器 + 连接串注入）全部缺位，用户自建数据库只能以普通 compose app 形态部署，承担四类现存的硬缺口：

| # | 现状 | 缺口 |
|---|---|---|
| 1 | 应用模型只有 git/image 两种来源（apps 表 + git 触发列，`internal/state/appgit.go`） | 无库实例资源：创建/暂停/升级/删除无生命周期面；无模板 |
| 2 | 跨 app 网络严格隔离（每 app 专属 overlay + 别名 = compose 服务名，`internal/naming.NetworkName`） | app 无法连另一个 app 的库——用户被迫把库塞进同一 compose，卷钉住与库运维语义混入业务 app |
| 3 | env 三层合并链已留位 `source=system`（envlayer 词表 + `env_vars.source` 列，v0.1 无生产者——代码注释明示「模板连接串留位」） | 无凭据生成/轮换/注入实现；FZ-1 挂账 W4 |
| 4 | compose `secrets` 在 Load 期显式拒绝（`E_COMPOSE_UNSUPPORTED`，评审 C1：平台密钥库未接入前拒绝比晚期失败诚实） | 无平台密钥库；Swarm secret 管道（`engine.SecretMount`/`substrate` SecretReference、`naming.SecretName` hash8 换名）已就绪但无来源 |
| 5 | 备份台账只有控制面状态（`state_backups`：SQLite 快照 + 本地路径，S3 上传随 E3） | 库数据无备份/恢复；E3（restic + S3/RustFS）是本设计的前置 |

实现侧已核实的三个关键事实（设计以此为锚，不重造）：

- **env 管道就绪度高于预期**：`env_vars(app_id, key, value 密文, source ∈ {platform,system}, status)` 唯一键 (app_id,key)；`envlayer.MergeChain` 已实现平台层内部 `system > platform`；pending 参与合并、部署成功后统一提升（S16-C4）。连接串注入 = 写 source=system 行，**合并链零改动**。
- **Swarm secret 管道就绪**：`engine.ServiceSpec.Secrets`（SecretName + Target=/run/secrets/\<名\>）与 `substrate` 适配已实现并有漂移反解；缺的只是存储（app_secrets 表）、compose 校验开放与 planner 接线。
- **回滚 env 语义有暗礁**：`internal/engine/rollback.go` 头注——重放「compose 字段与合并 env 按快照（desired_spec 密文，含合并 env 明文）」。连接串（含密码）一旦进快照，回滚会回放**旧密码**，与发布专项 D-REL-9「secret 值永远取当前」冲突。v0.1 因密钥库未接入而「结构性满足」；E4 落地时此路径必被击穿，须显式修正（D-DB-11，§6）。

## 2. 目标设计

### 2.1 资源模型：库实例 = app 的一种形态

**库实例（database instance，待入术语表）是 app 资源的一个形态**：apps 表加 `kind` 列（`app` | `database`），库专属明细落新表 `db_instances`（模板、镜像 digest、设置），模板渲染产物作为「平台生成的 compose.Spec」走**既有**部署管线。状态机（`state.EnterPhase` 单写点）、per-app 部署互斥队列、放置绑定、卷注册表（`RegisterAppVolume`）、漂移检测、审计、tombstone 删除（`deleting → deleted`、卷默认保留转 orphaned）全部照用，零第二实现。

```
用户/API                          平台内部                        既有机制（照用）
CreateDatabase ──→ app 行(kind=database) + db_instances 行
                 + 凭据生成(env_vars, source=system)
                 ──→ 模板渲染 → compose.Spec ──→ 部署队列/EnterPhase/健康门/观察窗
                                              ──→ 放置绑定 + 卷注册(volume.created)
                                              ──→ Swarm service（fleetly-<db>-<service>）
引用 app 部署 ──→ label 解析 → db_references 倒排 + system env 物化 + 网络挂载
```

- **deployments/revisions 复用**：库实例每次部署（创建/升级/设置变更/resume）产生正常 deployment；revision = 模板渲染快照（`source_kind=template`）——**库升级的回滚 = 既有单层重放**（回退到旧 digest 渲染），不新增机制。
- **UI/CLI 面**：`/v1/databases` 独立资源面（list/get/create/delete/suspend/resume/settings/rotate/backup/restore）；apps 列表以 kind 徽标并集展示。CLI `fleetly databases <verb>`。API-first 铁律：REST 先行，Console/CLI 为客户端。
- **审计面**：库操作全部入审计（`db.*` 动作词表见 §5），与业务写同事务 fail-closed 照用。
- **术语对齐**：库实例不是「服务」不是「容器」；模板（template）= 平台内置的引擎定义；引用（reference）= 引用方 app 服务对库实例的声明关系。均标注待入 UBIQUITOUS_LANGUAGE。

### 2.2 模板机制

模板 = **平台内置 Go 注册表条目**（`internal/dbtemplate` 新包），随平台版本发布；不是文件、不是数据、不可热更。每个模板定义：

| 字段 | postgres-16（首发） | redis-7（首发） |
|---|---|---|
| 镜像 | `postgres:16-x` digest 钉定（随平台 release 锁定，R7 门禁同批） | `redis:7-x` digest 钉定 |
| 引擎内部端口 | 5432 | 6379 |
| 卷 | key=`data`，挂 `/var/lib/postgresql/data`（PGDATA 子目录约定由模板处理 initdb lost+found 问题） | key=`data`，挂 `/data` |
| 凭据规格 | 固定 `POSTGRES_USER=fleetly`；`POSTGRES_DB=<实例名，'-'→'_'>`；生成 32 位 [a-zA-Z0-9] 密码 | 仅生成 32 位密码（requirepass） |
| 凭据投递 | **Swarm secret 文件**（`POSTGRES_PASSWORD_FILE=/run/secrets/password`，官方镜像原生支持） | 启动参数 `--requirepass`（Swarm spec 参数明文——与 env 同暴露类，文档明示，见 §6 已知边界） |
| 健康门 | `pg_isready -U fleetty`（interval 5s/timeout 3s/retries 3/start_period 30s） | `redis-cli ping`（同缺省） |
| 默认限额 | cpus 1.0 / memory 1Gi（创建时可覆盖） | cpus 0.5 / memory 256Mi |
| 连接串渲染 | `postgres://fleetly:<pw>@<实例名>:5432/<dbname>` | `redis://:<pw>@<实例名>:6379/0` |

- **升级语义**：平台 release 携带新 digest（minor/patch）→ 既有实例**不自动变**；`databases upgrade` 逐实例 opt-in（一次正常部署，revision 重放可回退）。平台检测到可升级 → `db.upgrade_available` 事件。主版本升级（16→17）与引擎切换不做（§7）。
- **镜像受管**：用户不可改库镜像/引擎参数（设置面只有限额与备份计划）；违规 → `E_DB_TEMPLATE_UNSUPPORTED`。
- 新引擎接入成本 = 一个模板条目 + 一个 `EngineAdapter`（§2.6）+ 备份镜像工具，与架构 §4.3「各引擎备份/恢复适配器是主要成本」一致；MySQL/Mongo 后置按需求排序。

### 2.3 生命周期

| 操作 | 语义 | 备注 |
|---|---|---|
| 创建 | app 行 + db_instances + 凭据生成 + 模板渲染部署 | 名字与 app 共享唯一名字空间（`ErrAppExists` 既有语义；DNS 名唯一性由同名字空间保证） |
| 设置变更 | 限额/备份计划 → settings 更新；限额变更触发一次部署 | desired-hash 变化自然进漂移面 |
| 暂停 | settings.suspended=true → 一次部署置 replicas=0（保留 service 与卷，同「首发失败 scale=0」形态） | 事件 `db.suspended`；引用方连不上是诚实暴露（错误信息即产品） |
| 恢复 | suspended=false → 正常重部署 | 事件 `db.resumed` |
| 升级 | digest 更新 + 一次部署 | §2.2 |
| 删除 | tombstone 两拍照用；**有引用禁删**（`E_DB_REFERENCED` 409 + 引用清单）；卷默认保留转 orphaned，显式 `--delete-volumes` 才删数据（复用卷语义） | reap 移除受管 service 与共享网络 |

### 2.4 跨 app 网络：平台牵线共享网络（V2-5 落地细则）

**库实例的「app 专属网络」即共享网络**：`fleetly-<db>-net`（复用 `naming.NetworkName`，零新命名公式）。库服务挂该网络，**别名 = 库实例名**（而非模板服务名——防两个 PG 实例的通用别名 `postgres` 在共享网络互撞 DNS；由模板渲染在 planner 层供给，naming 包不改）。引用方服务由平台在**其部署时**附加挂载到该网络（无附加别名，以 Swarm 服务名可达）。

| 时序点 | 行为 |
|---|---|
| 库首次部署 | `NetworkEnsure(fleetly-<db>-net)`（既有幂等创建，managed label） |
| 引用 app 部署（plan 期） | 解析服务 label `fleetly.databases: "pg-prod"`（逗号分隔，多库可引）→ 逐名校验：app 存在、kind=database、lifecycle=active，否则 `E_DB_NOT_FOUND`（404，附候选清单）；**不要求 running**（建库与引用部署可并行；库未就绪/已暂停 → 计划警告 `W_DB_REFERENCE_NOT_READY`，不阻塞） |
| 引用 app 部署（plan 期） | env 前缀计算：实例名 `-`→`_` 大写；同 app 引用两个前缀撞名（`pg-prod` vs `pg_prod`）→ `E_DB_ENV_PREFIX_CONFLICT`（422） |
| 引用 app 部署（releasing 前） | `NetworkEnsure` 全部附加网络（既有调用面扩展为多网络）→ ServiceSpec.Networks += 共享网络（desired-hash 含网络，后续手动摘挂进漂移面） |
| 引用关系登记 | planner 同事务维护 `db_references(db_app_id, app_id, service, env_prefix)` 倒排索引——**可从各 app 当前 revision 重建的派生登记**（compose 仍是唯一期望态真源）；label 移除并部署 = 行删除；引用 app 删除 = 行级联清理 |
| 库删除（前置哨兵） | `db_references` 非空 → `E_DB_REFERENCED`（409，context 列出引用 app/服务清单）——与放置前哨（卷-节点 409）同型的数据安全前哨 |
| 库删除（reap） | 引用清零后删除受管 service 与共享网络 |

不开放任何用户自由跨网语法（compose 外部网络仍在拒绝清单，V2-5 裁决）；网络内流量为 Swarm overlay 缺省**不加密**（性能与兼容取舍，已知边界，§6）。

### 2.5 凭据与连接串注入（FZ-1）

- **生成时机**：创建库实例时一次（模板凭据规格），落 **DB 实例自身** 的 `env_vars`（source=system，key=`PASSWORD`，age 密文——复用 env 存储与加解密边界，不建新凭据表）。
- **注入形态**：引用方 app 部署时，planner 把连接信息**物化**为引用 app 的 `env_vars` 行（source=system，走既有 upsert → pending → 本次部署合并消费 → 成功后提升的完整链路；值变化 → 行回 pending → 随下次部署生效——与 S16-C4 天然一致）。键集（模板渲染定义）：

| 引擎 | 物化键（`<NAME>` = 实例名大写下划线形） |
|---|---|
| PG | `FLEETLY_DB_<NAME>_URL`、`_HOST`（= 实例名 DNS 别名）、`_PORT`（5432）、`_USER`、`_PASSWORD`、`_DATABASE` |
| Redis | `FLEETLY_DB_<NAME>_URL`、`_HOST`、`_PORT`（6379）、`_PASSWORD` |

  `FLEETLY_*` 前缀为平台保留名字空间：用户 `SetEnv` 撞前缀 → `E_ENV_KEY_RESERVED`（422；同时防 system 行被用户 upsert 劫持 source——现状 `SetAppEnv` 会改写 source，此守卫是必要补丁）。
- **可见性**：只读展示照既有 env 投影（`EnvVarView.source=system` 已支持；Console 库详情页展示连接信息，密码默认脱敏、显式展开；`fleetly databases show` 同级 admin 面）——对齐 R5 先例（生成、存平台层、对用户只读）。
- **轮换**：**仅手动**（`databases rotate`，破坏性操作两段式确认）。流程按引擎经适配器钩子：PG = 一次性 job 执行 `ALTER USER fleetly WITH PASSWORD`（热轮换，库不重启）；Redis = 更新值 + 库实例重启（requirepass 重读）。随后：DB 侧凭据行更新 → 逐引用 app 的 system 行更新为 pending → **平台自动触发全部引用 app 重部署**（各自走正常部署队列；不重部署 = 旧密码失效即断连，无更诚实选项）→ `db.credentials_rotated` 事件 + 审计。定期自动轮换不做（§7）。
- **连接串格式**：引擎标准 URI（§2.2 表）；密码字符集 [a-zA-Z0-9] 免 percent-encode。

### 2.6 备份与恢复适配器（对接 E3）

每引擎一个 `EngineAdapter`（Go interface，conformance 套件随 MySQL/Mongo 接入时补齐——首两引擎单实现，先钉接口不建套件）：

```go
type EngineAdapter interface {
    // Backup 逻辑备份：一次性 Swarm job（钉实例绑定节点、挂实例卷）内
    // 流式导出 → restic 入库（repo 内路径 db/<instance>/<snapshot>）。
    Backup(ctx context.Context, in BackupInput) (BackupOutcome, error)
    // Restore 原地恢复：实例 scale 0 → job 挂卷 rw 重放 → 重部署。
    Restore(ctx context.Context, in RestoreInput) error
    // Verify 回读校验：restic 读回 + 引擎级头部校验（pg_restore --list /
    // RDB magic），台账 verify_status 置位——「备份假成功」零容忍。
    Verify(ctx context.Context, in BackupOutcome) error
    // RotateCredential 引擎侧热轮换（§2.5）。
    RotateCredential(ctx context.Context, in RotateInput) error
}
```

| 裁定点 | 结论 | 被否方案 |
|---|---|---|
| PG 备份形态 | **pg_dump -Fc 逻辑备份**（运行中一致性、引擎 minor 版本可移植、体积小） | restic 卷快照 PG 数据目录：WAL 在途写入非崩溃一致（需停库才稳）；恢复跨版本脆弱；被否 |
| Redis 备份 | RDB 流式导出（`redis-cli --rdb -` 管道） | AOF 复制（体积与恢复复杂度）；卷快照（同上） |
| 备份目标 | 复用 **E3 restic 基础设施**（外部 S3 端点或 opt-in RustFS），repo 内独立命名空间 `db/<instance>/` | 独立 S3 桶/独立 repo（配置面翻倍）；直传 S3 不过 restic（失去去重/加密/完整性） |
| 台账 | **独立 `db_backups` 表**（restic snapshot ID 寻址，非文件路径；kind ∈ daily/manual/pre_upgrade；verify_status 三态） | 并入 `state_backups`：控制面快照与库数据备份的保留策略/恢复语义/schema 全不同，CHECK 重建还牵连；被否 |
| 恢复 | **原地恢复**（同实例，停库重放，confirm 破坏性确认） | 一键恢复到新实例：跨节点 DR 场景走「建新库 + 手动重放 + rebind」文档化 runbook（E1 rebind CLI 组合），不进 v0.2 API |
| 执行体 | 一次性 Swarm job（同放置约束钉节点——**远端节点 local 卷不可经 manager 读**是既有硬约束）、镜像 = 平台 dbtools 镜像（引擎工具 + restic，digest 钉定随平台发布）、库凭据与 S3 目标经 Swarm secret 注入 | 控制面中转流（受远端卷不可读约束 + 单点带宽）；常驻 agent（违背轻单核） |
| 调度 | 复用 E5 调度核（备份 ticker 演进，同一调度器），per 实例计划（缺省每日 03:00 UTC，保留 7 份，prune 沿用台账保留期删除语义） | 自建定时器 |
| 诚实口径 | 同节点 RustFS 目标上的库备份 = **便捷层非灾备**（V2-2 口径延伸），Console 与文档同标注 | — |

### 2.7 平台密钥库与 compose secrets 开放

**开放面 = 所有 app**（含库实例自身），机制统一；仅对库实例开放会造出第二套 secret 语义。

- **唯一来源 = 平台密钥库**：新表 `app_secrets(app_id, name, value_cipher age 密文, hash8, UNIQUE(app_id,name))`；`fleetly secrets set/list/rm` + proto SecretsService（Set/List/Remove）。**不提供值读回**（忘记即轮换——比 env GetEnv 的 admin 明文路径更严一档：secret 是更敏感类；hash8 供引用比对）。轮换 = 换 hash8 换 Swarm secret 名（`naming.SecretName` 既有设计）→ 引用进 desired-hash → 随下次部署换挂——与 `secrets.SwarmSecretRef` 注释「值轮换即换名换引用，轮换天然触发重部署」逐字兑现。
- **compose 形态收窄开放**（受控子集只增方向）：顶层 `secrets:` 仅接受 `external: true` 形态；`file:`/`environment:` 来源拒绝（`E_COMPOSE_UNSUPPORTED`——**值不进 git 仓库**是硬边界）；服务级 `secrets:` 仅短语法或 `{source, target}`（uid/gid/mode 拒绝）。声明名在库中不存在 → 部署 preflight `E_SECRET_NOT_FOUND`（422）。`validate.go` 拒绝清单移除 secrets 两项 = C1 裁决预留口的显式解除（评审门禁，§6）。
- **注入**：planner 经 `app_secrets` 构造 `SecretMount`（SecretName=`fleetly-<app>-<name>-<hash8>`、Target=`/run/secrets/<compose 名>`）——`engine.ServiceSpec.Secrets` 与 substrate 适配**零改动**（管道已就绪，§1 事实 2）。
- 库实例引擎凭据同库存储（internal 行，source 标注 system），与用户 secret 同一加密与轮换纪律（§2.2/§2.5）。

## 3. 关键裁决表（D-DB-*）

| # | 裁决 | 理由 | 被否方案 | 终裁 |
|---|---|---|---|---|
| D-DB-1 | 库实例 = **app 的一种形态**（apps.kind + db_instances 明细），状态机/队列/放置/卷/审计/tombstone 全复用 | EnterPhase 单写点与 per-app 互斥队列是刚花 T0-V2.2 收敛的资产；库与 app 共享「有卷 + 钉住 + 部署记录」本质；12-20 人日预算只容复用 | 独立资源类型（新表/新 API/新状态机）：第二套「有卷有放置的东西」概念，重复实现互斥队列与 EnterPhase 语义，违约轻单核与 D13 核心收敛 | ⚖️ 留用户终裁（影响 API/UI 长期面） |
| D-DB-2 | 模板 = **平台内置 Go 注册表**，版本与镜像 digest 随平台 release 受管；实例升级逐个 opt-in、可 revision 重放回退 | 模板耦合备份适配器与凭据规格，是代码不是数据；热更模板 = 动态插件面（D13 禁）；版本偏斜产生未测组合；供应链受控（digest 门禁 R7） | compose 文件模板库热更（插件面 + 任意镜像引用的供应链面）；用户可控镜像（备份/健康门契约失守，Dokploy 式全开放被否） | ⚖️ 留用户终裁（镜像受管 vs 用户可控是产品口径） |
| D-DB-3 | 凭据：创建时生成、age 存 env_vars(source=system)、引用方**物化** `FLEETLY_DB_<NAME>_{URL,HOST,PORT,USER,PASSWORD,DATABASE}` 行（Redis 无 USER/DATABASE）、只读展示、**仅手动轮换**（自动触发引用 app 重部署） | 物化使 ListEnv/revision/desired-hash/pending 语义零改动兑现（envlayer 留位即为此设计）；R5 先例同构；轮换后不重部署引用方 = 必断连，自动化是唯一诚实选项 | 运行时合成不物化（投影 API 与快照链路全要另造）；定期轮换（无告警面配套前是定时炸弹） | ⚖️ 留用户终裁（键名前缀与键集是对外契约，改名窗口在落地前） |
| D-DB-4 | 引用声明 = compose 服务 label `fleetly.databases`（逗号分隔） | 单一真源 = compose（期望态治理规则）；与 fleetly.domains 同载体同风格；plan/diff 天然可见 | API 旁路注册（并行期望态，违反「不建与 compose 并行的期望态」纪律——cron 同款裁决） | 已裁 |
| D-DB-5 | 网络时序：库网络即共享网络、别名 = 实例名；引用前哨只查存在性（`E_DB_NOT_FOUND`）不查就绪（`W_DB_REFERENCE_NOT_READY` 警告）；**有引用禁删**（`E_DB_REFERENCED` 409） | 建库/引用可并行（bootstrap 顺序是用户事务，警告比阻塞诚实）；数据安全前哨与卷-节点 409 同型 | 就绪门禁（部署耦合库健康，级联阻塞）；允许删库留悬空引用（数据事故面） | 已裁 |
| D-DB-6 | 备份：每引擎逻辑备份（pg_dump -Fc / RDB 流式）→ restic 同基础设施独立命名空间；**独立 `db_backups` 台账** + 回读校验；恢复 = 原地 + confirm；跨节点 DR 走 runbook | 见 §2.6 表（各行被否方案已列） | restic 卷快照、并入 state_backups、控制面中转、一键跨实例恢复 | 已裁 |
| D-DB-7 | secrets 开放面 = **所有 app**，external-only 形态，平台密钥库为唯一来源，无值读回 | 机制 app 无关；file: 来源把密钥耦合进仓库；读回面扩大敏感面（忘记即轮换更安全） | 仅库实例开放（第二套语义）；file: 来源；GetSecret 明文读回 | 已裁 |
| D-DB-8 | 错误码/事件：新增 9 码（§5）+ `db.*` 7 事件；生命周期事件**复用** `app.*`/`deployment.*` 既有族（subject 带 kind） | 只增纪律下最小新增面；库的部署成功/删除与 app 语义同构，双份事件是噪音 | 全部新族（码集膨胀）；全部复用 E_COMPOSE_*（域语义错位，误导排障） | 已裁 |
| D-DB-9 | 库实例 = **用户 app 资源**，不计入 600MB 平台组件 idle 预算；默认限额 PG 1C/1Gi、Redis 0.5C/256Mi（创建可调） | 600MB 红线约束的是平台常驻组件（fleetlyd/Traefik/containerd/zot/VL/RustFS）；库是用户负载，与用户 app 同资源隔离与容量边界口径（§4.2 既有「构建与应用资源隔离」）；备份 job 为瞬态非常驻 | 库计入平台预算（把用户负载算进平台成本，口径失真）；无默认限额（单机雪邦面） | 已裁（口径确认，红线 1 复核通过：E4 零新增常驻组件） |
| D-DB-10 | 库引擎凭据投递：Swarm secret 文件优先（PG `POSTGRES_PASSWORD_FILE` 原生支持）；Redis 走启动参数（spec 明文，与 env 同暴露类，文档明示） | 原生支持 _FILE 的引擎零成本硬化；Redis 无文件形态，硬造 wrapper 是过度设计 | 全 env（PG 有更好形态不用）；全 secret（Redis 需自造 wrapper） | 已裁 |
| D-DB-11 | 回滚对 **source=system 行取当前值**（重放时按 key 从 env_vars 重读），不随 desired_spec 快照回放 | rollback.go 现状按密文快照整体回放合并 env——连接串含密码进快照后，轮换+回滚组合必回放旧密码断连，违反 D-REL-9；v0.1 靠「无密钥库」结构性豁免，E4 落地即失效 | 快照回放（断连事故）；轮换时改写历史快照（快照不可变纪律） | 已裁（对既有实现是**修正项**，随 E4 票据落地并回写发布专项 §2.4 一句限定） |

## 4. v0.2 切面与验收

**主验收闭环**（v0.2 规划 E4 行原文口径）：建库 → app 引用连接串部署 → 备份恢复闭环。E4 对 E3 的依赖仅在备份步（第 6 步）；模板/生命周期/注入/secrets（1-5、7 步）不依赖 E3，W3 若滑动可先行。

| 步 | 内容 | 验证方式 | 回滚路径 |
|---|---|---|---|
| 1 | 状态层迁移（apps.kind、db_instances、db_references、app_secrets、db_backups，只加法）+ `E_ENV_KEY_RESERVED` 守卫（SetAppEnv 拒 `FLEETLY_*` 用户写） | 迁移前后快照测试 + env 保留前缀负面测试 | 恢复快照（无 down migration，既有纪律） |
| 2 | 模板注册表 + 渲染器（两模板全字段）+ EngineAdapter 接口 | 渲染确定性 golden 测试（同输入同 Spec 同 hash） | 纯新增包，移除即回零 |
| 3 | 生命周期 API/CLI + 部署管线接入（渲染 Spec 进 planner；source_kind=template） | dind E2E：建 PG/Redis → healthy → 卷注册 → 钉住；suspend/resume | 删实例（卷默认保留） |
| 4 | 引用与注入：label 解析、前缀冲突哨兵、db_references 维护、system env 物化、多网络 NetworkEnsure、`W_DB_REFERENCE_NOT_READY` | E2E：app 以 label 引用 → 部署 → 容器内以 URL 真连接读写 PG；删除守卫 409；**回滚后仍连通（D-DB-11 断言）** | label 移除 + 部署（引用与注入行随之清除） |
| 5 | 轮换：rotate API + 适配器钩子 + 引用 app 自动重部署编排 | E2E：写入数据 → rotate → 引用 app 自动重部署 → 旧数据仍在且新连接成功 | 不可逆（审计 + 事件留痕；两段式确认） |
| 6 | 备份/恢复：dbtools 镜像（digest 钉定）+ 备份 job + db_backups 台账 + 调度核接入 + verify + 恢复 API（confirm） | E2E：写入行 → 备份 → verify=verified → 破坏性清空卷数据 → 恢复 → 行断言一致；失败路径红色告警；Redis 同型 | 恢复即逆操作；备份保留窗 prune |
| 7 | secrets 开放：存储 + SecretsService + compose 校验收窄开放 + planner 注入 | E2E：external secret 声明 → /run/secrets 读到值；缺库 `E_SECRET_NOT_FOUND`；file: 拒绝；golden 白名单更新 | 拒绝清单还原（C1 形态） |
| 8 | Console：库实例列表/详情（连接信息脱敏默认、备份列表、升级入口）+ secrets 页 | data-testid 锚点只增 + Playwright 冒烟扩展（沿用 W1 冒烟轨道） | 前端面独立，可单独回退 |

**横切验收**：600MB 复测确认平台组件无新增常驻（dbtools/restic job 为瞬态）；错误码/事件注册表 golden 快照更新过评审（只增门禁）；`buf breaking` 零破坏（新增文件与字段）；MCP 工具面预算核算（E4 相关读写工具 ≤30 预算内计入，E2 时点统一核算）。

## 5. 契约面

### 5.1 proto 加法（`fleetly.server.v1`，全为新增文件/RPC，零 breaking）

```proto
service DatabaseService {
  rpc CreateDatabase / GetDatabase / ListDatabases / DeleteDatabase   // 删除带 confirm + delete_volumes
  rpc SuspendDatabase / ResumeDatabase / UpdateDatabaseSettings       // 限额/备份计划
  rpc UpgradeDatabase                                                 // digest 升级（opt-in）
  rpc RotateDatabaseCredentials                                       // 破坏性两段式
  rpc TriggerDatabaseBackup / ListDatabaseBackups / RestoreDatabaseBackup  // 恢复带 confirm
}
service SecretsService { rpc SetSecret / ListSecrets / RemoveSecret }   // 无值读回
```

`DatabaseView`：name、template、image_digest、status（复用部署派生态）、suspended、placement、volume、connection（脱敏投影 + 显式 reveal）、backup_plan。`app.*` 既有 View 加 `kind` 字段（proto 加字段零 breaking）。

### 5.2 错误码（注册表只增，9 码）

| 码 | HTTP | 语义 |
|---|---|---|
| E_DB_NOT_FOUND | 404 | 引用/操作的目标库实例不存在或非 database 形态（附候选清单） |
| E_DB_REFERENCED | 409 | 有引用 app 时禁删（附引用清单） |
| E_DB_TEMPLATE_UNSUPPORTED | 400 | 模板 ID 未知 / 设置违反模板受管面 |
| E_DB_ENV_PREFIX_CONFLICT | 422 | 同 app 引用的多库 env 前缀撞名 |
| E_DB_BACKUP_FAILED | 500 | 备份 job 失败（资源终态类，主呈现于台账与事件） |
| E_DB_RESTORE_FAILED | 500 | 恢复失败（原地恢复中断即 critical 告警） |
| E_DB_ROTATE_FAILED | 500 | 轮换中途失败（附已完成阶段，人工收尾） |
| E_SECRET_NOT_FOUND | 422 | compose 声明的 external secret 不在库（preflight） |
| E_ENV_KEY_RESERVED | 422 | 用户写 `FLEETLY_*` 保留名字空间 |

警告码：`W_DB_REFERENCE_NOT_READY`（引用的库未就绪/已暂停，计划警告不阻塞）。

### 5.3 事件（7 个，只增）与审计

事件：`db.suspended`、`db.resumed`、`db.upgrade_available`、`db.backup_succeeded`、`db.backup_failed`、`db.restore_completed`、`db.credentials_rotated`。生命周期其余复用 `deployment.*`/`app.*`（subject=app:<id>，payload 带 kind=database）。审计动作：`db.create/delete/suspend/resume/upgrade/rotate/backup_trigger/restore`、`secret.set/removed`（human/ai_agent；system 类自动动作照「自动动作必入审计」纪律）。

### 5.4 表与配置

迁移单条（只加法）：`apps.kind` 列 + `db_instances`、`db_references`、`app_secrets`、`db_backups` 四表（schema 见 §2 各节；db_backups 以 restic snapshot ID 寻址，verify_status 三态与 state_backups 同款）。配置键：`databases.backup_interval_hours=24`、`databases.backup_keep=7`、`databases.backup_hour_utc=3`（平台缺省，实例可覆盖）。CLI：`fleetly databases <create|get|list|delete|suspend|resume|upgrade|rotate|backup|restore|show>`、`fleetly secrets <set|list|rm>`。

## 6. 与既有文档一致性

| 面 | 一致性核对 |
|---|---|
| 单写点/轻单核 | 库实例部署走既有 EnterPhase 单写点与队列；零新增常驻组件（红线 2 与 600MB 复核通过，D-DB-9）；模板为代码内注册表非动态插件（D13） |
| 只增纪律 | 错误码/事件/迁移/白名单全为加法；**拒绝清单移除 secrets 两项 = C1 裁决预留口的显式解除**（validate.go 注释原文「v0.2 平台密钥库接入后解除」预授权；走显式评审 + golden 同步，非静默减项） |
| 快照回滚语义 | revision 含三层合并结果（system 行 hash 已自然入快照）；**D-DB-11 修正**：回放对 source=system 行按 key 取当前值——本设计对发布专项 §2.4 的限定补写（「合并 env 按快照」增加「source=system 行除外，取当前」），实现载体 rollback.go 重放路径随 E4 票据改；卷数据/库内容不回滚（既有） |
| 诚实契约 | 备份 verify 三态 + 失败红色告警；同节点 RustFS 库备份标注「便捷层非灾备」（V2-2 延伸）；suspend/未就绪引用如实警告；Redis 凭据 spec 明文与 overlay 网内不加密为已知边界（对齐架构 §2.3「Swarm spec env 明文」边界族，文档明示） |
| 安全默认 | 库不发布 host 端口、无 fleetly.domains 即不进路由（「数据库默认不暴露公网」由构造满足）；凭据 age 密文；值不进事件/审计/日志（负面测试随票） |
| 术语 | 新词条待入 UBIQUITOUS_LANGUAGE：**库实例 database instance / 模板 template / 引用 reference（数据库语境）/ 库备份 database backup（与 backup=控制面快照分立）/ 库恢复 database restore（restore 裸词仍仅指控制面 DR——本设计用带限定词第二义，需术语表裁决收录）/ 平台密钥库 platform secret store** |
| 依赖链 | E3（restic/S3 目标）为备份步前置（W4 排序已含）；E5 调度核共享（cron 细则同款「与平台热备、数据库备份共用同一调度核」）；放置/卷/rebind 全复用放置专项 |

**发现的既有冲突（如实记录）**：① rollback.go 现状「合并 env 按密文快照回放」与发布专项 D-RL-9「secret 值取当前」在密钥库接入后冲突（v0.1 靠无密钥库结构性豁免）——D-DB-11 修正；② env.proto:27 注释「合并链只消费 effective 行」与 env.go/envlayer 实现（S16-C4：pending 参与合并）不一致——既有文档注释层小冲突，与本设计无直接关系，随 E4 票据顺手订正；③ 词汇表 backup/restore 词条与本设计的库备份/库恢复撞词——以带限定词新词条化解，待术语表裁决。

## 7. 明确不做

- MySQL/MongoDB 模板（紧随按需求排序——各引擎备份适配器是主要成本，架构 §4.3）
- 主版本升级（PG 16→17）与引擎切换迁移路径
- 读写分离/副本/库层 HA（有状态 HA 边界口径不变：库所在节点失联 = 该库不可用，恢复走备份重放 + rebind）
- 库内多 database/多用户管理、RBAC、计量计费、多租户（v0.2 规划 §8）
- 定期自动轮换凭据（仅手动）
- 一键恢复到新实例/跨节点自动 DR（runbook 化：建新库 + 手动重放 + rebind CLI）
- 库暴露公网/TLS 入口（网内不加密为缺省口径）
- 连接池/代理组件（pgbouncer 类）
- 纳管用户自建外部库（adopt）
- compose `secrets` 的 `file:`/`environment:` 来源与 uid/gid/mode 子键；secret 值读回 API
- 库实例自动换点/自动迁移（钉住纪律，D16）；用户自由跨 app 网络（V2-5）
