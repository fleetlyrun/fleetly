# R05 持久化、状态层与迁移 评审报告

- 评审者：r05-persistence（task-5）
- 日期：2026-10-19；仓库快照：`D:\Codes\qiulin\fleetly` 分支 main HEAD `35257f18`（工作树干净，只读评审）
- 基线：`PROFILE.md` + `MODULES.md` R05 节；全部结论基于生产代码静态阅读 + 包级测试 + `EXPLAIN QUERY PLAN` + `-overlay` 注入的临时复现测试（不落仓，见各条证据）
- 本机实测：`go test -race ./internal/infrastructure/persistence/...` 全绿；`go test ./internal/guards/` 全绿（现树守卫可跑绿）

## 1. 覆盖清单

### 完整读过（生产文件）
- 根包：`state.go`（153 行）、`statekit.go`（59 行）、`statetest/statetest.go`（59 行）
- 37 个聚合 repo 的生产文件全部读过（每包 `repo.go`）：alertrule、app、appvariable、audit、backup、browse、build、catalog、channel、config、database、deployment、freeze、githubapp、hook、idempotency、invitation、logcursor、membership、network、networkpeer、node、outbox、project、revision、role、route、run、schedule、secret、sharedvariable、sourceupload、task、team、token、user、volume
- 迁移：`migrations/*.sql` 全部 33 个（00001–00033，版本号连续无缺口）+ `migrations/forward_test.go`
- 守卫（本模块执法面）：`statekitlaw_test.go`、`statefacade_test.go`、`migrationchainanchor_test.go`、`transitionguard_test.go`、`acceptanceguard_test.go`、`uowguard_test.go`、`entityid_guard_test.go`、`aggregatetestbelt_test.go`、`rowmintguard_test.go`
- 与持久化直接相关的调用方：`application/transact/transact.go`、`domain/shared/state/state.go`、`domain/services/writes/writes.go`、`assembly/provides.go`（Binder/Janitor/Seed 装配、retention 窗口）、`engine/loop.go`、`engine/options.go`、`engine/transition.go`、`engine/converge.go`、`engine/backup.go`、`engine/taskobs.go`、`engine/taskverbs.go`、`interfaces/grpc/fleetlygrpc/acceptance.go`、`contexts.go`（events 410）、`identity.go`（ListAudit）、`application/authn/{interceptor,bootstrap,seed}.go`
- 定向读过 schema/索引相关迁移与 ADR-0070

### 完整读过的测试（18 个文件）
`state_test.go`、`migrations/forward_test.go`、deployment、task、run、outbox、audit、revision、app、sourceupload、token、secret、idempotency、backup、schedule、networkpeer、database、hook 的 `repo_test.go`

### 抽样读过（按测试函数名 + 结构 + 关键断言抽样）
其余 19 个聚合测试文件（alertrule/appvariable/browse/build×2/catalog/channel/config/freeze/githubapp/invitation/logcursor/membership/network/node/project/role/route/sharedvariable/team/user/volume）；未逐行精读

### 未覆盖/无法覆盖
- 真机/多进程并发行为（Docker 不可达；secret 测试已用同进程双 `Open` 模拟双写者，但未做真多进程压测）
- 长时间运行下的 WAL/文件增长、busy/lock 实测曲线（仅静态推断 + 查询计划）
- 迁移从 v1–v27 各中间版本的前滚演练（测试只覆盖 v28、v30–v32 起点，见 M3）
- 平台 Backup/restore 与 `VACUUM INTO` 的真机一致性（R04/R09 面，仅读实现）

## 2. 模块画像与边界

**职责**：`internal/infrastructure/persistence` 是控制面唯一持久化机械面。规模：生产 40 文件 5412 行、测试 4491 行、33 个迁移 SQL 806 行、94 个测试函数。根包只承载连接/事务/驱动错误判定/共享原子（`state.go`+`statekit.go`），聚合 SQL 按 37 个聚合分包，禁止门面（`statefacade` 白名单守卫执法）。

**对外接口**：行类型与 `Repo` 接口住在 `internal/domain/**`（ADR-0075 P1b），本层 `New(clock, run)` 返回 domain 接口；执行接缝 `state.Runner` 只出现在域契约与 persistence 内（`uowguard` 守卫执法）。写路径唯一事务入口是 `transact.Tx.Do`（显式 bundle UoW，ADR-0080），引擎侧另有 `e.tx.Do`；**读径**直接用组合根以 `*sql.DB` 绑定的 repo（CQRS-lite）。

**依赖方向**：infrastructure → domain（shared/state 契约、各聚合行类型）；application → infrastructure 的只有组合根装配（`assembly`）与 `transact.Tx` 消费；符合 ADR-0075。未发现 persistence 反向 import application/interfaces 的违规（`dep_matrix` 守卫覆盖）。

**与架构/ADR 声明的偏差（详见发现）**：
- 声明"四件一拍同事务"在状态迁移/受理写路径成立（组合点收口且有守卫）；**例外**是非事务 `writes.Emit` 双真事实（M1）。
- ADR-0070 声明 seq/id "构造性永不重发"，对**硬删行**的聚合不成立（M2）。
- ADR-0026 订阅契约"窗内不删不改、断档 410"依赖 seq 前缀裁剪，实现按 `created_at` 裁剪（M4）。
- 单连接池是已声明设计债（`state.go:107-109` 注释"读池分离待实测瓶颈再议"），但无扫描预算/索引配套，构成全局尾延迟面（M5/M6）。
- 审计只增不裁剪（ADR-0075/0066）在写侧成立（无 UPDATE/DELETE 面），但读侧只有最近 ≤1000 行、无游标（M7）。

## 3. 优点（带证据）

