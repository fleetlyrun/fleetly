# fleetly 引擎分解程序提案（规则下沉 + 卫星化 + 终态拆包，2026-10-30）

> 状态：**已裁决（2026-10-31 定稿）**——方向经七轮讨论定向（前提=开发阶段无向后兼容、硬理由可推翻）；四侧独立评审（事实核查/分层合法性/程序风险/守卫可执法性）+ 并行批复核（B1/B2a/修复批）发现已全部裁决回填；P1/P2 已由 zeabur 程序承载 ✅，下一站 P3a。
>
> 审计口径注：本文与设计稿的全部方法级数字为工作区快照；**2026-10-31 复测（zeabur 第 2+3 批合入后 67/341；同日四侧修复批 0a6db1b1 合入后 67/343）**——首批落盘时为 62/321，差异=B1+B2a 新增（confirm/preview/scaleprocess/diagnose/terminalcounts）；后续批次动工前重测。

## 0. 提案总表

| 批 | 内容 | 判据 | 依赖 |
|---|---|---|---|
| **P0 文档批（本批）** | 本提案 + 终态设计稿 + ADR-0087/0088 + 交叉评审回填 + **CONTEXT.md 词汇入册**（机制原语/卫星域等，随本批定稿 commit） | 纯文档 | 无 |
| **P1（已落地 2026-10-31）** | = zeabur 程序第 2 批（B1+B1s，e9a8558e..847f0974，ADR-0089）：确认闭环/预览管线/ScaleProcess | — | ✅ |
| **P2 试点（已落地 2026-10-31）** | = zeabur 第 3 批（B2a+B3a，198454ef..a88c9f9d，ADR-0090）：**诊断域按「规则 domain 纯函数/facts application 装配」落地——ADR-0087 试点形态已先行兑现**（六规则+unexplained 兜底在 `domain/services/diagnosis/`，装配器+纯派生在 engine/diagnose.go+terminalcounts.go，diagnosbypass 守卫执法导出面纪律）；**告警评估未随批**（alerting transitionRule 仍在 engine）→ 转 P3a 首单元 | ADR-0087 | ✅ |
| ~~P2' 退路批~~ | **取消**——使命（防 zeabur 第 3 批延期）已因该批落地而不成立；告警评估下沉回归 P3a 七单元清单 | — | — |
| **P3a 规则回填批** | 旧域规则下沉七单元（§4 表）+ terminalcounts 纯派生（P2 落地后新晋候选，§4 表）；「engine 函数基线登记簿」守卫上线（基线+豁免双向保鲜，writeclassguard 范式）；**开工前置=与 zeabur 第 4 批（B2b+B3b）碰撞核查**（B2b 触 managed/nodes/peers=B2b 钉位条款执行点） | ADR-0087 | P2 ✅ |
| **P3b 写原语收敛批** | acceptance.go 受理位改消费 engine 写原语、删自带 commit；**acceptanceguard 同批改针**（形态锚与判据随新形态重铸）；受理检查同事务钉测；独立上站锚、独立回滚 | ADR-0087 | P3a |
| **P4 卫星化批（包内，三波）** | **wave-0**：事实帮手收拢（五族入真核 facts 读面 + Source 锚归属登记，消既有星间边）+ maintenanceguard 判据泛化 + enginebackref 机制复核——**全批第一个 commit**；**wave-1**：零耦合卫星接收者迁移（logpipes/metrics 域/execs/intake；backups 待帮手收拢后视残余边归本波或 wave-2）；**wave-2**：半耦合卫星（manageds/databases/browses，含观测窄接口与 volumePinner/databasePassword 端口设计） | ADR-0088 | P3b |
| **P5 拆包批（终态形）** | 卫星独立成包（**路径裁定进本批首项**，连坐 depExemptions 行与守卫扫描面）、核→卫星边逐条裁决（§4 P5）、环内互引矩阵立法、apitest 经装配门面 | ADR-0088 | P4 收官 + 判稳窗（§4） |
| P4.5（可并入 P4/P5） | O4 审计门：application 全境 genproto 清点（**已知基线 19 个非测试文件**，非个例）→ 驱逐立法（红线②扩展）或豁免+基线冻结 | ADR-0088 | P3 后 |

