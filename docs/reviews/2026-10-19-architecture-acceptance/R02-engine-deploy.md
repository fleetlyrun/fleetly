# R02 engine：部署、收敛、状态机与受管编排评审报告

> 评审基线：仓库 HEAD `35257f18`（工作树干净），设计母本 `docs/design/2026-10-18-declarative-model.md` + ADR-0081/0082/0083。
> 结论：声明式模型的收敛主体（缺席重投影、双通道、签名生命周期、fail-closed 冻结、restart_seq 执行时判定）**基本按设计落码且钉测扎实**；发现 1 个 H（k3s 蓝绿窗隔离缺口）、7 个 M（冷却记账/裁决表/动作面重启/删除竞态/慢通道可观测/重启重建/环死亡可观测）、8 个 L。

## 1. 覆盖清单

**完整读过（生产文件，R02 文件组 31 个全量）**
- 壳与环：`internal/application/engine/engine.go`、`options.go`、`construct.go`、`lifecycle.go`、`loop.go`、`ensure.go`
- 部署链：`drive.go`、`transition.go`、`admission.go`、`deployspec.go`、`materialize.go`、`projection.go`、`bluegreen.go`、`rollback.go`、`predeploy.go`、`render.go`（projectRevision/RenderWorkloads 段）
- 收敛/观测：`converge.go`、`drift.go`、`observ.go`、`observdomain.go`
- 重启：`restart.go`、`processrestart.go`、`componentrestart.go`
- 收口与受管：`teardown.go`、`networkrebuild.go`、`peers.go`、`managed.go`、`routepublish.go`、`apipublish.go`、`hygiene.go`、`events.go`、`schemareg.go`

**完整读过（测试，R02 对应）**
`converge_test.go`、`drift_test.go`、`restart_test.go`、`transition_test.go`、`admission_idem_test.go`、`crashrecovery_test.go`、`healthgate_test.go`、`teardown_test.go`、`fakeruntime_test.go`

**抽样读过**
`engine_test.go`（夹具前 120 行）、`processrestart_test.go`/`componentrestart_test.go`（函数名+关键断言）、`bluegreen_test.go`/`managed*_test.go`（函数名清单）、`materialize_test.go`/`rollback_test.go`（函数名）、`predeploy_test.go`（驱动序段）；配套真源：`internal/domain/delivery/deployment/repo.go`、`persistence/deployment/repo.go:80-240`、`transact/transact.go`、`domain/services/writes/writes.go`、`domain/shared/spec/diff.go:136-169`、`eventcode/events.go`+golden+`eventcode_test.go`、`guards/{dep_matrix,transitionguard,registrysourceanchor}_test.go`、`assembly/provides.go:470-540`、`config/defaults.go:20-110`、`cmd/fleetly/cmd/verbs_structure.go:410-520`（apps restart）、`cmd/fleetly/cmd/doctor.go:248-267`、`interfaces/grpc/fleetlygrpc/{alerting.go:340-372,structure.go:427-509}`、`providers/{swarm,k3s}/restart.go`、`k3s/{translate.go:180-200,685-736,network.go:33-57}`、`providers/swarm/runtime.go`（resume 段）。

**未覆盖/无法覆盖**
- e2e dind / k3s 真机（本机 Docker daemon 不可达）：蓝绿窗、egress 强隔离、升级零滚均只做静态路径推演，未真机复验（报告已逐条标注）。
- providers 全量实现（R06 主审）、构建链细节（R03）、持久化实现细节（R05）、API handler（R08）——仅按需交叉阅读。
- `go test ./...` 全量（时间/环境面）；已跑：`go test ./internal/application/engine`（ok 21s）、converge/restart 子集 `-race`（ok 35s）、`go test ./internal/guards`（ok 19s）、`go vet ./internal/application/engine`（干净）。

## 2. 模块画像与边界

- **职责**：R02 文件组是平台唯一写序列的执行面——Deployment 状态机单写者（queued→preparing→building→releasing→observing→succeeded|failed|rolling-back→superseded/cancelled）、admission（去重/latest-wins/supersede/queue）、Spec→Workload 投影与物化（`materialize` 单序列）、初代健康门 L1/L2/L3 与回滚 Replay、启动基线重放、ADR-0082 缺省收敛（快/慢双通道+冷却+冻结闸门）、ADR-0083 声明式重启（执行时 seq-only 判定+kind 标注）、受管 Provider reconciler / Route 全量发布 / 网络重建串行化 / teardown 收口 / 事件与审计事实发射。
- **对外接口**：`Submit`/`Cancel`/`Rollback`/`TeardownApp`/`SubmitSpec`（冻结+受理咽喉）/`RestartProcess`/`RestartComponent`/`RebuildNetwork`/`Start`/`Stop`/`RenderWorkloads`/`InspectAppWorkloads`/`KickManagedLoop`/`SystemConvergeAlertState`；事务面经 `transact.Stores`+`Tx.Do`（ADR-0080）。
- **依赖方向**：application→domain 合规；`internal/guards/dep_matrix_test.go:85-104` 有两条**带理由的活豁免**——`application/engine → infrastructure/github|upload`（"P3 客户端接口化后收进 domain，届时删本行"），属已登记的架构债而非新漂移，双向保鲜会强制收紧。
- **与声明的偏差（详见发现清单）**：ADR-0082 十行裁决表在代码里是"信号集合+闸门"的隐式实现，`convergeVerdict` 表函数无生产调用（M2）；行 10 机制描述（缺席检测判为期望集外）与实现（慢通道重放）不符、且该场景无审计/事件（M5）；ADR-0082 §4 的"启动基线重放共用冷却记账"被空签名释放路径削弱且钉测未钉住（M1）。

## 3. 优点（带证据）

