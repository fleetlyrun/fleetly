# ADR-0087：裁决规则归域，写原语单源

日期：2026-10-30 ｜ 状态：**已裁决（2026-10-31 定稿）**（配套提案 `docs/proposals/2026-10-30-engine-decomposition.md`、设计稿 `docs/design/2026-10-30-engine-terminal-architecture.md`；四侧交叉评审+并行批复核已回填；P2 试点已由 zeabur B2a 承载 ✅；实施锚=P3a/P3b）

## 背景

ADR-0075 例外①登记「贫血聚合+状态机集中（writes kernel/engine 单写者收敛）」。现状定量：domain 全环 9,275 行，核心聚合（deployment 153/task 111/run 102/database 98/backup 97/build 71，合计 ~630 行）行为仅 `Terminal()/Active()` 级判定——部署收敛主产品线的裁决知识（状态机迁移规则、观测裁决表、漂移签名、收敛策略、保留策略、告警评估）全部住在 application/engine，与 I/O 编排同文件同接收者。后果：①规则测试拖全套 fake 编排（converge_test 1,398 行对 converge.go 974 行、task_test 960 对 598）；②架构文档 §6 写路径段登记的「序列与规则双真源」（api 侧 acceptance.go 受理位原语 ADR-0024 vs engine 侧 transition.go 驱动域原语）是登记在册的漂移面，2026-10-03 五份手搓拷贝收口已实际咬过一次；③zeabur 程序 B2a 已裁「诊断规则 domain 纯函数」——不修例外①，仓内将并存两套约定（旧域贫血、新域有行为）。

仓内已有三个成功先例证明规则可以且应该在 domain：spec IR（shared/spec 1,391 行）、dbtemplate（819）、dbbrowser（401）。

前提（2026-10-30 维护者裁定）：项目处开发阶段、staging 可重建、**无向后兼容约束**；既有约定有足够硬的理由可推翻。

## 决策

1. **例外①收窄**：ADR-0075 例外①由「贫血聚合+状态机集中」修订为「**写执行集中**」——CAS+四件一拍（状态 CAS+tombstone+Outbox 事件+审计同事务）、单写者序、锁纪律、环骨架**留 application/engine 不动**（transition.go 冻结面零漂移）；**裁决规则下沉 domain**。一句话：domain 学会裁决，application 保留执刑。
2. **规则下沉判据三条**（满足前两条才考虑，第三条加分）：纯度（零 I/O 引用；时间作参数传入——「零 clock 依赖」由 domainpurity 扩 `time.Now` 调用禁列执法或明记纪律项）；词汇性（表达平台裁决规则，进 CONTEXT.md 词汇域）；多消费方或独立演进。单消费者私有步骤不下沉（防仪式主义）。
3. **状态机搬家禁令**：迁移表（from→to 合法性、终态判定、裁决对账表）下沉；执行机制（CAS/Outbox/单写者序/锁）留 engine。两平面论据：行写（transit）与载体写（Ensure 族）分离、converge 纯决策零行写已是代码事实。
4. **双真源收敛**：规则单源 domain 后，api 侧受理位（acceptance.go）与 engine 侧驱动位消费**同一份域规则**；受理位写路径改为消费 engine 写原语而非自带一份 commit 实现——「执行双点、规则单源、原语单份」。本项是 ADR-0024 决策 4（「统一写原语……engine transit 与 api 写路径共用」）的兑现而非翻案。**进展注（2026-10-31）**：B1 三段单源抽取（preview.go `composeSpec` 等）已先行落地**合成腿**的单源（预览与受理共用同一合成函数）——本决策剩余面收窄为**写原语腿**（acceptance commit→engine 原语），且有落地范式可循。**事务作用域约束**：engine 写原语须暴露事务作用域入口，受理检查保持全部同事务（ADR-0024 决策 3 语义不回退——否则重开 PutConfig TOCTOU 旧洞）；同批改针 acceptanceguard（形态锚 `s.Tx.Do(` 与 `commit` 调用判据随新形态重铸）并修订 transitionguard 对 acceptance.go 的原语本体豁免条款。
5. **层间迁移协议**：能力在元/派生层间迁移走显式 ADR 修订设计稿 §2 分层表；禁止派生层私造影子原语。
6. **旧裁决连带**：带「单源在 X」声明的单元（如 projection.go 探针回退链，架构评审第二轮候选 7）下沉须同批修订该裁决记录。
7. **下沉单元清单**（P3a 批执行，见提案 §4）：Run 观测裁决表→domain/automation/run（切分契约：裁决表下沉、eventcode 发射留 engine 归 emitter 端口）；漂移签名+stopped 去抖→domain/delivery；converge 姿态/冷却/失败阈值→domain 收敛策略面；告警评估状态机→domain/telemetry（P2 主形态随 B2a 窗口，退路批 P2' 独立承载）；备份保留→domain/data/backup；Schedule 重叠→domain/automation/schedule；蓝绿判据→domain/delivery。
8. **形态（2026-10-31 按 B2a 先例分判据）**：下沉 destination 二分——**单聚合状态机迁移表/终态判定** → 聚合域包（`domain/<agg>`，复数普通名词 struct + 白盒测试，ADR-0075 决策 4 形态）；**跨源裁决策略/规则族** → `domain/services/<name>/` **Rule 注册表**形态（`var ruleX = Rule{ID, Check}` 表驱动 + fixtures，与 errcode/eventcode/ConfirmableVerbs 同文化——B2a `domain/services/diagnosis` 为落地范式）。P3a 各单元按此分派（**判据优先**——本注 2026-10-31 随首单元修正：单聚合迁移表一律聚合域包，旧注「告警评估→注册表」与判据矛盾，作废）：告警评估→聚合域包 domain/telemetry（已落，P3a 首单元）；Run 裁决表→聚合域包 domain/automation/run；蓝绿判据（实测勘误 2026-11-01：判定核散居 drive/observ/observdomain，非 bluegreen.go——其本体为物化编排）/漂移签名/converge 策略/备份保留/schedule 重叠/终态计数派生→批内逐单元按单/跨聚合定。domain 三红线（无 I/O/无 genproto/无 RPC 框架）即下沉验收门（**含既有登记豁免形态核对**——domainpurity 对 shared/spec 引 genproto spec/v1 有登记豁免，下沉包按同机制核）。