## 1. 背景：为什么是现在

engine（`internal/application/engine`）是单包 62 个非测试文件、~31k 行（含测试）、`*Engine` 承载 321 个方法（快照口径见抬头注）的汇聚体。三份独立证据指向同一结论：

1. **结构证据**：53 个文件共享同一接收者；域状态 struct（P3 域化批产物）已部分自带小行为（observations 18 个自有方法等），但大行为仍挂 `*Engine`——未归域的是接收者形态，不是全部行为。
2. **churn 证据**（44 个触碰 engine 非测试文件的 commit 挖掘，2026-10-30 快照）：`engine.go` 是几乎全部域文件的 Top 共变搭档——壳是万物共变枢纽。**时效注：P1/P2 本身重刀 engine，P4 开工前须重核共变与边归因，本表数字不作为 P4 常量引用。**
3. **协作证据**：并行会话（zeabur B1，confirm.go 已落 admission 临界区）与第 2 批（PreviewDeploy/ConfirmableVerbs/ScaleProcess 重刀 task/delivery/exec 动词面）都在同一热区施工。

同时主产品线的域知识在 domain 环的物理存在接近于零（domain 全环 9,275 行中核心聚合合计 ~630 行、行为仅 `Terminal()/Active()` 级判定）——决策规则寄生在编排代码里，测试账单可见（converge_test 1,398 行对 converge.go 974 行、task_test 960 对 598）。架构文档 §6 写路径段登记的「序列与规则双真源」（api 侧 acceptance.go 受理位原语 ADR-0024 vs engine 侧 transition.go 驱动域原语）是登记在册的漂移面，2026-10-03 已实际咬过一次（五份手搓拷贝收口）。

## 2. 审计结论（详细数据与归因见设计稿）

