# R13 横切：集成、事务、数据流与并发 评审报告

- 评审者：r13（task-R13，横切只读）
- 日期：2026-10-19；仓库快照：`D:\Codes\qiulin\fleetly` 分支 main HEAD `35257f18`（工作树干净）
- 基线：`PROFILE.md` §0-§5 + `MODULES.md` §R13；已读同目录 `findings/` 下 R01/R02/R04/R05/R06/R07 六份报告，只吸收线索、**独立核实、不重复计数**
- 结论：写路径主链（拦截器 → 受理位 commit → application → 四件一拍 → outbox/audit → 事件消费）的**事务收口与守卫是本仓最强的一环**（见 §3）；跨模块缺口集中在三处**"账本/预算口径与真实执行面不同源"**：期望账本没有退场面（M1）、关停预算被框架默认值截断（M2）、外部副作用与账本无统一补偿原语（M3）。计 3 M + 3 L，无 H。

## 1. 覆盖清单

### 完整读过（生产文件）
- **engine 壳与生命周期**：`engine.go`、`options.go`、`construct.go`、`lifecycle.go`、`loop.go`、`ensure.go`
- **写路径与事实发射**：`transition.go`、`materialize.go`、`admission.go`、`deployspec.go`、`drive.go`、`bluegreen.go`、`projection.go`、`peers.go`、`teardown.go`
- **收敛/观测/漂移**：`converge.go`、`observ.go`、`observdomain.go`、`drift.go`
- **受管/数据/维护**：`managed.go`、`database.go`、`rotate.go`、`networkrebuild.go`、`hygiene.go`、`routepublish.go`
- **构建面（横切关注段）**：`builder.go`、`buildsource.go`（:40-160）
- **接口层写/读链**：`interfaces/grpc/fleetlygrpc/acceptance.go`、`wait.go`、`contexts.go`（Events 面 :330-494）、`structure.go`（:218-349, :410-529）、`databases.go`（:185-315）
- **事务/幂等/事实 kernel**：`application/transact/transact.go`、`application/idem/enforcer.go`、`domain/services/writes/writes.go`
- **装配/进程**：`assembly/provides.go`（:620-899 全部 janitor/hook/service 段）、`cmd/fleetlyd/main.go`、`cmd/fleetlyd/admin.go`（重启/维护面）

### 抽样读过
- `engine/backup.go`（:30-80 退避/单飞、:180-300 执行+回执、:380-520 恢复）、`platformbackup.go`（:100-280 快照/仓/排除集）、`events.go`（:1-120 payload schema）、`task.go`/`taskobs.go`（仅锁面与缓存清点）、`browse.go`（仅 maintenance 锁面）、`execrelay.go`（仅 send/finish 交叉）
- `assembly/gateway*.go`（代理配置端点与 cleanup）、`interfaces/admin/**`（仅 rewrap 动词）
- 外部依赖语义（只读模块缓存）：`lynx@v1.20.0/{shutdown.go,options.go,lifecycle.go}`——用于核实"关停预算"与"StopTimeout"的真实语义（M2 的关键证据）

### 未覆盖/无法覆盖
- **e2e dind / 真机**：本机 Docker daemon 不可达（PROFILE §1.1），蓝绿双代窗、网络重建、升级矩阵均只做静态路径推演，未真机复验。
- **未构造并发复现**：M1（隔离自激环）与 L1（网络重建 × 删除交错）均为静态全链推演，未写注入时序的并发用例（本轮只在只读面取证；`-overlay` 复现留待 Lead 裁决是否需要）。
- **未跑测试**：本轮为纯静态 + 模块缓存取证（R02/R04/R05/R06 已各自跑过 engine 全包、`-race` 子集与 guards，结论一致）。未读 `internal/guards` 源码细节（R12 主审），守卫结论按 R05/R12 报告交叉引用。
- providers 实现细节（R06/R07 主审）、SQL 与迁移（R05）、API 契约（R08）、CLI（R10）仅按需交叉阅读。

## 2. 模块画像与边界

R13 不拥有文件，拥有的是**四类接缝的"同一性"**：

1. **写路径全链的同一事务性**：`commit`（API 受理位）与 `commitTransition/commitWrite`（engine）是两条同构前门，事实双行由 `writes.Sequence` 在**调用方事务**内扇出（`writes.go:27-32,75-85`）；事务唯一入口是 `transact.Tx.Do`（`transact.go:118-123`）。
2. **期望账本/收敛缓存的写入纪律**：`ensuredSpec/ensuredGen/expected/ensuredGen` 只由 Ensure 路径写（`recordEnsured` 单点，`observ.go:110-125`），观测播种被明文禁止（`observdomain.go:44-61`）。
3. **锁序与单写者**：`appLocks`（per-App）+ `ensureMu`（五张 memo 共用）+ `maintenanceMu`（RWMutex，网络重建写半边）+ `lifecycleMu`（Start/Stop）+ 每环一个 `Loop`（唯一 ticker 骨架）。
4. **启动/关停/崩溃恢复**：`Start`（`lifecycle.go:20-69`）同步跑孤儿构建重置与中断备份清扫、异步跑基线重放与漂移环；`Stop`（`:122-141`）取消 + `wg.Wait`。

