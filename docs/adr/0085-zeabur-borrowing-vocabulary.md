# ADR-0085: zeabur 借鉴程序 D0 词汇批——Change Preview / Confirm Ticket / Diagnosis / Finding / Fault Class / Platform Incident 入册

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-30 | 裁决真源 `docs/proposals/2026-10-30-zeabur-oss-borrowing.md` §8.1（13 项全量通过，D0 全套按候选名）；设计稿 `docs/design/2026-10-30-zeabur-oss-borrowing.md` §1.1-1.2（Change Preview/Confirm Ticket 语义）与 §2.1-2.3（Diagnosis/Finding/Fault Class/Platform Incident 语义与边界五分界）；ADR-0007（词汇冻结——新词入册走 ADR，更名即设计缺陷）；ADR-0081（X-Class 族真源：Write Class/Author Class）；ADR-0049/ADR-0051（Exec Session/Browse Session「非资源行」句式先例 + 冻结豁免面）；ADR-0017 附录 A.3（冻结豁免面守卫）；ADR-0041（Alert Rule——Finding/Incident 分立边界）；ADR-0022（Drift——Incident 分立边界）；ADR-0071 决策 5（预览载荷脱敏红线同款）；ADR-0039（SystemService 公开诊断面与 PlatformService 分立） |

## 背景

zeabur-oss 借鉴程序第 1 批（D0 词汇批，零 API 变更）：B1（变更确认闭环）与 B2（诊断引擎）两个机制批开工前，六个新词先入册。词汇先行是机制批的地基——B1/B2 的 proto 注释、CLI 文案、skills 散文、console 文案都将引用这六个词，先冻结命名防批间漂移（ADR-0007：更名即设计缺陷，新词入册必须走显式 ADR）。

裁决（提案 §8.1）：**全套按候选名入册**（Change Preview / Confirm Ticket〔非资源行〕/ Diagnosis / Finding〔非实体〕/ Fault Class / Platform Incident〔非实体〕）；Diagnosis 四义划界随词条入册；freezeguard 在册称谓 "diagnostics face"（第四义）随批改称「排障面」；zeabur 引述词 per-site 豁免政策同步。

## 决策

### 1. 六词条入册与落位（CONTEXT.md）

- **Change Preview 与 Confirm Ticket** 进「交付」节，紧随 Deployment 词条——预览与票是部署动词的确认半边（拆除预览同一对词覆盖）。
- **Fault Class** 进「写类与作者」节（Write Class/Author Class 旁，注明 X-Class 族）。
- **Diagnosis / Finding / Platform Incident** 新增小节「**诊断与排障**」，置于「写类与作者」与「状态语义」之间。
- 每词条一行内中文定义 + `_Avoid_`（分诊见决策 7）。

### 2. 非资源行/非实体声明

- **Confirm Ticket**：「**非资源行（不可列表回读，台账=审计）**」——仿 Exec Session/Browse Session 词条句式（ADR-0049/0051）。票据是进程内票据店的短命数据（第四 purpose，Launcher Ticket 先例）：重启即失、不可枚举，兑换成功落 LedgerOnly 审计行是唯一事后可证落点——「确认的正是展示的」由此可查证。
- **Finding / Diagnosis / Platform Incident**：「**读时计算、非实体、不落行**」——三者都是 B2 读时解释层的派生视图，事实源是观测事件快照/事件近窗/告警行/组件健康等既有与新增导出面，无自有行存储、重算即得；与 Alert Rule（规则行落库、阈值趋势义）的分界由此显式化。

### 3. Diagnosis 四义划界（边界句随词条入册）

「诊断」一词在仓内已有四个既有面，Diagnosis 词条只认 B2 诊断引擎义，四义边界句入词条：

1. **SystemService 公开诊断面**（GetVersion/GetStatus——控制面构建信息与服务状态，anonymous 公开；ADR-0039 与 PlatformService「全凭证平台操作面」分立）＝**平台自检**；
2. **`fleetly doctor`**（F0.4 本机自检：Docker 版本/daemon 可达/swarm 态/安装预检）＝平台自检的本机翼；
3. **Explain RPC**（资源自描述，`fleetly explain` 消费；GetSchema 是其全量同族面）；
4. **排障面**（exec/browse 冻结豁免义——原 freezeguard 在册称谓 "diagnostics face"，随本批改称，见决策 5）。