1. **四件一拍的组合点真的收口且被静态执法**。`state.DB.Tx` 是唯一事务机械（`state.go:154-172`），应用侧只有 `transact.Tx.Do`（`transact.go:118-123`）与 `engine` 的 `commitTransition/commitWrite` 两处序列原语（`engine/transition.go:95-162`）；事实两行由 kernel 单声明扇出（`domain/services/writes/writes.go:78-127`）。守卫反扫 engine/API 的"行写 × 事实发射"配对与 `DB.Tx` 直调，任何新写面绕开组合点即红（`guards/transitionguard_test.go:317-366`、`guards/acceptanceguard_test.go:87-149`）。这是本模块最强的架构承诺承载。
2. **CAS 纵深防御 + 确定性并发测试**（不是 sleep 猜时序）。所有状态机 repo 的 `Transit` 都在 `UPDATE` 尾缀 `persistence.TransitWhere` 并在 `RowsAffected=0` 时归 `ErrConflict`（如 `deployment/repo.go:231-238`、`task/repo.go:159-162`、`run/repo.go:180-183`、`build/repo.go:147-149`）；测试用 `mut` 作同步点确定性制造"前置 Get 与 UPDATE 之间被并发改写"（`deployment/repo_test.go:63-96`），同名并发 PUT 用 runner gate 复现唯一索引交错（`secret/repo_test.go:94-157`）。
3. **共享机械原子收敛成唯一入口且被 AST 守卫**。`statekit.go` 六原子（`ClampLimit/Placeholders/StateIn/JoinStates/ToAny/TransitWhere`）替换了 26 处手写钳位与三份占位串；`statekitlaw_test.go` 以 AST 执法"手写即红 + 豁免双向保鲜 + 防盲阈值 + 红灯 shape 用例"（`:355-437`、`:439-570`）。这是本仓"架构由测试承载"的正面样板。
4. **迁移纪律可审计**：goose 只前滚 + embed（`state.go:141-152`）、WAL/busy_timeout/foreign_keys/synchronous 全在 DSN 单点（`state.go:98-102`）；G13 守卫对账"forward_test 的 latest 常量 = 目录最大版本、检查点表含 N-1"（`guards/migrationchainanchor_test.go:122-142`）；前进测试真实前滚 v28→33 并断言 v31/v32 默认回填语义（`migrations/forward_test.go:68-110`）。
5. **软删/部分唯一索引语义自洽**：所有带 `deleted_at` 的部分唯一索引，其活跃行插入都显式落 `''`（如 `project/repo.go:42-45`、`app/repo.go:38-41`、`secret/repo.go:47-50`），读/删路径统一 `deleted_at = ''`；唯一冲突在 20+ 个 repo 归一为 `state.ErrAlreadyExists`/`ErrConflict`（`state.go:43-54`），且全量 repo 测试断言了归一形态而不是裸驱动错误。
6. **hermetic 夹具真 SQLite + 假时钟 + 37/37 聚合测试带**：`statetest.New` 打开 temp 真库并注入假时钟（`statetest/statetest.go:53-68`）；G19 守卫强制每个聚合子包有测试（`guards/aggregatetestbelt_test.go:102-114`，现树零豁免）；94 个测试函数覆盖 CAS/钳位/游标/幂等/冲突归一，无 `t.Skip`。

## 4. 发现清单

