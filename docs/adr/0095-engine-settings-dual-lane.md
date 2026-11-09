# ADR-0095：数据服务配置面——白名单旋钮与 static/dynamic 双车道

日期：2026-11-04 立法（提案 §5；设计 `docs/design/2026-11-04-managed-data-services.md` §3/§5）｜ 状态：Accepted（实施随 B2/B3；修订 ADR-0029 决策 1 的不可变半边；承接 ADR-0081/0082 声明式律与 ADR-0089 确认闭环）

## 背景

引擎参数（redis 落盘/maxmemory、PG max_connections、kafka retention、NATS JetStream 限额…）此前零暴露，resources 不建模，模板参数创建即不可变（ADR-0029 决策 1，立法理由"无版本矩阵"已被 ADR-0094 取代）。用户裁决（2026-11-04）：配置机制为一等面，且须支持**运行时热修改**（不改重启）。

## 决策

1. **白名单类型化旋钮是唯一通道**：每引擎注册 knob schema（`Key/Kind{bool,int,enum,duration,size}/Min/Max/Enum/Default/Class/Category`），按 **engine+version 键控**（绑版实例内分叉——各代默认值/参数名差异是引擎知识）。受理位 `ValidateSettings` 单源：白名单外键拒、类型/范围/enum 拒，精确报错。**自由配置文件/任意 env 注入明确否决**（postgresql.conf `preload_libraries`、redis.conf `loadmodule`、kafka `advertised.listeners`——安全与平台所有权）。
2. **平台-owned 键永不可设**：认证（requirepass/密码/SASL）、监听与通告、数据目录、复制与集群面、模块加载。执法 = 白名单闭集本身 + 守卫测试（TestPlatformOwnedKeysUnreachable：每引擎 owned 键集与 Knobs 交集恒空）。例外：kafka `heap_mb` 是平台 owned 面（KAFKA_HEAP_OPTS）暴露的安全子集（B6 校验 heap_mb 与 memory limit 联动）。
3. **旋钮键 = 引擎原生词**（`appendonly` 非 `persistence`）——不发明中间名，零翻译层；键名冻结同词条律（只增不改语义）。旋钮值过渲染安全闸（validatePassword 同族字符集纪律按 Kind 收敛——INI/YAML/conf 注入防线）。
4. **双车道分级**：每旋钮声明 `class`：
   - **static（重启类）**：折进 `Materials(password, settings)`/Workload env → 材料字节变=secret 值指纹名变=受监督滚动（或 spec 指纹→Generation）；机械同 digest bump，**环侧 memo 签名扩第四段 = 密文摘要 + static 子集摘要的复合代理**（评审 F1+红队 A5 降级翻案：ensureFresh 有 ReconcileReplayInterval〔缺省 60s〕强制重放兜底——不扩段是 60s 时延缺陷而非「永不重下发」，F1 降 P2；第四段不得字面用 MaterialsFingerprint〔吃已渲染字节→每拍解密凭证，推翻密文代理立法〕）；Generation 不随材料推进（RotatePassword 先例）。分级依据 = 引擎原生知识（PG postmaster 上下文参数、redis 无自持久面、nats 热加载面窄）。**喂入律**：投影/材料调用点只喂调用方预过滤的 static 子集，dynamic 键永不进 Workload env 与 Materials（守卫 TestDynamicKeysAbsentFromProjection）。
   - **dynamic（运行时类）**：**不进滚动材料**。`ApplyDynamicSettings(host,password,settings) (ApplySpec,error)` 经一次性工具容器执行（RotatePassword 先例：argv+材料、纯数组）。**持久化优先引擎自持久**：PG `ALTER SYSTEM`+`pg_reload_conf()`（auto.conf 住数据卷）、MySQL `SET PERSIST`（auto.cnf）、Kafka `kafka-configs.sh --alter` 动态面（住 KRaft 元数据）——重启自愈。redis 无自持久（CONFIG SET 重启即丢）：**首批 redis 旋钮全 static**，卷内 user.conf include+CONFIG REWRITE 模式挂账后手。
