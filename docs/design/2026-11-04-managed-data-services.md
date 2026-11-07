# 托管数据服务重设计：设计文档（2026-11-04）

> 配套：提案 `docs/proposals/2026-11-04-managed-data-services.md`（骨架裁决与负空间）、ADR-0093（词汇）/0094（版本矩阵）/0095（配置面）/0096（新家族）。本文档承载机制细化；标 **live 定案** 的点在对应实施批用 live_test 先例（`dbtemplate_live_test.go`）实证后收口。
>
> 前提：开发期无向后兼容义务、staging 可全量重建——不写双源解析、不留过渡形态。

## 1. 总览与家族划分

三家族各自一等实体，机制内核单源：

| 家族 | 引擎（首批） | 聚合/表 | RPC 服务 | CLI 组 | Secret 前缀 | DNS | 载体（ADR-0092 双段律） |
|---|---|---|---|---|---|---|---|
| Database（既有） | postgres/pgvector/mysql/mongo/redis | `databases`（既有表扩列） | DatabasesService（既有扩） | `fleetly databases` | `database:` | `db-<id>` | `fleetly-db-<name>-<id>` |
| PubSub | nats、kafka | `pubsubs`（新表） | PubSubsService（新） | `fleetly pubsubs` | `pubsub:` | `pub-<id>` | `fleetly-pub-<name>-<id>` |
| Bucket Store | silo | `bucket_stores`（新表） | BucketStoresService（新） | `fleetly buckets` | `bucketstore:` | `bkt-<id>` | `fleetly-bkt-<name>-<id>` |

模板包三立（词汇分立：dbtemplate 词条已钉数据库模板语义，ADR-0045/0050）：

```
internal/domain/services/
  dbtemplate/        （既有，版本化改造）
  pubtemplate/       （新）
  buckettemplate/    （新）
  enginetemplate/    （新：共享内核——VersionSpec/Knob/BackupSpec/RestoreSpec/
                       RotateSpec/ApplySpec/MintPassword/pinnedRef/validatePassword）
```

共享内核下沉后 dbtemplate 对外语义不变（`For/Engines/InfoFor` 签名随版本化演进，见 §2.2）；`BackupSpec/RestoreSpec/RotateSpec` 类型移入 enginetemplate，dbtemplate 以别名零漂移（engineports facts.go 先例）。

**Template 接口按家族分叉**：数据库家族保留 URL 单真源（`ConnURL`/`PasswordFromURL`——`database:<name>` 值=连接 URL 既有契约不动）；PubSub/Bucket 家族用连接材料渲染（Kafka 无 URL 方言——bootstrap+SASL 凭证），接口形：

```go
// pubtemplate.Template（要点）
ConnMaterial(host, password string) (ConnMaterialSpec, error) // nats: URL 串；kafka: bootstrap/mechanism/user/password 多字段
```

`pubsub:<name>` Secret 值 = 家族方言串（nats 为 URL；kafka 为可解析 k=v 串），回读器与渲染器同源（PasswordFromURL 同款写读同源律）。

## 2. 版本矩阵机制（ADR-0094）

### 2.1 三层解耦

| 层 | 载体 | 变更触发方 | 例 |
|---|---|---|---|
| **major 短名** | 行 `version` 列（NOT NULL） | 用户（创建时选定；换值=升级路径） | `17` |
| **镜像 tag** | VersionSpec.Tag（注册表常量） | 平台（suite/浮动 tag 演进） | `17-bookworm` |
| **digest** | VersionSpec.Digest（注册表常量，sha256 index digest） | 平台（ADR-0045 digest bump 序，追 patch） | `sha256:639ab…` |

推论：digest bump 只动注册表常量 → `Image()` 变 → 投影指纹变 → Generation 推进 → stop-first 受监督滚动恰一次；**行 `version` 与回显零变化**。major 升级 = 用户新建新 version 行 + 恢复（§2.4），不存在原地换版本。

### 2.2 注册表版本化

```go
// enginetemplate.VersionSpec
type VersionSpec struct {
    ID         string // 用户面 major 短名（行值/回显/API 值域）："17"
    Tag        string // 镜像 tag："17-bookworm"
    Digest     string // index digest（ADR-0045 律：tag@sha256 双段）
    Default    bool   // 每引擎恰一真
    Deprecated bool   // 拒新建、存量续跑、目录标灰
    Note       string // 弃用理由/移除窗口（目录回显）
}

// dbtemplate.Template 增Versions() []VersionSpec；绑版实例：
For(engine, version string) (Template, bool)   // version 空 = Default
VersionsFor(engine string) ([]VersionSpec, bool)
```

- 绑版实例 = adapter struct 持 version 字段（`Image()/Meta()/Materials()` 内按需分叉）；跨版本差异点收在 adapter 内（PG18 PGDATA 见 §2.5 注）。
- **pgvector 二维矩阵**：version ID 取复合短名 `0.8-pg17`/`0.8-pg18`（vector minor 跟平台钉版，tag 取 `0.8.x-pgNN-bookworm/trixie`）；注册表不建二维结构，pgvector adapter 自己维护 vector×pg 组合的合法集（在册表见 §2.5）。
- 受理位：`CreateDatabaseRequest.version` 值域 = 服务内注册表检查（与 engine 同律，ADR-0029 决策 10）；空 = Default；Deprecated 拒（E_INVALID_ARGUMENT 精确文案列合法值+弃用说明，errcode 零新码）。
- **行上回显换源**：`Database.version` 从「服务端模板动态取」改为「行值直读」——单源，无兼容层。
- migration：databases 表 `ALTER TABLE databases ADD COLUMN version TEXT NOT NULL DEFAULT ''` + `CHECK(version <> '')`？——**直接 NOT NULL 无 DEFAULT**，开发期库可弃（staging 全量重建后表空，新行恒带值；本地 dev 库按前提裁定重建）。不写回填 UPDATE、不写空值兜底解析。pubsubs/bucket_stores 新表生而带列。

### 2.3 目录自描述面（与 ADR-0095 决策 10 共构）

```
GET /v1/data-engines → repeated EngineCatalog{
  family, engine, dialect, versions[]{id, default, deprecated, note},
  knobs[]{...旋钮 schema，ADR-0095}, port, capabilities{backup, migrate, browse, restore_compat…}, license_note}
```

- 新 `DataEnginesService`（structure.proto），READ 桶，scope **认证即读**（2026-11-05 用户终裁：非敏感自描述面〔Explain/doctor 同类〕任意持证 Token 可读——纯 pubsubs/buckets Token 亦可渲染创建表单；B2 验证 authz 面免资源 scope 支持，不支持则降级 `databases:read` 并回注 ADR-0094）。
- **Console/CLI 唯一取值面**：`console/src/kit/databases.ts` 的 `DATABASE_ENGINES` 硬编码退役，创建对话框引擎/版本/旋钮全从目录渲染；CLI `engines list`/`config describe`。zeabur B5.2「catalog 形态」收口于此。
- 目录是**投影非存储**：服务端从三模板包注册表现场装配，零缓存零漂移。

### 2.4 升级路径正典

1. `databases create <new-name> --engine pg --version 18 --restore-from-backup <bk_…>`：受理位做 `RestoreCompat(engine, oldVersion, newVersion)` 检查——**oldVersion 取自 backups 行新增 `engine_version` 列**（新备份受理时落值；migration 只加列，旧备份行空值=同引擎放行但**受理文案标注「版本未记录，降级风险自负」**〔红队 A10：空值门放行 major 降级会在运行时才炸、留半删态〕）。模板层声明规则：首版=同引擎同版本放行、major 只许升不许降、跨引擎既有拒；**pgvector 复合 ID 分轴比较**（vector minor 与 pg major 各自独立只升——`0.8-pg17→0.8-pg18` 升、任一轴降即拒，红队 A10/B-AT-13）。
2. 数据落地、验证后（**② 前强制 TriggerBackup+VerifyBackup 取新备份**——① 的备份到 ② 删除之间的写入否则静默丢失，红队 A1；**③ 的恢复源显式=② 的新备份**）：删旧库（Secret 保留义不变）→ **处置旧数据卷**（动机修正〔红队 A1〕：ULID 卷键（ADR-0097，已落地 8da1255d）已消除「同名复用撞旧 PGDATA」——删卷步真动机=**容量回收**；现有动词只删行不删物理卷→**新增 `volumes purge` 动词**（wide 档确认+容量披露，CLI 触发 Runtime 物理卷删除，B7 落地——落地前为 runbook 手工序并显式标注））→ **同名重建序**：旧名重建新版本库 + 恢复 → Secret `database:<name>` 值覆盖新 host（`db-<新id>`）→ 引用方（`secret_refs`/`database_refs`）App 重新部署后生效（**断连窗披露**：③ restore 期间 Secret 指向已死 host，引用 App 断连=恢复时长，大库小时级——文案与 runbook 如实披露）。ADR-0029 决策 8 同名重建语义延伸。
3. CLI `databases upgrade` 编排向导（**B7 随批交付**，2026-11-05 用户终裁）：预览（版本 diff+断连窗+容量披露+purge 清单）→ wide 档确认 → 编排五步（新建+恢复→强制新备份→删旧→purge 旧卷→同名重建+恢复）**断点可重入**（每步独立可重放、中断态自描述）；runbook 手工序文档随批（向导的验金石）。

### 2.5 首批在册矩阵（建议值；digest B2 用脚本钉）

