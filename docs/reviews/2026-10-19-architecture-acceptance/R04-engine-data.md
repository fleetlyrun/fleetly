# R04 engine 数据/自动化/观测/备份 评审报告

> 评审基线：仓库 `D:\Codes\qiulin\fleetly` HEAD `35257f18`（工作树干净）。全程只读，未改仓库任何文件。
> 本报告所有结论均带 `文件:行` + 代码摘录；外部工具行为（pg_restore/mysql/restic）均以官方文档/源码核实后使用（见 §7 说明）。

## 1. 覆盖清单

### 完整读过（生产文件）
- R04 主审文件组（`internal/application/engine/`）：`task.go`、`taskverbs.go`、`taskobs.go`、`taskview.go`、`minttask.go`、`schedule.go`、`database.go`、`databaseenv.go`、`backup.go`、`browse.go`、`browseloop.go`、`exec.go`、`execrelay.go`、`platformbackup.go`、`nodes.go`、`logging.go`、`metrics.go`、`exposition.go`、`alerting.go`、`channelnotify.go`；同域配套 `rotate.go`（凭证轮换，Database 域）。
- 壳与调用链（为排除误读而读）：`engine.go`、`construct.go`、`loop.go`、`lifecycle.go`、`transition.go`、`ensure.go`、`observ.go`、`options.go`、`hygiene.go`（`SweepTerminalTaskCarriers` 上下文）。
- domain 契约：`internal/domain/automation/{task,run,schedule}/{task,run,schedule}.go` + `repo.go`；`internal/domain/data/{database,backup,browse}` 行类型/Repo 面（按视角抽读）；`internal/domain/services/dbtemplate/{postgres,mysql,mongo,redis}.go`；`internal/domain/services/dbbrowser/{dbbrowser,pgweb,adminer}.go`；`internal/domain/shared/capability/{ports,runtime}.go`（LogFrame/NamespaceRef/Utility/ObjectStore 契约）。
- infrastructure 消费面：`persistence/{state,database/repo,logcursor/repo,browse/repo}.go`；`providers/victorialogs/provider.go`（Ingest/Query/buildLogSQL/decodeFrame）；`providers/traefik/config.go`（ForwardAuth 渲染）；`providers/swarm/watch.go`（观测补面 pollTasks）、`providers/swarm/logs.go`（LogFrame 归因）；`providers/localobjectstore/provider.go` 与 `providers/s3objectstore/provider.go`（Put 错误路径——H2 证据）。
- interfaces/assembly：`interfaces/grpc/fleetlygrpc/{platform.go,contexts.go(日志三轴),automation.go(CreateTask/CreateSchedule),databases.go(BrowseDatabase/票据),exec.go(票据),eventtickets.go}`；`assembly/gateway_browse.go`（entry/authorize 原生入口）；`interfaces/admin` 未抽读。

### 抽样读过
- 测试（先列函数名再抽读关键用例）：`task_test.go`（27 例，重点 Lease/TTL/one-shot/stop/revoked/quota）、`taskview_test.go`、`schedule_test.go`（11 例）、`database_test.go`、`databaseenv_test.go`、`backup_test.go`（15 例，重点 restore/failure/backoff/prune）、`downloadbackup_test.go`、`browse_test.go`（6 例）、`exec_test.go`（8 例）、`platformbackup_test.go`（9 例）、`logging_test.go`（5 例，全文）、`metrics_test.go`（7 例）、`rotate_test.go`、`projection_task_test.go`；`testsupport/apitest/{browse_test.go,tasks_test.go,schedules_test.go,platform_test.go}` 相关段。
- 文档对照：ADR-0012/0018/0025/0039/0040/0041/0051/0062/0065、`docs/plan/2026-09-30-feature-checklist.md` F2.x 段、`skills/task-pool/SKILL.md`、`docs/runbooks/k3s-runtime.md:55`（平台恢复为手工 restic restore）。

### 未覆盖/无法覆盖
- k3s Provider 的观测补面/日志子面未读（`internal/infrastructure/providers/k3s/**`）；Run 观测自愈结论依赖 swarm 的 10s `pollTasks`，k3s 是否有等价面**未核实**（见 §7）。
- e2e dind 套件未运行（本机无 Docker，PROFILE §1.1）；dind-backup/upgrade 对本文若干结论的真机验证不可做。
- 未逐行读 `interfaces/grpc/fleetlygrpc` 全部 handler（R08 主审）；Browse/Exec 的 handler 只读到与本模块语义相关段落。
- `internal/guards` 未系统读（R12 主审）；本报告对守卫的判断仅基于 R04 相关行为与测试盲区。

## 2. 模块画像与边界

E3 是「数据/自动化/观测」四类域的集合，共享 engine 单写者骨架（`loop.go` 唯一 Loop；每域一个 ring，`construct.go:109-120` 注册 10 条环：deployment/build/managed/database/backup/logging/metrics/schedule/task/browse）。职责切分：