1. **缺席检测按 Revision 真源拍内重投影，不变量 1 合规**（ADR-0082 §1 行 2 / 审查 B1 的根治）：`converge.go:205-254` 的 `expectedCarrierIDs` 从 `LatestSucceeded` 重投影并 `stampDeploymentGenerations`，与 `materialize` 同源；`(revision,gen)` 缓存短路；`TestGhostOldGenerationMemberNotRebuiltAfterBGCollection`/`TestGhostRemovedProcessNotRebuiltAfterScaleDown` 双向钉住 BG 旧代与进程缩减两类缓存幽灵。

2. **收敛动作统一原语与双复查真实闭合竞态**（ADR-0082 §2 / B2+B6）：`converge.go:283-356` 闸门序（冷却→posture→冻结 fail-closed）→ `lockApp` 内 ②teardown 标记、①`DrivingByApp`（驱动权语义，含 failed 待回滚窗）、基线 freshness 复查 → `materialize(isolate=true)`；`TeardownApp` 在 appMu 内 `forgetOwner` 后 `markTeardown`（`teardown.go:55-76`），`Submit` 受理成功清标记（`admission.go:271-274`）——机制比设计文本的"expect 成员资格复查"更精确并有测试。

3. **签名生命周期与冷却三径按设计落码**（ADR-0082 §4 / B4+B10）：成功才 `resolved`+memo（`converge.go:402-437`），失败只计冷却（`:442-475`），挂起/跳过不记账；冷却解除三径在 `convergeCooldownOpen` 注释与实现一体（`:358-396`）；`TestConvergeCooldownLifecycle` 覆盖 N=3→10min→事件一次→超窗/签名变化/posture 切换三条解除径。

4. **restart_seq 八步机制链落码完整**（ADR-0083）：投影透传 `projection.go:128-131`；执行时对实际 succeeded 基线重算（`restart.go:31-57`，B8 正确）；`kind=restart` 经 `transitDeploymentAnnotated` 单行 LedgerOnly 审计（`transition.go:253-291`）；BG 舞步不受影响（`TestRestartSeqOnlyBlueGreenKeepsBGDance`）；漂移面显式排除并有共享法源注释（`drift_test.go:367-385`）；preDeploy 游标直落 done（迁移不重跑，`restart_test.go:83-113`）。

5. **物化单序列与带界执行**（架构 §0/ADR-0046/0064）：`materialize.go:39-87` 是全消费面（release/rollback/基线重放/收敛）唯一序列，`boundedStep`+maintenanceMu 读半边+wire-gen 归因修正同点收口；同批有性质测试 `TestMaterializeThreePathsIdentical` 钉三路下发一致。

6. **事实面可追证**：`workload.converged` 走 `writes.Emit` 带 `Action=workload.converge`、`Source=audit.SourceSystem`、`AfterFP=基线 digest`、team 资源轴（`converge.go:419-436`）；两事件 `eventcode` 注册+golden+usage 反扫三链齐（`eventcode/events.go:52-53`、`eventcode_test.go:45-107`）；内置告警/doctor 行真实接线（`alerting.go:360-370`、`doctor.go:248-260`）。

## 4. 发现清单

### R02-H1｜蓝绿双代窗旧代投影漏填 egress 事实——k3s 上在服旧代在窗口期失去 egress 强隔离
- 维度：安全（兼双 Runtime 一致性）
- 严重度：H（校准：ADR-0052 F4.1 把 `egress:none` 在 k3s 的 NetworkPolicy 强隔离列为明文验收锚；窗口期 Ensure 会**主动摘掉**在服旧代载体的 `fleetly.egress` 标记，使旧代（切流前真正在服务的 pod）从 deny policy 中脱选；影响面限 k3s+BG 策略+egress:none 网络，窗口=releasing 窗口物化到 observing 收口（L1 120s + 观察窗 60s 量级））
- 位置：`internal/application/engine/bluegreen.go:127`（对照同函数 `:147`）；`internal/infrastructure/providers/k3s/translate.go:187-189`、`:708-710`；`internal/infrastructure/providers/k3s/network.go:36-57`
- 证据：
  ```go
  // engine/bluegreen.go：旧代成员按 from_revision 重投影——egress 参数传 nil
  fromWS, _, err = Project(fromSpec, team, appName, fromDigests, fromPeers, nil) // :127
  // engine/bluegreen.go：新代成员带 egress 事实
  newWS, ns, err := Project(toSpec, team, appName, digests, peers,
      e.egressNetworkMap(ctx, toSpec.GetApp().GetProject())) // :147

  // k3s/translate.go：egress 标记只由 w.EgressNetworks 产生
  if len(w.EgressNetworks) > 0 {
      labels[labelEgress] = "true" // :187-188
  }
  // k3s/translate.go：deny policy 以 fleetly.egress=true 为 podSelector
  PodSelector: metav1.LabelSelector{
      MatchLabels: map[string]string{labelEgress: "true"}, // :708-710
  },
  ```
- 影响：k3s 集群上任何"blue-green 进程 + 挂 egress:none 网络"的 App，每次 BG 部署的窗口期内，**正在承载流量的旧代**可自由出站（跨 Namespace/外网），声明的出站强隔离被削弱；窗口结束后旧代被收口，隔离自动恢复（非永久）。引擎侧测试零覆盖（grep `Egress` 只命中 managed/task 测试），守卫也无此断言。
- 建议：`bluegreen.go:127` 改为 `Project(fromSpec, team, appName, fromDigests, fromPeers, e.egressNetworkMap(ctx, fromSpec.GetApp().GetProject()))`（S，一行）；在 `bluegreen_test.go` 增加"union 两代成员 EgressNetworks 与各自 Revision 投影一致"的负向钉测；k3s e2e 蓝绿腿加 egress:none 断言（属机制层补齐，建议随 R12 e2e 同批）。
- 状态：已亲验（引擎投影路径 + k3s netpol 选择器代码路径）；未真机复验（本机无 Docker/k3s，标记为需 k3s 真机确认）。