### R05-M1｜非事务面 `writes.Emit` 上的 `audit.Append` 会并发撞 UNIQUE(seq) 丢审计行，并使合并事实的"流/账两轴"可能分裂
- 维度：可靠性 / 可观测性
- 严重度：M（核心四件一拍写路径不受影响；受影响面是两个非事务调用点，失败方向 fail-closed 不损坏已有行；但审计账本出现无提示缺口、且合并事实可"有事件无审计"，与 kernel 的"一条事实两行同源"承诺冲突。若审计完整性被当硬承诺，可升 H）
- 位置：`internal/infrastructure/persistence/audit/repo.go:34-44`（seq 铸造）；`internal/infrastructure/persistence/state.go:65-76`（NextSeq 的原子性前提）；`internal/domain/services/writes/writes.go:87-93`（Emit 非事务声明）、`:98-127`（双行扇出）；调用点 `internal/application/authn/interceptor.go:354`（收窄审计）与 `internal/application/engine/converge.go:428`（收敛成功双真事实）
- 证据：
  ```go
  // audit/repo.go:34  —— MAX+1 与 INSERT 是两条独立语句
  seq, err := persistence.NextSeq(ctx, r.run, "audit")
  if err != nil { return err }
  e.Seq = seq
  e.ID = entityid.Audit(seq)
  _, err = r.run.ExecContext(ctx, `INSERT INTO audit (...) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ...)
  return err   // 唯一冲突未归一，裸错误上抛
  ```
  ```go
  // state.go:65-70 注释与实现都假定"事务内"才串行
  // NextSeq ... 连接池收口为 1（见 Open），事务内 MAX+1 读-改-写天然串行；
  // UNIQUE(seq) 索引使违规双铸 fail-closed。
  ```
  复现（`-overlay` 注入临时测试，不改仓）：32 goroutine × 8 次非事务 `audit.Append` → **205/256 次失败**，错误全部为 `constraint failed: UNIQUE constraint failed: audit.seq (2067)`。`writes.Emit` 的扇出顺序是"先 Outbox.Append（AUTOINCREMENT 安全）→ 后 Audits.Append"，converge 双真事实在 audit 失败时留下"有事件、无审计"。
- 影响：密码登录收窄告警、`workload.converged` 等系统事实在并发写审计时可能丢审计行（仅错误日志，无补偿）；审计取证面出现静默缺口，且合并事实的事件轴/账本轴可分裂。触发概率低频（非事务审计写点仅两处，其中一处 60s/Token 节流），但系统运行时长内无上界，且现树零测试覆盖该路径。
- 建议：①`audit.Append` 对唯一冲突做有界重试（2–3 次）或改为 `INSERT ... RETURNING`/AUTOINCREMENT 铸 seq；②收敛事实改走一次只含事实的 `Tx.Do`（或若接受 best-effort，显式降级为 StreamOnly 并在文档写明账本不保证）；③补一条非事务并发 Append 测试 + 一条"Emit 双行失败注入"测试。成本 S（①）+S（②）。
- 状态：已亲验（复现测试 + 全链路调用方阅读）

### R05-M2｜ADR-0070 的 seq/id "构造性永不重发" 在硬删行聚合上被破坏
- 维度：架构一致性 / 可靠性
- 严重度：M（不损坏现网数据、不越权；破坏的是身份/审计可追溯的架构承诺：同一 ID 可代表两个不同实体；对审计取证与按 seq 的游标语义有长期污染，且无守卫/测试兜底）
- 位置：`internal/infrastructure/persistence/state.go:70-76`（MAX+1）；硬删行的 repo：`user/repo.go:26-43,108-124`、`team/repo.go:26-43,77-93`、`role/repo.go:26-49,110-133`、`alertrule/repo.go:28-47,104-117`、`channel/repo.go:27-46,90-103`、`backup/repo.go:242-245`、`browse/repo.go:86-92`、`sourceupload/repo.go:109-112`；承诺原文 `docs/adr/0070-readable-entity-id-and-order-decoupling.md:14`
- 证据：
  ```go
  // state.go:70 —— 被删的最大 seq 行会让下一行重新拿到同一个 seq
  func NextSeq(ctx context.Context, run state.Runner, table string) (int64, error) {
  	var max sql.NullInt64
  	if err := run.QueryRowContext(ctx, "SELECT MAX(seq) FROM "+table).Scan(&max); err != nil { ... }
  	return max.Int64 + 1, nil
  }
  ```
  ADR-0070:14："…序号由单写者事务分配、含 tombstone 行取 MAX、**构造性永不重发**"。
  复现（overlay）：创建 user `alice` → `alice-1`；`Delete` 后再 `Create alice` → **再次得到 `alice-1`**（`seq=1`）。同款形态存在于 8 个硬删聚合；其中 users/teams/roles/channels 的 ID 直接由 name+seq 派生，审计行 `resource="user/alice-1"` 会指向两个不同实体。测试里反而把未验证的假设写成断言：`database/repo_test.go:131` "ID 永不复用，ADR-0029 决策 8"（该测试用外部显式 ID 规避了 MAX+1 面）。`entityid_guard_test.go:105-141` 只校验 seq 列存在与 UNIQUE 索引，不校验 non-reuse。
- 影响：①审计取证/排查中 ID 歧义（同一 `user/x-3` 两个实体）；②按 seq 的 `after_*` 游标在这些表上跨"删除+重建"可能跳行/重复；③派生类 ID（backup `db-b<seq>`、browse `db-s<seq>`）在同库删除最新行后重现，旧外部引用（如 `restore_from_backup` 悬挂值）理论上可命中新行。实际触发需要"删除当前最大 seq 行"，概率随运维操作增加。
- 建议：机制层补齐而非逐点修——①给硬删聚合的 seq 铸造改用不被删除影响的单调源（`sqlite_sequence` 风格 AUTOINCREMENT 影子表，或独立 `seq_counters(table, next)` 行，事务内 +1）；②或对这 8 张表改 tombstone、禁止硬删；③守卫层加一条"可删表 nextseq 断言 non-reuse"的红灯测试（删除 max 行后下一行 seq 必须更大）。成本 M。
- 状态：已亲验（user 聚合复现；其余为同构代码路径静态确认）

### R05-M3｜迁移 00016 对存量"跨项目同 host+path"无清数/前置校验，前滚会失败阻断启动；前进测试从 v28 起覆盖不到该类
- 维度：可靠性 / 测试与守卫
- 严重度：M（升级期可用性：失败发生在 `Open→migrate`（`state.go:114-117`），daemon 无法启动，恢复手段=恢复 Platform Backup；触发需要存量数据形态，pre-release 环境下概率低，但属"升级纪律"缺口）
- 位置：`migrations/00016_routes_global_host.sql:1-8`；前置 schema `migrations/00003_routes.sql:20`（旧索引只到 `(project_id,host,path)`）；前进测试覆盖起点 `migrations/forward_test.go:68-110,115-131`
- 证据：
  ```sql
  -- 00016 注释声称"host 数据已保证唯一"，但 00003 期间 schema 允许跨项目重复
  DROP INDEX idx_routes_project_host_path;
  CREATE UNIQUE INDEX idx_routes_host_path ON routes(host, path) WHERE deleted_at = '';
  ```
  复现（overlay，migrations 包）：goose 前滚到 v15 → 插入两个不同 project 的 `('example.com','/')` 活跃行（旧 schema 合法）→ `goose.Up` 在 00016 失败：
  `ERROR 00016_routes_global_host.sql: ... constraint failed: UNIQUE constraint failed: routes.host, routes.path (2067)`；库停在 v15，`persistence.Open` 返回错误。
  前进测试锚点：`TestMigrateForwardFromEra28ToLatest` 只从 v28 起，种子只落 revisions/outbox 两行，不覆盖 routes 等表的历史数据；`TestMigrateForwardFromRecentCheckpoints` 只测 30/31/32。
- 影响：任何曾跨项目创建同名 host 的存量库（00016 之前合法）升级即卡死；无迁移内修复步骤、无法启动回滚。同类"收紧型迁移"目前仅此一处（00010/00018 均为松弛方向，安全），但纪律缺口是"收紧迁移必须有数据前置校验/修复 + 前进测试覆盖早期版本"。
- 建议：①对该类迁移在 Up 前加 `SELECT ... GROUP BY host,path HAVING COUNT(*)>1` 检查并按文档指引清理（或先 tombstone 冲突行再建索引）；②前进测试增加"最早期库 + 关键业务表有数据"的检查点（至少 v1/v15）；③把"收紧迁移不得静默假设存量唯一"写进迁移纪律文档/守卫。成本 S（检查）+ M（测试矩阵）。
- 状态：已亲验（复现测试）

### R05-M4｜outbox 保留窗按 `created_at` 裁剪而非 seq 前缀：墙钟跳变下产生"中间洞"，订阅方不触发 410 而静默丢事件
- 维度：可靠性 / 功能
- 严重度：M（违反 ADR-0026 订阅契约"窗内不删不改 + 断档 410 重同步"；触发条件为墙钟非单调（VM 快照/错误 RTC/NTP 大步校正跨过 7d 边界），低频但静默——丢事件无任何错误面）
- 位置：`internal/infrastructure/persistence/outbox/repo.go:89-100`（TrimBefore 按 created_at）；`:81-87`（EarliestSeq=MIN(seq)）；`internal/interfaces/grpc/fleetlygrpc/contexts.go:384-410`（goneCheck 只比 after_seq < earliest）；裁剪调用 `internal/assembly/provides.go:747`
- 证据：
  ```go
  // outbox/repo.go:92
  func (r *repo) TrimBefore(ctx context.Context, cutoff time.Time) (int64, error) {
  	res, err := r.run.ExecContext(ctx,
  		`DELETE FROM outbox WHERE created_at < ?`, state.FormatTime(cutoff))
  ```
  ```go
  // contexts.go:402 —— 断档判定假定被删行是 seq 前缀
  gone := (earliest > 0 && afterSeq < earliest) || (earliest == 0 && last == 0)
  ```
  复现（overlay，outbox 包）：按 seq 插入 1(created_at=未来 2026-02-01)、2/3(2026-01-01)→ 以 2026-01-04 为 cutoff `TrimBefore` → 删掉 seq 2,3 保留 seq 1；`EarliestSeq=1`、`LastSeq=1`，持有 cursor=1 的订阅方 **不满足 after_seq < earliest**，`ListAfter(1)` 返回后续事件并静默跳过 2/3。
- 影响：事件流（gRPC/SSE/CLI wait/Console）在墙钟跳变后可能漏掉状态迁移；`EarliestSeq/410` 机制无法发现，客户端不会重同步，视图停在旧状态直到下一次事件。同类按 created_at 清理的 `hook_deliveries`（`hook/repo.go:140-155`）只是去重锚，影响较轻。
- 建议：改为 seq 前缀裁剪：`DELETE FROM outbox WHERE seq <= (SELECT COALESCE(MAX(seq),0) FROM outbox WHERE created_at < ?)`（保留"窗外行可多删"的语义但保证前缀），并补"时钟跳变"单测（可控 created_at 的 INSERT 即可，如本复现）；若坚持按时间裁剪，需把 `earliest` 语义改成"最大的连续前缀末尾"并据此 410。成本 S。
- 状态：已亲验（复现测试）

### R05-M5｜SQLite 连接池收口为 1：WAL 的读并发收益被作废，任一长扫描把全局（API/事件流/收敛环）变成串行尾延迟
- 维度：性能 / 可靠性
- 严重度：M（架构层已声明为待实测设计债；N0 单节点小团队规模可承受，但审计/事件/构建/运行历史单调增长使退化无界，且无扫描预算/超时/观测面；不属数据正确性）
- 位置：`internal/infrastructure/persistence/state.go:98-109`（DSN + `db.SetMaxOpenConns(1)` + 注释）；并发面：`engine/options.go:171-172`（`Ring.Tick` 默认 1s）、`engine/construct.go:110-122`（11 个环同 tick 并行）、`engine/loop.go:59-74`；读径同池：`assembly/provides.go:172-175, 183-192`
- 证据：
  ```go
  // state.go:98-109
  dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&..."
  db, err := sql.Open("sqlite", dsn)
  // SQLite 单写者本性：连接池收口为 1，消灭并发写互踩…读池分离待实测瓶颈再议
  db.SetMaxOpenConns(1)
  ```
  11 个收敛环共用该池、默认 1s 节拍；read repo 也由同一 `db.Runner()` 构造（`NewStores`），因此 WAL 允许多读的收益在进程内不可用。已知全表扫描点（M6 列出）在每轮 tick/每 10min janitor 上运行。
- 影响：①任意长查询（审计历史增长后的过滤查询、上传清扫相关子查询、备份保留滚动）执行期间，全部 API 请求、事件 follow 轮询（250ms/流，`contexts.go:382`）、状态迁移都排队 → p99 尖刺/超时；②单个慢查询可同时拖慢所有域，形成"单点串行化"故障放大面；③无 SQL 耗时/慢查询观测，退化只能靠外部症状发现。
- 建议（机制层）：①读池分离——WAL 下多读连接安全，`MaxOpenConns(1)` 只约束写池；②为 janitor/后台扫描加独立连接与耗时预算（`context.WithTimeout` 已有，但缺"扫描量"界）；③补慢查询/DB 队列深度指标（本模块与 R13/R09 共建）。成本 M。
- 状态：已亲验（代码与装配静态确认 + 查询计划；未做压测）

### R05-M6｜关键查询缺索引/全表扫描，且随数据量单调恶化（审计过滤、事件裁剪、runs/builds 列表、上传清扫、备份裁剪）
- 维度：性能
- 严重度：M（当前小数据量下不致命，但与 M5 的单连接叠加形成全局阻塞；其中 audit 过滤与 outbox 裁剪随数据量无界恶化）
- 位置与证据（`EXPLAIN QUERY PLAN` 实测，schema 由 33 个迁移原样建库）：
  - `audit/repo.go:54-83`（ListFiltered）— actor/action/resource 过滤**无索引**，计划 `SCAN audit USING INDEX idx_audit_seq`：过滤查询随审计总量线性扫描。`migrations/00001_core.sql:120-121` 只有 `idx_audit_seq/idx_audit_created`，`00017:8` 只有 team。
  - `outbox/repo.go:92-99`（TrimBefore）— `DELETE ... WHERE created_at < ?` 无 created_at 索引，计划 `SCAN outbox`；janitor 每 10min 执行（`provides.go:695,747`）。
  - `run/repo.go:70-80`（ListByProject）— `runs` 无 `(project_id,seq)` 索引（`migrations/00008_tasks_runs.sql:45-48`），计划走 `idx_runs_seq(seq<?)` 反扫全表再过滤项目；按 project 拉 Run 历史是 API 常规读。
  - `build/repo.go:81-97`（ListByRevision）— `builds.revision_id` 无索引，计划 `SCAN builds USING INDEX idx_builds_seq`；每部署步/观测路径调用（`engine/buildsource.go:72`、`engine/observ.go:81`）。
  - `sourceupload/repo.go:118-124`（SweepUnreferenced）— 对每颗过期上传行执行相关子查询 `NOT EXISTS (SELECT 1 FROM revisions rv WHERE rv.spec LIKE '%'||u.id||'%')`（BLOB 全量 LIKE，无索引可用），计划 `SCAN rv`；janitor 每 10min（`assembly/provides.go:785-812`）→ O(过期上传行 × revisions 总数 × spec 长度)。
  - `backup/repo.go:221-239`（DueForPrune）与 `:137-142`（DueForFailedRowSweep）— 全表 `SCAN backups` + `ORDER BY created_at` 临时 B 树；`engine/backup.go:44-50` 的 `backupStep` 每 1s 节拍都调 `prunePass`（`:557,576`），且 `OldestPending`（`backup/repo.go:86-105`）每次 `COUNT running` 也全表扫。
  - `hook/repo.go:140-155`（RecordDelivery）— 每个 webhook 交付都 `DELETE FROM hook_deliveries WHERE received_at < ?`（无索引，全表扫）。
- 影响：审计表永续增长（ADR 要求不裁剪）使审计过滤查询与全部写路径竞争唯一连接；上传清扫/备份裁剪在事故/积压后（staging 曾出现 11.4 万失败备份行）放大为秒级扫描；`runs/builds` 列表在历史累积后逐页变慢。
- 建议：逐条补索引（`audit(team_id,seq)` 已有、需 actor/action 前缀/资源面的可用形态或改 seq 游标分页；`outbox(created_at)`；`runs(project_id,seq)`；`builds(revision_id,seq)`；`backups(created_at)`；`hook_deliveries(received_at)`）并把"大表全扫"改成有界查询（如上传清扫改成"逐 Revision 反查引用的 upload"或对 upload id 建反向表）；配合 M5 的读池。成本 M。
- 状态：已亲验（迁移原样建库 + EXPLAIN QUERY PLAN；调用方频次已读）

### R05-M7｜审计读面：无游标分页 + 过滤列无索引——永续账本 API 只能读最近 ≤1000 行，更老历史不可回读
- 维度：可观测性 / 功能
- 严重度：M（取证/排障面：ADR-0075/0066 把审计定为"严格只增、不裁剪"的权威账本，但读面没有游标，超过 1000 条后早期行不可达；代码与契约无任何"仅最近 N 条"的声明）
- 位置：`internal/infrastructure/persistence/audit/repo.go:47-101`（Filter 无 cursor、`ClampLimit(...,1000,1000)`、`ORDER BY seq DESC LIMIT ?`）；契约 `proto/fleetly/identity/v1/identity.proto:582-588`（ListAuditRequest 只有 source/action/resource/limit/actor）；handler `interfaces/grpc/fleetlygrpc/identity.go:581-605`（limit 缺省 100、上界 1000）
- 证据：
  ```go
  // audit/repo.go:52-56、82-83
  // ListFiltered 按过滤返回审计（新→旧；limit 上界钳制）
  f.Limit = persistence.ClampLimit(f.Limit, 1000, 1000)
  query := `SELECT ... FROM audit`  // 无 after_seq 条件
  ...
  query += " ORDER BY seq DESC LIMIT ?"
  ```
  ```proto
  // identity.proto:582
  message ListAuditRequest {
    string source = 1; string action = 2; string resource = 3;
    int32 limit = 4;     // 上界 1000
    string actor = 5;
  }
  ```
- 影响：审计只增不删 → 超过 1000 条后更早记录仅能靠平台备份/直接读库取回；合规/事故复盘场景（"谁在何时改了这个资源"）可能查不到；同时过滤查询无索引（M6）使其随时间变慢。跨模块：proto 需 R08 增 `after_seq`（only-add）。
- 建议：repo 的 `audit.Filter` 增 `AfterSeq` 并改为 `seq < ?` 游标（与 events/revisions 的 after_* 惯例一致），proto 只增字段 + CLI/Console 透传；补"跨 1000 行翻页"测试。成本 S（repo）+S（proto/CLI，R08/R10 协同）。
- 状态：已亲验（repo + proto + handler 阅读；未运行 API）

### R05-M8｜`statefacade` 守卫的聚合表清单停在 00005 时代（19/38），约一半聚合表不在根包反扫面
- 维度：测试与守卫
- 严重度：M（守卫是"根包禁止聚合查询"的第二层防线；第一层文件白名单仍有效，但新聚合表名不会触发红——守卫声称覆盖面比实际大）
- 位置：`internal/guards/statefacade_test.go:23-27`（aggregateTables 列表）对照迁移建表全集
- 证据：
  ```go
  // statefacade_test.go:23
  var aggregateTables = []string{
  	"projects", "apps", "revisions", "deployments", "builds",
  	"outbox", "audit", "nodes", "secrets", "configs", "volumes", "networks", "routes",
  	"users", "teams", "roles", "memberships", "tokens", "invitations",
  }
  ```
  迁移里已存在的 `tasks/runs/schedules/network_peers/change_freezes/source_uploads/databases/backups/log_cursors/alert_rules/notification_channels/shared_variables/template_catalog/browse_sessions/app_variables/github_apps/app_hooks/hook_deliveries/idempotency_records`（19 张）不在列；根级文件里写 `FROM tasks` 不会红。
- 影响：后续若有人把聚合查询塞回根包（历史教训"state=2.9 万行门面"），只要表名不在旧清单就漏检；清单本身是 schema 的第二份真源，会随迁移继续腐烂。
- 建议：不维护手写表清单——从 `migrations/*.sql` 解析 `CREATE TABLE` 集合（或从各聚合包的 SQL 反扫）动态生成扫描面；补 shape 用例（新增表名即纳入）。成本 S。
- 状态：已亲验（对照 33 迁移建表名逐一比对）

### R05-L1｜`browse.Delete` 用 `err == sql.ErrNoRows` 判 DELETE，分支不可达（死代码），"幂等成功"实际靠不检查 RowsAffected
- 维度：可维护性
- 严重度：L（行为正确：DELETE 不存在行本就返回 nil 影响；仅死代码误导读者）
- 位置：`internal/infrastructure/persistence/browse/repo.go:85-92`
- 证据：
  ```go
  func (r *repo) Delete(ctx context.Context, id string) error {
  	_, err := r.run.ExecContext(ctx, `DELETE FROM browse_sessions WHERE id = ?`, id)
  	if err == sql.ErrNoRows { return nil }   // ExecContext 不会返回 ErrNoRows
  	return err
  }
  ```
- 影响：无功能影响；误导读者的错误处理面。建议删分支/改注释。成本 S。状态：已亲验。

### R05-L2｜"重复删除"的幂等语义跨聚合不一致：多数资源二次 DELETE 返回成功，部分返回 ErrNotFound → API 200 与 404 分裂
- 维度：功能 / 架构一致性
- 严重度：L（用户可见语义不统一；单次删除正确；调用方/客户端无法依赖统一的重试语义）
- 位置：成功（幂等）：`project/repo.go:164-179`（0 行回查含 tombstone→nil）、`githubapp/repo.go:131-144`、`route/repo.go:120-132`、`volume/repo.go:66-72`、`browse/repo.go:86-92`、`sourceupload/repo.go:109-112`、`backup/repo.go:242-245`；返回 NotFound：`app/repo.go:166-182`、`database/repo.go:139-157`、`secret/repo.go:132-146`、`appvariable/repo.go:121-136`、`sharedvariable/repo.go:124-139`、`user/repo.go:108-124`、`team/repo.go:77-93`
- 证据（两例对照）：
  ```go
  // project/repo.go:172-177 —— 已删 → 回查成功 → nil（幂等 200）
  if n, _ := res.RowsAffected(); n == 0 {
  	if _, err := r.Get(ctx, id); err != nil { return err }
  }
  return nil
  // app/repo.go:174-180 —— 已删 → ErrNotFound（404）
  n, err := res.RowsAffected(); if err != nil { return err }
  if n == 0 { return state.ErrNotFound }
  ```
- 影响：同一 DELETE 动词按资源类型产生不同可重试语义（Agent/CI 无法统一"删除已删=成功"）；审计/事件面在幂等成功路径不落行（重复 DELETE 无痕迹）与 404 路径不同。建议在 domain Repo 契约层统一口径（建议统一为幂等成功或统一 404，并在 API 文档写明），补跨聚合一致性测试。成本 S（口径决策）+S。状态：已亲验。

### R05-L3｜状态词表在 SQL 里被硬编码（run driving/active、deployment 幂等索引），与 domain 单源之间无守卫咬合
- 维度：测试与守卫 / 架构一致性
- 严重度：L（当前词表一致、行为正确；latent：新增/改名状态时静默漏驱动或部分唯一索引变窄）
- 位置：`run/repo.go:83-104`（`state IN ('pending','running','stopping')`）、`:124-144`（`state IN ('pending','running')`）对照 `domain/automation/run/run.go:31-35`（只有 `ActiveStates`，无字符串 helper）；`migrations/00001_core.sql:75-79` 与 `00018_idempotency_app_scope.sql:14-16`（活跃态字面量表）对照 `domain/delivery/deployment/deployment.go:44-59`（`ActiveStates()/ActiveStateStrings()`）
- 证据：
  ```go
  // run/repo.go:101-103 —— 字面量词表，domain 无对应单源
  selectCols+" WHERE state IN ('pending', 'running', 'stopping')"+
  	" AND task_id IN (SELECT id FROM tasks WHERE state IN ("+persistence.Placeholders(len(states))+"))"
  ```
  对照：task/deployment 已收编字符串词表并有测试咬合（`task/repo_test.go:307-320` 的 `TestTaskTerminalVocabularySingleSource`），run driving 集与迁移索引字面量无同类测试；`transitionguard`/`entityid_guard` 也不检查。
- 影响：新增状态（如 run 引入 `paused`）时 `ListDriving` 会静默漏行（Run 永不驱动）；deployment 新增活跃态时部分唯一索引不覆盖 → 同 idem 键可双活跃，admission 去重静默失效。建议：run 增 `ActiveStateStrings()/DrivingStateStrings()` 单源；迁移索引谓词由守卫检查与 domain 词表一致（或把字面量生成写入迁移注释+对账守卫）。成本 S。状态：已亲验（字面量与词表对照）。

### R05-L4｜`Transit` 的 UPDATE 列白名单会静默丢弃未列入的 mut 字段（未来加字段的陷阱）
- 维度：可维护性
- 严重度：L（当前所有 mut 只写已列入字段，无现存缺陷；但接口 `mut func(*Row)` 允许任意改，改到未列入字段即静默不落库）
- 位置：`deployment/repo.go:218-227`、`task/repo.go:148-155`、`run/repo.go:169-176`、`build/repo.go:136-140`
- 证据：
  ```go
  // task/repo.go:148-155 —— mut 可改 Task 任意字段，但只有 4 列 + 终态列落库
  res, err := r.run.ExecContext(ctx, `
  	UPDATE tasks SET
  		state = ?, lease_deadline = ?, desired_concurrency = ?,
  		updated_at = ?, finished_at = ?
  	`+persistence.TransitWhere, ...)
  ```
- 影响：未来（如 ADR-0083 restart 线）在 mut 里改 `DNSName/WorkloadID/OwnerTokenID` 会被静默丢弃，只表现为"改了没生效"的运行期怪异；无编译期/测试保护。建议在 Transit 文档处列明"可写列白名单"并在新增字段时同步；或对 mut 后的行与 CAS 后回读做字段差断言（测试）。成本 S。状态：已亲验（列清单与全部 mut 调用点核对）。

### R05-L5｜三个 Upsert 的注释声称"tombstone 行复活"，实现实际是"插入同名新行"（文档-代码漂移）
- 维度：文档一致性
- 严重度：L（行为本身安全：部分唯一索引只约束活跃行，重建合法；只是注释误导，且反复建删会积累 tombstone 行）
- 位置：`secret/repo.go:25-57`（注："同名活跃行覆盖…tombstone 行复活"；`GetByName` 过滤 `deleted_at=''` 使复活臂不可达）、`appvariable/repo.go:25-58`、`sharedvariable/repo.go:27-60`
- 证据：
  ```go
  // secret/repo.go:29 —— 只查活跃行；tombstone 行永远走不到 UPDATE 臂
  if existing, err := r.GetByName(ctx, s.ProjectID, s.Name); err == nil {
  	_, err := r.run.ExecContext(ctx, `UPDATE secrets SET ciphertext = ?, ... deleted_at = '' WHERE id = ?`, ...)
  ```
  `GetByName` 的 WHERE 为 `... AND deleted_at = ''`（`:61-65`）。
- 影响：无功能错误；注释与 `githubapp/appvariable/sharedvariable` 三处同款表述会让读者误以为有 undelete 路径；建议改注释为"tombstone 让位、同名新建"。成本 S。状态：已亲验。

### R05-L6｜`backup.DueForPrune` 无状态谓词：理论上可裁剪 pending/running 行，造成行/对象成对断裂
- 维度：可靠性
- 严重度：L（需要"在途行老于行级保留窗（默认 7d）"才可达，正常环单飞 + 执行有硬超时，概率极低；一旦发生：Finish*/MarkRunning 归 ErrConflict，已写入的对象成为无行可清的孤儿）
- 位置：`backup/repo.go:217-239`（`selectCols + ORDER BY created_at LIMIT ?` 无 status 过滤）；调用 `engine/backup.go:44-50,556-573`
- 证据：
  ```go
  // backup/repo.go:221-224
  func (r *repo) DueForPrune(ctx context.Context, now time.Time, lookahead int) ([]backup.Backup, error) {
  	lookahead = persistence.ClampLimit(lookahead, 100, 500)
  	rows, err := r.query(ctx, selectCols+` ORDER BY created_at LIMIT ?`, lookahead)
  ```
- 影响：极端积压下可能先删断在途行；建议 SQL 加 `WHERE status NOT IN ('pending','running')`（失败行由 48h 清扫面单独负责）。成本 S。状态：已亲验（调用方与生命周期阅读）。

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **R08（契约）/R10（CLI）**：审计读面缺 `after_seq` 游标（M7），需 proto only-add + CLI/Console 透传；events 的 `eventsGoneCheck` 与 outbox 裁剪语义耦合（M4），410 契约文档应写明"被裁剪集合 = seq 前缀"的前提。
2. **R02/R13（引擎写路径）**：`writes.Emit` 被 converge 用作"双真事实"发射（`engine/converge.go:428`），但它按注释只适用于"观测事实/登录审计等无聚合写伴生"的场景；建议 R02/R13 复核该调用是否应升级为只含事实的 tx，或显式 StreamOnly 并在 ADR-0066 侧记录偏离。
3. **R07（upload/objectstore）**：janitor 的上传 GC 是"删行 tx → CountByDigest → DeleteBlob"三段非原子（`assembly/provides.go:794-807`）；与并发 finalize（写 blob + 落行）交错时理论可删掉刚被新行引用的内容寻址 blob。属 R07/R13 面，建议加"blob 引用计数行锁或 GC 前二次校验"。
4. **R04（backup 环）**：`backupStep` 每 1s 调 `prunePass`，其中两次全表扫描（M6）；且 `OldestPending` 用全局 `COUNT(running)` 做单飞闸（`backup/repo.go:86-105`），与 `DueForPrune` 无状态谓词（L6）共同构成"积压→扫描放大"面。
5. **R09（装配/启动）**：迁移失败即 `Open` 失败、daemon 不启动（`state.go:114-117`）；M3 的收紧迁移类问题需要 runbook/install 侧有"升级前数据检查"步骤或迁移自检查。
6. **R01（domain Repo 契约）**：37 个聚合 Repo 接口与 persistence 实现一一对应（编译期由 `New` 返回 domain 接口保证）；但"软删后读语义"在聚合间分叉：`project.Get` 含 tombstone（`project/repo.go:52-58` 注释"tombstone 是事实"）而 `app/database` 的 Get 一律隐藏 tombstone（`app/repo.go:48-55`）。当前调用方都正确处理（`requireActiveProject` 显式 `p.Deleted()` 判定），但契约层建议在 R01 明确"哪些聚合的 Get 含 tombstone"，否则新调用方易踩。
7. **R14（安全）**：审计/事件 Team 轴在 persistence 侧实现正确（写侧铸入、读侧 `team_id=? OR ''`），但 `outbox` 行的 `team_id` 不进 Event 消息（迁移 00032 有意为之）；跨队可见性依赖所有写点正确铸 team——本模块看到 `app.teardown_abort` 等少数平台级事件用 `''` 且有注释理由，建议 R14 从写点侧再核一遍"该铸未铸"的面。
8. **R12（守卫）**：本模块相关的守卫（statekitlaw/statefacade/migrationchainanchor/transitionguard/acceptanceguard/uowguard/entityid/aggregatetestbelt）在现树全绿，但存在两类结构性盲区：字面量 schema 副本无对账（M8、L3），以及"NextSeq 只在事务内原子""可删表 seq 不复用""trim 必须是前缀"三条不变量无守卫（M1/M2/M4 的根因）。建议 R12 评估把这三条做成守卫（AST/单测均可）。

## 6. 测试与守卫质量评估

**覆盖强度**：37/37 聚合有 repo 测试（G19 守卫强制，`aggregatetestbelt_test.go:102-114`），94 个测试函数、4491 行测试代码；全部用真 SQLite + 假时钟（`statetest.New`），无 `t.Skip`/`t.Parallel`。核心域（deployment/task/run/audit/outbox/revision/secret/idempotency/backup）的错误分支覆盖扎实：CAS 冲突文案、终态封锁、钳位三形态、游标越界、唯一冲突归一、FK RESTRICT 归一都有断言。

**断言强度**：正向强于大多数仓——有"确定性交错"而非 sleep（`deployment/repo_test.go:66-96`、`secret/repo_test.go:94-157`）、有词表与谓词咬合断言（`task/repo_test.go:307-320`、`run/repo_test.go:190-221`）、迁移前进测试断言了回填语义而不是"没报错"（`forward_test.go:86-109`）。

**假阳性/假阴性风险**：
- 假阴性（本报告 M/L 的共同根因）：测试几乎都在"单连接 + 事务内"形态下运行，无法覆盖 ①非事务 `NextSeq` 并发（M1）、②硬删后 seq 非复用（M2）、③墙钟非单调下的裁剪（M4）、④慢查询对全局的影响（M5/M6）；`database/repo_test.go:131` 还把"ID 永不复用"写成未经实现的假设。
- 假阴性（守卫面）：`statefacade` 表清单陈旧（M8）；`statekitlaw` 有防盲阈值与 shape 用例，质量高；`migrationchainanchor` 只对账最新版本与检查点，不对账"迁移假设的数据形态"（M3）。
- 假阳性风险低：CAS 并发用例的同步点设计正确（mut 内阻塞），不会把"没触发冲突"误判为通过。

**守卫是否真拦得住典型错误**：对"绕过组合点手写四件一""root 门面回潮""手写钳位/占位串""迁移链锚漂移""聚合缺测试""Task 行手搓"这六类，守卫有 AST 级红灯与 shape 自证，可信；对"状态词表双源""seq 复用""trim 前缀""非事务 NextSeq"四类，无守卫（本报告 M1/M2/M4/M8/L3）。

## 7. 未决与置信度

**最不确定的 3-5 点**：
1. M5/M6 的**实际影响量级**：未做压测/长跑，1s×11 环在真实数据量（尤其 audit/outbox 增长）下的 p99 退化只能给方向性结论；建议 R13/真机专项实测（可先加慢查询日志）。
2. M1 的**真实触发频率**：取决于 converge 事件频率与 API 审计写并发；静态看低频，但累计危害与"是否应升 H"取决于产品对审计完整性的硬承诺（需 ADR-0066/0075 裁决口径）。
3. M3 的**实机可达性**：pre-release 无旧版本升级用户，00016 是否真实会咬取决于存量的"跨项目同 host"数据是否存在——本报告只证明机制上会失败，未证明某真实库必然命中。
4. M2 的**外部引用风险**：`restore_from_backup` 等悬挂引用命中复用 ID 的链条较长，未构造端到端复现，仅列理论路径。
5. 迁移 00001–00028 曾被 ADR-0070"原位修订"，本报告的"schema 与代码一致性"只对照了现树 33 个迁移（自建库 + 全包测试全绿），未核历史升级链上的每一版；`forward_test` 从 v28 起也限制了这个结论的强度。

**整体置信度**：中高。M1/M2/M3/M4/M8 均有代码 + 可复现测试证据；M5/M6/M7 为静态 + 查询计划证据（未压测）；L 级均为已亲验的代码事实。跨模块结论已注明归属模块，未越界下最终裁决。
