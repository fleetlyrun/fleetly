# fleetly zeabur-oss 借鉴程序提案（变更确认闭环 + 诊断引擎 + 测试/前端纪律，2026-10-30）

> 状态：**已裁决（2026-10-30）——13 项全量通过 + 同日第二轮补充裁决（前提：开发阶段/staging 可重建/无向后兼容）**：重裁 2 项（B1 强制度 opt-in→**默认强制+逃生门**；强制集 = Deploy+delete 族+Rollback）+ 纯改良 4 项（ConfirmableVerbs 表驱动立法 / 终态计数单源化事件账本 / ScaleProcess 升一等 RPC / pending 窗直接改列）。§0 与 §8 已逐项回填（含一处首轮非推荐裁决：§8.8 pending 窗持久化）。各实施批开工时各自开 ADR 并携带守卫任务（AGENTS.md 纪律）；配套设计稿 `docs/design/2026-10-30-zeabur-oss-borrowing.md` 同步定形。
> 核心问题：**zeabur-oss/zeabur 的哪些机制值得 fleetly 嫁接，哪些明确不抄**。结论（已裁）：四个立项面——**B1 变更预览与确认闭环**（把「确认的正是展示的」从 UI 约定升为机制事实）、**B2 诊断引擎**（fault 归属 + unexplained 兜底 + Finding 注册表）、**B3 测试文化两件**（compose corpus 语料闸门 + 契约双跑）、**B4 console 纪律两件**（风险分级确认 + UI 体积预算）；结构性参考集 B5 不单独立项。总原则：**抄机制，不抄赌注**——zeabur 的无状态投影 / 每请求用户凭据执行两条架构赌注明确不采纳（§7）。
> 载荷澄清：zeabur-oss/zeabur 是 Zeabur 新开源的 **Rust 控制面**（pre-alpha 0.1.1，151k LOC / 20 crate，本机 `~/workspace/paas/zeabur`），**不是**那个 Next.js 公开仓。其赌注：无自有数据库无 CRD、K8s 对象唯一真源、读时投影、SA 零 Role 每请求以用户身份执行、一切写走 plan→密封 token→dry-run→apply。本文对其机制的事实断言经 2026-10-30 五路深读 + 一轮事实校验 + 一轮四侧独立审查（产品/引擎/安全/治理），关键机制带 zeabur 仓锚点随文标注；fleetly 侧断言全部带本仓 file:line（见设计稿事实基线）。
> 词汇遵守 CONTEXT.md（ADR-0007 词汇冻结）：zeabur 域内词汇 **plan / apply / gap / incident / managedFields** 在本文件为**引述语境**；`apply` 是 Deployment 词条 _Avoid_ 在册词，进代码注释/skills 散文/console 文案须走 wording 守卫 per-site 豁免。fleetly 侧新词已裁入册（D0，§8.1）。
> 关联：ADR-0005（四件一拍）、ADR-0016（Admission）、ADR-0022（Drift）、ADR-0024（幂等）、ADR-0041（metrics/告警）、ADR-0064（内容锚定）、ADR-0071（可读观测）、ADR-0072（所有权分裂）、ADR-0074（compose 受理）、ADR-0075（四环）、ADR-0081（Write/Author Class——X-Class 词条族真源）、ADR-0082（收敛缺省）、ADR-0084（grpcapi policy）；`docs/proposals/2026-10-16-pages-api.md`（预览环境词义）。

## 0. 提案总表（裁决已回填）

