# 引擎终态架构设计（机制原语分层 + 八卫星 + 规则归域，2026-10-30）

> 配套：提案 `docs/proposals/2026-10-30-engine-decomposition.md`（分批程序）、ADR-0087（规则归域与单源写原语）、ADR-0088（卫星包终态与生长律）。本文承载**终态形状与审计数据**；批次与排期归提案，裁决与承诺归 ADR。第一轮交叉评审（2026-10-30，四侧）发现已回填。
>
> 审计口径注：**P3a 收官快照（2026-11-01）**：66 非测试文件（terminalcounts 迁 domain）、engine 非测试 17,446 行（P3a 前 ~17.6k）、domain 11,635 行（P3a 前 9,275+zeabur 增量——规则归域净迁 ~2.4k+）、域状态 struct 14、Engine 字段 64+；首批落盘时为 62/321。差异=B1+B2a 新增五文件（confirm/preview/scaleprocess/diagnose/terminalcounts，归属见 §3）。批次动工前重测。

## 1. 终态蓝图

```
interfaces   proto ↔ IR 翻译唯一居所（消费面窄接口 8 张：Deploy/Task/Exec/Browse/Backup/Registry/System/Node）
application  ┌ engine 核（终态唯一「引擎」，单写者机器）
             │   L0 机制原语 ×7：环骨架/唯写序列/事实发射/备忘/维护门/实体锁/watch+观测底座
             │   L1 复合原语 ×3：converge 决策域/项目解析面/lifecycle+监督
             │   派生层（编排）：admission(+票)/drive/drift 看门狗/materialize/策略动词/平台面
             ├ 卫星包 ×9（P5 全数出包，平级包 internal/application/<sat>/）：intake/backups/
             │   logpipes/metrics 域/execs/manageds/databases/browses/diagnosis（wave-6 收官 9/9）
             │   （经机制端口注入，端口面见 §5；星间零 import——环内互引矩阵执法）
             ├ engineports 契约叶子（核与卫星共享的机制契约单源，按需最小面下沉）
             └ authn/idem/governance/composeintake/apptemplate/schema/transact（同侪包）
domain       聚合 + 规则行为（迁移表/裁决表/策略）+ spec IR + 既有域服务 + capability 端口 + 注册表
assembly     组合根：Deps 按卫星分面注入
```

判据一句话：**domain 学会裁决，application 保留执刑与节拍，interfaces 独占契约翻译。**

词汇注：卫星名避用 telemetry——CONTEXT.md Metrics 词条 `_Avoid_: telemetry（另指事件面）`，`internal/domain/telemetry/` 与 genproto telemetry/v1 已冻结占用该词（事件/告警底座义）；P5 包名同样禁用。

## 2. 机制原语分层（元/派生之辨）

### 2.1 判据（三条，与守卫文化同构）

1. **扇出 ≥2**：被至少两条独立产品线复用；
2. **零上行依赖**：不调用派生层逻辑（唯一例外=枚举式上行钩子，见 2.4）；
3. **守卫或裁决锚**：有冻结面、守卫执法或注释裁决在案。

### 2.2 L0 纯原语（互相正交、无上行依赖）

| # | 原语 | 位置 | 语义 | 锚 |
|---|---|---|---|---|
| 1 | Loop 环骨架 + 环表/DriveOnce | loop.go | tick/kick 时间与事件驱动 | loopguard（AST 钉全仓唯一 ticker，正向锚防哑） |
| 2 | 唯写序列 transit/commit 家族 | transition.go | 行状态机 CAS × 事实发射配对 | transitionguard（配对只许住该文件） |
| 3 | 事实发射 emitter | transition.go:175/187 | emitObservation/emitLedger + eventTeamOf | errcode/eventcode 注册表 |
| 4 | ensureMemo 备忘协议 | ensure.go | ensureRemember/Fresh/Forget 签名短路（7 张 memo map 分住域 struct、单锁） | 注释裁决（engine.go 锁语义零漂移）；影子私造执法随 P4「派生层禁直写」并入 |
| 5 | maintenanceGate | networkrebuild.go:225 | 载体写动词串行化（读半边纪律；12 文件消费） | maintenanceguard（AST 先锁后写） |
| 6 | 实体锁族 | ensure.go:56/71 | appLocks/lockTask（E30 不清退裁决） | 注释裁决在案 |
| 7 | watch 消费 + obs/expect 底座 | observ.go/observdomain.go | consumeWatch 扇出 + 观测缓存 + 期望台账 | 架构 §6（决策读权威表，缓存仅信号） |

