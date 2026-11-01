# ADR-0088：engine 卫星包终态与生长律

日期：2026-10-30 ｜ 状态：**已裁决（2026-10-31 定稿）**（实施锚=P4 卫星化批 + P5 拆包批，均未开工；四侧交叉评审+并行批复核已回填）

## 背景

engine 单包 62 非测试文件、`*Engine` 方法 321（2026-10-30 快照：导出 80 + 未导出 241）、Engine 字段 64（8 capability + 11 Loop + 14 域状态 struct + 9 锁）。P3 域化批（2026-10-15）完成了状态归域（域 struct 进域文件、enginebackref 守卫前置封堵回引后门），行为归域（接收者迁移）未发生。方法级耦合审计（设计稿 §4）：星间 9 条（8 卫星终局口径）中 7 条为误置事实帮手、2 条真耦合；处置后星间硬边可归零——超级对象是**汇聚形态问题，不是网状纠缠问题**。

此前会话曾保守裁定「同包嵌入为终态、拆包暂缓」。前提变更（2026-10-30 维护者裁定：开发阶段、无向后兼容、硬理由可推翻）+ 规则先下沉（ADR-0087 P3a 先行缩小 engine 质量）消解了拆包的排序与迁移风险论据；长期论据站在拆包一侧：**守卫在模拟编译器免费提供的东西，并行 AI 会话在增多——守卫维护成本 O(会话数)，编译器执法 O(0)**。ADR-0075 决策 4 禁止的是 domain 内核导出（XxxKernel），application 环导出机制端口不冲突。

## 决策