- **Task/Run**（`task.go` + `taskview.go` + `taskobs.go` + `taskverbs.go` + `minttask.go`）：resident/one-shot 双形态；Run 状态机 `pending→running→stopping→stopped|failed`，终态七枚举停止原因；单写者 `taskStep`（批量 `ListDriving` 分组，`taskview.go` 拥有"方向锚 + 双占用"两条行集不变量的唯一判定点 `occupiedFor`）；观测裁决分轨（`workloadRun` 路由到 Run 状态机）；四件一拍经 `transition.go` 的 kernel 单声明。
- **Schedule**（`schedule.go`）：时区 cron 解释面在 domain（`schedule.ParseCron`，嵌入 tzdata）；`next_fire_at` 绝对时刻；到期拍铸 one-shot Task；Fire/RecordFire CAS 双发防御；手动触发不挪 cron 节奏。
- **Database**（`database.go` + `databaseenv.go` + `rotate.go`）：不进 Deployment 状态机，per-Database 指纹门 `EnsureGeneration` + 签名短路；凭证单真源 = `database:<name>` Secret（完整连接 URL）；卷行幂等补建 + 钉住；`database_refs → FLEETLY_DB_*` 注入；恢复挂起（preseed）门控。
- **Backup**（`backup.go`，backupLoop 第 8 收敛环）：数据库备份台账（调度→执行→恢复挂起→保留滚动）+ Platform Backup（restic 链，同环末段 `platformBackupPass`，执行体在 `platformbackup.go`）。
- **Browse**（`browse.go` + `browseloop.go`）：会话活体注册表 + `browse_sessions` 台账双真；ephemeral 双 Route（免门禁 entry / ForwardAuth 工具路由）；grant cookie 校验；只读方言分层（`dbbrowser.Enforcement`）。
- **Exec/Relay**（`exec.go` + `execrelay.go`）：会话注册表（进程内，零持久化）、per-Team 8 并发/空闲 10min/硬 30min；节点中继 hub（同节点重连顶替）。
- **Observability**（`logging.go`/`metrics.go`/`exposition.go`/`alerting.go`/`channelnotify.go`）：日志采集（每隔离域一条 Follow 流 + 批汇 + 游标）；cadvisor 抓取原文透传 VM + 平台词汇导出 + 内存阈值评估；告警迁移沿落行 + 通道派发。
- **节点观测**（`nodes.go`）：DescribeCluster 对账 + node.left 去抖。

与架构/ADR 声明的偏差（除 §4 发现外）：①本模块注释宣称的并发/重放兜底有两处不成立（`backup.go:360-363` restic 仓锁兜底并发 Platform Backup、`logging.go:248` 空闲冲刷失败"gap 由重启重放兜底"、`logging.go:5-9` "游标只在 Ingest 成功后推进"）——已分别落为 H3/M2。②`skills/task-pool/SKILL.md:101` 称 "run-scoped log retrieval is a known gap"，但 `interfaces/grpc/fleetlygrpc/contexts.go:595-635` 已实现 `streamRunLogs`（IA v3 T8）——文档-代码漂移（归 R10/R15，见 §5）。

## 3. 优点（带证据）

3.1 **Task 域行集不变量收口为类型 + 单点判定，并有专测**：`taskview.go:67-72` 把"resident 占用=活槽位 / one-shot 占用=全驱动行（stopping 也占位）"做成 `occupiedFor` 唯一判定点，`taskview.go:30-35` 用构造保证新→旧序；`taskview_test.go` 三个用例分别钉方向锚/双占用/排空锚。这直接防住历史上"停老保新""one-shot 补第二 Run"两类实证缺陷。
3.2 **one-shot 终态镜像的收口次序正确**（E29-2 反转）：`task.go:264-271` 先空集 `ensureTaskWorkloads` 成功、再迁移 Task 终态；失败保持 active 留驱动集下拍重试，避免"Task 已终态退出 ListDriving、残留载体无人重试"的窗口。`task.go:277-289`（排空完成）同构。
3.3 **Schedule 的绝对时刻 + CAS 双发防御 + 错过窗单拍补跑**：`schedule.go:75-76`（`next = sched.Next(now)` 单拍补跑）、`:178-215` 在**同一事务**内铸 Task 与 Fire/RecordFire（CAS 失配整单回滚），`schedule_test.go` 有 `TestTriggerScheduleConcurrentSingleMint`/`TestScheduleMissedWindowCatchUpOnce` 钉住；手动拍用 `fromLastTaskID` 作锚（`schedule/repo.go:27-32`）解决并发双铸。
3.4 **Backup 链从 staging 事故中长出的失败面工程**：失败退避 5min + 失败行 48h 清扫（`backup.go:31-40`，注释记录 33h/11.4 万行垃圾的实录）；`backup.go:191-203/275-284` 把"迟到失败/成功回执"按 `state.ErrConflict` 安静丢弃（终态是事实），不覆写；`persistence/database/repo.go:176-184` 恢复失败清挂起、明确"半恢复态不重试"并由 `TestRestoreFailureRecorded` 钉住；保留滚动行/对象成对删（`backup.go:556-587`）。
3.5 **只读 enforcement 的分层诚实**：`dbbrowser.go:32-45` 把 session（PG 服务端 `default_transaction_read_only=on`）/tool（redis-commander/mongoku 工具层）/none（adminer）三档显式建模；`adminer.go:38-40` 对 readonly=true 直接 fail-closed 返回错误，受理面 `databases.go:517-524` 对 `EnforcementNone` 或无 write scope 的 rw 请求要求 `databases:write`——分层不是文案摆设。
3.6 **Exec 的 send-vs-close 竞态被结构性消除**：`execrelay.go:96-111` 把"发送 + closed 复查"放在同一把 `s.mu` 临界区，`finish` 先置 closed 再 close 通道（`exec.go:438-473`）；`TestExecDeliverVsFinishNoSendOnClosed`、`TestExecUnattachedSessionsCollectedByHardTTL`（硬 TTL 从受理起算，防"未附着会话永占 Team 限额"）覆盖。观测侧 `swarm/watch.go:129-139` 的 10s `pollTasks` 为观测丢失提供自愈补面（Task 域逐 task 上报）。

## 4. 发现清单