| 引擎 | 在册 version ID | tag 形 | Default | 备注 |
|---|---|---|---|---|
| postgres | `16` / `17` / `18` | 16/17-bookworm、18-trixie | `18` | **PG18 起镜像默认 PGDATA 迁版本化路径 `18/docker`**：DataTarget 维持全代父挂 `/var/lib/postgresql`（**现模板已是父挂 + `PGDATA=/var/lib/postgresql/pgdata` 子目录**，TestTemplateFaces 钉板）——16/17 维持 pgdata 子目录、18 走镜像默认，仅 PGDATA 一处由 adapter 绑版分叉；live 定案：B2 live_test 双代实证 |
| pgvector | `0.8-pg17` / `0.8-pg18` | 0.8.x-pgNN-suite | `0.8-pg18` | vector minor 平台钉版；tag 精确版 B2 定 |
| mysql | `8.4` / `9` | 8.4 / 9.x | `9` | 8.0 已 EOL（2026-04）不入册；9=innovation track 短在册窗（EOL 看护承载） |
| redis | `7.4` / `8.2` | 7.4 / 8.2-x | `8.2` | **8.0 于 2026-12-01 EOL 不入册**；7.2 临 EOL 不入册 |
| mongo | `8.0` / `8.2` | 8.0 / 8.2-x | `8.2` | 7.0 不入册（无兼容义务，收敛在册面） |
| nats | `2.10` / `2.11` | alpine 变体 | `2.11` | §4.6 选型 |
| kafka | `4.0` / `4.1` | apache/kafka | `4.1` | 3.x 不入册（新平台无历史包袱） |
| redpanda | `26` | redpandadata/redpanda v26.x | `26`（单条） | Kafka 协议族第三方实现（§4.6 方言族机制）；25.3 EOL 2026-11-19 临期不入册；**BSL 1.1 许可披露**（Additional Use Grant 禁 Streaming/Queuing Service 形态——目录 Note 显著露出，托管合规裁定归运营方）；tag B6 定版 |
| silo | `2026-09` | RELEASE.2026-09-16T… | 单版 | 单版引擎（VersionSpec 仅一枚，Default 恒真） |

在册政策：每引擎 2-3 条；**Default 策略 = 全最新（2026-11-05 用户终裁）——在册最新 major 为 Default**（e2e/staging 首验即新版本：风险前置、长期维护面收敛；mysql 9 innovation 短在册窗由 EOL 看护承载）；新 major 引入 = 注册表 entry + digest 脚本 + 目录自动出现（零发布成本）；旧 major 转 Deprecated = 注册表改标 + 目录标灰 + Create 拒**全新建**（**同版本带 restore 的同名重建放行——灾备义**，否则弃用版卷丢失时灾备被迫 major 升级；存量继续跑、备份/恢复/轮换全语义保持）；移除 = 待存量清零后的 runbook 动作（时间线见 §2.6）。

### 2.6 版本生命周期 SOP（ADR-0094 承载承诺，工具随 B2）

1. **引入**：PR 模板项 = 注册表 entry（tag+digest）+ `TestImageDigestsPinned` 扩容自动连坐 + 目录面自动出现 + e2e 默认版本不变（仅门禁级验证）。**e2e 矩阵只跑每引擎 Default 版本**（CI 时长控制）；非默认版本靠单测门禁。
2. **digest 追 patch**：`scripts/digests.sh <engine> [<version>]`（crane/go 脚本，B2 落地）——拉 registry manifest 回填注册表常量；全量漂移检查模式供 CI 可选跑。
3. **弃用**：Deprecated 标记 → 拒**全新建**（无恢复源）；**同版本带 restore 的同名重建放行**（灾备义，§2.5 在册政策）；跨版本恢复按 RestoreCompat（升向放行）。目录 Note 带移除窗口（建议 = 转弃用起 2 个发布周期）→ **存量清零后**才移除 entry（移除后收敛环对存量行 `For()` 未命中走防御跳过路径——仅兜底、不承诺收敛续跑，故移除以清零为前提）。
4. **EOL 看护**：上游 EOL 表纳入 SOP 人工核对（redis/mysql 教训入档：8.0/9.x 类短命线慎入 Default）。
5. **非默认版本真实启动走查（Default-only 盲区的答案）**：每次引入新 major 或翻转 Default 的批次，staging 走查清单加「新版本 create→连接→写读→dynamic/static 各一旋钮」冒烟（随批跑、不进 CI）；无版本变更的发布周期不强制。

## 3. 配置面机制（ADR-0095）

### 3.1 knob schema（注册表单源）

```go
// enginetemplate.Knob
type Knob struct {
    Key      string        // 用户面键："appendonly"（引擎原生词，不发明中间名）
    Kind     KnobKind      // bool | int | enum | duration | size
    Min, Max string        // int/size 范围（字符串承载，受理位解析）
    Enum     []string      // enum 值域
    Default  string
    Class    KnobClass     // static | dynamic
    Category string        // 表单分组："persistence"/"memory"/"connections"
    Restarts bool          // 展示提示（static 恒 true；dynamic 恒 false）
}
Knobs() []Knob            // Template 接口增面；engine+version 键控（adapter 绑版实例内分叉）
ValidateSettings(engine, version string, settings map[string]string) error  // 受理位单源：白名单外键拒、类型/范围/enum 拒，精确报错
```

- **平台-owned 键永不可设**（清单即守卫对照集，漏列即回流口——B2 落守卫前逐引擎补全）：认证（requirepass/密码/SASL/JAAS）、监听与通告（bind/listeners/advertised/listener.security.protocol.map）、数据目录（dir/datadir/store_dir/log.dirs）、复制与集群面（replicaof/quorum voters/super.users/authorizer.class.name）、模块与 shell 执行面（loadmodule/preload_libraries/shared_preload_libraries/local_preload_libraries/dynamic_library_path/**archive_command/restore_command**/rename-command）、文件包含与身份面（include/aclfile/hba_file/ssl_key_file/system_account/resolver 族）。执法=白名单本身就是完整闭集（不在 Knobs 里即不可设）+ 守卫测试（B2：TestPlatformOwnedKeysUnreachable——对每引擎断言 owned 键集与 Knobs 交集为空）。
- 值经渲染安全闸：旋钮值进配置文件/命令时过 validatePassword 同族字符集纪律（INI/YAML/conf 注入防线，按 Kind 收敛——bool/enum 天然安全，int/size 数值校验，duration 白名单格式）。

### 3.2 双车道

**static 车道**：settings 折进 `Materials(password, settings)`（接口签名扩展）→ adapter 渲染进它拥有的配置面（redis.conf/postgresql.conf 覆盖/server.properties/nats-server.conf）→ 材料字节变 = secret 值指纹名变 = 载体滚动替换。Workload env 类旋钮（kafka `KAFKA_HEAP_OPTS`）走 Workload env → spec 指纹 → Generation 推进 → 滚动。
**环侧 memo 签名扩段（评审 F1；红队 A5 降级翻案）**：`ensureFresh` 短路有 `ReconcileReplayInterval`（缺省 60s）强制重放兜底——不扩签名是 **60s 时延缺陷而非「永不重下发」**，F1 定级降 P2；扩段价值=static 旋钮变更**即时**送达（不等兜底拍）。**第四段 = 密文摘要 + static settings 子集摘要的复合代理**（红队 A5：不得按字面用 `MaterialsFingerprint`〔吃已渲染字节〕——那要求每拍解密凭证，推翻 database.go「凭证以密文摘要为代理」的短路立法；代理摘要保持零解密）。Generation 不随材料推进（RotatePassword 先例）。B2d 验收锚断言「static-only 变更 → fake Runtime **即时**收到 Ensure 真实重发（材料含新值）」。

**喂入律（结构性保证，评审 F2）**：投影与材料调用点只喂**调用方预过滤的 static 子集**——`tpl.Workload(staticOnly)`/`tpl.Materials(password, staticOnly)`；dynamic 键永不进 Workload env（kafka 的 `KAFKA_*` env→properties 翻译使 env 泄漏是最自然的错误实现）与 Materials。执法 = 接口契约注释 + 守卫 TestDynamicKeysAbsentFromProjection（dynamic 键出现在投影 env/材料即红）。

**dynamic 车道**（运行时热修改）：

```go
// enginetemplate.ApplySpec（RotateSpec 同族）
ApplyDynamicSettings(host, password string, settings map[string]string) (ApplySpec, error)
// Argv + SecretFiles + Env；执行载体 = RuntimeUtility 一次性工具容器（RotatePassword 先例）
```

| 引擎 | 命令面 | 自持久 | 备注 |
|---|---|---|---|
| postgres/pgvector | `ALTER SYSTEM SET …` + `SELECT pg_reload_conf()`（user 上下文参数免 reload） | ✅ `postgresql.auto.conf` 住 PGDATA（数据卷），重启自愈且优先于主 conf | 仅 sighup/user 上下文参数声明 dynamic；postmaster 上下文（shared_buffers/max_connections）声明 static |
| mysql | `SET PERSIST …` | ✅ `mysqld-auto.cnf` 住数据目录 | 8.4/9 全系支持 |
| kafka | AdminClient `incrementalAlterConfigs`（需客户端工具容器；B6 live 定案载体——apache/kafka 镜像自带 bin 脚本无 AdminClient CLI，用 `kafka-configs.sh --alter`） | ✅ 动态配置住 KRaft 控制器元数据 | dynamic broker config 子集（log.retention.* / message.max.bytes） |
| redis | `CONFIG SET` | ❌ 无自持久 | **首批 redis 旋钮全声明 static**（滚动重启成本低、卷钉住数据安全）；卷内 user.conf include + `CONFIG REWRITE` 模式挂账后手（B4 后评估） |
| mongo | — | — | 首批零旋钮（setParameter 面窄） |
| nats | — | — | 首批旋钮全 static（nats-server 热加载面极窄） |
| silo | — | — | 首批零旋钮 |