**与架构/ADR 声明的偏差（详见 §4）**：
- "期望账本只由 Ensure 写"成立，但**"Ensure 域内收敛移除载体时账本同步退场"从未声明也从未实现**（M1）。
- "优雅退出：in-flight Ensure 可安全中断重放"（`lifecycle.go:117-121`）的**实际排水预算不是引擎自己的参数**，而是框架 `StopTimeout` 缺省 5s（M2）。
- 外部副作用（容器/数据面/对象仓/上游 API）与账本落库之间**没有共享原语**，六处形态各异（M3）。

## 3. 优点（带证据）

1. **事务组合点是真收口，且被 AST 守卫反向执法**。API 侧只有 `Services.commit` 一个写入口（`acceptance.go:54-68`），engine 侧只有 `commitTransition/commitWrite`（`transition.go:95-162`），事实双行由 kernel 单声明扇出、`LedgerOnly/StreamOnly` 偏离必须显式带旗（`writes.go:98-127`）。`transact.Stores` 把 37 个聚合 repo + `NextSeq` 一起绑进事务（`transact.go:56-103`），所以"CAS + 事件 + 审计 + seq 铸造"天然同事务——这是我逐条追写路径后唯一没有找到绕行点的结构性保证。

2. **锁序无环，且锁都有单一归属面**。实测全仓 `lockApp` 只在 3 处（admission/converge/teardown）、`lockMaintenance` 有 11 处调用点（materialize/managed×1/database×2/browseloop×2/bluegreen/rotate/backup×2/task）、`maintenanceMu.Lock` 只在 `RebuildNetwork`；嵌套方向恒为 `appMu → maintenanceMu(R)`（`converge.go:318-350`、`admission.go:96-103`）与 `taskMu → maintenanceMu(R)`（`task.go:146,470`），**没有反序路径**（`RebuildNetwork` 持写锁时只调 provider 与 repo，不触 appMu/taskMu，`networkrebuild.go:78-147`）。`ensureMu` 只在三条 memo 原语内短暂持有，从不跨调用（`ensure.go:31-53`）。

3. **收敛动作的双复查是"设计意图 → 机制载体"的正确翻译**。`attemptConverge` 在 `appMu` 内先查 teardown 标记、再查 `DrivingByApp`（驱动权语义，含 failed 待回滚窗）、再**重读基线行并核对 (revision,gen)** 才 materialize（`converge.go:322-350`）——第三道复查把"扫描到加锁之间基线已更替"的窗口显式闭合，且注释说明了为什么不能用 `ActiveByApp`（`:329-331`）。

4. **事件流消费是"轮询权威表"而非"内存广播"**。`subscribeEvents` 从 outbox 按 seq 升序重放 + 250ms 拍跟随，`wait` 原语先取 `LastSeq` 再发首帧（`wait.go:110-132`），两步间落下的事件只会多触发一次幂等重读——"事件只是信号、行重读是真源"这句话在代码里是成立的。SSE 票据、Team 轴过滤、410 重同步都在同一订阅核上（`contexts.go:384-410,454-485`）。

5. **崩溃恢复的三个面都有明确的对称动作**：`Start` 同步把 `building` 行重置 `queued`（`lifecycle.go:97-115`）+ 在途备份行清扫（`:32`），异步基线重放按 App 串行且在 `appMu` 内复查（`drift.go:51-96`）；构建输入的进程内登记丢失由 `driveBuilding` 幂等重建（`buildsource.go:82-87,139-159`），并在"无登记无归属"时一跳 `cancelled` 而不是 queued↔building 弹跳（`builder.go:80-102`）。

6. **预算化执行面是普遍纪律而非孤例**：`boundedStep` 统一给 materialize/受管/数据库/删除/清扫派生硬上限（`ensure.go:61-68`），带界获取顺序被显式规定为"锁等待不占步预算"（`networkrebuild.go:221-228`），provider 侧 no-op 断路器 + 内容等价让 60s 强制重放近零成本——这条链是我认为"性能面没有系统性问题"的主要依据。

## 4. 发现清单

