# ADR-0082: 缺省收敛——App 域收敛升格（Converge by Default）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-18 | 设计母本 `docs/design/2026-10-18-declarative-model.md` §1/§4（唯一规格源）；**推翻 ADR-0005「漂移检测默认开、收敛默认 opt-in」条款**（该条款自 ADR-0010 起已降参考缺省，本 ADR 显式了断）；**推翻 ADR-0022 decision 1** 的「失配仅发事件、只观测不迁移」口径（**decision 3 的 stopped 只观测实质保留**——本 ADR 裁决表对 spec 相符 stopped 维持告警不收敛）；**登记在役引用点 `docs/adr/0046:74`**（「drift 只发事件、Ensure 是唯一写动词（ADR-0005）」——该行的扫描-重建互不干扰论证不受影响，引用的收敛口径自本 ADR 翻向）；**翻案 admission N0「不自动拆除」条款在 Cancel 遗留场景的适用**（2026-10-18 用户裁决：回卷 LatestSucceeded 基线）；ADR-0064（内容锚定等价——收敛的等价判据与停摆 resume 先例）；ADR-0048（双代窗与驱动权口径）；ADR-0017（变更冻结语义）；ADR-0066/0075（写序列 kernel 与审计行）；CONTEXT.md Converge 词条同批修订（「默认 opt-in」翻案）；**修订 2026-10-19（S12）**：§1 表裁为 8 判定行 + 2 非行且判定函数被生产路径真实调用；§2 事实面按「fast 源 ∨ 真实变更」分流；§6 账本归属与 replay 重放标记；§5 detect 的零运行时写为 staging 验证后待办（当前启动基线重放不受门限 = 已知降级） |

## 背景

ring 域（受管/Database/Task）已实现控制器赢：`ReconcileReplayInterval` 60s 强制重放使环外变更由本节拍兜底收敛（`options.go:100-105` 注释原话）。App 域却是「收敛 opt-in」的真缺口：无常驻收敛环，仅启动基线重放；drift 扫描只发事件（`drift.go:3-6`）。四个具体洞：

1. **缺席盲区**：spec 对照只遍历观测到的载体，载体被整删零信号（`drift.go:214-217`）。
2. **期望账本是累积并集**：ensuredSpec/ensuredGen 逐载体 last-write-wins，无集合差删（`observdomain.go:47-55`）——蓝绿收口/L1 失败/进程缩减/回滚产生永久幽灵期望。
3. **冻结只拦 RPC 面**：引擎内写径（对载体的收敛动作）绕过冻结拦截器（`governance/freeze.go:119-130`）。
4. **收敛自激前车**：受管载体曾在 20s 内 49 次版本推进（`runtime.go:179-184` 事故注释）——收敛机构必须与防自激同批立法。

本 ADR 不是从零发明收敛，而是把 ring 域 60s 强制重放的既有控制器语义**升格为显式法则并把 App 域纳入同律**。

## 翻案登记（ADR-0080 范式：被翻条款显式在案）

| 被翻条款 | 处置 |
|---|---|
| ADR-0005「漂移检测默认开、收敛默认 opt-in」 | **推翻**：缺省收敛（posture 缺省 all）。不变量「Ensure 是唯一写动词」保留——收敛动作的执行机构就是 Ensure（materialize→Ensure），升格的是 Ensure 的调用者 |
| ADR-0022 decision 1「失配 → workload.drift_detected，只观测不迁移」 | **推翻「不迁移」半边**：spec 失配经收敛动作拉回（检测与事件面照发不变）；decision 3 的 stopped 只观测**实质保留**（裁决表：spec 相符 + stopped = 告警不收敛） |
| `docs/adr/0046:74` 在役引用（「drift 只发事件、Ensure 是唯一写动词」） | **登记**：引用的收敛口径自本 ADR 翻向；该行论证的「扫描与重建互不干扰」（网络附件不在对照面）不受影响 |
| admission N0「不自动拆除」（`admission.go:275-277` Cancel 注释：已下发 Workload 由后续部署或 Remove 收口） | **在 Cancel 遗留载体场景废止**：取消的部署从未成为期望态，收敛回卷 LatestSucceeded 基线（controller-wins 一致延伸，2026-10-18 用户裁决）。域外/无主孤儿「只登记永不自动删」原则不动 |

