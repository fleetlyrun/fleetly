# ADR-0029: Database 最小集——用户域收敛环、模板钉版、凭证单真源

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-02 | CONTEXT.md Database/Backup/Secret 词条、ADR-0014（材料分发）、ADR-0020（备份默认）、ADR-0021（钉版口径）、ADR-0023（删除语义）、ADR-0025（NamespaceRef 轴分立先例）、ADR-0026（List 惯例）、ADR-0028（Team 轴） |

## 背景

F1.12 要求 postgres（含 pgvector 形态）+ redis 模板、默认本地备份目标开箱
即用、连接串注入 Secret、`fleetly databases` 命令组。Database 是 Project
级一等实体（词汇真源），但部署形态、凭证铸造与备份口径均未裁决。

**事实修正（钉版调研，2026-10-02）**：Docker Hub 不存在 `percona/pgvector`
镜像（Percona 把 pgvector 打进 `percona/percona-distribution-postgresql`
发行镜像，单 arch、约 700MB）。pgvector 形态的真源是上游
`pgvector/pgvector`（官方、multiarch）。本 ADR 的 pgvector 模板落上游
镜像；spec.proto 注释里的 "percona-pgvector" 示例名随之修正为 "pgvector"。

## 决策

1. **实体与表**：`databases` 表（id ULID、project_id、name、engine、
   credentials_ref、volume_id、generation、backup_interval_secs、
   backup_retention_secs、status、created/updated/deleted_at），唯一索引
   (project_id, name) 限活跃行。**无 Revision / 无 Deployment 行**：模板
   参数创建即不可变（引擎升级 = 新建 + 恢复；版本矩阵随 F2）。挂靠卷与
   凭证 Secret 是 Project 级材料（各自生命周期，非 Database 子资源）。
2. **模板注册表**：engine 名 → 钉版镜像 + 端口 + 数据卷目标 + 凭证材料
   渲染方式，住 engine（投影知识，`internal/engine/dbtemplate.go`）。值域
   本批 = `postgres` / `pgvector` / `redis`，缺省模板钉版（ADR-0021 口径，
   zot 先例）：
   > 落位修订（2026-10-03）：随 F2.7 目录化提前（架构评审第二轮候选 1，
   > grilling 共识），`internal/engine/dbtemplate.go` →
   > `internal/engine/dbtemplate/` 子包——per-engine 接口 adapter
   > （`dbtemplate.Template`，注册表零 switch；预留 F2.2 `BackupCommand`
   > 与 digest 钉定空槽）。注册表真源语义不变；DNS 铸名与连接串组合公式
   > 仍住 engine（模板 ConnURL 收 host 注入）。
   - postgres → `postgres:17-bookworm`（major+suite 级钉：trixie 基座
     启动坑 docker-library/postgres#1363 规避；精确 patch 钉随 F2 版本
     矩阵；PG17 数据目录 `/var/lib/postgresql/data`——PG18 迁移路径坑
     不进入本批）；
   - pgvector → `pgvector/pgvector:0.8.6-pg17-bookworm`（精确版本钉；
     与 postgres 模板同 PG major/Debian suite）；
   - redis → `redis:7.4`。
   自定义 version 本批不受理（无版本矩阵；请求面无 version 字段）。
3. **部署形态 = 用户域收敛环**（第三形态，与两条既有轨分立）：
   不进 Deployment 状态机（App 键控，Database 无部署链语义面）；不走
   `fleetly/system` 受管域（数据库要挂项目网供 App 连）。engine 新增
   `databaseLoop`（第七条收敛环）：活跃 Database 行 → DatabaseSpec
   （IR 单真源，ValidateDatabase 把守）→ Workload 投影 → `Runtime.Ensure`
   （唯一写动词，幂等重放）。per-Database Generation **落行持久**
   （重启安全，managed 域进程内 gen 的用户域加强版）。创建/删除后
   Kick 即时收敛。