### R13-M1｜期望账本没有退场面：蓝绿退役代条目永久残留，使"跨域附件隔离不变式"进入永不自愈的 30s 重收敛环
- 维度：可靠性（兼架构一致性／性能）
- 严重度：M（校准：不损坏数据、不影响在跑流量；但 ADR-0082 §2"防自激三件套"与 §4"隔离是收敛不变式"这两条明文承诺在"BG 退役代 + 跨域引用失效"组合上**没有不动点**——每个漂移拍都重跑一次完整 materialize，无事件、无审计、无日志，属可复现的系统性错误而非一次性缺陷）
- 位置：`internal/application/engine/observdomain.go:53-61`（唯一写点）、`:102-118`（唯一清理点，仅 teardown 调用）、`:259-274`（隔离不变式的数据源）；`internal/application/engine/bluegreen.go:155-191`（双代窗把旧代记入账本）；`internal/application/engine/drive.go:296-305`（收口只记新代）；`internal/application/engine/peers.go:175-216`（复核 → 无条件重放）；`internal/application/engine/converge.go:289-356`（重放不查 memo）
- 证据：
  ```go
  // observdomain.go:53-61 —— 只写不删：ENSURED 账本无"退场"语句
  func (o *observations) recordSpecs(owner workloadOwner, gen uint64, ws []capability.Workload) {
      o.mu.Lock()
      for _, w := range ws {
          o.workloadApp[w.ID] = owner
          o.ensuredGen[w.ID] = gen
          o.ensuredSpec[w.ID] = w
      }
      o.mu.Unlock()
  }
  // bluegreen.go:163-167,191 —— 窗内把「旧代成员」也记进账本（含 NetworkRefs）
  for _, w := range fromWS { if bg[w.Process] { union = append(union, w) } }
  e.recordEnsured(d, fromGen, union)
  // drive.go:301 —— 收口只记新代：旧代 ID（-g<old>）从此无人改写/删除
  if err := e.materialize(ctx, d, d.ToRevision, d.Generation, false); err != nil {
  // observdomain.go:263-271 + peers.go:203-214 —— 不变式读账本 + 失配即重放（无签名门）
  for wid, ensured := range o.ensuredSpec {
      if len(ensured.NetworkRefs) == 0 { continue }
      ...
  if !bad { continue }
  d, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
  ...
  for _, a := range stale { e.replayAppBaseline(ctx, a, staleDepl[a.ID]) }
  ```
- 影响：触发链为 **①App 含 blue-green 进程 + ②某代声明了 `project:<id>/<net>` 跨域引用 + ③该引用之后失效（peer 撤销 / 目标网络删除 / 目标 Project 删除）**。③之后：`crossProjectRefs()` 从 `ensuredSpec` 读到**退役代**的 `NetworkRefs` → `peerRefApproved` 恒 false → 每 30s（`DriftScanInterval`）一次 `replayAppBaseline → attemptConverge → materialize`（`converge.go:289-356` 无 `ensureFresh` 门），materialize 只会把**新代**ID 重写一遍（仍无 refs），退役代条目永远留着 → 下一拍重复。实测语义：该 App 每 30s 一次投影+Secret 解密+卷钉住+`runtime.Ensure`（provider 内容等价会 skip，但控制面成本不减），**永久不停**，且 `convergeSucceed(src=baseline)` 早退不发事件/审计（`converge.go:416-418`），运维无从察觉。次生：`workloadApp/ensuredGen/ensuredSpec` 对每个 BG 进程每次部署净增一条（无界增长），`routeExpectations`/`readyGen`/`scanGen` 全是 O(全量) 遍历。
- 建议：给账本加**退场面**——在 BG 收口（`drive.go:300-305`）用"本代保留集"对 `appOwner(appID)` 做差集 `obs.forget(retiredWid)`，或让 `crossProjectRefs` 只消费每个 Process 的**最新代**条目（`ensuredGen[wid] == max`）。修完补一条钉测：BG 两代 + peer 撤销 → 断言"第二次 `driftScan` 不再产生 `runtime.Ensure`"（现有 `TestCrossProjectPeerIsolationSelfHeals` 只用 rolling 形态，`peers_test.go:103-114`，覆盖不到该形态）。成本 S（收口处 3-5 行 + 1 钉测）；**建议机制层**：账本"写入 = 全集替换"而非"逐条累加"，否则任何未来的"按 ID 集合判断存在性"的新消费面都会重犯。
- 状态：已亲验（写点/清理点/消费面三处全仓检索 + BG 收口路径逐行阅读）；未构造并发/多拍复现用例。

