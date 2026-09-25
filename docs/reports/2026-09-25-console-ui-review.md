# fleetly Console UI 全面审查（2026-09-25）

对照基准：dokploy v0.30.6（源码 `D:/Codes/baas/dokploy`）。方法：staging 真机浏览器逐页走查（23 路由全覆盖 + review-\* 一次性对象实走关键流程）× 代码交叉核实（前端能力门、服务端权限/scope 登记）× dokploy 功能面逐模块对照。走查构建 = 当日工作区最新代码（含未提交的 ProjectDetailPage），按既定口径叠加部署到 staging `/opt/fleetly/console`（旧产物备份于 `/tmp/console-backup-20260925-152918`）。

截图证据：`D:/tmp/fleetly-console-review/`（01–54，索引见 §9）。

---

## 1. 总评

**「距离可用还很远」的判断成立，但主因不是缺页签，而是核心写入链路在产品层断裂。** 三颗雷叠在一起，使 Console 当前实际形态是「只读仪表盘 + 数据库/团队管理面」，而非部署工具：

1. **全站没有「创建应用」入口**（应用列表只有筛选/刷新；Home 无 CTA；项目详情卡的 Applications 空态也没有按钮）。后端 deploy 是 upsert 语义（首次部署即建 app），但该能力只有 CLI/git push 能触达。
2. **Console 对既有应用的部署 100% 失败**：5e593d9 把应用详情导航改成平台 id 寻址后，服务端 A1 校验（`internal/api/deployments.go:121`）拿**原始请求参数（ULID）**与 compose `name:` 比对，永远不等 → `E_COMPOSE_UNSUPPORTED`。实测两次（compose name 分别为新名与 `stressapp` 本名）均失败（截图 27/28）。
3. **平台管理员在服务端对全部资源只读**（`internal/api/ownership.go:113-117`，含自己 owner 的团队），而前端能力门只看团队角色（`console/src/lib/context.tsx:246-269`）→ Deploy/Rollback/建库/轮换凭据等按钮照常渲染，点了必 403。CLI 交叉验证：founder 用自己绑定的 PAT 向自己 owner 的 `review/review` 部署 → `requires deploy; resolved read`。

单用户自托管者（v0.3 注册即平台管理员 + 个人队 owner，README 主打受众）注册后的真实体验：**看得到一切、点不动任何写按钮、新建无门**。这与「首启语义=注册窗口」的设计叠加后，首次上手路径在 Console 上是死的。

与之相对，**视觉与信息架构的外壳反而成熟**：dokploy 式卡片栅格、状态色点、诚实空态、结构化错误信封（code/message/suggestion/docs）、组件健康网格、HA 边界的「You get / You do not get」、 Secrets 页的 write-only 语义文案，都是高于平均水准的。可以用一句话概括：**外壳 80 分，发动机没接油管。**

---

## 2. P0 —— 可用性阻断

### P0-1 应用创建无 UI 入口
- 位置：AppsPage（04）、HomePage（03）、ProjectDetailPage（20）。
- 事实：全站 grep 与实走均无创建/部署第一个应用的入口；`deploy` API（`/apps/{app}/deployments`，endpoints.ts:492）目标是**已存在的路由参数 app**，从 Console 只能进入既有应用的 Deployments 页，不存在「以新名部署」的 UI 路径。DeployCard 文案 "The app is created on first deploy"（截图 27）描述的是 CLI/git 语义，在 Console 语境里是误导。
- 对照：dokploy 项目页 = 创建枢纽（Add Application/Database/Compose/Template 四入口 + 统一服务列表）。

### P0-2 Console 部署既有应用 100% 失败（id 寻址回归）
- 复现：任一应用 → Deployments → 粘贴 compose（`name:` 与应用本名一致）→ Deploy → `E_COMPOSE_UNSUPPORTED: compose application name "stressapp" does not match the requested target app "01M3BRQY9NQMPPGPJ3Q828AEJ7"`（截图 28、29）。
- 根因：`deployments.go:121` `if req.GetApp() != "" && spec.Name != req.GetApp()` 用**未解析的原始参数**比对；Console 自 5e593d9 起传平台 id。CLI 按名部署不受影响（e2e 也没盖住这条 UI 路径）。
- 附带：错误文本把 ULID 当「requested target app」暴露给用户，可读性差（见 P2-1）。

