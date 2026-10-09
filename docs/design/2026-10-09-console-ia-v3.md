# Console 信息架构 v3——fly.io 式分组与一级资源升格

日期：2026-10-09；状态：**设计定稿，原型已过走查，待实施**。

原型：`2026-10-09-console-ia-v3-prototype.html`（五屏：侧栏分组 / App 8-tab / Databases 列表 / Database 6-tab / Components），走查记录见 `docs/reviews/2026-10-09-console-ia-v3-prototype-walkthrough.md`（PASS）。

上游：ADR-0057（UI v2 设计系统——shadcn/TanStack Router/双主题/六原型解剖**全部延续**，本设计只动信息架构层）；ADR-0044（词汇冻结执法面覆盖全部新 UI 文案）。

## 0. 决策记录（2026-10-09 与用户对齐）

1. **App 详情含独立 Settings tab**（8 tabs）。边界（2026-10-09 二/五轮修订）：**Variables = 高频编辑面**（env vars + secret refs，独立 tab；原更名历程 Configuration→Environment→Variables，终名与 Add variable 按钮及项目级 "Shared Variables" 成族）；**Settings = 低频配置面 + 生命周期与凭证**（General / Build / Processes & rollout / Git hook / Danger zone——Railway 的 Variables/Settings 分法）；Database 的 Settings tab 同语义。
2. **推翻 "managed proxy" 泛称策略**：Components 页露出组件实名（Traefik / zot / VictoriaLogs / VictoriaMetrics）。词汇变更随一期实现 commit 同批开 ADR + CONTEXT.md 词条（见 §7）。
3. **Database 的 Logs/Metrics tab 接受降级预案**：载体寻址验证（§8 T8）不过则一期 4-tab 上线，6-tab 为目标态，验证通过或二期补齐后点亮。
4. **对象存储留战略位**：DATA & STORAGE 分组名已占住语义；导航**不上空项**（与原则 4 一致），产品立项时新增 "Object Storage" 项并开独立 ADR。

## 1. 问题（现状四病灶）

1. App 绑定功能全局散页：Logs/Metrics 是 App 详情的外链、Config 在项目级、Terminal 是独立全局页——排障动线被切碎。
2. 数据库后端能力完整（5 引擎、备份/校验/恢复/在线浏览全有 API），但 Console 只给 Data 页里一个表格面板，非一级资源。
3. Storage（卷只有创建+列表）、Registry（zot 零管理面）、受管组件（VL/VM 健康恒 HEALTHY、无排障入口）——全站空白。
4. 数据安全无聚合视图："昨晚备份成没成"要逐库点开行展开、平台快照埋在 Settings。

## 2. 设计原则

1. **两域模型保留**：侧栏上半 = 项目域（对应 fly.io 的 org 语境），下半 = 平台域（跨项目舰队面）。
2. **资源绑定功能下沉**：凡离开该资源没有意义的功能（App 的日志/指标/终端/配置/路由，DB 的备份/浏览）一律成为资源详情 tab；全局工作台保留为跨资源排障工具，重定位 ≈ fly.io 的 Grafana 位。
3. **一级资源 = 独立生命周期实体**：Apps / Databases / Tasks 是工作负载三兄弟；**DB 与 App 对等**（DB 本身是受管 workload，有容器/日志/指标/凭证）；Volumes / Registry 是项目资产；受管组件是平台设施。
4. **导航无空壳**：每个导航项上线当天就有真数据（一期每页都有现有 API 可拼，见 §8）。
5. **新页复用 ADR-0057 六原型解剖**：列表页走 List、详情页走 Detail、Components 走 Dashboard 卡片、Logs/Metrics/Terminal 走 Workbench、危险区走 Settings。
6. **创建交互统一模式**（2026-10-09 三轮修订拍板）：CRUD 列表的创建入口固定在**列表工具栏右上**（页面级列表在 pagehead 右上），创建/编辑表单一律**模态对话框**；行级动词动作（Deploy / Back up / Browse / Verify）留行尾。原型已按此执法四处：App Routes（Add route）、App Variables（Add variable）、Apps 列表（New app）、Databases 列表（New database），侧栏 + New 与 ⌘K 同链路。同批拍板：**tab 级提交型动作（Apply changes）与创建动作同驻工具栏右侧，脏态（有暂存）才启用**。