### R13-M2｜引擎"有界排水"的实际预算是框架缺省 `StopTimeout=5s`，而非 `ShutdownTimeout=30s`：在途 Ensure/构建/备份被截断后 DB 与 Runtime 仍被 cleanup 关闭
- 维度：可靠性（兼可观测性）
- 严重度：M（校准：不产生数据损坏（SQLite 事务回滚 + 重启重放自愈），但"优雅退出 / in-flight Ensure 可安全中断重放"（`lifecycle.go:117-121`、ADR-0005）的实现强度被一个未显式设置的第三方缺省值决定；关停路径上"引擎最后停"的编排意图因此只对**快步骤**成立）
- 位置：`cmd/fleetlyd/main.go:109-110`（只设 Drain/Shutdown，未设 StopTimeout）；`internal/assembly/provides.go:835-845`（`engineService.Stop → e.Stop(ctx)`，ctx 是 app ctx）；`internal/application/engine/lifecycle.go:122-141`（`Stop` 内部 `select { <-done; <-ctx.Done() }`）；外部语义：`lynx@v1.20.0/shutdown.go:73-81`（`stopServiceBounded` 用 `StopTimeout` 兜底，超时后"跳过并继续"）、`lynx@v1.20.0/options.go:19`（`DefaultStopTimeout = 5s`）
- 证据：
  ```go
  // main.go:109-110 —— 只给了排水窗口与钩子预算，没给单服务 Stop 预算
  lynx.WithDrainTimeout(30*time.Second),
  lynx.WithShutdownTimeout(30*time.Second),
  // assembly/provides.go:842 —— 引擎 Stop 收到的 ctx 由框架给（app ctx，本身无 deadline）
  func (s *engineService) Stop(ctx context.Context) error { return s.e.Stop(ctx) }
  // lifecycle.go:130-140 —— 自带 deadline 分支，但入参 ctx 无 deadline ⇒ 该分支不生效
  go func() { e.wg.Wait(); close(done) }()
  select {
  case <-done: return nil
  case <-ctx.Done(): return fmt.Errorf("engine drain exceeded deadline: %w", ctx.Err())
  }
  // lynx shutdown.go:74-81 —— 真正生效的上界是 StopTimeout（缺省 5s）
  stopCtx, cancel := context.WithTimeout(context.Background(), app.o.StopTimeout)
  ... timedOut → "service stop timed out" → 继续下一个服务（Stop 的 goroutine 泄漏但流程不挂）
  ```
- 影响：引擎单步预算 `ManagedStepTimeout=30s`（`options.go:186-188`）、`NetworkRebuildTimeout=5m`（`:183-185`）、`BackupTimeout=15m`（`:180-182`）、`BuildTimeout=15m`（`:177-179`）**全部大于 5s**。关停时序（`provides.go:881-886` 注释 + `NewServices` 注册序）是 gateway/grpc 先停、engine 最后停——但 engine 只拿到 5s：一个正在 `materialize`（docker API 慢）或正在 `executeOneBackup`（GB 级 dump，pipe 流式）的 step 会被截断；随后 `OnPostStop` 的 wire cleanup 依次 `db.Close()` 与 `runtime.Close()`（`provides.go:245`、`:263-264`），**在 ring/构建 goroutine 仍存活时关闭底层句柄**；`main` 随即返回 → 进程退出。留痕：`building` 行、`running` 备份行、半写对象——分别由 `resetOrphanBuilds`/`sweepInterruptedBackups`/失败行清扫兜（`lifecycle.go:31-32`），故不构成数据事故；但"升级零扰动"的关停半边实际是**5s 硬截断**，且 `Stop` 里那条 `drain exceeded deadline` 错误永远不会出现（ctx 无 deadline），排障时会把"关停不干净"误判成 step 卡死。
- 建议：①`main.go` 显式 `lynx.WithStopTimeout(...)`（≥ `ManagedStepTimeout`，如 35s），并让 `engineService.Stop` 传入自带 deadline 的 ctx（`context.WithTimeout(ctx, opts.Ring.DrainBudget)`），使 `lifecycle.go:138` 的分支真正可达；②把"引擎排水预算"写进 `Options.Ring` 并在 `e2e:dind-upgrade` 增一条"关停期间 mock 慢 Ensure（<预算）+ 断言进程退出前 Ensure 返回"的锚。成本 S。**机制层**：三个预算（drain/stop/shutdown）分属两个框架参数 + 一个引擎参数，建议在 `assembly` 单点算出并互校（如 `StopTimeout = max(步预算) + 余量`），否则新增一个 15m 预算的面就会重开同缺口。
- 状态：已亲验（fleetly 侧调用链 + lynx v1.20.0 关停实现逐行读模块缓存）；未真机观测一次慢关停。

### R13-M3｜外部副作用与账本落库之间没有共享补偿原语：凭证轮换的"数据面已改密、Secret 未换"窗口无任何恢复通道（文档所称的 Browse 恢复路径本身依赖被覆盖的 Secret）
- 维度：可靠性（兼数据安全）
- 严重度：M（校准：窗口 = 一次 utility 返回到一次 `Secrets.Upsert` 之间（毫秒级）+ 崩溃/写错误才可达，概率低；但后果是**该 Database 的凭证永久丢失**：新密码只在内存里、永不回显/落盘，平台与操作者都拿不到；`rotate.go:8-12` 自称的恢复路径（Browse 手工对齐）在实现上不成立——Browse 用的是同一条 `database:<name>` Secret）
- 位置：`internal/application/engine/rotate.go:8-12`（自述窗口）、`:59-78`（改密 → 重封装 → 写 Secret 的顺序）；对照同族形态：`engine/backup.go:275-284`（R04-M5）、`engine/platformbackup.go:216-223`（R04-H3）、`interfaces/grpc/fleetlygrpc/structure.go:427-469`（先收口后落账）、`application/idem/enforcer.go:191-206`
- 证据：
  ```go
  // rotate.go:59-78 —— utility 成功即数据面换密；随后才是 Secret 重写
  if len(spec.Argv) > 0 {
      if err := e.runRotateUtility(ctx, row, tpl, spec); err != nil { return err }
  }
  newURL := tpl.ConnURL(DatabaseDNSName(row.ID), next)   // next 只存在于本次栈帧
  sealed, err := e.cipher.Produce([]byte(newURL))
  ...
  if err := e.repos.Secrets.Upsert(ctx, sec); err != nil {
      return fmt.Errorf("rewrite credential secret %q: %w", row.CredentialsRef, err)
  }
  // rotate.go:8-12 —— 自述恢复路径
  // 两步之间崩溃则数据库已持新值而 Secret 尚旧（引用方断连），恢复走 Browse
  // 会话手工对齐或重试轮换前先经载体重下发回卷（redis 形态自愈）。
  ```
