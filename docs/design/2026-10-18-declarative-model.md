# 严格声明式模型设计（Declarative Model）

- 日期：2026-10-18（同日经三路独立对抗性审查修订，裁决记录见附录 A）
- 状态：设计稿——本方案是三份 ADR（暂记 0081/0082/0083，动工复核编号）的母本与全境审计基线。
- 前提：fleetly 处于开发阶段，按最优设计激进更新、不承担向后兼容（2026-10-18 用户裁决）。

## 0. 问题陈述

三个命题（2026-10-18 设计对话沉淀）：

- **P1** 平台能否精炼为严格声明式模型——CLI、Console、将来的类 terraform 配置面都只声明期望状态，核心引擎保证满足该状态？
- **P2** 声明的状态有三个来源——compose 文档定义的、Console/CLI 创建的（Managed Databases / Routes / Config / Variables 等）、系统动态调整的（自动缩扩容等）——如何建模？
- **P3** 重启这类操作如何在声明式模型中建模？ansible 的参考价值边界在哪？

答案概览：P1——核心引擎已经在该形态上，缺口不在引擎而在**立法与外围写径**（§3/§4）；P2——单一合成后期望态的**三个作者类**，联邦不合并（§2）；P3——**双面**：声明面 `restart_seq` + 动作面 ADR-0069 保留，ansible 只取幂等模块律、check-mode、handler-notify 三样，不取 playbook 过程序与 `state: restarted` 过渡冒充状态（§5）。

## 1. 核心模式

### 1.1 四不变量

1. **期望态单一真源**：每个域以一种作者形态持有期望态真源；引擎的职责是把运行时收敛到期望态；观测缓存永不参与决策，**收敛判定与收敛动作的输入都必须是期望态真源（Revision / 实体行），不是缓存**（ADR-0005「直读再写」律的延伸，见 §4.1 缺席检测）。
2. **Ensure 是唯一写动词**：Runtime 端口窄面契约（`internal/domain/shared/capability/runtime.go:17-28`）——收敛升格自动化的是 Ensure 的**调用者**，永远不新增第二写径。
3. **内容寻址等价**：两级幂等桩——Revision 级 sha256 内容寻址复用（`deployspec.go:189-233`）；运行时级 modulo 代戳三路决策 skip / scale-only / rolling（ADR-0064）。未变声明的重放 = 零滚动 no-op，这是声明幂等性的机制基础。
4. **观测与收敛分面**：检测、观测、审计是只读面（Watch 流恒只读）；写永远经收敛机构（环或部署管线）；单写者循环骨架全平台唯一（`loop.go:6-8` 守卫执法）。

### 1.2 联邦形态（不是一份合并大文档）

期望态是**实体联邦**：每个实体有自己的期望态与自己的收敛环，跨实体只在两个明确点位相遇——部署时物化（compose + 实体引用 + 变量合成 → 冻结 Spec）与各自独立的运行时收敛。五种作者形态/代际方案并存（冻结 Revision blob、DB 行→IR、provider 代码声明、route 行、内存会话）**即终态**，不做大合并重写。`drift.go` 的 typed owner 域（`observdomain.go:273-296`）已是作者类的仓内雏形。

### 1.3 现状定位（证据锚）

| 事实 | 锚 |
|---|---|
| ring 域（受管/Database/Task）已实现控制器赢：60s 强制重放 = 有界滞后收敛 | `options.go:100-105` 注释原话「环外变更…由本节拍的强制重放兜底收敛」 |
| Ensure 是完整命名空间收敛原语：缺失重建 + 域内多余载体移除 | swarm `runtime.go:160-163`（create 分支）、`:217-225` |
| App 域收敛 opt-in 是真缺口（无常驻环，仅启动基线重放） | `drift.go:3-6`「drift 只发事件，Ensure 是唯一写动词」 |
| 缺席盲区：spec 对照只遍历观测到的载体，载体被整删零信号 | `drift.go:214-217` |
| 期望缓存（ensuredSpec/ensuredGen）是逐载体 last-write-wins 的累积 map，无集合差删 | `observdomain.go:47-55`（仅 upsert；唯一清除是整 App forgetOwner） |
| freeze 只拦 RPC 面，引擎内写径绕过冻结 | `governance/freeze.go:119-130` 拦截器 |
| 驱动权口径是 ListDriving（含 failed 待自动回滚），ActiveByApp 不含 failed | `deployment/repo.go:126-135`、`persistence/deployment/repo.go:92` |
| 收敛自激环有实证前车（受管载体 20s 内 49 次版本推进） | `runtime.go:179-184` 事故注释 |

## 2. 三作者权属模型（P2 答案）

### 2.1 三类作者

