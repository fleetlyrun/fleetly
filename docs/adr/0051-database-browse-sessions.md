# ADR-0051: 数据浏览器——按需 Browse 会话、Launcher Ticket、方言注入与 ForwardAuth 门禁

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-07 | 选型真源 docs/research/2026-10-01-db-browser-selection.md（§8.1 终选 + §6 形态 B + §7 安全要求）、F3.6（checklist）、ADR-0029（Database 面/凭证单真源）、ADR-0044（Console 消费面纪律）、ADR-0045（digest 钉定纪律）、ADR-0049（票据泛化 purpose+payload/会话域先例）、ADR-0014（材料分发敏感度）、ADR-0021（钉版口径）、ADR-0007（词汇冻结） |

## 背景

F3.6 给 Database 配数据浏览器：四件按需实例（pgweb/redis-commander/Adminer 6.1.1/
Mongoku，选型报告 §8.1 终选）+ launcher ticket + 默认只读。选型报告已裁接入形态
（形态 B 按需实例、Proxy 门禁、服务端凭据注入）与安全要求（§7），本 ADR 落
实施留白。**全部关键方言面经本地 docker 实证（2026-10-07，镜像与行为快照）**，
实证记录进决策 3 的表格——这是本 ADR 与纯文档裁定的差别：不是"报告说可行"，
是"跑通过才写进来"。

## 决策

### 1. 形态：engine 第四类会话域（browse 会话）+ 持久回收台账

- **Browse 会话不是资源行**（exec 同款语义：不可列表回读、无独立读面；受理
  回显即全部读面），但**回收台账持久化**（`browse_sessions` 表，迁移 00027）：
  browse 载体是外部活体（容器），daemon 重启后会话注册表丢失而载体仍在跑——
  行是重启后重注册与到点回收的锚（Task run 行持久化的同一理由；exec 会话
  无此需求是因为会话本体就是活连接，随 daemon 消亡）。
- **NamespaceRef 增第五轴 `.Browse`**（互斥分支 +1）：label
  `fleetly.ns.browse=<session id>`、载体 `fleetly-browse-<session id>`、
  Addressing 铸名 `browse-<session id>`。与 Database 轴分立的硬理由：Runtime
  Ensure 按命名空间收敛整集，browse 载体若落在 Database 命名空间会被
  databaseLoop 的 Ensure 当多余载体拆除；每会话独立命名空间即每会话独立收敛
  单元。否决复用 Task 轴（browse 无 Task 行，标签语义撒谎）与 App 轴（同）。
- **生命周期**：`BrowseDatabase` 受理（四件一拍：browse 行 + 
  `database.browser_opened` 事件 + `database.browse` 审计，受理事务内）→ 铸
  Launcher Ticket（决策 2）→ browseLoop（Kick 即时）投影浏览器 Workload：
  挂 Database 所在 Project 全部活跃网（`db-<id>` 可达）、材料经
  Materials.SecretFiles（0444，非 root 工具用户可读）、Restart always（交互
  会话崩溃自愈）。
- **回收**：硬 TTL 30min（受理起算）+ 空闲 10min（entry/authorize 接触即续
  活——门禁流量就是活性的真实信号）→ `Runtime.Remove` + 删行。daemon 重启后
  注册表从行恢复（grant 重铸——旧 cookie 全部失效，用户重开会话；诚实行为，
  不做 cookie 迁移）。到点未回收的行由 browseLoop 步进收敛。
- **quota**：per-Team 并发 browse 会话上限 4（`E_QUOTA_EXCEEDED`；exec 8 的
  浏览器版——浏览器载体是常驻容器，比 exec 会话更重）。
- **freeze 豁免**（exec 同语义注记）：browse 不变更资源状态（诊断面恰在冻结
  窗最需要）；idem 不入（会话型非幂等资源，CreateExecSession 同款）。

### 2. Launcher Ticket = ADR-0049 票据泛化的第三用途

- purpose `"browse"`、payload = 会话 ID、**TTL 120s**（冷启动余量：受理后实例
  拉起 + 用户点击的窗口；exec 60s 不够浏览器首次拉镜像）、单用途（兑换即烧）。
