# ADR-0096：PubSub 与 Bucket Store 家族——三轴分立、连接注入与备份诚实面

日期：2026-11-04 立法（提案 §6；设计 `docs/design/2026-11-04-managed-data-services.md` §4/§6）｜ 状态：Accepted（实施随 B4/B5/B6；ADR-0029 用户域第三形态的全量复制先例）

## 背景

Kafka/NATS/对象存储托管服务全仓零存在。用户裁决（2026-11-04）：三引擎全进（NATS→silo→Kafka 批次序）。Database 的用户域第三形态（ADR-0029 决策 3：收敛环投影、不进 Deployment 状态机、不进 fleetly/system 受管域）证明可复制；本 ADR 裁决新家族的实体/轴/连接/凭证/备份面。

## 决策

1. **统一托管服务机制 + 家族特化（2026-11-05 用户裁决：底层统一、上层特化）**：三聚合三表三 RPC 面不变（词汇冻结，`pubsubs`/`bucket_stores` 表与 Database 同形——含 ADR-0094 version 列、ADR-0095 settings/resources 列与 Update 动词；域包 `internal/domain/data/pubsub|bucketstore/`，Repo 接口随聚合，ADR-0075 律）；**机制层归一** = `internal/application/dataservices` 单卫星单环 + family descriptor 注册表（域聚合 Repo/模板包/凭证连接方言/备份能力/配额/事件码族）——受管域「一个通用 reconciler + N 个 Describe()」（ADR-0004）的用户域对称形态，满足 ADR-0087 机制原语三判据（扇出=3≥2/零上行依赖/守卫可执法）。**既有 databases 卫星迁入 descriptor 是 B2 机制统一步**（行为/golden 零漂移验收），pubsubs/bucketstores 为实例二/三。**环表 10→11**（独立环取舍反转记录：三份环骨架复制违背机制单源；故障域隔离改由 per-family 子步 recover 承载——家族步 panic 记账继续他族，环 skeleton 仅基础设施级 panic 转 ring dead；代价如实=单环家族间收敛耦合，换取生命周期单源与第四家族近零成本）；kick 面保持分族。CRUD 三 proto 服务面不变，gRPC 实现共享通用 handler core（descriptor 参数化，manageds 先例）。**三轨一基**：数据服务与 App/受管域共享 L1 公共运行保障（Workload/Runtime.Ensure/材料纪律/Generation 内容等价/卷网观测），仅不取 App 编排面（Deployment 状态机/Revision/Strategy/Admission——行即期望态，per-row Generation 原位收敛）。
2. **三轴与双 runtime**：NamespaceRef 增 `.PubSub`/`.BucketStore`（ADR-0025 分立律——拒塞 `.Database` 字段）；swarm label/载体名（`fleetly-pub-<name>-<id>`/`fleetly-bkt-<name>-<id>`，ADR-0092 双段律）/DNS（`pub-<id>`/`bkt-<id>`，engineports 铸名族下沉同源）；**swarm 与 k3s 翻译同批落**（databaseNamePrefix 同源公式先例，metricsquery 双臂正则连坐扩）。挂全部活跃项目网（fail-closed 零网拒建）+EgressSubset+卷钉住+挂卷 stop-first 既有律全适用。
3. **模板包三立**：pubtemplate/buckettemplate 新包 + enginetemplate 共享内核（VersionSpec/Knob/Backup·Restore·Rotate·Apply Spec/MintPassword/pinnedRef/validatePassword）；Template 接口按家族分叉——**连接材料渲染取代 URL 假设**（Kafka=bootstrap+SASL 无 URL 方言；数据库家族保留 URL 单真源不动）；Secret 值 = 家族方言串，渲染器/回读器同源（写读同源律）。
4. **凭证**：`pubsub:<name>`（nats=平台合成用户住 nats-server.conf——声明式轮换 redis 同款；kafka=SASL admin 用户住 listener 配置——轮换=滚动，**轮换方言 B6 评估倾向挂账**）；`bucketstore:<name>`（silo root AK/SK——复合凭证 k=v 串，轮换=滚动）。**silo 单用户模式取舍（评审 F-07）**：root 全权限超管是 zot per-Project 凭证域先例（ADR-0056）的倒退；v1 接受论证 = 单 Project 独占实例（zot 是多 Project 共享）+ per-App service account（silo 是 MinIO 系 fork，IAM 策略面可支撑）挂 B5 live 定案。MintPassword 同源；值永不回显。Browse 面对两家族 fail-closed 拒（ADR-0051 注册表外语官既有拒；轻量 web 工具缺——kafka-ui Java 重/nats 无，挂账独立评估）。
5. **连接注入**：App Spec `pubsub_refs`/`bucketstore_refs`（ProcessSpec **字段 19/20**——17/18 已被 config_mounts/restart_seq 占用）→ `FLEETLY_PUB_<NAME>_{URL|BOOTSTRAP}/HOST/PORT/USER/PASSWORD`（NAME=引用名 UPPER_SNAKE；nats 出 URL、kafka 出 BOOTSTRAP+MECHANISM）与 `FLEETLY_BKT_<NAME>_ENDPOINT/ACCESS_KEY/SECRET_KEY`；**明文 env 承载凭证 = ADR-0014 附录 A.2 追认范围显式扩至本两族 refs**（PUB_PASSWORD/BKT_SECRET_KEY 是长效凭证、暴露面大于可原地轮换的 DB 密码——「同信任域随载体 spec env 下发」论证在此落纸，不默示继承 database_refs）；`InjectDatabaseEnv` 泛化为 refs 注入帮手；compose label `fleetly.pubsubs`/`fleetly.bucketstores` 走 external 声明律+**intake 白名单显式入册**+受理告警建议（**zeabur B5.5 挂账收口**）；`secret_refs` 文件注入通道复用。
6. **统一转储契约（备份/还原/迁移，2026-11-05 用户裁决）**：备份、还原、迁移 = 同一原语（实例数据面 dump→load）的三种编排；引擎差异收进模板契约（Dump/Load Spec、ArtifactShape=单流|对象集、RestoreCompat 版本矩阵、DataCapabilities 能力位），机制层（backups 卫星）跨族单源（family descriptor 归一）。**产物 = manifest 锚定的对象集**（单流引擎 1 成员、NATS 逐流 `.tgz`；verify/Download/修剪全 manifest 驱动；无兼容层前提直上 manifest 形不迁旧键；Platform Backup 捎带两层分立不动〔ADR-0039〕；manifest 机制随 B4 落地全族换形+e2e 重钉）。**还原 = restore plan**（受理 RestoreCompat〔源=backups 行 engine_version，ADR-0094 决策 5〕+ preseed 前置〔Generation==0+卷钉控制面〕；三入口同 plan：`--restore-from-backup`/升级正典①/实例克隆）。**迁移铁律**：数据面迁移一律 dump→load（ObjectStore 断点可重放可验证）、载体面迁移一律卷搬运（drain+repin，限同 Runtime）——两径不混用；版本迁移=ADR-0094 正典、载体迁移=runbook/ADR-0052 语义。**能力位诚实推论：backup=false ⇒ migrate=false**（kafka v1 major 升级=新空实例或外部工具，目录/文案披露；silo 同挂账）。挂账：跨 Project 克隆、post-load 数据断言（e2e 域）、nats 产物形态 B4 live 定案（nats-box 工具容器钉版）。
7. **引擎选型**：nats=`nats:<ver>-alpine`（scratch 默认变体无 shell 不可 exec 探针；alpine 带 busybox wget 打 8222 `/healthz`——「不依赖 nc 假设」律满足；JetStream `-js -sd /data`）；kafka=`apache/kafka:<ver>`（KRaft 缺省、`KAFKA_*` env→properties 翻译、单节点 combined、探针 `kafka-broker-api-versions`——**探针/listener 分面 B6 live 定案**：9092 开 SASL 后无凭证探针被拒，加内部明文 listener 专供探针；SASL 机制 **PLAIN vs SCRAM-SHA-256 二选一 B6 live 定案、倾向 SCRAM**〔免证书且凭证不过明文，KRaft 3.5+ 住元数据〕，评审 F-14；传输 PLAINTEXT 内网口径——zot 明文同文化；bitnami 变动荡排除）；silo=`pgsty/silo`（e2e 既有镜像转正，正式选型 B5 落定；RustFS 维持 ADR-0042 挂账不吸收）。首批矩阵与 Default 见 ADR-0094 决策 7。
8. **API/CLI/Console/守卫**：PubSubsService/BucketStoresService（Create/Get/Resolve/List/Delete/Update/RotatePassword/Backup 族按 capabilities 裁剪；ADR-0092 Resolve name 寻址同律）；五守卫+authz 词表 `pubsubs`/`bucketstores` 资源；写类分桶（CRUD=DECLARATION，Trigger/Rotate=ACTION）；事件只增（`pubsub.*`/`bucket_store.*` 各四——created/updated/deleted/credentials_rotated，与 database.credentials_rotated 先例对齐，评审 F-17）；errcode 复用既有 + E_SETTINGS_APPLY_FAILED（ADR-0095）。CLI 组 `fleetly pubsubs`/`fleetly buckets`；Console 姐妹页（ListPage+详情英雄卡 tab 同形态，一页一壳律）；skills `pubsub-provision`/`bucketstore-provision` 随批。**配额**：pubsubs 8、bucket_stores 4（databases 16 不动）。
9. **平台自宿 ObjectStore 显式分立**：本程序是用户域数据服务线；ADR-0042 决策 3 RustFS 受管自宿（系统域备份目标自宿）维持独立挂账，互不吸收。