### R04-H1｜Browse ForwardAuth 只校验 cookie、不绑定目标会话 → 跨会话/跨租户越权
- 维度：安全
- 严重度：H（拥有**任意**有效 browse 会话即可通过受害者会话的 ForwardAuth 门禁；`databases:read` 只读会话可借此进入他人 read-write 会话的写控制台，构成权限提升；同 host 可推导时跨 Team）
- 位置：`internal/assembly/gateway_browse.go:113`（authorize 入口）、`internal/application/engine/browse.go:287-295`（每条 tool route 用同一 Auth.Address）、`internal/application/engine/browse.go:213`（只能按 cookie 内 sid 校验）、`internal/interfaces/grpc/fleetlygrpc/databases.go:580`（entry URL 带 session，cookie 值 = `<sid>.<grant>`）
- 证据：
  ```go
  // assembly/gateway_browse.go:113-125
  func serveBrowseAuthorize(w http.ResponseWriter, r *http.Request, gate *BrowseGate) {
      c, err := r.Cookie(browseCookieName)
      if err != nil || c.Value == "" { w.WriteHeader(http.StatusUnauthorized); return }
      sid, grant, ok := splitBrowseCookie(c.Value)
      if !ok || !gate.validate(sid, grant) { w.WriteHeader(http.StatusUnauthorized); return }
      w.WriteHeader(http.StatusOK)
  }
  // engine/browse.go:287-295（每个会话的 tool 路由；Auth 地址与会话无关）
  out = append(out, capability.Route{
      Host: host, Process: s.browser.Name(), Port: s.browser.Port(), ...
      Auth: &capability.RouteAuth{Address: e.opts.Data.Browse.GatewayURL + "/v1/browse/authorize"},
  })
  ```
- 影响：traefik 的 forwardAuth 只把 Address 交给 auth 服务、**不传"本次请求属于哪个会话"**（`providers/traefik/config.go:157` 只渲染 address；`docs/adr/0051-database-browse-sessions.md:56-61` 的实证也只验证 cookie 透传）。攻击者用手上 A 会话的合法 cookie，构造 `Host: browse-<B>.<suffix>` 的请求：authorize 取 cookie 内 sid=A 校验通过 → 200 → traefik 把请求转发到 **B 的工具容器**（pgweb/adminer 等本身无认证）。B 的 host 可推导：session id = `entityid.Derive(databaseID,"s",seq)`（`persistence/browse/repo.go:46`），且同队审计 detail 明文携带 `session_id`（`databases.go:552`）。同队内 = 只读→读写提权；跨队 = 需要 dbID/seq 猜测，但枚举面存在（host 只由可猜字段组成）。
- 建议：authorize 绑定目标会话——校验 `r.Header.Get("X-Forwarded-Host") == "browse-"+strings.ToLower(sid)+"."+Browse.HostSuffix`（traefik 默认注入该头），或给每条 tool route 的 `RouteAuth.Address` 附加 `?session=<sid>` 并比对 cookie sid；补 apitest：用 A 会话 cookie 打 B 的 `/v1/browse/authorize`（Host=B）必须 401。成本 S；这属于 **ForwardAuth 契约层**修复（`capability.RouteAuth` 应要求会话绑定参数，而非裸 URL），建议机制层补齐而非逐点。
- 状态：已亲验（静态全链：route 生成→traefik 渲染→assembly 入口→grant 校验；未做运行时利用验证，见 §7）。

### R04-H2｜备份对象写失败时 `io.Pipe` 读端未关闭 → backup 环永久挂死（并按读锁阻塞网络重建）
- 维度：可靠性
- 严重度：H（单写者环永久停摆 = 备份/恢复/保留滚动/Platform Backup 全部停摆；`unlockMaintenance` 永不执行 → maintenance 读锁永久持有，`RebuildNetwork` 写锁路径永久阻塞；无失败行/事件落账，监控面静默）
- 位置：`internal/application/engine/backup.go:237-264`（pipe/goroutine/`<-runErr`）、`backup.go:187-188`（defer unlockMaintenance）；端口侧：`internal/infrastructure/providers/localobjectstore/provider.go:87-96`、`internal/infrastructure/providers/s3objectstore/provider.go:183-195`
- 证据：
  ```go
  // engine/backup.go:237-264
  pr, pw := io.Pipe()
  ...
  go func() {
      err := e.utility.RunUtility(execCtx, req, io.MultiWriter(pw, tail), stderr)
      if err != nil { _ = pw.CloseWithError(err) } else { _ = pw.Close() }
      runErr <- err
  }()
  info, putErr := e.objectStore.Put(execCtx, key, pr)   // 错误可早退/中途弃读
  if rerr := <-runErr; rerr != nil && putErr == nil { ... }   // 无界等待
  ```
  ```go
  // localobjectstore/provider.go:95-96（Put 早退：不读也不关 reader）
  if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
      return capability.ObjectInfo{}, err
  }
  ```
- 影响：`io.Pipe` 是无缓冲管道，writer 的 `pw.Write` 阻塞到 reader 读/关为止。`Put` 在任何错误（本地磁盘满/权限、S3 网络/凭证故障、ctx 入界取消）提前返回后，无人再读也未 `pr.Close()`；工具容器 goroutine 阻塞在 `Write`（`RunUtility` 无法返回，故 `runErr` 永远不来），`<-runErr` 永久阻塞。备份环是单写者 `Loop`，该 step 永不返回 → 后续所有备份停摆；持有的 `maintenanceMu` 读锁让随后 `RebuildNetwork`（ADR-0046 写锁）永久等待，级联到其他 Ensure 族（Go RWMutex writer pending 时新读者也阻塞）。触发条件很现实：S3 上传中途断网（GB 级 dump 必然在流中）、数据盘写满（备份目标自填满）。测试为何没拦：`backup_test.go:62-72` 的 `fakeObjectStore.Put` 恒 `io.ReadAll(r)` 全额排空，永不复现"错误 + 弃读"形态。
- 建议：pipe 创建后立即 `defer pr.Close()`（读端关闭让 writer 的 Write 返回 `ErrClosedPipe`，goroutine 得以收尾并把错误送进 `runErr`）；`<-runErr` 改 ctx 感知 select（`execCtx.Done()` 兜底）。补测试：注册一个"未读即返回错误"的 fake ObjectStore，断言 executeOneBackup 在秒级返回并把行落 failed。成本 S；机制层建议把"调用方拥有 reader 生命周期、端口错误路径不承诺排空"写进 `capability.ObjectStore` 契约注释，并加一条守卫/测试基座。
- 状态：已亲验（静态；provider 早退路径逐行读过）。

