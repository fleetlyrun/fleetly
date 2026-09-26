# 三角色浏览器黑盒走查(第二轮)——2026-09-26

> **修复批(同日)**:本报告发现的问题已于当日按 mechanism-gap 方法修复并部署复验,见 §8 修复闭环。§3 的状态标注以此为准。

| 项 | 值 |
|---|---|
| 环境 | staging `https://console.dev.fleetly.run/`(fresh v0.3,TLS platform 模式) |
| 方法 | ZCode 内置浏览器(IAB)纯 GUI 黑盒走查,web-gui-tester 方法论;Playwright DOM 快照 + 截图双重观察;服务端 DB/审计日志交叉验证 |
| 角色 | 平台管理员 = founder(首用户);团队管理员 = walkadmin(Founder 队 admin + 自有队 owner);普通成员 = walkdev(Founder 队 developer + 自有队 owner)——两账号为本轮经邀请链新铸 |
| 上轮基线 | 5ffbeac(2026-09-26 三角色走查修复批)+ 3891db8/fe0ab75,均已部署 staging |
| 证据 | 截图内联于走查过程并持久化于 `C:\Users\1234\.zcode\cli\artifacts\sess_27073f12-...\`(IAB 工具落盘路径,见 §7);服务端审计 `/var/lib/fleetly/fleetly.db` audit_log/tokens 表 |

---

## 1. 总评

**三角色主链路全通,上一轮修复项全部确认生效,无白屏、无 500、无假按钮。** 修复后的 Console 在「功能正确性」维度已达到可日常使用的水平:部署链(创建→queued→running≈10s)、日志 Live/Search、终端 E2E、数据库托管链(reveal/备份/台账)、邀请链、审计互证全部真实闭环,且错误呈现几乎全部「诚实」(报错解释原因、没有假成功态)——这是多数自研 PaaS 做不到的。

对照主流 PaaS(dokploy/vercel/railway/render),本轮暴露的差距集中在**易用性打磨**而非功能缺失:

- **P1×1**:平台管理员的 PAT 页展示了**全平台所有令牌**(含其他用户的个人 PAT 与机具令牌)且可吊销,无属主列,页面文案仍称 "as you"——观感越权 + 误吊销风险(普通成员视角已验证只看自己,非漏洞,是设计/文案缺陷)。
- **P2×5**:git push 地址显示 `127.0.0.1` 不可用;System 页 Metrics 与 Alerts 两卡对 `metrics.mode` 的显示自相矛盾;应用详情内重部署要求全局项目上下文(应用自身项目明明已知);终端错误文案截断;登录页回车不提交(上轮挂账仍在)。
- **P3 若干**:ULID 裸显(上上轮 backlog #12,持续最痛的一类)、备份台账不自动刷新、`mem — B` 之类的未设值渲染、组件长文本横向溢出、版本号 "vdev"、audit `app.env_set` actor 记为 system。

**主流 PaaS 水平差距结论**:核心动词(部署/回滚/看日志/开终端/管数据库)的 UI 成功率本轮为 100%(上轮为 0%);剩余差距是「新用户冷启动引导(模板/示例 compose/docs 链接)」与「信息可读性(ULID)」两类,见 §5。

---

## 2. 上一轮修复项复核(全部生效 ✅)

| 修复项(5ffbeac/fe0ab75/3891db8) | 本轮实测 |
|---|---|
| DeployCard 传项目上下文(多团队未选→禁用+提示) | ✅ walkadmin 在项目页部署 walkapp 一次通过;项目过滤器=All projects 时 Deploy 按钮禁用且显示 "Select a team and project above to deploy." |
| 项目详情创建 CTA(Deploy application / Create database) | ✅ 团队 admin/developer 可见可用;平台管理员处替换为只读说明(正确) |
| create-app-dialog 名称注入反馈 | ✅ 显示 "The compose file has no top-level name — name: walkapp is prepended on submit." |
| 部署受理反馈(回列表+queued 提示带部署 id) | ✅ "Application "walkapp" queued for deployment (01M3DYDS93QK…). It appears below once created" |
| webhook/push remote 按业务名寻址(非 ULID) | ✅ `ssh://git@…/walkapp.git`、`/v1/apps/walkapp/webhooks/github` |
| DB 详情页角色门(viewer/developer 见说明卡) | ✅ developer 视角:Suspend/Rotate/Delete/Reveal/Back up now 全部不渲染,替换为 "Database lifecycle actions require the admin role in this project.";备份台账仍可读 |
| System 页写面 useIsPlatformAdmin 门 | ✅ 团队 admin 视角:Back up now / S3 配置卡显示 "Platform administrator required.";台账读面保留 |
| DB 详情写面按 canAdminResources 门 | ✅ 团队 admin 视角按钮齐全(reveal 成功、Back up now 受理成功) |
| 终端页固定高度/resize 接线 | ✅ 会话盒高度稳定,行列正确(命令回显无错位) |
| 401/登出中性提示 | ✅ 登出后登录页显示 "You have been signed out." |
| 邀请吊销两步确认 | ✅(吊销按钮在;本轮未实点破坏性路径) |