- **兑换链**（两段式，全部本地 traefik v3.6 实证）：
  1. Console/CLI 拿到 `url = https://browse-<sid>.<suffix>/v1/browse/entry?session=&ticket=`；
  2. 该路径命中**免门禁路由**（决策 5 的第一条 ephemeral Route，PathPrefix
     `/v1/browse/entry` → gateway）：烧票（单用途）→ 铸 host-only cookie
     `flt_browse=<sid>.<grant>`（HttpOnly、Path=/、MaxAge=会话剩余、无 Domain
     属性——不跨子域泄漏）→ 302 `/`；
  3. 后续每请求经**门禁路由**的 ForwardAuth 中间件（→ gateway
     `/v1/browse/authorize`）校验 cookie 的 grant（注册表内常量时间比对）+
     续活。
- **ForwardAuth 行为实证**（traefik v3.6 + 容器化双后端，2026-10-07）：原始
  Cookie 头透传给 auth 服务 ✓；auth 2xx 放行到后端 ✓；auth 401/302 时状态行、
  响应体与 Set-Cookie **原样透传给客户端** ✓（401 兜底与 302 流都可用——本
  设计只依赖 2xx/401，302 透传记档备用）。
- 票据存储复用 `eventTicketStore`（purpose+payload 泛化面，TTL 参数化
  only-add）；铸造在 api 层（响应携带 ticket + expires_in，exec 响应同形），
  兑换在 assembly 原生入口（`Services.RedeemBrowseTicket`，exec 的
  `RedeemExecTicket` 同款挂法）。

### 3. 四件浏览器方言面（`internal/engine/dbbrowser`，dbtemplate 同款纪律）

per-browser adapter（接口 `Browser`）+ digest 钉定常量住 adapter 文件 +
TestBrowserDigestsPinned 门禁（形态/自洽/全值域非空，ADR-0045 同款）。
Adminer 的 router.php 是平台 vendor 的 ~40 行 PHP（go:embed，Apache-2.0/GPL-2.0
双许可镜像生态中的自有胶水——不改上游代码）。

| | pgweb 0.17.0 | redis-commander 0.9.1 | Adminer 6.1.1 | Mongoku 2.11.3 |
|---|---|---|---|---|
| 方言 | postgres/pgvector | redis | mysql | mongo |
| 镜像 | `sosedoff/pgweb:0.17.0` | `ghcr.io/joeferner/redis-commander:0.9.1` | `adminer:6.1.1` | `huggingface/mongoku:2.11.3` |
| 凭证注入 | argv `--url`（**无密码**）+ `--passfile`（pgpass 秘密文件） | env `REDIS_HOSTS=label:host:port:dbIndex:password` | 秘密文件 db-conn.json + vendored router.php（php -S） | env `MONGOKU_DEFAULT_HOST=mongodb://user:pw@host:port` |
| 只读 | `--readonly` + **URL options=`-c default_transaction_read_only=on`** | `READ_ONLY=true`（API 层 403 中间件 + CLI 白名单） | **无方言** | `MONGOKU_READ_ONLY_MODE=true`（写端点门） |
| 执法层级 | **session（服务端）** | tool（工具层） | none | tool（工具层） |
| XFO/CSP | 无（iframe 可） | 无（iframe 可） | `X-Frame-Options: deny` + CSP（不可 iframe） | 无（iframe 可） |
| 端口 | 8081（`--listen`） | 8081 | 8080（php -S） | `MONGOKU_SERVER_PORT` 指定 |

全部实证记录（2026-10-07 本地 docker，镜像 digest 见决策 4）：

- **pgweb 服务端只读**：带 options 的 URL 连入后 `SHOW default_transaction_read_only`
  返回 `on`——绕过 pgweb 关键词过滤的写语句（DO 块）由 **postgres 本身**拒绝
  （会话级 server-enforced，不是 UI 摆设）；passfile 权限 0444 满足非 root
  工具用户（swarm secret 载体缺省 0444，translate.go 既有注释）。
