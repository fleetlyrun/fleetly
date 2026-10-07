# fleetly Console 走查报告（F3.1 写面批，2026-10-06）

对象：staging 现役 Console（`161b62b-f31walk`，SSH 隧道 `127.0.0.1:19528` → manager `:9081`）。
走查口径：**HTTP 面与消费契约级**——本环境无浏览器后端（`agent.browsers.list()` 空，IAB/cdp 均不可用；browser-walkthrough skill 已加载并核实），UI 渲染层由 tsc 严格 + vitest 19 用例承载，浏览器级走查待有浏览器后端的环境补（F2.6 同款先后序：先 HTTP 对拍、后浏览器走查补档）。凭证 `console-walk-f31`（owner，走查后已吊销）。

## 换装

`826fd93-conswalk → 161b62b-f31console`（本批四 commit）→ `161b62b-f31walk`（W1 修复）。前置 Platform Backup `aadd7634` + 卷 tar（三受管库卷 + zot 卷 + 存量 torchwood-pg 旧卷）。00024 迁移随批前滚（goose → 24）。

**零扰动断言带注**：task 行数 22 服务中 19 逐位一致；3 个用户域服务滚动一次——**ADR-0048 引擎批（e2e20e6..8e8a925）首次上真机的一次性载体 spec 收敛**（P16 双别名等新渲染面 vs 旧代码物化的存量载体；mlbridge 为停服窗内应用自崩 exit 1 后自愈）。收敛后 2 分钟复测零再滚（STABLE-NO-FURTHER-ROLLS）；tw.dev 200、torchwood-pg 21 表锚不变、journal 无 goose 报错。

## 逐面判据

### HTTP 面（五项）—— 全部通过

index no-cache / hashed asset immutable / SPA fallback 200 html / 未知 `/v1/*` JSON 404 / 无凭证 401（统一信封 E_UNAUTHENTICATED）；登录验证面 whoami：好 token 200（token_name/role_name 可渲染）、坏 token 401 带建议文案。

### 蓝绿叙事（F3.1 旗舰面）—— 全部通过

走查项目 walk-f31 / app bgwalk，compose `deploy.strategy: blue-green`（whoami）：

| # | 判据 | 结果 | 证据 |
|---|---|---|---|
| ① | 第二笔部署 from_generation 落 REST 面 | ✅ | 响应 `"generation":"2"` + `"from_generation":"1"` + from_revision（首代行零值省略——不可见性同验） |
| ② | 双代窗内两代载体并存 | ✅ | observing 时点旧代 `9efc0102` 1/1 + 新代 `b6614b75` 1/1 并存；observe_deadline 在场 |
| ③ | 窗口收口旧代退役 | ✅ | succeeded 后唯一在役服务 = 新代；容器 Aliases 三形态 `web` / `web.bgwalk` / `web.g5`（{proc}/{proc}.{app}/{proc}.g{gen}） |
| ④ | Revision 策略目录 | ✅ | R1..R3 全带 `"process_strategies":[{"process":"web","strategy":"DEPLOY_STRATEGY_BLUE_GREEN"}]`（rolling 零值省略口径与契约一致） |
| ⑤ | 取消写面 | ✅ | 卡 L1 的部署（镜像 tag 不存在）`POST /v1/deployments/{id}/cancel` → cancelled；L1 卡因如实（拉取拒绝循环，旧代零扰动） |
| ⑥ | Route 端到端经蓝绿换代 | ✅ | walk.dev（http :80）200，whoami 主机名 = 收口代服务名 |

### 写面（UI 表单的同一 REST 调用）—— 全部通过

projects/apps 创建删除（项目有 App 拒 409 文案如实）/ secrets put+list+delete / configs put（版本化）/ shared-variables put（affected_apps 信封在场）/ routes create+delete / **uploads raw-tar POST**（10KiB 确定性 tar，重传 `"deduplicated":true` 内容寻址去重）/ databases create（redis pending→running 16s）+ backup trigger / tasks create+stop+delete / schedules create+trigger+delete / tokens create（secret 只显一次）+revoke / freeze set+lift / audit 过滤读面（database.* 动作可见）。错误信封渲染面：compose ports 整数形态被拒（`port entry must be a string like "8080"`——字段路径精确，UI ErrorNote 直接可用）。

## 发现（按处置）