## 决策

### 1. 收敛裁决表（8 判定行 + 2 非行；判定函数被生产路径真实调用 + golden 钉死）

十行中 **8 行**由单一判定函数 `convergeVerdict` 承载，并有两个生产构造点：信号侧 `signalVerdictInput`（6 类信号 → 行，`allConvergeSignalClasses` 完备性守卫）与闸门侧 `gateVerdictInput`（冻结/驱动权 → 行）；`convergeStep` 与 `attemptConverge` 都真实调用它，golden `internal/application/engine/testdata/converge-verdicts.golden` 钉 8 行裁决值与优先序（行号留空 05/09 以便读者看出非行）。行 5 为**信号层非行**（无信号即无动作），行 9 **明确未落码**。行 10 的裁决字面是 `replay-baseline`（动作与 `converge` 同为基线物化，差别只在触发来源与事实面标签）。**行号沿用原十行表**（05/09 留空）以免既有引用错位。

| 漂移类 | 裁决 | 机制 |
|---|---|---|
| App spec 失配（image/command/replicas） | **Converge** | driftScan 判定 → 收敛动作（§2） |
| App 载体缺席（期望集 − 观测集 ≠ ∅，新增检测面） | **Converge** | 期望集 = **LatestSucceeded Revision 拍内重投影**（与部署 RenderWorkloads 同源；digest 签名短路控开销）——决策输入是 Revision 真源而非缓存（不变量「观测缓存永不参与决策」合规），缓存幽灵/BG 旧代/进程缩减三类假阳性根源性消除 |
| 稳态 gen 偏离 | **Converge** | 检测**下沉 driftScan**：compareSpecs 的 gen 偏离分支不再让位只读 Watch 流，独立签名键 + 收敛信号（Watch 流 detectDrift 维持只读） |
| 滚动停摆（终态后：spec+gen 均一致、滚动卡中间态） | **Converge** | 重放途经 resumeStalledRollout（5min 护栏沿用，ADR-0064 决策 5）；部署窗内停摆仍走 L1 超时回滚既有语义 |
| spec 相符 + stopped | **告警不收敛（信号层非行）** | 该裁决**不由判定函数承载**：稳态看门狗只发 `workload.stopped` 事件、**不产收敛信号**（`drift.go:182-210`），「无信号即无动作」在信号产生层实现——`convergeVerdict` 无此分支，本行保留为产品决策记录（`TestSteadyStoppedDoesNotConverge` 钉住）。Ensure 走内容等价 skip = 无效动作；swarm 自负责任务重启；自动重启显式非目标（k8s CrashLoopBackOff 同构）——ADR-0022 decision 3 实质保留 |
| 活跃部署中（含 failed 待自动回滚窗） | **跳过** | 复查口径与驱动权同源：ListDriving 语义 per-App 查询（ActiveByApp 不含 failed，单独使用会开竞态窗——重放×回滚交错→vacuously ready→gen 错位→BG 旧代锚漂移） |
| 冻结中 | **挂起** | 不消费签名、不发收敛（§3 fail-closed 闸门）；解冻后下一拍重扫即收敛 |
| 域内多余载体 | **Converge** | **`extra` 快信号**：期望集 = 基线 Revision 投影（与缺席检测同源同 memo），观测集 = `InspectWorkloads` 域内枚举（两 Provider 均按 `fleetly.managed=true` + 域选择器过滤）；差集非空即信号（签名键 `extra\|<ids>`，`drift.go:250-252`）。动作仍是 Ensure 域内移除（swarm `runtime.go:233-239`、k3s 对象 `runtime.go:228-234` 与 Service 收敛 `:257-261`）；部署窗内由行 6 让位（`TestExtraSignalYieldsToDrivingGate`） |
| 域外/无主孤儿观测 | **只登记（未落码）** | **明确未落码**：观测面只有域内枚举（`InspectWorkloads` 带域选择器），平台看不见域外载体；`WorkloadOrphaned` 事件码在 eventcode 注册表不存在（`internal/domain/shared/model/eventcode/events.go` 全表零该名）；`detectDrift` 对非管辖载体直接 return（`observ.go:254-256`）。不新增集群级列举（成本与 RBAC 面大、且「只登记」在用户可见面无消费方）。「只登记永不自动删」原则保留，但**没有登记面**——本行不得计入「表即断言」 |
| Cancel 遗留载体（终态行、部分下发） | **回卷 LatestSucceeded 基线** | 取消/失败/被抢占的部署从未成为期望态。**真实信号源** = 非成功终态且该行已进过物化相位（releasing/observing/rolling-back，`converge.go:774-780`）时落的重放标记（`transition.go:251-258` 的 `transit` 单前门），下一拍由 `replay\|<deployment-id>` 快信号触发统一收敛原语（基线物化；`drift.go:233-238`）；慢通道 60s 重放仍是兜底。**机制不是缺席检测**（遗留载体是「观测有、期望无」，缺席检测按其定义看不见）——原句机制描述错误，本修订更正。翻案 admission N0 在本场景的适用不变 |