- 影响：`runRotateUtility` 用的是 `ALTER USER ... PASSWORD`/`CONFIG SET requirepass` 一类**破坏性**改密（旧值立即失效）。若 `cipher.Produce` 或 `Secrets.Upsert` 失败（磁盘满、DB busy、KEK 不可用），终态是：数据面只认新密码；平台 Secret 仍是旧密码；**新密码既未落库也未返回给调用方**（`RotateDatabasePassword` 只有 error），审计也只有失败/无行。此后该库既不能经平台连接（Secret 旧），也不能经 Browse「手工对齐」（Browse 需要从 Secret 解出连接串），更无法再次轮换（`databasePassword` 读旧值 → utility 用旧值改密会被数据库拒绝 → 永久 `ErrRotateRejected`）。运维唯一出路是 SSH + 数据面本地改密（跳出平台）。这是"外部副作用 + 账本"这一类里**后果不可逆**的实例；同族的 backup/restore/systemd 面（R04-M5 等）都能靠"行状态 + 重放"自愈，只有这里不能。
- 建议：①改为**两阶段**：先写"待生效凭证"（新密码封进一条 `rotating:` 前缀的 Secret 或同行的 pending 列/审计 detail），utility 成功后再切换主 Secret（切换失败仍有据可查）；②或在 utility 成功后立刻把新值写进 `platform-backups` 之外的恢复通道（如审计 `Detail` 的 age 信封——需 R14 裁决密值进审计面是否可接受），至少让"人工对齐"名副其实；③最小止血：utility 前先 `Upsert` 新值并记 `pending` 审计、utility 失败再回写旧值（redis 形态可自愈，PG/MySQL 不可逆但方向诚实）。成本 M（①/②）。**机制层**：建议把"调用外部破坏性动作"收敛成一个 `ExternalAct{pre-record, act, commit/compensate}` 原语，`rotate`/`platformbackup`/`restore`/`teardown` 四处共用——现在的共同根因是"每处各写一遍先后序，且都没定义补偿"。
- 状态：已亲验（代码路径 + 自述注释 + 同族调用面）；未构造失败注入复现（需要 KEK/磁盘故障缝）。

### R13-L1｜`maintenanceMu` 读半边纪律在 2 个载体写动词上缺失（`TeardownApp` / `SweepTerminalTaskCarriers`），与"全部载体写动词持读锁"的声明不一致
- 维度：架构一致性（兼可靠性）
- 严重度：L（校准：交错后果是网络重建在 `re-attach` 段失败/半完成，返回诚实错误可重试；不产生孤儿载体、不损坏数据）
- 位置：`internal/application/engine/teardown.go:18-53`（`runtime.Remove`，无 `lockMaintenance`）；`internal/application/engine/hygiene.go:91-94`（同）；声明面 `internal/application/engine/networkrebuild.go:14-17`、`engine.go:130-138`（"全部 Ensure 族调用点与 **TeardownDatabase** 持读锁"）；对照合规面 `database.go:352-353`、`task.go:470-471`、`backup.go:187-188,441-442`
- 证据：
  ```go
  // teardown.go:44-53 —— 载体写动词，但无 lockMaintenance（TeardownDatabase 有）
  rctx, rcancel := e.boundedStep(ctx)
  removeErr := e.runtime.Remove(rctx, ns)
  rcancel()
  // hygiene.go:91-94 —— janitor 的终态 Task 残留清扫同样裸调 Remove
  stepCtx, cancel := e.boundedStep(ctx)
  ns := capability.NamespaceRef{Team: team, Project: t.ProjectID, Task: t.ID}
  err = e.runtime.Remove(stepCtx, ns)
  ```