### 3.3 Update 动词（分流语义）

```
UpdateDatabase{database_id, resources?, settings?} → Database
```

- 受理位：引擎+版本键控 `ValidateSettings` 全量校验（含未变键的重放——声明式幂等）；resources 范围校验。
- **分流执行**：变更集 diff 出 static/dynamic 两子集——
  - dynamic 子集：**同步热应用先行**（工具容器执行 ApplySpec；失败=专用码 `E_SETTINGS_APPLY_FAILED` 精确错误〔哨兵映射，E_DATABASE_ROTATE_FAILED 同构；errcode 只增 +1〕，行不动——与 RotatePassword 同序：先数据面后持久层）→ 成功才落行；
  - static 子集：落行 + `KickDatabases()` → 收敛环滚动（async，既有机械）。
- **单写边界（红队 B-击一立法）**：dynamic apply 的全部驱动者（Update API 同步热应用、环重放）**共持 per-entity 互斥**（rotateMu 同款随域走——dataservices 域 utilityMu）；Update 全程持锁使 apply→memo→行三连原子化（堵并发 Update 的 engine=B/行=A 倒挂）；**memo 写入经 engineports memo 端口**（禁直写卫星私有表，ADR-0088 决策 7）。**半应用黑洞修**：多旋钮一次 apply 部分失败时「行不动+AppliedDigest 不动」会让 memo 永不失配、引擎停半应用态——**apply 失败路径强制 Forget AppliedDigest**，环重放拉回行值收敛（Rotate「失败零状态变更」论证在多键配置面不成立，不得搬运）。
- **重放时机门（红队 A7）**：重放仅 `status=running` 且非恢复挂起行执行（新行首拍/StartPeriod 窗/restorePass 在途不重放——否则穿窗 60–90 次失败工具容器 churn）；重放失败退避。在滚窗竞态（红队 A6）：Update 受理位 status!=running 即 E_CONFLICT 提示「滚动进行中」。
- **幂等/声明式律（承诺降格，红队 B-AT-08）**：行=期望态单真源；dynamic 半边是**事件触发收敛**（memo 失配/重建/重启触发重放）**非持续对照**——外部经 browse/exec 直改引擎配置不纠正（负空间声明；ADR-0081「持续纠正」判据在 dynamic 半边如实降格）。重放检查在 memo 短路判定之前执行。实例重建判据=卷内实例标记（**ADR-0097 契约升 enginetemplate 内核、字段实体中性化（entity_id/family）——三家族通吃**，红队 B-击三：否则新家族带着刚修完的异源缺陷出厂）。
- **dynamic memo 协议（评审 F6 立法）**：per-entity memo 增 `AppliedDigest`（已应用 dynamic 子集摘要），Update 路径在 apply 成功后、行落库同窗口**经 memo 端口**写入；memo 进程内随重启丢失——重启后按失配触发一次幂等重放。**已应用未落行窗**（apply 成功后崩溃）：恢复后以行值为期望态幂等重放拉回——用户可能收到过成功响应后遭回退，与 RotatePassword 崩溃窗同构，行单真源的正确代价，文案如实记录。
- **确认闭环**：接 ConfirmableVerbs（ADR-0089 默认强制）——预览 = settings/resources diff + 车道判定 + 滚动后果（dynamic 全集=zero_touch；含 static=replace）；确认档 standard（ADR-0086）。
- **写类**：DECLARATION 桶；freeze 入 FrozenVerbs；审计 `database.update`；事件 `database.updated`（eventcode 只增 +1/家族 ×3）。
- engine/version 不可变：Update 请求含之即 E_INVALID_ARGUMENT 指引升级路径文案。

### 3.4 首批旋钮表（class｜持久化路径）

| 引擎 | 旋钮 | Kind | class | 落点 |
|---|---|---|---|---|
| postgres/pgvector | `shared_buffers` | size | static | postgresql.conf 渲染 |
| | `max_connections` | int(1..10000) | static | postgresql.conf 渲染 |
| | `work_mem` | size | **dynamic** | ALTER SYSTEM 自持久 |
| redis | `appendonly` | bool | static | redis.conf 渲染（现硬编码 yes → 旋钮化） |
| | `maxmemory` | size | static | redis.conf 渲染 |
| | `maxmemory-policy` | enum | static | redis.conf 渲染 |
| mysql | `max_connections` | int | **dynamic** | SET PERSIST |
| | `innodb_buffer_pool_size` | size | **dynamic** | SET PERSIST（在线调） |
| kafka | `log.retention.ms` | duration | **dynamic** | kafka-configs.sh 动态面 |
| | `message.max.bytes` | size | **dynamic** | 同上 |
| | `num.partitions` | int | static | server.properties（`KAFKA_NUM_PARTITIONS`） |
| | `heap_mb` | size | static | `KAFKA_HEAP_OPTS`（Workload env；**平台 owned 面暴露的安全子集**） |
| nats | `jetstream_max_file` | size | static | nats-server.conf 渲染 |
| | `jetstream_max_memory` | size | static | 同上 |

旋钮集只增不改语义（键名即引擎原生词，冻结律同词条）；增补 = 注册表 + 目录自动出现 + golden 扩容，零发布成本。

### 3.5 可视化（Console/CLI）

- Console 创建对话框与 Settings tab：**目录驱动动态表单**（按 Category 分组、Kind→控件映射 bool=Switch/int/size=TextField+单位、enum=SelectField、校验消息服务端口径同源）；class 徽标（`instant` / `rolling`）；变更确认框（wide 档仅 destructive；settings 变更 standard 档，static 子集展示「将触发滚动重启」）。
- CLI：`databases create … --set appendonly=true --set maxmemory=512mb`（可重复）；`databases update <id> --set …`；`fleetly engines list [--json]` / `databases config describe <engine> [--version 17]`（旋钮面人读表+class 标注）。
- Agent 面：目录 `--json` 完整 schema；skills 同步（§6.3）。

## 4. 新家族机制（ADR-0096）

### 4.1 统一托管服务机制与家族特化（2026-11-05 用户裁决：底层统一、上层特化）

垂直分层（机制与策略分居；满足 ADR-0087 机制原语三判据——扇出=三家族 ≥2、零上行依赖、守卫可执法）：

| 层 | 载体 | 职责 |
|---|---|---|
| L4 用户面特化 | DatabasesService/PubSubsService/BucketStoresService + CLI 三动词组 + Console 姐妹页 | 三词条三聚合三 RPC 面不变（词汇冻结）；家族方言（连接材料形态、备份能力、browse 拒） |
| L3 托管服务机制 | **`internal/application/dataservices` 单卫星包 + family descriptor 注册表（注册表本体下沉 domain）+ 环表三条目**（每族一条，step 同源调 descriptor——机制单源/故障隔离/ring dead 三保，红队 B-AT-02 翻单环案） | 通用收敛生命周期：行→投影→材料→Ensure→观测→status 推进；Update 双车道分流；备份排程/执行/修剪/恢复调度；凭证铸造/轮换调度；配额；级联删除 |
| L2 家族/引擎声明 | dbtemplate/pubtemplate/buckettemplate + enginetemplate 共享内核 | 版本矩阵、knob schema、材料渲染、Backup/Restore/Rotate/Apply Spec、连接材料方言、目录装配 |
| L1 公共运行保障（**与 App 同基**） | capability.Workload + Runtime.Ensure（唯一写动词）+ Generation/内容等价（ADR-0064）+ 材料纪律（ADR-0014）+ Volume/Pinning/stop-first + Network/Egress + 观测/稳态看门狗 + drift/posture（ADR-0082）+ 事件/审计 kernel | 一切载体级保障 |
| L0 Provider 特化 | swarm/k3s 翻译 | 载体名/标签/卷/网络原语 |

**三轨一基**（对「最底层是否与 App 共基」的立法答案）：App 部署轨（Deployment 状态机/Revision）、受管域轨（ADR-0004）、数据服务轨（ADR-0029 第三形态）**共享 L1 全部保障原语**；数据服务不取的仅是 App 编排面——Deployment 状态机、Revision 冻结、Deployment Strategy（词条明定不适用）、Admission 队列：行即期望态、per-row Generation 原位收敛，无「旧→新受监督迁移」编排（第三形态的裁决本体）。

**family descriptor**（受管域「一个通用 reconciler + N 个 Describe()」〔ADR-0004〕的用户域对称形态）：每家族注册 {域聚合 Repo、模板包、凭证/连接方言、备份能力、配额、事件码族}；**注册表本体下沉 domain**（enginetemplate 域侧伴生——backups/dataservices 两卫星与 gRPC handler 三方合法消费，规避卫星间硬边〔红队 B-AT-01，ADR-0088 卫星矩阵〕）。

**环表形态（红队二次翻案，2026-11-05）：单卫星包 + 环表三条目**——`database`（保留现名，step 改调 dataservices 卫星分族步）/`pubsub`/`bucketstore` 三条，环表 **10→12**。原「单环 + per-family recover」案被否（红队 B-击二：假二分——单环只省一个 ticker，却把 loop.go「panic→ring dead」（全 application 层唯一 recover 的 fail-loud 教义）的触发面收窄到基础设施级 panic，家族级 bug 从此不进 health 面）；三环条目=机制单源（同一卫星包+descriptor）+ 故障域隔离（族 panic 只死族环）+ ring dead 原样执法三者全保，且每族一环=每族一写者，单写者序更纯。kick 面天然分族。代价注记：三环共享卫星内部状态需并发安全（memo 端口单锁/maintenanceMu 既有）。

聚合与表（L4 存储按聚合分表不并表——词汇/API 面分立，机制层经 descriptor 归一）：