---

## 3. 新发现问题

### P1

**W2-1. 平台管理员的 PAT 页 = 全平台令牌总账(含他人个人令牌),无属主标注**
- 现象:founder(平台管理员)`/ui/pat` 列出 staging-ops、mate-pat、sv2、w3-verify、w5-*、alm2、console-* 等全部令牌(mate-pat/sv2 属 mate 名下,已用 DB tokens 表核实),每行带删除按钮。页面文案仍是 "PATs authenticate the CLI … **as you**",且无「属主」「类型(个人/机具)」列。
- 定界:walkdev(普通成员)同页只显示自己的令牌(空态)——跨用户可见性仅限平台管理员,非越权漏洞。
- 为什么仍是问题:①平台管理员误以为这是自己的令牌清单,误吊销他人机具令牌会打断对方 CI;②把「个人 PAT」与「机具令牌(user_id=NULL)」混排,无从区分;③与 "as you" 文案直接矛盾。
- 建议:平台管理员视图拆成两卡(My tokens / Machine tokens),行加 owner 列;或非本人令牌去掉删除钮只留查看。对照:dokploy 的 API tokens 严格 per-user。

### P2

**W2-2. Deploy triggers 卡的 git push 地址显示 `ssh://git@127.0.0.1:8424/<app>.git`**——远程用户复制后不可用(应显示可达主机名,如 manager 公网主机名或 base_domain)。walkapp 实测截图为证;服务端 git SSH 本身在 8424 真实监听,纯属提示地址来源(回环)问题。

**W2-3. System 页自相矛盾:`metrics.mode` 双卡显示不一致**——Metrics 卡:"metrics.mode: on · explicitly set · retention 14 days";同页 Alerts 卡:"default (unset) · metrics.mode: unset"。metrics 栈实际在役(三件 1/1)。两卡读取源/缓存不一致,至少一处是错的。

**W2-4. 应用详情页内重部署要求全局项目上下文**——项目过滤器为 "All projects" 时 Deploy 卡按钮禁用(提示在,见 §2)。但用户此时就在该应用详情页,应用所属项目已知,应默认带入该项目上下文,而不是让用户去顶栏先选项目(多一步、且报错形态是「按钮灰」而非「说明为什么灰」;说明文字有但离按钮远)。对照:dokploy 在应用页内重部署从不要求额外上下文。

**W2-5. 终端打开失败的错误文案截断**——scratch 镜像(traefik/whoami,无 shell)实测:"closed — code 400: execrelay: none of the whitelisted shells (/bin/bash, /bin/sh) are available in the target container (last probe: execrelay: probe exec start: Error response from daemon: OCI runtime exec failed: exec…" 在 "exec" 处截断。错误本身诚实且解释了原因(好),但 ①文案被截断;②UI 在明知镜像来源的情况下不前置提示「该容器可能无可用 shell」;③whitelisted shells 不可用时 Service 下拉仍可选。

**W2-6. 登录页回车不提交**(上轮 P2 候选仍在)——walkdev 登录时在密码框按 Enter 无动作,必须点按钮。主流 PaaS 登录页均支持 Enter 提交。

### P3