### R04-H3｜Platform Backup 无单飞：并发触发可产出"损坏但 restic check 通过"的恢复点
- 维度：数据安全 / 可靠性（这正是"恢复成功但数据错"的假阳性面）
- 严重度：H（Platform Backup 是升级前置硬停分支与唯一控制面灾备产物；损坏恢复点在 `restic check`、`restic backup` 两级校验下都不可见，直到真要恢复才暴露）
- 位置：`internal/application/engine/platformbackup.go:119-181`（`runPlatformBackup` 无互斥）、`:216-223`（快照文件先删后写）、`:365-372`（手动触发直接执行）、`internal/application/engine/backup.go:54-76`（ring 节拍调用）
- 证据：
  ```go
  // platformbackup.go:216-223
  func (e *Engine) snapshotState(ctx context.Context) error {
      ...
      target := e.platformBackupPath(platformSnapshotName)
      _ = os.Remove(target) // VACUUM INTO 不覆写既有文件
      return e.db.VacuumInto(ctx, target)
  }
  // platformbackup.go:365-372（API 手动触发，与 ring 无互斥）
  func (e *Engine) TriggerPlatformBackup(ctx context.Context) (*PlatformSnapshot, error) {
      ...
      runErr := e.runPlatformBackup(execCtx, *cfg)
  ```
- 影响：SQLite 单连接（`persistence/state.go:109` `SetMaxOpenConns(1)`）只串行化 `VACUUM` 本身，**`os.Remove(target)` 与后续 `restic backup` 读同一路径都不在该串行化内**。A（ring）与 B（手动/另一个手动）并发：B 的 `Remove` 可删除 A 正在写的目标（unlink 后 A 继续写已删 inode），随后 A 的 `restic backup DataRoot` 读到的是 B 正在 `VACUUM` 写的**半成品 state-snapshot.db**；restic 只做内容去重/校验和，不校验 SQLite 一致性，`restic check` 校验的是仓结构——于是双绿 + 损坏恢复点。代码注释 `platformbackup.go:360-363` 声称"与节拍环的并发互斥由 restic 仓锁兜底（同仓并发 backup 一方持锁一方精确失败，无半写态）"——仓锁只覆盖 `backup` 子进程，**不覆盖被备份的 SQLite 快照文件**，该断言不成立。另：两个并发 backup 会导致一方拿到 `repository is already locked` → 手动触发返回 E_PLATFORM_BACKUP_FAILED（升级硬停的假失败）。
- 建议：引擎级 `platformBackupMu` 单飞（手动+节拍共用，`TryLock` 语义：并发手动触发应显式返回"已在执行"而非并发跑）；快照写唯一临时名（如 `state-snapshot.<uuid>.db`）后原子 rename；`restic backup` 前对快照跑 `PRAGMA integrity_check`（失败即中止并保旧快照）；把"快照文件不属于 restic 仓锁临界区"写进注释与测试。成本 S/M；建议机制层：所有"导出→归档"链共用"单飞 + 原子快照 + 完整性校验"基座。
- 状态：已亲验（静态；并发时序未真机复现，见 §7）。

### R04-M1｜Owner Lease 首次布防缺失：resident 池在"创建后未续期"时永不排空
- 维度：功能 / 可靠性
- 严重度：M（与 ADR-0012 声明的"属主失联超宽限即排空回收"不一致；客户化场景下是资源/配额永久占用，但不影响既有在跑数据）
- 位置：`internal/application/engine/minttask.go:81-86`（铸造不写 LeaseDeadline）、`internal/application/engine/taskverbs.go:92-98`（唯一写 lease 的点是 RenewTask）、`internal/application/engine/task.go:174-179`（空 deadline 直接不判）；受理面 `interfaces/grpc/fleetlygrpc/automation.go:113-173` 同样不设
- 证据：
  ```go
  // minttask.go:81-86（行装配无 LeaseDeadline）
  return &task.Task{
      ID: taskID, Seq: seq, ProjectID: projectID, Name: name,
      Form: form, State: task.StateActive, Spec: body,
      OwnerTokenID: ownerTokenID, DesiredConcurrency: desired,
      NetworkGroup: s.GetNetworkGroup(), DNSName: TaskDNSName(taskID),
  }, nil
  // task.go:174-179（空 deadline 即跳过整个租约判定）
  func (e *Engine) driveLeaseExpiry(ctx context.Context, t *task.Task, now time.Time) {
      if t.State != task.StateActive || t.Form != task.FormResident || t.LeaseDeadline == "" {
          return
      }
  ```
- 影响：`fleetly tasks create --form resident --owner-token-id ...` 之后，若客户端在第一次 `tasks renew` 之前崩溃/被回收（或自动化脚本只创建不续期），`lease_deadline` 永远为空：lease 判定整段短路，resident 池永久保活（resident 默认无 TTL），直到用户显式 stop/delete 或吊销 token。ADR-0012 与 `skills/task-pool/SKILL.md:11-12,23-27` 的语义都是"错过 deadline + 宽限即排空"，但**deadline 的起点没有在创建时布防**。测试为何没拦：`TestTaskLeaseExpiryDrainAndRevive`（`task_test.go:408-456`）先显式 `RenewTask` 再推时钟，`TestOwnerRevokedDrainsTasks`（`:661-662`）直接 `UpdateLease` 布防——没有任何用例覆盖"创建后不 renew"。
- 建议：`MintTaskRow`（或 CreateTask 受理）在 resident 且 `ownerTokenID != ""` 时铸 `LeaseDeadline = now (+ TaskLeaseInterval)`，让"首拍即失联"可收敛；补测试 create-with-owner + 不 renew → 宽限后 drained。成本 S。
- 状态：已亲验（写点用 grep 全仓确认：`LeaseDeadline` 只在 `UpdateLease` 与 Transit 原值回写处被写）。