```
pubsubs:        id/seq/project_id/name/engine/version/settings/resources 同 database
                credentials_ref/generation/spec_fingerprint/status/backup_interval_secs/
                backup_retention_secs/last_backup_at/created/updated/deleted_at
bucket_stores:  同形（silo 单引擎；quota 见 §7.4）
```

Repo 接口随聚合落 `internal/domain/data/pubsub/`、`data/bucketstore/`（ADR-0075 接口层律：domain 接口 + persistence 实现）；persistence 按聚合分包。状态面四值 `pending|running|degraded|stopped` 同 Database（观测推进，gen 匹配才采信——同款`advanceStatus`）。

### 4.2 轴、载体、双 runtime

- NamespaceRef 增 `.PubSub`/`.BucketStore` 轴（ADR-0025 决策 4 分立律）；swarm label `fleetly.ns.pubsub`/`fleetly.ns.bucketstore`；k3s 侧 namespace/前缀同源公式（`databaseNamePrefix` 先例，metricsquery 双臂正则连坐扩）。
- 载体 `fleetly-pub-<name>-<id>`/`fleetly-bkt-<name>-<id>`（ADR-0092 双段律）；挂全部活跃项目网（fail-closed 零网拒建）+ EgressSubset 同款；卷名公式化 + PinnedNodeID 钉住 + 挂卷恒 stop-first（既有律）。
- **增轴连坐清单（评审 F9：Database 轴现状 7 文件 39 处触点，B4/B5 防漏清单）**：swarm translate/maintenance/nodecontainers、k3s translate/network/netisolate/nodecontainers、observdomain owner 族、drift 跳过面、metricsquery 双臂正则、**composeintake 白名单三处**（intake.go:77/415/548——非白名单 label 拒收，`fleetly.pubsubs`/`fleetly.bucketstores` 必须显式入白）。
- **卷键协同**：采用并行提案的 ULID 卷键形态（若其先落）——B2 合流点。

### 4.3 收敛环

- 收敛环 = **dataservices 单卫星包 + 环表三条目**（§4.1 L3；ADR-0088 生长律登记：环表 10→12，`engine/construct.go`——database 环保留名改 step 指向+新增 pubsub/bucketstore 两条）。既有 databases 卫星迁入 descriptor 形态是 **B2e 机制统一步**，验收口径 = **明示漂移清单下的稳态等价**（漂移清单：memo 签名第四段、环 step 指向、卫星包名——环名不变/health 面不变；ringregistry 守卫白名单按登记制扩行；不再宣称「零漂移」〔红队 C-AT-02〕）。备份环（backups 卫星）行源同款经 descriptor 归一跨族（engine 空间已按模板泛化）。CRUD 服务端：三 proto 服务面不变，gRPC 实现共享通用 handler core（descriptor 参数化——manageds 先例）。
- 投影：`Workload{ID=行ID, Process=engine, Image=tpl.Image(), Env/Command=tpl.Workload(staticOnly), Replicas:1, Ports=[Meta.Port], Healthcheck=tpl.Probe(), Volumes=[{VolumeID=名, Target=tpl.DataTarget()}], StopGrace, Addressing=[家族 DNS 名], Networks=项目活跃网}`（**喂入律：只喂 static 子集**，§3.2）——与 `databases/projection.go` 同构，共享 helper 下沉 application 层（`dataservices` 共享投影帮手包或 engineports，实施时定，倾向 engineports 旁挂）。

### 4.4 凭证与连接注入

- 铸造：`MintPassword` 同源；NATS = 平台合成用户（nats-server.conf users 段，声明式轮换=Secret 重写+载体重下发——redis 同款）；Kafka = SASL admin 用户（JAAS/listener 配置面，static 材料内，轮换=滚动；**Kafka 凭证轮换方言 B6 评估，倾向挂账**）；silo = root AK/SK（启动 env，轮换=滚动）——**单用户模式取舍（评审 F-07）**：root 全权限超管是 zot per-Project 凭证域先例（ADR-0056）的倒退；v1 接受论证 = 单 Project 独占实例（zot 是多 Project 共享）+ per-App service account（silo 是 MinIO 系 fork，IAM 策略面可支撑）挂 B5 live 定案；暴露面经 ADR-0014 A.2 显式追认与轮换=滚动补偿。
- Secret 值：`pubsub:<name>` = `ConnMaterial` 方言串；`bucketstore:<name>` = `endpoint|ak|sk` k=v 串（AK/SK 复合凭证）。值永不回显、指纹+名回显同律。
- 注入：App Spec `pubsub_refs`/`bucketstore_refs`（spec.proto ProcessSpec **字段 19/20**——17/18 已被 config_mounts/restart_seq 占用）→ `FLEETLY_PUB_<NAME>_{URL|BOOTSTRAP}/HOST/PORT/USER/PASSWORD`（NAME=引用名 UPPER_SNAKE，`FLEETLY_DB_<NAME>` 同律；nats 出 URL、kafka 出 BOOTSTRAP+MECHANISM）与 `FLEETLY_BKT_<NAME>_ENDPOINT/ACCESS_KEY/SECRET_KEY`；**明文 env 承载凭证 = ADR-0014 附录 A.2 追认范围显式扩至本两族 refs**（PUB_PASSWORD/BKT_SECRET_KEY 是长效凭证、暴露面大于可原地轮换的 DB 密码——论证落 ADR-0096 决策 5，不默示继承 database_refs）；`InjectDatabaseEnv` 同构扩（databaseenv.go 泛化为 refs 注入帮手）；compose label `fleetly.pubsubs`/`fleetly.bucketstores`（ADR-0074 external 声明律 + **intake 白名单显式入册**（§4.2 连坐清单）+ 告警建议——B5.5 收口）。
- `secret_refs` 文件注入通道复用（值=方言串直用）。

### 4.5 统一转储契约：备份 / 还原 / 迁移（2026-11-05 用户裁决）

**立法：备份、还原、迁移 = 同一原语的三种编排——实例数据面的「转储（dump）→ 再注入（load）」。** 引擎差异全部收在模板契约（L2），机制层（backups 卫星，L3）跨族单源（family descriptor 归一行源）。

**契约面（enginetemplate）**：

```go
Dump(host, password) (DumpSpec, error)   // 转储执行（argv+材料；现 Backup() 升格）
ArtifactShape() ArtifactShape            // single_stream | object_set
Load(host, password) (LoadSpec, error)   // 注入执行（RestoreStream/RestorePreseed 既有二形态）
RestoreCompat(srcVer, dstVer) error      // 源×目标版本装载矩阵（ADR-0094 升级正典数据源）
DataCapabilities() DataCapabilities      // backup/restore/migrate 能力位（目录回显）
```

**产物集与 manifest 锚**：备份产物 = manifest 锚定的**对象集**——`backups/<project>/<id>/<ts>-<bk>/manifest.json`（成员键列表+逐件 sha256+family/engine/engine_version+总 digest）+ 成员对象。单流引擎（pg/mysql/mongo/redis）= 1 成员；NATS = 每流一 `.tgz`；未来分卷形态零机制变更。**verify = manifest 驱动逐件重算；Download 按成员流式；保留修剪 = manifest 驱动整集删除（行+对象集成对，既有律）**。无兼容层前提：直上 manifest 形，旧单对象键不迁移不承诺；Platform Backup（restic）捎带照旧——对 restic 只是文件，两层分立不动（ADR-0039）。

**统一还原编排（restore plan，restorePass 既有最高优先 pass）**：

```
RestorePlan{source=manifest 键, target=新建行|既有行, mode=auto（模板 LoadMode 定）}
受理：RestoreCompat（源=backups 行 engine_version × 目标 version）+ 形态前置（preseed 要求目标 Generation==0 + 卷钉控制面节点——既有约束）
执行：manifest 逐件拉取 → 工具容器注入（stream）或种子卷（preseed）
完成：restore_from_backup 清位 + 事件（既有语义）
```

三入口同一 plan：`create --restore-from-backup`（还原）/ 升级正典①（跨版本还原）/ 实例克隆（同版本还原到新实例）。

**迁移三形态统一归类**：

| 形态 | 机制 | 编排 |
|---|---|---|
| **版本迁移**（major 升级） | dump→load 跨版本（RestoreCompat 升向） | ADR-0094 正典：新建+恢复+同名重建序切换 |
| **实例迁移/克隆/灾备重建** | dump→load 同版本 | `create --restore-from-backup`（弃用版同版本重建放行——灾备义） |
| **载体迁移**（跨节点/跨 Runtime） | **不走转储契约**：卷搬运（drain+repin，数据不落地、限同 Runtime）；跨 Runtime = 显式数据处置 + dump→load 重建（ADR-0052 语义） | runbook 承载 |

**铁律：数据面迁移一律经 dump→load（ObjectStore 断点、可重放、可验证）；载体面迁移一律卷搬运——两径不混用。**

**能力位诚实推论**：`capabilities.backup=false ⇒ capabilities.migrate=false`（kafka v1 无数据备份 ⇒ 亦无 dump 基版本迁移——major 升级=新空实例或外部工具，目录/文案如实披露；silo 同挂账）。

**排程与治理（既有面跨族确认）**：schedulePass（interval/retention per 行，默认 24h/7d）**按 `DataCapabilities.backup` 过滤——无备份引擎不铸行、受理位禁配 interval**（红队 A9：否则 kafka 每 24h 铸必败行，「33h 11.4 万行」事故重演）；单飞执行/失败退避/修剪——descriptor 归一后备份环行源跨三族。

