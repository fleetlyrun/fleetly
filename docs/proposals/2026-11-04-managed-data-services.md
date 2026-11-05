# 托管数据服务重设计：三家族 + 版本矩阵 + 双车道配置面（2026-11-04）

> 状态：**文档批，待用户终裁**。配套：设计文档 `docs/design/2026-11-04-managed-data-services.md`、ADR-0093（词汇）/0094（版本矩阵）/0095（配置面）/0096（新家族）。终裁后按 §7 批次实施。
>
> 前提裁定（2026-11-04 用户）：开发期**无向后兼容义务，staging 可全量重建**——「无兼容层」原则贯穿全程序（不写双源解析、不留过渡形态、schema/接口/用户面直接定终态）。配置机制（含运行时热修改）为一等面。

## 0. 背景与动机

托管 Database 线（ADR-0029/0039/0045/0051）已跑通完整闭环：per-engine adapter 模板注册表、用户域第三形态收敛环、引擎原生 dump→ObjectStore 备份链、凭证单真源 Secret、四件浏览方言。但有四组结构缺口长期挂账：

1. **无版本矩阵**：每引擎单一钉版，`CreateDatabaseRequest` 无 version 字段（ADR-0029 决策 2 挂账"随 F2"至今）；无 major 升级路径；用户被锁死在平台选定的唯一版本上。
2. **无配置面**：引擎参数零暴露（`maxmemory`/`appendonly`/`shared_buffers`/retention 全不可调），Console 创建对话框注释直书「引擎参数配置属二期」；模板参数创建即不可变的旧裁决把 resources 一起锁死。
3. **家族单一**：Kafka/NATS/对象存储类托管服务全仓零存在——消息与对象存储是 PaaS 数据服务面的另两块主拼图（zeabur 借鉴提案 B5.2/B5.5 均点名"托管数据库线"为未立项未来线）。
4. **受理面不自描述**：引擎值域硬编码在 console（`DATABASE_ENGINES` 常量）与 proto 注释里，版本矩阵化后静态枚举必然漂移。

本程序 = **沿既有健康底座做泛化与新家族复制**，不是推翻：收敛环/Runtime.Ensure/备份链/凭证面/材料纪律全部复用，新增的是版本维度、配置车道与两个新家族。

## 1. 现状盘点（要点）

- 五引擎（postgres/pgvector/redis/mysql/mongo）单版本 tag+digest 双钉（ADR-0045）；version 只活在回显投影（服务端从模板动态取，行上无列）。
- dbtemplate = per-engine adapter 注册表零 switch；`Template` 接口是 URL 假设（`ConnURL`/`PasswordFromURL`）。
- 部署 = 用户域收敛环（databases 卫星，ADR-0088），replicas 恒 1、resources 不建模（`capability.Resources` 存在但投影不填）、卷名公式化、节点钉住经 Volume.PinnedNodeID；swarm+k3s 双 runtime 支持。
- 备份四引擎 e2e 矩阵（pg/mysql/mongo 流式 + redis 预置卷；pgvector 复用 postgres 基座但不在矩阵）。
- 连接注入两通道：`secret_refs` 文件注入（DATABASE_URL 惯例）+ `database_refs` → `FLEETLY_DB_*` env。
- `bucket` 是 ObjectStore 词条 _Avoid_ 禁词（S3 配置字段本义豁免）；Kafka/NATS/PubSub/topic 零词汇登记。

## 2. 骨架裁决

### 2.1 已定项（本文档批立法，可随终裁翻案）