- **Adminer 零输入登录全链**：官方镜像 + 平台 router.php——GET `/` 仿真一次
  登录 POST（`verifyLoginToken()` 桥免 CSRF，Adminer 6.1.0+ 官方面向外部认证
  的钩子）+ 会话 token 预铸 → 302 `?server=&username=&db=`（URL 只有非密参数）
  → 200 已登录库视图（`Database: fleetly - ... - Adminer`）。凭证只存在于
  php 进程内存（来自秘密文件），不进 env/argv/spec/URL。**边界**：会话密码
  进 adminer 的 PHP session（服务端文件，容器内）——与密码在工具进程内存
  同面。
- **redis-commander 只读 = 工具层**：`checkReadOnlyMode` 中间件挂在全部变更
  路由（源码核对）+ `/exec` CLI 白名单 `isReadOnlyCommand`——工具 HTTP API
  面整体 403/拒绝写命令，非仅 UI 隐藏；但仍非 redis 服务端执法。
- **Mongoku 只读 = 工具层**：`MONGOKU_READ_ONLY_MODE === "true"` 门写端点
  （构建产物源码核对）。
- **iframe PoC（选型报告 §8 未证实项收口）**：redis-commander 与 Mongoku
  实测**无 XFO/CSP**（连同 pgweb 共三件 iframe 可用）；Adminer `deny` + CSP
  （`page_headers()` 硬编码，headers() 钩子只能追加不能撤——不可 iframe）。

### 4. digest 钉定（ADR-0045 同纪律）

`Image()` 恒为 `tag@sha256:<index digest>` 双段（钉 OCI index / manifest-list
digest，registry API `Docker-Content-Digest` 原值；2026-10-07 核对）：

- pgweb `sosedoff/pgweb:0.17.0` → `sha256:a5256d416e2e8b92d69a4459058e3eca33a9f075d8325491644411d0bc3bd70b`
- redis-commander `ghcr.io/joeferner/redis-commander:0.9.1` → `sha256:2c2404630820613c7e540ec26a8d1dd24b671d1a80373c3dfad287c7a1374ad1`
- Adminer `adminer:6.1.1` → `sha256:74f29c416e148b98305e84446a18db7cd2c1038264dec464359d36774ef080ce`
- Mongoku `huggingface/mongoku:2.11.3` → `sha256:99486754b32cabd2d9c3d98318cccfdccdfb6822976f23a066d41c77d1b219c7`

执法 = 引用面冻结（消费点单源 `Browser.Image()`）+ 测试门禁（形态合法 +
Image/ImageDigest 自洽 + 全值域非空）；**e2e/CI 预拉通道只暖层不暖 index 的
既有坑适用**（digest 引用在 dind 内解析需 registry 可达一次）。bump 射程
约束同 ADR-0045 决策 4（同版本契约内 patch 吸收；major 是新批）。

### 5. 接入与门禁面

- **API**：DatabasesService 新增 `BrowseDatabase`
  （`POST /v1/databases/{database_id}/browse`，静态 scope `databases:READ`；
  响应 session_id/url/ticket/expires_in/browser/read_only/enforcement）。
  动态提权在服务内：`read_write=true` 或 mysql 方言（无只读执法）时要求
  `databases:write`——静态注解只能表达最低门，写档是运行时校验。
- **每会话两条 ephemeral Route**（合并进 `publishRoutes` 发布集，**非 route
  行**——行是用户资源，会话路由是平台内置拓扑）：
  1. `{Host: browse-<sid>.<suffix>, Path: /v1/browse/entry, BackendAddr:
     <gateway>, 无 Auth}`——票据兑换入口（规则更长优先级更高）；
  2. `{Host 同上, BackendAddr: <浏览器后端>, Auth: ForwardAuth(<gateway>
     /v1/browse/authorize)}`。
  `capability.Route` 增 `Auth *RouteAuth`（只增字段）；受管 traefik 动态配置
  增 middlewares 面（forwardAuth.address，渲染 + 预检）。后端经
  `Runtime.Addresses` 解析（browse 期望集，App 路由同机制）；解析不到跳过
  该拍（冷启动秒级 404 窗口，诚实）。