Diagnosis 词条（B2 读时解释层，DiagnoseApp 答「为什么现在坏」）与上述四义互不混用：新文案以「diagnosis/诊断」统称五个面即设计缺陷，各面用各自在册名词。部署失败解释不进 Diagnosis（deployment error 与 build log 既有面承载——首批规则集稳态面的取舍由此显式化，设计稿 §2.1）。

### 4. Fault Class 与 X-Class 族

Fault Class 是 X-Class 族第三词条：Write Class（写类轴）/ Author Class（作者轴）/ **Fault Class（错误归属轴）**。四档枚举 `app | declaration | platform | external`（platform 三分合一已裁 §8.1③：受管组件/控制面/收敛环/集群不再切档）；自托管语义在「**往哪看**」非「谁来修」；产品原则「**platform 的错永远不读成用户的错**」（Platform Incident 可解释时改写 fault_class 并链回事故条目）由 B2a 批守卫承载。

### 5. freezeguard 改称：diagnostics face → 排障面

`internal/guards/freezeguard_test.go` 冻结豁免理由的第四义称谓 "diagnostics face" 改称「**排障面（exec/browse 冻结豁免义）**」：三条豁免理由（CreateExecSession / StreamExecSession / BrowseDatabase）与其同义中文注释改用「排障面」，豁免语义不变（会话不变更资源状态，冻结窗内排障恰恰最需要）。

**改称范围仅守卫注释**（D0 硬约束：不动用户可见英文文案）：`internal/domain/shared/model/eventcode/events.go` 在册事件 Summary（`exec.session_opened` / `database.browser_opened` 的 "change freeze is exempt — diagnostics face"）与 console 终端页文案（terminal.tsx 两处 "it is a diagnostics face"）是已发布用户可见英文，本批不改——其义按排障面理解，后续批次触碰该域时再评估换词。

### 6. 免责与引述政策（zeabur 域内词）

zeabur 域内词 **plan / apply / gap / incident**（及 managedFields）在提案/设计/ADR 正文属引述语境（文档面不入词汇扫面）；进代码注释、skills 散文、console 文案（词汇守卫扫面）时：

- **已入 bannedPatterns 者**（`apply`——Deployment 词条 _Avoid_ 在册词）须 **wording per-site 豁免**：wordingExemptions 逐条带理由（既有五处豁免即此形态），双向保鲜；
- **skip 分诊者**（`plan` / `incident`——本批随 Change Preview / Platform Incident 词条入册，见决策 7）人工评审把关：仅引述 zeabur 机制语境合法，用于 fleetly 域内语义（把 Change Preview 叫 plan、把 Platform Incident 裸叫 incident）即评审红；
- `gap` 未入 _Avoid_（无 fleetly 冻结词与之争名），引述自由；B2 批若发现争名再显式补裁。

### 7. _Avoid_ 定稿与分诊（宁少勿滥，全仓 grep 核碰撞后）