竞品锚：fly.io（用途分组 + 资源下沉）；Railway（service 固定 tab 集——Deployments/Metrics/Variables/Settings 范式）；Dokploy 反例（[侧栏混乱 issue #2805](https://github.com/dokploy/dokploy/issues/2805)，空壳/交叉入口之弊）。

## 3. 侧栏结构

```
┌────────────────────────────────────────┐
│ [ProjectSwitcher ▾]        [+ New]     │ + New: Project / App / From Template
│  Overview                              │ 项目总览 /p/$id
│ ── BUILD ────────────────────────────  │
│   Apps          → 8-tab 详情           │
│   Deployments   全项目部署流（客户端 fan-out，轴不变）│
│   Tasks         一次性 + cron          │
│   Registry      项目镜像仓视图 ★        │
│ ── DATA & STORAGE ───────────────────  │
│   Databases     一级资源·6-tab 详情 ★  │
│   Storage       Volumes + Uploads ★    │
│ ── NETWORK ──────────────────────────  │
│   Routes / Networks                    │
│ ── MONITOR ──────────────────────────  │
│   Logs          → /logs?project=$id    │
│   Metrics       → /metrics?project=$id │
│ ── CONFIGURATION ────────────────────  │
│   Configuration                        │
├────────────────────────────────────────┤ 平台域
│ ── FLEET ────────────────────────────  │
│   Nodes         enroll/drain/cordon    │
│   Components    受管组件健康 ★          │
│   Events        舰队事件流              │
│   Alerts        规则+状态；渠道→Settings │
│   Backups       数据安全聚合视图 ★      │
│ ── ORGANIZATION ─────────────────────  │
│   Identity / Audit / Settings          │
└────────────────────────────────────────┘
```

- `nav.ts` 单源延续（侧栏 + ⌘K 共用），新增 `group` 字段承载分组标签。
- **导航撤下三项**（路由全部保留兼容）：Quickstart、Templates → `+ New` 入口；Terminal → App tab（`/terminal` 留深链）。
- 项目域 MONITOR 的 Logs/Metrics 是**参数化入口**（跳全局工作台带 `?project=`），不新增路由——保住现有工作台 URL 契约与深链（App tab 的 `/logs?app=` 协议不变）。
- 通知渠道（webhook/telegram，舰队级资源）从 Alerts 页迁到平台 Settings。
- 舰队总览 `/overview` 承担全局入口：最近事件、Backups、全库工作台快捷方式。

## 4. 资源详情 tab 集

### 4.1 App 详情（8 tabs）

| Tab | 内容 | 就绪度 |
|---|---|---|
| Overview | 状态 hero、健康探针、副本与滚动状态（含 `rollout_stalled`）、**firing alerts 徽标**（链 Alerts）、最近部署、暴露的 routes | 现有 API |
| Deployments | 列表 + 叙事详情；**Builds 与 Revisions 收编为 tab 内子视图** | 现有 API |
| Metrics | app 域预设（CPU/内存/网络），复用全局工作台预设机制 + app 过滤 | 现有 API |
| Logs | app 域流（工作台 scoped 变体，search params 协议不变） | 现有 API |
| Terminal | exec 会话，process/replica 选择器（复用 execstream） | 现有 API |
| Variables | **env vars + secret_refs 表格化编辑**（secret ref 链到库详情；Add/Edit/Remove 走对话框暂存，**Apply changes 在工具栏右侧、暂存后才启用**，应用 = 组装新 spec 走 Deploy）；高频编辑面，故独立成 tab | ❌ **T2 摸底修正（2026-10-09）：spec 无读取通路**——v1Revision 不带 spec、GetApp 仅 id/name/created，console 从未展示过 app env；tab 位保留 hidden，**二期 proto：GetApp 带 spec（或 v1App 加 spec 摘要）**（与 db/run logs 轴同批）。DeploySheet 的 image 模式 env 字段是现有唯一 env 写入面 |
| Routes | 该 app 的 host 暴露面子集 + 创建（预选本 app）/ 删除 | 现有 API |
| Settings | **低频配置面 + 生命周期与凭证**：General（名称/ID/创建）、**Build**（source/builder/dockerfile 路径）、**Processes & rollout**（副本/滚动/健康门/协议）、Git deploy hook（get/set/**rotate token**）、危险区（删除 + 级联披露）。rename 语义需验证（appID 稳定则安全，zot 仓布局按 appID 键） | 现有 API；rename 待验证 |

### 4.2 Database 详情（6 tabs；降级预案 4 tabs）

| Tab | 内容 | 就绪度 |
|---|---|---|
| Overview | 状态（含 restore pending）、引擎/版本、endpoint（host:port）、凭证卡（Secret `database:<name>` 引用 + Reveal once）、**引用此库的 Apps 反查**（客户端 fan-out 扫 app spec 的 secret_refs）、卷与节点钉住 | 现有 API |
| Metrics | 库载体容器指标（cadvisor 预设） | ✅ **T8 已验证：可**——`container_label_fleetly_workload_id="<dbID小写>"`（或 `fleetly_ns_database`），k3s 走 `namespace="fleetly-<project>"`+pod 前缀；见 §8 T8 |
| Logs | 库载体日志流 | ❌ **T8 已验证：需二期 proto**——`StreamLogsRequest` 仅 app 轴（API 层 app_id 必填钉死 App 域）；数据已在 VL（`fleetly_workload=<dbID>`），Service 层 NamespaceRef 本有 Database/Task 域轴，二期最小改动=proto 加 `database_id`/`run_id` + API 换轴 + VL 过滤项，provider 零改动。tab 位一期保留 hidden |
| Backups | 调度节奏、列表（时间/大小/digest/状态）、Trigger / Verify / Restore-to-new 向导 | 现有 API；Download 二期 |
| Browse | 按方言起 adminer/pgweb/rediscommander/mongoku，只读/读写两档，TTL 明示 | 现有 API |
| Settings | 危险区（删除，明示级联：载体拆、卷与凭证 Secret 保留）；Rotate password 二期（级联语义：引用库的 App 须重启取新值，确认页必须披露） | 删除现有；rotate 二期 |

列表页：engine 过滤、**备份健康度列**（最近成功/成败）、行内 backup/browse、新建向导含 restore-from-backup。

### 4.3 Task 详情（3 tabs）——工作负载三兄弟补齐

Task 是程序化工作负载（one-shot / resident 双形态 + Owner Lease；Schedules 按 cron 铸 one-shot），详情页范式与 App/DB 对齐，Runs 的日志与等待收流不再无处承接。

| Tab | 内容 | 就绪度 |
|---|---|---|
| Overview | 定义（镜像/命令/env/secret_refs）、形态（one-shot/resident）、并发与 Lease、最近 runs、来源 Schedule 反链 | 现有 API |
| Runs | 运行列表（状态/起止/时长）；**Run 详情：状态、日志流、Stop**——叙事页沿 deployment-detail 范式（wait 收流 + 轮询兜底） | 现有 API；run 日志经 logs API 定址并入 T8 一并验证 |
| Settings | Scale 并发、Stop、删除（级联披露） | 现有 API |

### 4.4 删除级联披露原则

所有危险区按动词真实级联语义写文案（app 删除：routes 解绑/registry repo 留置/secrets 不级联删——以动词实现为准，实现时核实）。

## 5. 新页明细

### 5.1 Registry（项目域 BUILD）——一期就有真数据

- **一期**：按 app 维度"当前镜像"视图——每 app 当前部署 image + digest（revisions/builds 拼），链到部署记录。回答"到底跑的哪个镜像"。
- **二期（proto 先行）**：zot `/v2/_catalog` + tags/list 按项目凭证代理（新动词）→ 完整 tag 清单/大小/最近推送；**凭证轮换**入口（构建时经 Secret `registry:<host>` 解析，理论无级联，需验证）。
- 平台侧（GC/配额/总用量）归 Components 页，不做项目入口。

### 5.2 Components（平台域 FLEET）——排障驾驶舱

卡片四张：**Traefik（Proxy）/ zot（Registry）/ VictoriaLogs（Logs）/ VictoriaMetrics（Metrics）**（+ 每节点 cadvisor 附注）。每卡三层：

1. **活着吗**：健康 + 版本。一期健康照实显示 `unverified`（GetStatus 现恒 HEALTHY，provider Health() 未接线——二期接），版本现有 API。
2. **吃得下吗**：磁盘水位（二期，cadvisor fs 指标）、retention 展示（改值二期 API）。
3. **数据新鲜吗**：**ingest 时效**——最新样本/最近日志行的 age，用现有 PromQL/VL 查询即可算。"日志断了"的第一诊断信号，一期就做。
- 动作：跳对应工作台（一期）；Restart carrier（二期动词，载体是普通 workload）；VL/VM 原生查询面受管链接（三期 ticket 代理）。

### 5.3 Backups（平台域 FLEET）——数据安全聚合页

每库一行：最近备份时间/成败/动作入口；平台快照（restic）区块。实现 = 客户端 fan-out ListBackups（沿 ADR-0057 deployments fan-out 先例），聚合 API 二期再优化。失败项一键跳库详情 Backups tab。

### 5.4 Storage（项目域，两 tab）

- **Volumes**：列表 + 创建（节点钉住）+ 挂载 Apps 反查；用量/删除二期（删除仅限未挂载）。
- **Uploads**：源包列表（digest/大小/引用 build）。

### 5.5 创建对话框契约（New app / New database）

**只承载"身份 + 初始源"**，其余默认值创建后到 tab 配置（create-then-configure，Heroku/Railway/Dokploy 同型——瘦对话框正是 8-tab 详情页成立后的自然结果）：

- **New app**：Name + Source 三模式按选择联动字段——Image（镜像引用；私有仓凭证经 secret `registry:<host>` 解析）/ Compose（compose YAML 文本域）/ Upload（源码包 dropzone + builder 选择 dockerfile/railpack/static）。**Git hook 不是创建时源**——hook 是 App 建好后的 per-app 设置（Settings → Git deploy hook）。进程/端口/协议/健康门默认值创建，后续在 Settings、Variables、Routes 各 tab 配置。
- **New database**：Name + engine（5 引擎）+ 可选 restore-from-backup；引擎参数配置属二期（§8）。
- 多步流程不进对话框：Restore 向导、模板实例化走 Flow 型页面（v2 六原型之 Flow）。

## 6. 路由映射

| 现状 | 目标 | 动作 |
|---|---|---|
| `/p/$id/apps/$appId`（2 实页 + 2 外链） | 同路径 8-tab | Logs/Metrics 转实页；+Terminal/Variables/Routes/Settings |
| `/p/$id/data`（三面板） | `/p/$id/databases`(+`/$dbId` 4~6 tab)、`/p/$id/storage` | 拆页升格，`/data` 重定向 |
| `/p/$id/tasks`（双 tab 列表） | 列表保留 + `/$taskId` 详情（3 tab，含 `/$taskId/runs/$runId`） | 详情新增 |
| `/logs` `/metrics` | 保留（全局工作台）；项目域 MONITOR 参数化入口 | 入口重定位 |
| `/terminal` | 导航撤下，App tab 承接 | 深链保留 |
| `/quickstart` `/templates` | 导航撤下，`+ New` 承接 | 路由保留 |
| `/alerts` 内渠道区块 | 渠道迁平台 `/settings` | 拆 |
| — | `/p/$id/registry`、`/components`、`/backups` | 新增 |

## 7. 词汇与 ADR 事项

- **组件实名 ADR**（随一期实现 commit 同批）：推翻 "managed proxy" 泛称；Components 页实名露出 Traefik/zot/VictoriaLogs/VictoriaMetrics；其他页面保持产品语态（Routes 页副标题可注 "managed Traefik"）。CONTEXT.md 新词条：Components、FLEET（分组名）、四组件实名；退役词条按流程处理。守卫反扫同步（散文/示例里的 "managed proxy" 一并清）。
- 新 UI 文案全部过 ADR-0044 词汇冻结执法面（既有测试自动覆盖）。tab 终名 **Variables** 沿用既有词条族（项目级 "Shared Variables" 同源），不新增词条；退役的 "managed proxy" 一并进 T9 的 CONTEXT.md 批次。

## 8. 分期与一期任务清单

### 一期（现有 API + 两处低成本验证；每页上线即有真数据）

| # | 任务 | 说明 |
|---|---|---|
| T1 | 导航重组 | nav.ts 分组 + `+ New` + 撤三项 + ⌘K 同步（单源自动） |
| T2 | App 详情 8-tab | 一期 **7 tab 实页**（Metrics/Logs/Terminal scoped + Routes/Settings）+ Variables tab hidden 挂账二期 proto（spec 读取通路）；Overview 加 firing 徽标 |
| T3 | Databases 一级页 | 列表（备份健康度列）+ 详情（Overview/Backups/Browse/Settings 先行，Logs/Metrics 视 T8） |
| T3b | Task/Run 详情页 | 3-tab 详情 + Run 叙事详情（复用 deployment-detail 范式；run 日志定址并入 T8 验证） |
| T4 | Storage 页 | Volumes（挂载反查）+ Uploads（引用 build 反查） |
| T5 | Registry v1 | 当前镜像视图（revisions/builds 拼） |
| T6 | Components | 版本 + `unverified` 状态 + ingest 时效 + 工作台链接（Dashboard 原型） |
| T7 | Backups 聚合页 | 库 fan-out + 平台快照区块 |
| T8 | **验证：载体寻址** | ✅ **已完成（2026-10-09）**：metrics 可（零后端，PromQL 按 `container_label_fleetly_workload_id` / k3s namespace+pod，swarm 实证）；logs 不可（`StreamLogsRequest` 无 db/run 轴，需二期 proto 加 `database_id`/`run_id` + API 换轴 + VL 过滤项，provider 零改动）。**T3 据此定稿：DB 详情一期 5-tab（Overview/Metrics/Backups/Browse/Settings），Logs tab 二期点亮；T3b Run 详情一期无日志流（WaitRun 状态流不受影响）** |
| T9 | 词汇 ADR + CONTEXT.md | 同批落地（§7） |
| T10 | golden/守卫更新 | nav 快照、反扫、走查记录 |

### 二期（proto 先行，按排障价值排序）

GetStatus 真健康接线 + 磁盘水位；carrier restart 动词；**logs database/run 轴（`StreamLogsRequest` 加 `database_id`/`run_id` + API 换 NamespaceRef + VL 过滤项，provider 零改动）**；**App spec 读取通路（GetApp 带 spec 或 v1App 加 spec 摘要——Variables tab 点亮的前提）**；zot catalog 代理 + 凭证轮换；DB 备份 Download、密码轮换（含级联语义）；Volume 删除/用量；Backups 聚合 API。

### 三期

VL/VM 原生查询 ticket 代理；DB 账号/参数/扩容；App restart/scale 专用动词；对象存储（战略位，独立 ADR 立项时启动）。

## 9. 动线验证

1. **应用 5xx**：App 详情一跳到位（Overview 徽标 → Metrics/Logs → Terminal 复现），全程不出 App 语境。
2. **日志断了**：FLEET → Components → VictoriaLogs 卡片 ingest 时效标红 → 一期跳工作台验证，二期就地 Restart carrier。此前全站零入口。
3. **昨晚备份成没成**：FLEET → Backups 一屏看全（每库 + 平台快照）。
4. **跑的哪个镜像**：项目 → Registry → 当前 digest + 链到部署记录。

## 10. 不做清单（本期明确出界）

对象存储 app 面（Tigris 对位——战略位见 §0.4）；DB 账号管理（单凭证模型，产品级重构）；配额/计费；多集群管理。