5. **Update 动词分流**（新 RPC，三种数据服务类型同构）：static 子集→落行+Kick 滚动（async 既有机械；**环 memo 签名含材料指纹后 Ensure 真实重发**）；dynamic 子集→**同步热应用先行**（工具容器执行，失败=专用码 `E_SETTINGS_APPLY_FAILED` 精确报错行不动——先数据面后持久层，RotatePassword 同序，E_DATABASE_ROTATE_FAILED 同构的哨兵映射；errcode 只增 +1）→成功才落行。混合变更一次受理、两车道各自审计。**单写边界（红队 B-击一）**：dynamic apply 全部驱动者（API 热应用/环重放）共持 per-entity 互斥（dataservices 域 utilityMu，rotateMu 先例随域走）——Update 全程持锁使三连原子化；Update 受理位 status!=running 即拒（「滚动进行中」提示，红队 A6）。
6. **声明式律（红队修正后口径）**：行 = 期望态单真源；未变声明重放 = 零扰动 no-op；**重放检查在 memo 短路判定之前执行**。**dynamic 半边是事件触发收敛（memo 失配/重建/重启）非持续对照——外部直改引擎配置不纠正**（负空间声明，红队 B-AT-08：ADR-0081「持续纠正」判据在此半边如实降格）。**dynamic memo 协议（评审 F6+红队 B-击一/A7 补强）**：per-entity memo 增 AppliedDigest，Update 路径在 apply 成功后、行落库同窗口**经 engine/contract memo 端口**写入（禁直写卫星私有表，ADR-0088 决策 7）；**apply 失败路径强制 Forget AppliedDigest**（半应用黑洞修：多旋钮部分失败时行与 memo 俱不动→引擎停半应用态永不收敛——Rotate「失败零状态变更」论证在多键配置面不成立，不得搬运）；**重放仅 status=running 且非恢复挂起行执行+失败退避**（新行穿 StartPeriod churn 防）；已应用未落行窗（apply 成功后崩溃）= 恢复后以行值幂等重放拉回，RotatePassword 崩溃窗同构、正确代价如实记录。实例重建判据=卷内实例标记（**ADR-0097 契约升 dataservicetemplate 内核、字段实体中性化〔entity_id/kind（wire 键 family）〕，三种数据服务类型通吃**——红队 B-击三：否则新类型带着刚修完的异源缺陷出厂）。
7. **resources 随本 ADR 入面**：`resources{cpu_millis,memory_mb}` 可选（缺省 nil=无限制=现状），Create/Update 可变（static 语义：载体资源变更触发任务替换）；投影填 `capability.Resources`（swarm Limits/k3s requests+limits 既有翻译）。OOM 诚实立场：v1 缺省无限制；硬 Limits 对 PG 共享内存是 OOM 风险，警示随目录旋钮 Note。
8. **engine/version 不可变**：Update 请求含之即拒（E_INVALID_ARGUMENT 指引升级路径文案）——ADR-0029 决策 1 修订为「engine/version 创建即不可变；settings/resources 经 Update 可变」。修订记录随本 ADR 在案。
9. **确认闭环与写类**：Update 接 ConfirmableVerbs（ADR-0089 默认强制）——预览 = settings/resources diff+车道判定+滚动后果（dynamic 全集=zero_touch；含 static=replace）；确认档 standard（ADR-0086）。写类 DECLARATION 桶——**与 Rotate=ACTION 的分界**（ADR-0081 §5 混合范式对照，评审 F7）：Update 落可 diff 期望态行、static 车道有收敛面接棒、未变重放 no-op，三判据齐全；Rotate 是纯动作无行可持有（效果经材料指纹接棒）——ADR-0081 附录扩行时带此边界注。freeze 入 FrozenVerbs；审计 `database.update` 等；事件 `database.updated`/`pubsub.updated`/`bucket_store.updated`（只增）。
10. **可视化 = schema 驱动**：旋钮 schema 随 Engine Catalog（ADR-0094 决策 4）下发；Console 目录驱动动态表单（Kind→控件映射、Category 分组、class 徽标 instant/rolling、确认框分级披露）；CLI `--set k=v`（可重复）/`config describe`（class 标注）；零硬编码引擎页面。首批旋钮集见设计文档 §3.4（PG 3/redis 3/mysql 2/kafka 4/nats 2/mongo 0/silo 0——含 dynamic 6 个）。

## 后果

- 「创建即不可变」让位于「期望态可调」：数据服务获得 resize（resources）与调参（settings）能力，全部走既有收敛机械，无新写路径。
- dynamic 面对引擎自持久能力的依赖成为分级事实：PG/MySQL/Kafka 得热修改，redis v1 全滚动（诚实取舍，后手挂账）。
- Update 是数据服务第一个可变声明动词：ConfirmableVerbs 预览（diff+车道+滚动后果）是它的配套验收面。
- knob schema 进目录 = Console/CLI/Agent 三面同步自描述，增旋钮零前端发布。

## 验收锚

- [x] B2：ValidateSettings 单测（白名单外/范围外/enum 外/类型不符精确报错；平台 owned 键不可达）；TestPlatformOwnedKeysUnreachable + TestDynamicKeysAbsentFromProjection 绿（B2b 兑现：渲染面形态 TestDynamicKeysAbsentFromStaticRender + TestSplitSettings；B2d 补投影/命令面缺席断言〔update_test/apitest 喂入律〕）。
- [x] B2：Update 分流 golden（static→行+Kick+**fake Runtime 收到 Ensure 真实重发且命令覆写面含新值**〔memo 签名第四段断言——TestUpdateStaticLaneRollsImmediately〕；dynamic→fake 工具容器执行序+失败不落行+E_SETTINGS_APPLY_FAILED；混合两车道〔update_test + apitest TestDatabaseUpdateDualLane〕）。
- [x] B2：重建后重放单测（AppliedDigest 失配触发/重放先于短路拍/崩溃窗回退语义/dynamic 集 idempotent + pending/恢复挂起不重放 + 失败退避〔TestReplayDynamicSettingsGate〕）。
- [ ] B3：CLI `--set`/`config describe` golden（class 标注）；Console 目录驱动表单 vitest + class 徽标。
- [ ] B7：e2e 双车道断言（dynamic 变更→容器 started-at 不变+生效值查证〔CONFIG GET/pg_settings 回读〕；static 变更→滚动发生）。