初稿候选逐个经 `/usr/bin/grep -ri` 全仓核碰撞（扫面 = internal/cmd/sdk/go/genproto/e2e 的 .go + proto/ + skills/*.md + install.sh + console/src），高频合法碰撞且无人工评审价值的删除；留下的按机械直扫可行性分诊：

| 词条 | token | 分诊 | 依据（grep 实况） |
|---|---|---|---|
| Change Preview | plan | skipped | 63 处既有合法语汇：dokploy 迁移计划（DokployPlan）、railpack 构建计划（plan.json/--plan-out）、CLI 退出码注释「diff/plan 类」、泛指计划义——Change Preview 同义词语境人工评审 |
| Change Preview | apply preview | banned | 短语零命中直扫（zeabur 词形精确钉死；单词 apply 已由 Deployment 词条在册禁） |
| Confirm Ticket | confirmation token | banned | 零命中 |
| Confirm Ticket | approval | skipped | 网络 peer 声明批准义（"needs receiver approval"）为 eventcode 在册文案（6 文件 7 处）——Confirm Ticket/审批流同义语境人工评审（B1 明确不是审批流） |
| Diagnosis | self-check | banned | 零命中——防把诊断引擎叫自检；自检义归 doctor/GetStatus（决策 3） |
| Diagnosis / Platform Incident | incident(裸用) | skipped | 仅禁裸用（Platform Incident 应全称）；冻结测试夹具 "incident window"（一般事件义，2 处）合法 |
| Finding | issue | skipped | IssueEventTicket 签发义、GitHub issues webhook 事件（第三方契约）、"re-issue" 动词义在册高频（64 处）——Finding 工单同义语境人工评审 |
| Finding | alert | skipped | Alert Rule/alerting 实体族本义高频（166 处）——Finding 同义语境（把诊断条目叫 alert）人工评审 |
| Fault Class | blame | banned | 零命中 |
| Fault Class | fault owner | banned | 零命中短语——归属是「往哪看」非「谁来修」（决策 4） |
| Platform Incident | outage | skipped | converge_test 的 freeze store/gate outage（一般宕机义，4 处——失败关门/不进冷却台账断言）合法；Platform Incident 同义语境人工评审 |

被删除的候选 token：**无**——每个候选要么机械零碰撞可直扫，要么碰撞全部落在既有合法语境（恰是 skippedTokens 分诊表的形态，workload/Run 词条同款先例），无「连人工评审都无价值」的泛词。初稿假想的 banned 候选 outage 经实核改判 skipped（grep 首轮输出截断误判零命中，守卫实跑纠偏）。

## 后果

- B1/B2 批的 proto 注释、CLI 文案、skills 散文、console 文案有了冻结命名可引；后续批次若发现词条不合适，走显式 ADR 翻案（ADR-0007 律），不接受局部混用。
- 「诊断」一词的五面各归各位：新面（B2 诊断引擎）得名 Diagnosis，四个旧面保持各自在册名词——「排障面」自此是 exec/browse 冻结豁免义的正式称谓。
- wording 守卫新增 5 个 banned token 与 6 个 skipped token（去重后），双向保鲜照旧：CONTEXT.md 撤词条则分诊表同步清死条目。
- 用户可见英文文案中的 "diagnostics face" 字样（events.go Summary ×2、console terminal.tsx ×2）本批不动，登记于决策 5。
- **顺带清偿（双向保鲜欠账）**：console kit 化批次（批3-S2 terminal 收口 50e83430、批4 读面 kit 化 b82a21b9、uikit re-vendor 8e5b7577）遗留的 wording 豁免失养随本批补齐——xterm addon 豁免迁径（exec-terminal.tsx → use-exec-session.ts，旧两条死条目清走）、filter-bar/app-observe 的 apply、kit 数值组件的 ms-epoch、variables store 测试的 api-key 夹具逐条带理由豁免；补齐前 HEAD 的 TestNoBannedWording 既有红因此清零。

## 验收锚

- [x] CONTEXT.md 六词条入册：交付节（Change Preview/Confirm Ticket，Deployment 之后）、写类与作者节（Fault Class，X-Class 族注记）、新节「诊断与排障」置于「写类与作者」与「状态语义」之间（Diagnosis/Finding/Platform Incident）
- [x] 非资源行/非实体声明在册：Confirm Ticket「非资源行（不可列表回读，台账=审计）」（Exec Session/Browse Session 句式）；Diagnosis/Finding/Platform Incident「读时计算、非实体、不落行」
- [x] Diagnosis 四义划界边界句随词条在册（公开诊断面/`fleetly doctor`/Explain/排障面四义与 Diagnosis 词条互不混用）
- [x] wording 守卫双向保鲜：11 个新 token 全部分诊入表（banned 5 / skipped 6，outage 实核改判 skipped），`go test ./internal/guards/ -run 'Wording|Avoid' -count=1` 绿（含顺带清偿 console kit 化批次的既有豁免欠账，见后果）
- [x] freezeguard 注释改称：「diagnostics face」→「排障面（exec/browse 冻结豁免义）」（三条豁免理由 + 同义中文注释；豁免语义与豁免面不变），`go test ./internal/guards/ -run Freeze -count=1` 绿
- [ ] zeabur 引述词 per-site 豁免政策由 B1/B2 批落地时逐点兑现（plan/incident 引述进 skills/console 时按决策 6 口径评审/豁免）——实施批携带