## 后果

- domain 承载裁决知识后可白盒测试，engine 测试质量回落到编排本位；双真源漂移面消失。
- ADR-0075 例外①与架构文档 §6 写路径段「双真源」表述需**本 ADR 定稿 commit 时同批修订**（验收锚追踪）。
- 新域一律按「规则 domain 纯函数 + facts application 装配」落地（B2a 试点即此形态的标准化）。
- 词汇面：「机制原语」「卫星域」词条随 P0 批入 CONTEXT.md（ADR-0085 程序同款）。
- 风险：下沉过程中规则签名变化的调用点改写面较大——P3a 按单元小步、每单元一 commit 带 golden/白盒测试迁移。

## 验收锚

- [x] P0/定稿：ADR-0075 例外①修订 + 架构文档 §6 双真源段修订随本 ADR 同批落。
- [x] P2/P2' 试点：诊断域（主形态）或告警评估（退路批）在 domain 落地；纯函数测试拆两项措辞——import 可扫项（测试零 fake/编排依赖 import）+ 白盒纪律项。**进展注（2026-10-31）**：诊断域半边已由 zeabur B2a 先行落地（`domain/services/diagnosis` 六规则+unexplained，ADR-0090）——本锚诊断部分视为已满足先例；**告警评估未随批**（transitionRule 仍在 engine），为 P3a 首单元。
- [x] P3a：「engine 函数基线登记簿」守卫上线（enginefuncregistry：基线 594 函数 golden+零 I/O 直接引用判据+登记/豁免双向保鲜+三函数分辨力锚；已知局限=间接 I/O 不判/clock 故意不在载体表/非 Engine 形参不可见，头注声明）；domainpurity 扩 `time.Now` 调用禁列（AST 调用级，实测零命中零豁免立法，合成锚+mutation 验证）。
- [x] P3a：**八单元下沉完成（2026-11-01，含 P2 遗留告警评估与终态计数两扩容单元）**——alertrule.Evaluate/run.Adjudicate/drift.Adjudicate*/converge 策略包/backup retention/schedule adjudicate/bluegreen adjudicate/diagnosis terminalcounts；domain 三红线（含豁免形态）对下沉包全绿；engine 调用点全部经域 API 消费（装配→裁决→执行三段）；基线 594→575（净 −19 函数）。
- [x] P3b：**完成（2026-11-01）**——`Engine.Commit` 导出原语住 transition.go（配对单点不违冻结），acceptance 薄委托+类型统一（writeFact 单份）；同事务钉测三断言（同事务可见性/回滚连带/零包装上抛）+变异红灯验证；acceptanceguard 形态锚改 `s.Engine.Commit(`+反扫撤销豁免+红灯双向验证；transitionguard 本体注记单点化；writeclassguard 159 台账零变化；golden 双形态零漂移；ADR-0024 追记修订注。
- [ ] 「单源在 engine」旧裁决修订（探针回退链下沉时）或该单元豁免登记带理由。