### P0-3 平台管理员资源面：前端渲染写按钮、服务端全拒
- 服务端：`ownership.go:113-117` 平台管理员无条件 `levelRead`（职责分离，设计如此）；scope 侧 `reachableScopesForUser` 却给平台管理员**全集 scope**（`auth.go:447-448`），所以 scope 门全放行，最后由资源角色门 403。
- 前端：`useTeamCapabilities` 只解析团队角色（owner→全开），完全不建模 `is_platform_admin` → founder（owner）看到 Deploy/Rollback 卡（截图 27）、建库对话框（31）、Reveal/Rotate/Delete 等全部按钮。
- CLI 实证：`fleetly deploy --project review/review`（founder 的 PAT，绑定该项目，founder 是该队 owner）→ `insufficient role on this project (requires deploy; resolved read)`。
- 影响：v0.3 首用户=平台管理员 → 主力受众的整条写链路「按钮可见、点了必炸」。同类影响面：`DeleteApp/SuspendDatabase/RotateDatabaseCredentials/RevealDatabaseCredentials/TriggerBuild/SetAppWebhookSecret` 等 scope.go 全部 admin 登记项。

---

## 3. P1 —— 重要缺陷

### P1-1 邀请注册死路（匿名 + 注册关窗）
匿名打开邀请链接 → "Sign in to accept" → 登录页只有邮箱+口令，**注册表单因注册关窗被隐藏**（截图 42/43）——而 W3 后端明确支持「关窗 + 有效邀约注册」。被邀请的新用户在 Console 无路可走，只能请管理员先建号再回来点邀请（本次实测被迫走这条四步链：Admin 建号 → 临时口令 → 登录 → 重开邀请链接接受）。登录页应感知「带邀请 token 的匿名访客」并开放受邀注册表单。

### P1-2 登出→登录后身份串号（暂态，观察到一次）
review-user 登录后整层身份壳仍是 founder：用户菜单/团队切换器/Admin+Audit 导航全为 founder，而数据查询已是 review-user（Applications=0）。整页刷新后恢复正确（截图 48）。判定为登录切换时 Me 投影/查询缓存未彻底失效的暂态串号——服务端数据未被越权（数据面按新会话解析），但非管理员会看到 Admin 入口等错误视觉，且若缓存口径再宽一点就是越权渲染。复现率低（1/3），建议登录成功后强制整树 re-mount 或 `queryClient.clear()`。

### P1-3 多团队用户未选团队 → 写面静默消失
founder 有 3 个团队，顶栏选「All teams」时 `useTeamCapabilities` 解析不出角色 → Deploy/Rollback/建库等全部消失，无任何提示（对比：单团队用户有回落）。实测先在 All teams 下看 Deployments 页无 Deploy 卡，切选团队后出现。应在切换器或页面给出「选择团队以启用操作」的显式引导，而不是让按钮无声蒸发。

### P1-4 localStorage Bearer 优先 → 外来 token 静默换身份
api client 规则「localStorage 有 token → Bearer 优先不回落」（client.ts:5-6,133）。本次走查实际被咬：同一浏览器 profile 里并行会话残留的 PAT 劫持了我会话的请求，ACME 设置卡出现「token scope insufficient (requires admin)」（截图 34）——清掉 `fleetly.console.token` 后同页恢复正常（截图 34b），证明是凭据混入而非产品 bug。但该机制本身是隐患：共享机器/多会话场景下，一个残留 token 会把整个 Console 的身份静默换成 token 主人，且 403 文案完全解释不了「我明明登的是管理员」。建议：token 仅存内存或 sessionStorage；至少在 UI 显著位置显示「当前以 API token 身份操作」。

### P1-5 Drift 全无 UI
staging 事件流持续刷 `reconcile.drift_detected`（截图 03/39），CLI 有 `drift show/converge/enable/disable`，Console 零呈现——用户看不到哪些应用漂移、漂了什么、如何收敛。至少应：应用行/详情徽章「drifted」+ Overview 卡显示漂移摘要 + Converge 按钮。

### P1-6 Builds 全无 UI
BuildsService 三 RPC + CLI `builds list`，Console 无构建台账；build 失败只能从部署行/日志间接感知。dokploy 的 Deployments 页签把构建日志与部署历史并置。

### P1-7 deleteApp / deleteTeam 无 UI（无危险动作区）
`deleteApp`/`deleteTeam` 在 endpoints.ts 已封装（注释还写着两段式删除），无任何页面消费。应用详情壳没有 Danger Zone，删应用/删队只能 CLI。团队页对比：项目删除有入口（团队设置 Projects tab），应用没有——同一产品里删除能力覆盖不一致。