- 影响：`RebuildNetwork` 持写锁做 detach→rm→create→attach（`networkrebuild.go:78-147`），其 re-attach 循环基于 detach 前的快照。并发的 `DeleteApp`（无读锁）可在 detach 后删掉该载体 → rebuild 的 re-attach 命中已消失载体（provider 侧"幂等跳过"，但 swarm 侧 `AttachNetwork` 对不存在 service 报错）→ 重建以错误收场（网已重建、部分载体已挂回，需重试）。反向（Ensure 族）因持读锁不会插入。同类还有 janitor 的清扫（每 10min）与删库级联的交错。全仓共 13 个载体写动词，11 处持读锁、2 处不持——缺口面清晰。
- 建议：两处补 `unlock := e.lockMaintenance(); defer unlock()`（各 2 行）；机制层：在 `internal/guards` 加一条反扫——`engine` 包内出现 `runtime.Remove|Ensure|AttachNetwork|DetachNetwork` 的函数必须在调用前出现 `lockMaintenance`（或显式带豁免注释）。成本 S。
- 状态：已亲验（全仓 `lockMaintenance` 调用点对照 + 两处 Remove 逐行）。

### R13-L2｜幂等拦截器的"24h 重放"承诺在回执写失败时静默退化为 90s，双层去重只对 admission 面兜底
- 维度：可靠性（兼文档一致性）
- 严重度：L（校准：`enforcer.go:188-190` 已显式承认降级并指明兜底；但"兜底存在"只在有业务唯一约束/活动期去重的动词上成立——`CreateToken`/`CreateInvitation`/`SetUserPassword`/`TriggerBackup` 类动词没有等价兜底）
- 位置：`internal/application/idem/enforcer.go:40-43`（`ClaimTTL=90s`）、`:153-169`（Claim → handler → complete）、`:191-206`（complete best-effort）；`application/transact` 面无法覆盖 handler 事务
- 证据：
  ```go
  // enforcer.go:191-206 —— 回执写失败只记日志：认领行的过期时间仍是 ClaimTTL
  func (e *Enforcer) complete(ctx context.Context, key string, resp any) {
      ...
      if err := e.repo.Complete(ctx, key, typeURL, body, Retention); err != nil {
          e.log.Error("idem: complete record", "key", key, "err", err)
      }
  }
  // enforcer.go:144-151 —— 过期（含崩溃遗留 inflight）即 Release + 重新认领 ⇒ handler 重跑
  case err == nil:
      if rerr := e.repo.Release(ctx, key); rerr != nil { ... }
  ```
- 影响：业务写已提交 + `complete` 失败（或进程在 handler 返回后、complete 前崩溃）→ 90s 后同键重试**重新执行 handler**。对有唯一约束的创建动词无害；对"每次执行都产生新副作用"的动词（发 Token/邀请、重置密码、触发备份）是重复副作用，且与 ADR-0024"同键同体 → 同一响应（24h）"的字面承诺不符。已文档化，故不升级。
- 建议：`complete` 失败时把认领行**保留**（不改写 `expires_at` 即可——现状即是）并在 90s 窗口内把"inflight 且超过 handler 超时"视作**可重放失败**而非可重跑（需要 handler 结果落库才能做到，本质是 idem 与业务事务同事务的经典难题）；现实修法是把"不可重跑"的动词（`SetUserPassword`/`CreateToken`/`CreateInvitation`/`TriggerBackup`）改为"回执写失败即返回 E_INTERNAL 且不释放认领"（当前 handler 成功路径不释放，行为已接近），并在协议面写明"幂等承诺 = 24h（回执成功）/90s（回执失败）"。成本 S。状态：已亲验。

### R13-L3｜Database 状态推进用 call-gen，而账本与期望登记用回执 wire-gen（同函数内两套口径）
- 维度：功能（低危）
- 严重度：L（校准：只导致"内容等价 skip"那一拍的状态推进空转，下一拍 memo 路径用 wire-gen 自动纠正；不产生错误状态）
- 位置：`internal/application/engine/database.go:190-204`
- 证据：
  ```go
  // :194 —— 回执 wire-gen（注释明确"登记必须按回执 wire-gen"）
  wireGen := report.WireGen(w, capability.Generation(gen))
  e.ensureRemember(e.database.ensure, row.ID, ensureMemo{sig: sig, gen: uint64(wireGen), at: now})
  e.obs.recordOwners(uint64(wireGen), ...)
  e.expect.expected[databaseOwner(row.ID)] = uint64(wireGen)
  // :204 —— 状态推进入口却传 call-gen
  e.advanceDatabaseStatus(stepCtx, row, gen)
  ```
  `advanceDatabaseStatus` → `dbStatusFromObservation(ev, gen)` 要求 `ev.Generation == gen` 严格相等（`:235-238`），而载体实际以 wire-gen 上报。