| # | 裁决 | 承载 |
|---|---|---|
| 1 | **底层统一、上层特化**（2026-11-05 用户裁决升级）：L3 统一托管服务机制（dataservices 单卫星单环 + family descriptor）承载收敛生命周期；上层三词条三聚合三 RPC+CLI+Console 特化不变；最底层与 App 共基（三轨一基：共享 Workload/Runtime.Ensure/材料/观测保障，不取 App 编排面） | ADR-0093/0096 |
| 2 | **版本矩阵**：每引擎 2-3 条 major 在册；Create 加 `version`（缺省=默认最新）；digest 逐版本钉定（ADR-0045 门禁扩容）；弃用版本拒新建、存量续跑；**major 升级 = 新建新版本实例 + restore-from-backup + 同名重建序切换引用** | ADR-0094 |
| 3 | **三新引擎全进，批次序 NATS → Bucket Store(silo) → Kafka（KRaft 单节点）**；MinIO 社区版 EOL 排除；RustFS 维持 ADR-0042 挂账不翻案 | ADR-0096 |
| 4 | **resources 进**：Create/聚合加可选 `resources{cpu_millis,memory_mb}`，Update 可变 | ADR-0095 |
| 5 | **配置面进**：白名单类型化旋钮 + **static/dynamic 双车道**（static=滚动替换；dynamic=工具容器热应用+引擎自持久优先）；settings/resources 经 `Update` 动词可变，engine/version 仍不可变（ADR-0029 决策 1 显式修订） | ADR-0095 |
| 6 | **目录自描述面**：`GET /v1/data-engines` 跨家族目录 RPC（引擎+在册版本+默认+弃用+旋钮 schema），Console/CLI 从目录取值，硬编码引擎常量退役 | ADR-0094/0095 |

### 2.2 待终裁项（开放问题，见 §8）

- 子资源管理面（topics/streams/buckets CRUD）是否随批（§8.1）。
- CLI `databases upgrade` 编排向导是否随 B3（§8.2）。
- 各家族默认版本值与在册集终值（§8.3，B2 选型定版）。

## 3. 家族与词汇（ADR-0093）

- **Database**（既有词条，值域收窄为 OLTP/KV/文档引擎：postgres/pgvector/mysql/mongo/redis）。
- **Bucket Store**（新词条）：S3 兼容的托管对象存储实例（silo 在册引擎；bucket 作为新词条域内本义合法，`bucket` 从 ObjectStore _Avoid_ 表移置——ObjectStore 词条自身保留 storage/S3 禁令，bucket 专项让渡，S3 配置字段豁免不变）。
- **PubSub**（新词条）：托管消息服务实例（nats/kafka 在册引擎；queue 已被部署 admission 队列语义占用，消息义统一走 PubSub/topic 词汇线）。
- 命名族：CLI 组 `fleetly buckets`/`fleetly pubsubs`；Secret 前缀 `bucketstore:`/`pubsub:`；DNS `bkt-<id>`/`pub-<id>`；载体 `fleetly-bkt-`/`fleetly-pub-<name>-<id>`（ADR-0092 双段律同款）。
- 单写原语与写类分桶（ADR-0081/0087）不动：新家族实体行是 DECLARATION 桶、目录 RPC 是 READ 桶。

## 4. 版本矩阵（ADR-0094）

- **三层解耦**：行 `version` 存用户面 major 短名（"17"）；tag（`17-bookworm`）浮动；digest 钉 patch。digest bump（ADR-0045 序）不动行值；只有 major 升级换值。
- 注册表版本化：adapter 声明 `Versions() []VersionSpec{ID,Tag,Digest,Default,Deprecated}`；`For(engine,version)` 返回绑版实例；跨版本差异 adapter 内分叉。
- **升级路径正典**：新建新版本实例（RestoreCompat 兼容检查）→ 恢复 → 同名重建序切换引用（删旧 → 同名建新+restore → Secret `database:<name>` 值覆盖新 host → 引用 App 重部署生效——ADR-0029 决策 8 既有语义的自然延伸）。
- **版本生命周期 SOP**：引入流程、digest 批量获取脚本、弃用→移除时间线（设计文档 §2.6）。

## 5. 配置面（ADR-0095）