### P1-8 Git push 通道不可发现（GitKeys / webhook source 无 UI）
git push 部署是一等通道，但 GitKeys（add/list/rm/fingerprint）、`SetAppSource`、webhook secret 配置全部 CLI 专属，Console 无引导无页面。dokploy 有完整 Git providers + Webhook 设置页。对应 scope.go 的 admin 登记项全部闲置。

---

## 4. P2 —— 打磨与一致性

1. **ULID 裸显**：Events 行 subject（`team:01M3CCT6…`）、Home Recent activity、Audit target（`token:01M3…`）、Databases Placement（`n_01M39V6Q…`）、P0-2 错误文本。面包屑/列表都已做反解，这些漏网点应统一走「反解业务名」管线。
2. **Tab 状态 IA 不一致**：SystemPage 用 `?tab=`（可深链），TeamPage 用 useState（不可深链）→ ProjectDetailPage 头部「members & role overrides」链接永远落在 Members tab（两个链接还同指一个 URL）；TeamPage 项目行不可点进 /projects/:id，与 ProjectsPage 行为相反。
3. **时间显示不一致**：同一张卡里 Created=`2026/9/25 15:51:09`（绝对）vs Updated=`7h ago`（相对）（截图 05）；ACME 卡在未配置态显示 `saved 1970/1/1 08:00:00` 零值时间戳（截图 34b）。
4. **AppEnvPage 行删除无确认框**——违背全站破坏性动作两步确认纪律（Secrets/PAT/Admin/库均有）。
5. **PAT 页**：无页头标题（卡片直接裸起，截图 24b）、面包屑小写 `pat`、入口仅用户菜单（可发现性弱，dokploy 放 Settings）。
6. **导航**：Teams/Projects 无分组标签地夹在 Home 与 Platform 组之间；Admin 与 System 共用 Server 图标。
7. **缓存失效面窄**：建队后「All teams (platform view)」列表不刷新（截图 13 里 Review 已在「我的团队」但平台视图没有）。
8. **HomePage 实际是静态 import**（App.tsx:25），与「仅登录/邀请/壳 eager」的注释不符（首页本就首屏，注释该更新或该 lazy）。
9. **面包屑子页签态**：应用子页签（Logs/Env/…）在 get 响应前显示字面 `Application`（截图 06-10）。
10. **Terminal 页右上角状态芯片**渲染原始占位 `status...`（截图 10）。
11. **已封装未消费端点**（grep 全 src 核实）：`deleteApp`、`deleteTeam`、`triggerBackup`（手动状态备份）、`rotateJoinToken`、`updateAlertRule`（告警规则只能删了重建）、`getWebhookEndpoint`（单体 GET）。
12. **后端能力在、UI 缺**（CLI 有 / Console 无）：`MoveApp/MoveDatabase`（v0.3 规划 W2 明文承诺「资源改派」，未落地）、`UpdateDatabaseSettings`（建库后改限额）、`GetLogsBackend/SetLogsBackend`、`UpdatePlacement/GetPlacementMigrationPlan`、独立 volumes 列表、`GetTeam/UpdateTeam`（团队改名）。
13. compose 受控子集拒绝 `ports` 的报错（E_COMPOSE_UNSUPPORTED）文案清晰，但 Console 侧没有「如何声明域名/路由」的指引链接，新手在 Deploy 卡会反复撞墙。

---

## 5. dokploy v0.30.6 对照矩阵

覆盖度：✅ 对等 / 🟡 部分 / ❌ 缺失 / ⛔ 定位差异（不视为缺口）。fleetly 定位：swarm 原生、单二进制轻量、CLI-first——这些前提下不做的东西标 ⛔。