- 影响：仅当 `Ensure` 因内容等价 skip（call-gen 推进但载体保留旧 gen，ADR-0064 归属回写正是为此设）时，该拍 `SetStatus` 不执行；下一拍短路路径传 `memo.gen`（=wire-gen）即恢复。触发面是升级/指纹跨二进制漂移（注释自述 2026-10-18 upgrade 矩阵实证），表现为数据库状态显示滞后 1 拍（1s），无事件面影响。
- 建议：`:204` 改传 `wireGen`（一行），并在 `database_test.go` 补一条"call-gen ≠ wire-gen 时状态仍按 wire-gen 推进"的钉测。成本 S。状态：已亲验（读写两侧逐行）。

## 5. 交叉观察（不在本模块，值得其他模块注意）

1. **R02/R05（事实 kernel 的非事务面）**：R05-M1 的根因在 `NextSeq` 的"MAX+1 必须在事务内"前提，而 `writes.Emit` 的执行面是 `*sql.DB`（`writes.go:87-93`）。补充一条机制观察：**这不只是两处调用点的问题，而是 kernel 的 API 形状问题**——`Emit` 与 `Sequence` 共享 `Repos` 但语义完全不同（一个要求调用方提供事务、一个明确不提供），类型上无法区分（同一 `writes.Repos`）。建议 R12 评估把非事务面改名为 `EmitNonTx` 或收窄为只接 Outbox（`StreamOnly` 强制），让"双真"只能走事务面。R05-M1 / R02-L4 已计，不复计。
2. **R08/R05（事件订阅容量）**：每条 follow 流每 250ms 一次 outbox 点查（`contexts.go:375-385`），而读 repo 与 11 条收敛环共用**同一个** `MaxOpenConns(1)` 连接（R05-M5）。二者叠加后，"~50 并发跟随流"锚意味着 200 qps 的点查与全部写事务排队在同一条连接上——单连接模型下这是**订阅面反过来拖慢写面**的唯一路径，建议 R05 在评估读池分离时把此面列入首个拆池对象。
3. **R14（Team 轴 fail-open）**：`eventTeamOfProject/eventTeamOfApp/ownerTeam/factOf` 在归属解析失败时一律降级 `""`（`transition.go:41-59,66-80`、`acceptance.go:80-88`），而 `""` 在 outbox 读侧等价于"平台级、全员可见"（`contexts.go:456` 的 `team_id=? OR team_id=''`）。同一失败取向在执法面被明文拒绝（`converge.go:304-311`："搬进执法面即 fail-open"），说明团队已知该降级只适用于只读事件面；建议 R14 裁决"事件面降级空 Team 是否可接受"（一次 Projects 读失败 = 一条本属 A 队的事件对全租户可见），并考虑"解析失败即不发射 + 记日志"的 fail-closed 替代。我只做线索，不计发现。
4. **R06（期望账本与 k3s Addresses）**：R06-L4 指出 k3s `Addresses` 按期望集合成端点、不校验载体存在。我核实了 M1 的残留账本条目**不会**因此产出坏后端（BG 成员的 `Addressing[0]` 恒为稳定进程名 `projection.go:138-141`，`resolveBackend` 取首个 Process+Port 命中 → 稳定 Service 名，`routepublish.go:139-143`）——两者叠加是安全的，故未升级；但若未来 `routeExpectations` 改成按代次化名优先，该组合会立刻变成 502。建议 R06 在补存在性校验时同步加注释。
5. **R04/R02（单写者 step 内的无界等待）**：R04-H2（`io.Pipe` 读端未关）暴露的是一类形态：`step` 内的阻塞等待不感知 ctx 取消。我顺着查了其余持 `lockMaintenance` 的 step，结论是只有该处；`boundedStep` 对 provider 调用有效，对"goroutine + channel 汇合"无效（`backup.go:237-264` 的 `<-runErr` 不在 select 里）。建议 R02/R04 统一审查所有 `<-ch` 形态。
6. **R09（与 M2 同源）**：`cmd/fleetlyd/main.go:109-110` 的两个 30s 参数与实际生效的 5s `StopTimeout` 是本轮找到的"配置面语义断点"；若 R09 已独立发现，请以 R09 为主（装配归其主审），本报告只提供 lynx v1.20.0 外部实现证据（`shutdown.go:73-81`）与引擎侧预算对照表。
7. **R05（DB 关闭顺序）**：`NewStateDB` 的 cleanup 是 `db.Close()`（`provides.go:245`），挂在 `OnPostStop`；配合 M2 的"5s 截断"，实际存在"关闭时仍有事务在跑"的窗口。SQLite 语义上安全（回滚），但 `database is closed` 会污染关停期日志、掩盖真实错误——建议关停期在 `db.Close` 前加一行"engine still draining"计数（引擎可暴露 `e.wg` 计数）。

## 6. 测试与守卫质量评估

**覆盖强度（整体强）**：横切面（事务组合点、CAS 交错、收敛双通道、BG 双代窗、网络重建、幂等冲突）都有**行为级**钉测，且大量用例用"确定性交错"而非 sleep（R05 §6 已举证 `deployment/repo_test.go:63-96`、`secret/repo_test.go:94-157`；engine 侧 `converge_test.go` 16 例、`networkrebuild_test.go` 4 个并发用例、`engine_test.go:476-496` 的 Start/Stop 并发风暴）。这是本仓在本轮所有发现中"最少假阳性"的基础。