- **白名单类型化旋钮**：每引擎 knob schema（bool/int 范围/enum/duration/size + `class: static|dynamic`），按 engine+version 键控；平台-owned 键（认证/监听/数据目录/SASL/复制面）永不可设；**否决自由配置文件注入**（postgresql.conf `preload_libraries`、redis.conf `loadmodule`、kafka `advertised.listeners`——安全与平台所有权边界）。
- **static 车道**：折进 `Materials(password, settings)` 既有渲染 → 材料字节变 = secret 指纹名变 = 受监督滚动（与 digest bump 同机械）。
- **dynamic 车道**：不进滚动材料；`ApplyDynamicSettings` 经一次性工具容器（RotatePassword 先例）执行引擎原生命令（redis `CONFIG SET`/PG `ALTER SYSTEM`+`pg_reload_conf`/MySQL `SET PERSIST`/Kafka AdminClient `incrementalAlterConfigs`）；**持久化优先引擎自持久**（PG `auto.conf` 住数据卷、MySQL `auto.cnf`、Kafka 控制器元数据——重启自愈）；redis 无自持久 → 卷内 user.conf include 模式或该旋钮降级 static（首批旋钮表逐行定 class）。
- **Update 动词分流**：static 子集→落行+Kick 滚动；dynamic 子集→同步热应用先行（失败精确报错不落行，先数据面后持久层）→成功落行；重建后（restore/generation 变）首 tick 幂等重放 dynamic 集；行=期望态单真源，声明式律（ADR-0081/0082）不破。接 ConfirmableVerbs 确认闭环（ADR-0089）：预览=旋钮 diff+车道判定+滚动后果（zero_touch/replace）。
- **可视化 = schema 驱动表单**：旋钮 schema 随目录 RPC 下发；Console 动态渲染表单按 class 出徽标（即时生效/触发滚动重启）+确认框分级披露；CLI `--set k=v`/`config describe` 同标注。

## 6. 新家族机制（ADR-0096）

- 三轴分立：NamespaceRef `.PubSub`/`.BucketStore`（ADR-0025 词汇分立先例）；swarm+k3s 双 provider 翻译同批。
- 统一托管服务机制：`dataservices` 单卫星单环 + family descriptor 注册表承载三家族收敛生命周期（受管域 ADR-0004 通用 reconciler 的用户域对称形态；环表 10→11，2026-11-05 用户裁决——独立环取舍反转）；上层三词条三聚合三 RPC 面特化不变。
- 连接注入：`pubsub_refs`/`bucketstore_refs`（App Spec）→ `FLEETLY_PUB_<NAME>_*`/`FLEETLY_BKT_<NAME>_*` env；compose label `fleetly.pubsubs`/`fleetly.bucketstores`（composeintake external 声明律扩——**zeabur B5.5 挂账随线收口**）。
- 备份诚实面：NATS=JetStream snapshot（`nats stream backup` 按流粒度 `.tgz` 单档，多流多对象；nats-box 工具容器钉版）；Kafka=v1 显式无数据备份挂账（无官方 dump 工具）；Bucket Store=v1 挂账（数据冗余非 dump 契约对应物）。
- 引擎选型：NATS=`nats:<ver>-alpine`（scratch 默认变体无 shell 不可 exec 探针；alpine 带 busybox wget 打 8222 `/healthz`）；Kafka=`apache/kafka` 官方（KRaft 缺省、`KAFKA_*` env→properties 翻译、单节点 combined；bitnami 变动荡排除）；silo=pgsty/silo（e2e 既有口径，正式选型 B5 落定）。

## 7. 范围与批次

文档批（本批）：proposal + design + ADR-0093..0096 + checklist F5 节 + 双侧重交叉验证。

实施批（终裁后，每批一 commit，纪律见设计文档 §7）：

| 批 | 内容 |
|---|---|
| B1 | 词汇落地（CONTEXT.md + guards 禁词面） |
| B2 | 机制统一+内核批：dataservices 卫星归一（databases 迁入 family descriptor、环表 10→11、行为零漂移）+ 注册表版本化 + knob schema 内核 + settings/version/resources 列 + Create/Update 受理 + 目录 RPC + digest 门禁扩容 + digest 脚本 |
| B3 | 用户面：CLI `--version`/`--resources`/`--set`/`update`/`config describe` + Console 目录驱动表单 + golden |
| B4 | NATS 批：live 定案 → pubtemplate + 聚合/环/双 provider/注入/CLI `pubsubs` 组/Console 页/观测/skills/JetStream 备份 + e2e |
| B5 | Bucket Store 批：silo 选型落定 → buckettemplate 全链 + AK/SK + skills |
| B6 | Kafka 批：KRaft 模板 + SASL 凭证 + AdminClient 动态配置面 + 备份挂账声明 + skills |
| B7 | 收尾：e2e 扩 pgvector+NATS+双车道断言、CI 预拉策略、checklist 回填；（若裁决进）B8 子资源管理面 |

