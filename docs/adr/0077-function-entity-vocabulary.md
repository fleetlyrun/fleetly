# ADR-0077: Function 一等实体入册与 ADR-0007 词汇修订批（Task 让渡 function、Spec 第四种、守卫同步）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-16 | ADR-0007（词汇冻结——显式修订通道）、ADR-0029（受管聚合与凭证链先例）、ADR-0064（内容寻址 Ensure 语义族）、ADR-0070（可读实体 id）、ADR-0075（词条入册同批守卫同步先例）、docs/design/2026-10-16-functions-api.md（设计真源）、docs/proposals/2026-10-16-function-hosting.md（被取代的 torchwood 视角原提案，其裁决框架由设计文档头部声明取代） |

## 背景

torchwood Functions 托管提案（2026-10-16）最初按「fleetly 不立实体、纯通用面承载」提出（其 §8.6 自答「无需立实体、不推荐」）。同日经对抗性评审与三轮用户裁定，方向反转：

1. **Function 是 fleetly 一等公民产品线**（与 App/Database/Task 并列），独立设计、独立演化，不绑定 torchwood、不依赖 torchwood 代码；
2. **从通用 Functions API 出发**：不假设消费者协议/SDK/触发方；数据面纯 HTTP，调用语义归函数、平台语义归 fleetly；torchwood 只是第一个 dogfood 消费者；
3. **exposure 双态、private 缺省**（public 才合成受管 Route）。

设计定稿于 `docs/design/2026-10-16-functions-api.md`（边界三轴 + Database 受管模式 + Phase 0-3 分期，§15 留六问终裁）。

词汇面与现行冻结冲突两处：①Task 词条 _Avoid_ 含 `function`（2026-10-01 词汇冻结裁决把该词挡在 Task 门外——当时的产品判断是 FaaS 明确不做，该判断已被上述裁定推翻）；②wording 守卫分诊表 skippedTokens["function"] 理由文本「FaaS 义在明确不做清单」随之失效。本 ADR 走 ADR-0007 的显式修订通道（更名即设计缺陷，词条不合适走显式 ADR，不接受局部混用），是 Functions 产品线 Phase 0 先行件——实施批（Phase 1）动 proto/engine 前必须先解除 Task 词条对该词的占用。

## 决策

1. **Task 词条让渡 function**：_Avoid_ 表删 `function`——该词不再是「Task 的同义绕道」，而是另一实体的名字。Task 语义不变（程序化工作负载双形态，one-shot/resident）；Function 与 Task 的用户判据边界 = 「要不要 URL 与请求语义」（设计文档 §2.1 三轴：激活模型/计量轴/粒度）。

2. **Function 词条入册 CONTEXT.md**（插于 Variable 与 Task 之间，与 App/Task 并列的第三执行产品）：

   - 定义：请求驱动、可缩零的托管执行单元；单代码入口（一个 handler 进程形态）。走受管模式（Database 先例：FunctionSpec 为第四种 Spec，engine 直接投影 Workload，不产生 App 行）；平台语义 = URL/伸缩/唤醒/观测，exposure 双态 = private（缺省，仅网络成员资格可达）| public（合成受管 Route）；调用语义（鉴权/载荷/幂等）归函数自己（ADR-0077）。
   - _Avoid_: lambda
   - 双名共存条款：第三方产品同名实体（如 torchwood Functions）按 Language 节第三方本义豁免引用；域内指称 fleetly 实体时一律用 Function。

3. **Spec 词条**：三种 → 四种（AppSpec、TaskSpec、DatabaseSpec、FunctionSpec）。

4. **守卫同步（同批执法，ADR-0001→irguard 范式）**：
   - skippedTokens **删** `function` 条目——该词升格为冻结实体名后不再是需要人工评审的语境词；双向保鲜要求死条目清走。机械面：`function` 全然不扫，与其余冻结实体名（app/task/run…）同待遇。
   - bannedPatterns **增** `lambda`（词边界直扫）——AWS 专名同义绕开冻结命名，机械无歧义；入扫面（.go/.proto/skills md/install.sh/console src）全量零命中实证（2026-10-16 grep），无需豁免条目。

5. **执法方向翻转**：Function 入册后，词汇违例方向 = 把 fleetly Function 叫成别的词（lambda 等即红）；`function` 一词本身在全仓任何面合法。wordingExemptions 零新增。

6. **真源分工**：本 ADR 只裁词汇与方向锚（一等实体/受管模式/exposure 双态 private 缺省/让渡边界）；字段面、Revision/Deployment 表归属、分期与实施验收锚的真源 = 设计文档及其 §15 终裁后的 Phase 1 实施 ADR——本批不重复立法。

## 理由

- **词汇先行是既定次序**（设计文档 Phase 0）：先解除占用再动代码，否则新实体每一行代码都在撞冻结表。
- **让渡而非并存**：`function` 同时坐在 Task 的 Avoid 与新实体名上会造成「同词两义」的评审混沌——Task 失去该词、Function 独占，边界由词条定义承载。
- **入册前提 = deletion test 通过**（设计文档 §2.1）：三轴组合（请求驱动激活 + per-invocation 观测轴 + 单代码入口）是 App/Task 面没有也无法便宜补上的语义，实体撤案不成立；每期复盘重跑。
- **对 2026-10-01 裁决的推翻是显式的**：Task 词条正文保留原裁决留痕句，本 ADR 背景节记录推翻理由（产品判断从「不做」反转为「一等产品线」），无隐性混用。

## 后果

- CONTEXT.md 三处修订（Task Avoid、Function 词条、Spec 四种）+ 守卫两处改动与本 ADR 同 commit。
- Phase 1（Functions MVP：聚合/FunctionSpec/Revision+Replay/engine 投影/exposure 双态/CLI+Console/e2e）解除词汇障碍可直开；其实施 ADR 携设计文档 §15 六问终裁。
- 词法守卫的「FaaS 明确不做」时代结束：域内讨论函数托管不再绕词；把 Function 叫 lambda 即红。
- 双名共存的评审口径：torchwood Functions（第三方产品）与 fleetly Function（域内实体）在散文中各自按本义出现；混指（用 torchwood 语境指 fleetly 实体或反之）属评审把关面，无机械执法。

## 验收锚

- [x] CONTEXT.md：Task 词条 Avoid 删 function、Function 词条入册（定义 + lambda Avoid + 双名共存句）、Spec 词条四种——与本 ADR 同 commit
- [x] wording 守卫：skippedTokens 移除 `function` + bannedPatterns 增 `lambda`（带理由注释）；TestAvoidTokensTriaged 与 TestNoBannedWording 双向保鲜绿〔2026-10-16 `go test ./internal/guards/ -run 'TestAvoidTokensTriaged|TestNoBannedWording' -count=1` 本地实证〕
- [x] `lambda` 入扫面全量零命中实证（grep .go/.proto/skills md/install.sh/console src，2026-10-16）
- [ ] Phase 1 实施 ADR 开工时复核词条与设计文档一致性（词条 = 词汇面真源，设计文档 = 形态真源；不一致以新 ADR 显式收敛）
