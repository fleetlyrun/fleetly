# ADR-0090：诊断核心（Finding 注册表 + 事件账本 facts + pending 直接改列 + 受理面确定性）与 compose corpus

日期：2026-10-31 ｜ 状态：已实施（zeabur-oss 借鉴程序第 3 批 B2a+B3a，裁决记录见 `docs/proposals/2026-10-30-zeabur-oss-borrowing.md` §0/§8〔含 §8.5 通知投递两类、§8.8 pending 持久化与第二轮直接改列定稿、§8.6 语料〕，设计规格 `docs/design/2026-10-30-zeabur-oss-borrowing.md` §2/§3；词汇面 ADR-0085、确认闭环 ADR-0089 为前置。`ls docs/adr/` 复核 0090 空闲）

## 背景

fleetly 此前的「为什么坏」回答散在三处：观测面给出现场（state/reason/退出码）但不归因，告警给阈值趋势，部署失败给受理错误——操作者看到 workload failing 仍要人肉拼终态计数与事件流。zeabur-oss 参照（引述语境）：Finding/FaultClass 分层 + 跨 App 聚合「需要你」页。第二轮裁决已定四根桩：诊断是**读时解释层**（不收敛、不评估趋势）；**终态计数单源化 = 事件账本派生**（砍内存计数候选）；**Alert Rule pending 窗直接改列**（无双写过渡）；**通知只投两类**（Platform Incident + crash-loop Finding，App 级其余不投）。B3a 是受理面的行为钉子：真实世界 compose 语料 + golden——W5c corpus 实测咬出受理面确定性缺陷（多违反项并存时错误标识随 map 遍历序随机），本 ADR 一并记档修复。

## 决策