| 作者类 | 定义 | 现役代表 | 权威/节律/持久化 |
|---|---|---|---|
| **Document**（文档作者） | 部署事务的输入文档，冻结成 Revision | compose（ADR-0074 受理 + 原文冻结）；模板实例化产出的 spec | 事务性（部署时刻）；内容寻址；不常驻不参与每拍收敛 |
| **Entity**（实体作者） | 持续在线的平台实体行 | Databases / Routes / Configs / Variables / Volumes / Networks / Secrets | 持续性（每次编辑 = 实体自身变更）；行级权属 + 独立收敛环或 Ensure 时输入 |
| **System**（系统作者） | 控制环与平台决策 | 受管域代码声明、pin 决策、凭证铸造、模板版本、（将来）缩扩容执行 | 派生槽位；可从 succeeded 基线重放重建 |

App spec 与实体的关系是**按引用绑定，不按值内嵌**：compose 的 external 声明律（`composeintake/intake.go:397-429`）已是引用闸门——引用不存在在受理即失败。

### 2.2 SYSTEM 三铁律

1. **授权槽位**：系统只写用户经策略委托的字段（无缩扩容策略，系统永不碰 replicas——现状即如此，`replicas` 纯用户声明）。
2. **drift 对照 effective 而非 authored**：自动调整后，收敛/漂移判定对照合成后期望态，否则控制环输出每拍都是漂移假阳性（2026-10 drift 风暴同族教训）。
3. **声明重放不清洗系统态**：声明面重跑不回滚系统调整；显式删除策略才让槽位回落用户值并收敛。

### 2.3 冲突语义表

| 冲突 | 裁决 | 执法点 |
|---|---|---|
| 文档 vs 实体（字段重叠） | 分界即所有权：workload 形状归文档、平台资源归实体 | 受理闸门 |
| 用户 vs 用户（push vs console） | ADR-0072 已裁（push 合并 / spec_file overlay；只读观测面见 ADR-0071） | 在役 |
| 用户 vs 系统 | 无授权 = 用户赢；有授权 = 系统在策略区间内赢 | 策略实体 + 合成函数 |
| 任何来源 vs 运行时 | effective 赢（这是收敛的定义） | drift + Ensure |

### 2.4 合成点先例

AppVariable 三级合成（Shared 底 < 源声明 env < AppVariable 顶，`spec/variables.go:43-88`，冻结咽喉 `deployspec.go:158-178`）已是跨实体合成的现役实现；将来配置面不新造合成器，是把它升格为通用律。

## 3. 写面立法（ADR-0081：Write Class + Author Class）

### 3.1 执法面：Go 表先行，proto 注解推迟（审查修订）

**proto 全量 RPC 实数：14 个服务文件、153 个方法**（不含 spec.proto/error.proto 两个无服务文件）。立法执法面 = **ADR 附录完整分类表 + Go 侧表守卫**（`freezeguard_test.go:109` 三桶全分类的既定形态）；一方 proto 注解（`classification.proto`，MethodOptions 扩展——该消息现占用 1042（openapiv2）与 52301（grpcapi method_auth），届时取 5240x 段）**推迟到配置面立项同批**：注解面在本程序内唯一消费者是守卫自身，运行时零消费，按 §3.4「不造无人消费的机制」原则不应前置（对抗审查 C3 裁决接受）。配置面立项时，分类表已是经守卫保鲜一轮检验的成熟资产，注解只是机械搬运。

### 3.2 分类表（ADR 附录底稿；「未分类即红」的完整清单）

**ACTION（动作写，例外清单，每条带理由）**：CancelDeployment、RestartProcess（ADR-0069 零 lineage）、RestartComponent、TriggerBackup、VerifyBackup（产出验证事实）、TriggerPlatformBackup、RotateDatabasePassword、RotateHookToken、RefreshGitHook、RefreshTemplates、TriggerSchedule、StopRun、StopTask、RenewTask（租约续期）、DrainNode（迁移语义归编排器）、RebuildNetwork（修护动词）、TestNotificationChannel、Login、AcceptInvitation、ReceiveWebhook、StartManifestFlow、ConsumeManifestCode、CompleteManifestFlow（仪式三件）。

**铸造短命凭证/会话族**（边界案例组，统一裁决）：CreateExecSession、StreamExecSession、IssueEventTicket、BrowseDatabase——铸短命会话/一次性票据，ACTION 语义；scope 方向豁免沿用 `scopeguard_test.go:37-52` 既有 scopeOpExemptions 文案。