| # | 问题 | 证据 |
|---|---|---|
| W2-7 | **ULID 裸显多处**:Home 活动流(user:01M3…)、Events 页 subject、DB 详情/列表 Placement 节点 ID、Audit target 列 | 上上轮 backlog #12,至今未铺;对纯用户是不可读噪音 |
| W2-8 | 备份受理后台账不自动刷新:提示说 "appears in the ledger below",但表格不轮询,需手动刷新;服务端 audit 已确认 backup 实际完成(manual · verified) | pgshared Back up now 实测 |
| W2-9 | DB 详情未设值渲染生硬:"Limits: cpu — · mem — B"、"Backup plan: every —h · keep — · —:00 UTC" | 应显示 "not set / unlimited" |
| W2-10 | System→Components 的 notifications 长状态文本把页面撑出横向滚动条(1280 视口) | 状态文本应截断+展开 |
| W2-11 | 版本号显示 "vdev"(侧边栏/Home 控制面卡/CLI 提示同) | 正式版应为真实版本号 |
| W2-12 | audit `app.env_set` 的 actor 记为 `system` 而非操作者(同会话的 `api.DeploymentsService.Deploy` 正确记录 user:walkadmin) | 审计互证时发现,actor 归因不一致 |
| W2-13 | 统计卡加载瞬态显示 "—"(Home Components、System Nodes 首屏一闪) | 可接受,骨架屏更佳 |
| W2-14 | Autoscaling 策略格 "Loading…" 持续数秒后才出值 | 慢查询,非卡死 |
| W2-15 | 登录页无「注册」入口(仅受邀链接可注册);新用户自助注册不可发现 | 若注册窗关闭属设计内;开窗时也应显入口 |

---

## 4. 三角色实测矩阵(通过项)

### 平台管理员 founder
登录 ✅ · Home 全平台概览 ✅ · Administration 组(Admin/Audit)仅此角色可见 ✅ · 应用详情八页签(Overview/Deployments+diff/Builds/Logs Live+Search/Env/Secrets/Domains+Verify/Terminal)只读形态+逐卡说明 ✅ · 数据库详情只读+说明 ✅ · System 七页签(Components/Nodes+join 向导+HA 边界卡/Ingress+ACME/Metrics/Alerts/Notifications+SMTP/Storage+backups+S3)全部可写可控 ✅ · Admin 用户管理(建号/禁用/重置/授 admin)✅ · Audit(过滤器/留存管理/展开行)✅ · Events 实时流 ✅ · Teams「我的队+全平台队」双表 ✅ · 项目详情 Edit details ✅ · PAT/GitKeys 页可达 ✅ · 用户菜单含 PLATFORM ADMIN 徽章+多队角色 ✅

### 团队管理员 walkadmin(Founder 队 admin)
登录 ✅ · 默认上下文落在受邀队 ✅ · 项目页 Deploy application → 填名+compose → queued → **running 全链 ≈10s** ✅ · DeployCard 重部署第二版(alpine)→ releasing → observing → succeeded,历史表含 Cancel ✅ · Rollback 卡/Deploy triggers(业务名 URL)✅ · **终端 E2E**:Open terminal → connected(30m 硬限倒计时)→ echo/hostname/uname 回显正确 → Disconnect code 0 ✅;compose 注入 env(WALK_MARKER)在容器内生效 ✅ · scratch 镜像开终端诚实 400(W2-5)✅ · DB reveal(明文+Copy/Hide)✅ · Back up now(受理提示,服务端 audit 落账 backup_trigger+完成)✅ · 邀请创建(一次性链接模态框,audit 落账)✅ · 邀请台账(pending/accepted/吊销钮)✅ · 团队设置只读(admin 不能改成员角色)✅ · env 写入+按键明文读(admin)✅ · System 读面+写面 "Platform administrator required." ✅ · Admin/Audit 侧边栏隐藏 ✅

### 普通成员 walkdev(Founder 队 developer)
登录 ✅ · 应用列表/详情可见 ✅ · Deploy 卡可写(compose 空→按钮禁用属正常表单态)✅ · **env:写允许(Set 成功入 Pending),按键 Reveal 按钮不渲染**(服务端 403 兜底,UI 直接不给入口——优雅)✅ · **终端可用**(Open terminal 按钮,服务 burn 服务可选)✅ · **Danger Zone 不渲染**(deleteApp 属 admin scope)✅ · **DB 详情:生命周期按钮全部替换为角色说明文字**,备份台账可读 ✅ · **PAT 页只显示自己的令牌**(空态)+建令牌表单(scope 说明+项目绑定)✅ · 团队设置:无 Invites 页签、成员表只读 ✅ · Projects 页建项目表单(个人队 owner 可用)✅