- **config 只增**：`browse.host_suffix`（browse-<sid>. 的后缀域；**空 = 面
  停用**，受理拒 `E_BROWSE_DISABLED`——收窄是显式动作，升级零扰动）、
  `browse.gateway_url`（容器可达的 gateway 基址，如 `http://10.124.0.3:9081`
  ——traefik configEndpoint 同文化）、`browse.tls`（`none`|`auto`，缺省
  `none`；auto 走既有 LE per-host SAN）。
- **Console**：Resources 页 databases tab 行动作 "browse" → `BrowseDatabase`
  （只读档）→ `window.open(url)` 新窗口。**iframe 嵌入不进 v1**：Adminer 不可
  iframe（决策 3），四件工具 UI 异构（高度/路径适配成本），统一新窗口是可
  预期形态；三件可行的 PoC 证据在册，嵌入挂账按需启用（届时只动 Console，
  服务端零改）。
- **CLI**：`fleetly databases browse <id> [--write]`——打印 URL（不自动开
  浏览器：无 webbrowser 先例、CLI 面以脚本消费为主）；`--json` 双形态 golden
  钉死（ticket/ULID 进 normalize 掩码）。

### 6. 只读口径（诚实分层，回显执法层级）

`enforcement` 值域 `session | tool | none`：PG 家族 = session（postgres 会话
级只读，服务端执法）；redis/mongo = tool（工具 HTTP API 层门）；mysql =
none（Adminer 无只读方言）。**scope 门禁按层级收**：

- 只读 browse 需 `databases:read`——**mysql 例外**：none 层级下读权用户会
  拿到可写控制台（Adminer 连的是库的全权用户），诚实收窄为需
  `databases:write`，直到服务端只读角色铸造落地（挂账）。
- 写档（`read_write=true`）一律需 `databases:write`。
- 选型报告 §7.1 的"双保险"（工具开关 + 平台铸造只读 DB 角色）本批兑现前半
  +PG 家族的真执法（URL options 就是服务端执法）；角色铸造是引擎方言 ×4 的
  新面，挂账后续批。

### 7. 词汇（入册 CONTEXT.md，ADR-0007 程序）

- **Browse Session**：进入 Database 数据面的托管浏览器按需会话（受理铸造、
  TTL 回收、非资源行）。_Avoid_: db console, data explorer, admin panel。
- **Launcher Ticket**：Browse 会话的一次性进入票据（短 TTL、单用途、绑会话；
  兑换铸 cookie 会话凭证）。_Avoid_: launch token（与 Token 词条撞）， magic
  link, access url。

## 后果

- NamespaceRef 第五轴 + capability.Route.Auth 是 IR 只增面：fakes/守卫同批
  扩展；swarm translate 增互斥分支。
- browse_sessions 表与 browseLoop：新一条收敛环（Kick 通道 + 签名短路同款）；
  载体镜像不在受管域（用户域 Workload——digest 钉定在 dbbrowser 常量）。
- redis-commander/Mongoku 的连接串进载体 env（方言边界：工具只吃 env；平台
  侧密码仍不出 Secret 单真源——铸造期解封注入，但落在 inspect 可见面，比
  DB Workload 的纯文件面弱一档，记档不粉饰）。
- 受管 traefik 动态配置新增 middlewares——validateDynamicConfig 预检面同步
  扩（地址 http(s) 形态断言）。
- staging 需操作者置三个 config 字段（runbook 操作序随批）；不置 = 面停用
  （`E_BROWSE_DISABLED` 精确拒绝，不静默降级）。
- Console 新窗口消费 browse URL——同源/跨源依赖部署形态（staging console 与
  browse 同在 *.dev.fleetly.run 下即同站；cookie 是 host-only 无跨站面）。

## 验收锚