**DECLARATION（声明写）**：全部实体 Create/Put/Delete/Set/Revoke（projects/apps/secrets/configs/shared+app variables/volumes/networks/databases/routes/tasks/schedules/users（含 SetUserPassword——无收敛面接棒，状态即事实，与 Rotate 系的界线即「有无收敛面接棒」）/teams/roles/tokens/invitations/告警通道与规则）、Deploy、**Rollback**（= 向旧 Revision 的声明性回归，经管线 Replay）、**ScaleTask**（desired_concurrency 落行）、**Cordon/Uncordon**（节点 schedulable 期望态）、EnrollNode（节点实体落地）、SetChangeFreeze / LiftChangeFreeze（治理态）、InstantiateTemplate、UploadSource、SetGitHook / DeleteGitHook、SetGitHubApp / DeleteGitHubApp、Declare/Approve/RevokeNetworkPeer。

**READ**：其余全部（List/Get/Wait/Stream/Browse/Download/Diff 等）。

其余边界案例显式钉死：Drain=ACTION vs Cordon=DECLARATION（同域两动词分属两类）；RebuildNetwork=ACTION（期望态是行，重建是修护）；Rotate 系=ACTION 但效果经材料指纹进收敛面——**动作改事实 + 收敛面接棒**的混合范式，写进 ADR 作为 ACTION 与 DECLARATION 协作的标准形态。

### 3.3 守卫与旧表一致性

新守卫 `writeclassguard_test.go`（Go 侧表 + 完整分类：153 方法逐个落桶，未分类即红）+ ACTION 双向保鲜（例外不再命中即红）+ 一致性断言（**单向**：DECLARATION ⇒ scope WRITE/ADMIN；ACTION 允许 READ-scope，经铸造族豁免通道显式登记——`scopeOpExemptions` 五条既有先例对齐）。与 FrozenVerbs 断言：FrozenVerbs ⊆ DECLARATION∪ACTION；idem create 前缀 ⊆ DECLARATION∪豁免。

词汇消歧（审查 A10）：WriteClass 的 ACTION 与 Scope 词条的 `resource:action`（read/write 授权轴）同词异轴——Declaration 词条内一句话消歧（授权 action 见 Scope 词条），不改名。

### 3.4 Author Class 学说

字段/实体级权属表入 ADR 附录（含 §2 全部）；autoscale 落地时才建字段权属注册表 + 守卫（不造无人消费机制，维持 backlog v1-out）——本条原则同样约束 §3.1 的执法面选型。

## 4. 收敛升格（ADR-0082：Converge by Default）

**叙事**：不是从零发明收敛，而是把 ring 域 60s 强制重放的既有控制器语义**升格为显式法则并把 App 域纳入同律**。**翻案清单**（ADR 关联行显式登记，ADR-0080 范式）：推翻 ADR-0005「收敛 opt-in」条款（自 ADR-0010 已降参考缺省）；推翻 ADR-0022 **decision 1** 的「失配仅发事件」口径（decision 3 的 stopped 只观测**实质保留**——本设计裁决表对 spec 相符 stopped 维持告警不收敛）；登记下游在役引用点 `docs/adr/0046:74`（正文引用「drift 只发事件、Ensure 是唯一写动词」）。**同批修订 CONTEXT.md Converge 词条**（现文「默认 opt-in」在翻案后成为假法）。收敛裁决表落 ADR-0082 正文 + 落码 golden，不落 CONTEXT.md（该文件只承载词汇不承载架构）。

### 4.1 收敛裁决表（落码 + golden 钉死 = ADR 承诺执法）

| 漂移类 | 裁决 | 机制 |
|---|---|---|
| App spec 失配（image/command/replicas） | Converge | driftScan 判定 → 收敛动作（§4.1 末） |
| App 载体缺席（期望集 − 观测集 ≠ ∅，**新增检测面**） | Converge | 期望集 = **LatestSucceeded Revision 拍内重投影**（RenderWorkloads 同源；digest 签名短路控开销）——Revision 是决策输入（不变量 1 合规），缓存幽灵/BG 旧代/进程缩减四类假阳性根治 |
| 稳态 gen 偏离 | Converge | 检测**下沉 driftScan**：compareSpecs 的 gen 偏离分支不再让位只读 Watch 流，独立签名键 + 收敛信号（Watch 流 detectDrift 维持只读） |
| 滚动停摆（终态后：spec+gen 均一致、滚动卡中间态） | Converge | 重放途经 resumeStalledRollout（5min 护栏沿用）；部署窗内停摆仍走 L1 超时回滚既有语义 |
| spec 相符 + stopped | **告警不收敛** | Ensure 走内容等价 skip = 无效动作；swarm 自负责任务重启；自动重启显式非目标（k8s CrashLoopBackOff 同构） |
| 活跃部署中（含 failed 待自动回滚窗） | **跳过** | 复查口径与驱动权同源：ListDriving 语义 per-App 查询（ActiveByApp 不含 failed，单独使用会开竞态窗） |
| 冻结中 | 挂起 | 不消费签名、不发收敛；解冻后下一拍重扫即收敛 |
| 域内多余载体 | Converge（顺带） | Ensure 域内移除既有语义（`runtime.go:217-225`） |
| 域外/无主孤儿观测 | 只登记 | WorkloadOrphaned「只登记永不自动删」原则不动 |
| Cancel 遗留载体（终态行、部分下发） | **待裁决** | 三案：回卷 LatestSucceeded 基线（controller-wins 一致延伸，推荐）/ 保持现状 / 冻结收敛至下次部署；裁决后翻案登记 admission N0「不自动拆除」条款（`admission.go:275-277`） |