### R04-M2｜日志采集：空闲冲刷失败后游标越过丢批（重放补不回）+ flush 并发数据竞争
- 维度：可观测性 / 可靠性
- 严重度：M（静默日志缺口；对 one-shot 失败/崩溃诊断是直接损伤；并发 Save 可让游标回退导致重复重放，依赖 VL 去重兜底）
- 位置：`internal/application/engine/logging.go:270-291`（WriteLog 前移 `b.cursor`）、`:294-304`（flushIdle 失败仅告警）、`:308-335`（flush 内 `cursorPersistAt` 无锁读写 + Save）；`internal/infrastructure/persistence/logcursor/repo.go:47-52`（Save 无条件 upsert）
- 证据：
  ```go
  // logging.go:320-333（Ingest 失败只计数丢批；b.cursor 已在 WriteLog 前移到丢批之后）
  if err := b.logging.Ingest(ictx, frames); err != nil {
      b.dropped += len(frames)
      ...
      return err
  }
  if time.Since(b.cursorPersistAt) >= logCursorPersistInterval {
      if err := b.cursors.Save(ctx, b.ns.String(), cursor); err != nil { ... }
      b.cursorPersistAt = b.clock.Now()   // 与 StreamLogs goroutine 的并发 flush 无锁竞争
  }
  ```
- 影响：①空闲冲刷（低流量域唯一冲刷面，1s 节拍）失败时批被丢弃但内存 cursor 已推进；流继续存活，下一次成功 flush 会把**包含丢批区间之后**的 cursor 落盘——重启重放从该 cursor 起算，丢掉的帧再也补不回（与 `logging.go:248` 注释"gap 由重启重放兜底"及 ADR-0040"游标只在 Ingest 成功后推进"矛盾）。阈值冲刷失败路径之所以安全，是因为错误上抛、流中断后**重新从 repo 游标构造新批**；idle 路径没有这个复位。②`flush` 可被 StreamLogs goroutine（阈值触发，`WriteLog:286-289`）与 loggingStep goroutine（`flushIdle`）并发进入同一 `nsBatch`：`frames` 有锁，但 `b.cursorPersistAt` 的读/写不在锁内（data race），且并发 Save 的旧值可把已推进的游标写回去（Save 是盲 upsert）。
- 建议：flush 失败时把 `b.cursor` 回退到该批首帧时间（保留重放锚），或失败即让流重启（与阈值路径同语义）；`cursorPersistAt` 并入 `b.mu`；Save 加 `WHERE excluded.last_ts > last_ts` 单调守卫。补测试：idle 冲刷失败后重启流从丢批前游标起算；两 goroutine 并发 flush 跑 `-race`。成本 S/M。
- 状态：已亲验（读全四条 flush 调用路径与 repo SQL；本机 `go test -race ./internal/application/engine` 通过 293.9s 是因测试未构造并发 flush，非代码无竞态）。

### R04-M3｜Task/指标域进程内缓存与缓冲区无回收：随运行时长无界增长
- 维度：性能 / 可靠性
- 严重度：M（长生命期 daemon 的稳态内存增长；Schedule 铸 Task 是永久行、Run churn 大时更明显。E30 只裁决了 `locks` 的接受，本组其余条目无裁决）
- 位置：`internal/application/engine/taskobs.go:23-29`（三个 map）、`:114-124`（唯一清点 `drainObs`）、`taskverbs.go:282`（唯一调用点＝DeleteTask）、`task.go:490-498`（`workloadRun`/`ensure` 写入）；`internal/application/engine/metrics.go:288-294`（`lastCPU` 只增）；`backup.go:238`（`stderr` 无界 `bytes.Buffer`）
- 证据：
  ```go
  // taskobs.go:114-124（只清指定 task；DeleteTask 是唯一调用方）
  func (x *tasks) drainObs(taskID string) {
      for wid, owner := range x.workloadRun {
          if owner == taskID { delete(x.workloadRun, wid); delete(x.runObs, wid) }
      }
  }
  // metrics.go:288-294（容器 CPU 差分基面：每容器一条，从无删除面）
  key := nodeID + "|" + id
  prev, had := e.metricsDom.lastCPU[key]
  e.metricsDom.lastCPU[key] = cpuCounter{value: v, at: now}
  ```
- 影响：`workloadRun`（每 Run 一条）、`task.ensure`（每 Task 一条）、`task.locks`（每 Task 一把）只在 `DeleteTask` 清理；而 **Schedule 铸出的 one-shot Task 行永不删除**（"Task 行不删——审计单位"），用户也很少删终态 Task → 条目只增。`metrics.lastCPU` 对每个 fleetly 容器实例一条、仅在 scrape 时写、无淘汰面（`construct.go:86` 建 map，全仓无 delete）。规模估算：一天 1 次调度 = 1 Task + 1 Run/天；崩溃重启 resident 池或 30min TTL 池（`createRun` 每 TTL 一轮）可到千条/天。另 `executeOneBackup` 的 `stderr` 是全量 `bytes.Buffer`（无 `boundedTail`），工具容器 stderr 刷屏时按输出量吃内存（15m 上界）。
- 建议：Task 迁移到终态时清 `ensure`/`locks`/`workloadRun` 对应键（终态 Task 的 Run 均已终态，`runObs` 已由 `recycleObs` 回收；注意与 E30"删锁铸第二把锁"的顾虑不同——终态 Task 不会再被驱动，可安全清理，或至少保留 locks 现状只清另两张）；metrics 每拍做"本轮未见容器"淘汰（按 node 内 container 集合）；stderr 换 `boundedTail`。成本 S/M。
- 状态：已亲验（三处 map 的写/删点全仓 grep 核对）。