- [x] dbbrowser 四 adapter + digest 全钉（tag@sha256 形态/自洽/全值域——
  TestBrowserDigestsPinned 门禁；router.php vendored）
  （internal/engine/dbbrowser/{dbbrowser,pgweb,rediscommander,adminer,mongoku}.go
  + dbbrowser_test.go TestBrowserDigestsPinned/TestAdminerRouterPHPPinned/
  TestRegistryCoversDbtemplateEngines/TestRenderingsAreClean，2026-10-07）
- [x] NamespaceRef.Browse 轴：swarm 标签/载体名/选择器分支 + fakes
  （internal/capability/runtime.go NamespaceRef（第五轴 + String 分支）；
  internal/providers/swarm/translate.go labelBrowse/browseNamePrefix 与
  workloadServiceName/workloadLabels/nsSelector 三分支；e2e 实证载体名
  fleetly-browse-<lower(sid)>）
- [x] browse 会话域：受理四件一拍 + quota + 硬/空闲 TTL 回收（fake clock 引擎
  测试）+ 重启行恢复（grant 重铸）
  （internal/engine/browse.go RegisterBrowseSession/browseStep/
  teardownBrowseSession + internal/state/browse/repo.go + 迁移 00027；
  internal/engine/browse_test.go 六件：收敛投影/双路由/grant 生命周期/
  硬 TTL/空闲续活与回收/重启恢复）
- [x] Launcher Ticket：120s 单用途（entry 烧票；二次兑换 401——apitest）
  （internal/api/fleetlygrpc/databases.go BrowseDatabase:425 + eventtickets.go
  issueWithTTL（purpose browse）+ internal/assembly/gateway_browse.go
  serveBrowseEntry:82；apitest/browse_test.go TestBrowseEntryTicketAndCookie）
- [x] ForwardAuth 门禁：无 cookie 401 / 有效 cookie 放行 / grant 失效 401
  （traefik 真链 e2e）
  （internal/assembly/gateway_browse.go serveBrowseAuthorize:113 +
  internal/providers/traefik/config.go middlewares 渲染；e2e/dind-browse.sh
  无 cookie 401 + cookie 放行断言）
- [x] 只读分层回显 + scope 动态门（mysql/write 档 PermissionDenied——apitest）
  （databases.go BrowseDatabase 动态 HasScope 校验 + internal/authn
  Identity.HasScope；apitest TestBrowseWriteScopeGate/TestBrowseMysqlRequiresWriteForReadOnly）
- [x] publishRoutes 合并 ephemeral 双路由（entry 免门禁 + 工具路由带
  ForwardAuth）；traefik middlewares 渲染 + 预检 + golden
  （internal/engine/managed.go publishRoutes（browseRoutesFingerprint 并入
  签名）+ internal/engine/browse.go browseCapabilityRoutes:530 +
  internal/capability/routevalidate.go ValidateRouteAuthAddress；
  traefik testdata/dynamic-config.json 增 browse 双路由 golden）
- [x] CLI `databases browse` golden 双形态（ticket/ULID 掩码）
  （cmd/fleetly/cmd/verbs_databases.go newDatabasesBrowseVerb +
  databases_golden_test.go TestGoldenDatabasesBrowse + verbs_golden_test.go
  browseTicketRe 掩码（殿后——digest/ULID 先占位））
- [x] Console browse 动作（新窗口）+ dist 同 commit
  （console/src/pages/Resources.tsx DatabaseRow browse 动作 +
  window.open；console:verify 零漂移，2026-10-07）
- [x] e2e dind-browse 腿：postgres 库 → browse → traefik 真链 200（pgweb 页）
  + `SHOW default_transaction_read_only`=on（服务端只读锚）+ 票据复用拒
  （e2e/dind-browse.sh + mise 任务 e2e:browse；2026-10-07 本地 dind
  BROWSE E2E PASSED——受理回显/载体在场/entry 烧票 302+cookie/同票二次
  401/pgweb 页经 ForwardAuth 200/无 cookie 401/服务端只读 on）
- [ ] staging 真机走查：config 置位 → CLI browse → curl 全链 + Console 按钮
  （浏览器级）+ runbook 记录