**收敛动作（统一原语）**：`lockApp` 内复查（①驱动权语义查询零行；②expect 缓存成员资格——teardown 的 forgetOwner 同在 appMu 内清缓存，共享互斥即闭合「重建刚拆载体」竞态）→ `materialize(基线 Revision, 行 Generation, isolate=true)`（boundedStep 带界）→ 审计行（source=system，AfterFP=基线 digest）+ `workload.converged` 事件。复用 `replayAppBaseline` 骨架，不新建 Deployment 行（审计走 SourceSystem 行而非部署 lineage——避免收敛高频行垃圾）。

### 4.2 冻结闸门（补 freeze 语义洞）

收敛前查活跃冻结：owner 的 Team 解析 + `freezes.ActiveForTeam`（平台域走全局冻结 '' 判定）。**判定 fail-closed**：解析失败跳过本拍收敛下拍重试——不走事件面 ownerTeam 的降级契约（`transition.go:61-80` 解析失败降级 '' 对只读事件合理，搬进执法面即 fail-open）。冻结期收敛挂起、检测与事件照发；**冻结同时是手动热修的官方通道**（freeze set → docker 干预 → 回写 spec 或 freeze lift，见 §7 runbook 盘点）。注入面：Stores 已含 Freezes（`transact/stores.go:78`），动工仅确认 engine 装配注入。

### 4.3 双通道触发、签名生命周期与冷却（防自激 + 防死锁）

- **快通道**：driftScan 判定（spec 失配 / gen 偏离 / 缺席 / 停摆四类，各自独立签名）→ 触发收敛。
- **慢通道（兜底）**：App 域纳入 `ReconcileReplayInterval`（60s）强制重放——与 ring 域同律，自愈窗口有界；provider 侧 no-op 断路器（C19-1）+ 内容等价使重放近零成本。**去抖/冷却只挂快通道**，慢通道无条件但被断路器短路——名实合一（§4 叙事引用的 ring 先例即此形态）。
- **签名生命周期绑定收敛结果**：收敛**成功**才落签名；**失败/挂起不落签名**（失败计入冷却计数，挂起记账解冻重扫）——否则「同失配一次收敛」会把唯一触发机会消耗在失败上，冷却永远到不了 N（审查 B4 死锁形态）。
- **冷却钉死**：连续失败 N=3 → 冷却窗 10min + `workload.converge_failed` 事件一次；解除 = 超窗自动 / 漂移签名变化重触发 / posture 切换清零。计数器**内存态**（崩溃循环由 boundedStep + 慢通道节拍等效兜底；启动基线重放共用同一冷却记账，不受冷却豁免盲打）。可发现性：doctor 增收敛冷却行 + `converge_failed` 接内置告警规则（platform-offsite-backup 先例对齐）。
- 事件：`workload.converged` / `workload.converge_failed`（eventcode 注册：Source 锚 + golden + usage 反扫）。

### 4.4 范围与账本纪律

**Task/Browse 域不入收敛面**（Task 池自有补足环且观测缓存经 C3/P1-7 裁决显式分家；Browse 短命会话）；Watch 流恒只读。**期望账本纪律**（ADR 钉死）：ensuredSpec/ensuredGen **只由 Ensure 写入，禁止观测播种**（受管域 gen 观测播种先例不得照抄到 App 域——否则手动热修被播种成期望态，静默击穿 controller-wins）；重启冷窗（基线重放完成前 expected 为空 → 缺席检测盲，方向安全）成文入 ADR；扫描面补「有 succeeded 基线但无期望缓存 → 收敛触发（重建期望缓存本身是收敛动作）」，修复 rebuildBaselines 单 App 失败即永久自愈盲（`drift.go:66-69` continue 与注释「下一扫描拍兜底」不符）。仓内论据：enforcePeerIsolation（`peers.go:175`）是观测环内自动纠正的唯一在役先例——环可以纠正，前提是判据法律安全。

## 5. 重启双面（ADR-0083：restart_seq）

### 5.1 机制链（八步）