### 2. 收敛动作（统一原语）

`lockApp` 内双复查（共享互斥即闭合「重建刚拆载体」竞态——teardown 的 forgetOwner 同在 appMu 内清缓存）：

1. 驱动权语义查询零行（活跃部署含 failed 待回滚窗则本拍跳过）；
2. expect 缓存成员资格复查（防无主孤儿误建）；

→ `materialize(基线 Revision, 行 Generation, isolate=true)`（boundedStep 带界，回传 `capability.EnsureReport`）→ **事实面按真实变更分流**（`convergeSucceed` / `convergeAuditDetailJSON`）：

1. **审计行**（`SourceSystem` + `AfterFP` = 基线 digest + `Action=workload.converge` + `Detail` 携带 `{source, trigger, changed, removed}`）：**只要本次收敛真的写穿了内容等价判定**（`EnsureReport.Changed`/`Removed` 非空）**或是信号驱动（fast 源）**就发——不论触发源。等价于「系统改了你的运行时必留痕」；
2. **事件**（`workload.converged`，outbox 流面，payload 携带 `trigger` = 聚合信号签名）：**维持 fast 源才发**——慢通道/启动重放是例行节拍，进事件流即行垃圾；它们的真实变更由审计行承载（行 8/10 在补 `extra`/`replay` 快信号后本就落在 fast 源，事件自然在场）；非 fast 的真实变更审计走 `LedgerOnly`（偏离理由写在 `convergeSucceed` 的注释里）；
3. `Changed` 的语义是「provider 判定的内容变更」（Workload ID，升序去重）：创建 / 整更新 / 仅副本数 / 停摆 resume 计入；内容寻址 skip 不计；**k3s 冷账本重放（进程重启首拍、本地账本为空）不计**——该形态严格等于重启前已下发内容的幂等重放，计入会让每次重启产生 N 条假变更审计。**已知漏审计窗**：冷账本 + 无快信号的冷窗真实变更不会留痕（方向安全：少一条取证行，不误报），由下一拍信号驱动或下次真实变更审计兜底；
4. **不新建 Deployment 行**（原裁决不变——收敛高频，部署行会成行垃圾；「收敛走 kind=reconcile 部署行」替代案已驳回）；`Removed` 元素是 `"<kind>/<name>"` 形态（被移除者没有期望 Workload ID）。

复用 `replayAppBaseline` 骨架；审计走 SourceSystem 行而非部署 lineage。

### 3. 冻结闸门（fail-closed，补 freeze 语义洞）

收敛前查活跃冻结：owner 的 Team 解析 + `freezes.ActiveForTeam`（平台域走全局冻结 '' 判定）。**判定 fail-closed**：解析失败跳过本拍收敛、下拍重试——不走事件面 ownerTeam 的降级契约（`transition.go:61-80` 解析失败降级 '' 对只读事件合理，搬进执法面即 fail-open）。冻结期检测与事件照发、收敛挂起（不消费签名）；**冻结同时是手动热修的官方通道**（freeze set → docker 干预 → 回写 spec 或 freeze lift——controller-wins 与 runbook 手动干预的正式和解序，收尾批 runbook 盘点落文）。注入面：`Stores` 已含 Freezes（`transact/stores.go:78`），动工仅确认 engine 装配注入。

### 4. 双通道触发、签名生命周期与冷却（防自激 + 防死锁并重）

