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
  family, engine, versions[]{id, default, deprecated, note},
  knobs[]{...旋钮 schema，ADR-0095}, port, capabilities{backup, browse, restore_compat…}}
```

- 新 `DataEnginesService`（structure.proto），READ 桶，scope `databases:read`（目录不敏感；纯 pubsubs/buckets Token 渲染表单的可达性矛盾→**scope 终裁项**：维持 databases:read / 认证即读 / 三家族 read 任一，见 proposal §8.4，B2 按 authz 面能力定案）。
- **Console/CLI 唯一取值面**：`console/src/kit/databases.ts` 的 `DATABASE_ENGINES` 硬编码退役，创建对话框引擎/版本/旋钮全从目录渲染；CLI `engines list`/`config describe`。zeabur B5.2「catalog 形态」收口于此。
- 目录是**投影非存储**：服务端从三模板包注册表现场装配，零缓存零漂移。

### 2.4 升级路径正典

1. `databases create <new-name> --engine pg --version 18 --restore-from-backup <bk_…>`：受理位做 `RestoreCompat(engine, oldVersion, newVersion)` 检查——**oldVersion 取自 backups 行新增 `engine_version` 列**（新备份受理时落值；migration 只加列，旧备份行空值=同引擎放行不比版本）。模板层声明规则：首版 = 同引擎同版本放行、major 只许升不许降（pg 旧→新 OK、新→旧拒）、跨引擎既有拒不变。
2. 数据落地、验证后：删旧库（Secret 保留义不变）→ **显式处置旧数据卷**（升级序必含删卷步——卷名=库名公式，同名重建会复用旧卷、旧 PGDATA 撞新版本首启；并行提案的卷内实例标记将 fail-closed 拒异源复用，构成防呆层；处置序入 runbook）→ **同名重建序**：旧名重建新版本库 + 恢复 → Secret `database:<name>` 值覆盖新 host（`db-<新id>`）→ 引用方（`secret_refs`/`database_refs`）App 重新部署后生效——ADR-0029 决策 8「同名重建=新 ULID+新凭证（旧 Secret 值被覆盖）」既有语义的自然延伸，无需新机制。
3. CLI `databases upgrade` 编排向导：**挂账**（proposal §8.2——destructive 面先跑通手工序，runbook 承载）。

### 2.5 首批在册矩阵（建议值；digest B2 用脚本钉）

| 引擎 | 在册 version ID | tag 形 | Default | 备注 |
|---|---|---|---|---|
| postgres | `16` / `17` / `18` | 16/17-bookworm、18-trixie | `17` | **PG18 起镜像默认 PGDATA 迁版本化路径 `18/docker`**：DataTarget 维持全代父挂 `/var/lib/postgresql`（**现模板已是父挂 + `PGDATA=/var/lib/postgresql/pgdata` 子目录**，TestTemplateFaces 钉板）——16/17 维持 pgdata 子目录、18 走镜像默认，仅 PGDATA 一处由 adapter 绑版分叉；live 定案：B2 live_test 双代实证 |
| pgvector | `0.8-pg17` / `0.8-pg18` | 0.8.x-pgNN-suite | `0.8-pg17` | vector minor 平台钉版；tag 精确版 B2 定 |
| mysql | `8.4` / `9` | 8.4 / 9.x | `8.4` | 8.0 已 EOL（2026-04）不入册；9=innovation track 短在册窗 |
| redis | `7.4` / `8.2` | 7.4 / 8.2-x | `7.4` | **8.0 于 2026-12-01 EOL 不入册**；7.2 临 EOL 不入册 |
| mongo | `8.0` / `8.2` | 8.0 / 8.2-x | `8.0` | 7.0 不入册（无兼容义务，收敛在册面） |
| nats | `2.10` / `2.11` | alpine 变体 | `2.11` | §4.6 选型 |
| kafka | `4.0` / `4.1` | apache/kafka | `4.1` | 3.x 不入册（新平台无历史包袱） |
| silo | `2026-09` | RELEASE.2026-09-16T… | 单版 | 单版引擎（VersionSpec 仅一枚，Default 恒真） |

在册政策：每引擎 2-3 条；新 major 引入 = 注册表 entry + digest 脚本 + 目录自动出现（零发布成本）；旧 major 转 Deprecated = 注册表改标 + 目录标灰 + Create 拒**全新建**（**同版本带 restore 的同名重建放行——灾备义**，否则弃用版卷丢失时灾备被迫 major 升级；存量继续跑、备份/恢复/轮换全语义保持）；移除 = 待存量清零后的 runbook 动作（时间线见 §2.6）。

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
**环侧 memo 签名必须扩段（评审 F1，P1）**：现签名 = 投影指纹+寻址域+凭证密文摘要，**不含材料字节**——digest bump 之所以同机械成立是因为 Image 在投影指纹里、rotate 成立是因为密文摘要进签名，conf 渲染类 static 旋钮在两者之外：不扩签名则短路早退、材料永不重下发。**裁决：memo 签名增补第四段 = `MaterialsFingerprint`（engineports 既有单源）**；Generation 不随材料推进（RotatePassword 先例：材料变更不推进 gen 即触发滚动替换）。B2 验收锚必须断言「static-only 变更 → fake Runtime 收到 Ensure 真实重发（材料含新值）」，不是只断言行更新+Kick。

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
- **幂等/声明式律**：行 = 期望态单真源。重放（未变声明）= ValidateSettings 通过 + dynamic 子集 diff 为空 → 零扰动 no-op。**重放检查在 memo 短路判定之前执行**（短路拍直接早退，不跳过重放）。实例重建判据与并行提案「卷内实例标记」合流（proposal §10）。
- **dynamic memo 协议（评审 F6 立法）**：per-entity memo 增 `AppliedDigest`（已应用 dynamic 子集摘要）。所有权与写入时机 = Update 路径在 dynamic apply 成功后、行落库同窗口内写入（apply→memo→行三连；memo 是进程内表随重启丢失——重启后按「行摘要≠memo（空）」触发一次幂等重放，无害）。**已应用未落行窗**（apply 成功后崩溃）：恢复后以**行值**为期望态幂等重放，已应用的孤儿值被拉回行值——用户可能收到过成功响应后遭回退；与 RotatePassword 崩溃窗（rotate.go:1-12 注释在案）同构，是行单真源的正确代价，响应文案与 runbook 如实记录。
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
| L3 托管服务机制 | **`internal/application/dataservices` 单卫星单环 + family descriptor 注册表** | 通用收敛生命周期：行→投影→材料→Ensure→观测→status 推进；Update 双车道分流；备份排程/执行/修剪/恢复调度；凭证铸造/轮换调度；配额；级联删除；kick 分族 |
| L2 家族/引擎声明 | dbtemplate/pubtemplate/buckettemplate + enginetemplate 共享内核 | 版本矩阵、knob schema、材料渲染、Backup/Restore/Rotate/Apply Spec、连接材料方言、目录装配 |
| L1 公共运行保障（**与 App 同基**） | capability.Workload + Runtime.Ensure（唯一写动词）+ Generation/内容等价（ADR-0064）+ 材料纪律（ADR-0014）+ Volume/Pinning/stop-first + Network/Egress + 观测/稳态看门狗 + drift/posture（ADR-0082）+ 事件/审计 kernel | 一切载体级保障 |
| L0 Provider 特化 | swarm/k3s 翻译 | 载体名/标签/卷/网络原语 |

**三轨一基**（对「最底层是否与 App 共基」的立法答案）：App 部署轨（Deployment 状态机/Revision）、受管域轨（ADR-0004）、数据服务轨（ADR-0029 第三形态）**共享 L1 全部保障原语**；数据服务不取的仅是 App 编排面——Deployment 状态机、Revision 冻结、Deployment Strategy（词条明定不适用）、Admission 队列：行即期望态、per-row Generation 原位收敛，无「旧→新受监督迁移」编排（第三形态的裁决本体）。

**family descriptor**（受管域「一个通用 reconciler + N 个 Describe()」〔ADR-0004〕的用户域对称形态）：每家族注册 {域聚合 Repo、模板包、凭证/连接方言、备份能力、配额、事件码族}；卫星单环迭代三家族行集。**环表 10→11**（独立环取舍反转，2026-11-05：三份环骨架复制违背机制单源方向；故障域隔离改由 per-family 子步 recover 承载——家族步 panic 记账并继续他族，环 skeleton 仅基础设施级 panic 转 ring dead；代价如实：单环家族间收敛耦合，换取生命周期单源与第四家族近零成本）。kick 面保持分族（KickDatabases/KickPubSubs/KickBucketStores → 同环分族唤醒）。

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

- 收敛环 = **dataservices 单卫星单环**（§4.1 L3；ADR-0088 生长律登记：环表 10→11，`engine/construct.go`）；既有 databases 卫星迁入 descriptor 形态是 **B2 机制统一步**（行为/golden 零漂移为验收），pubsubs/bucketstores 作为 descriptor 实例二/三接入；备份环（backups 卫星）行源同款经 descriptor 归一跨族（engine 空间已按模板泛化）。CRUD 服务端：三 proto 服务面不变，gRPC 实现共享通用 handler core（descriptor 参数化——manageds 先例）。
- 投影：`Workload{ID=行ID, Process=engine, Image=tpl.Image(), Env/Command=tpl.Workload(staticOnly), Replicas:1, Ports=[Meta.Port], Healthcheck=tpl.Probe(), Volumes=[{VolumeID=名, Target=tpl.DataTarget()}], StopGrace, Addressing=[家族 DNS 名], Networks=项目活跃网}`（**喂入律：只喂 static 子集**，§3.2）——与 `databases/projection.go` 同构，共享 helper 下沉 application 层（`dataservices` 共享投影帮手包或 engineports，实施时定，倾向 engineports 旁挂）。

### 4.4 凭证与连接注入

- 铸造：`MintPassword` 同源；NATS = 平台合成用户（nats-server.conf users 段，声明式轮换=Secret 重写+载体重下发——redis 同款）；Kafka = SASL admin 用户（JAAS/listener 配置面，static 材料内，轮换=滚动；**Kafka 凭证轮换方言 B6 评估，倾向挂账**）；silo = root AK/SK（启动 env，轮换=滚动）——**单用户模式取舍（评审 F-07）**：root 全权限超管是 zot per-Project 凭证域先例（ADR-0056）的倒退；v1 接受论证 = 单 Project 独占实例（zot 是多 Project 共享）+ per-App service account（silo 是 MinIO 系 fork，IAM 策略面可支撑）挂 B5 live 定案；暴露面经 ADR-0014 A.2 显式追认与轮换=滚动补偿。
- Secret 值：`pubsub:<name>` = `ConnMaterial` 方言串；`bucketstore:<name>` = `endpoint|ak|sk` k=v 串（AK/SK 复合凭证）。值永不回显、指纹+名回显同律。
- 注入：App Spec `pubsub_refs`/`bucketstore_refs`（spec.proto ProcessSpec **字段 19/20**——17/18 已被 config_mounts/restart_seq 占用）→ `FLEETLY_PUB_<NAME>_{URL|BOOTSTRAP}/HOST/PORT/USER/PASSWORD`（NAME=引用名 UPPER_SNAKE，`FLEETLY_DB_<NAME>` 同律；nats 出 URL、kafka 出 BOOTSTRAP+MECHANISM）与 `FLEETLY_BKT_<NAME>_ENDPOINT/ACCESS_KEY/SECRET_KEY`；**明文 env 承载凭证 = ADR-0014 附录 A.2 追认范围显式扩至本两族 refs**（PUB_PASSWORD/BKT_SECRET_KEY 是长效凭证、暴露面大于可原地轮换的 DB 密码——论证落 ADR-0096 决策 5，不默示继承 database_refs）；`InjectDatabaseEnv` 同构扩（databaseenv.go 泛化为 refs 注入帮手）；compose label `fleetly.pubsubs`/`fleetly.bucketstores`（ADR-0074 external 声明律 + **intake 白名单显式入册**（§4.2 连坐清单）+ 告警建议——B5.5 收口）。
- `secret_refs` 文件注入通道复用（值=方言串直用）。

### 4.5 备份/恢复

| 引擎 | 备份 | 恢复 | 产物 |
|---|---|---|---|
| nats | `nats stream backup <stream> <out>.tgz` 逐流（工具容器 nats-box 钉版；流清单经 `nats stream info --json` 枚举） | `nats stream restore`（空流前提） | **每流一个 .tgz 对象**，键 `backups/<project>/<pubsubID>/<ts>-<id>/<stream>.tgz`（多流=多对象，Backup 台账行聚合；restore 全流循环）。KV bucket 是流（`KV_*`）天然覆盖。**live 定案 B4**：tgz 单档实测 + 空流 restore 语义 |
| kafka | **v1 显式无数据备份**（无官方 dump 工具；单 broker 流数据业界常态可重建）——BackupPolicy 不可配，目录 capabilities 标 `backup:false`，TriggerBackup 拒（E_INVALID_ARGUMENT 挂账文案） | — | — |
| silo | **v1 挂账**（数据冗余/外同步域，非 dump 契约对应物——2026-10-29 评估既定判断） | — | — |

### 4.6 引擎选型与实现调研（live 定案点）

- **NATS = `nats:<ver>-alpine`**：默认 scratch 变体无 shell（无 exec 探针/无工具）；alpine 带 busybox wget → `Probe() = ["wget","-q","-O","/dev/null","http://127.0.0.1:8222/healthz"]`（-m 8222 监控口；「不依赖 nc 假设」探针律满足——wget 实证在场）。JetStream：`-js -sd /data`（DataTarget=`/data`）。
- **Kafka = `apache/kafka:<ver>`**：KRaft 缺省、`KAFKA_*` env→server.properties 翻译（`KAFKA_PROCESS_ROLES=broker,controller` 单节点 combined、`CLUSTER_ID` 自动 format）；探针 `kafka-broker-api-versions --bootstrap-server localhost:9092`——**探针/listener 分面**（9092 开 SASL 后无凭证探针被拒：加内部明文 listener 专供探针，B6 live 定案）；DataTarget=`/var/lib/kafka/data`（KRaft meta+log 同卷，`log.dirs`）；SASL 机制取舍（评审 F-14）：**PLAIN vs SCRAM-SHA-256 二选一 B6 live 定案，倾向 SCRAM**（免证书且凭证不过明文，KRaft 3.5+ 住元数据）；传输 PLAINTEXT 内网口径（TLS 内网不现实——与 zot 明文口径同文化）；bitnami 排除（Broadcom 变动荡）。
- **silo = `pgsty/silo:RELEASE.…`**：e2e 既有假端点镜像转正；S3 API 面 + root AK/SK env；正式选型 B5 落定（备选 RustFS 维持 ADR-0042 挂账不吸收）。DataTarget/健康面 live 定。
- dbbrowser：新家族 fail-closed 拒（ADR-0051 注册表外语官既有拒；kafka-ui 重（Java）、nats 无轻量 web——browse 面挂账独立评估）。

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

1. **守卫**：TestImageDigestsPinned 扩版本矩阵全量（含新包）；TestPlatformOwnedKeysUnreachable（新）；TestTemplateFaces/TestBrowserDigestsPinned 同构扩 pubtemplate/buckettemplate；wording 禁词面（ADR-0093 批）；metricsquery k3s pod 正则连坐；ADR-0081 分桶表扩行守卫。
2. **e2e**：dind-backup 矩阵 + nats（逐流备份恢复断言）+ pgvector 收编；新增双车道断言段（dynamic 变更→容器 started-at 不变+生效值查证；static 变更→滚动发生）；`e2e:dind` 增版本矩阵段（同引擎 2 版本并建并写读）。
3. **CI 预拉策略**：仅每引擎 Default 版本 + e2e 直接依赖镜像进 Pre-pull 清单（矩阵其余靠单测门禁）。
4. **配额（按数不按权的显式取舍，评审 F-16）**：databases 16（既有不动）；pubsubs 8；bucket_stores 4（受理位 E_QUOTA_EXCEEDED 同律；值可终裁调）——计数配额不感知资源画像（8 pubsubs 可全是 1G 堆的 kafka），真实约束靠 resources 上限与节点容量，不引入 per-引擎加权配额。
5. **proto/console**：触碰后 `console:gen && console:build` 同 commit；swagger/golden/dist 零漂移。
6. **门禁**：每批 `mise run test`（三 module -race）+ `lint` + `generate:verify`；B2/B4-B6 e2e 局部；staging 全量重建验证（ADR-0092 序：旧 DB 移档 `.pre-<tag>` → fresh install → 应用栈重播 → 走查锚）。

## 8. 批次与验收（对应 proposal §7）

| 批 | 验收锚（要点） |
|---|---|
| B1 | CONTEXT.md 词条/禁词落地；wording 守卫绿（双向保鲜） |
| B2 | **dataservices 卫星归一**（databases 迁入 descriptor、环表 10→11、行为/golden 零漂移）；注册表版本化单测（绑版实例/Default/Deprecated 拒）；version/resources/settings 列迁移；Create/Update 受理+分流 golden；目录 RPC golden（含 knob schema）；digest 门禁扩容绿；digests.sh 可用；**PG DataTarget 挂父目录 live_test 双代实证** |
| B3 | CLI/Console 全量换装 golden；目录驱动表单 vitest；`DATABASE_ENGINES` 退役 |
| B4 | NATS 全链：fake Runtime 投影断言 + e2e（创建/连接/FLEETLY_PUB_* 注入/逐流备份恢复/dynamic-static 分车道）+ staging 实证 |
| B5 | silo 全链 + AK/SK 注入 + e2e（S3 客户端读写 roundtrip）+ staging 实证 |
| B6 | Kafka 全链 + SASL 连接 + 动态配置热应用断言 + 备份拒文案 + staging 实证 |
| B7 | e2e 矩阵收口 + checklist 回填 + runbook（升级序手工文档） |

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
| 协同：卷键 ULID 化（并行提案） | B2 合流点，先其落地则直接采用 |

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

**修订（2026-11-05，用户追问促成）**：独立三环 → **统一托管服务机制**（§4.1 分层：L3 dataservices 单卫星单环 + family descriptor，环表 10→11；独立环取舍反转记录于 ADR-0096 决策 1）；「三轨一基」立法（数据服务与 App 共享 L1 公共运行保障、仅不取 App 编排面）。