- **W1·服务端中危（已同日修复）**：compose 声明 `ports` 不自动挂项目 default 网——image/upload 形态有 `portDeclNetworks`、compose 路径漏了；Route-facing 只兑现 404 半边（端口已知、Proxy 不可达 → 502）。修复 = ports 在场且 networks 未列 default 时补挂（`internal/spec/normalize.go`，spec_file 用户亲笔不改写）；同批单测 `TestNormalizeComposePortsAttachDefaultNetwork` 三锚（补挂/显式保留/零端口零挂网）；修复后 staging 复验 walk.dev 200。
- **O1·观察（API 面）**：`GET /v1/databases` 不带 project_id 回 **404 E_NOT_FOUND "project not found"**（而非 400 E_INVALID_ARGUMENT）——ListApps 是 400 形态，两读面口径不一致；Console 恒带 project_id 不受影响。归属 API 面小裁决，留后续批。
- **O2·观察（已知坑复证）**：manager 自打公网 IP 的 https 探针 000（manager 出站自环防火墙形态），本地 `-k` 200（LE staging CA 不受信是既知）；走查探针统一走 `--resolve 公网IP` 或 localhost+Host。
- **O3·观察（引擎批上真机）**：三个用户域服务在换装时一次性滚动（载体 spec 收敛），属 ADR-0048 引擎批首次真机部署的预期形态，但**升级零扰动断言的 e2e 矩阵覆盖不到**（夹具无旧代码物化的存量载体）——已在 runbook 记录；后续引擎批上真机应预期同类一次性收敛。

## 结束动作（已全部执行）

- walk-f31 项目级联清理（app→db→project 逐级删除 200；`fleetly-vol-walkredis` 卷残留手工清除——Database 级联删库不删卷，与 staging 残留清理先例同形态）；
- `console-walk-f31` token 吊销 + 已吊销复核；
- SSH 隧道关闭；staging 现役 = `161b62b-f31walk`（W1 修复在内）。

## 总判定

**PASS-with-notes**：判据面全绿；W1 同日修复带回归锚；O1/O3 记档归属后续批。

## 浏览器级补档（2026-10-07，走查对象 b4953ca-f36browse → 修复换装 6ff4f08-w1fix）

后端在场环境（ZCode IAB / Chromium 146，视口 1440×900）对 F3.1 全功能面补做浏览器级走查。SPA bundle `assets/index-CSOhUoLd.js`（修复换装后 `index-D58UhAS2.js`）；凭证 `console-walk-browser`（owner）+ SSE 活体专用 `console-walk-sse`（member），走查毕双双吊销（401 复核）。JS 报错监测（error+unhandledrejection 收集器）全程读数恒空。

### 读面（全部通过）

| 面 | 结果 | 活体锚 |
|---|---|---|
| 登录 | ✅ | 坏 token 401 态不白屏（whoami 诚实信封 + "may be revoked or mistyped" 提示）；好 token whoami → 身份栏 `console-walk-browser·owner` |
| 部署列表 | ✅ | torchwood 13 代全列（失败行带 health gate L1 / first boot job 具体原因）；未选项目引导态（F4 修复形态在场）；本地时区渲染 |
| 部署详情 | ✅ | gen13 行 processes 表带 strategy + **三别名 DNS 列**（`redis · redis.torchwood · redis.g13` 六进程）；row fields 零值 `—` |
| **双代窗叙事卡（旗舰活体）** | ✅ | compose blue-green（whoami）gen2 部署窗内实拍：releasing→observing 态转换 + `current generation g2 ← serving g1` + `observe window` **秒级倒计时**（1m52s→0m51s 实测推进）+ cancel 按钮活跃（收口后消失）+ from_generation/from_revision 字段 + 说明段（双代并存/DNS round-robin 提醒）；收口 succeeded 后卡转 `closing` 终局形态 |
| Resources 八 tab | ✅ | networks（default 网 + rebuild + peers 声明面）/routes（真路由 tw.dev + h2c）/volumes/secrets（torchwood 9 secrets）/configs/variables/databases（torchwood-pg 行带 backups/browse/backup/delete）/uploads——空 tab 均有诚实空态 + 各自建动作 |
| Tasks/Schedules | ✅ | 子 tab 各自项目输入 + 引导态/空态 |
| Events | ✅ | SSE following + 游标 seq；**实时推进活体**（CLI 铸 token → `token.created` 3s 内上顶，cursor 117854→117855） |
| Audit | ✅ | 过滤面（source/action/actor/resource）+ 本走查自身 deployment.transit 序列在案（releasing→observing→succeeded） |
| Settings | ✅ | identity + 12 token 史（历代走查 token 全列）+ freeze（无理由禁用态）+ Trigger platform backup 按钮面 |
| Quickstart 向导 | ✅ | **单表单全链活体**：project→app→network→deploy→route→wait 六步逐步呈现，wait 流 `deployment reached succeeded`；`walk-qs.dev.fleetly.run:80 → 200 "Welcome to nginx!"` 端到端 |
| 全局路由 | ✅ | hash 直达各路由 + 登出回登录页 + Sign out 后 401 态（localStorage 陈旧 token 的诚实错误面板） |