| # | 提案 | 重量 | 批次 | 裁决（2026-10-30） |
|---|---|---|---|---|
| D0 | **词汇批**：新词条入册（Change Preview / Confirm Ticket / Diagnosis / Fault Class / Platform Incident / Finding，均非资源行/非实体声明）+ Diagnosis↔doctor/Explain/「diagnostics face」四义划界（第四义改称「排障面」）+ zeabur 引述词 per-site 豁免 | 小 | 第 1 批 | **通过**（全套按候选名，§8.1） |
| B1 | **变更预览与确认闭环**：PreviewDeploy / PreviewTeardown 投影 RPC + Confirm Ticket（验票/烧票进受理临界区）+ ConfirmableVerbs 表驱动强制 + console 接线 + CLI（交互预览默认/--yes/--ticket）+ **Rollback 强制验票** | 中 | 第 2 批 | **通过**（首轮 opt-in/两面+rollback·restart/ACTION+不进 FrozenVerbs，§8.2-8.4；**第二轮重裁 §8.2'：默认强制+逃生门**，强制集 = Deploy+delete 族+Rollback，webhook/Restart·Scale/--yes 豁免） |
| B1s | **ScaleProcess 一等 RPC**：`fleetly apps scale <app> <process> N` 为其皮；服务端读当前生效 Spec 改 replicas 走 MergeOverlay（ADR-0064 保证 scale-only 零替换）；B2a 现场漂移 Fix 的 verb 落点；豁免强制确认 | 小 | 第 2 批（随 B1） | **通过**（§8.10 立项随第 2 批；**第二轮定稿**：升一等 RPC） |
| B2a | **诊断层核心**：Finding 注册表 + Fault Class（四档）+ unexplained 兜底 + 读面/console「需要你」（project 域）+ CLI 动词 + **终态计数 = 事件账本派生**（container 终态 eventcode + App 索引 + WorkloadEvent 快照导出）+ **通知投递两类**（§8.5 裁入）+ **Alert Rule pending 窗直接改列**（§8.8 裁入持久化，第二轮定形态） | 大 | 第 3 批 | **通过**（通知投递两类 / project 域 / pending 持久化，§8.5/8.8/8.9；**第二轮**：计数单源化事件账本、pending 直接改列） |
| B2b | **平台事故层**：受管组件健康 → 波及面联动 + 组件健康/managed 状态导出面 | 中 | 第 4 批 | **通过** |
| B3a | **compose corpus 语料闸门**：真实世界语料库 + golden 断言（Apache/MIT + 30-50 起步，§8.6） | 中 | 第 3 批 | **通过** |
| B3b | **契约双跑**：Runtime 端口行为清单同一套断言对 fake 与真 runtime 双执行 | 中 | 第 4 批 | **通过** |
| B4a | **风险分级确认**：console 确认对话框风险分级（risk 模块纯函数 + 分组单测 + 反模式守卫扩条） | 小 | 第 1 批 | **通过** |
| B4b | **UI 体积预算**：dist 产物 brotli 口径预算文件 + console:verify 执法（**双层口径**，§8.7） | 小 | 第 1 批 | **通过** |
| B5 | **结构性参考集**（不立项，各归各线，§6） | — | 备注 | **通过**（按参考集形态保留） |

## 1. 背景：zeabur-oss 是什么、fleetly 学什么

五路深读 + fleetly 侧两轮事实核查 + 一轮事实校验 + 一轮四侧独立审查（2026-10-30）后的根本判断：zeabur-oss 与 fleetly 是**镜像赌注**——

| 轴 | zeabur-oss | fleetly |
|---|---|---|
| 期望态 | 无自有状态，K8s 对象即真源，读时投影 | SQLite 持有期望态，CAS 四件一拍（ADR-0005） |
| 执行模型 | plan→确认→立即 apply（带 runtime 版本前置条件） | 受理→冻结→Admission→受监督迁移→Ensure 收敛 |
| 身份 | 每请求以用户身份执行（SA 零 Role） | 控制面凭据收敛 + Team/Scope 授权面（行级） |
| 契约 | OpenAPI（utoipa 生成） | proto 唯一契约源 + swagger 派生 |
| 集成面 | modules（声明式 Module + 纯函数 + composer 代读） | providers 注册表（互不 import，ADR-0075 环律） |
| 历史/事件 | 从集群副产物重建（events 1h / managedFields 末次） | 事件溯源 + 审计双轴（更强，无需学） |