- **快通道**：driftScan 判定四类信号（spec 失配 / gen 偏离 / 缺席 / 停摆），各自独立签名 → 触发收敛。
- **慢通道（兜底）**：App 域纳入 `ReconcileReplayInterval`（60s）强制重放——与 ring 域同律，自愈窗口有界；provider 侧 no-op 断路器 + 内容等价使重放近零成本。**去抖/冷却只挂快通道**，慢通道无条件但被断路器短路——名实合一。
- **签名生命周期绑定收敛结果**：收敛**成功**才落签名；**失败不落签名**（计入冷却计数）、**挂起不落签名**（记账，解冻重扫）——否则「同失配一次收敛」会把唯一触发机会消耗在失败上，冷却永远到不了 N（死锁形态）。
- **冷却钉死**：连续失败 N=3 → 冷却窗 10min + `workload.converge_failed` 事件一次；解除三径 = 超窗自动 / 漂移签名变化重触发 / posture 切换清零。计数器**内存态**（崩溃循环由 boundedStep + 慢通道节拍等效兜底；启动基线重放共用同一冷却记账，不受冷却豁免盲打）。可发现性：doctor 增收敛冷却行 + `converge_failed` 接内置告警规则（platform-offsite-backup 先例对齐）。
- **事件**：`workload.converged` / `workload.converge_failed` 入 eventcode 注册表（Source 锚 + suggestion + golden + usage 反扫同批）。

### 5. posture 旗标

`all`（缺省——检出漂移即收敛）| `detect`（只报事件、不收敛，检测面照常）。配置键承载；detect 是收敛的观察期逃生门而非缺省。上站裁决：e2e:dind + e2e:upgrade 金丝雀绿后 staging 换装即 posture=all 缺省生效，**不设 detect 观察期**（runbook 手动面盘点同批落地作对冲，2026-10-18 裁决）。

**当前实现与已知降级（2026-10-19 S12 成文）**：

1. detect 门限对 **fast / slow** 源生效（`attemptConverge` 的 posture 门）；**启动基线重放的期望缓存重建不受门限**（`src == baseline` 豁免，ADR-0005 旧行为：缓存重建是检测面的前提）。因此 detect **仍会在启动时静默改载体**（基线重放走 `materialize → Ensure`）——这是**已知降级**，不是「零写」承诺；
2. **待办 D3（detect = 零运行时写）**：删除 baseline 豁免、启动面改为「只重建期望账本」，使 fast / slow / 启动基线重放 / peer 撤销的即时隔离重放四源同受门限。**Lead 裁决（2026-10-19）**：本轮不做；须在 staging 验证后另立批，验证前**不得改 detect 行为**；
3. D3 落地必须一并成文的三条降级（现状同样适用，只是当前因 baseline 重放仍写而部分被掩盖）：① **gen 偏离与稳态 stopped 看门狗在进程重启后失明**——gen 锚只能由 Ensure 回执的 wire-gen 提供，不写则无回执，直到切回 `all` 或发生一次部署；② **撤销 peer 的隔离在 detect 下不执行**（剥离是运行时写），被拦时落 Warn（`convergeOutcomePostureDetect`），恢复 `all` 后下一拍剥离；③ **路由发布与 peer 隔离不变式依赖 `ensuredSpec` 缓存**（`routeExpectations` / `crossProjectRefs` 读它）——启动面必须重建期望账本（投影 spec + 归属），否则用户路由解析不到后端；这也是 D3 必须先补账本重建、再删豁免的原因。

### 6. 期望账本纪律