**物理约束（红队 A2/A3 立法——manifest 落地前必读）**：
1. **NATS 不可流式**：`nats stream backup` 产文件不产 stdout 流（现行链是 stdout→io.Pipe→ObjectStore 零落盘）——工具容器**逐成员串行**「落盘→Put→删本地」，manager 磁盘峰值=**最大单成员**（非对象集总量；swarm 走 manager 临时目录、k3s 走 hostPath）；`UtilityRequest` 单 Input 契约扩展进 B4a 验收锚。
2. **执行预算**：对象集=**逐成员独立预算**（BackupTimeout 15m 单飞硬顶对整集不适用——大体量整集单预算=永久失败循环；断点/续传挂账）。
3. **部分成功语义**：**manifest 最后写**（全成员成功才落行+台账 ObjectKey）；apply 失败路径显式删已 Put 成员（现「失败行无对象产物仅删行」假设在对象集模型下不成立）；List 驱动孤儿对账挂 backups 环节拍（B4a）。

**L4 家族特化清单**：

| 引擎 | 转储 | 注入 | 形态 |
|---|---|---|---|
| nats | `nats stream backup <stream> <out>.tgz` 逐流（**natsio/**nats-box 工具容器钉版——synadia/ 命名空间停更 5+ 年不可用〔红队 C-AT-06〕；流清单 `nats stream info --json` 枚举；KV bucket 是流天然覆盖；**memory storage 流不可快照→受理拒**） | `nats stream restore`（空流前提，全流循环） | object_set（每流一 .tgz 成员）；**live 定案 B4b**：tgz 实测+空流 restore 语义 |
| kafka | **v1 无数据备份**（无官方 dump 工具）——BackupPolicy 不可配、TriggerBackup 拒（E_INVALID_ARGUMENT 挂账文案）、目录 capabilities 标 `backup:false, migrate:false` | — | —（解封条件=官方工具面成熟或多副本立项） |
| silo | **v1 挂账**（数据冗余/外同步域非 dump 契约对应物——2026-10-29 评估既定） | — | — |

挂账：跨 Project 克隆（源对象键含 projectID，跨项目引用需显式受理——v1 不进）；数据级 post-load 断言（行数/键数核对）留 e2e 域不进产品面；manifest 机制随 B4 落地（全族换 manifest 形+e2e 重钉一次）。

### 4.6 引擎选型与实现调研（live 定案点）

**方言族（dialect family）机制**（第三方兼容实现的通用承接，pgvector:postgres embed 先例推广，2026-11-05 用户裁决）：**engine = 实现名**（kafka/redpanda 不合成命名），**方言族 = 连接与注入形态的复用单元**——Kafka 协议族（kafka+redpanda 共享 bootstrap+SASL 材料渲染与 `FLEETLY_PUB_*_BOOTSTRAP` 注入形态，`redpandaTemplate` 嵌 kafka 方言基座、只覆盖镜像/配置渲染/探针/版本线/knob 集）；目录消息加 `dialect` 字段（§2.3）同族互示兼容，客户端零改动切换。未来 valkey（redis 协议）/mariadb（mysql 协议）/ferretdb 等同机制可入（本批只进 redpanda，其余走引入流程）。

- **NATS = `nats:<ver>-alpine`**：默认 scratch 变体无 shell（无 exec 探针/无工具）；alpine 带 busybox wget → `Probe() = ["wget","-q","-O","/dev/null","http://127.0.0.1:8222/healthz"]`（-m 8222 监控口；「不依赖 nc 假设」探针律满足——wget 实证在场）。JetStream：`-js -sd /data`（DataTarget=`/data`）。
- **Kafka = `apache/kafka:<ver>`**：KRaft 缺省、`KAFKA_*` env→server.properties 翻译（`KAFKA_PROCESS_ROLES=broker,controller` 单节点 combined、`CLUSTER_ID` 自动 format）；探针 `kafka-broker-api-versions --bootstrap-server localhost:9092`——**探针/listener 分面**（9092 开 SASL 后无凭证探针被拒：加内部明文 listener 专供探针，B6 live 定案）；DataTarget=`/var/lib/kafka/data`（KRaft meta+log 同卷，`log.dirs`）；SASL 机制取舍（评审 F-14）：**PLAIN vs SCRAM-SHA-256 二选一 B6 live 定案，倾向 SCRAM**（免证书且凭证不过明文，KRaft 3.5+ 住元数据）；传输 PLAINTEXT 内网口径（TLS 内网不现实——与 zot 明文口径同文化）；bitnami 排除（Broadcom 变动荡）。
- **Redpanda = `redpandadata/redpanda:v26.x`**（Kafka 协议族第三方实现，2026-11-05 用户裁决入值域）：native 单二进制无 JVM 堆（`--memory` 显式配——资源画像远轻于 apache/kafka）。**配置形态（红队 A4 纠偏）**：`--mode dev-container` 是官方明示「不推荐生产」的开发预设——**生产形态由平台完整渲染配置**（不用 dev 模式；dev 默认逐项收编，`auto_create_topics=false` 强制渲染——owned 键闭集外的平台默认收编）。**探针/listener 分面同 kafka**（红队 A4：SASL 启用后 `rpk cluster info` 无凭证被拒——「无分面问题」断言撤销；三选一 B6b live 定案：admin API 9644 探测/带凭证 rpk〔凭证入材料〕/内部明文 listener 专供探针）。**方言基座继承律**：基座只含连接/注入形态——**Dump/Load/Apply/Rotate Spec 必须各自覆盖或显式声明不继承**（redpanda 镜像无 kafka-configs.sh，ApplySpec 不可继承——redpanda dynamic 面首批挂账或走 rpk 自有配置面，红队 A15b）；备份同 kafka 挂账（backup=false⇒migrate=false）；knob 集自持（memory/smp 等）。**许可 = BSL 1.1**（Additional Use Grant 禁 Streaming/Queuing Service 形态；**Change Date 各版发布起 4 年逐版转 Apache 2.0**——披露完整化，红队 C-AT-06）：托管合规裁定归运营方，目录 `license_note` 显著露出，代码面不设限。
- **silo = `pgsty/silo:RELEASE.…`**：e2e 既有假端点镜像转正；S3 API 面 + root AK/SK env；正式选型 B5 落定（备选 RustFS 维持 ADR-0042 挂账不吸收）。**许可 = AGPLv3**（MinIO fork 继承——**目录 license_note 全引擎化**披露，红队 C-AT-06：AGPL 网络条款对「平台替用户跑」形态的审视通常比 BSL 用例限制更敏感，运营方裁定）。**live 定案（B5，2026-11-07 本机 docker 实测 RELEASE.2026-10-26T00-00-00Z，digest 同日 Docker Hub index）**：①健康面 = MinIO 系 `/minio/health/ready`（9000 S3 口）实测 200；②**镜像内有 curl 无 wget**（基础变体 bash+coreutils+curl+mcli；distroless 变体无 shell 不可 exec 探针——选基础变体的全部理由，nats scratch→alpine 同款取舍）→ 探针 `curl -sf -o /dev/null http://127.0.0.1:9000/minio/health/ready`；③DataTarget=`/data`（`server /data` 起服内生 .minio.sys）；④root 凭证 env = `MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD` 实测生效（user ≥3 字符/password ≥8——平台 AK=fleetly、SK=hex 48 满足）；⑤Workload.Command 在 swarm/k3s 皆 entrypoint 覆盖语义——`silo server /data` 直跑实测起服（entrypoint 脚本只做 _FILE 展开/降权，绕过无损）；⑥控制台口不传 `--console-address` 时随机口，不投影；⑦S3 roundtrip：他容器经网以 root AK/SK `mcli mb/cp/cat` 实测通过（镜像自带 mcli = mc fork——e2e/探针 App 工具面零额外镜像）。版本 ID 取 RELEASE 年月短名 `2026-10`（设计稿 `2026-09` 建议值随上游 RELEASE 浮动重钉，Default=全最新）。**凭证形态结构性推论**：SK 住 Workload env ⇒ 收敛环每拍解密（≤4 行/项目配额，age 解封 µs 级可接受）+ 投影指纹落行/memo 前取 sha256 摘要（明文永不进 bucket_stores 行）；轮换 = 指纹变 → Generation 推进 → stop-first 滚动（database/pubsub 族材料面轮换不推进 gen——本族 env 形态的结构性差异）。
- dbbrowser：新家族 fail-closed 拒（ADR-0051 注册表外语官既有拒；kafka-ui 重（Java）、nats 无轻量 web——browse 面挂账独立评估）。

### 4.7 子资源管理面（B8，2026-11-05 用户终裁随批进）

三族子资源 = **声明行 + admin 工具容器收敛**（对齐 ADR-0081/0082 严格声明式；草图在此，B8 批细化为独立验收锚）：

| 家族 | 子资源 | 行面（配置） | 收敛工具（admin 工具容器，按 engine 方言） |
|---|---|---|---|
| kafka 协议族（apache/redpanda） | Topic | partitions / retention.ms（**副本恒 1**——单 broker 事实，目录标注） | `kafka-topics.sh --alter` / `rpk topic` |
| nats | Stream | subjects / retention / storage / max_stream_size | `nats stream create/update` |
| silo | Bucket | 名 + 配额（如引擎支持） | S3 API（minio-go 控制面直连或工具容器） |

- **形态**：子资源表（project/父实例 id/name/配置——配置可变面走 Update 双档同律）；收敛挂 dataservices 卫星分族步的子资源段（对照行期望态 ensure：存在+配置匹配，否则创建/更新）；**删除 = 声明式 GC**（行删→收敛步删子资源——Topic/Stream 删除即数据删除：wide 档确认+断言不挂引用）。
- 写类 DECLARATION；预览=diff；配额 per 实例（建议 topics/streams 100、buckets 50，B8 定）；事件码只增（`topic.*`/`stream.*`/`bucket.*`）。
- **词汇**：Topic（kafka 族）/Stream（nats）词条随 B1 入册（ADR-0093 决策 4 翻案）；Bucket 由 Bucket Store 词条域内本义覆盖。CLI 子资源动词命名 B8 细化（`buckets` 组名已被 Bucket Store 实体占用，避免撞名）。
- e2e：三族建改删 roundtrip + 引用防呆断言。

**B8 落地回填（2026-11-07）**：

| 面 | 定案 |
|---|---|
| 域/存储 | `internal/domain/data/topic|stream|bucketresource`（行 = project/父实例 id/name/配置/status〔pending\|ready\|error，收敛段推进〕/软删）；migration 00042 三表 `topics`/`streams`/`bucket_resources`（bucket 词保留给实体域——表/包分名同律） |
| 可变面分档 | Topic：partitions（**只增不减**——方言层受理拒 + 引擎 InvalidPartitionsException 纵深）/ retention_ms（nil=不管理，引擎缺省）；Stream：subjects/max_stream_size 可变，**retention/storage 创建后不可变**（live：`nats stream update` 无该二旗标）；Bucket：quota_bytes 可变（nil=无配额） |
| 方言分派 | kafka：`kafka-topics.sh --create/--alter/--describe/--delete` + `kafka-configs.sh --entity-type topics`（describe 首行 PartitionCount/Configs 解析）；redpanda：`rpk topic create/describe/add-partitions/alter-config/delete`（**add-partitions -n 是增量**——live 实测 3 分区 add -n 5 得 8；describe 缺主题退出 0 且 SUMMARY 空 = 缺席判据）；**TopicAdminFace 不入 KafkaDialect 基座**（继承律「各自覆盖」半边结构性满足）；nats：nats-box `nats stream add --defaults`/`update -f`/`info --json`/`rm -f`；silo：镜像自带 mcli，root AK/SK 经 `MC_HOST_<alias>` env（工具容器无落盘配置） |
| silo quota live 探明 | **支持面在册**（pgsty/silo RELEASE.2026-10-26：`mcli quota set <b> --size <bytes>` 成功、`quota info` 回读 "hard quota of 954 MiB"、`quota clear` 撤销、无配额形 "has  quota of 0 B"）——v1 行面 = 名 + 可选 hard quota，**无挂账** |
| 收敛段 | dataservices 分族步尾段（pubsub 族步对 Topic/Stream、bucketstore 族步对 Bucket）：父非 running 整段跳过（status 门）→ 行集期望态签名进 subMemo（ensureFresh 端口同协议 60s 强制重放窗，行集变化即失配）→ 活跃行 探测→差异→创建/变更→复核→ready/error 落行 → tombstone 行 探测→删实况→硬删行（实况已缺席直接硬删）→ 失败退避（replayFails 同款窗） |
| RPC 归属 | **家族 Service 内增 RPC**（PubSubsService 14 面 + BucketStoresService 7 面），不立新 Service：词汇冻结 + authz scope 词表零扩（复用 pubsubs/bucketstores）；全部请求携父实例锚（pubsub_id/bucket_store_id）——冻结/授权归属解析走 KindPubSub/KindBucketStore 既有链零新 Kind。无 Resolve 面（子资源恒自父实例上下文寻址——CLI 名字经 List 父内匹配） |
| 确认面 | Update×3 = ConfirmableVerbs standard 档；Delete×3 = **wide 档**（删除即数据删除）；票经 PreviewUpdate*/PreviewDelete* 铸；digest 族前缀分立 `topic=`/`stream=`/`bucket=`（删除票 `*-del=` 绑行 id+名） |
| 「不挂引用断言」 | v1 子资源无 refs 通道（App 经家族实例 refs 注入，不按 topic/stream/bucket 粒度引用）→ 断言改「删除披露行」：PreviewDelete* 列 live 数据量——nats `state.messages/bytes`、silo `mcli du --json` 的 size/objects；kafka CLI 无 per-topic 数据量面 → 如实披露不可恢复性不虚标数据量 |
| 配额 | per 实例：topics 100 / streams 100 / buckets 50（按数不按权——§7.4 同一取舍） |
| 事件码 | +9：`topic.*`/`stream.*`/`bucket.*` × created/updated/deleted（父实例删除的级联行清理不落子资源事件——父 deleted 事件承载事实面） |
| CLI | 复合嵌套：`fleetly pubsubs topics …`/`fleetly pubsubs streams …`/`fleetly buckets bucket …`（**避撞律：单数 bucket**——复数 `buckets` 是 Bucket Store 实体组）；五动词 create/list/get/update/delete；golden 双形态 |
| Console | 父详情页子资源 tab（pubsubs：engine=nats → Streams，其余 → Topics；bucket stores：Buckets）——列表 + 创建对话框 + 删除 wide 确认（disclosure 进 Consequences 段）；Update 面 v1 在 CLI/API |
| 目录 | EngineCatalog 增 `note`（proto 只增）——kafka/redpanda「Topic 副本恒 1」单 broker 事实标注 |