### R02-M1｜冷却解除径②在"无签名源"（基线/隔离重放）上恒真——冷却窗可被基础重放解除，钉测用注入态掩盖
- 维度：可靠性（兼测试与守卫）
- 严重度：M（校准：冷却窗/事件一次的记账可被非信号源重放重置，告警会误 resolved 后再 firing；但只影响冷却节流与告警沿，不改变收敛终态）
- 位置：`internal/application/engine/converge.go:362-387`、`:390-396`；`internal/application/engine/drift.go:94-96`；`internal/application/engine/peers.go:166,213-215`；测试 `internal/application/engine/converge_test.go:493-511`
- 证据：
  ```go
  // converge.go:370-372——"漂移签名变化"解除径：空签名源也会命中
  release := now.Sub(until) >= e.opts.Converge.Cooldown ||
      e.converge.failSig[appID] != agg ||
      e.converge.posture[appID] != e.opts.Converge.Posture
  // drift.go:94-96——基线/隔离重放固定传空 agg
  func (e *Engine) replayAppBaseline(ctx context.Context, a *app.App, d *deployment.Deployment) {
      e.attemptConverge(ctx, a.ID, d, "", convergeSourceBaseline)
  }
  // converge_test.go:501-504——钉测手工把 failSig 置空，绕开真实形态
  e.converge.cooldown[tAppID] = now
  e.converge.posture[tAppID] = ConvergePostureAll
  e.converge.failSig[tAppID] = ""
  ```
- 影响：某 App 因连续 3 次 Ensure 失败进入冷却（`failSig` 非空）后，一次 `replayAppBaseline`（peer 撤销的即时隔离重放 `IsolateNetworkPeer`、漂移拍 `enforcePeerIsolation`、或同进程 Stop→Start 的启动重放）即以 `failSig != "" == agg` 释放冷却：重置 `fails/eventSeen`、发 `alert.resolved` 沿、并立即再试一次——ADR-0082 §4"启动基线重放共用同一冷却记账、不受冷却豁免盲打"与 `TestBaselineReplaySharesCooldownLedger` 声称的钉住点在生产真实状态下不成立（测试靠注入 `failSig=""` 才通过）。
- 建议：给 `attemptConverge` 增加"本次尝试是否携带签名"形参（或按 `src` 派生），仅在 fast/slow 且 agg 非空时允许"签名变化"解除；`failSig` 比较改为显式三态。补充真实形态钉测（冷却中触发 `IsolateNetworkPeer`，断言冷却不释放/告警不翻转）。成本 S，建议机制层修（解除径判定收敛到一个谓词函数）。
- 状态：已亲验（代码路径 + 测试注入点）；未构造端到端复现用例。

### R02-M2｜ADR-0082「裁决表即断言」不成立：`convergeVerdict` 是生产不可达的装饰函数，row 10/8/9 无生产落点
- 维度：测试与守卫（兼文档一致性）
- 严重度：M（校准：ADR 验收锚①声称"十行落码 + golden 钉死 = 表即断言"，实际判定函数只有测试引用，真实闸门/信号逻辑分散在 `compareSpecs`/`attemptConverge`；改动真实决策不会让 golden 变红，属"听起来严厉但拦不住"的守卫形态）
- 位置：`internal/application/engine/converge.go:144-201`（`convergeVerdict` 定义）、`internal/application/engine/converge_test.go:77-108`（唯一引用）、`internal/application/engine/drift.go:229-352`（真实信号面）、`internal/application/engine/converge.go:262-356`（真实闸门面）；ADR `docs/adr/0082-converge-by-default.md:89`
- 证据：
  ```go
  // converge.go:176-201——仅在 converge_test.go:95 被调用（生产代码零引用）
  func convergeVerdict(in convergeVerdictInput) string {
      switch {
      case in.Frozen:       return verdictSuspend
      case in.Driving:      return verdictSkip
      case in.SpecMismatch: return verdictConverge
      ...
      case in.CancelLegacy: return verdictReplayBaseline
  ```
  golden 行：`10-cancel-legacy-partial-issue replay-baseline`——但生产代码里没有任何路径把 `CancelLegacy=true` 输入该函数；row 10 的"回卷"实际由慢通道重放兑现（`converge_test.go:566` 注释自认："慢通道重放兑现回卷"），row 8（域内多余载体）/row 9（孤儿只登记）也无独立检测落点（分别由 Ensure 域内移除与"跳过"隐式承担）。
- 影响：ADR 承诺的"表→码→golden"三链断在"码"这一环：表函数是第二实现，真实分支顺序/缺失改变时 golden 恒绿；审查 B5（Cancel 遗留）的裁决因此在代码里只有"慢通道兜底"这一半，且不可发现。
- 建议：二选一——(a) 让 `attemptConverge` 真正以 `convergeVerdict` 判定（信号→输入结构→统一 verdict→动作），golden 才有执法力；(b) 若保留现结构，修订 ADR-0082 验收锚①措辞（"判定函数+golden 为文档锚，行为由八类钉测承载"）并删除 `verdictReplayBaseline`/`CancelLegacy` 死分支。成本 M（a）/S（b）。
- 状态：已亲验（grep 全仓 `convergeVerdict` 仅 converge.go+converge_test.go）。

### R02-M3｜动作面进程重启按 unscoped workloadID 定位载体——蓝绿应用 swarm 报错 / k3s 静默假成功
- 维度：功能（兼可靠性）
- 严重度：M（校准：ADR-0069 动作面在 BG 应用上不可用/假成功，排障动线失效；BG 已在 e2e 矩阵中，属双特性交叉缺口；无数据风险）
- 位置：`internal/application/engine/processrestart.go:56-57`；`internal/infrastructure/providers/swarm/restart.go:20-23,34`；`internal/infrastructure/providers/k3s/restart.go:15-21`；测试盲区 `processrestart_test.go:35-47`、`testsupport/apitest/processrestart_test.go`
- 证据：
  ```go
  // engine/processrestart.go——传裸 workloadID
  ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: appID}
  return restart.Restart(ctx, ns, WorkloadID(appID, process)) // :56-57
  // swarm/restart.go——按 labelWorkload 全等匹配（BG 载体 label 是 -g<gen> 代次化 ID）
  if svc.Spec.Labels[labelWorkload] != workloadID { continue } // :21
  return fmt.Errorf("swarm restart %s: workload not found in %s", workloadID, ns) // :34
  // k3s/restart.go——DeleteCollection 零命中不报错 = 假成功
  sel := labels.Set(map[string]string{labelWorkload: workloadID}).AsSelector()
  ...DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: sel.String()}) // :16-19
  ```
  载体 ID 代次化真源：`engine/bluegreen.go:32-34`（`genScopedWorkloadID`）与 `stampDeploymentGenerations` 的 `ws[i].ID = genScopedWorkloadID(...)`（`:55`）；swarm 载体 label 取 `w.ID`（`swarm/translate.go:138`）。