4. **NamespaceRef 增 `.Database` 轴**（ADR-0025 决策 4 同款词汇分立：
   拒把 Database ID 塞 `.App` 字段）。swarm Provider 分支补齐：归属
   label `fleetly.ns.database`、域选择器、载体命名
   `fleetly-db-<database-id>`。
5. **网络与寻址**：数据库挂**全部活跃项目网**（受管 Edge 同款语义，
   限本项目——Project 是网络隔离轴，库是 Project 级共享服务）；网集
   变化推进 Generation 一次（managed_edge 同款钉死语义）。DNS 名
   `db-<database-id>`（engine 铸名单源，Addressing 声明；TaskDNSName
   先例）。创建受理检查要求项目至少有一个活跃网络（fail-closed：
   零网项目的库不可达，拒绝优于静默孤岛）。卷钉住复用
   pinVolumes/applyVolumePinning 既有面。
6. **凭证与连接串——单真源 Secret**：创建时平台铸造随机密码并落
   Project Secret `database:<name>`（age 信封，ADR-0014），**值 = 完整
   连接 URL**（postgres:
   `postgresql://fleetly:<pw>@db-<id>:5432/fleetly`；redis:
   `redis://:<pw>@db-<id>:6379/0`）。三个消费面同一真源：
   ① App Process `secret_refs: ["database:<name>"]` → 文件注入直用
   （DATABASE_URL 惯例）；② DB Workload 材料 = engine Ensure 期解密
   Secret、URL 解析出密码再渲染（postgres：密码文件 +
   `POSTGRES_PASSWORD_FILE` 环境变量只含路径；redis：平台合成
   `requirepass` 配置文件、argv 干净不落明文）；③ 回显面永不回值——
   API/CLI 只回 secret 名 + 指纹 + 去密码连接面（host/port）。凭证
   轮换 = 重写 Secret 值（载体按值指纹命名 → 滚动替换自愈，zot
   附录 B 同款机制）。密码材料经 swarm secret 载体（值指纹命名）进
   spec diff——材料变更无需推进 gen 即触发滚动替换。
7. **备份口径（本批先钉，ADR-0020 承接）**：
   - **本地目标开箱即用（本批交付）**：`internal/providers/localobjectstore`
     实现 capability.ObjectStore（DataRoot/backups/ 目录；Put/Get/List/
     Delete，键钳制防路径穿越），注册 KindObjectStore 工厂——零配置默认
     在册（RegisteredFactories 注册面即所见；doctor/schema 的 Provider 列
     面尚不存在，外置 S3 Provider 随 F2 一并接装配消费面——备份执行链
     落地时 Build 进图）。对象键段禁反斜杠/盘符/穿越段；Windows 控制
     面文件名禁 ":"——备份执行器铸键用无冒号时间戳形态。
   - **触发形态裁决**：**定时**（backup_interval 缺省 24h）+ 保留窗滚动
     （缺省 7d）；"随版本触发"不采纳（本批模板参数不可变，无版本事件
     面）。执行面 = 平台铸造 one-shot Run 跑模板的备份命令、产物写
     ObjectStore（key 形态 `backups/<project>/<database>/<timestamp>`）
     ——**落 F2**（与恢复演练同批）。
   - **恢复演练面**：`databases restore`（从 ObjectStore 指定 key 恢复
     到新建 Database）+ backup verify——F2；ADR-0020 的"恢复演练是
     验收标准"锚定在 N2 e2e 不变。BackupPolicySpec 字段本批落数据库
     行（interval/retention），执行链未接前为声明面。
8. **删除语义（对齐 ADR-0023）**：无部署状态机 → 无活跃部署守卫面。
   收口序列 = `Runtime.Remove`（先拆载体）→ tombstone 事务
   （database.deleted 事件 + 审计同生共死）。**卷与凭证 Secret 不随
   删除**（备份保留义：两者都是 Project 级材料、随各自生命周期；同
   ADR-0023 "卷/网络等材料不动"）。ID 永不复用；同名重建 = 新 ULID +
   新凭证（旧 Secret 值被覆盖）。