## 5. resources 面

- `CreateDatabaseRequest.resources{cpu_millis, memory_mb}`（可选；缺省 nil=无限制=现状）；投影填 `capability.Resources` → swarm `NanoCPUs/MemoryBytes` Limits（k3s requests/limits 同构）。
- 聚合列 `cpu_millis INTEGER NULL`/`memory_mb INTEGER NULL`；Update 可变（static 语义=滚动生效；swarm 资源变更触发任务替换——内容等价律 ADR-0064 的 mod 副本外字段）。
- **OOM 诚实立场**：v1 缺省无限制（多库争抢靠配额与节点容量；硬 Limits 对 PG 共享内存/连接内存是 OOM 风险——用户设了就是用户裁定，文档警示随目录旋钮 Note 承载）。

## 6. 用户面

### 6.1 API

- DatabasesService：`CreateDatabase` 增 `version`/`resources`/`settings`；新 `UpdateDatabase`；`Database` 消息增 `version`(行值直读)/`resources`/`settings` 回显。proto 只增不破（buf breaking 不动）。
- PubSubsService/BucketStoresService：`Create/Get/Resolve/List/Delete/Update/RotatePassword/TriggerBackup/ListBackups/VerifyBackup`（ADR-0092 Resolve name 寻址同律；Browse 拒）；写类分桶 + 五守卫（idem/freeze/acceptance/grpc/gateway）+ authz 词表 `pubsubs`/`bucketstores` 资源。
- DataEnginesService：`ListDataEngines`（READ）。
- 事件码只增：`database.updated`、`pubsub.created/updated/deleted/credentials_rotated`、`bucket_store.created/updated/deleted/credentials_rotated`（rotate 事件与 `database.credentials_rotated` 先例对齐，评审 F-17）；errcode 只增 +1：`E_SETTINGS_APPLY_FAILED`（§3.3），其余复用（E_INVALID_ARGUMENT/E_NOT_FOUND/E_ALREADY_EXISTS/E_CONFLICT/E_QUOTA_EXCEEDED/E_SECRET_UNAVAILABLE）。

### 6.2 Console/CLI

- Console：databases 列表/详情扩 version 列与 Settings 编辑（目录驱动表单）；新 `/p/$projectId/pubsubs`、`/buckets` 页（databases 姐妹页，ListPage/详情英雄卡+tab 同形态——一页一壳律）；创建对话框引擎/版本/旋钮全目录渲染。
- CLI：`databases create --version --resources-cpu/--resources-memory --set k=v`、`databases update`、`engines list`、新 `pubsubs`/`buckets` 动词组（groups golden 扩容）；golden 双形态同批。

### 6.3 skills

- `database-provision` 先例：新增 `pubsub-provision`/`bucketstore-provision`（B4/B5 随批，守卫执法散文示例命令同律）。

## 7. 工程纪律（每批连坐）

1. **守卫**：TestImageDigestsPinned 扩版本矩阵全量（含新包）；TestPlatformOwnedKeysUnreachable（新）；TestDynamicKeysAbsentFromProjection（新）；TestTemplateFaces/TestBrowserDigestsPinned 同构扩 pubtemplate/buckettemplate；wording 禁词面（ADR-0093 批，含红队 B-AT-10 补分诊：dialect/manifest 机制词、数据服务机制 _Avoid_、catalog 豁免 ListRegistryCatalog/模板目录、heap_mb 自造键例外注）；metricsquery k3s pod 正则连坐；ADR-0081 分桶表扩行守卫；**结构真源四件随 B2e 同批修订**（architecture.md §2 分层树/终态架构 §3 归属表+§4.1 边表/engineSatellitePackages 守卫基线——ADR-0088「架构文档同批修订」纪律，红队 B-AT-12）。
2. **e2e**：dind-backup 矩阵 + nats（逐流备份恢复断言）+ pgvector 收编 + **manifest 断言**（多成员 verify/整集修剪/部分成功清理）；双车道断言段（dynamic 变更→容器 started-at 不变+生效值查证；static 变更→滚动发生）；`e2e:dind` 增版本矩阵段（同引擎 2 版本并建并写读；**豁免条款**：非 Default 镜像 job 内拉取并计入时长预算——与 §7.3 预拉口径由本条显式和解，红队 C-AT-03）。
3. **CI 预拉策略**：仅每引擎 Default 版本 + e2e 直接依赖镜像进 Pre-pull 清单（**新增五件**：kafka/redpanda/pgvector/nats-alpine/natsio-nats-box；红队 C-AT-03 量化 +50–90min runner 时长——分 job 并行消化，主张口径改为 runner 分钟数）；矩阵其余靠单测门禁；**非 Default 镜像的 e2e 版本矩阵段在 job 内拉取并计入时长预算**（§7.2 豁免条款）。
4. **配额（按数不按权的显式取舍，评审 F-16）**：databases 16（既有不动）；pubsubs 8；bucket_stores 4（受理位 E_QUOTA_EXCEEDED 同律；值可终裁调）——计数配额不感知资源画像（8 pubsubs 可全是 1G 堆的 kafka），真实约束靠 resources 上限与节点容量，不引入 per-引擎加权配额。
5. **proto/console**：触碰后 `console:gen && console:build` 同 commit；swagger/golden/dist 零漂移。
6. **门禁与 staging 策略（红队 C-AT-05 裁决）**：每批 `mise run test`（三 module -race）+`lint`+`generate:verify`；**仅 B2c（列 flag-day）与 B4a（manifest flag-day——旧单对象备份键孤儿化）走全量重建**（ADR-0092 序：旧 DB 移档→fresh install→应用栈重播），**B5/B6/B4b 新表新引擎走 goose 前向迁移+ADR-0015 升级序验证**（重建税从 4–6 次降为 2 次）；dogfood 数据处置（torchwood-pg 种子通道/备份跨代只读）进 runbook。proto 触碰批（B2c/B4b/B5/B6）`console:gen && console:build` 同 commit。