1. **Finding 注册表（errcode 同构三链）**：`internal/domain/shared/model/finding` 首发八条目（七解释条目 + `F_DIAGNOSIS_UNEXPLAINED` 兜底）——crash loop（基础/exit 126·127 命令坏两档）、health check failing、image pull failing、OOM（带/不带 limit 两档）、stopped-explained（看门狗语义解释化，advice 档）。三链同构 errcode 纪律：链 A 构造期 fail-fast（ID 格式 `F_[A-Z0-9_]+`、重复、Source 空、Severity·FaultClass 合法性、Fix 纯值——map[string]string 或 nil，无函数无指针）；链 B golden（`testdata/findings.golden` 与在册集逐字节相等）；链 C usage 反扫（每条目须在生产代码出现为带引号字面量，预留条目进豁免清单带理由）。**只增律**：新条目带 Source 锚点（规则文件+函数+设计稿锚）。Severity 两级（warning/advice，ADR-0085 已裁）；FaultClass 四档（app/declaration/platform/external）；Fix 是纯值（Verb + Params + ConsolePath——verb 仅 restart/scale 等已裁动词，声明面修正如 command/image 带 `field` param 深链部署面走 B1 预览，不带 verb）。
2. **六规则 + unexplained 完备性承诺**：规则集住 domain 环 `internal/domain/services/diagnosis`（`rules_workload.go`：ruleCrashLoop〔含 exit 变体〕/ruleHealthCheck/ruleImagePull/ruleOOM〔两变体〕；`rules_steadystate.go`：ruleStopped；`unexplained.go`：unexplainedGaps）——纯函数 Facts→[]Finding，零 I/O。完备性承诺（设计 §2.1）由守卫测试承载：失败态输入全集 3 态 × 7 退出码（含 nil/0）× 5 reason 原文 × 5 窗内终态次数（阈值上下）× 3 事件面 = **1575 组**，断言「观测面可读且有失败态观测时 Evaluate 永不为空」——无解释规则命中即兜底条目最小陈述覆盖。fixtures 18 组（case.yaml + input.json + expected.json partial-compare 三件套，负向「这不是 X」为主体）同批落地。
3. **facts 面：三件新造两件新导出**（设计 §2.4 现实修正的落地）：新造 ① container 终态 eventcode（`workload.container_exited` 等，载荷带 exit_code/exit_sig——退出信号从观测面搬运进事件账本，幂等：Event 域去重窗承载）；② 00037 `outbox_aggregate_index`（(team, aggregate, aggregate_id) 索引——App 近窗事件查询的账本通道）。新导出 ③ `Engine.ObservationSnapshot`（obs 未导出观测缓存的窄只读出口，`diagnosbypass` AST 守卫执法零旁路：observ/managed/组件健康不经导出面直接被 engine 外引用即红）。**计数纯派生**：`terminalcounts.go` 从账本行投影（EventFact 最小值形状）即时计算，无独立计数器状态——`terminalcounters` 守卫断言无计数表迁移、无计数列、无内存计数 map。
4. **Alert Rule pending 窗直接改列（00038）**：`alert_rules` 加 `pending_since TEXT NOT NULL DEFAULT ''`（空 = 无窗；非空 = UTC RFC3339 起窗时刻）。语义：起窗落列、窗内采样推进**不重置**、满窗迁移 firing、回落清列。无双写（不建内存 map 过渡——推翻 ADR-0041 期「重启即丢是采样近似一部分」的旧语义，重启续窗）；断言三件：迁移换装锚（forward_test）、重启不重置（repo 测试重开库）、诊断吃 pending 态（alerting 测试）。
5. **crash-loop 投递沿纪律 = 事件账本 marker**：不开独立投递状态——`workload.crash_loop_entered/cleared` 两 eventcode 先入账、后投递（Notification Channel 复用）；沿开合以「该 workload 聚合账本里最后一条 marker」判定（00037 索引扫描，行上界钳制），重启安全、不建内存沿 map、不加沿列。**App 级其余 Finding 不投由结构承载**（无投递代码路径即不存在，不靠开关）；Platform Incident 投递随 B2b 批。
6. **API 面**：`proto/fleetly/runtime/v1/diagnosis.proto` 两 RPC 全 READ 桶 `workloads:read`——`DiagnoseApp`（单 App 深诊断：facts 装配 → 规则纯函数 → Finding 条目 + unreadable 诚实面）与 `ListProjectFindings`（「需要你」跨 App 聚合，v1 挂 project 域）。分页 50/200（page_size 缺省 50、上限 200）；廉价先行筛（App 级 state 摘要先滤，命中的 App 才做深诊断装配）。
7. **CLI/console 面**：CLI `fleetly apps diagnose <app>` 与 `fleetly findings list`（全命令双形态，16 golden：健康/崩溃/stopped/unexplained × 人形/json × 分页）；skills 动线升级为新命令序列（散文示例同守卫执法）。console：App 页诊断 tab（条目 + unreadable 面 + Fix 深链）、「需要你」页（project 级 findings 聚合，platform 组特殊形态——platform FaultClass 条目不挂 Fix 动词只给解释），418 vitest 测试。
8. **corpus + 受理面确定性缺陷修复（本 ADR 的修复记录）**：corpus 49 文件（26 accept/23 reject）五层断言（永不 panic/逐文件 golden/manifest↔目录一致/双跑防随机/内嵌目录防漂移）。W5c 实测咬出：**拒绝面多违反项并存时错误标识不定**——六个拒绝循环直接 range map（顶层闭集扫描、configs 声明与声明面键扫描、externalDeclarations 三组、svc.Labels 两处、hc.Extensions、deploy.Extensions、serviceNetworks 长语法报错），报哪个违反随遍历序随机。修复 = 各拒绝面**按排序键执法**（`sortedKeys` 基元，键名字典序——ServiceNames 排序同先例）：同批多违反报字典序首个，单违反行为零变化。gitea-postgres 样本恢复上游双违反原形（W5c 曾裁掉 git_data 声明行绕行），manifest 再生成后仅该行 source/error 变化。**accept digest 零变化论证**：排序只改拒绝路径的控制流（选中哪条错误），accept 路径无拒绝回环、产物落点本就排序归一——实证：49 行 manifest 的 26 条 accept digest 逐字节不动（-update 再生成 diff 仅 gitea-postgres 一条 reject 行）。**连带咬出并补钉**：corpus digest 的 protojson 空白形态由 detrand 按测试二进制撒种（同进程恒定、跨构建翻转）——任意无关代码改动会使 committed manifest 全体 digest 漂移（修复时实测 26 条齐翻）；digest 计算叠 `json.Compact` 空白归一（gateway `writeHookProtoJSON` 同先例），compact 形态即 W5c 首档落盘形态，归一后跨构建稳定且与首档逐字节相合。
9. **诚实边界（本批不做到的，防回头重议）**：组件健康/managed 导出面是 B2b 领地——诊断对组件健康面 `None`（读不了）沉默，不猜；v1 是稳态规则集，**部署失败解释归既有 error 面**（受理/预览/部署管线错误已精确），诊断不接管；Fix 的 field param 是**表单预填提示不进 spec 字段**（声明修正走 B1 预览闭环，诊断不写）；「需要你」页与诊断 tab 的 staging 真机走查待 staging 批（本地 vitest/apitest 金样已钉）；Platform Incident 投递与故障注入 e2e 随 B2b。

