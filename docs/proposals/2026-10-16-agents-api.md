# fleetly Agents API 提案（交互式 coding agent 托管环境，2026-10-16）

> 状态：**待裁决**（本文件即裁决载体——§0 总表逐案给裁决，§9 开放问题逐条给答；裁决后实施批各自开 ADR 并携带守卫任务，按 2026-10-03 提案集同款纪律）
> 来源：2026-10-16 Agents API 方向会话三轮收敛。**载荷澄清（本提案的成立前提）**：所指 agent 是 **Claude Code / Codex / OpenCode 这类交互式 coding agent 本体**（人在终端里使唤它干活的环境），**不是** LangGraph/CrewAI 那类 agent 框架服务——后者是 App/Function 形状的皮（长驻=App、请求驱动=Function、跑批=Task），不立实体；前者经 deletion test 重测（§2）判定为现有三执行产品 + 会话原语都不承载的空地。
> 词汇遵守 CONTEXT.md：`Agent` 本提案中指**拟议新实体**（裁决通过则走 ADR-0007 显式修订通道：Task 词条让渡 `agent`——ADR-0077 让渡 `function` 同款先例——+ Agent 词条入册 + 守卫同步）；裁决前本文件为提案语境，非域内混用。拟议实体名恰好避开全部冻结禁词：竞品词 **workspace**（Team/Project 词条 Avoid）、**environment**（ADR-0011 否决实体 + Avoid）均不可用，`Agent` 是唯一既不撞冻结表又语义自洽的候选——且与 CONTEXT.md 开篇「人类与 AI Agent 同为一等用户」的操作者语义**同一物种**（部署的正是这等用户本身：平台两侧 = 操作侧与承载侧，居民领 Token 反向操作 fleetly，Token 词条「服务人与 Agent」直接闭环）。
> 关联：`docs/design/2026-10-16-functions-api.md`（Functions 程序范式：三轴 deletion test → 词汇 ADR 先行 → 设计稿 → 交叉验证；本提案若获裁按同款程序走）；CONTEXT.md Task/Token/Event 词条；ADR-0049（Exec Session/Relay）、ADR-0051（Browse Session/Launcher Ticket）、ADR-0059（在册页面名词非资源行先例）、ADR-0062（Task Mint）、ADR-0077（词条让渡先例）。

## 0. 提案总表

| # | 提案 | 重量 | 建议批次 | 裁决（review 填写） |
|---|---|---|---|---|
| A1 | **Agent 一等实体**：资源行（环境：镜像+工作树卷+凭证引用+网络+回收策略）× 既有 Exec Session 进入面 | **大** | 词汇 ADR 后首批 | 待裁决 |
| A2 | **Network egress 白名单**原语（现仅 egress:none 布尔）——A1 安全基线前置件 | 中 | 与 A1 同批或先行 | 待裁决 |
| A3 | **闲置回收环**（Owner Lease 语义移植：属主=用户会话，宽限后排空、工作树保留） | 中 | A1 落地后（Phase 2） | 待裁决 |
| A4 | **Anthropic self-hosted sandbox 协议对接**（fleetly 作为官方 managed agents 的自托管载体） | 中 | Phase 2（影响 A1 API 形状，§6） | 待裁决 |
| 前提 | **词汇 ADR**：Task 让渡 `agent` + Agent 词条入册 + 守卫同步——A1 生效前置 | 小 | Phase 0 | 待裁决 |

## 1. 背景与动机

### 1.1 赛道实态（2026-10）

交互式 coding agent 的云端化/自托管化是 2026 热战场：Anthropic 官方化 self-hosted sandboxes（Claude Managed Agents 会话跑在自带基础设施上；**environment 一次创建、session 按 ID 引用**的两层模型；MCP tunnels research preview）；Docker 发布官方 agent sandboxes；Coder/Daytona/DevPod/E2B 被定位为 agent 执行标准 substrate；自托管 DIY 方案（容器 + web 终端）成批出现。差异化空位：**自托管 PaaS 把 agent 环境做成一等实体**——现有玩家要么是通用 CDE（无凭证/网络治理面），要么是托管云（非自托管）；fleetly 的 Exec Session/Relay/Secret/Network/Console 终端页地基密度是现成优势。

### 1.2 fleetly 现成面映射

| Agent 环境需要 | fleetly 现成面 |
|---|---|
| 长驻交互进程 + 终端进入 | Exec Session（tty 双形态：Console 终端页 / `fleetly shell`）+ Relay（节点中继，零入站端口） |
| 工作树持久化（跨会话续作） | Volume（无显式 Placement 默认钉住节点） |
| LLM key / git 凭证注入 | Secret（文件注入 /run/secrets，值永不回显）+ GitHub App（installation token 现铸） |
| 隔离网 + 受控出站 | Network（egress:none 有；**白名单缺** = A2） |
| 专属网络组 | Task Network Group（挂靠先例） |
| 按需开灭 + 闲置回收 | **缺口**（= A3；Owner Lease 语义先例） |
| 进入票据 / 会话凭证 | Launcher Ticket（Browse Session 先例） |
| 载体投影与收敛 | Workload 投影 + Ensure（受管聚合先例：Database / Function） |