勘误（评审回填）：#4 原锚 ensuredledger 有误——该守卫执法的是**期望台账写点**（ensuredGen/ensuredSpec/ensuredBy/expect.expected 白名单，ADR-0082 §6），非 memo 协议；memo 协议现状仅注释裁决，专守卫随 P4 双表守卫补位。

### 2.3 L1 复合原语（L0 组合、仍被多产品线消费）

| # | 原语 | 组合 | 实测证据 |
|---|---|---|---|
| 8 | converge 缺省收敛域（ADR-0082） | memo 族 + expect 台账 | 纯决策零行写：converge/drift/ensure 对 transit/commit 零回调；消费方=drift 看门狗+teardown 告警边 |
| 9 | 项目解析面（五族收拢） | repos 读组合 | team/network/cluster-node/volume/registry-endpoint；现为 P4 wave-0 工作项（从 database.go/backup.go 收拢） |
| 10 | lifecycle/supervision | Loop+wg 排水+ringDeaths 记账 | Start/Stop/health 聚合面 |

**写/载两平面**：行写（transit）与载体写（Ensure 族+maintenanceGate）分离，converge 只驱动后者从不碰前者——决策/执行分离是代码事实。

### 2.4 上行钩子（枚举冻结）

元层对派生层的知识流仅下列入口，**钩子集合本身是冻结面**：

1. watch 扇出 verdict-owner 分发（observ.go handleObservation：Run 轨→task 状态机、App 轨→部署面、节点事件→锚定；C3 裁决；ownerTeam 的 app/database 域解析面属同族分发点）；
2. transition 终态 `markReplay/clearReplay` 通知 converge 重放窗（transition.go:255-257）。

### 2.5 派生层（= 机制原语的组合配方）

受理线（admission+confirm+deployspec+predeploy+render+projection+materialize+**preview+scaleprocess**——B1 预览=受理只读孪生三段单源、ScaleProcess=MergeOverlay 薄动词，均 SubmitSpec 同族）、策略线（bluegreen/rollback/restart/teardown，各为唯写序列的不同使用模式；B1 验票/烧票临界区为受理线成员——**升格裁定 2026-10-31：不入 L0**，扇出=受理单产品线（Deploy/delete 族/Rollback），未来 Task 线等第二产品线用票时按 §2.6 升格）、看门狗（drift=7+8+9）、任务线（taskverbs/minttask/taskobs/schedule/processrestart/componentrestart）、平台面（routepublish/apipublish/console/networkrebuild/hygiene/peers/nodes）。**P3a 规则回填批的动刀面全在派生层**：纯决策函数下沉 domain（ADR-0087），编排留驻。

### 2.6 层间迁移协议

能力会在层间迁移（先例：ensureMemo 2026-10-03 由 task 专用收编为通用协议）。升格/降格走显式 ADR 修订本表（**履约记录：2026-10-31 B1 票据合入触发首次裁定——判派生层受理线，见 §2.5**）；禁止在派生层私造影子原语（第二套备忘/第二套环即红——执法随 P4 双表守卫）。

## 3. 完备性对账（62 非测试文件全量）