## 后果

- 三家族机制单源化（enginetemplate 内核+目录+Update 双车道），第四家族（若有）复制成本 = 一个模板包+一个环+一组 CRUD。
- kafka 无数据备份是诚实取舍：目录 capabilities 让用户创建前可见，不静默。
- FLEETLY_* env 族扩两支：App 连接面从 DB 单支扩为三支，compose/intake/文档面同批连坐。
- 环表 10→11（dataservices 统一机制环取代原 databases 环 + 规划中的两独立环）：单环家族间收敛耦合的代价由 per-family 子步 recover 补偿，观测性能锚随 B2 归一步复测；第四家族（若有）接入成本 = 一个模板包 + 一个 descriptor entry + 一组 CRUD。

## 验收锚

- [ ] B2：dataservices 卫星归一（databases 迁入 family descriptor；既有行为/golden 零漂移；环表 10→11 登记；家族子步 recover 单测）。
- [ ] B4：nats 全链——fake Runtime 投影断言（镜像/alpine 探址/卷/网/DNS）+ e2e（创建→App 经项目网连接→FLEETLY_PUB_* 注入→逐流备份恢复 roundtrip→双车道）+ staging 实证。
- [ ] B4：tgz 逐流备份产物 live 定案记录（设计文档回填）。
- [ ] B5：silo 全链——S3 客户端（minio-go e2e 既有依赖）读写 roundtrip + AK/SK 注入 + staging 实证。
- [ ] B6：kafka 全链——SASL 连接+produce/consume roundtrip + `kafka-configs.sh` 动态配置热应用断言（容器不重建）+ TriggerBackup 拒文案 + staging 实证。
- [ ] B4-B6：五守卫/authz/事件码/分桶表/groups golden/console 姊妹页/skills 各批连坐绿；metricsquery k3s pod 正则连坐扩。