因此可移植的不是架构而是**机制层**，且每条都要过 fleetly 模型的适配律：① fleetly 是**收敛模型**不是即时 apply——确认闭环绑定的应是逻辑状态而非 runtime 对象版本；② 控制面**单进程**（SQLite 单写者）——进程内票据店够用；③ proto 唯一契约——新面必须是 RPC 先行。

**fleetly 侧现状核查结论**（锚点在设计稿 §0）：确认机制**纯 UI 层**（`confirm-dialog.tsx`，destructive 单布尔；proto/API 零确认面）；只读投影底座**现成**（`RenderWorkloads`/`DiffRevisions`，投影前段与部署管线单源）；票据店**现成**（`eventtickets.go`，Launcher Ticket 已是第三 purpose）；诊断面**为零**且**事实源比想象更不现成**（崩溃循环所需的活状态只在未导出的 obs 缓存、全仓无 restart 计数器、事件读面无 App 索引——独立审查轮纠正）；console 纪律 = vitest 反模式守卫 + tsc（无 lint 配置，无体积预算）；真实世界语料库**为零**。

## 2. B1：变更预览与确认闭环

**zeabur 机制**（引述，关键锚点）：评审输出 = 每对象 before/after YAML（与对象页 YAML 视图同源裁剪后的展示形，`crates/zeabur-plan/src/plan.rs` shown()）+ merge-key 字段级 diff（Secret 值打码）+ 字段归属账本（`managedFields`）；确认 = AES-256-GCM 密封 change token（request + 每对象版本钉 + 人 + 组，600s，`crates/zeabur-api/src/plan_token.rs`）；apply 只收 token，重读重算，版本钉不等即 409 重评审；前置条件按对象**有无 generation 分钉**（workload 钉 generation、ConfigMap/Secret/Namespace 回退 resourceVersion，`plan.rs` precondition()）；plan 评审阶段即跑 dry-run。三条配套机制随批纳入：**重预览增量展示**、**warnings 可并入修正**（v2 档）、**在途部署预警**（超出参照物的自设补强）。

**fleetly 缺口**：确认只在 console 弹窗，描述与执行物无机制绑定；破坏性操作无 API 级确认面；用户看不到「这次部署会不会滚动、动几个、断不断流」的后果视图。**真机案例收编（2026-10-30）**：手动 docker/kubectl 扩副本（1→2）后重部署被回拉至声明值 1，全程无预警——严格声明式按设计执法（ADR-0081/0082），但 drift 事实埋在事件流没到决策时刻；B1 的**现场漂移预警**（设计稿 §1.3）收此案例。

**嫁接形态**（设计稿 §1）：`PreviewDeploy` = 部署受理的**只读孪生**（三段单源抽取：api 受理前半 + engine merge/合成 + 投影前段），产出 spec diff + 逐 Workload 滚动后果判定（zero_touch/scale_only/replace——内容锚定红利）+ 结构化 warnings + Confirm Ticket；`Deploy(confirmation)` 的验票/烧票在 SubmitSpec 受理临界区（appMu）内；rollback/restart 增 optional confirmation（单目标验票，B2a Fix 依赖闭环）；拆除面为新装配的级联/保留清单。两处刻意偏离 zeabur：不引入 runtime 版本钉（收敛模型 + Converge 兜底）；不走 AEAD（Secret write-only 预览无密文，脱敏红线 = ADR-0071 决策 5 同款）。

**强制度（重裁 §8.2'，2026-10-30 第二轮）**：**默认强制 + 逃生门**——人/Agent 直发 Deploy/delete 族/Rollback 默认必须带票；CLI 交互式默认 = 打印预览 + y/N，脚本/CI 用 `--yes`，Agent 走两调用流（Preview→ticket→Deploy）；**webhook/git 面天然豁免**（材料归 git，ADR-0072——代码评审就是确认）；Restart/Scale 豁免（轻且可逆，与 B4a 风险分级间档）。强制集表驱动立法（ConfirmableVerbs，freezeguard 同型 confirmguard 守卫）。**前提补充（同轮）**：fleetly 处开发阶段、staging 可重建、无向后兼容约束——每次真实部署都走预览路径，机制由日常流量持续检验。