## 后果

- 「为什么坏」有了单源回答：Finding 注册表（三链保鲜）+ 六规则纯函数 + 事件账本派生 facts——诊断条目的每个字段可追到规则 Source 锚，新增解释能力 = 注册表加条 + 规则/守卫同批。
- 终态计数从此无第二棵状态树：账本行（00037 索引）是唯一事实，计数/近窗/crash-loop 沿全部即时派生——重启安全不是断言出来的，是结构性的（无状态可丢，pending 窗除外且已改列持久）。
- Alert Rule pending 语义升级为持久窗（00038）：控制面重启后 pending 续窗不再重置采样近似，告警 firing 判定跨重启一致。
- 受理面错误标识与输入一一对应：多违反报字典序首个——CLI/API 的报错可测试、可文档化、可 Agent 解析；corpus digest 跨构建稳定，committed golden 不再受无关代码改动牵连。
- 债与后手：Platform Incident 波及面 + 组件健康/managed 导出 + 故障注入 e2e（B2b）；诊断规则集扩容（每规则 fixtures 先行）；「需要你」platform 组形态随事故面实装后走查定稿。
- 守卫/测试连坐（同批）：`internal/guards/diagnosbypass_test.go`（AST 零旁路）、`terminalcounters_test.go`（无计数表）、finding 三链（fail-fast/golden/usage）、unexplained 1575 组、fixtures 18、pending 三断言、crashnotify 沿纪律断言、CLI 16 golden、console 418、corpus 五层（49 文件）。

## 验收锚（对照设计稿 §6 B2a/B3a 行；本地已验勾选）

- [x] Finding 三链绿 + 纯值类型 + severity 两级定形（golden 形状冻结：`testdata/findings.golden`）。
- [x] 负向 fixtures ≥ 6（实落 18 组，负向为主体）。
- [x] unexplained 兜底守卫（1575 组失败态全集，Evaluate 永不为空）。
- [x] platform 改写断言（纯函数在册；事故面 rewrite 接入随 B2b——本批 platform FaultClass 条目零 Fix 动词）。
- [x] facts 断言：container 终态 eventcode 三链入册（fail-fast/golden/usage + exitSig 载荷）；事件 (team, aggregate, aggregate_id) 索引查询（00037）；WorkloadEvent 快照导出（ObservationSnapshot 窄出口 + diagnosbypass AST 守卫零旁路）；计数纯派生无独立计数器状态（terminalcounters 守卫：无计数表/列/map）。
- [x] Alert Rule pending 窗直接改列迁移（00038）+ 重启不重置断言 + 诊断吃 pending 态断言（无双写：无内存沿）。
- [x] 投递两类断言（crash-loop Finding 投〔事件账本 marker 沿纪律：entered/cleared 先入账后投递〕；App 级其余不投由结构承载；Platform Incident 投递随 B2b）。
- [x] 守卫四件连坐（writeclassguard/ADR-0081 附录/AssertAllRegisteredHavePolicy/scopeguard）。
- [x] CLI `apps diagnose`/`findings list` 双形态 golden（16 件）。
- [x] skills 动线升级为新命令序列（散文示例入守卫执法面）。
- [x] ListProjectFindings 分页/预算（50/200 + 廉价先行筛）。
- [x] console 诊断 tab + 「需要你」页 + platform 组特殊形态（418 vitest）；**「需要你」staging 走查待 staging 批**。
- [x] corpus 四层+第五层断言零漂移（49 文件；**确定性缺陷修复后**：六拒绝面排序执法、gitea-postgres 双违反原形恢复、26 条 accept digest 逐字节不动、digest 加 json.Compact 归一跨构建稳定）。