1. **卫星包终态**：9 个卫星域终态独立成 application 包——intake/backups/logpipes/metrics 域/execs/manageds/databases/browses/**diagnosis**（清单与文件归属见设计稿 §3，2026-10-31 增补五文件；**命名与包名避用 telemetry**——CONTEXT.md Metrics 词条 `_Avoid_`，该词冻结给 domain/telemetry 与 genproto telemetry/v1 的事件面。diagnosis 为 P2 落地新晋：规则已在 `domain/services/diagnosis`，application 侧按本 ADR 过渡条款驻留包内——**生长律的必要性实证：新域 application 半边仍默认落 engine**）。engine 核终态=单写者机器（机制原语 L0/L1 + 派生编排层）。
2. **两步迁移**：P4 包内成形（接收者下域 + **嵌入提升**——核→卫星方向调用点零改；被迁方法体内 `e.xxx` 引用改写为卫星字段/端口调用）→ P5 拆包（卫星迁出、engine 核导出机制端口）。**P4→P5 窗口是 62 文件归属表的最后低成本改判窗**（P5 后改判需动 import 图+端口面+互引矩阵）。P5 前置判稳窗（可验收判据）：≥1 次 staging 换装 + ≥7 天 dogfood 零 engine 相关 journal 错误 + e2e 全绿；窗内发现 P4 缺陷修补后重计窗。
3. **机制端口导出面**：emitter/writer/maintenance/memo/facts——engine 核对外机制导出面，卫星依赖经端口注入，端口契约引用各原语守卫锚。卫星依赖形态**显式逐字段**（enginebackref R1/R2 哲学：禁回引 `*Engine`、禁方法参数夹带、显式最小依赖；`transact.Stores` 目录整体注入可）。设计稿 §4 的 6 条例外边（buildRootCtx/failDeployment/loadSpec/reconcileNodes/publishRoutes）P4 设计时逐条归位，端口表随之修订。
4. **锁归属**：`platformBackupMu`→backups、`rotateMu`→databases 随域走；`maintenanceMu`（12 文件消费）与 `ensureMu` 备忘族（11 文件消费）为跨域共享设施，真核持有、gate 注入，锁语义零漂移（纪律项，锁字段×持锁点清单对账承载）；拆锁另案（争用数据触发）。
5. **生长律（新域禁入 engine）**：新域一律走立域模板五条（设计稿 §7）；P5 前的过渡形态=engine 包内卫星 struct 驻留、P5 后平移。**执法形态=环表条目登记制**：`construct.go` 环表条目集与 lifecycle 独立 Run 注册点（driftLoop 现状）**双锚**冻结为白名单（每条带域锚/ADR 锚；新条目未登记即红、登记条目删除即红，双向保鲜）——「来自立域模板批」是 commit 语境不可树内判定，降级为登记制+评审可查锚。**环表与 lifecycle 注册点是 engine 仅有的两个合法生长点。**
6. **O4 审计门（genproto）**：application 全境清点（**已知基线 19 个非测试文件 import genproto spec v1**：engine 17 + composeintake + schema）；结论二选一——驱逐立法（proto↔IR 翻译唯一居所=interfaces 环；**红线②（genproto）扩展至 application**——非「三红线扩全内环」，红线①（无 I/O）对编排环不适用）或豁免登记+基线冻结（import 点快照+超基线即红）。**已知约束**：spec IR 原始类型物理住 genproto/spec/v1，domain 环自身经登记豁免引用（domainpurity 在案）——驱逐路须同批设计 IR 类型的 domain 再导出面或等价豁免，净收益按清点实测定。
7. **执法面调整**：
   - 「星间零硬边」P4 为 AST 调用级守卫（三要素：62 文件归属表入守卫双向保鲜 + struct 归属映射 + 符号定义反查；白名单条目=**端口实现边**形态，预估 volumePinner/databasePassword 两条，终值 P4 wave-2 波首定）；P5 拆包后**降级为 import 级并继续执法**（非撤销——编译器不拦合法 import、dep_matrix 现仅执法跨子树方向、importguard 叶表无卫星包，三路均不接管），接管件=**application 环内互引矩阵**（dep_matrix 扩：卫星两两禁 import、engine 核单向、装配经 assembly）。
   - 「派生层禁直写元能力私有状态」守卫：**双表制**——元能力私有状态字段清单（observations/expects/备忘族字段枚举，含 memo 影子私造执法）+ 派生层文件表（62 文件逐一元/派生归属，以设计稿 §2.5/§3 合一为单源）——双表入守卫、双向保鲜；AssignStmt/IncDecStmt/delete 以元能力字段链为 LHS 即直写、方法调用即合法入口（taskobs 观测写点为白名单钩子，ensuredledger 判据核范式）。
   - **真核熔断线**：engine 核包全部 FuncDecl 计数（包级计数封死「挂已嵌入 struct 新方法」「新 struct 自立」两条绕道；接收者字面计数在嵌入改形后失效）超基线即红——**基线于 P4 收官冻结**（P5 只加不减）。
8. **分层豁免随迁**：engine→infrastructure 的 github/upload 客户端注入豁免（ADR-0075 登记项）**随卫星包路径平移、条数不增**（「维持原样」与拆包互锁：depExemptions subtree 不再命中即双向保鲜红）——卫星包路径裁定（engine/ 子包 vs 平级包）与豁免行处置、守卫扫描面（四个 root=engine 硬编码守卫：maintenanceguard/transitionguard/ensuredledger/loopguard 拆包后静默变哑的扫描面扩容与防盲阈值重定；enginebackref 防盲锚 namedStructs<60 拆包必红的调整；**卫星包回引 `*engine.Engine` 的跨包执法**）一并进 P5 首项。

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
- [ ] P5 首项：卫星包路径裁定 + 豁免行平移 + 守卫扫描面整节重构（四 root=engine 件扩容/防盲阈值重定/enginebackref 跨包执法）。
- [ ] P5：核→卫星 22 条文件级边逐条裁决表（环表 step 经 assembly 注入/反向端口/函数搬迁三选一；transition.go:420 边显式处置）；环内互引矩阵立法；apitest golden 零漂移（harness 消费代码按装配门面改写为预期工作量）；星间守卫 AST→import 级降级记录（豁免表清空）；架构文档 §2 分层树表述同批修订。
- [ ] 生长律守卫：环表条目登记制 + lifecycle 注册点双锚上线（driftLoop 现状收编或双锚覆盖）；新域过渡条款（包内卫星 struct 驻留）执法不误伤。
- [ ] O4：19 文件基线复核 → 驱逐立法（红线②扩展）或豁免+基线冻结，结论落本 ADR 修订，不留悬案。