1. `ProcessSpec.restart_seq`（uint32、非负；**单调性是调用方约定，平台保证最终态 = 最后执行的声明**——受理不加执法，ADR 显式声明；命名已避 epoch/apply 禁词）。
2. SubmitSpec 冻结（digest 覆盖——seq 变更即新 Revision）。
3. `projectRevision` 投影 → `capability.Workload.RestartSeq`（**新增 typed 字段**；Workload 无 Labels/Annotations 泛型槽，窄面端口演进）。
4. swarm 铸 `fleetly.restart_seq` 载体标签：**seq=0 不铸标签、canonical 对照对缺失标签按 0 归一**——存量载体升级首拍零滚动（bb5259d「label 消失+方言归一→全量滚替→torchwood-pg WAL 损坏」P0 前车，runbook:113-121）；k3s 账本 canonical key 同批纳入（动工核在役形态）。
5. 内容等价三路：seq 变 → full CAS update → 滚动替换 = 声明式滚动重启（k8s rollout restart 同构，provider 语义零新增）；未变 → no-op（账本冷启动由 serviceSpecEqualJSON 兜底，已验证成立）。
6. **seq-only diff 规则（判定延迟到执行时）**：驱动器拾取时对**实际最新 succeeded 基线**重算 To/From 树 diff，diff ⊆ restart_seq 才标 kind=restart + `PreDeploy=PreDeployDone`（镜像 rollback 处理 `admission.go:214-219`；树 diff 助手下沉 `domain/shared/spec`）——受理时判定的 From 锚可能已被更早排队部署推进，受理时判定会让「回退中间变更却免跑迁移」的组合静默通过（审查 B8）。
7. 审计入 lineage（Deployment 行 + kind=restart）——与 ADR-0069 的张力显式记录：0069 决策 1「重启不入部署叙事」被**部分**推翻（声明面入叙事，动作面仍零 lineage不动）；0069 决策 6「App 级整体重启动词不做」与 §10.2 CLI 动词张力同批记录。
8. 迁移不得因重启重跑（PreDeployDone 语义即此）。

### 5.2 边界裁决

Rollback 回落低 seq 显式豁免单调约定（操作者意图优先）；BG 策略进程按 BG 舞步重启；compareSpecs 不对照 restart_seq（意图字段非可观测态——同一字段在内容等价（收敛面）与 spec 对照（漂移面）两比对器的分际是**显式规则**，两个守卫测试以此为共同法源）；compose 方言 v1 不暴露（spec 级字段，入口 = spec_file / API / console Spec 编辑器 / 将来配置面）；Component restart 维持纯动作（受管域无用户 spec 面）。

## 6. 全境收敛审计（设计自证）

**三问判据**：一个域是否应收敛 = ①有运行时镜像吗？②等价可比吗（终态可 diff）？③纠正判据法律安全吗（幂等可逆）？任一问不过，偏离即设计而非缺陷。

### 6.1 完全收敛区（模式本体）

App 域（冻结 Revision → 部署环 → materialize → Ensure）；Database 域（行 → IR → databaseLoop → 指纹门 EnsureGeneration）；受管四件（provider 代码声明 → managedLoop）；Route 面（行集 + 签名短路 + kick）；console/API 公网发布（配置 → 合成路由并入 publishRoutes，`apipublish.go:34-46`）。

### 6.2 退化收敛区（声明在、收敛半边空转或间接）

材料/结构实体（Config/Secret/Variable/Volume/Network）：Ensure 时输入间接生效——是**输入不是状态**，无载体形态可对照。Identity/治理/告警实体：状态即事实，无运行时基底。刻意的不对称：变量轮换需显式 redeploy（内容，digest 面）vs DB 凭证轮换下一拍 Ensure 生效（材料，永不进 spec，ADR-0014）。

### 6.3 原则性例外（四族，偏离有据）

1. **一次性语义**（等价测试不成立）：Task/Run（desired=completed-once + 补足池 + 租约；缓存分家裁决 `task.go:5-15`）；Schedule（只收敛何时拍，错过窗口补一拍不追补 `schedule.go:9-18`）；preDeploy jobs（PreDeployDone 反收敛标记）；Builds/Backups/Restore（过程性动作；restore-pending 闸门是收敛环对一次性物化的等待协议）。
2. **过渡态动词**（无终态可持有）：RestartProcess/RestartComponent（本程序给 App 面补声明面）；Rotate 系（动作-收敛混合范式）；Drain vs Cordon 分立。
3. **人裁保留**（删除不可逆）：WorkloadOrphaned 只登记 vs 卫生清扫（法律判据齐全才自动删）。
4. **仪式与修护动词**：EnrollNode、manifest flow、RebuildNetwork（经 maintenanceMu 与 Ensure 串行）、exec/browse 铸造族、Browse。

### 6.4 无理由欠账（三问全过却未收敛——本程序标的）

App 域收敛 opt-in；缺席盲区；冻结闸门只拦 RPC；restart 无声明面。

## 7. 实施程序（小步 commit，守卫 + 测试同批）