**断言强度（分层不均）**：
- 强：状态机 + 四件一拍（审计 AfterFP/Source/Team 逐行断言）、CAS 冲突、BG 幽灵不复建、`materialize` 三路一致性（性质测试）。
- 中：关停面只测"并发不 panic + 幂等"（`engine_test.go:476-496` 用 `context.Background()`，**没有任何 deadline/慢 step 用例**）——M2 因此结构上不可能被发现。
- 弱（对应本报告发现）：①`peers_test.go:103-114` 的隔离自愈只用 rolling 形态，BG 交叉零覆盖（M1）；②`rotate_test.go` 只覆盖"utility 拒绝"分支，无"utility 成功 + Secret 写失败"注入（M3）；③`database_test.go` 无 call-gen ≠ wire-gen 用例（L3）；④无"非事务回执失败后重试"用例（L2）。

**假阳性/假阴性风险**：
- `fakeRuntime.InspectWorkloads` 从"最近一次 Ensure 的 spec"推导（R02 §6 已指出），意味着"Ensure 返回成功但载体没变"不可表达——M1 的"每 30s 重 Ensure"在测试里会被 fake 完全吸收，即便写用例也只能断言调用次数（可做，建议如此）。
- 本报告 M1/M3 的洞都在"真实 provider/真实故障"一侧，单测与 e2e 之间存在缝隙：**e2e 矩阵（upgrade/backup/bluegreen/k3s）没有一条覆盖"凭证轮换"**（grep `e2e/*.sh` 无 `rotate`），所以 M3 的窗口在 CI 全绿下完全不可见。

**守卫是否拦得住本模块的典型错误**：
- 拦得住：绕过组合点手搓四件一（`transitionguard`/`acceptanceguard` 双向保鲜）、跨环自建 ticker（`loopguard`）、engine 直调 repo 状态迁移。
- 拦不住（本报告点名的）：**期望账本的写点集合**（R02-L6 已挂账，M1 是它的同族后果）、**`maintenanceMu` 读半边的调用点完整性**（L1，纯注释纪律）、**外部副作用序与补偿**（M3，无任何静态锚）、**根包/装配层的预算互校**（M2）。四者共同特征：**"纪律写在注释里，没有可执行锚"**——与 `statekitlaw`/`transitionguard` 那种 AST 级执法形成对照，建议 R12 按此清单补 2-3 条 AST/单元守卫（成本各 S）。

## 7. 未决与置信度

**最不确定的 5 点**
1. **M1 的真实触发面**：机制链已逐行读完（写点无退场 → 消费面按"存在性"判 → 重放无门），但"某 App 同时满足 BG + 跨域引用 + 之后失效"在生产出现的频率未知（staging 是否有 BG+跨域叠加未核实）。若 Lead 认为该组合近乎不存在，可降 L；机制缺口本身建议照修。
2. **M2 的 lynx 语义**：`StopTimeout=5s` 来自模块缓存 `lynx@v1.20.0`（与 `go.mod` 一致），`stopServiceBounded` 的"超时跳过并继续"是确定实现；但我没有实测一次"慢 Ensure + SIGTERM"完整时序，也没验证 `OnPostStop` 的 `db.Close` 与引擎 goroutine 的先后精确性（读 `lifecycle.go` 顺序成立）。
3. **M3 的可达性**：需要 utility 成功后 `cipher.Produce`/`Secrets.Upsert` 失败或进程崩溃；`cipher` 是纯内存操作，失败面主要是 `Upsert`（磁盘满/DB 故障）与崩溃。概率低但非零；"不可恢复"的论断依赖"新密码只在栈帧内"这一事实（已逐行确认无日志/无返回值）。
4. **L1 的交错后果**：我按 provider 语义推断"re-attach 已消失载体 → 报错"，未核 swarm `AttachNetwork` 对不存在 service 的确切行为（R06 报告称"已消失载体在 Provider 侧幂等跳过"，若确实幂等跳过则后果降为"重建多做一次无用 attach"）。
5. **M2 与 R09 的重叠度**：装配面归 R09，若其已报同一问题，本条的独立价值只剩"引擎侧预算对照 + lynx 外部证据"。

**整体置信度：中高**。本报告 6 条发现全部有 `文件:行` + 代码摘录 + 调用方/相邻代码核对；M1/M2 是"机制链完整可推演"级别（未构造运行时复现），M3/L1/L2/L3 是"代码事实"级别。未覆盖：真机 e2e、并发竞态的定量复现、lynx 运行时关停观测。

**范围声明**：只读评审，未修改仓库任何文件、未运行任何写仓命令、未派生子代理；唯一写入为本 findings 文件；临时文件仅在模块缓存与系统临时目录读取，未在仓库或评审目录留下中间产物。
