# ADR-0093：托管数据服务词汇批——Bucket Store/PubSub 入册与 bucket 移置

日期：2026-11-04 立法（提案与裁决记录见 `docs/proposals/2026-11-04-managed-data-services.md` §3；设计 `docs/design/2026-11-04-managed-data-services.md` §1）｜ 状态：Accepted（词条/禁词随 B1 批入册——本批未动 CONTEXT.md）

## 背景

托管数据服务从单一 Database 家族扩为三家族（Database/Bucket Store/PubSub）。`bucket` 是 ObjectStore 词条 _Avoid_ 禁词（S3 桶泛称禁；S3 配置字段本义豁免——ADR-0039/0042 在案）；PubSub/topic/queue 全仓零登记，`queue` 已被部署 admission 队列语义占用（ADR-0016）。ADR-0007 词汇冻结律：新实体先立词条，禁词冲突走显式移置而非局部混用。

## 决策

1. **PubSub 入册**：托管消息服务实例（nats/kafka 在册引擎；Project 级一等实体，聚合/收敛环/凭证与 Database 同构）。_Avoid_: message bus, broker(作实体名), queue(消息义), MQ。
2. **Bucket Store 入册**：托管 S3 兼容对象存储实例（silo 在册引擎）。_Avoid_: object storage(作实体名), minio(泛指), S3 server。
3. **bucket 移置**：`bucket` 自 ObjectStore 词条 _Avoid_ 表移除——该词让渡给 Bucket Store 词条域内本义（实体内 bucket 子资源、旋钮/文档/Console 文案）；ObjectStore 词条保留 `storage`/`S3(泛指)` 禁令不变，S3 五元组配置字段豁免（ADR-0039/0042）原样有效。移置理由注记随词条携带。
4. **topic/subject 划界**：`topic`（kafka）/`subject`（nats）为引擎原生词按本义使用（第三方本义律）；平台级统称用「PubSub 子资源」；**Topic（kafka 族）/Stream（nats）词条随 B1 入册**（2026-11-05 用户终裁子资源面随批进 B8——原「不立 Topic 词条」翻案；Bucket 由 Bucket Store 词条域内本义覆盖）。`queue` 语义维持 ADR-0016 部署 admission 队列专属，消息义禁用。
5. **命名族**（ADR-0092 双段律与项目解析面铸名族同源）：CLI 动词组 `fleetly pubsubs`/`fleetly buckets`；Secret 前缀 `pubsub:`/`bucketstore:`；DNS `pub-<id>`/`bkt-<id>`；载体 `fleetly-pub-<name>-<id>`/`fleetly-bkt-<name>-<id>`；env 注入前缀 `FLEETLY_PUB_<NAME>_`/`FLEETLY_BKT_<NAME>_`。_Avoid_: msg/mq 等缩写进标识符（钦定短形 db/pub/bkt 除外——`db-` DNS 先例同款；Avoid 执的是未钦定缩写的私造）。
6. **家族词与机制词**：跨三家族统称「托管数据服务」（managed data services，文档用语非实体名）；**「数据服务机制」（Data Service Mechanism）入册引擎与分层节**（机制词非实体——机制原语/卫星域同款）：统一托管服务收敛机制（dataservices 单卫星单环 + family descriptor 注册表），三家族与未来家族是其实例（2026-11-05 用户裁决：底层统一、上层特化）；Database 词条值域收窄为 OLTP/KV/文档引擎（postgres/pgvector/mysql/mongo/redis），Kafka/silo 不得以 Database 称。**engine 名 = 实现名**（kafka、redpanda、valkey 各自入册，不造「kafka-compatible」类合成名；协议兼容性经目录 `dialect` 字段表达，非命名承载）。
7. **Engine Catalog（引擎目录）入册**：`GET /v1/data-engines` 自描述面（引擎+在册版本+默认+弃用+旋钮 schema 的服务端注册表投影，非存储实体）。_Avoid_: marketplace, catalog(泛指其他含义)。

## 后果

- CONTEXT.md 增 PubSub/Bucket Store/Engine Catalog 三词条、修订 Database/ObjectStore 两词条；wording 守卫禁词面同批扩（含 skills/散文/proto 全覆盖，双向保鲜律）。**分诊预点名（评审 F-18）**：`minio(泛指)` 与 `queue(消息义)` 进 skipped/豁免——minio-go 库 import（s3objectstore）不可更名、admission 域 queue 本义在册（CONTEXT.md Admission 词条「queue 满反馈」）。**红队 B-AT-10 补分诊**：dialect/manifest 机制词入册；「数据服务机制」_Avoid_ 补（与受管域 managed 分界）；`catalog` 豁免 ListRegistryCatalog/模板目录既有合法用法；`heap_mb` 为「平台 owned 面安全子集」自造键——例外显式登记于 ADR-0095 决策 2（不违反决策 3 原生词律的私造，是 owned 面暴露的显式例外）。
- `bucket` 移置后既有文案零违规面（现全仓 bucket 仅 S3 配置字段本义使用——B1 复核确认后移置生效）。
- 模板包名 pubsubtemplate/bucketstoretemplate/dataservicetemplate（共享内核）随本词汇批合法化；databasetemplate 词条（ADR-0045/0050）不动。

## 验收锚

- [ ] B1：CONTEXT.md 词条/禁词落地 + wording 守卫绿（新增禁词全仓零命中，例外条目带理由）。
- [ ] B1：`bucket` 移置后全仓扫描零新违规（S3 配置字段豁免面白名单在册）。
- [ ] 命名族落地：B2-B5 各批载体/DNS/Secret/CLI/env 前缀与词条一致（golden + metricsquery 正则连坐）。