- **Batch 0 预检**：核对 drift.go 未提交改动（并行会话）；复核 ADR 下一号；mise install。
- **Batch A 立法**：CONTEXT.md 词条（新增 Declaration / Action（含授权轴消歧）/ Write Class / Author Class / Posture；restart_seq 并入 Spec 词条注记；**修订 Converge 词条**——「默认 opt-in」翻案；裁决表不落 CONTEXT.md）+ ADR×3（含验收锚 checklist 与翻案登记行）+ 程序计划文档 `docs/plan/2026-10-18-declarative-model-program.md`。
- **Batch B 写面立法**（Go 表形态，1-2 commit）：ADR 附录 153 方法完整分类表 + `writeclassguard_test.go`（完整分类 + 双向保鲜 + 单向一致性断言 + 铸造族豁免通道）。proto 零触碰。
- **Batch C 收敛升格**（2-3 commit）：C1 缺席检测（重投影期望集 + digest 签名短路）+ App 域双通道接线（快通道四类信号 + 60s 慢通道 + 签名生命周期 + 冷却 + 冻结 fail-closed 闸门 + 审计行 + 两事件 + doctor/告警接线）；C2 裁决表落码 + golden + posture 旗标。测试翻向 + 钉测族扩充：冻结挂起（解冻重扫）/ 活跃部署跳过（含 failed 待回滚窗）/ stopped-不收敛 / 缺席重建 / **幽灵负向**（BG 收口后旧代不重建、进程缩减后不重建）/ **teardown 竞态** / **gen 抹标签收敛修复** / **终态后停摆恢复** / 失败冷却与解除。**skills 同批**：deploy-diagnose 漂移决策树翻向（spec 失配自愈语义）。
- **Batch D restart_seq**：spec.proto + Workload.RestartSeq + 投影 + swarm/k3s 内容等价纳入（contentequiv 测试加例：seq 变→update、同 seq→skip、缺失→create、**存量无标签×期望 0→skip 升级零滚**）+ 执行时 seq-only 判定 + CLI golden（**含 selfdescription.golden.json、schema/explain golden、revisions get / apps workloads seq 非零夹具**）+ `console:gen && console:build`（dist 同 commit）+ console Spec 编辑器对 restart_seq 的控件/校验影响评估 + **skills restart 双面指引**。
- **Batch E 可选切片**：E1 受管/route 收敛 memo 持久化；E2 projects 项目级逃生门；E3 ring 域 ensuredSpec 记录 + spec 对照（检测可见性）。
- **收尾门禁与文档**：mise run test / lint / console:verify 全绿（generate:verify 本程序 proto 仅 D 批触碰）；e2e:dind + e2e:upgrade（**预记两分支**：升级首拍零滚为绿；若现役载体带非零 seq 形态则一次性收敛为预期红，需人工判读——BB 事故重演红线）；eventcode golden + usage 反扫；**runbook 手动面盘点**（scale-0 收敛法 / replicas-0 救援 / docker rm -f / drain 逐条标注，写入「freeze 优先」标准逃生序）；runbook 记录条目；staging 换装另窗挂账。

## 8. 非目标

terraform/HCL 配置面（另立计划；届时 proto 注解随其立项同批，§3.1）；autoscale 实现（维持 v1-out backlog，只立 §2 学说）；变量轮换自动重部署（维持显式部署语义）；stopped 自动重启；五种作者形态合并重写；Task/Browse 域收敛面；Component restart 声明面。

## 9. 风险与坑

- 并行会话：drift.go 现役未提交改动；提交前复核暂存面；ADR 号防撞车（0066 撞车实咬）。
- 收敛防自激与防死锁并重：no-op 断路器 + 内容等价 + 签名生命周期绑定结果，三件缺一即回到 20s 49 次版本推进或「永不收敛」两个失败形态之一。
- 升级首拍滚替红线：restart_seq 的 seq=0 归一裁决是零滚前提，e2e:upgrade 与 bb5259d 前车双保险。
- freezes 注入面：Stores 已含 Freezes，仅确认 engine 装配（审查 A12 修正：非风险是待办）。
- 词汇守卫连生成物都扫：新命名已避禁词（apply/manifest/epoch/workspace/lambda/site/snapshot/sync 系）；注释中文、用户可见文案英文。
- golden `-update` 与变更同 commit（含 selfdescription/schema）；本机代理 10801 偶发拒连探活重试。

## 10. 裁决记录（2026-10-18，四项均按推荐案裁决）