结论：原语密度极高，真缺口两处（egress 白名单、闲置回收），产品语义缺一处（环境生命周期）——即 A1+A2+A3 的分工。

## 2. 激活模型与 deletion test（实体资格）

| 轴 | Agent | App | Function | Task |
|---|---|---|---|---|
| 激活模型 | **交互驱动**：人开、人进、人收；可离开可回，工作树跨会话续作 | 声明拓扑常驻（副本 ≥1） | 请求驱动、可缩零唤醒 | 创建/调度驱动，TTL 或租约收口 |
| 计量轴 | per-session（在线/闲置时长） | per-runtime（实例×时间） | per-invocation | per-Run |
| 粒度 | 单环境：镜像 + 工作树卷 + 凭证集 | 多 Process 声明拓扑 | 单代码入口 | 镜像直投单形 |

用户判据：**「是我在里面干活，还是它在对外服务」**。逐项排除：App 硬拼（建 App + shell 进入）缺按需开灭/闲置回收/环境生命周期，Revision/滚动/副本是服务心智与「我的工作环境」错配；Function 是请求语义非交互语义；Task 是程序化无人交互；Exec Session 是「进入已有载体」非「开一个带工作树的环境」；Browse Session 形态最近（按需+TTL+票据）但定位是短命数据面浏览工具、非资源行、无持久生命周期。**deletion test 成立：交互式有状态按需环境是 App/Function/Task 面没有也无法便宜补上的形状。**（审查义务与 Functions 同款：每期复盘重跑本节。）

## 3. A1：Agent 一等实体（主提案）

**动机**：§2 实体资格 + §1.1 空位。给 CONTEXT.md 开篇的承诺补上承载面：AI Agent 不但是 fleetly 的用户，还可以是 fleetly 的居民。

**设计方向（供 review，细节归实施 ADR + 设计稿）**：

- **两层模型**（与 Anthropic environment × session 官方模型同构，词汇映射留文档层）：**Agent 资源行** = 持久环境（镜像、资源、工作树 Volume、凭证引用集、网络挂靠、回收策略）；**进入面** = 既有 Exec Session（tty）——不另造终端会话机制。
- **生命周期**：Create（铸造：卷 + 网络挂靠 + 凭证引用装配，Task Mint 受控铸造点先例）→ Start（起载体）→ 进入干活（Console 终端页 / `fleetly agent shell`）→ 离开（载体留守或 A3 闲置回收，工作树恒保留）→ 回来续作 → Stop / Delete（**收口级联零孤儿**：载体、卷、网络挂靠、凭证引用——FB-1 教训前置，DeleteApp 级联缺口在案）。
- **投影**：受管聚合（Database/Function 先例，engine 投影 Workload）；spec 极简（无 Revision/滚动/副本语义——环境不是服务，换镜像 = 显式 recreate 或就地换轨，实施批 design-it-twice）。
- **API 草图**：`CreateAgent / GetAgent / ListAgents / StartAgent / StopAgent / DeleteAgent`（幂等面 + 冻结桶按 Functions 同款纪律）；进入走既有 Exec 面。CLI `fleetly agents` 动词组（双形态 golden）；Console Agents 页（列表 + 详情 + 终端页复用）。
- **观测**：stdout 走既有 Logging；per-session 观测（在线/闲置）随 A3。
- **配额**：MaxAgentsPerProject + 资源维度（常驻小时成本面，开放问题 §9.6）。

**取舍**：重量大的原因是新聚合 + 安全面执法（A2 依赖）；不做的是协议理解（agent 与 LLM 供应商的通信对 fleetly 是普通出站流量——同 Functions「不定义第二协议」纪律）与 agent 内部编排（任务/会话内容平台零感知）。

## 4. A2：Network egress 白名单（安全基线前置件）

**动机**：agent 载荷 = **任意代码执行**。「egress:none 全断 + 手动开全放」之间没有中间档，「只许出 LLM API + git remote」做不出来——这不只是 agent 需求，是一切不可信代码执行载荷的共同基线（沙箱化先于 agent 立案）。

**设计方向**：Network 声明从 egress 布尔扩为 allowlist（host/port 级）；两 Runtime **语义一致、强度诚实分级**（Functions §6 同款口径）：k3s 有 NetworkPolicy 原生执法；swarm 无原生 per-network egress 白名单原语，实现路径（iptables/外部执法/降级为全断+显式全开过渡）是本提案最大技术开放问题（§9.2）。