**Logs 页修复复验三锚（F2.6 F1/F2/F3 换装后生效确认，全部通过）**：①`text=redis&follow=1` 2.5s 内出 200 帧积压（服务端 300ec2d 生效）；②Stop 后按钮翻回 Start、fetch 计数恒 1 无重提交（f9d42f5 生效）；③不存在的 process + follow=0 → `0 frames · ended` + "Stream ended — no frames matched" 提示（f9d42f5 生效）；follow=1 空流保持 streaming 属诚实活流形态。

### 发现（本批新增，按处置）

- **W1·P0（当日修复 `6ff4f08`）**：Modal 壳层的 `method=dialog` 外层 form 包裹业务表单形成**嵌套 form**（非规范 HTML），真实浏览器中内层 submit 事件不冒泡出外层 form（Chromium 146 实测：捕获相到达 root、冒泡相截断于外层 form），React 委托 onSubmit 整体收不到——**Apps/Resources/Tasks/Settings/Templates/DeployForm 全部 Modal 表单在真实浏览器点击提交零动作**，且未 preventDefault 的默认提交带 `?` 整页跳转（首发现场实测复现）；jsdom 冒泡行为不同，走查前的组件测试全绿正是漏网原因。修法 = Modal 壳层去 form（div 承载）+ ✕ 改 `type=button`；测试同批（ui.test.tsx 四守卫）；**换装 6ff4f08-w1fix 后活体复验：同路径点击 New project → walk-verify 项目真创建 + 关窗 + 零跳转零报错**。
- **W3·P1（与 W1 同批修复 `6ff4f08`）**：dialog 的 `close`/`cancel` 事件不冒泡、React 19 委托面收不到——Escape 关窗后受控 open 态与原生 dialog 失同步，**再点同一入口无响应**（Templates grafana 详情实测死窗形态：dialog 内容在、open 恒 false、再点 show 无效）。修法 = useEffect 内 `addEventListener('close')` 直挂 dialog 节点，Escape/✕/背板三路关闭归一到 onClose；换装后活体复验：close 事件（真实浏览器 Escape 的规范事件）→ 态清 → 再开成功；✕ 关闭 → 再开成功。
- **W2·P2（CLI 契约面，挂账后续批）**：`fleetly templates instantiate --project <项目ID>` 不解析 ID——get-or-create 按名字语义把裸 ID 当新项目名静默建项目（本批实测：幽灵项目"01M49WV6C3…"，demo-site 落入其中）；而 `apps create --project` 只收 ID（名字报 E_NOT_FOUND）。两子命令 `--project` 语义相反，且 Console 项目下拉会如实渲染幽灵项目名（裸 ID 行）。已随走查清理；名字/ID 解析归一挂 API/CLI 面小裁决。
- **O·观察**：①部署详情 `observe_deadline` 原始 UTC ISO，与 created_at 本地化并存（F2.6 O1 同族跨字段不一致）；②蓝绿卡收口终局仍写 `← serving g1`（已收口时语义略误导，打磨项）；③Tasks 子 tab 切换不共享已填项目 ID；④App.tsx 注释"终端页不在导航"已过时（F3.2 后 Terminal 在导航内）；⑤走查环境注记：本 IAB 构建输入通道怪癖三件——locator click 超时 / rAF 挂起（xterm 不落屏，数据面经 WS 帧解码验证）/ 编程式 `dialog.close()` 不发 close 事件，均非产品缺陷（F2.6 双重曝光伪影同类先例）。

### F3.2 终端页（本批并入，浏览器级）

torchwood/torchwood/server 进程 + 缺省 `/bin/sh`：xterm 挂载 + 受理换票据 + WS 连接（banner `Connected`）全链在浏览器活体；**交互数据面经 WS 帧解码坐实**——meta 帧（instance/node_id）→ PTY 提示符回显 → `hostname` 输出真实容器名（`fleetly-default-01m3y8vk…-01m3ydmzacf-10ab04fc`，与 runbook 记录·十一 CLI 形态一致）→ `exit` 后 **exit code 0 透传**（React banner）+ Connect 重开面（新会话新票据形态）。xterm 视觉落屏受走查环境 rAF 挂起所限（数据已到 `term.write`，缓冲区在；非产品缺陷，同批 F2.6 伪影先例记档）。Process 目录由最新 Revision 的 process_strategies 渲染（server/worker/dispatcher/packer/redis/minio 六进程可选）。