1. **收敛上站**：e2e:dind + e2e:upgrade 金丝雀绿后，staging 换装即 posture=all 缺省生效（不设 detect 观察期；runbook 手动面盘点同批落地作对冲）。
2. **重启双面操作者契约**：`fleetly apps restart` **默认走声明面**（对全部进程 bump restart_seq → 正常部署管线，有 lineage、跳迁移）；`--runtime` 旗标走 ADR-0069 零 lineage 动作面作逃逸。ADR-0083 显式推翻 ADR-0069 **决策 6**（App 级整体重启动词不做）——CLI 默认面即 App 级声明式重启。
3. **Cancel 遗留载体**：**回卷 LatestSucceeded 基线**——取消的部署从未成为期望态，controller-wins 一致延伸；ADR-0082 翻案清单登记 admission N0「不自动拆除」条款在该场景的废止（`admission.go:275-277`）。
4. **Batch E**：A-D 收官即收本程序，E 三切片（memo 持久化 / 项目级逃生门 / ring 域 spec 对照）按需另批。

## 附录 A：对抗性审查裁决表（2026-10-18 三路独立审查）

三路审查（A 立法与架构一致性 / B 并发与分布式正确性 / C 产品面与工程账）各带证伪立场独立进行，29 项发现，裁决如下。「落点」为本文档修订处。

### A 路（立法与架构一致性）

| # | 级 | 主张 | 裁决 | 落点 |
|---|---|---|---|---|
| A1 | P0 | 分类表漏 SetUserPassword/RevokeToken/IssueEventTicket/BrowseDatabase；实数 153 非约 100；ConsumeManifestFlow 应为 ConsumeManifestCode | **接受** | §3.1/§3.2 重编（153 方法表 + 铸造族 + identity 判据） |
| A2 | P1 | Converge 词条「默认 opt-in」翻案后须同批修订，Batch A 漏列 | **接受** | §4 叙事 + Batch A |
| A3 | P1 | push/spec_file 裁决是 ADR-0072 非 0071 | **接受** | §2.3 |
| A4 | P1 | 翻案对象定位错位：真正被翻的是 0022 decision 1（事件-only），decision 3（stopped 只观测）实质保留；0046:74 在役引用未登记 | **接受** | §4 翻案清单重写 |
| A5 | P1 | scope 方向断言与 VerifyBackup（READ-scope ACTION）冲突；铸造族需显式消化 | **接受** | §3.3 断言单向化 + 豁免通道；§3.2 铸造族组 |
| A6 | P2 | 期望集缓存 vs 不变量 1「缓存不参与决策」未立锚 | **接受**（经 B1 根治） | §1.1/§4.1 重投影 |
| A7 | P2 | 冻结挂起×签名去抖互锁；裁决表缺「活跃部署」「冻结中」行 | **接受** | §4.1 补行 + §4.3 签名生命周期 |
| A8 | P2 | ADR-0069 决策 6（App 级重启动词不做）张力漏记 | **接受** | §5.1 步 7 + §10.2 |
| A9 | P2 | 裁决表入 CONTEXT.md 违「只载词汇」体例；Posture/restart_seq 未入词条清单 | **接受** | Batch A 词条清单修正 |
| A10 | P3 | ACTION 与 Scope 词条 resource:action 同词异轴 | **接受消歧、驳回改名**（IMPERATIVE 成本高收益低） | §3.3 消歧注 |
| A11 | P3 | 扩展号论证不精确（52302 是 ServiceOptions；MethodOptions 实占 1042+52301） | **接受**（随注解推迟降为注记） | §3.1 |
| A12 | P3 | Stores 已含 Freezes，风险条不成立 | **接受** | §4.2/§9 修正 |

### B 路（并发与分布式正确性）