## 8. 批次与验收（对应 proposal §7）

| 批 | 验收锚（要点） |
|---|---|
| B1 | CONTEXT.md 词条/禁词落地；wording 守卫绿（双向保鲜） |
| B2a | 内核与版本化：enginetemplate 下沉（含**卷实例标记契约实体中性化**+ADR-0097 同批修订）；VersionSpec/绑版实例/Deprecated 单测；digest 门禁扩矩阵全量；digests.sh；PG DataTarget 父挂双代 live 实证 |
| B2b | knob schema：Knob/ValidateSettings+逐引擎 owned 键闭集；TestPlatformOwnedKeysUnreachable+TestDynamicKeysAbsentFromProjection |
| B2c | 存储与受理：三列+backups.engine_version 迁移；Create 受理；目录 RPC（dialect/license_note）golden；**ADR-0029 决策 1 修订注随批落**；**staging 全量重建（列 flag-day）** |
| B2d | Update 双车道：分流+ApplyDynamicSettings（utilityMu 互斥+memo 端口+失败 Forget 拉回+仅 running 重放+在滚拒）；签名第四段复合代理；E_SETTINGS_APPLY_FAILED |
| B2e | 卫星归一：dataservices 卫星+descriptor 下沉 domain+环表三条目（10→12，ring dead 原样执法）；**明示漂移清单验收**；结构真源四件 |
| B3 | CLI/Console 全量换装 golden；目录驱动表单 vitest；`DATABASE_ENGINES` 退役 |
| B4a | manifest 机制：全族备份换 manifest 形（物理约束四条随批：逐成员串行/独立预算/manifest 最后写+失败清理/能力位门控；Utility 多成员输入契约）；e2e 重钉；**staging 全量重建（manifest flag-day）** |
| B4b | NATS 家族全链：natsio/nats-box 钦定+pubtemplate+聚合接入 descriptor+连接注入+CLI/Console/skills+逐流备份恢复（memory 流拒面）+双车道 e2e+staging 前向迁移走查 |
| B5 | silo 全链（AGPLv3 license_note）+ AK/SK 注入 + e2e（S3 客户端读写 roundtrip）+ staging 前向迁移走查 |
| B6a | apache/kafka 全链：KRaft 模板+SASL（SCRAM 倾向）+探针 listener 分面+kafka-configs.sh 动态面+备份拒+旋钮集 |
| B6b | redpanda 全链：完整配置渲染（非 dev 模式+auto_create_topics 收编）+探针三选一+ApplySpec 不继承声明+旋钮集+两实现 roundtrip e2e |
| B7 | 收尾批：**`databases upgrade` 编排向导**（预览=diff+断连窗+容量披露+purge 清单→wide 确认→五步断点可重入）+ **`volumes purge` 动词** + e2e 矩阵收口 + checklist 回填 + 升级序 runbook（手工序=向导验金石） |
| B8 | 子资源管理面（2026-11-05 用户终裁随批）：Topic/Stream/Bucket 声明行+admin 工具容器收敛（§4.7）+配额+CLI/Console 子面+三族建改删 e2e roundtrip |

## 9. 风险与挂账

| 项 | 状态 |
|---|---|
| NATS tgz 逐流备份/空流 restore 语义 | B4 live 定案 |
| Kafka 凭证轮换方言（JAAS 面） | B6 评估，倾向挂账 |
| redis dynamic 自持久（卷内 include + CONFIG REWRITE） | 挂账后手（首批全 static） |
| Kafka heap 与 resources 的联动语义 | B6 立法三分支：两者皆设→heap ≤ memory×0.7；仅设 memory→渲染期默认 heap=memory×0.5（apache/kafka 缺省 1G 堆会 OOMKill 循环，评审 F-13）；仅设 heap→放行 |
| 子资源管理面（topics/streams/buckets） | proposal §8.1 待终裁 |
| `databases upgrade` 向导 | 挂账（runbook 先行） |
| Browse 面（kafka/nats/silo） | 挂账独立评估 |
| 版本矩阵 N×M 维护负担 | SOP + digests.sh 承载；Default-only e2e 控 CI |
| 协同：卷键 ULID 化（ADR-0097 **已落地** 8da1255d） | 直接采用；实例标记契约升 enginetemplate（B2a 随批，ADR-0097 同批修订「dbtemplate 拥有」句） |
| 三成本主张注价（红队 C-AT-04） | 增版本=一小 commit+一次人工走查（目录 golden 是热 golden）；第四家族=一个 B5 级整批（3–5k LOC），「近零成本」仅指机制面 |
| B5.5 债主正名（红队 B-AT-07） | compose label 通道引 ADR-0074 external 声明律；真 B5.5（裸 postgres/redis 镜像→建议托管形态告警）单列挂账 B3 |

## 10. 交叉评审回填（2026-11-04）

双侧重独立评审收官（机制/收敛语义侧 + 运营/安全/词汇侧，审查变体=读主线文档独立审查）：**无 P0；P1×1、P2×9、P3×12**。全部裁决（F 编号=机制侧、F-编号=运营侧）：

| 发现 | 裁决 |
|---|---|
| F1（P1）static 车道被 memo 短路截断（签名不含材料字节，conf 类旋钮永不重下发） | **采纳**：签名扩第四段=MaterialsFingerprint（§3.2）；Generation 不随材料推进（RotatePassword 先例）；B2 验收锚加 fake Runtime 真实重发断言 |
| F2 dynamic 键泄漏进投影 env（kafka env 翻译最易错） | **采纳**：喂入律结构性保证（§3.2/§4.3 只喂 static 子集）+ TestDynamicKeysAbsentFromProjection 守卫 |
| F3/F-02 ProcessSpec 字段号 17/18 已被占用 | **采纳**：改 19/20（§4.4） |
| F4 RestoreCompat oldVersion 无数据源 | **采纳**：backups 行加 `engine_version` 列（§2.4；migration 只加列，旧行空值=同引擎放行） |
| F5 同名重建序撞卷保留语义 | **采纳**：升级正典显式含删卷步+异源标记防呆层（§2.4） |
| F6 dynamic memo 协议三处未定义 | **采纳**：AppliedDigest 所有权/时机/崩溃窗/执行序立法（§3.3；回退语义=行单真源的正确代价，如实记录） |
| F7 Update=DECLARATION 与 Rotate=ACTION 分界未论证 | **采纳**：ADR-0095 决策 9 补分界论证 |
| F8 apply 失败码通用化丢诊断面 | **采纳**：新码 E_SETTINGS_APPLY_FAILED（errcode 只增；§3.3/§6.1） |
| F9 增轴连带面清单 | **采纳**：连坐清单入 §4.2（7 文件 39 处+composeintake 白名单三处） |
| F10 弃用移除内部矛盾 | **采纳**：移除以存量清零为前提，防御路径仅兜底（§2.6） |
| F11 PG 现模板 pgdata 子目录措辞 | **采纳**：§2.5 对齐现状（父挂已是现状，仅 PGDATA 一处绑版分叉） |
| F-01 env 前缀 <N>/<NAME> 并存 | **采纳**：统一 <NAME>（§4.4；ADR-0093 与仓内现状是正源） |
| F-03 ADR-0014 A.2 追认未扩新 env 族 | **采纳**：ADR-0096 决策 5 显式扩追认（silo SECRET_KEY 暴露面 > DB 密码，必须落纸） |
| F-04 owned 键清单漏项 | **采纳**：§3.1 补全（archive_command/restore_command/dynamic_library_path/local_preload_libraries/include/rename-command/super.users/listener.security.protocol.map/system_account 等） |
| F-05 弃用版灾备矛盾 | **采纳**：弃用=拒全新建、同版本 restore 同名重建放行（灾备义，§2.5/§2.6） |
| F-06 非 Default 版本零真实启动 | **采纳**：SOP 增「版本轮转走查」（§2.6 条 5，随引入批次 staging 冒烟） |
| F-07 silo root 与 zot 凭证域倒退 | **采纳**：取舍论证入 §4.4 + per-App service account 挂 B5 live 定案 |
| F-08 ADR-0093 状态行歧义 | **采纳**：词条随 B1 批入册（状态行改） |
| F-09 zeabur B5.2/B5.5 状态改记时点 | **采纳**：改为随 B2/B5 批注记（proposal §10） |
| F-10 §2.3 悬空引用 | **采纳**：改引 ADR-0095 决策 10 |
| F-11 批号与并行提案撞名 | **采纳**：协同句写全称（proposal §10） |
| F-12 ADR-0093 缩写 Avoid 自打 | **采纳**：钦定短形豁免句（db- 先例同款） |
| F-13 kafka heap 单设分支 | **采纳**：B6 三分支联动（§9） |
| F-14 PLAIN vs SCRAM 未议+探针 listener | **采纳**：倾向 SCRAM+探针 listener 分面，B6 live 定案（§4.6/ADR-0096 决策 7） |
| F-15 目录 scope 语义错位 | **移入终裁项**（proposal §8.4；B2 按 authz 面能力定案） |
| F-16 配额按数不按权 | **采纳**：显式取舍注记（§7.4） |
| F-17 新家族缺 rotate 事件 | **采纳**：各家族 +credentials_rotated（§6.1） |
| F-18 B1 禁词分诊预命中 | **采纳**：minio/queue 预点名进 skipped（ADR-0093 后果节） |