## 3. B2：诊断引擎

**zeabur 机制**（引述）：四层正交纯函数（gap/diagnosis/incident/trace）；fault 归属六源 × 谁来修矩阵（`crates/zeabur-model/src/diagnosis.rs`），「platform 的错永远不读成用户的错」（`incident.rs` link() 改写）；unexplained 兜底；规则 = 稳定 id + citations + `Fix{Plan|Install}` 动作化修复；「读不了 ≠ 不存在」三态 + 负向 fixture 为主体；拒收证据化。

**fleetly 缺口与嫁接**：解释层为零——App 挂了只有状态徽章与原始事件流；平台组件健康孤立展示不与受影响 App 联动。嫁接（设计稿 §2）：Finding 注册表（errcode 同构三链）+ Fault Class 四档（**已裁 §8.1③**：app/declaration/platform/external，platform 合一）+ 诊断规则 domain 纯函数 + unexplained 兜底（守卫承载）+ CLI 动词（`fleetly apps diagnose`——Agent 消费面）+ **通知投递两类（已裁 §8.5）**：Platform Incident + crash-loop Finding 复用 Notification Channel（Alert Rule 指标面仅 cpu/mem 盖不住可用性故障）+ **Alert Rule pending 窗持久化（已裁 §8.8，非推荐项）**：随 B2a 修，诊断可提前吃 pending 态 + 事实源三件新造两件新导出。**自托管语义**：fault_class 价值在「往哪看」非「谁来修」——「需要你」页（**已裁 §8.9：project 域**）platform 组单列「平台造成（等管理员修）」形态。边界五分界（Alert Rule/Drift/Event/doctor·Explain/部署失败解释）设计稿列表。

## 4. B3：测试文化两件

- **B3a compose corpus**（已裁口径 §8.6：**Apache-2.0/MIT + 30-50 文件起步**，仓内优先）：逐文件 golden 断言（Spec digest 或精确拒绝码）+ manifest↔目录一致性 + 再生成双跑防随机。修正历史认知：normalize 静默截断洞**已被 ADR-0074 修复**，corpus 是防回归网非修洞。
- **B3b 契约双跑**：Runtime 端口行为清单（Ensure/Remove 幂等、scale-only 零替换、代戳恒同移、观测口径）同一套断言双执行（单测对 fake、e2e 对真 swarm/k3s）；新腿同步 mise+CI+e2eciaccount。

## 5. B4：console 纪律两件

- **B4a 风险分级确认**（zeabur 参照：risk.ts 查表+分支+分组单测；low→小框 / high→宽框后果先行 / >5 变更→汇总；伴生「能撤销的先做后 Undo」哲学登记不实施）：risk 模块三档（standard / wide / type_to_confirm）+ vitest 单测 + 反模式守卫扩条。
- **B4b UI 体积预算**（已裁口径 §8.7：**双层**——entry JS/CSS + 懒加载 chunk 逐项上限；zeabur 只靠注释纪律，fleetly CI 联查是自设加码）：`console:verify` 内嵌 brotli 断言对 committed 预算文件。

## 6. B5：结构性参考集（不立项，各归各线）

1. **modules 三分离** → B2 架构参照（规则 domain 纯函数 / facts application 装配 / id 中心收口）。
2. **catalog 独立数据仓**（钉版嵌入 + 实测内存 + tested version 措辞 + crosscheck）→ 托管数据库线目录形态参照，该线立项再引。
3. **installer plan/host 分离**（纯确定性 plan、每步幂等、doctor 对照记录）→ install.sh/fleetlyd 引导面远期参照。
4. **VL 查询代理「解析后重建」** → Components 页在册挂账 "needs VL query proxy (phase 3)" 形态参照。
5. **compose 数据库 counterpart 建议**（postgres/redis 镜像→建议托管形态）→ 受理面告警，随 B2a 或托管线。