| # | 级 | 主张 | 裁决 | 落点 |
|---|---|---|---|---|
| B1 | P0 | ensuredSpec/ensuredGen 是累积并集非集合快照——BG 收口/L1 失败/进程缩减/回滚产生永久幽灵期望，缺席检测永久假阳性 | **接受**：缺席检测期望集改为 LatestSucceeded Revision 拍内重投影（缓存幽灵根治且不变量 1 合规）；ensuredSpec 保留供 spec 对照但钉死写点纪律 | §4.1/§4.4 |
| B2 | P1 | 两层复查用 ActiveByApp 但驱动权口径含 failed 待回滚——failed 窗竞态（重放×回滚交错→vacuously ready→gen 错位→BG 旧代锚漂移） | **接受**：复查口径改驱动权同源查询 | §4.1 裁决表「活跃部署中」行 |
| B3 | P1 | gen 偏离收敛无实现挂点（compareSpecs 让位只读 Watch 流） | **接受**：检测下沉 driftScan 独立签名 | §4.1 |
| B4 | P1 | 签名先落×失败/挂起不清→冷却永不可达、解冻后永久静默 | **接受**：签名生命周期绑定收敛结果 + 慢通道节拍兜底 | §4.3 |
| B5 | P1 | Cancel 遗留（N0 不自动拆除）被静默翻转为自动回滚 | **接受**：升待裁决三案 | §4.1 表 + §10.3 |
| B6 | P1 | teardown×缺席检测竞态（重建刚拆载体成无主孤儿） | **接受**：appMu 内复查 expect 成员资格（teardown 同锁清缓存即闭合） | §4.1 收敛动作 |
| B7 | P2 | 冻结判定走 ownerTeam 降级契约 = fail-open | **接受**：fail-closed（解析失败跳过本拍） | §4.2 |
| B8 | P2 | seq-only 判定的 From 锚受理时冻结，执行基线可能已前移→回退变更却免跑迁移 | **接受**：判定延迟到执行时重算 | §5.1 步 6 |
| B9 | P2 | 终态后滚动停摆无恢复面（resume 只在 Ensure 途经） | **接受**：裁决表补停摆行（5min 护栏） | §4.1 |
| B10 | P2 | 冷却计数器持久性未声明；rebuildBaselines 不受冷却管辖 | **接受**：声明内存态 + 等效兜底论证 + 启动重放共用记账 | §4.3 |
| B11 | P2 | rebuildBaselines 单 App 失败→期望缓存永空→自愈盲（注释与实现不符） | **接受**：「succeeded 无期望缓存」纳入收敛触发 | §4.4 |
| B12 | P3 | seq 单调约定无受理执法 | **接受文档化、驳回受理执法**（并发受理警告语义模糊）：ADR 显式声明调用方约定 | §5.1 步 1 |
| B-③ | — | 替代方案：收敛走 kind=reconcile Deployment 行（状态机继承） | **驳回**：收敛高频会产部署行垃圾；审计需求以 SourceSystem 审计行满足（采纳其意图） | §4.1/§4.3 |
| B-② | — | 替代方案：App 域挂 60s 节拍而非事件触发 | **部分采纳**：双通道（快事件 + 慢节拍兜底）——名实合一且消除死锁面 | §4.3 |

### C 路（产品面与工程账）

| # | 级 | 主张 | 裁决 | 落点 |
|---|---|---|---|---|
| C1 | P1 | restart_seq 存量迁移语义未裁决（seq=0 铸标签→升级首拍全量滚替，bb5259d WAL 事故前车）；e2e:upgrade 金丝雀可能合法变红 | **接受**：seq=0 不铸标签 + canonical 缺失归 0；测试加存量零滚用例；e2e 预记两分支 | §5.1 步 4 + Batch D/收尾 |
| C2 | P1 | controller-wins 与 runbook 在案手动干预（scale-0 收敛法/replicas-0 救援/docker rm -f）正面冲突；缺冻结优先逃生序盘点 | **接受**：runbook 手动面盘点独立收尾项 + 冻结=官方逃生通道 | §4.2 + 收尾批 |
| C3 | P1 | Batch B 注解面违反 §3.4「不造无人消费机制」；153 方法口径失实约 50% | **接受**：执法面改 Go 表 + ADR 附录；proto 注解推迟至配置面立项 | §3.1 重构 |
| C4 | P2 | converge_failed 冷却不可发现不可解除无告警 | **接受**：参数钉死 + doctor 行 + 内置告警 | §4.3 |
| C5 | P2 | 收敛写径审计面未裁决（SourceSystem 在位） | **接受**：落审计行 source=system | §4.1 |
| C6 | P2 | skills/ 能力面零批次（deploy-diagnose 决策树语义翻转） | **接受**：C/D 批同批更新 | Batch C/D |
| C7 | P2 | Batch D golden 清单漏 selfdescription/schema/explain | **接受** | Batch D |
| C8 | P2 | 期望账本寄居 observations 域命名混淆；观测播种风险；重启冷窗未成文 | **接受**：ADR 钉死写点纪律 + 冷窗成文；结构析出留实现裁量 | §4.4 |
| C9 | P2 | 双面无操作者决策规则，声明面摩擦高→死物风险；console Spec 编辑器现役存在（入口成立） | **接受**：升格待裁决为双面操作者契约 | §10.2 |
| C10 | P3 | SetUserPassword 与 Rotate 系界线未写 | **接受** | §3.2 |

### 审查方诚实声明的采信注记

- B 路时序推演（B1/B2/B4/B6）按设计文本 + 现役机制推演，未落码验证——已按结构性修法采纳（重投影/口径统一/生命周期绑定/锁内复查），实现时以钉测族承载。
- C 路自驳两条采信：「§3.1 零触碰 vs Batch D console:gen」不矛盾（MethodOptions 注解不进 swagger、ProcessSpec 字段进）；staging 现役应用栈可能已重置（记录·四十八挂账①），首收敛金丝雀实为 e2e:upgrade 三负载。
- A 路未验证项（openapi 行为、k3s 形态）随对应批次动工核。
