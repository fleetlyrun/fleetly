# ADR-0094：数据服务引擎版本矩阵——三层解耦、目录自描述、升级正典

日期：2026-11-04 立法（提案 §4/§2.1-2；设计 `docs/design/2026-11-04-managed-data-services.md` §2）｜ 状态：Accepted（实施随 B2/B3；承接 ADR-0029 决策 1/2 挂账"版本矩阵随 F2"与 ADR-0045 digest bump 序）

## 背景

ADR-0029 立项时裁「每引擎单一钉版、自定义 version 不受理（无版本矩阵）」，patch 级追新由 ADR-0045 digest bump 序承载；major 选择与升级路径一直显式挂账。用户裁决（2026-11-04）：版本矩阵进本程序，且开发期无向后兼容义务（staging 可全量重建）——本 ADR 直接定终态，不设过渡形态。

## 决策

1. **三层解耦**：行 `version` 列存**用户面 major 短名**（如 `17`，NOT NULL，创建时选定、存续期不可变）；镜像 tag（`17-bookworm`）浮动住注册表；digest（index sha256）钉 patch 住注册表（ADR-0045 律延伸至全版本矩阵）。**digest bump 不动行值**——追 patch 是纯平台行为（投影指纹→Generation→受监督滚动恰一次）；**major 换值只经升级路径**（决策 5）。
2. **注册表版本化**：Template 接口增 `Versions() []VersionSpec{ID,Tag,Digest,Default,Deprecated,Note}`；`For(engine,version)` 返回**绑版实例**（adapter 持 version 字段，跨版本差异收在 adapter 内——PG18 PGDATA 版本化路径经 DataTarget 全代统一挂 `/var/lib/postgresql` 消灭分叉，B2 live_test 实证）。pgvector 二维矩阵（vector×pg major）以复合短名（`0.8-pg18`）表达，adapter 自持合法组合集，不建二维注册表结构。
3. **受理与存储**：`Create*.Request.version` 值域 = 服务内注册表检查（与 engine 同律）；空 = Default；Deprecated 拒新建（E_INVALID_ARGUMENT 列合法值与弃用说明）。回显换源：`version` 行值直读，退役「服务端模板动态取」双源。migration 直接 NOT NULL 新列（无兼容层前提：不回填、不写空值兜底）。
4. **引擎目录（Engine Catalog）**：`GET /v1/data-engines` 跨家族目录 RPC（READ 桶、**暂定** `databases:read`——终裁三案见 proposal §8.4，B2 定案后随批固化；红队 B-AT-11：Accepted ADR 与开放问题不得并存），服务端从模板注册表现场装配（零缓存零漂移）——引擎/版本/默认/弃用/旋钮 schema 的唯一自描述面；Console/CLI 取值一律走目录，硬编码引擎常量（`DATABASE_ENGINES`）退役。zeabur 借鉴 B5.2 挂账在此收口。
5. **升级正典 = 新建+恢复+同名重建序（红队 A1 修正后）**：①新建新版本实例（受理位 `RestoreCompat(engine, oldV, newV)`——oldV 取自 backups 行 `engine_version` 列（旧备份行空值=同引擎放行但**受理文案标注「版本未记录，降级风险自负」**，红队 A10）；同引擎同版本放行、major 只许升不许降、**pgvector 复合 ID 分轴比较**（vector minor×pg major 各自独立只升，红队 A10/B-AT-13）、跨引擎拒）+`--restore-from-backup`；②验证后**强制 TriggerBackup+VerifyBackup 取新备份**（①的备份到②删除之间的写入否则静默丢失，红队 A1）→删旧（Secret 保留义不变；旧数据卷处置动机=**容量回收**——ULID 卷键〔ADR-0097 已落地〕已消除同名复用撞库；**`volumes purge` 动词 B7 立项**〔wide 档确认+容量披露〕，落地前为 runbook 手工序并显式标注）；③同名重建新版本库+**恢复源显式=②的新备份** → Secret 值覆盖新 host → 引用 App 重部署生效（**断连窗=③恢复时长，文案与 runbook 披露**）。in-place major 升级（pg_upgrade 等）**明确否决**。CLI `databases upgrade` 编排向导挂账。
6. **生命周期 SOP**（承诺面，工具 B2 落）：引入 = 注册表 entry（digest 钉定门禁自动连坐）+目录自动出现+e2e 仅 Default 版本（CI 时长控制；**非默认版本真实启动走查** = 引入/翻 Default 批次随 staging 冒烟跑，设计 §2.6 条 5）；追 patch = `scripts/digests.sh` 批量回填；弃用 = Deprecated 标记拒**全新建**（**同版本带 restore 的同名重建放行——灾备义**，否则弃用版卷丢失时灾备被迫 major 升级）+存量全语义续跑+目录 Note 带移除窗（建议两个发布周期）；移除 = **存量清零后** runbook 动作（移除后收敛环对存量行 For() 未命中走防御跳过——仅兜底、不承诺收敛续跑，故以清零为前提）。上游 EOL 表人工看护（redis 8.0 于 2026-12-01 EOL、mysql 8.0 已 EOL——短命线不入 Default 的教训入档）。
7. **首批在册矩阵**（建议值，digest B2 钉）：postgres 16/17(默认)/18；pgvector 0.8-pg17(默认)/0.8-pg18；mysql 8.4(默认)/9；redis 7.4(默认)/8.2；mongo 8.0(默认)/8.2；nats 2.10/2.11(默认)；kafka 4.0/4.1(默认)；silo 单 RELEASE 版。EOL 线不入册（redis 8.0/7.2、mysql 8.0、mongo 7.0、kafka 3.x）。

## 后果

- ADR-0029 决策 1「模板参数创建即不可变」的 version 半边废止（settings/resources 半边由 ADR-0095 修订）；ADR-0045 digest bump 序不变、门禁扩容至矩阵全量。
- 版本矩阵 N×M 维护是持续成本：SOP+脚本承载，e2e Default-only 控 CI；每新增引擎版本零发布成本（注册表+目录自动出现）。
- 升级路径依赖 Backup/Restore 链成熟度——pgvector 随本程序收编 e2e 备份矩阵（复用 postgres dump 方言）。
- resources 字段与版本同批入 Create/行面（ADR-0095 共构），但立法归属 ADR-0095。

## 验收锚

- [ ] B2：`For(engine,version)` 绑版实例/Default 解析/Deprecated 拒/RestoreCompat 单测绿；TestImageDigestsPinned 扩矩阵全量绿（含 tag@digest 双段律）。
- [ ] B2：version 列 migration 落地（NOT NULL、无回填）；回显行值直读（golden 钉）。
- [ ] B2：目录 RPC golden（三家族/版本/默认/弃用/旋钮 schema）；`digests.sh` 可用且与注册表常量自洽。
- [ ] B2：PG DataTarget 挂 `/var/lib/postgresql` 双 major live_test 实证（16/17/18 至少两代）。
- [ ] B3：CLI `--version`/Console 版本下拉目录驱动；`DATABASE_ENGINES` 常量删除（守卫或 grep 断言）。
- [ ] B7：e2e 版本矩阵段（同引擎双版本并建并写读）+ 升级序 runbook 文档。
