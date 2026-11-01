# ADR-0088：engine 卫星包终态与生长律

日期：2026-10-30 ｜ 状态：**已裁决（2026-10-31 定稿）——P4 已收官（2026-11-01，六波八卫星+守卫五件+熔断冻结）；P5 进行中（wave-1 路径裁定+ringregistry+logpipes 试点已落；wave-2 execs/metrics、wave-3 backups/browses、wave-4 databases/manageds 六卫星拆包已落，2026-11-01；末星 intake 待拆）**

## 背景

engine 单包 62 非测试文件、`*Engine` 方法 321（2026-10-30 快照：导出 80 + 未导出 241）、Engine 字段 64（8 capability + 11 Loop + 14 域状态 struct + 9 锁）。P3 域化批（2026-10-15）完成了状态归域（域 struct 进域文件、enginebackref 守卫前置封堵回引后门），行为归域（接收者迁移）未发生。方法级耦合审计（设计稿 §4）：星间 9 条（8 卫星终局口径）中 7 条为误置事实帮手、2 条真耦合；处置后星间硬边可归零——超级对象是**汇聚形态问题，不是网状纠缠问题**。

此前会话曾保守裁定「同包嵌入为终态、拆包暂缓」。前提变更（2026-10-30 维护者裁定：开发阶段、无向后兼容、硬理由可推翻）+ 规则先下沉（ADR-0087 P3a 先行缩小 engine 质量）消解了拆包的排序与迁移风险论据；长期论据站在拆包一侧：**守卫在模拟编译器免费提供的东西，并行 AI 会话在增多——守卫维护成本 O(会话数)，编译器执法 O(0)**。ADR-0075 决策 4 禁止的是 domain 内核导出（XxxKernel），application 环导出机制端口不冲突。

## 决策