| 子系统 | 文件 | 数 |
|---|---|---|
| 真核·组合根 | engine/construct/options/lifecycle/loop/health/events/schemareg | 8 |
| 真核·受理族 | admission/confirm/deployspec/predeploy/render/projection/materialize/**preview/scaleprocess** | 9 |
| 真核·状态机与收敛 | transition/bluegreen/rollback/restart/teardown/hygiene/observ/observdomain/converge/drift/drive/ensure | 12 |
| 真核·任务族 | task/taskverbs/taskobs/taskview/minttask/schedule/processrestart/componentrestart | 8 |
| 真核·平台面 | nodes/peers/routepublish/apipublish/console/networkrebuild | 6 |
| intake | gitmaterial/materials/buildsource/builder/buildlog/registrycontent | 6 |
| backups | backup/platformbackup | 2 |
| metrics 域 | metrics/alerting/channelnotify/exposition/**crashnotify** | 5 |
| execs | exec/execrelay | 2 |
| databases | database/databaseenv/rotate | 3 |
| browses | browse/browseloop | 2 |
| manageds | managed | 1 |
| logpipes | logging | 1 |
| **diagnosis（P2 新晋）** | diagnose（facts 装配器）——terminalcounts 已随 P3a 迁 domain/services/diagnosis（1f6aad93）；**P5 wave-6 出包 internal/application/diagnosis/**（P4 收官「半步驻核（P5 前专议）」裁决出包，九星对账闭合 9/9） | 1 |
| **合计** | 真核 43 + 卫星 23 | **66**（P3a 收官实况：terminalcounts 迁出 engine）→ P5 终态：真核 44 文件驻 engine 包（+resolve.go），九星全部独立成包 |

（predeploy 归受理族——部署受理的预部署作业链，非策略线；本表为唯一归属口径。2026-10-31 增补：B1+B2a 六文件按上述归位——preview/scaleprocess→受理族、crashnotify→metrics 域（crash-loop 通知投递，dispatchNotification 边随归内化）、diagnose/terminalcounts→diagnosis；**首轮 v3 漏 crashnotify 且合计误记 66 为 67，本版自纠——文件清单对账入守卫（P4 锚）后此类漏项机械即红**。diagnosis 曾为半合规先例——规则已在 `domain/services/diagnosis`，application 侧按 ADR-0088 过渡条款驻留包内；**该过渡条款随 P5 wave-6 出包消解——此后新域一律直接立卫星包，不再驻 engine**。）task 归核理由：observ↔task 双向耦合（observ.go runOwner 读 task.workloadRun 路由、taskobs 写 runObs 裁决）+ zeabur 第 2 批热区（已落地，双向耦合仍在）。

## 4. 正交性审计（方法级）

三向边（241 未导出方法、376 内部调用边；**文件级口径：卫星→核 41 / 核→卫星 22 / 星间 9（8 卫星终局口径）**）：

- 卫星→核 41 条中 ~35 条落 §5 机制端口面；**6 条例外登记**：builder/logging→`buildRootCtx`（L1#10）、buildsource→`failDeployment`/`loadSpec`（受理线）、managed→`reconcileNodes`/`publishRoutes`（平台面）——P4 设计时逐条归位（端口扩面或函数改挂）。
- 核→卫星 22 条为组合根与环表方向（环表 10 项中 6 项 step 落卫星文件；transition.go:420 引 build 日志=冻结面持有的唯一卫星边）——**P5 拆包前须逐条裁决**（提案 §4 P5），否则构成 Go 包循环。
- 星间 9 条归因：误置帮手 7 + 真耦合 2，处置后星间硬边归零、**端口终值=0**（volumePinner/databasePassword 终形均为端口/组合根绑定，白名单空表——wave-2b 终值裁定）。**P4 勘误（2026-11-01）**：本表为方法级口径（241 未导出方法），包级函数不在面——intersatellite 守卫上线咬出 11 条函数级边/6 帮手（stderrTail/trimTrailing/铸名×2/指纹×2），wave-3b 全数清偿（豁免 7→0）。**执法期守卫无口径遗漏，设计期审计有——本程序元教训在案**。

误置帮手病根是「域事实帮手随首个消费者落户」——项目解析面成族后以 Source 锚钉五帮手归属（errcode 同款，代码位置锚随 P4 搬移同批更新），新消费者只准经项目解析面。

### 4.1 核→卫星边终表（P5 终态节，wave-6 归档）

设计期审计 22 条（P3a 快照）→ 2026-10-31 复测 ~24（intake 增重）→ P5 各波全处置、**零未决**。终表按处置形态归档（边 = 核消费面 → 卫星；计数口径：文件级，construct.go 与 engine.go 各计一条聚合边——装配生长点与门面/别名面是法定形态不是散边）：

| 处置形态 | 边 | 先例锚 |
|---|---|---|
| 环表 step 卫星方法值 | construct.go 环表 → intake/manages/databases/backups/logpipes/metrics/browses 七步（deployment/schedule/task 留核） | wave-1 logpipes（e.logpipes.Step）；环表与 lifecycle 注册点是 engine 仅有的两个合法生长点（ADR-0088 决策 5，ringregistry 守卫） |
| 装配合法生长点 | construct.go → 九星 New+Deps；engine.go 卫星指针字段 | wave-1 裁定三：核经构造注入持有卫星（否决装配根组合） |
| 域动词直调（卫星指针） | bluegreen→databases（EgressNetworkMap）+intake（材料/卷钉住）；componentrestart→backups（ObjectStore 访问器）；converge→metrics（DispatchSystemAlert）；drive→intake（DriveBuilding）；lifecycle→intake（ResetOrphanBuilds）+backups（SweepInterrupted）+execs（ExecFace 中继挂载）；materialize→intake+databases（InjectDatabaseEnv）；networkrebuild→manageds（ManagedProviders）；observ→intake（LocalImageDigestRef 包名直引）+metrics（EvaluateCrashLoopNotify 评估点）；preview/render→intake（ApplyVolumePinning）+databases（投影事实面）；routepublish→browses（ephemeral 路由合并面）；task→databases（投影/ApplyDatabaseEnv）；taskverbs→intake（MaterialsForProcess） | wave-2 observ 直调卫星导出面；卷钉住/投影事实面 wave-3/4 各波改道 |
| 留核配对裁定的类型引用 | ensure.go→manageds（ManagedGen 类型；gensFor 与 ensureMu 锁配对——表本体 managedGens 留核，wave-4 裁定维持） | wave-4 gensFor 裁定 |
| 门面族 | engine.go 核门面委托：execs 10（受理/路由/中继）+metrics 5（AlertRule/Channel/SystemOffsite）+backups 5（Verify/Download/Platform 面）+browses 7（受理/grant/URL 面）+databases 3（Teardown/Rotate/连接串声明委托）+intake 6（Registry/Clone/git-head/build 日志面）+diagnosis 2（DiagnoseFacts/CheapHit，wave-6）+Kick 族（环挂载留核） | 通则③：gRPC/assembly 消费面消费点零改（wave-1 LoggingProvider 首例） |
| 别名族 | engine.go 类型/哨兵/常量别名+声明委托（execs 类型 9/哨兵 8、metrics 载荷与事件名、backups 类型 3/哨兵、browses 类型 3/哨兵 2/常量 2、databases/intake 哨兵与声明委托、diagnosis Facts/DiagnoseWindow）；events.go→backups 事件铸名别名对（backup/database_restored/platform_backup 五事件名+三载荷） | wave-2 SystemAlertState；wave-5 events 别名对 |
| engineports 下沉（已消解的核内帮手边） | SleepCtx/LogIngestBatchFrames（wave-1）；EmitLedger 签名与 WorkloadID/AuditDetailJSON/ContainerExitFact/DiagnoseWindow（wave-2）；Check/WriteStep/WriteFact/CommitWrite、Memo 与 ensure 族端口、铸名/指纹/尾部帮手（wave-3）；EgressSubset（wave-4）；Build 事件铸名/载荷（wave-5） | 裁定二：契约叶子按需最小面下沉，核内别名零漂移 |
| 冻结边裁决（已消解） | transition.go:420/443 → 函数随卫星走：transitBuildRelease 承终态双副作、核 transitBuild 纯化四件一（transitRunFour 同形），transition 冻结面零残留卫星边 | wave-5 冻结边裁决（ADR-0088 决策 8 前提段处置二） |
| 例外边端口归位（已消解） | 6 条例外边全数归位：rootCtx（wave-1 端口）/spawn/Add-Done（排水单源留核）/failDeployment/loadSpec（wave-5 受理线端口）/reconcileNodes/publishRoutes（wave-4 平台面端口）；registry 端点族维持留核（resolve.go 解析面锚，wave-0） | §5 端口形态定形；resolutionface 守卫 |

执法分工（P5 后终态）：卫星↔卫星 = dep_matrix 环内互引矩阵（import 级，TestApplicationIntraRingMatrix，wave-6）；卫星→engine 根 = enginebackref 跨包腿；核→卫星 = 法定形态（上表）；AST 级分辨力 = intersatellite shapes 合成树（真实树腿退役，见该守卫头注）。

## 5. 机制端口面（卫星注入清单）

| 端口 | 内容 | 原语对应 |
|---|---|---|
| emitter | emitObservation/emitLedger/eventTeamOf | L0#3 |
| writer | commitWrite/transit 家族受控暴露 | L0#2 |
| maintenance | lockMaintenance 读半边（gate；锁入口方法名集为守卫判据一部分） | L0#5 |
| memo | ensureRemember/Fresh/Forget（单锁零漂移） | L0#4 |
| facts | 项目解析面五族 | L1#9 |

**端口形态定形（P4 wave-1 试点裁定，c559a4f1）**：端口一律**函数字段**、命名对齐核方法名（`emitLedger` 对齐使 enginefuncregistry `emit*` 前缀判据零漂移——端口命名与守卫判据互锁）；卫星与核同名依赖副本双持为 P5 过渡形态；嵌入装配走卫星局部指针（影子字段限定赋值陷阱）；接收者名尊重域内既约。已注入端口：logpipes.rootCtx（**六条例外边之一提前归位**）/logpipes.spawn（Add·Done 配对单源留核，卫星结构性无法破坏排水）/execs.emitLedger（emitter 端口全仓首例）。QF1008 双轨约定（construct 限定/lifecycle+测试省略）不豁免。

§4 的 6 条例外边不强行塞入本表——按归属原语扩面或改挂（rootCtx 已于 wave-1 归位，余 5），端口表随之修订（升格协议 §2.6）。机制端口契约测试由本程序 P5 另立（**非 zeabur B3b**——B3b 是 Runtime capability 端口双跑，对象不同）。**既有先例（P2 落地）**：`ObservationSnapshot`/`WorkloadProcess` 导出面 + diagnosbypass 守卫已实现"obs 私有缓存唯一合法出口=导出面"——P4 wave-2 观测窄接口直接沿用该形态。

## 6. 切口生成器方法论（供后续切分复用）

验收器（高内聚低耦合等）只能打分；切点由**已被强制力维护的边界**生成，九类：

- 运行时族：状态所有权/锁拓扑、并发拓扑（11 Loop、Kick 边）、事务边界（UoW/四件一拍）；
- 契约族：聚合目录（transact.Stores）、端口边界（capability/Deps）、消费面簇（engine 外非测试消费 68 个导出方法聚 8 面）；
- 演化族：变更共现（churn 挖掘）、并行冲突面、裁决记录（ADR/豁免表/词汇表）。

**共识法则**：切点在多生成器收敛处；生成器分歧处进 ADR；生成器也会错（maintenanceMu 锁拓扑指向合并，实为共享原语）——生成器提出、验收器裁决、守卫冻结。**时效注**：churn 与边归因是快照量（44 commit，2026-10-30），P4 开工前重核。

## 7. 立域模板（新域生长律的操作面，立法见 ADR-0088）

新域（Pages/Agents/Functions 等解冻日）五条：

1. domain：聚合 + 规则行为 + Repo 接口（P1b 形态）；
2. application：独立卫星包，消费机制端口（**P5 前的过渡形态：engine 包内卫星 struct 驻留，P5 后平移**——见提案 §4 过渡条款）；
3. engine 核：只加一行环表/lifecycle 注册（登记制，见 ADR-0088 决策 5）；
4. interfaces：自己的消费面窄接口；
5. 守卫同批：星间零硬边自动覆盖新包。

## 8. 词汇登记（随 P0 批入 CONTEXT.md，ADR-0085 词汇批同款程序）

| 词条 | 定义 | 备注 |
|---|---|---|
| 机制原语（mechanism primitive） | L0/L1 十件协调机制的统称；判据=扇出≥2/零上行依赖/守卫或裁决锚 | 替代会话中「元能力」措辞（与 domain capability 端口撞词） |
| 卫星域（satellite） | 经机制端口注入、星间零硬边的 application 子系统（终态 8 个） | P3「域化」语言的续篇；包名避用 telemetry |
| 真核 / 派生层 / 立域模板 | 本设计定义的结构称谓 | 口语降级不入册；进正式文档时随用随注 |
| **项目解析面**（Project Resolution Face）| L1#9 五族帮手单源 | 曾用名因与 Finding 事实源/write-class 事实语义撞车而废（2026-10-31 定名，_Avoid_ 在册） |

## 9. 本设计的可推翻性

本设计自身服从推翻程序：每项裁决走 ADR、每项承诺配守卫或显式纪律项、层间迁移走升格协议。两条长存律（裁决归域执刑留应用/新域禁入 engine）之外皆是执行细节，可被未来更好证据再裁。