**词汇面前置判断（评审核实）**：文档批先于 B1 提交**不触发门禁红**（wording 守卫扫 Go 三 module+proto+skills+console，不扫 docs/；`bucket` 现为 skipped 人工评审词）——B1 必须同 commit 带 CONTEXT.md 词条+守卫面（bucket 移出 skipped、新禁词全量分诊，minio/queue 预点名进 skipped）。

**修订（2026-11-05，用户追问促成）**：独立三环 → 统一托管服务机制（初版落「单卫星单环」）→ **红队 §11 二次翻案为「单卫星包+环表三条目」**；「三轨一基」立法（数据服务与 App 共享 L1 公共运行保障、仅不取 App 编排面）。

## 11. 红队对抗审查回填（2026-11-05）

用户授命三路红队（A 机制运行时攻击 / B 立法与终态冲突 / C 落地成本）对已提交文档批（6da54d29+b2ff8a36+0654c1c1）做对抗性审查，授命翻案既往裁决。结果：**无设计推翻级发现；A 队结论「机制主体站得住，四处 P1 须修订」；B 队两项翻案成立；C 队三处排期重排**。全部裁决：

| 发现 | 裁决 |
|---|---|
| A1（P1）升级正典：删卷无执行原语（只删行不删物理卷→每次升级泄漏整卷）；恢复源未指定→①~②间写入静默丢失；③期间 Secret 指死 host 断连=恢复时长 | **采纳**：②前强制新备份+Verify、③恢复源显式、断连窗披露、**`volumes purge` 动词 B7 立项**（落地前 runbook 手工序标注）；动机改写=ULID 键已消除复用、删卷步真动机是容量回收（§2.4） |
| A2/A3（P1）manifest 撞物理现实：NATS 不可流式（落盘峰值顶 manager 根分区）、15m 单飞硬顶=大体量永久失败、部分成功孤儿无 GC | **采纳**：物理约束三条立法（§4.5——逐成员串行峰值=最大单成员/逐成员独立预算/manifest 最后写+失败清理+List 对账）；Utility 多成员契约进 B4a 锚 |
| A4（P1）redpanda 探针断言与 SASL 待裁自相矛盾；dev-container 生产默认 auto_create_topics 不可关 | **采纳**：断言撤销；生产=平台完整渲染配置+auto_create_topics=false 收编；探针三选一 B6b live 定案（§4.6） |
| A5（翻案）F1「永不重下发」不成立——ensureFresh 有 60s 强制重放兜底；第四段字面 MaterialsFingerprint 需每拍解密凭证 | **采纳**：F1 降 P2；第四段=密文摘要+static 子集摘要复合代理（§3.2；ADR-0095 决策 4 同步） |
| A6/A7/A15a dynamic 竞态族：在滚窗 apply 连 VIP 拒、新行重放穿 StartPeriod、Update 无单飞锁 | **采纳**：受理位 status!=running 拒+重放仅 running+失败退避（§3.3；与 B-击一互斥立法合并） |
| A8/B-击二 单环 recover 废 ring dead 教义+假二分 | **采纳翻案**：**单卫星包+环表三条目**（10→12，ring dead 原样执法、每族一写者——§4.1/§4.3；ADR-0096 决策 1 二次修订） |
| A9 schedulePass 无能力位门控→kafka 每 24h 铸必败行 | **采纳**：DataCapabilities.backup 过滤+受理禁配 interval（§4.5） |
| A10/B-AT-13/14 RestoreCompat 空值允许 major 降级+pgvector 复合偏序未定义 | **采纳**：空值=放行但标注「版本未记录降级风险自负」；pgvector 分轴比较立法（§2.4；ADR-0094 决策 5 同步） |
| A11 灾备半删态 footgun（E_CONFLICT 文案引导删卷→备份坏才发现） | **采纳**：suggestion 加「先 VerifyBackup 再处置卷」（B2c 文案面） |
| A12/A14 材料滚动期 status 采信安全（好消息）；单次混合 Update=一次滚动（好消息） | 记档；「分次 update=分次重启」入 CLI/Console 提示（B3） |
| A15b 方言基座未含 ApplySpec——redpanda 镜像无 kafka-configs.sh | **采纳**：基座继承律=Dump/Load/Apply/Rotate 各自覆盖或显式声明不继承（§4.6） |
| A13/B-AT-05/C-AT-02 环表算术三文档互斥（10→10/10→11/10→13 无一自洽）+proposal §10 陈旧句 | **采纳**：三环条目定形后统一口径 10→12；陈旧句随本批清除 |
| B-击一（P1）dynamic 凿穿单写原语：多驱动者无互斥/memo 越端口写/半应用黑洞 | **采纳**：utilityMu per-entity 互斥+memo 经 engineports 端口+失败强制 Forget 拉回（§3.3；ADR-0095 决策 5/6 同步） |
| B-击三（P1）新家族缺卷实例标记门（ADR-0097 刚修的缺陷将原样复发） | **采纳**：标记契约升 enginetemplate 内核（实体中性字段）+ADR-0097 同批修订+B4b/B5 异源门断言（§3.3/§4.1） |
| B-AT-01 descriptor 归属撞卫星零硬边守卫 | **采纳**：注册表本体下沉 domain（§4.1） |
| B-AT-06 ADR-0029 修订不走载体 | **采纳**：B2c 随批在 ADR-0029 决策 1 落修订注（本批先落，见 0029 修订记录） |
| B-AT-07 B5.5 记错债主 | **采纳**：label 通道改引 ADR-0074；真 B5.5（裸镜像告警）挂账 B3（§9） |
| B-AT-08 dynamic 无持续对照、声明式承诺过强 | **采纳**：如实降格为事件触发收敛+负空间声明（§3.3；ADR-0095 决策 6 同步） |
| B-AT-09 「三轨一基」撞 ADR-0029 driftScan 豁免+新铸词未入册 | **采纳**：L1 拆「稳态看门狗（共享）/spec 对照 drift（0029 豁免维持）」（§4.1 注）；词条随 B1 入册 |
| B-AT-10 词汇漏网捆（dialect/heap_mb 自造键违反自家原生词律/manifest/数据服务机制 _Avoid_/catalog 误伤 ListRegistryCatalog） | **采纳**：B1 分诊清单扩（§7.1）；heap_mb 以「平台 owned 面安全子集」例外注入 ADR-0095 决策 2 |
| B-AT-11 目录 scope 既立法又开放 | **采纳**：ADR-0094 决策 4 改「暂定」+终裁指针（§2.3） |
| B-AT-12 结构真源四件未列 | **采纳**：B2e 连坐清单（§7.1） |
| B-AT-15 B5.2 应记部分采纳 | **采纳**：zeabur 文档回改时注「部分采纳（目录形态；内存画像/tested version 未采纳）」 |
| B-AT-16 并行措辞过时（0097/0098 已落地） | **采纳**：措辞改「已落地，直接采用」（§9/proposal §10） |
| B-AT-17 归一与扩段同批互咬 | **采纳**：B2 拆五批天然分离（归一在 B2e、扩段在 B2d） |
| C-AT-01（P0）B2 是五合一 commit（100–150 文件/+6–9k LOC） | **采纳**：拆 B2a-e 五批（§8；checklist F5.2 拆行） |
| C-AT-02（P0）「零漂移」不可证且自相矛盾 | **采纳**：改「明示漂移清单下的稳态等价」（§4.3；ADR-0096 验收锚同步） |
| C-AT-03（P0→P1）e2e/CI 膨胀+§7.2/§7.3 互斥 | **采纳**：豁免条款+预拉五件+runner 分钟数口径（§7.2/§7.3） |
| C-AT-04 三成本主张偏乐观 | **采纳**：注价入 §9（热 golden/一小 commit+走查/B5 级整批） |
| C-AT-05（P0）staging 重建次数未裁决（B4 manifest 是隐藏 flag-day） | **采纳**：仅 B2c/B4a 重建、B4b/B5/B6 前向迁移（§7.6） |
| C-AT-06 外部事实三雷：silo=AGPLv3 零披露、synadia/nats-box 死命名空间、redpanda BSL Change Date 披露不完整；NATS memory 流不可快照 | **采纳**：license_note 全引擎化（silo AGPLv3/redpanda BSL+4 年 Change Date）；钉 natsio/nats-box；memory 流受理拒（§4.5/§4.6） |
| C-AT-07 21 新 RPC≈160+守卫登记行未计价；B6 双实现 1.5× | **采纳**：B6 拆 B6a/B6b（§8）；批次守卫连坐条数随批清单化 |
| C-AT-08 文档批未收敛 | **采纳**：本批一并回正 |