### R04-M4｜Exec attach 与 finish 收口可交错：Close 先于 Open 到达 → 节点侧 exec 会话成为孤儿
- 维度：可靠性 / 资源回收
- 严重度：M（窗口包含一次阻塞的 `pipe.Send`，非微秒级；后果是节点上漏一个 exec 进程到自然退出，无资源限额回收面）
- 位置：`internal/application/engine/exec.go:313-341`（attach 路径）、`:438-473`（finish 路径）
- 证据：
  ```go
  // exec.go:313-341（置 attached 后先 Send 元数据、再向中继发 Open；全程未复查 closed）
  e.exec.mu.Lock()
  s := e.exec.sessions[sessionID]
  ...
  s.attached = true
  relay := s.relay
  e.exec.mu.Unlock()
  if err := pipe.Send(ExecServerFrame{Meta: ...}); err != nil { e.exec.finish(s, nil); return nil }
  open := capability.RelaySessionOpen{...}
  if err := e.exec.send(relay, capability.RelayFrameOpen, s.info.ID, ...); err != nil { ... }
  ```
- 影响：`finish` 由硬 TTL timer（30min，受理起算）、中继顶替/失联、client 断开触发；它与 AttachClient 并发时，`finish` 先写 `RelayFrameClose` 并关 `s.out`，AttachClient 仍可能把 `RelayFrameOpen` 写给同一个中继/同一 session ID。节点中继侧（`capability.RelaySessionOpen` 的消费方）在 Close 未知会话后收到 Open，会起一个控制面已无会话的 exec：无人送 stdin、无人发 Close，进程泄漏到自然结束。`TestExecAttachConflict` 只覆盖双附着，无 finish×attach 用例。
- 建议：attach 在 `s.mu` 内完成"closed 复查 + 置 attaching 状态"，`finish` 对 attaching 会话延迟 Close 帧直到 Open 已发（或 attach 在发送 Open 后复查 closed 并补 Close）；把"附件状态机"从两个 bool 收编为显式状态（idle/attaching/attached/closing）。补并发测试。成本 S。
- 状态：已亲验（静态时序推理；未复现）。

### R04-M5｜备份/恢复"最后一次回执写"失败无收尾：running 行悬置（阻断调度）或恢复被逐拍重跑
- 维度：可靠性
- 严重度：M（触发需存储写错误——磁盘满/损坏是现实场景；后果是某库备份长期停摆或恢复 utility 秒级重跑，直到进程重启）
- 位置：`internal/application/engine/backup.go:275-284`（FinishSucceeded 非冲突错误仅记日志返回）、`:501-504`（ClearRestoreSucceeded 失败仅记日志）、`:386-401`（restorePass 每拍枚举，无退避）
- 证据：
  ```go
  // backup.go:275-284
  if err := e.repos.Backups.FinishSucceeded(ctx, b.ID, key, info.Digest, info.Size); err != nil {
      if errors.Is(err, state.ErrConflict) { ... return }
      e.log.Error("backup step: record success", "backup", b.ID, "err", err)
      return   // 行仍在 running；对象已落库，无台账/事件
  }
  ```
  ```go
  // backup.go:501-504（恢复成功但清挂起失败：RestoreFromBackup 仍在 → 下一拍重跑）
  if err := e.repos.Databases.ClearRestoreSucceeded(ctx, db.ID); err != nil {
      e.log.Error("restore: clear pending", "database", db.ID, "err", err)
      return
  }
  ```
- 影响：`Backups.HasActiveFor`（`backup.go:131-137`）见到 running 即跳过该库调度 → 该库所有后续定时备份停摆；`SweepInterrupted` 只在 `Start` 跑（`lifecycle.go:32`），期间无自愈。恢复侧：清位失败导致 `RestoreFromBackup` 未清，`restorePass` 每 1s tick 重跑整次恢复 utility（pg_restore 对已恢复库再跑、mysql 对已存在表再 source）——最终大概率以失败清位告终，但产生重复执行与一条误导性的 `restore_error`（"恢复成功"事实被抹成失败），且期间 utility 容器风暴。
- 建议：回执写失败走 `fail()`/`FinishFailed` 兜底（或加一个每 N 拍扫描"running 超 BackupTimeout"的行并按启动清扫同语义落 failed）；`ClearRestoreSucceeded` 失败改为指数退避重试，重试前先读行判断是否仍 pending；两个回执失败都应在事件面显式可见。成本 S。
- 状态：已亲验。

### R04-L1｜Schedule 时区/DST 语义是 ADR 声明但无任何测试钉住
- 维度：测试与守卫
- 严重度：L（当前依赖 robfig/cron 行为正确；缺测试意味着库升级/替换会静默改变"春跳不存在时刻/秋跳重叠时刻"的触发语义）
- 位置：`internal/domain/automation/schedule/schedule.go:56-74`（注释声称"春跳不存在的时刻按 time.Date 归一、秋跳重叠取前一义"）；该包**没有 `_test.go`**；`internal/application/engine/schedule_test.go` 无时区/DST 用例；`testsupport/apitest/schedules_test.go` 只断言时区字符串往返
- 证据：
  ```go
  // schedule.go:56-59（声明是行为契约，但无测试锚）
  // ParseCron 解析带时区的 5 字段 cron 表达式... 跨夏令时由 robfig/cron 按墙钟
  // 解释（SpecSchedule.Next 在目标时区做墙钟算术——春跳不存在的时刻按 time.Date
  // 归一，秋跳重叠时刻取前一义）。表达式与库选型结论见 ADR-0018 附录 A。
  ```