9. **状态面**：status 列 `pending | running | degraded | stopped`，
   收敛环从观测缓存推进（普通行写，无状态迁移事件——DB Workload 已
   登记稳态看门狗 expected 面，停机告警由既有 `workload.stopped` 事件
   覆盖；per-entity 状态事件待消费者出现再加，只增原则零成本）。
10. **API/守卫/CLI**：DatabasesService（Create/Get/List/Delete；List 走
    `after_database_id + limit`，ADR-0026 惯例）。受理位：
    parentProjectAlive + databaseQuota（per-Project 上限 16）+ 活跃网络
    在场检查 + engine 值域校验。五守卫喂食：idem（CreateDatabase）、
    freeze（Create/Delete 入 FrozenVerbs，Delete 走 scopeDatabase 逐级
    回行）、acceptance（commit 原语）、grpc/gateway（注册挂载）、
    authz（scope 注解 + 词表加 `databases` 资源）。errcode 零新码
    （E_INVALID_ARGUMENT/E_NOT_FOUND/E_ALREADY_EXISTS/E_CONFLICT/
    E_QUOTA_EXCEEDED/E_SECRET_UNAVAILABLE 既有码全覆盖）。eventcode
    +2：`database.created` / `database.deleted`。CLI `fleetly databases`
    命令组（create/list/get/delete）双形态 golden，组动词进 groups
    golden 清单。

## 后果

- 数据库 Workload 的归属/期望缓存键 = `database/<id>`（非 App 行键）；
  driftScan 的 spec 对照面对该前缀跳过（managed 域键同款），稳态看门狗
  面保留。
- 模板钉版是平台行为面：镜像 tag 变更（如 17-bookworm 内 patch 滚动）
  不推进 Generation（引用不变即 spec 不变）——digest 级钉随 F2 版本
  矩阵裁决。
- firstBootJobs 部署链接线不随本批（状态机等待语义需独立设计，挂
  F1.13；checklist 记录）。
- mysql/mongodb 模板、外置 S3、版本矩阵、备份执行链、恢复面 = F2。

## 验收锚

- [x] 五守卫绿：DatabasesService 在 idem/freeze/acceptance/grpc/gateway
  五面注册在册（guards 包测试即执法）
- [x] 创建→收敛→running：databaseStep 投影（模板镜像/项目网/db-<id>
  寻址/卷挂载/凭证材料）经 fake Runtime 断言；Generation 落行、重启
  幂等（重放同 gen）
- [x] 凭证单真源：Secret `database:<name>` 创建期铸造、值永不回显
  （API 响应/CLI golden 只含 secret 名与指纹）；DB 材料经 URL 解析
  （postgres 密码文件 / redis 配置文件）
- [x] 挂网语义：项目全部活跃网进 Workload.Networks；网集变化推进
  Generation 一次
- [x] 删除收口：Runtime.Remove 先行、tombstone 事件+审计同事务；卷与
  Secret 残留（备份保留）
- [x] 本地 ObjectStore 开箱：工厂注册进 RegisteredFactories（注册面即
  所见）；Put/Get/List/Delete 有测试
- [x] ListDatabases 走 after_database_id + limit（ADR-0026 惯例面 +1）
- [ ] 备份执行链 + 恢复演练（F2；ADR-0020 验收 N2 e2e 不变）（排期 = 功能清单 F2.2，验收必过项）
- [x] staging 真机实证（受管形态起服 + App 经项目网连接 + 双节点卷
  钉住；随 F1.15 批记录）（F1.15 ⑥：pgvector torchwood-pg running + App/migrate job 经项目网连 db-<id> 跨节点 + 卷钉住 manager；runbook 2026-10-02 节）