## 7. 不采纳清单（明确不抄，防回头重议）

1. **无状态投影架构**：换来零运维，付出无跨对象事务、freshness 一致性、事件 1h、单副本。fleetly 期望态 + 事件溯源 + 双 runtime 收敛是更强模型；timeline 重建是「没有事件流时的兜底上限」。
2. **每请求用户凭据执行**：前提是集群有用户级身份面，swarm 无此面，且与「engine 以控制面凭据收敛」根本冲突。同精神纪律已由 grpcapi v0.4 ViewRules（ADR-0084）承载。
3. **Message key 三语契约**：单语英文 + golden 双形态已钉；i18n 立案再启用。
4. **Secret Reveal 面**：zeabur 的 Reveal 方案弱于 fleetly write-only（无 GetSecretValue RPC），反向是它该学我们。

## 8. 开放问题（已全部裁决，2026-10-30）

1. **词汇定名（D0）**——**裁决：全套按候选名入册**（Change Preview / Confirm Ticket〔非资源行〕/ Diagnosis / Finding〔非实体〕/ Fault Class / Platform Incident〔非实体〕）；四义划界随词条入册，freezeguard "diagnostics face" 第四义随批改称「排障面」；zeabur 引述词 per-site 豁免同步。
2. **B1 强制度**——原裁 opt-in；**重裁（§8.2'，无兼容约束第二轮）**：**默认强制 + 逃生门**（Deploy/delete 族/Rollback 必须带票；CLI 交互默认预览+y/N、脚本 `--yes`、Agent 两调用流；webhook/git 豁免；Restart/Scale 豁免）。强制集 = Deploy + delete 族（App/Database/Route/Project）+ Rollback。
3. **B1 v1 范围**——**裁决：Deploy + Teardown(App) 两面 + Rollback 验票**（单目标验票，闭 B2a Fix 依赖；Database 预览后置）；第二轮补裁：delete 族全量入强制集（Database/Project 用轻量确认载荷 = 保留清单文案单源化，完整级联预览仍按批）。
4. **B1 桶归类与冻结期**——**裁决：ACTION 桶 + mint-family，不进 FrozenVerbs**（冻结期预览可用；冻结期铸的票解冻后 600s 内可消费属预期）。
5. **B2 通知集成**——**裁决：v1 投递两类**（Platform Incident + crash-loop Finding，复用 Notification Channel）；App 级全量投递后置。
6. **B3a 语料**——**裁决：Apache-2.0/MIT + 30-50 文件起步**，仓内优先，逐文件标注来源与 license。
7. **B4b 预算口径**——**裁决：双层**（entry JS/CSS + 懒加载 chunk 逐项上限）。
8. **B2a 与 Alert Rule pending 窗**——**裁决：顺手持久化（非推荐项，用户裁定）**：pending 持续窗随 B2a 批落行（alert 域表迁移），诊断层可提前吃 pending 态。
9. **「需要你」聚合域**——**裁决：project 域**（ListProjectFindings 沿 project 链授权）；平台级跨 App 视图后置（届时 platform:read + RequirePlatformAdmin）。
10. **apps scale 声明式动词**——**裁决：立项，随第 2 批**；**第二轮定稿**：升一等 RPC `ScaleProcess`（对称 ScaleTask；scope/幂等/writeclass 表项齐；ADR-0064 保证 scale-only 零替换；B2a 现场漂移 Fix 的 verb 落点，真机案例动线全闭环）。

**第二轮补充裁决（2026-10-30，前提：开发阶段/staging 可重建/无向后兼容）**：① §8.2 重裁为默认强制+逃生门（上）；② 强制集表驱动立法 ConfirmableVerbs + confirmguard 守卫（freezeguard 同型）；③ 终态计数**单源化 = 事件账本**（新增 container 终态 eventcode + (team, aggregate, aggregate_id) 索引，计数纯派生——砍内存计数候选，与 pending 持久化同哲学）；④ Alert Rule pending 窗**直接改列**（无内存/落行双写过渡）；⑤ DeleteDatabase 弹窗文案自相矛盾顺手修（保留清单文案单源化，作为 delete 族轻量确认载荷的第一块）。