| dokploy 模块 | fleetly Console | 评注 |
|---|---|---|
| Projects（=环境）卡片网格 + Add Service 六入口 + 批量启停删 | 🟡 | team/project 模型有了，聚合列表/详情卡都有（WIP）；**缺创建入口与批量操作**，项目页不是创建枢纽 |
| Application 详情 13 页签 | 🟡 | 7 页签且核心四件（Deployments/Logs/Env/Terminal）质量高；缺 Domains 绑定 UI、Preview Deployments、Volume Backups、Advanced（资源/网络/端口）、Schedules（fleetly 有 cron 但只读+手动触发） |
| Database 详情（Backups/Environment/Monitoring/Advanced） | 🟡 | 生命周期/凭据/备份链完整且更强（rotate/reveal/upgrade）；缺 settings 编辑、per-db monitoring |
| Templates 模板市场 | ❌ | 无（⛔ 可接受为暂缓，但建库模板 postgres/redis/mysql/mongo 已对齐） |
| Deployments 历史 + 回滚 | ✅ | 追踪/失败信封/字段级 diff/取消/回滚齐全（但见 P0-2：UI 部署本体是坏的） |
| Monitoring 全局页 + per-container | 🟡 | App 内 metrics 卡 + System/Metrics 模式管理有；无全局监控页（⛔ 部分可接受，App Overview 的曲线是有的） |
| Docker 管理（containers/images/volumes/networks） | ❌ | 完全无（swarm 原生平台反而更该有 volumes/networks 只读面） |
| Settings: Git providers / Registry / SSH keys | ❌ | fleetly 对应物 = GitKeys/webhook，无 UI（P1-8） |
| Settings: Notifications 12 渠道 + 事件订阅 | 🟡 | webhook/slack/email + patterns + 试发 + 投递台账，质量好；渠道数少（够用） |
| Settings: Certificates / DNS providers | ✅ | ACME 卡含通配 DNS-01 + 探针（dokploy 的 DNS zones 管理没有，属 ⛔） |
| Settings: S3 Destinations | ✅ | S3 设置 + 探针 + 备份台账 |
| Settings: Users & 权限矩阵 | 🟡 | 四档角色 + 项目覆写模型清晰；无 per-resource 细粒度权限矩阵（⛔ 可接受） |
| Audit Logs | ✅ | dokploy 是企业付费件，fleetly 开源自带且带留存管理（差异化优势） |
| Previews（PR 预览） | ❌ | 无，依赖 git 通道（P1-8 的下游） |
| Multi-organization + 切换器 | ✅ | team/project + 顶栏双切换器，形态不输 |
| Overview（跨项目 Services/Backups/Domains/Deployments） | 🟡 | Home 有统计+活动+关注卡；无 Backups/Domains 全局面 |
| Schedules（cron） | 🟡 | cron 服务运行台账 + 手动触发有；无「给应用加定时任务」的 UI |
| Bulk actions / 统一服务列表 | ❌ | 列表分散在 Apps/Databases 两页，无跨类型混合列表与批量操作 |

**量化**：18 个对照模块，✅4 / 🟡7 / ❌4 / ⛔3。若只数「有界面」的比例并不难看（11/18），难看的是**核心动词（部署）的 UI 成功率 0%**（P0-1+P0-2+P0-3 三雷叠加）。

---

## 6. IA 建议

1. **把「创建」收口到项目页**：ProjectDetailPage 的 Applications/Databases 卡加 CTA（Deploy compose→就地建 app；Create database→复用既有对话框）。这是消除 P0-1 最自然的落点，也 align dokploy「项目=创建枢纽」的心智。
2. **Deploy 卡语义修正**：支持「新名字 = 新应用」（在项目上下文中允许 name: 新名），或提供独立的 New application 对话框；同时修 A1 校验（解析 id 后与 spec.Name 比对，而不是比原始参数）。
3. **能力门统一建模**：`useTeamCapabilities` 纳入 `is_platform_admin`（资源面降为只读视图），并把「为什么按钮消失/为什么 403」显式化（banner 或 tooltip），与 `ownership.go` 的解析序（机具→平台管理员→覆写→团队角色）同构。
4. **导航分组**：Home / Workloads（Applications, Databases）/ Platform（Events, System）/ Administration（Admin, Audit），Teams+Projects 并入 Home 组或单列 Workspace 组；PAT 入 Settings 组。
5. **Tab 深链统一**：TeamPage 改 `?tab=`；ProjectDetail 头部链接带 tab 落点。
6. **详情页签重排**：Deployments 紧跟 Overview（dokploy 顺序），Domains 在 Env 之后（目前顺序无大碍，P0-2 修好前 Deployments 是死页签）。

---

## 7. 优先级 Backlog