staging 验证 = ADR-0092 同款**全量重建**（旧 DB 移档 `.pre-<tag>`、fresh install、应用栈重播）。

## 8. 开放问题（待终裁）

1. **子资源管理面**（§2.2）：topics（partitions/retention——单 broker 副本恒 1）/streams（subjects/retention）/buckets CRUD。机制草图 = 声明行 + admin 工具容器收敛（对齐 ADR-0081/0082 严格声明式），量级约等于一个小家族。**建议：v1 不进，挂账独立 ADR**（用户自管客户端现成可用）；若用户明确要，排 B8。
2. **CLI `databases upgrade` 编排向导**：编排「新建新版本+恢复+同名重建序」三步。**建议：B3 只做 runbook 文档，向导挂账**（升级序含删旧库等 destructive 面，先让手工序跑通一遍再固化）。
3. **默认版本值与在册集**：设计文档 §2.5 给建议矩阵（含 EOL 核对：redis 8.0 于 2026-12-01 EOL 不入册、mysql 8.0 已 EOL 不入册）；终值 B2 选型定版。
4. **目录 RPC scope**（评审 F-15）：`GET /v1/data-engines` 定 `databases:read` 对纯 pubsubs/buckets Token 的表单渲染有可达性矛盾。候选三案：维持 databases:read / 认证即读（非敏感自描述，Explain 同类）/ 三家族 read 任一（取决于 authz 面是否支持 OR）。**B2 按 authz 面能力定案**。

## 9. 负空间（本程序明确不做）

- 多副本/高可用数据服务（单副本+卷钉住+stop-first 既有形态不翻案；Kafka 单 broker topic 副本恒 1）。
- 跨节点 Volume 迁移原语（Backlog 既有挂账不动）。
- 自由配置文件/任意 env 注入（白名单旋钮面是唯一通道）。
- in-place major 升级（pg_upgrade 等危险面；升级=新建+恢复正典）。
- 平台自宿 ObjectStore（ADR-0042 RustFS 挂账**显式分立**：那是系统域受管组件线，本程序是用户域数据服务线；互不吸收）。
- 兼容垫片/双读路径/过渡形态（前提裁定直接消灭）。
- 每引擎参数全集（白名单首批小而精，按需增补走注册表+目录自描述，零发布成本）。

## 10. 协同面

- **并行提案 `2026-11-04-volume-carrier-keying-and-job-order.md`**（卷载体键 ULID 化/库卷异源检测/pre-deploy job 序）：与本程序在**数据库自动卷**面相交。裁定：卷键 ULID 化属 Volume 域地基，**先于或随本程序 B2 批落地**（并行提案自用批号 B1-B3 与本程序 B1-B7 撞名，跨文档引用写全称）；本程序新家族卷公式直接采用 ULID 键形态；「库模板首启在卷内落实例标记（instance id）」与本程序的 dynamic settings 重建重放（识别"实例重建"事件）共享同一标记面——B2 实施时合流。
- **zeabur 借鉴 B5.2**（catalog 独立数据仓→托管数据库线目录形态参照）：本程序目录 RPC 是其收口形态；zeabur 借鉴文档 B5.2/B5.5 状态注记**随本程序 B2/B5 批回改**（文档批不动 zeabur 两文档）。
- **ADR-0088 生长律环表**：pubsubs/bucketstores 两环随 B4/B5 登记。
- **ADR-0081 写类分桶表**：UpdateDatabase + 新家族 CRUD + 目录 RPC 逐 RPC 落桶，随 B2/B4/B5 扩表。