## 9. 分期（已裁）

- **第 1 批（D0+B4a+B4b）**：词汇 + console 纪律 + 守卫，零 API 变更，可独立上站。**（已实施 2026-10-30：ADR-0085/0086，Z1.1 记档）**
- **第 2 批（B1+B1s）**：PreviewDeploy/PreviewTeardown（含 CEL 单源）+ **ConfirmableVerbs 表驱动强制确认（Deploy/delete 族/Rollback；拦截器通用验票 + engine 内容绑定两段分工）** + `--yes` 逃生门与 webhook 豁免 + 三段单源抽取 + 票据临界区原语 + **ScaleProcess 一等 RPC** + console 接线 + CLI（交互预览默认/--yes/--ticket）+ e2e 腿（预览-确认-执行-过期重预览-增量展示-并发同票拒绝-无票拒绝-webhook 豁免）。**（已实施 2026-10-30：ADR-0089）**
- **第 3 批（B2a+B3a）**：Finding 注册表 + 诊断核心规则 + unexplained 兜底 + 事实源（**终态计数=事件账本派生**：container 终态 eventcode + App 索引 + WorkloadEvent 快照导出）+ **pending 窗直接改列** + **通知投递两类** + CLI 动词 + corpus。**（已实施 2026-10-31：ADR-0090）**
- **第 4 批（B2b+B3b）**：平台事故波及面 + 组件健康/managed 导出 + 契约双跑。
- 每批收官在 `docs/plan/2026-09-30-feature-checklist.md` 记档；B5 参照点随各自线立项时引用。

## 10. 验收锚（实施完成后逐批核）

1. **B1/B1s**：**无票直发拒绝腿 + `--yes` 逃生门腿 + webhook 豁免腿 + Restart/Scale 豁免腿**（ConfirmableVerbs 表与守卫对账）；console 部署/拆除恒走预览；预览=执行守卫（逐字节 + CEL 零漂移 + marshalSpec 同参 + strict/isolate fixture）；脱敏红线断言；票五拒绝腿（过期/漂移/不匹配/请求者不符/并发同票）+ 幂等降级窗腿 e2e + golden；烧票契约（queue-full 不烧、tombstone 可辨因）；LedgerOnly 审计行断言；滚动判定字段 golden；Rollback 验票腿；**ScaleProcess 双形态 golden + scale-only 零替换断言**；七件守卫连坐 + confirmguard 同 commit；CLI 三形态（交互预览默认/--yes/--ticket，含非 TTY）；console 走查记录。
2. **B2a**：Finding 三链绿 + 纯值类型 + **severity 两级定形**；负向 fixtures ≥ 6；unexplained 兜底守卫；platform 改写断言；**facts 断言**（container 终态 eventcode 入册 + App 索引查询 + WorkloadEvent 快照导出 + **计数纯派生无独立计数器状态** + 零旁路）；**pending 窗直接改列迁移 + 重启不重置断言 + 诊断吃 pending 态断言**；**投递两类断言（Platform Incident/crash-loop 投、App 级其余不投）**；守卫四件连坐；CLI `apps diagnose`/`findings list` 双形态 golden；skills 动线升级新命令序列；「需要你」platform 组形态走查；ListProjectFindings 分页/预算。
3. **B2b**：dind 故障注入腿；组件健康/managed 导出面单测；ListPlatformIncidents = platform:read + RequirePlatformAdmin。
4. **B3a**：corpus 四层断言零漂移。
5. **B3b**：断言清单两侧同名对账守卫 + e2e CI 对账。
6. **B4a/B4b**：risk 单测 + 反模式扩条双向；**双层预算**上涨无理由即红；基线冻结。
7. **D0**：词条 + 非资源行/非实体声明 + 四义划界 + wording 双向保鲜 + 引述词 per-site 豁免。