- 影响：ADR-0018 的验收锚（跨 DST 行为）无承载；更换 cron 库或升级版本不会红。
- 建议：新增 `schedule` 包测试，固定 tzdata 断言 `America/New_York` 2026-03-08 02:30（不存在）与 2026-11-01 01:30（重叠）的 `Next` 结果；再补一条 `Asia/Tokyo` 与 UTC 的绝对时刻对照。成本 S。
- 状态：已亲验（目录内无测试文件，测试函数清单核对过）。

### R04-L2｜browse cookie 在 TLS=auto 下仍无 `Secure` 属性（代码注释自认挂账）
- 维度：安全
- 严重度：L（host-only cookie + HttpOnly/SameSite=Lax 已限制面；仅 TLS 模式下缺传输层约束，注释明确"随 TLS 批次收口"）
- 位置：`internal/assembly/gateway_browse.go:98-106`
- 证据：
  ```go
  http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124：Secure 刻意缺省 false——browse 路由缺省明文 none（ADR-0051 决策 5）；tls=auto 形态下 cookie 面 Secure 化随 TLS 批次收口
      Name: browseCookieName, Value: cookieValue, Path: "/",
      MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode,
  })
  ```
- 影响：`browse.tls=auto` 时浏览器仍可能把 cookie 发往同主机 http 明文（若存在 80 端口重定向/中间人降级），与 H1 叠加会放大 cookie 窃取面。
- 建议：按 `Browse.TLSMode == "auto"` 条件设置 `Secure: true`；顺手把 ADR-0051 的挂账勾掉。成本 S。
- 状态：已亲验。

### R04-L3｜node.left 事件发射失败时 nodes 表保持 `available=true`，且去抖签名已置位（不重试）
- 维度：可观测性
- 严重度：L（nodes 表是观测缓存非权威表；影响节点列表/路由诊断面的诚实性）
- 位置：`internal/application/engine/nodes.go:41-59`
- 证据：
  ```go
  for _, n := range cached {
      if present[n.PlatformID] || e.nodeLeftSeen[n.PlatformID] { continue }
      e.nodeLeftSeen[n.PlatformID] = true          // 先置去抖
      if err := e.emitObservation(...); err != nil {
          e.log.Error(...); continue               // 事件失败 → 跳过下面的 available=false 落库
      }
      if err := e.repos.Nodes.Upsert(ctx, &node.Node{... Available: false}); err != nil { ... }
  }
  ```
- 影响：事件发射失败（outbox 写失败）时，节点被标记为已离开（不再重试发事件）却仍在 nodes 表显示 available=true，直到下次全量 upsert 修复（节点不会回归快照，故可能长期不一致）。
- 建议：把 `Upsert(available=false)` 提前到 emit 之前（或无论事件成败都执行），事件失败用单独的重试标记。成本 S。
- 状态：已亲验。

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **R14（安全）/R08（接口层）**：H1 的根因是 ForwardAuth 契约缺"目标会话绑定"，修复点跨 `capability.RouteAuth`（R01）、`assembly`（R08）与 engine route 生成（R04）。建议由 R14 复核利用面（host 可枚举性来自审计 detail 的 session_id 与 `entityid.Derive` 形态）。
2. **R07（服务类 Provider）**：H2 的另一半是 `ObjectStore.Put` 端口契约未声明"错误路径是否保证排空/关闭 reader"；`localobjectstore`/`s3objectstore` 均提前返回且不关 reader。修 H2 时应同步在端口注释与 adapter 契约测试中固化"调用方拥有 reader 生命周期"。
3. **R02（部署/收敛）**：H2 的 `maintenanceMu` 读锁被永不返回的 step 持有，会使 `RebuildNetwork` 写锁永久等待——这是"单写者 step 内无界等待"这一类机制缺口的横切实例；建议 R02/R13 统一审视所有持 `lockMaintenance` 的 step 是否都能被 ctx 终止。
4. **R13（横切并发/事务）**：H3（跨入口无单飞）与 H2（step 内等待不 ctx 感知）可归并为 engine 的两条共同基座缺口：**入口级单飞**、**step 级可中断等待**。同类候选还有 `TriggerPlatformBackup` 的 idem 豁免（ADR-0081 只保证同 key 重放，不防不同 key 并发）。
5. **R06（Runtime Provider）**：swarm `Watch` 有 10s `pollTasks` 观测补面（`swarm/watch.go:129-139`），Run 观测丢失可自愈；**k3s 是否有等价补面未核实**——若无，Task 域"观测路由注册前到达的观测"将成为永久 pending（`observ.go:199-205` 依赖 `workloadRun` 映射）。
6. **R10/R15（CLI/Skills/文档）**：`skills/task-pool/SKILL.md:101` 称 "run-scoped log retrieval is a known gap (log face is app-scoped)"，但 `interfaces/grpc/fleetlygrpc/contexts.go:595-635` 已实现 Run 轴与 Database 轴的流式日志（IA v3 T8）——技能文档已过期（文档-代码漂移）。
7. **R08（日志归因）**：`LogFrame` 只有 `Team/Project/App`（`capability/runtime.go:730-745`），Task/Database/Browse 域日志入库后没有域主体字段；当前 Run/Database 日志检索靠 `WorkloadIDs` 收窄（`contexts.go:578-634` + `victorialogs/provider.go:523-567` 的 buildLogSQL 忽略 `Namespace.Task/Database/Browse`）才正确。若后续出现"按 Task/库聚合查询"，会静默退化为 project 全量——建议 R08 在查询面契约中显式禁止空 WorkloadIDs 的 Task/Database 轴查询或补齐字段。
8. **R07/VictoriaLogs**：`vlLine.Task` 字段承载的是 `LogFrame.Container`（实例锚，`provider.go:280`），而平台 Task ID 另存 `fleetly_workload`（Run ID 前缀含 Task）。命名双义（"task" 在 VL 里不是 Task ID）有误读风险，建议随字段演进更正/注释。