| # | 项 | 级别 | 量 | 依赖端点就绪？ |
|---|---|---|---|---|
| 1 | 修 A1 校验：resolve 后按 app 名比对（或 Console 传名） | P0 | S | ✅ |
| 2 | 前端能力门建模 is_platform_admin（写按钮降级为只读提示） | P0 | S | ✅ |
| 3 | 项目页创建 CTA（部署即建 app + 建库对话框复用） | P0 | M | ✅ |
| 4 | AppsPage/详情壳 Danger Zone（deleteApp 已封装） | P1 | S | ✅ |
| 5 | 邀请链接感知：登录页开放受邀注册表单 | P1 | M | ✅ |
| 6 | 登录切换清 queryClient / 整树 re-mount | P1 | S | ✅ |
| 7 | 「未选团队」显式引导（写面隐藏时给提示） | P1 | S | ✅ |
| 8 | token 存储降权（内存/sessionStorage）+ Bearer 身份指示 | P1 | S | ✅ |
| 9 | Drift 面板（列表徽章 + Converge 按钮） | P1 | M | ✅（drift RPC） |
| 10 | Builds 台账页 | P1 | M | ✅ |
| 11 | GitKeys/webhook source 设置页 | P1 | M | ✅ |
| 12 | ULID 反解管线铺到 events/audit/placement/错误文本 | P2 | M | 部分（需投影） |
| 13 | TeamPage tab 进 URL + ProjectDetail 链接落点 + 行可点 | P2 | S | ✅ |
| 14 | AppEnv 删除确认框 | P2 | S | ✅ |
| 15 | triggerBackup/rotateJoinToken/updateAlertRule 编辑接 UI | P2 | S | ✅ |
| 16 | MoveApp/MoveDatabase、DB settings 编辑 | P2 | M | ✅ |
| 17 | ACME 零值时间戳、terminal status 占位、面包屑字面量、时间格式统一 | P2 | S | ✅ |
| 18 | 导航分组 + PAT 入口 + 图标去重 | P2 | S | ✅ |

---

## 8. 走查环境、限制与残留

1. **部署口径**：本机 `pnpm build`（tsc 零错）→ scp → 叠加 `/opt/fleetly/console`（os.DirFS 免重启），远端 curl 验证 200+gzip。回滚备份：`/tmp/console-backup-20260925-152918`。本机 8420 daemon 未运行（本地 dev 链路不可用，记为素材）。
2. **并行会话污染（重要）**：走查期间发现另一 ZCode 会话与本次共用同一 IAB 浏览器 profile（同一 cookie jar/localStorage）。DB tokens 表证据：`console-terminal-verify`（founder/mate 各一）、`review-deploy`（实落 mate 名下）等令牌在走查窗口内被并发创建。已剔除的受污染结论：ACME 403（外来 token 所致，非产品缺陷）、review-deploy PAT 归属错乱（同）、多账号后段的 UI 证据（改用 CLI+DB 交叉验证）。**建议：多会话并行做浏览器走查时必须用独立 profile。**
3. **工具怪癖**：IAB 的 Playwright click/scroll 在本环境持续超时，改用坐标点击（CUA）；滚动用 `window.scrollBy` evaluate 等效（披露：仅用于查看内容，未绕过任何交互）；登录页回车不提交（无 form submit-on-enter，记为 P2 候选）。
4. **staging 残留**（均为 review-\* 命名，无危害，可留作复现环境）：team `review` + project `review/review`（founder owner）、用户 review-user@fleetly.run（Review 队 admin，临时口令已展示过）、PAT `console-review`（founder，read+deploy，绑定 review/review）、PAT `review-deploy`（实际落在 mate 名下，建议由 mate 吊销）、两枚已吊销邀请。清理建议：用非平台管理员的 owner 会话（或 machine 令牌）删项目→删队；Admin 页禁用 review-user。
5. **未走查项**（受污染风险/时间取舍）：DatabaseDetailPage 的 Reveal/Rotate 实点（平台管理员必 403 已由代码+CLI 证实）、终端实开（并行会话正在测）、newbie viewer 视角（团队页只读形态已在代码层核实）。

---

## 9. 截图索引（D:/tmp/fleetly-console-review/）

01 登录页 · 03 Home · 04 应用列表（无创建入口） · 05 App Overview · 06-10 Logs/Env/Secrets/Domains/Terminal · 11-13 建队流 · 14-16 队设置/建项目 · 17-18 邀请创建 · 19 Projects 聚合页 · 20-22 ProjectDetailPage(WIP) 全链 · 24/24b PAT 页 · 25-26 建令牌 · 27-29 **部署 P0 实证** · 30-31 数据库列表/建库对话框 · 32-38 System 七页签 · 39 Events · 40 Admin · 41 Audit · 42-43 邀请匿名死路 · 44 Admin 建号 · 45-47 邀请吊销/重发 · 48 身份串号刷新后 · 49 E_INVITE_INVALID · 52 邀请接受成功 · 53 review-user PAT 表单