- 影响：`fleetly apps restart --runtime --process web`（ADR-0083 §3 声明的逃生通道）在 blue-green 进程上：swarm 直接报 "workload not found"（动线失效）；k3s 选择器匹配 0 pod 但 `DeleteCollection` 返回 nil → **报告成功而什么都没重启**（agent 面假成功信号，更危险）。现有测试只用 rolling 形态 App，双特性交叉零覆盖。
- 建议：engine 侧按最新 succeeded 行的 Generation 解析候选载体 ID（`WorkloadID` 与 `genScopedWorkloadID` 两个形态都尝试，或直接委托 provider 按 `labelProcess`+域轴定位）；同时在 `TestRestartProcessTargetsWorkloadID` 增加 BG 用例（swarm/k3s 各断言"命中恰一个代次化载体"）。成本 S。
- 状态：已亲验（引擎传参 + 双 provider 实现 + BG ID 公式）；未真机复验。

### R02-M4｜删除收口对 failed 待自动回滚窗失明（ActiveByApp vs 驱动权口径）——回滚重放可在收口后重建载体
- 维度：可靠性（兼数据一致性）
- 严重度：M（校准：窗口窄但根因与 ADR-0082 审查 B2 同族——`ActiveByApp` 不含 failed 待回滚、驱动权口径是 `ListDriving`；这里是同一错配未被修的第二处，后果是删除后孤儿载体复活，无清理路径）
- 位置：`internal/application/engine/teardown.go:27-33`；`internal/interfaces/grpc/fleetlygrpc/structure.go:449-456,478-488`；`internal/domain/delivery/deployment/repo.go:26-33`（口径声明）；`internal/application/engine/drive.go:86-87,329-379`（failed→rollback→materialize）
- 证据：
  ```go
  // teardown.go:27-33——收口前复查用"活跃态"（不含 failed 待回滚）
  active, err := e.repos.Deployments.ActiveByApp(ctx, appID)
  if len(active) > 0 { return fmt.Errorf("%w: ...", ErrActiveDeployment, appID, len(active)) }
  // domain/deployment/repo.go:30-33 自己写明 ActiveByApp 不含 failed 待回滚窗
  //   DrivingByApp 是 ListDriving 的 per-App 形态（... 活跃态 + 待自动回滚的 failed。
  //   ActiveByApp 不含 failed 待回滚窗，单独使用会开"收敛×回滚交错"竞态窗）
  // drive.go:86-87——failed 行由单写者环接手自动回滚（重放基线 → materialize → Ensure）
  case deployment.StateFailed:
      return e.startRollback(ctx, d)
  ```
  同一错配也在 API 侧两处：`structure.go:449`（无锁预检）与 `:479`（tombstone 事务内复查）。
- 影响：App 有一条 `failed`（`from_revision!=''`、`rollback_attempted=0`）部署时（正常仅一个单写者节拍，但环被其他 App 的 30s Ensure 拖住时可延长到分钟级）：DeleteApp 的两次活跃态检查都通过 → `TeardownApp` 持 appMu 执行 `Runtime.Remove` 拆载体、清缓存、`markTeardown`；同时部署驱动器**不持 appMu**，仍会驱动该 failed 行 → rollback → `materialize` → `Ensure` 在 Remove 之后落盘（或与 Remove 交错），在已删 App 的命名空间重建载体。随后 tombstone 提交，`Apps.List` 不再返回该 App、期望缓存已清、teardown 标记只约束收敛不约束驱动 → 这些载体成为无人认领的孤儿（删除语义"拆干净"被破坏，且无 hygiene 面收 Service/Deployment 容器）。反向时序（Ensure 先于 Remove）无害。
- 建议：`TeardownApp` 与 DeleteApp 两处预检统一改为"驱动权语义"查询（`DrivingByApp`，或新增 `HasDriving`），并把该断言写进 `TestTeardownAppRefusesFailedPendingRollback`（构造 failed 行直插即可测）。成本 S；建议机制层把"驱动权口径"收敛成 repo 单一谓词。
- 状态：已亲验（静态路径：状态集合 + 驱动器分支 + 锁面）；未构造并发复现（需注入 Ensure 挂起时序）。

### R02-M5｜Cancel 遗留回卷实际走慢通道：无审计无事件，且 ADR-0082 行 10 机制描述与实现不符、Cancel 注释仍持已翻案的 N0 口径
- 维度：文档一致性（兼可观测性）
- 严重度：M（校准：ADR-0082 把"Cancel 遗留 → 回卷 LatestSucceeded"列为裁决表第 10 行并翻案 admission N0，但机制描述与代码不一致；该回卷是用户取消后的实质状态修正，却既无事件也无审计，运维无法从事件面见证；不影响终态正确性）
- 位置：`internal/application/engine/converge.go:398-437`（事实面仅 fast 源）；`internal/application/engine/converge.go:190-191`（`verdictReplayBaseline` 死分支）；`internal/application/engine/admission.go:279-283`（Cancel 注释）；ADR `docs/adr/0082-converge-by-default.md:25,42`
- 证据：
  ```go
  // converge.go:402-418——只有 fast 源发 workload.converged + 审计；慢通道静默
  func (e *Engine) convergeSucceed(... src convergeSource) {
      ...
      if src != convergeSourceFast {
          return
      }
  // admission.go:279-283——ADR-0082 翻案后仍未更新的口径
  // 在途取消是"停止推进"语义：当前 step 收尾后不再迁移；已下发的
  // Workload 由后续部署或 Remove 收口（N0 不自动拆除——诚实暴露）。
  ```
  ADR-0082:42 行 10 机制列写"——缺席检测把遗留载体判为「期望集外」，收敛动作回卷基线"；但缺席检测是 `期望集−观测集`（`converge.go:237-254`），Cancel 遗留属于"观测有、期望无"的多余载体，不会产生 absent 信号；实际兑现者是慢通道重放（`converge_test.go:566` 自注）。