- ensuredSpec/ensuredGen **只由 Ensure 写入，禁止观测播种**——受管域 gen 观测播种先例不得照抄到 App 域，否则手动热修被播种成期望态，静默击穿 controller-wins。
- **账本条目带写入部署归属**：每条 `ensuredSpec`/`ensuredGen` 记录写入它的 Deployment ID（`observations.ensuredBy`）；drift 的期望侧**只采用写入者 == 当前 `LatestSucceeded` 行的条目**（`drift.go:277-291`）。未成功终态（Cancel/failed/superseded）或进行中部署的物化因此不再污染期望态——那是「上次下发了什么」，不是「期望什么」。**方向安全性**：不采用 = 跳过该载体的 spec/gen 对照（漏报不误报）。**部署窗收窄成文**：在途部署写下的条目在窗内不参与对照（窗内驱动权在部署链，裁决行 6 执法）；窗口结束后条目归属新基线、对照自动恢复。窗口内人工改载体不被 drift 面看见（≤ 一个部署窗），是可接受且必须成文的取舍。
- **非成功终态 → 重放标记**：终态且该行已进过物化相位（releasing/observing/rolling-back，`stateMayHaveMaterialized`）→ 落标记，下一拍 `replay` 快信号以基线重放重建账本（行 10 的兑现机制）；成功终态撤销标记（`transit` 单前门 + `convergeSucceed` + teardown）。标记值携带部署 ID——第二次取消 = 新签名，可再触发。
- **重启冷窗成文**：基线重放完成前 expected 为空 → 缺席检测盲，方向安全（漏报不误报），启动重放收口即闭合（detect 下的冷窗形态见 §5 第 3 条）。
- **succeeded 无期望缓存 → 收敛触发**：扫描面补「有 succeeded 基线但无期望缓存」分支（重建期望缓存本身是收敛动作），修复 rebuildBaselines 单 App 失败即永久自愈盲（`drift.go:66-69` continue 与「下一扫描拍兜底」注释不符）。
- **detect 下的账本重建属 D3 待办**（§5 第 2/3 条）：D3 落地后启动面在 detect 只写 `ensuredSpec` + `ensuredBy`（写入者 = 基线行），**不写 gen 锚**；当前 detect 仍走基线重放完整写账本（§5 第 1 条已知降级）。

### 7. 范围

**Task/Browse 域不入收敛面**（Task 池自有补足环且观测缓存经裁决显式分家；Browse 短命会话）；Watch 流恒只读。判据三问（有运行时镜像吗 / 等价可比吗 / 纠正判据法律安全吗）不过的域，偏离即设计而非缺陷。

## 后果

- App 域获得与 ring 域同律的有界自愈：人工改载体/整删载体/停摆/Cancel 遗留，最迟 60s+一拍内收敛（冷却窗内除外，doctor 可见）。
- 缺席检测以 LatestSucceeded Revision 重投影为期望集：缓存幽灵三类假阳性根源性消除，代价是每拍一次重投影（digest 签名短路，未变基线近零成本）。
- 收敛防自激三件套（no-op 断路器 + 内容等价 + 签名生命周期绑定结果）缺一即回到「20s 49 次版本推进」或「永不收敛」两个失败形态——钉测族承载。
- runbook 手动面（scale-0 收敛法 / replicas-0 救援 / docker rm -f / drain）与 controller-wins 正面冲突的条目，收尾批逐条标注「freeze 优先」标准逃生序。
- ADR-0022 的检测面（driftScan/事件/golden）全部保留，只增收敛半边；`workload.drift_detected` 语义不变。

## 验收锚