1. **卫星包终态**：9 个卫星域终态独立成 application 包——intake/backups/logpipes/metrics 域/execs/manageds/databases/browses/**diagnosis**（清单与文件归属见设计稿 §3，2026-10-31 增补五文件；**命名与包名避用 telemetry**——CONTEXT.md Metrics 词条 `_Avoid_`，该词冻结给 domain/telemetry 与 genproto telemetry/v1 的事件面。diagnosis 为 P2 落地新晋：规则已在 `domain/services/diagnosis`，application 侧按本 ADR 过渡条款驻留包内——**生长律的必要性实证：新域 application 半边仍默认落 engine**）。engine 核终态=单写者机器（机制原语 L0/L1 + 派生编排层）。
2. **两步迁移**：P4 包内成形（接收者下域 + **嵌入提升**——核→卫星方向调用点零改；被迁方法体内 `e.xxx` 引用改写为卫星字段/端口调用）→ P5 拆包（卫星迁出、engine 核导出机制端口）。**P4→P5 窗口是 62 文件归属表的最后低成本改判窗**（P5 后改判需动 import 图+端口面+互引矩阵）。P5 前置判稳窗（可验收判据）：≥1 次 staging 换装 + ≥7 天 dogfood 零 engine 相关 journal 错误 + e2e 全绿；窗内发现 P4 缺陷修补后重计窗。
3. **机制端口导出面**：emitter/writer/maintenance/memo/facts——engine 核对外机制导出面，卫星依赖经端口注入，端口契约引用各原语守卫锚。卫星依赖形态**显式逐字段**（enginebackref R1/R2 哲学：禁回引 `*Engine`、禁方法参数夹带、显式最小依赖；`transact.Stores` 目录整体注入可）。设计稿 §4 的 6 条例外边（buildRootCtx/failDeployment/loadSpec/reconcileNodes/publishRoutes）P4 设计时逐条归位，端口表随之修订。
4. **锁归属**：`platformBackupMu`→backups、`rotateMu`→databases 随域走；`maintenanceMu`（12 文件消费）与 `ensureMu` 备忘族（11 文件消费）为跨域共享设施，真核持有、gate 注入，锁语义零漂移（纪律项，锁字段×持锁点清单对账承载）；拆锁另案（争用数据触发）。
5. **生长律（新域禁入 engine）**：新域一律走立域模板五条（设计稿 §7）；P5 前的过渡形态=engine 包内卫星 struct 驻留、P5 后平移。**执法形态=环表条目登记制**：`construct.go` 环表条目集与 lifecycle 独立 Run 注册点（driftLoop 现状）**双锚**冻结为白名单（每条带域锚/ADR 锚；新条目未登记即红、登记条目删除即红，双向保鲜）——「来自立域模板批」是 commit 语境不可树内判定，降级为登记制+评审可查锚。**环表与 lifecycle 注册点是 engine 仅有的两个合法生长点。**
6. **O4 审计门（genproto）：裁决 = 豁免+基线冻结（2026-11-01 P4.5）**。全量清点实测 22 非测试文件 / 23 import 点（engine 20 + composeintake + schema；含 preview.go 的 delivery/v1 枚举直引一条——原记 19 基线已漂移，以实测定基）。清点结论：全部消费为「IR 生成类型作内部数据结构」形态（持久化 protojson blob 锚 2 + 端口/struct 签名面 + proto.Clone 深拷贝直用 + 投影/构造器），零「边界转换」形态——wire 翻译层现状本就唯一居所 interfaces。驱逐立法被否决，三理由：①spec IR 持久化绑定（Revision/Task Spec 列 = protojson blob，驱逐=数据迁移）；②IR 管道 domain（shared/spec，domainpurity 豁免在案）↔application 双向流通，单禁 application 不自洽、连禁 domain 超出 O4 范围；③proto 唯一契约源+反双真源是既有仓级裁决（schema/specs.go、preview.go 注释在案）。**执法 = appgenprotofreeze 守卫**：application 全环 genproto import 点基线快照（23 条 golden）+ 超基线即红（扩张须评审裁定同 commit 刷新）+ 基线死条目即红（双向保鲜）+ 防盲三锚；红灯三腿实测。P5 卫星包若挪出 internal/application/，扫描面随 P5 首项守卫重构同批调整。

7. **执法面调整**：
   - 「星间零硬边」P4 为 AST 调用级守卫（三要素：62 文件归属表入守卫双向保鲜 + struct 归属映射 + 符号定义反查；白名单条目=**端口实现边**形态，预估 volumePinner/databasePassword 两条，终值 P4 wave-2 波首定）；P5 拆包后**降级为 import 级并继续执法**（非撤销——编译器不拦合法 import、dep_matrix 现仅执法跨子树方向、importguard 叶表无卫星包，三路均不接管），接管件=**application 环内互引矩阵**（dep_matrix 扩：卫星两两禁 import、engine 核单向、装配经 assembly）。
   - 「派生层禁直写元能力私有状态」守卫：**双表制**——元能力私有状态字段清单（observations/expects/备忘族字段枚举，含 memo 影子私造执法）+ 派生层文件表（62 文件逐一元/派生归属，以设计稿 §2.5/§3 合一为单源）——双表入守卫、双向保鲜；AssignStmt/IncDecStmt/delete 以元能力字段链为 LHS 即直写、方法调用即合法入口（taskobs 观测写点为白名单钩子，ensuredledger 判据核范式）。
   - **真核熔断线**：engine 核包全部 FuncDecl 计数（包级计数封死「挂已嵌入 struct 新方法」「新 struct 自立」两条绕道；接收者字面计数在嵌入改形后失效）超基线即红——**基线于 P4 收官冻结**（P5 只加不减）。
8. **分层豁免随迁**：engine→infrastructure 的 github/upload 客户端注入豁免（ADR-0075 登记项）**随卫星包路径平移、条数不增**（「维持原样」与拆包互锁：depExemptions subtree 不再命中即双向保鲜红）——卫星包路径裁定（engine/ 子包 vs 平级包）与豁免行处置、守卫扫描面（四个 root=engine 硬编码守卫：maintenanceguard/transitionguard/ensuredledger/loopguard 拆包后静默变哑的扫描面扩容与防盲阈值重定；enginebackref 防盲锚 namedStructs<60 拆包必红的调整；**卫星包回引 `*engine.Engine` 的跨包执法**）一并进 P5 首项。

## P5 首项注记（wave-1 路径裁定，2026-11-01）

**裁定一：卫星包路径 = 平级包 `internal/application/<sat>/`**（设计稿 §1 终态蓝图形态；logpipes 试点落地）。四项评估：

1. **dep_matrix 豁免行平移预案**：github/upload 两行豁免的真实消费者是 gitmaterial/buildsource（intake 域）——intake 拆包波把两行 subtree 自 `internal/application/engine` 改写为 `internal/application/intake`，**条数不增**（2 行改写、0 新增），engine 行不再命中即双向保鲜红、强制同批改写（守卫文化的既定工作方式）。engine 子包形态的「subtree 前缀自动匹配」是路径巧合收益，且把豁免钉在 engine 前缀上模糊了豁免的域归属（豁免属 intake 域，不属 engine 核）。
2. **守卫扫描面重定**：四件 root=engine 硬编码守卫（loopguard/maintenanceguard/transitionguard/ensuredledger）扩容为「engine 树 ∪ `engineSatellitePackages` 登记表」（enginebackref_test.go 在册；每拆一星同批追加、条目目录消失红）——显式登记制优于子包形态的递归 WalkDir 隐式覆盖（隐式覆盖把执法面耦合在路径前缀巧合上，重构即静默变哑；登记表把扫描面本身变成双向保鲜契约）。防盲阈值实测 wave-1 后全部成立（65 文件 ≥40、载体写点 ≥11 不变、函数 ≥150、账本写点 ≥16；enginebackref namedStructs 87→85，阈值 60 仍立，后续波次按实测重锚）。
3. **enginefuse 真核熔断线语义**：平级形态保「engine 包 FuncDecl 计数 = 真核」纯净——卫星合法增长不计入核熔断面（579 基线只加不减；wave-1 实测 579→571 = logpipes 8 方法迁出 + sleepCtx 下沉 −1、核门面 LoggingProvider +1，下降是允许方向）。engine 子包形态的递归 WalkDir 会把卫星增长计入核熔断（顶爆基线）或迫使 enginefuse 打「排除子目录」判据洞——两者皆劣。
4. **loopguard 骨架基名撞名**：骨架正向锚按基名 loop.go 匹配——子包形态下任何卫星包自有 loop.go（如未来 browses 域）与骨架锚互扰；平级形态下扩容面按「engine 包内 loop.go」目录精确判定（本波已实现），撞名面消除。
5. **装配与 apitest**：wire 提供集零改（engine.New 仍是唯一构造入口，卫星在 construct.go 内装配）；gRPC 消费面（assembly NewAPIDeps 的 `e.LoggingProvider()`）经核门面委托零改；apitest 经装配门面（harness → NewAPIDeps）零改。

**裁定二：共享契约叶子 = 平级新包 `internal/application/engineports`**（卫星与核都 import 它、无环；**按需最小面下沉**——wave-1 仅 SleepCtx 与 LogIngestBatchFrames；Check/WriteFact/WriteStep 写路径类型与 memo/观测端口签名随首个消费它们的卫星波次下沉，transition.go 届时改引下沉类型=别名零漂移）。否决 `engine/ports` 子包形态三理由：①engine 树保平铺单层——intersatellite/enginefuse/enginefuncregistry 的「engine 包」口径继续字面成立，无需引入子目录排除判据；②未来环内互引矩阵（决策 7 P5 项）立法简单——「卫星准 import {domain, engineports}、禁 import engine 根与他星」的前缀规则无需为叶子打例外洞；③application 环契约叶子有 transact 先例。

**裁定三：形态反转实现 = 核经构造注入持有卫星**（engine 持 `*logpipes.Logpipes` 指针字段，construct.go 装配卫星 New；嵌入解开）。否决「装配根组合」（engine 不 import 卫星、step 自 assembly 注入）：环表与 lifecycle 注册点是 engine 仅有的两个合法生长点（决策 5），step 注入会把生长点外移给 assembly，且 gRPC/apitest 消费面被迫改形——与「最小改面」相反。

**跨包执法（决策 8 兑现）**：enginebackref 新增跨包腿——已拆卫星包禁 import engine 根包（import 禁令是 Engine/*Engine 类型引用的机械全称：类型不可名即 R1/R2 各引用位全数不可达）；`engineSatellitePackages` 登记表双向保鲜。

**wave-1 落地（logpipes 试点）**：internal/application/logpipes 包（logging.go 逐字迁入；struct logpipes→Logpipes、环步 loggingStep→Step 导出更名、New/Deps 装配面）；observ.go sleepCtx 与 buildlog.go logIngestBatchFrames 经包内别名引 engineports 单源（调用点零漂移）；ringregistry 守卫上线（生长律双锚登记制，验收锚「生长律守卫」）；intersatellite 删 logpipes 条目（子系统清单一改表驱动）；enginefuncregistry golden 同 commit 刷新；wire/golden 零漂移。范式通则与 logpipes 特例分账见 wave 报告（后续七星拆包模板）。

## 后果

- engine 核保留 ~179 方法（快照口径；单写者机器的诚实体量）；卫星 ~142 方法随域独立演进；新动词默认落点从 Engine 改为卫星/新域。
- 外部消费面（68 导出方法被 engine 外非测试消费）经嵌入提升零变化；消费面窄接口随各卫星批顺路落 interfaces 侧。
- B1 验票/烧票原语按 L0 邻位入册时修订设计稿 §2 分层表（升格协议）。
- 架构文档 §2 分层树「engine(壳+九域内核)」表述随 P5 同批修订（本 ADR 生效时列入 P5 验收锚）。
- ADR-0075 例外②③与冻结面（loop 唯一环骨架/transition 唯写序列/ensureMemo/capability 6 方法窄面）不受本 ADR 影响；**例外**：transition.go:420 引 build 日志属冻结面持有的卫星边，P5 裁决表须给该边唯一豁免或函数随卫星走并同批修订冻结面注记。

## 验收锚

- [x] P4 wave-0 ✅（2026-11-01）：resolve.go 解析面成形（六函数逐字迁入，星间边 9→2——误置 7 边归零，独立复测证实；volumePinner 唯一白名单坐实）；resolutionface 归属锚守卫（定义位置钉+防盲 fatal+四腿 shapes+真实树破坏红灯验证）；maintenanceguard 判据泛化（类型认定两遍 AST+接收者×字段绑定+锁入口方法名集+卫星形 fixture 三腿锚+旧字面判据致盲位实证）。
- [x] P4 ✅（2026-11-01，六波 8 commit）：8 域 134 方法接收者归位+嵌入改形（通则六条范式链）；星间零硬边 AST 守卫上线（三要素；**实现边白名单终值=空表**——预估 volumePinner/databasePassword 两条的终形均为端口/组合根绑定，本锚原文预估由 wave-2b 终值裁定修正）；派生层禁直写双表守卫上线（memo 协议由注释裁决升格守卫）；守卫上线咬出方法级审计盲区（11 条函数级边/6 帮手）并已 wave-3b 全数清偿（豁免 7→0）；enginebackref/ensuredledger/transitionRepoFields 随注入形态核账完成；wire/golden 全程零漂移。
- [x] P4 收官 ✅：真核熔断基线冻结=579（enginefuse 守卫承载，2026-11-01/d4ad5f71 时点）；归属表复核——intersatellite 守卫 67 文件归属表为执法真源（真核 44+卫星 23），最后改判窗结论：task 留真核（双向耦合+zeabur 热区已过但耦合仍在）、diagnosis 应用侧半步驻核（P5 前专议）、intake 嵌套形态（intake+build builds）保冻结边。
- [x] P5 首项 ✅（2026-11-01 wave-1）：路径裁定=平级包 + engineports 契约叶子 + 核构造注入持有（裁定三项与四项评估见「P5 首项注记」节）；四 root=engine 件经 engineSatellitePackages 登记表扩容；enginebackref 跨包腿上线；防盲阈值 wave-1 实测全部成立（后续波次按实测重锚）；豁免行平移预案在册（github/upload 两行随 intake 波改写 subtree、条数不增）。
- [ ] P5：核→卫星 22 条文件级边逐条裁决表（环表 step 经 assembly 注入/反向端口/函数搬迁三选一；transition.go:420 边显式处置）；环内互引矩阵立法；apitest golden 零漂移（harness 消费代码按装配门面改写为预期工作量）；星间守卫 AST→import 级降级记录（豁免表清空）；架构文档 §2 分层树表述同批修订。（wave-1 logpipes 试点定形范式；wave-2 execs/metrics 按六通则落地——engineports 增 EmitLedger 签名与 WorkloadID/AuditDetailJSON/ContainerExitFact/DiagnoseWindow 事实帮手，观测窄接口 ownerOf/ownerTeam 基础类型化为 appOwnerOf，metastatewrite 补位为第五件扩容守卫；wave-3 backups/browses 落地——写路径类型 Check/WriteStep/WriteFact 与 CommitWrite writer 端口、Memo 备忘形态与 ensureFresh/Remember/Forget 端口签名随首消费者下沉 engineports（transition.go/ensure.go 别名零漂移），铸名/指纹/尾部纯帮手同沉（DatabaseDNSName/DBCredentialSecretName/ManagedFingerprint/MaterialsFingerprint/StderrTail/TrimTrailing，resolve.go 保解析面锚），观测窄接口 recordOwners/recordExpected 语义窄化为会话轴（workloadOwner 全域下沉裁定：否——域判别留核绑定闭包），memo 表本体组合根铸造注入（metastatewrite D3 纪律）；enginefuse 518→465、enginefuncregistry 基线 519→465、enginebackref namedStructs 64→53 同批重锚；wave-4 databases/manageds 落地——workloadOwner 全域下沉重裁：维持否（两星消费面各为单域轴：databases 记账恒 databaseID、manageds 归属恒 w.Process，无跨域判别需求；按 browses 会话轴先例窄化为 database 轴/system 轴签名，databaseOwner/systemOwner 铸造留核绑定闭包，engineports.Owner 升格不发生），Database 投影前段 ProjectDatabase+探针节拍常量随域迁入卫星（唯一消费者；egress 过滤公式 EgressSubset 下沉 engineports 与核投影面同源），gensFor 留核裁定维持（ensureMu 锁与 gens map 配对——表本体 managedGens 随核、ManagedGen 类型随卫星包），memo 表组合根铸造注入（metastatewrite D3），maintenanceguard 豁免 Caller 升格 文件:函数 双键（五星环步统一命名 Step）；enginefuse 465→440、enginefuncregistry 基线 465→440、enginebackref namedStructs 74→69（wave-3 注记 53 与 AST 实测不符，勘误为 74）、appgenproto 基线随文件路径改道同 commit 刷新（消费面零增）同批重锚；末星〔intake〕待拆。）
- [x] 生长律守卫 ✅（2026-11-01 P5 wave-1）：ringregistry 上线——环表条目集（name/loop/step 三元组+表序）与 lifecycle 注册点双锚（受管挂载册+bare 挂载册）登记制，双向保鲜+防盲（条目数/挂载数下限）+合成树 shapes 七腿+真实树红灯实验两腿（新环未登记红/登记环消失红）；新域过渡条款（包内卫星 struct 驻留）不误伤（在册七星仍驻 engine 包、enginebackref R1/R2 原判据执法）。
- [x] O4 ✅（2026-11-01）：清点 22 文件/23 点（实测修正原 19 基线漂移+delivery/v1 遗漏）→ 裁决 B（豁免+基线冻结），appgenprotofreeze 守卫上线（超基线红/双向保鲜红/防盲三锚，红灯三腿实测），决策 6 已修订。