- 影响：取消某次在途部署后，被部分下发的载体在 ≤90s 内被静默回卷到基线（用户可见的一次重要状态回退），但 `workload.converged`/审计都不落（慢通道不发射）——ADR 宣称的统一原语"审计行 source=system + converged 事件"在这一行裁决上缺席；同时代码注释与 ADR 翻案登记互相矛盾，后续维护者会按注释判断"不自动拆除"。
- 建议：为"Cancel 遗留回卷/域内多余载体移除"这两类慢通道实质动作补一条 `SourceSystem` 审计（含基线 digest 与触发类），并同步修 `admission.go` Cancel 注释 + ADR 行 10 的机制列；若坚持"慢通道零事实"，在 ADR 写明例外（成本 S）。
- 状态：已亲验（代码路径 + 测试注释 + ADR 文本对照）。

### R02-M6｜声明式重启对构建源应用触发全量重建（新 Revision→新 Build 行），重启≠纯滚动替换且依赖构建面在线
- 维度：功能（兼可靠性/文档一致性）
- 严重度：M（校准：ADR-0083 承诺 seq 变→"内容等价三路→full CAS update→滚动替换（provider 语义零新增）"，但新 Revision 在构建链眼里是全新内容；重启会重建+重推镜像，且构建器/registry 不在线时"重启"直接失败——与"重启=重排"的操作者预期冲突）
- 位置：`internal/application/engine/drive.go:162-166`；`internal/application/engine/buildsource.go:72-106`；ADR `docs/adr/0083-restart-seq.md:21`
- 证据：
  ```go
  // drive.go:162-166——seq-only 判定后仍按 Build 声明进构建态
  if spec.GetBuild() != nil {
      // Build 声明存在 → 构建链（building 态由 driveBuilding 驱动）。
      return e.transitAndReload(ctx, d, []deployment.State{deployment.StatePreparing},
          deployment.StateBuilding, nil)
  }
  // buildsource.go:72-76——构建行按 Revision 复用；新 Revision（seq bump）无行
  existing, err := e.repos.Builds.ListByRevision(ctx, d.ToRevision)
  for _, b := range existing { if b.State == build.StateSucceeded { ...skip build... } }
  // :94 无 Build 行：准备输入并受理（新 Revision → 新 Build 行）
  ```
- 影响：`apps restart`（缺省声明面）对 git/构建源应用会产生新 Revision → `ListByRevision` 空 → 新 Build 行 → 完整构建+推送（BuildKit 缓存命中时成本低，但**流程依赖 builder+registry 可用**；构建非字节可复现时镜像 digest 也会变，实质替换了镜像内容）。ADR-0083 的"provider 语义零新增/内容等价护航"没有覆盖构建链这一层；e2e 与单测均未覆盖"构建源应用重启"。
- 建议：执行时 seq-only 判定命中后，把基线 succeeded Build 的 digest 作为新 Revision 的 from_build 解析结果复用（或按 source 内容（commit+上下文）做构建缓存键，而非 revision seq）；`TestRestartSeqOnlySkipsPreDeployJobs` 增加 build 源变体断言"零新 Build 行"。成本 M。
- 状态：已亲验（代码路径）；未真机构建复验。

### R02-M7｜单写者环 panic 终止无消费面：`Loop.done` 无消费者、`Engine.Loop()` 无调用方，收敛静默停止仅留日志
- 维度：可观测性（兼可靠性）
- 严重度：M（校准：单写者环是部署/受管/漂移/备份等全部收敛的唯一执行者；任一环 panic 后仅一行 ERROR 日志，health/doctor/告警全部无感，平台表现为"读面正常、写面永久静止"，只能靠人看日志）
- 位置：`internal/application/engine/loop.go:19-22,52-53,77-87`；`internal/application/engine/lifecycle.go:33-40`；`internal/application/engine/engine.go:195-196`
- 证据：
  ```go
  // loop.go:19-22 注释声称显式可见
  // panic 恢复后终止本循环并关闭 done——单写者死亡必须显式可见（日志
  // ERROR + done 信号），不静默吞掉也不弄死整个控制面。
  // loop.go:52-53 done 面（当前无消费方，文件内注释自认）
  func (l *Loop) Done() <-chan struct{} { return l.done }
  // engine.go:195-196 暴露面（全仓零调用方）
  func (e *Engine) Loop() *Loop { return e.loop }
  ```
  全仓 grep：`Loop.Done()`/`Engine.Loop()` 无生产消费方；`assembly.NewHealthCheckers`（`provides.go:269-273`）只聚合 Provider 健康，不含 engine 环存活。
- 影响：`runStep` 的 `recover` 终止该 ring 后（`lifecycle.go:34-40` 的 goroutine 只是 `r.loop.Run`），部署/受管/漂移/备份等面永久停摆：`deployments` 停在 queued/observing、受管载体失收敛、drift 失检测，而 `/healthz`、doctor、alerting 均无信号；`Start` 幂等检查（`cancel != nil`）也不会重启已死环。
- 建议：环存活纳入 health/doctor（`Loop.Done()` 有消费方：engine 级 supervisor 记录并暴露 `ring=<name> dead`，同时在 doctor 出一行）；或环 panic 后受控重启（受限次数）。成本 S。
- 状态：已亲验（代码 + 全仓引用检索）。