## 6. 测试与守卫质量评估

**亲跑证据**：`go test ./internal/application/engine/ -count=1` → `ok ... 20.721s`；`go test -race ./internal/application/engine/ -count=1` → `ok ... 293.960s`（均通过；仓库工作树未被改动）。

**覆盖强度（正面）**：R04 五个域的测试函数数量充足（task 27 / taskview 3 / schedule 11 / database 10 / backup 15 / browse 6 / exec 8 / platformbackup 9 / metrics 7 / logging 5 / rotate 5），且大量用例直接钉行为语义而非快照：`TestTaskResidentPoolScaleDownDrains`（缩容排空）、`TestDrainingTaskReStopsOrphanRuns`（排空补停）、`TestScheduleMissedWindowCatchUpOnce`、`TestTriggerScheduleConcurrentSingleMint`（并发单铸）、`TestRestorePreseedFlow`（预置卷门控 + 卷钉住 + 首启恢复）、`TestBackupFailureBackoffWindow`、`TestExecDeliverVsFinishNoSendOnClosed`、`TestSystemOffsiteAlertStateRace`（并发读锁面）。这是本模块最大的质量资产。

**断言强度（盲区）**：
1. `fakeObjectStore.Put` 恒排空 reader（`backup_test.go:62-72`）——**H2 类的错误路径不可能被现有测试发现**；fake Utility 也只在失败时写 stderr，不模拟"容器继续写 stdout 而对象端失败"的交错。
2. **并发竞态测试缺口**：无 Platform Backup 并发触发（H3）、无 idle-flush 与阈值 flush 并发（M2，`-race` 全绿不代表无竞态，只代表未被触发）、无 finish×attach（M4）、无 DeleteTask×driveTask 之外的 Task 缓存清理断言（M3）。
3. **时区/DST 无测试**（L1），ADR-0018 的可测承诺无承载。
4. **Lease 起点无测试**（M1）：两个 lease 用例都手工布防，恰好绕过"创建即失防"形态。
5. **恢复侧无"数据正确性"断言**：`TestRestoreStreamFlow` 只断言 argv/清位/事件（`backup_test.go:304-330`），"恢复后数据在"由 e2e dind（`e2e/dind-backup.sh` 的 F28 标记行断言）承载；本机无法复核 e2e，按 PROFILE 只评静态断言质量——e2e 存在数据断言，因此"恢复成功但数据错"的主要风险收敛为 H3（破坏快照）与 M5（重跑/回执），而非恢复渲染面。**已核实并排除**一个候选误报：pg_restore 默认"继续并统计 SQL 错误"（[官方文档 `-e/--exit-on-error`](https://www.postgresql.org/docs/current/app-pgrestore.html)），但 [pg_restore.c main 结尾](https://raw.githubusercontent.com/postgres/postgres/master/src/bin/pg_dump/pg_restore.c) 为 `exit_code = AH->n_errors ? 1 : 0`，即 SQL 错误**会**非零退出 → `restoreDatabase` 会走失败面，故"无 `--exit-on-error` 导致恢复假成功"不成立（mysql batch 默认遇错中止同理）。
6. **守卫面**：`loopguard` 只保证 ticker 单点，不覆盖"step 内部无界等待"（H2）；H1/H3 属契约/并发形态，现有守卫（dep/domainpurity/wording 等）天然覆盖不到——建议按 §4 修复时同批补行为测试，而非指望静态守卫。
7. **测试与实现耦合**：`backup_test.go:315` 逐字钉 `pg_restore` argv、`platformbackup_test.go` 以假 restic 钉 argv/env 形态——好处是契约稳定，风险是修复（如加 `--exit-on-error`/完整性检查）会被"测试需要改"误读为回归；属可接受代价。

## 7. 未决与置信度

**最不确定的 5 点**：
1. **H1 的运行时可利用性**：静态链路完整（cookie→authorize→traefik→工具后端），但未在真机构造双会话跨 Host 请求验证；host 可枚举性依赖审计 detail/`entityid.Derive` 暴露面（同队内确定可见）。置信度：静态高、可利用性中高。
2. **H3 的损坏复现**：并发时序依赖 `VACUUM INTO` 持续时间与 `os.Remove` 交错，未做并发真机实验；但"快照文件不在 restic 仓锁临界区"这一事实与注释自述矛盾是确定的。置信度：中高。
3. **k3s 观测补面**：本报告假设 Run 观测丢失可被 provider 周期补面上报自愈（swarm 有 10s `pollTasks`）；k3s 实现未读。若 k3s 无等价面，需追加"Run 观测丢失 → pending 悬挂"的 M/H。置信度：中（依赖 R06 交叉核实）。
4. **`workloadRun` 泄漏的规模阈值**：条目增长速率取决于调度频率与 Run churn，本报告只证明"无回收面"，未量化到内存/延迟拐点；E30 对 `locks` 的"v1 规模可接受"裁决是否应扩展到另两张 map 需要 Lead 裁决。置信度：高（无回收）／中（影响量级）。
5. **Schedule 时区面**：`ParseCron` 的 `time.LoadLocation` + 嵌入 tzdata 逻辑正确，但"控制面容器无 zoneinfo"这一支撑前提只在注释与 ADR 中声明，未在本机/容器验证；DST 无测试见 L1。置信度：中高。

**整体置信度：中高**。3 条 H 均有直接代码证据（静态全链），M 组均亲验写点/调用方；H1/H3 缺运行时复现、k3s 面未覆盖，已在 §7 明示。评审边界：只对 R04 主审范围负责，providers 实现细节与他人模块的裁决只在必要处作为证据引用。