### 横向
空态全部有下一步指引(Builds "use fleetly build"、Secrets "declare under compose secrets:"、GitKeys "add one below, then push to deploy")✅ · 时间格式统一(相对+title 绝对)✅ · 面包屑层级正确 ✅ · 行内健康原因+view events 链接 ✅ · 危险动作普遍两步/两拍(deploy triggers 整体替换语义披露、restore 语义披露、reveal 审计披露)✅

---

## 5. 主流 PaaS 对照补充(在上轮 dokploy 矩阵基础上)

本轮验证了上轮矩阵中修复后的「核心动词成功率」:部署/回滚入口、终端、DB 托管、通知/告警/ACME/S3 全部 UI 可达可用。剩余体验差距按影响排序:

1. **冷启动引导缺失**(对照 dokploy Templates/vercel 示例):无模板市场、无示例 compose、部署对话框只有占位符。建议:建项目后的空态卡直接给 2~3 个可一键部署的样例(whoami/nginx + postgres),消除「compose 写什么」的门槛。
2. **信息可读性**(ULID,见 W2-7):观测/审计页对非开发者用户接近不可读。
3. **创建入口**:Applications 列表页无创建按钮(必须经项目页)。dokploy 在列表页有显眼 Add;建议列表空态/页头加 CTA 跳对应项目。
4. **文档/帮助**:整个 Console 无一条 docs 链接(CLI 命令提示是亮点,但缺 "Learn more")。
5. **批量操作/跨类型列表**(上轮 ❌ 项,维持)。

---

## 6. 环境准备披露(与测试行为区分)

- 账号铸造:以 founder 会话经 REST 邀请链创建 walkadmin(Founder 队 admin)、walkdev(Founder 队 developer),密码为本轮设置;founder 沿用既有账号。见 `/tmp/walk2-prep.sh`。
- 浏览器:打开前发现 IAB 残留**上一轮走查的 review-user 会话**(localStorage token 污染,与记忆中警告一致),经 GUI 登出清除后开始;三角色均为 GUI 登录,无任何 token 注入。
- 测试产生的数据:应用 walkapp(部署 2 次:whoami 失败于终端探测、alpine 在役)、stressapp 的 env 键 WALK_ENV_PROBE、邀请 newmember@fleetly.run(未注册)、pgshared 手动备份 1 次。均为非破坏性,可留作复现。
- 登出残留:walkdev 登录态留在 IAB 标签页(已停用价值,可登出)。
- 工具怪癖(非产品问题):IAB Playwright click 稳定超时改用坐标点击;页面 fullPage 截图为拼贴伪影;rAF 本轮未挂起(xterm 渲染正常);快照偶发「页签 active 但内容未切」为点击与重渲染竞态,重点击即恢复。

## 7. 截图索引(节选,均为 IAB 落盘绝对路径)