### R02-L1｜快通道"已收敛签名"在漂移消失后不清零：同一失配复发只由慢通道收敛（≤90s 有界）
- 维度：可靠性（低危）
- 位置：`internal/application/engine/converge.go:262-281`、`:402-406`
- 证据：
  ```go
  // converge.go:266-271——resolved 只与聚合签名比较；漂移回归时无人清 resolved
  agg := signals.aggregate()
  fast := len(signals) > 0
  e.converge.mu.Lock()
  unchanged := e.converge.resolved[appID] == agg
  e.converge.mu.Unlock()
  fast = fast && !unchanged
  ```
- 影响：同一 App 先被人工改载体→收敛成功（resolved=A）→人工改回→再改成**同一形态**失配：签名仍为 A，快通道判"已收敛"，只能等 60s 慢通道重放兜底（+30s 扫描节拍，≤90s）。有界自愈，不产生错误终态；但与 ADR-0082"检出漂移即收敛（快通道）"的字面有差距。建议：信号集为空时清 `resolved[appID]`（S）。
- 状态：已亲验（逻辑推演；`drift_test.go:178-189` 用"不同签名"绕开了该形态）。

### R02-L2｜`deploymentEventPayload.Kind` 字段从未填充——kind=restart/rollback 只在审计 AfterFP 后缀可见
- 维度：可观测性
- 位置：`internal/application/engine/events.go:30`、`:385-399`
- 证据：
  ```go
  type deploymentEventPayload struct {
      ...
      Kind            string `json:"kind,omitempty"` // :30 声明
  }
  func deploymentEventPayloadJSON(d *deployment.Deployment) []byte {
      p := deploymentEventPayload{ ... } // :385-396 构造处无 Kind 赋值
  ```
- 影响：ADR-0083 步骤 7 声称"审计入 lineage（Deployment 行 + kind=restart）"，实际行上无 Kind 列（ADR 落码注记已承认）、事件 payload 虽有 `kind` 字段却恒空——订阅事件流的 CLI/Console/Agent 无法从 `deployment.*` 事件区分声明式重启/回滚与普通部署，只能查审计 AfterFP 文本后缀。建议：受理/补标处把 kind 带进行（如加列）或在事件发射点补 `Kind`（需先有载体，属小批显式裁决）。L。
- 状态：已亲验。

### R02-L3｜ADR-0082 翻案后三处陈旧注释/行锚（observ.go"重启冷窗"、networkrebuild.go"app 域没有周期重放面"、ADR:25 引用 admission.go:275-277）
- 维度：文档一致性
- 位置：`internal/application/engine/observ.go:104-109`；`internal/application/engine/networkrebuild.go:104-108`；`docs/adr/0082-converge-by-default.md:25`
- 证据：
  ```go
  // observ.go:106-109——缺席检测已改为 Revision 重投影（不依赖 expected 缓存）
  // 重启冷窗语义：基线重放完成前 expected 为空 → 缺席检测按「succeeded
  // 无期望缓存」扫描面分支兜底（方向安全：漏报不误报）...
  // networkrebuild.go:106——ADR-0082 后 App 域已有 60s 周期重放面
  // app 域没有周期重放面——不回滚会把 app 服务留在网外直到下次部署
  ```
- 影响：两处注释描述的机制在 ADR-0082 后已不成立（缺席检测已与 expected 缓存解耦；App 域已有慢通道周期重放），会误导后续维护者/评审者；ADR-0082:25 的 `admission.go:275-277` 行锚因后续插入已偏移到 279-283。建议同批修注释与行锚（S）。
- 状态：已亲验。

### R02-L4｜收敛事实用非事务 `writes.Emit`：`workload.converged` 的 outbox/audit 两行可单边落库，且不在 transition 组合点内
- 维度：可靠性（低危）
- 位置：`internal/application/engine/converge.go:428-436`；`internal/domain/services/writes/writes.go:87-127`
- 证据：
  ```go
  // converge.go:428-433——"双真"经非事务 Emit（outbox 先、audit 后）
  if err := writes.Emit(ctx, writes.Repos{Outbox: e.repos.Outbox, Audits: e.repos.Audits},
      writes.FactRow{ ... Source: audit.SourceSystem, TeamID: team }); err != nil {
  // writes.go:102-107——Outbox.Append 成功后 Audits.Append 失败即返回错误：事件有、审计无
  if !f.LedgerOnly { ... repos.Outbox.Append(...) }
  if !f.StreamOnly { ... repos.Audits.Append(...) }
  ```
- 影响：收敛是 SourceSystem 的实质写动作，若审计段失败，流面已产生 `workload.converged` 而取证面无行（反向不可能）；另外该事实不在 `transition.go` 的 `commitTransition/writeFact` 组合点内，`transitionguard` 因"无聚合行写"不覆盖它（守卫盲区，见 §6）。建议：若需强配对，给 converge 审计走 `Sequence`+同事务的单一审计行（无聚合写可只落 audit），或显式在 ADR 承认 best-effort；并补 kernel 使用点守卫。L。
- 状态：已亲验（kernel 实现 + 调用面）。

### R02-L5｜`TestFreezeGateFailClosedOnUnresolvableTeam` 实际未覆盖 `convergeFrozen` 的 fail-closed 分支
- 维度：测试与守卫
- 位置：`internal/application/engine/converge_test.go:178-193`；`internal/application/engine/drift.go:152-156`
- 证据：
  ```go
  // converge_test.go:188——soft delete 后直接跑 driftScan
  require.NoError(t, e.repos.Apps.SoftDelete(context.Background(), tAppID))
  before := len(rt.calls())
  e.driftScan(context.Background())
  assert.Equal(t, 0, ensureDelta(t, rt, before), "unresolvable team must skip the round (fail-closed)")
  ```
  `driftScan` 先做 `appTeam` 解析（`drift.go:152-156`），App 已删时**在进入 `convergeStep` 之前就 continue**，`attemptConverge`/`convergeFrozen` 的错误分支（`converge.go:304-311`）从未被执行——断言为真但覆盖的是另一条路径。