- [x] 收敛裁决表 **8 判定行**由单一判定函数承载并**被生产路径真实调用**：信号侧 `signalVerdictInput`（6 类信号，`allConvergeSignalClasses` 完备性守卫 + `TestConvergeVerdictSignalMapping` / `TestConvergeVerdictSignalClassTotality`）+ 闸门侧 `gateVerdictInput`（`TestConvergeVerdictGateMapping`）+ `convergeStep` / `attemptConverge` 两处调用；golden 钉裁决值与优先序（`testdata/converge-verdicts.golden`，8 行、行号留空 05/09）；`verdictFieldProducers` 反射守卫双向保鲜（`TestConvergeVerdictFieldProducersComplete`）
- [x] 行 5（stopped 不收敛）登记为**信号层非行**（`TestSteadyStoppedDoesNotConverge`）；行 9（孤儿只登记）登记为**未落码**（观测面无域外载体、无 `WorkloadOrphaned` 事件码）；两项不计入上方「表即断言」
- [x] 钉测族五类以上：冻结挂起（解冻重扫）/ 活跃部署跳过（含 failed 待回滚窗）/ stopped 不收敛 / 缺席重建 / 幽灵负向（BG 收口后旧代不重建、进程缩减后不重建）/ teardown 竞态 / gen 偏离收敛修复 / 终态后停摆恢复（converge_test.go 八类全钉）
- [x] 签名生命周期钉测：失败不落签名计冷却、挂起记账解冻重扫（TestConvergeCooldownLifecycle / TestFreezeSuspendsConvergeUntilLift）
- [x] 冷却钉测：N=3 → 10min + converge_failed 一次；解除三径各自可达（超窗/签名变化/posture 切换，TestConvergeCooldownLifecycle；基线重放共用记账 TestBaselineReplaySharesCooldownLedger）
- [x] `workload.converged` / `workload.converge_failed` 事件注册三链（schemareg + golden + usage 反扫）
- [x] doctor 收敛冷却行 + `converge_failed` 内置告警规则（converge-failed 系统行入 ListAlertStates；doctor converge 冷却行带处置建议）
- [x] posture 配置键落地 + 缺省 all 钉测（engine.converge_posture / failure_threshold / cooldown_secs 三键 + ParseConvergePosture fail-fast；TestConvergePostureDefaultsToAll）
- [x] 期望账本写点纪律：ensuredSpec/ensuredGen 仅 Ensure 写点（观测播种禁，recordSpecs/recordEnsured 注释钉死）+「succeeded 无期望缓存 → 收敛触发」分支钉测（TestSucceededWithoutExpectCacheConverges）
- [x] 账本归属钉测：非当前基线的写入不被采用（`TestExpectationLedgerSkipsForeignWriter`）；非成功终态按「是否已物化」落重放标记（`TestReplayMarkOnlyAfterMaterialization`）；成功终态撤销标记（`TestReplayMarkClearedOnSucceededDeployment`）
- [x] Cancel/失败终态回卷 LatestSucceeded 基线钉测：非成功终态落重放标记 → 下一拍 `replay` 快信号回卷（`TestCancelReplaySignalDrivesBaselineReplay`），且回卷事实双真（`workload.converged` 事件 + `workload.converge` 审计）；`TestCancelLegacyConvergesBackToBaseline` 保留为慢通道兜底形态（去掉「兑现回卷」的唯一性表述）
- [x] 行 8 域内多余载体钉测（`TestExtraCarriersSignalConvergesDomainRemoval`；部署窗内让位行 6：`TestExtraSignalYieldsToDrivingGate`）
- [x] 慢/基线事实面钉测：慢通道真实变更写审计不写事件（`TestConvergeSlowChannelMaterialChangeWritesAudit`）、纯例行重放零事实（`TestConvergeSlowChannelNoChangeWritesNoFact`）、基线重放真实变更审计 1 条（`TestConvergeBaselineReplayMaterialChangeWritesAudit`）、审计 Detail 变更集（`TestConvergeAuditDetailCarriesChangedSet`）
- [ ] posture=detect **零运行时写**（D3：删 baseline 豁免 + 启动面只重建期望账本；待 staging 验证后另立批）。当前成文为已知降级：启动基线重放不受门限（§5 第 1 条）；gen/stopped 检测失明、撤销 peer 不剥离、路由依赖 `ensuredSpec` 三条降级见 §5 第 3 条

## 变更记录

- **2026-10-19 修订（S12b1 代码 `2740a09d` / S12b2 文档批）**：
  1. §1 表裁为 **8 判定行 + 2 非行**（行 5 信号层非行、行 9 明确未落码，行号保留 05/09 空位），并把判定函数接进生产（`signalVerdictInput` / `gateVerdictInput` + `convergeStep` / `attemptConverge` 两处调用，golden 8 行）；行 8/10 补 `extra` / `replay` 真实信号；
  2. §2 事实面按「fast 源 ∨ 真实变更」分流：`EnsureReport.Changed`/`Removed`、审计 `SourceSystem` + `AfterFP` digest + `Detail{source,trigger,changed,removed}`、事件维持 fast-only（非 fast 真实变更审计 `LedgerOnly`）、k3s 冷账本重放不计变更；
  3. §6 账本归属（`ensuredBy == 当前 LatestSucceeded` 才采用）与非成功终态 replay 重放标记；行 10 的机制描述更正——**不是缺席检测**，是重放标记驱动的基线回卷；
  4. §5 成文 detect 的**已知降级**（启动基线重放不受门限），并把「detect = 零运行时写」（D3）登记为 **staging 验证后另立批**（附三条落地降级）。