**取舍**：若 swarm 侧真白名单代价过高，过渡形态 = A1 首批只提供 egress:none（默认）与 egress:open（显式确认）两档 + Console 确认文案披露，白名单随 k3s 腿先行——诚实降级优于假执法。

## 5. A3：闲置回收环（Phase 2）

**动机**：agent 会话一开数小时、大部分时间在等 LLM 响应或等人；常驻成本模型需要「无人在场即回收载体、工作树保留」。

**设计方向**：Owner Lease 语义移植——属主从程序（Task resident 的调用方心跳）换为人（用户会话在场信号：终端会话在 / 显式 keep / 均无则宽限后排空）；回收只停载体，卷与行保留，回来即续作。与 Task Owner Lease 的异同（属主性质、宽限语义、回收面）在实施 ADR 显式对照，防第二份租约语义漂移。

## 6. A4：Anthropic self-hosted sandbox 协议对接（Phase 2）

**动机**：Anthropic 官方支持自带基础设施跑 Managed Agents 会话（environment 按 ID 引用、MCP tunnels 预览）——fleetly 若成为该协议的自托管载体，即获得比 torchwood 更硬的 dogfood 钩子：官方协议导流的真实负载。

**设计方向**：fleetly 侧 Agent ↔ 官方 environment、Exec Session ↔ 官方 session 的**文档层映射**（词汇冻结不破：environment 在域内是禁词，映射表住文档不住代码）；对接面（session 创建 API、MCP tunnel、心跳约定）待协议出 research preview 后专门调研。**对 A1 的前置影响**：两层模型 API 形状尽量同构（§3 已按此取向），避免 Phase 2 对接时二次改面。

## 7. 分期

- **Phase 0**：词汇 ADR（Task 让渡 `agent` + Agent 词条入册 + 守卫同步，ADR-0077 同款三件事一批）。
- **Phase 1（MVP）**：A1 最小面（行 + 铸造 + Start/Stop/Delete 收口级联 + Exec 进入 + CLI/Console）+ A2 过渡档（egress 两档或白名单，按 §9.2 裁决）+ e2e 腿。
- **Phase 2**：A3 闲置回收、A2 全量白名单、A4 协议对接、环境模板（预装 OpenCode/Codex 变体进模板目录）。

## 8. 验收锚（实施完成后逐条核）

1. e2e 全链腿（swarm + k3s 双腿）：create → start → 进入敲命令 → 文件落工作树卷 → stop → start **续作文件还在**；delete 后载体/卷/网络挂靠/凭证引用**零孤儿**；控制面重启后 Agent 行幂等重放。
2. 安全断言：egress 执法腿（白名单外出站被拒 / egress:none 载体出站全断）；凭证注入不回显（Secret 既有纪律外延）；进入面鉴权（scope + 行级授权沿 project 链）。
3. 守卫批：词汇批双向保鲜、CLI 双形态 golden、配额执法、收口级联反扫（FB-1 同型）。
4. dogfood：staging 上一个真实 Claude Code 或 OpenCode 会话全链干活（git clone → 改代码 → push），操作者 = 部署在 fleetly 上的 agent 本人（Token 反向操作闭环叙事的实证）。
5. 每期复盘重跑 §2 deletion test。

## 9. 开放问题（请 review 逐条给答）

1. **词汇裁决**：Agent 词条入册 + Task 让渡 `agent`；操作者/承载双义的共存条款文案（ADR-0077 torchwood 双名条款同型）——是否随本提案裁决一并定稿？
2. **A2 swarm 实现路径**：per-network egress 白名单在 swarm 上的执法手段（iptables per-network / 外部组件 / 不可行）——决定 Phase 1 是「两档过渡」还是「白名单全量」；建议实施前 spike 一腿。
3. **闲置判定信号源**（A3）：终端会话在场 / 用户显式 keep / 平台侧活动观测（载体 CPU+TTY 双静默），单一还是组合？
4. **进入面**：复用 Exec Session（会话绑定受理时实例——载体重启后需重进）是否足够，还是需要 Launcher Ticket 式独立进入凭证（Browse Session 先例）供 Console 分享场景？
5. **镜像面**：平台预装镜像目录（claude-code/opencode/codex 变体，平台追随供应商版本义务）vs 用户自备任意镜像（零义务但安全面全归 A2）——或两者并存、预装仅为模板？
6. **计量与成本维度**：agent 常驻小时计费面要不要进配额族（MaxAgentsPerProject 之外加资源总量维度），还是纯观测不执法？
7. **与 Functions 的形状边界**：Agent 极简 spec（无 Revision）与 Function/App 的 Revision 纪律形成第三种形态——实施批是否需要「环境类聚合无冻结体」的显式 ADR 口径（防「为什么 Agent 没有 Revision」成为评审悬案）？