- 影响：ADR-0082 §3 的核心安全承诺（冻结判定 fail-closed、解析失败跳过本拍而非 fail-open）没有真正的钉测。建议：构造"App 行存在但 Project 行被删/重建失败"或注入 Freezes repo 错误（测试缝），断言不 Ensure、不落签名、下拍重试。L。
- 状态：已亲验（控制流推演）。

### R02-L6｜「期望账本只由 Ensure 写」只有注释与 ADR 声明，无可执行守卫
- 维度：测试与守卫
- 位置：`internal/application/engine/observ.go:98-125`；`internal/application/engine/observdomain.go:44-61`；ADR-0082 §6（`:71-73`）
- 证据：
  ```go
  // observdomain.go:48-52——纪律以注释钉死（无守卫反扫）
  // 期望账本纪律（ADR-0082 §6 钉死）：ensuredSpec/ensuredGen 只由 Ensure
  // 路径写入（recordEnsured ← materialize 单序列），禁止观测播种——受管域
  // 的 gen 观测播种先例（managed.go seedFromRuntime）不得照抄到 App 域...
  ```
- 影响：这是 controller-wins 的机制底座（B1/C8 裁决）。`recordSpecs/recordOwners` 是泛化 API，未来任何新调用点（观测环/新域）都能静默播种期望态而无红灯；ADR 验收锚自身也写明只是"注释钉死"，属明确挂账。建议：加 AST 守卫——`recordSpecs` 调用点白名单（仅 observ.go 的 recordEnsured），`recordOwners` 白名单（managed/database/browse），越界即红；或把 App 域账本写点收窄成不导出方法。L（机制层补齐建议）。
- 状态：已亲验。

### R02-L7｜收敛冷却记账不在 App 收口时清理：已删 App 可使内置 converge-failed 告警永久 firing / doctor 永久告警
- 维度：可观测性
- 位置：`internal/application/engine/converge.go:87-120,390-396,540-548`；`internal/application/engine/teardown.go:18-77`（无 converge 清理）
- 证据：
  ```go
  // converge.go:540-547——alertFiring 由"任一 App 冷却"驱动
  st, since := alertrule.StateOK, time.Time{}
  if e.converge.alertFiring { st, since = alertrule.StateFiring, e.converge.alertSince }
  // teardown.go：forgetOwner/expect/drift/stopped 都清，唯独不碰 e.converge.{cooldown,fails,failSig,eventSeen}
  ```
- 影响：某 App 收敛失败进入冷却（内置告警 firing）后若被删除（或先删除再无人驱动），`cooldown[appID]` 永不清除 → `alertFiring` 恒真 → `converge-failed` 系统行永久 firing、doctor 永久 warn（`doctor.go:254-258` 只认 firing 行），且只能重启进程清除；同时这几个 map 对已删 App 泄漏。建议：`TeardownApp` 内同步 `convergeReleaseLocked` 并使用与新冷却释放一致的沿处理（S）。L。
- 状态：已亲验（路径推演；未跑真实双态）。

### R02-L8｜egress 事实读失败 fail-open：投影期网络查询错误→载体不带 egress 标记（隔离失效至下一拍重放）
- 维度：安全（低危，跨 R04 文件）
- 位置：`internal/application/engine/database.go:293-319`（`egressNetworkMap`/`projectNetworkFacts`，"查询失败如实记日志返回空集：隔离是收敛不变式，下一拍重放自愈"）；消费面 `render.go:45`、`bluegreen.go:147`
- 证据：
  ```go
  rows, err := e.repos.Networks.ListByProject(ctx, projectID)
  if err != nil {
      e.log.Error("database reconcile: list project networks", ...)
      return nil, nil // :300——错误折成"无 egress 网络"
  }
  ```
- 影响：一次 DB 读失败会让该次 Ensure 下发**不带** `fleetly.egress` 标记（k3s 上等于撤除出站强隔离），自愈依赖下一次重放（releasing 等待期 60s / 依赖 ADR-0082 慢通道 60s）；与 ADR-0052"强隔离"承诺相比属 fail-open 窗口。建议：隔离事实读失败时按"跳过本拍 Ensure"处理（与受管域 P1-5 观测失败跳拍同款取向）而非空集代偿。L/M，落点跨 R04 主审文件，建议 R04 与 R02 同批裁决。
- 状态：已亲验（代码路径）。

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **R06 / 双 Runtime**：k3s Provider 的 `InspectWorkloads` 从不设置 `RolloutStalled`（全目录 grep 零命中），ADR-0082 裁决行 4（终态后滚动停摆→收敛）实际只有 swarm 半边；`resumeStalledRollout` 也是 swarm 专有。ADR-0082 未标注该诚实边界（"App 域获得与 ring 域同律的有界自愈"在 k3s 上缺一类信号）。
2. **R06 / R01 端口契约**：`capability.RuntimeRestart.Restart(ns, workloadID)` 在蓝绿代次化载体下语义不闭合（M3）——建议 R01 评估端口是否应改成 process 维度或返回可枚举载体集，R06 双 provider 同批改。
3. **R08**：DeleteApp 的无锁预检（`structure.go:449-456`）与 tombstone 事务内复查（`:478-488`）使用 `ActiveByApp`，与 M4 同根；建议 R08/R02 统一用驱动权语义查询（`DrivingByApp`）。
4. **R10 + ADR-0083**：`apps restart` 的声明面取"最新冻结 Revision"（`verbs_structure.go:456,497-522`）——回滚到旧 Revision 后再次 restart，会把较新的失败 Revision 内容（镜像等）重新冻结部署，**静默撤销回滚声明**；ADR-0083 未定义"重启的源 = 最新声明 vs 当前生效基线"的边界。构建源应用叠加 M6（重建）。
5. **R03 / 冻结链**：`apps restart`/spec_file 重提交的是冻结体（已含当年合成的 Shared/App variable env），服务端只把**当前生效**变量叠上去——期间被删除的变量值会随旧冻结体"复活"进新 Revision（`spec/variables.go:37-88` + `deployspec.go:158-178`）。需 R03/R10 裁决是否属 spec_file 全量声明语义。
6. **R15 / 文档一致性**：ADR-0082 验收锚①"表即断言"夸大（M2）、行 10 机制描述不符（M5）、行 4 缺 k3s 半边、`:25` 行锚偏移；ADR-0083 最后一条 `e2e:upgrade` 零滚锚未勾（ADR 已诚实标注挂账，收尾批判读口径需真机证据）。
7. **R05**：`ListDriving`/`DrivingByApp` 谓词在 SQL 里重复手写（`persistence/deployment/repo.go:129-176`），"驱动权口径"没有单一构造点——M1/M4 类错配正源于此，建议 repo 内共享谓词常量。
8. **R12**：收敛事件的三链（注册表 golden/usage 反扫/schemareg）是真实有效的守卫；但 `convergeVerdict` 金色只测装饰函数（M2）、期望账本写点无守卫（L6）、`runtime.Ensure` 调用点无白名单守卫（当前有 12 个调用点，靠人肉纪律）。
9. **R13**：`attemptConverge` 持 appMu 期间经 `materialize` 取 `maintenanceMu` 读锁——网络重建窗（最长 5m）内同 App 的 Submit/Teardown 会排队该时长（既有 materialize 纪律，非新引入）；`writes.Emit` 非事务配对（L4）供横切复核。