| 测试点 | 路径(artifacts/sess_27073f12-…/) |
|---|---|
| 上轮残留 review-user 会话(污染证据) | call_9e5f42b4…png |
| 登录页(founder 填单) | call_7b0b0786…png |
| founder Home | call_27f2d54d…png |
| 应用列表(健康原因内联) | call_d8722033…png |
| App Overview | call_a3c6c6b2…png |
| Deployments+只读说明 | call_da61b286…png |
| Logs Live 跟流 | call_375628a5…png |
| Logs Search 表单 | call_dc7d2358…png |
| Terminal(平台管理员不可用说明) | call_65a3777a…png |
| 数据库列表 | call_f6fb4a6e…png |
| System Components | call_916a3409…png |
| PAT 页跨用户令牌(W2-1 证据) | call_8bb1a18c…png |
| 项目页创建 CTA(walkadmin) | call_b54bb356…png |
| create-app-dialog(W2 前注入提示) | call_b3e57ab9…png |
| queued 受理提示 | call_67a38c8d…png |
| Deploy 卡禁用态(W2-4 相关) | call_39c4a9f7…png |
| 终端会话(/ # 提示符) | call_7108f068…png |
| 终端命令回显(MARKER 正确) | call_f6b20503…png |
| scratch 镜像终端 400 截断文案 | call_c5211563…png |
| pgshared admin 视角(Reveal 后) | call_3103167a…png |
| env 写入表单 | call_b4bd2898…png |
| walkdev 数据库列表(无创建钮) | call_fab2b71e…png |

完整路径前缀:`C:\Users\1234\.zcode\cli\artifacts\sess_27073f12-f1f4-46d5-a266-7ebf35dfd9e9\`。

---

## 8. 修复闭环(同日 mechanism-gap 修复批,已部署 staging 并真机复验)

方法论:每项先还原逃逸路径(问题途经哪些本可拦截的层),判定个案/一类,再在最早可拦截的层补守卫;修复以「下一个同类问题在哪层被拦」为验收。

### 8.1 W2-1(平台管理员 PAT 页混排全平台令牌)——P1,已修 ✅

- **逃逸路径**:ListTokens 票面裁决「平台管理员=全部」(服务端语义正确)→ PatPage 契约注释写明「只管自己的 PAT」但实现照单全收 RPC 响应 → 类型层无属主收敛、测试 mock 无他人令牌行 → 每层都放行。**一类问题**:「服务端多视图端点 + 单视图消费页」的口径错位,换个端点还会再犯。
- **修复(消费侧收敛 + 正确落点补建)**:①PatPage 按 `me().user.id` 过滤 `t.user_id`(Me 未就绪不渲染任何行——宁可空也不短暂裸显他人令牌),补「本页只列你自己的令牌;平台管理员在 Administration → Platform tokens 管理机具令牌」说明行;②Admin 页新增 Platform tokens 卡:属主列(email 反解/machine token 徽章)+吊销——保留票面裁决的平台级可见性,给它正确的呈现位。
- **守卫(下一同类问题在哪被拦)**:PatPage.test mock 加他人令牌行,断言「不渲染 not-mine」;AdminPage.test 断言 machine 徽章与属主 email。同类错位再发生时,消费页测试的属主断言即红。

### 8.2 W2-2(git push 提示地址 127.0.0.1)——P2,已修 ✅

- **逃逸路径**:`gitEndpointForHint` 对通配监听硬回落 127.0.0.1(单机自测合理)→ 服务端无法自行得知公网主机名是本质约束,但 `base_domain` 已在配置里却未被引用 → 提示面无测试覆盖远程可用性。**个案**(单一拼装点)。
- **修复(边界层数据流)**:解析链 `git.public_endpoint` 显式配置 > `base_domain` 域名 > 监听地址主机位(回环兜底保留);config-example.yaml 补键位文档。
- **守卫**:`TestGitEndpointForHint` 八场景表驱动钉死解析链优先级。staging 复验:`git_remote_hint` = `ssh://git@dev.fleetly.run:8424/walkapp.git`。

### 8.3 W2-3(System 页 metrics.mode 两卡矛盾)——P2,已修 ✅

- **逃逸路径定性反转**:两端点服务端数据一致(DB/REST 复核 metrics_mode=on),矛盾是**加载态说谎**——status 查询未返回时卡体渲染 `alerts.mode: `(空)+ "default (unset) · metrics.mode: unset" 文案,把「尚未加载」呈现成「确认关闭」的假事实。**一类问题**:「`data?.x ?? 默认值` 渲染在 isLoading 分支之外」,Metrics/Alerts 两卡同族。
- **修复(渲染层守卫)**:两卡的 CardContent 增加 `!status.isSuccess` 中性占位分支("Loading alerting/metrics status…",带独立 testid),事实文案只在数据就绪后渲染。
- **守卫**:占位 testid(`metrics-status-loading`/`alerting-status-loading`)可测;staging 复验两卡显示一致(on/on)。

### 8.4 W2-4(应用详情内重部署要求全局项目上下文)——P2,已修 ✅

- **逃逸路径**:DeployCard 只读 `useProjectContext()`(顶栏全局态)→ 应用归属其实已在详情壳的 GetApp 缓存里(team_slug/project_slug,W2-S3 起投影)→ 隐式全局状态依赖遮蔽了本地可用数据。**一类**:「详情页动作卡依赖全局上下文而不看资源自身归属」。
- **修复(数据流层)**:DeployCard 消费 `["app", name]` 共享缓存的应用归属,`effectiveProjectRef = 应用自身归属 || 顶栏上下文`;归属未达时保持旧门(多团队禁用+指路)。
- **守卫**:DeployCard 测试三态——归属可解析(载荷=应用归属,即使顶栏未选)/归属未达(禁用+新指路文案)/单团队回归不变。

### 8.5 W2-5(终端错误文案截断)——P2,已修 ✅

- **逃逸路径**:`shortErr` 头部截 200 字节——docker 错误的诊断价值在尾部("stat /bin/bash: no such file or directory"),头部是样板前缀。**个案**(单一 helper)。
- **修复**:截断改保尾(`"…" + msg[-197:]`,总预算 200 不变),换行折叠保单行。
- **守卫**:`TestShortErrKeepsTail`(尾保留/省略号标注/长度预算/换行折叠/短文不变五断言)。

### 8.6 W2-12(env_set 审计 actor=system)——P3,已修 ✅

- **逃逸路径**:state 层 `SetAppEnv` 审计 actor 硬编码 "system" → api 层(唯一用户路径调用方)无署名通道。**一类**:「审计署名硬编码在 state 层」——同文件其它写法都是 actor 显式传参,唯独此点遗漏。
- **修复(签名约束=让错误状态不可表示)**:`SetAppEnv` 增加 `actor` 必传参数,空 actor 构造错误拒写;api 层按 principal 署名(用户会话 `user:<id>`/机具 `human`);系统物化路径(rotate/dbinject)显式传 `system`。
- **守卫**:`TestSetEnvAuditActorAttribution`(用户路径署名/机具路径 human/空 actor 拒绝)+ 17 处测试调用点机械迁移。staging 复验:walkadmin 写 env → audit actor=`user:01M3DX76…`。

### 8.7 W2-7(ULID 裸显)——P3,Console 半步已修 ✅

- **定性**:backlog #12(服务端投影补名称)是完整解;本次在展示层补齐 Console 半步——**单一出口** `lib/subject.ts`(subjectLabel/nodeLabel)+ `use-subject-resolver` hook(聚合列表缓存,同 key 去重不加请求),接线 Home 活动流/Events/审计 target/库列表与详情 placement 五处。规则:`kind:ref` 命中缓存出业务名;反解不到且 ref 是 26 字符平台 ID → 只出 kind(ID 无信息量,事件名+时刻可定位);可读 ref 原样。过滤语义不变(仍按原始值匹配)。
- **守卫**:subject.test.ts 12 断言(含空 lookup/未知 kind 降级/裸 n_ 形态)。staging 复验:`app:stressapp` 出业务名、`deployment:01M3…` 降级为 `deployment`、placement 显示 `fleetly-dev`。

### 8.8 其余 P3 处置

- **W2-9 未设值渲染**:已修——Limits 空显示 "not set (engine defaults)"、Backup plan 空显示 "not set (platform default: daily 03:00 UTC, keep 7)",复验 ✅。
- **W2-10 组件长文本溢出**:已修——健康卡网格项 `min-w-0` + 错误文本 `flex-1 truncate`(title 悬浮全文)。
- **W2-11 版本 "vdev"**:维持——构建注入 `-X main.version` 只在 release 流,dev 构建显示 vdev 是事实;不修。
- **W2-6 登录回车/W2-8 备份台账刷新/W2-15 注册入口:走查误报**——登录页本就有原生 form submit(Enter 应提交,复验未见异常,疑为 IAB 键事件伪影);备份台账本就有 10s 轮询(走查时查早了);注册入口存在且按注册窗门控(staging 窗关闭故隐藏,设计内)。均已从问题清单划去。

### 8.9 验证与部署

- go 31 包全绿(含 3 个新回归测试)+ vet 净;console 319 测全绿(基线 313,净增 6)+ typecheck/lint/build 净。
- staging 部署:linux/amd64 二进制替换(/opt/fleetly/bin/fleetlyd,备份 .bak-20260926-w2)+ console dist 叠加(备份 /tmp/console-backup-20260926-w2)+ 重启,daemon active、REST 200、新 bundle index-DAjdTZUz.js。
- 真机复验(上表 ✅ 项):PAT 收敛(2/12)、Platform tokens 台账、git remote hint(dev.fleetly.run)、Events/Home 反解、placement 主机名、Metrics/Alerts 一致、DB 未设值渲染、env actor 审计。
- staging 新增残留:stressapp 的 W2_ACTOR_PROBE env 键(可删)。