- **耦合三向边**（241 未导出方法、376 条内部调用边；首批文件级口径 41/22/9）：卫星→真核 41 条文件级边中 ~35 条落在 5 类机制端口面，**6 条例外登记待 P4 归位**（builder/logging→`buildRootCtx`(L1#10)、buildsource→`failDeployment`/`loadSpec`(受理线)、managed→`reconcileNodes`/`publishRoutes`(平台面)）；真核→卫星 22 条（组合根与环表方向，**2026-10-31 复测 67 文件基线增至 ~24 条，intake 增重 12/10——P5 前须逐条裁决**，见 §4 P5）；**星间 9 条（8 卫星终局口径）**。**稳定性实证（2026-10-31 复测）**：B1+B2a 新增六文件后星间边仍=9 条且归因不变——新代码经导出面/共享抽取/薄动词落位，**零新增星间耦合**。
- **星间 9 条归因**：误置帮手 7（`projectNetworkFactsNamesOnly`×2、`projectTeamOf`、`controlPlaneNode`、`ensureDatabaseVolume`、`databasePassword`×2 调用点）+ 真耦合 2（databases→材料面 `pinVolumes`/`applyVolumePinning`→`volumePinner` 端口；`databasePassword` 迁 databases 后 backup 消费经窄端口）。builds↔materials 克隆/registry 三边经 intake 合并内化。
- **完备性**：62 文件全量对账 = 真核 41 + 卫星 8 域 21，无孤儿无双重归属。
- **task 域归核**：observ↔task 双向耦合（observ 在 Runtime Watch 消费路径读 task.workloadRun 路由表；taskobs 反向写 runObs 做裁决）+ zeabur 第 2 批热区。

## 3. 目标形态（详见设计稿与两份 ADR）

一句话判据：**domain 学会裁决，application 保留执刑与节拍，interfaces 独占契约翻译。**

- 真核 = 部署收敛单写者机器（机制原语 L0×7 + 复合原语 L1×3 + 编排/派生层），冻结面（loop 唯一环骨架、transition 唯写序列、ensureMemo 单锁）不动。
- **9 卫星**（intake/backups/logpipes/metrics 域/execs/manageds/databases/browses/**diagnosis**）经机制端口注入，终态独立成包。diagnosis 为 P2 落地后新晋：规则已在 `domain/services/diagnosis`（半合规先例），application 侧（diagnose.go 装配器 + terminalcounts.go 纯派生）按 ADR-0088 过渡条款以包内卫星 struct 形态驻留。词汇注：卫星名避用 telemetry（CONTEXT.md Metrics 词条 `_Avoid_`，该词已指事件/日志查询面——domain/telemetry 与 genproto telemetry/v1 冻结占用）。
- 新域（Pages/Agents/Functions 等解冻日）一律走立域模板，**禁止入 engine**（ADR-0088 生长律）。

## 4. 各批设计要点

### P3a 规则回填批（ADR-0087 执行批）

下沉单元与去处（下沉判据三条见 ADR-0087 决策 2）：

| 规则单元 | 现居 | 去处 |
|---|---|---|
| Run 观测裁决表（六行对账） | taskobs.go | domain/automation/run ✅（e49cfe51；切分契约执行：发射留 engine 零新码） |
| 漂移签名比对 + stopped 去抖 | drift.go | domain/delivery/drift ✅（8e7652c5；实测无去抖窗——gen 作用域签名零时间参数） |
| converge 姿态/冷却/失败阈值（ADR-0082 策略） | converge.go | domain/delivery/converge ✅（10556757） |
| 告警评估状态机（threshold→Firing/OK） | alerting.go | domain/telemetry ✅（P3a 首单元 9a2f2d95） |
| 备份保留策略 | backup.go（窗判定原在 infra repo——错层勘误） | domain/data/backup ✅（75b80d3b；R05-L6 忠实平移） |
| Schedule 重叠策略 | schedule.go | domain/automation/schedule ✅（73278051） |
| 蓝绿晋升/回退判据 | 判定核散居 drive.go（release/observe/rollback 三门）+observ.go（watchdogBite）+observdomain.go（readyGen）；bluegreen.go 本体是物化编排（2026-11-01 第 7 单元实测勘误，原记 bluegreen.go 不符） | domain/delivery/bluegreen ✅ |
| 终态计数纯派生（事件账本→事实投影） | terminalcounts.go（已删） | domain/services/diagnosis ✅（1f6aad93——B2a 自留射程补齐） |

不下沉（形态铁律）：CAS+四件一拍执行、单写者序、锁纪律、环骨架——**规则下沉 ≠ 状态机搬家**：迁移表下沉，执行机制留 engine（transition.go 冻结面零漂移）。带「单源在 X」旧裁决的单元（projection.go 探针回退链）下沉须连带修订该裁决。

### P3b 写原语收敛批

acceptance.go 受理位改消费 engine 写原语（ADR-0024 决策 4 的兑现，非新翻案）。同批义务：**acceptanceguard 改针**（`s.Tx.Do(` 形态锚与 `commit` 调用判据随新形态重铸，防守卫自认盲/全红）；**受理检查同事务钉测**（engine 原语须暴露事务作用域入口，受理读不退回事务外——否则重开 PutConfig TOCTOU 旧洞）。

### P4 卫星化批（三波）

- **wave-0（全批第一个 commit，不可后置）**：事实帮手收拢（五族入真核 facts 读面 + Source 锚归属登记——P4 验收锚含锚随迁）；maintenanceguard 判据泛化（按 `capability.Runtime` 类型认定接收者字段 + 锁入口方法名集认定，绑定接收者×字段防跨 struct 误报；正向锚补卫星字段先锁后写腿）；enginebackref 豁免表机制复核。**时序理由：wave-1 卫星（backup/materials/metrics/logging 均消费 runtime+lockMaintenance）一旦迁移，旧判据即静默致盲。**
- **wave-1**：零耦合卫星（logpipes/metrics 域/execs/intake；backups 在 wave-0 帮手收拢后残余边仅 databasePassword 端口，归本波或 wave-2 由 wave-0 收官实测定）。
- **wave-2**：半耦合卫星（manageds/databases/browses），观测窄接口与端口设计（volumePinner 实现边 + databasePassword 端口边，白名单条目带理由）在波首设计定形。
- 迁移手法：**嵌入提升**——核→卫星方向调用点零改（提升可见）；**卫星方法体内 `e.xxx` 引用须改写**为卫星字段/端口调用（评审纠偏：改写面不含包内其余调用点，但含被迁方法自身）。锁归属：`platformBackupMu`→backups、`rotateMu`→databases 随域走；`maintenanceMu`（**12 文件**消费）与 `ensureMu` 备忘族（**11 文件**消费）为跨域共享设施，真核持有、gate 注入，锁语义零漂移（纪律项，锁字段×持锁点清单对账承载）；拆锁另案。
- 生成物链：构造器改形必动 wire——**每 commit `generate:all` + `generate:verify` 零漂移**（ADR-0080 先例）。
- 归属表反悔窗：**P4→P5 窗口是 62 文件归属表的最后低成本的改判窗**（P5 后改判需动 import 图+端口面+互引矩阵，成本跳档）——wave 收官时逐域复核归属。

### P5 拆包批（终态形）

- **前置判稳窗（可验收判据）**：≥1 次 staging 换装 + ≥7 天 dogfood 零 engine 相关 journal 错误 + e2e 全绿；窗内发现 P4 缺陷即修补重计窗。
- **首项=卫星包路径裁定**：engine/ 下子包 vs 平级包——连坐 dep_matrix 豁免行（github/upload 注入豁免**随子树平移、条数不增**，杜绝「维持原样」与拆包互锁）、四个 root=engine 守卫的扫描面、loopguard 骨架基名匹配撞名风险。
- **核→卫星 22 条文件级边逐条裁决表**（三选一/接口）：环表 step 经 assembly 注入；反向端口（transition.go:420 引 build 日志=冻结面持有卫星边，须裁决为「冻结面文件的唯一豁免边」或函数随卫星走并同批修订冻结面注记）；函数搬迁。环表/Loop 对象/ringDeaths 监督的拆包后所有权同表裁。
- **环内互引矩阵立法**：dep_matrix 现仅执法跨子树方向（环内互引放行）——为 application 环内子包增设互引矩阵（卫星两两禁 import、engine 核单向、装配经 assembly），「星间零硬边」从 AST 调用级**降级为 import 级**并继续执法（非撤销）。
- **守卫扫描面整节重构**：maintenanceguard/transitionguard/ensuredledger/loopguard 四件 root=engine 硬编码（拆包后静默变哑：真核 41 文件恰在防盲阈值附近、卫星绕 writer 端口直调 `Stores.Tasks.Transit` 失执法）+ enginebackref（防盲锚 namedStructs<60 拆包后必红）+ **卫星回引 `*engine.Engine` 跨包执法**（扫描面扩卫星包或「只准 import 端口接口子包」白名单）。
- apitest/harness 经装配门面（零漂移定义=golden 行为零漂移；harness 消费代码按门面改写属本批预期工作量）。
- 新域过渡条款：P5 前解冻的新域按立域模板以「engine 包内卫星 struct」形态驻留，P5 后平移出包——不违生长律（未挂 Engine 接收者）。

## 5. 与 zeabur 程序合流

- **B2a（第 3 批，已落地 2026-10-31）= ADR-0087 试点已兑现**：规则 domain 纯函数形态首航成功；其 `ObservationSnapshot`/`WorkloadProcess` 导出面 + diagnosbypass 守卫（obs 私有缓存唯一合法出口=导出面）= **P4 wave-2 观测窄接口的既有雏形**，wave-2 设计时直接引用该先例而非新造。
- **B1（第 2 批）= 新机制原语入册**：验票/烧票临界区原语按 L0 邻位登记（设计稿 §2.6 升格协议，B1 合入当天修订 §2 分层表——责任人=P4 程序 owner）。
- **B3b 保持 zeabur 原定义**（Runtime capability 端口行为双跑：Ensure/Remove 幂等、scale-only 零替换），**不是**机制端口契约测试——后者由本程序 P5 另立条目，方法论共用、批不合并。
- **B2b（第 4 批）钉位**：触碰 managed.go（P4 卫星接收者迁移对象）与 nodes/peers（真核平台面）——排在 **P4 开工前或 P5 收官后**，禁止落 P4-P5 窗内。

## 6. 风险与开放问题

1. **ADR 编号竞态**：0087/0088 提交前复核并行会话占号；**改号联动范围=全仓 grep `0087`/`0088`**（提案/设计稿/验收锚多处嵌入）。
2. **开放问题（P3a/P4 开工前清零）**：a) memo 族拆锁立案（倾向不动工）；b) 半耦合卫星观测窄接口形状（P4 wave-2 波首定）；c) 消费面接口缝不独立验收（嵌入已保零变化）；d) 熔断线措辞已按评审修正（ADR-0088：包级 FuncDecl 计数 + P4 收官基线），P4 收官时冻结数值。
3. **小样本与时效**：churn 44 commit 快照；P1/P2 重刀后 P4 前重核（§1 时效注）。
4. **测试面**：包内测试经提升零改（核→卫星方向）；apitest golden 双形态不受影响（DriveOnce/环表不动）。
5. **mise 任务面**：无需新任务（守卫均 go 测试，test/lint 覆盖；e2e 腿 P2 继承 zeabur 程序）——留档防误开通道。
6. **双程序 checklist 主从**：P2 一批三处勾选面（zeabur §10.2 主、ADR-0087 §7 从、feature-checklist 记档），试点改形同步责任=P4 程序 owner。

## 7. 验收锚（各批动工时逐项核，本批不勾）

- [x] P0：CONTEXT.md 词汇入册（机制原语/卫星域/项目解析面三词条 + wording 分诊，ADR-0085 程序）；四文档评审回填完成；ADR-0075 例外①收窄 + 架构文档双真源段改单源化（含过时路径修正）同批落。
- [ ] P2/P2'：诊断域或告警评估按新约定落地；domain 纯函数测试 import 可扫项（零 fake 编排依赖）+ 白盒纪律项分别措辞。
- [x] P3a：**八单元下沉完成**（含终态计数扩容单元；七处勘误/裁定随批落：蓝绿判据居所/ADR-0082 六类同步/R05-L6 忠实平移/schedule 两档实况）+ 函数基线登记簿守卫（e84c6e06 上线，594→575 随批刷新四次）+ domainpurity time.Now 禁列（零豁免立法）+ domain 三红线接收验收全绿。
- [ ] P3b：acceptanceguard 改针 + 受理检查同事务钉测 + golden 双形态零漂移 + transitionguard acceptance 豁免条款同批修订。
- [ ] P4 wave-0：帮手收拢 + Source 锚登记 + maintenanceguard 泛化（含锁入口判据与正向锚）+ 收拢后星间残余边实测=端口白名单预估（volumePinner/databasePassword）。
- [ ] P4 全波：8 卫星接收者归位；星间零硬边守卫（**62 文件归属表入守卫双向保鲜 + struct 归属映射 + 符号反查三要素齐**；白名单条目=端口实现边形态）；「派生层禁直写元能力私有状态」守卫（**元能力字段清单+派生层文件表双表入守卫**；taskobs 合法观测写点白名单；memo 影子私造并入执法）；enginebackref/ensuredledger/transitionRepoFields 随注入形态核账；wire 再生成零漂移；全门禁不新增红（既有红=origin/main 已红项，按案卷口径单列不混提）。
- [ ] P5：卫星包路径裁定 + 核→卫星 22 边裁决表 + 环内互引矩阵 + 守卫扫描面整节重构（四 root=engine 件 + enginebackref 跨包 + 防盲阈值重定）+ apitest golden 零漂移 + 熔断基线冻结（包级 FuncDecl 计数）+ 星间守卫降级记录（AST→import 级）。
- [ ] 生长律守卫：环表条目登记制（条目集白名单双向保鲜；**环表+lifecycle 注册点双锚**，driftLoop 独立 Run 现状收编或双锚覆盖）。
- [ ] P4.5：genproto 清点基线 19 文件复核 → 驱逐立法（红线②扩展）或豁免+基线冻结，二选一落 ADR-0088 修订。