## 6. 测试与守卫质量评估

- **覆盖强度（强）**：本文件组测试约 40 个文件、1.1 万行量级；核心机制的钉测密度高——converge 16 个用例（裁决表 golden+冻结挂起+活跃跳过含 failed 回滚窗+stopped 不收敛+缺席重建+双幽灵负向+teardown 竞态+gen 抹标签+终态停摆+冷却三径+基线记账+posture+defaults+Cancel 遗留+投影短路+账本盲区）、restart 5 个、bluegreen 6 个、transition 7 个、admission idem 3 个、crashrecovery 4 个、healthgate 3 个。夹具质量好：真 SQLite + fake clock + fake runtime，手动驱动 step 不依赖真实节拍。
- **断言强度（较好）**：多数断言落在行为面（Ensure 的 Gen/Spec/ByID、事件名与次数、审计 AfterFP/Resource/Source、状态机终态），并有负向断言（幽灵不复建、停摆不 roll、冻结期零 Ensure）。`-race` 下 converge/restart 子集通过。
- **假阴性风险（真实存在）**：①`fakeRuntime.InspectWorkloads` 从"最近一次 Ensure 的 spec"推导（`fakeruntime_test.go:253-303`）——收敛永远"真的生效"，无法表达"Ensure 返回成功但载体没变"的 provider 缺陷，于是"快通道把 Ensure 成功当收敛成功"这一假设无法被测试证伪（L1 只是有界兜底）；②M1 的钉测用注入态绕开真实冷却形态；③M2 的金色不连真实决策；④BG egress 漏投影零断言（H1）；⑤BG × 动作面重启零用例（M3）；⑥删除 × failed 回滚窗无并发用例（M4）；⑦`TestFreezeGateFailClosedOnUnresolvableTeam` 未到达被测分支（L5）。
- **守卫有效性（本模块相关）**：`transitionguard_test.go` 是有效守卫（AST 扫描 150+ 函数、防盲自检、"Transit 只在 transition.go"+"行写×事实发射配对"双向保鲜）——它真能拦住手搓四件一回潮；`dep_matrix` 双豁免有理由+双向保鲜（收紧计划在案）；`registrysourceanchor`/`eventcode usage` 对收敛事件三链有效。**盲区**：收敛动作的 `runtime.Ensure` 调用面没有守卫（materialize 单序列只是注释纪律）；期望账本写点无守卫（L6）；`convergeVerdict` 金色是装饰（M2）。
- **ADR-0082 验收锚逐条核对**：9 条勾选均可找到代码/测试落点（posture 三配置键+装配透传 `assembly/provides.go:487-523`、doctor/告警行、事件三链、钉测族、Cancel 回卷、账本分支、投影短路），但 #1"表即断言"为字面兑现/实质未接线（M2）、#4 冷却钉测被注入态弱化（M1）、行 10 机制描述不符（M5）。**ADR-0083** 八步全部有落点，唯最后一条 e2e:upgrade 零滚断言未执行（ADR 已标注挂账，属真机面）。

## 7. 未决与置信度

最不确定的 5 点：
1. **M4 竞态未复现**：静态路径明确（ActiveByApp 不含 failed、驱动器不持 appMu），但窗口实际宽度取决于单写者环负载（正常 1 拍 ≈1s，被其他 App 的 30s Ensure 拖住时扩大）。需要注入 Ensure 挂起时序的并发用例才能定量。
2. **H1 未真机复验**：结论由"旧代投影 egress=nil + k3s label 驱动 podSelector"两条代码事实推出；本机无 Docker/k3s，未跑 k3s e2e。建议 R12 k3s 蓝绿腿加 egress:none 断言做机制闭环。
3. **M6 实际成本**：BuildKit 缓存命中时"重建"可能近零成本且 digest 不变；但"重启依赖 builder/registry 在线"这一失败模式与"重启是纯滚动替换"的语义偏差不受缓存影响。
4. **M7 概率**：panic 属防御路径，历史是否发生过未审计日志；影响面（静默停写）与触发概率需权衡。
5. **行 4（滚动停摆）在 k3s 的缺席是否设计诚实边界**：k8s 无 swarm `UpdateStatus=paused` 同构物，可能应作为"运行时差异"写进 ADR 而非缺陷；本报告只作交叉观察，未计入发现。

整体置信度：**中高**。核心收敛/重启机制经全量静态阅读 + 目标包测试（engine 全包 + converge/restart 子集 `-race` + guards）亲验；H1/M2/M3/M5/M7/L* 有代码级直接证据。未覆盖：e2e 真机（Docker 不可达）、k3s/蓝绿/egress 运行时行为、历史 panic 日志、并发竞态的定量复现。
