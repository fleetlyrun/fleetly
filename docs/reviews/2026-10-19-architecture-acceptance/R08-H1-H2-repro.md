# R08-H1/H2 运行时进程内复现（独立验证报告）

> 角色：Lead 的对抗性复核 / 独立验证者（不参与 R08 打分，只做运行时取证）。
> 基线：`D:\Codes\qiulin\fleetly`，`main` @ `35257f183c9a480183f5cca655c13d00fc96a774`（工作树干净，复现前后 `git status --porcelain` 均为空）。
> 环境：Windows，Go 1.26.6 windows/amd64；SQLite（modernc）+ 全部 33 个迁移随夹具启动。
> 约束遵守：仓库**零改动**（测试文件经 `go test -overlay` 注入，仓外文件）；未运行 mise/生成/e2e；未派生子代理。
> 复现日期：2026-10-19。

## 0. 结论摘要

| 项 | 结论 | 一句话 |
|---|---|---|
| R08-H1 平台面越权 | **成立（运行时复现）** | 非 default Team 的 builtin-admin 用户经真实登录所得 Token 含 `platform:admin`/`nodes:admin`；`EnrollNode`（含 `rotate=true`）、`DrainNode`、`RestartComponent` 均越过平台门进入 handler/编排器 |
| R08-H2 身份面接管 | **成立（运行时复现）** | 非 default Team 的 admin 会话重置 default Team 平台 owner 密码 → PUBLIC `Login` 返回 `team=default / role=builtin-owner` Token → 跨队读行、再铸平台 owner 用户；另证两个非 default Team 互管 |
| R08-H1 的 scope 事实（非 default Team admin 是否带 `platform:admin`/`nodes:*`） | **成立** | WhoAmI 实测 32 个 scope：`platform:admin`、`nodes:admin` 均在列，`audit:read` 为唯一非 admin |
| 证伪 | **无** | 未发现任何能推翻 R08-H1/H2 的运行时事实；两处细节需校准（见 §6） |
| 无法复现项 | 无（但有 2 处证据边界） | ①假 Runtime 的 join 材料是桩，证明的是**授权门缺席**而非真集群 token；②`RestartComponent` 只证到「未被平台门拒绝、进入 handler」，夹具无受管 Provider，未跑真实重启副作用 |

## 1. 目标

1. **H1**：取一个**非 default Team 的 admin 角色** Token，调用 `EnrollNode`（备用 `DrainNode`）——验证当前实现放行（漏洞成立），而应有平台管理员门时拒绝。
2. **H2**：Team A 的 admin Token 调用 `SetUserPassword` 重置 **default Team 的 owner 用户**密码（备用：两个非 default Team 用户互证），随后 PUBLIC `Login` 用新密码取 Token，检查返回的 `TeamId/RoleId` 是否已成为受害者（=接管成立）。
3. **scope 事实**：非 default Team admin 的 Token 实际携带的 scopes 是否包含 `platform:admin`、`nodes:*`。

判定标准（任务书）：能证伪更好；无法复现则如实写「未能复现/需要真机」；不得编造。

## 2. 方法

### 2.1 注入方式（overlay，不落仓）

用 `go test -overlay=<json>` 把仓外测试文件映射为 `internal/testsupport/apitest` 包内一个**不存在的** `_test.go` 路径（Go 官方 overlay 语义：映射目标可不存在，构建视图内视为存在）。仓库文件系统只读、无任何写动作。

- 被注入路径（仓库内，**磁盘上不存在**）：`internal/testsupport/apitest/r08h1h2_repro_test.go`
- 实际文件（仓外）：`D:\Codes\qiulin\fleetly-review-2026-10-19\verification\_tmp_r08repro_r08h1h2_test.go`
- overlay JSON：`D:\Codes\qiulin\fleetly-review-2026-10-19\verification\_tmp_r08repro_overlay.json`

overlay JSON 内容：

```json
{
  "Replace": {
    "D:\\Codes\\qiulin\\fleetly\\internal\\testsupport\\apitest\\r08h1h2_repro_test.go":
      "D:\\Codes\\qiulin\\fleetly-review-2026-10-19\\verification\\_tmp_r08repro_r08h1h2_test.go"
  }
}
```

### 2.2 夹具与「真实性」控制

用 `apitest.New(t)`：真装配 engine + 真 SQLite（含迁移）+ **与生产同一条拦截器链**（policy + authn + idem + ratelimit + freeze + error exit）+ bufconn 传输 + 假 Provider。`harness.go:157-177` 与生产 `assembly.NewGRPCServer` 同构，且 `AssertAllRegisteredHavePolicy` fail-closed。

为排除「夹具直插 Token 表造特权 Token」的质疑，**两条 H 的身份链路全部走产品面**：

1. owner（bootstrap）`CreateUser`（带初始密码、指定 Team/Role）；
2. 该用户经 **PUBLIC `Login`** 自证，拿 `password session` Token；
3. 该 Token 即被测调用方（有属主 Token 走 ADR-0038 实时收窄，但 token.RoleID==membership.RoleID 时是同角色免读快路径，scope 不变——`authn/interceptor.go:286-299`）。

### 2.3 对照组设计（证明「不是环境/夹具坏了」）

| 对照 | 作用 | 期望 |
|---|---|---|
| 同一非 default Team admin Token 调 `SetGitHubApp(global=true)` | 该 handler 调 `requirePlatformAdmin`（`githubapps.go:220-226`） | `E_FORBIDDEN` |
| 同一 Token 跨队 `CreateUser(team=default)` | `resolveTargetTeam` 的 Team 轴（`authz.go:231-247`） | `E_FORBIDDEN` |
| 受害者重置前 `Login` | 证明后续成功登录只能归因于攻击者重置密码 | `E_UNAUTHENTICATED` |
| 既有跨队矩阵 `TestRowLevelTeamAuthorization` / `TestScopeEnforcementMatrix` | 证明数据面行级授权本身工作正常（盲区只在平台/身份面） | PASS |

### 2.4 断言方向性

测试断言按「**当前实现的实际行为**」写：PASS = 漏洞仍在；若未来加了平台门/行级门，测试会 FAIL。这是复现脚本的应然方向（不是回归测试）。

## 3. 实际命令

全部在 `D:\Codes\qiulin\fleetly` 下执行（只读仓库）。

**基线（既有矩阵）**

```powershell
go test ./internal/testsupport/apitest -run 'TestRowLevelTeamAuthorization|TestScopeEnforcementMatrix' -v -count=1
```

输出（节选）：

```
=== RUN   TestScopeEnforcementMatrix
--- PASS: TestScopeEnforcementMatrix (0.09s)
=== RUN   TestRowLevelTeamAuthorization
--- PASS: TestRowLevelTeamAuthorization (0.10s)
PASS
ok  	github.com/fleetlyrun/fleetly/internal/testsupport/apitest	0.240s
```

**复现（overlay 注入）**

```powershell
go test -overlay=D:\Codes\qiulin\fleetly-review-2026-10-19\verification\_tmp_r08repro_overlay.json `
  ./internal/testsupport/apitest -run 'TestR08H1TeamAdminPlatformFace|TestR08H2IdentityTakeover' -v -count=1
```

输出（节选，完整日志见 `verification\_tmp_r08repro_go-test.log`）：

```
--- PASS: TestR08H1TeamAdminPlatformFace (0.16s)
--- PASS: TestR08H2IdentityTakeover (0.32s)
PASS
ok  	github.com/fleetlyrun/fleetly/internal/testsupport/apitest	6.154s
```

**零改动核对**

```powershell
git status --porcelain=v1   # 复现前后均无输出
git rev-parse HEAD          # 35257f183c9a480183f5cca655c13d00fc96a774
```

## 4. 结果

### 4.1 R08-H1：非 default Team admin 触发平台/节点面（成立）

实测日志（`-v`，逐条对应断言）：

```
[H1] non-default team created: id="h1-acme-2"
[H1] team-admin session (PUBLIC Login): user="h1-team-admin" team="h1-acme-2" role="builtin-admin"
[H1] WhoAmI: team="h1-acme-2" role="builtin-admin" user="h1-team-admin-1"
[H1] token scopes (32): alerts:admin app_variables:admin apps:admin audit:read builds:admin
     channels:admin configs:admin databases:admin deployments:admin events:admin exec:admin
     github_app:admin invitations:admin logs:admin metrics:admin networks:admin nodes:admin
     platform:admin projects:admin registry:admin revisions:admin roles:admin routes:admin
     secrets:admin shared_variables:admin tasks:admin teams:admin templates:admin tokens:admin
     users:admin volumes:admin workloads:admin
[H1] EnrollNode(team-admin, rotate=false) => err=<nil>
[H1] join material returned: "docker swarm join --token TESTTOKEN 127.0.0.1:2377"
[H1] EnrollNode(rotate=true) accepted; material: "docker swarm join --token TESTTOKEN 127.0.0.1:2377"
[H1] DrainNode(node-1) => err=<nil>
[H1] RestartComponent(traefik) with the same token => rpc error: code = NotFound
     desc = E_NOT_FOUND: component "traefik": unknown managed provider name
     (authn+scope passed, handler reached; no platform gate)
[H1] CONTROL SetGitHubApp(global) with the same token => rpc error: code = PermissionDenied
     desc = E_FORBIDDEN: this operation manages platform-scoped material;
     a platform-admin token (default team owner) is required (requirePlatformAdmin enforced)
```

判定链：

1. **身份/授权路径**：`builtin-admin` 模板由 `identity.BuiltinRoles(assembly.ScopeResources())` 对**每个资源**生成 `:admin`（`internal/domain/identity/roles.go:70-92`；`platform` 在词表内，`internal/assembly/policy.go:45-53`）。该模板是平台级、可在**任意 Team** 授予（`acceptance.go:263-278`）。运行时的 32 scope 与静态路径完全一致。
2. **handler 无平台门**：`EnrollNode` 只做 `commit`（审计）+ `Runtime.Enrollment`（`contexts.go:82-113`）；`nodeAdmin` 只做子面协商 + 编排器调用 + 审计（`contexts.go:153-179`），两者均无 `requirePlatformAdmin` 调用。
3. **运行时放行**：`EnrollNode` 返回 `nil` 错误并把 join material 回给调用方；`rotate=true` 直达假 Runtime（`EnrollCalls()==[false,true]`，说明「作废全部既有 join 材料」的意图调用被受理）；`DrainNode` 返回 `nil` 且假 Runtime 记录 `{Verb:"drain", NodeID:"node-1"}`。
4. **门确实存在**：同一 Token 调 `SetGitHubApp(global=true)` 被 `requirePlatformAdmin` 拒为 `E_FORBIDDEN`（该门判定 `callerTeam` 的 `platformAdmin = RoleID==builtin-owner && TeamID==default`，`authz.go:31-38,58-71`）。即：**门是运行时有效设施，只是没有铺到节点/治理/身份面**——这正是 R08-H1 的指控。
5. **对照标准**：ADR-0035 决策 2 明确「default Team 的 builtin-owner 才是平台管理员；其他 Team 的 owner 角色是该队管理员，不跨队」（`docs/adr/0035-row-level-team-authorization.md:14`）。本 Token 是 `h1-acme-2` 的 admin，按该决策不构成平台管理员。

**证据边界（诚实标注）**：apitest 的 `FakeRuntime.Enrollment` 是桩（`harness.go:604-613`，固定 `TESTTOKEN` join 命令），因此本复现证明的是「**平台管理员门在 handler 面缺席、非平台管理员可拿到 Enrollment 受理并触发 rotate**」，而不是「真实 swarm join token 已泄露」。真实材料副作用依赖生产 Runtime Provider（swarm/k3s），本机无 Docker daemon，未真机验证——但这不影响 H1 的授权结论（授权检查发生在 handler，桩不影响该路径）。

### 4.2 R08-H2：跨 Team 密码重置 → 受害者身份登录（成立）

实测日志（`-v`）：

```
[H2] attacker session: user="h2-attacker-admin" team="h2-acme-2" role="builtin-admin" users:admin=true
[H2] victim: user=h2-victim-owner-2 team=default role=builtin-owner password=unset
[H2] pre-reset Login(victim) => rpc error: code = Unauthenticated
     desc = E_UNAUTHENTICATED: name or password is incorrect (expected)
[H2] CONTROL cross-team CreateUser => rpc error: code = PermissionDenied
     desc = E_FORBIDDEN: target team default does not match the token's team h2-acme-2;
     tokens operate within their own team (E_FORBIDDEN; team axis enforced there)
[H2] SetUserPassword(teamB-admin -> default-team owner h2-victim-owner-2) => err=<nil>
[H2] Login(victim, attacker password) => team="default" role="builtin-owner"
     user="h2-victim-owner" token=tok-3
[H2] hijacked token WhoAmI: team="default" role="builtin-owner" scopes=[*]
[H2] IMPACT: hijacked token read team B project h2-acme-core-1 (row team=h2-acme-2)
[H2] IMPACT: captured token created platform-owner user h2-implant-owner-3
[H2] VARIANT Login(h2-gamma-admin after team-B reset) => team="h2-gamma-3" role="builtin-admin"
     (want "h2-gamma-3" / "builtin-admin")
```

判定链：

1. **无门**：`SetUserPassword` 的受理 checks 仅 `userExists(user_id)`（`identity.go:181-182`），无平台门、无「该用户是否在本队」的行级门；`Login` 是 PUBLIC 且密码对即按 `memberships[0]` 铸全权 Token（`identity.go:120-169`）。
2. **实测成功**：team `h2-acme-2` 的 admin 会话对 default Team 的 `builtin-owner` 用户重置密码 → `err=<nil>`；受害者在重置前 `Login` 明确失败（`E_UNAUTHENTICATED`），排除了「密码本来就能登录」。
3. **接管坐实**：PUBLIC `Login` 返回 `TeamId=default`、`RoleId=builtin-owner`；新 Token 的 `WhoAmI` 为 `team=default role=builtin-owner scopes=[*]`——调用方身份**就是受害者**。
4. **影响面实证**：被俘获 Token 跨进 Team B 读 Project 行成功（owner 豁免）；并可再 `CreateUser(role=builtin-owner)` 铸出**另一个平台 owner 用户**（R08-H2 影响链第 2 步的持久化面）。
5. **旁证（非 default 前置独立成立）**：Team B admin 重置 Team C 的 `builtin-admin` 用户密码 → `Login` 得 `team=h2-gamma-3 role=builtin-admin`。该变体不依赖「default Team 存在 owner/admin 用户」这一前置，与 R08 的备用论证一致。
6. **身份面并非无 Team 轴**：同一 Token 的跨队 `CreateUser` 被 `resolveTargetTeam` 拒为 `E_FORBIDDEN`（`authz.go:231-247`）——缺的只是 C6 新增的密码/登录写面（ADR-0035 决策 5 只豁免 Users/Teams/Roles 目录读写面，`:22`）。

**证据边界**：`Login` 铸出的 Token 未做 TTL/有效期实测（本复现只验证「铸出即可用」）；R08 的「无 TTL」是静态代码事实，本复现不构成新证据，也不反驳。

### 4.3 scope 事实（R08-H1 的前置断言）

- 非 default Team（`h1-acme-2`）的 `builtin-admin` 会话 Token，WhoAmI 返回 **32 个 scope**。
- `platform:admin` **在列**；`nodes:admin` **在列**（因而蕴含 `nodes:write`/`nodes:read`）；`audit:read` 是唯一非 admin 项（与 `roles.go:75-79` 逐字一致）。
- 结论：R08-H1 关于「内建 admin 模板把 `platform`/`nodes` 下放给每个 Team」的静态断言**在运行时成立**，且对**非 default Team** 同样成立。

补充静态佐证：`internal/domain/identity/identity_test.go:176-184` 把「admin 必须覆盖词表内每个资源的 write」写成**正向断言**——说明这是被测试钉死的设计事实，不是某处漏赋值；因此修复必然涉及授权模型/ADR 层（与 R08 建议 ②③ 一致），而非补一行判断。

### 4.4 既有测试为何没拦住（运行时对账）

- `TestRowLevelTeamAuthorization` 与 `TestScopeEnforcementMatrix` 实测全绿（§3 基线）。它们覆盖了数据面行级授权与 member/owner 的 scope 差异，但：
  - `authz_test.go:84-90` 的 C3 用例只断言 **member** 对 `EnrollNode` 403、**owner** 通过；`builtin-admin`（每个 Team 都有）从未被负例触碰。
  - `teamauth_test.go:75` 铸了 team B admin Token，但只打 Project/App/Task/Run/Secret 等**数据面**（会过 `authorize*`），没有打节点/平台/身份写面（这些面根本不调 `authorize*`）。
  - 全仓 `*_test.go` 检索：`SetUserPassword(` 与 `UsersService.Login(` 调用数为 **0**（`grep` 结果确认），即 R08「identity 密码/登录链零测试」的静态结论成立。
- 因此两处漏洞是**测试矩阵的覆盖盲区**，不是环境异常；这也解释了为什么 CI 全绿的代码仍可被运行时打穿。

## 5. 结论（逐条）

| # | 待验证断言 | 结论 | 依据 |
|---|---|---|---|
| 1 | 非 default Team admin 可调 `EnrollNode`（含 rotate） | **成立** | 实测 `err=<nil>`、join material 回显、`EnrollCalls=[false,true]` |
| 2 | 非 default Team admin 可调 `DrainNode` 任意节点 | **成立** | 实测 `err=<nil>`、假 Runtime 记录 `drain node-1` |
| 3 | `RestartComponent`/平台面缺平台门 | **部分成立**（门缺席已实锤；副作用未跑） | 同一 Token 进入 handler 得 `E_NOT_FOUND: unknown managed provider`（非 403）；夹具无受管 Provider |
| 4 | 非 default Team admin Token 含 `platform:admin`/`nodes:*` | **成立** | WhoAmI 32 scopes 含 `platform:admin`、`nodes:admin` |
| 5 | 跨 Team `SetUserPassword` 重置 default Team owner | **成立** | 实测 `err=<nil>`；重置前 Login 失败 |
| 6 | PUBLIC `Login` 用新密码返回受害者 `TeamId/RoleId` | **成立** | `team=default role=builtin-owner`，WhoAmI 同 |
| 7 | 接管后可跨队读行并持久化平台 owner | **成立** | 读 Team B Project 成功；`CreateUser(role=builtin-owner)` 成功 |
| 8 | 不依赖 default-owner 前置的两 Team 互证 | **成立** | Team B admin → Team C admin 用户接管 |
| 9 | R08-H2「跨 Team 用户接管不依赖前置」 | **成立** | 同 8 |
| 10 | R08-H2「链到平台 owner 依赖 default Team 存在 admin/owner 用户」 | **前置可满足，非阻塞** | 该用户由公开的 `CreateUser` 路径创建（本复现即此形态）；一旦存在，链即成立 |
| — | 证伪项 | **无** | 未发现反例 |

**未能复现/未覆盖**（如实列出，不编造）：

1. 真实 join token 签发/作废（需真 Docker/Swarm；本机 daemon 不可用）——只验证授权门缺席。
2. `RestartComponent` 的真实重启副作用（夹具无受管 Provider）——只验证平台门未拦截。
3. REST 入口重放（`POST /v1/nodes/enroll`、`POST /v1/auth/login`）——gRPC 同一 handler/拦截器链已在进程内取证；REST 只是 gateway 转发，未重复做，风险低。
4. `Login` Token 的 TTL/吊销时效——未做时间旅行测试。
5. H2 相关审计行落在攻击者 Team 而非受害者 Team（代码可读，未查询断言）——未取证，故不在结论中主张。

## 6. 对 R08 报告的修正建议

1. **R08-H1 状态升级**：从「已亲验（静态路径完整；运行时无测试覆盖）」改为「**已亲验（静态 + 运行时进程内复现）**」，并在证据节补：非 default Team admin 会话实测 32 scopes 含 `platform:admin`/`nodes:admin`；`EnrollNode`（rotate 真/假）与 `DrainNode` 实测放行；同一 Token 在 `SetGitHubApp(global)` 上被 `requirePlatformAdmin` 403（证明门存在但未铺开）。建议附本报告路径。
2. **R08-H1 影响 #3（RestartComponent）措辞校准**：运行时只能证「未被平台门拒绝、进入 handler」，不能证「实际重启了受管组件」（apitest 无受管 Provider）。建议在影响条目上标注「门缺席已实锤；副作用面为静态推断」，避免被质疑过度声称。
3. **R08-H1 影响 #1（join token）措辞校准**：授权结论成立，但「取活 join token」在复现中由假 Runtime 桩返回固定材料；建议注明「授权面已运行时复现；材料真实性归 Provider（R06）」，或直接引用本报告的证据边界。
4. **R08-H2 置信度上调**：§7 未决点 2 说完整提权链依赖「default Team 存在 admin 用户」，置信「中高」。复现表明该前置可由**产品支持的 `CreateUser` 路径**直接满足，且链的每一步（重置 → 登录 → 跨队读 → 再铸 owner）均运行时成立。建议改为「前置是配置事实、非阻塞；给定前置时链的置信：高」。
5. **R08-H2 补充一条影响细节**：除 default-owner 链外，**两个非 default Team 之间**也成立（B admin → C admin 用户），且攻击者自身只需「该 Team 的 builtin-admin 用户 + 密码登录」，不需要任何超出产品常规能力的特权。建议在影响节补此变体，降低「依赖平台 owner 用户」的论证脆弱性。
6. **R08 §6 守卫盲区**可补一条精确表述：`identity_test.go:176-184` 把「admin 覆盖全部词表资源的 write（含 platform/nodes）」写成**正向断言**——这意味着根因不是「忘了加一条 scope」，而是**授权模型把平台资源当普通资源**；任何按「补一行判断」的修法都会与该测试冲突，必须走 ADR/模型层裁决（与 R08 建议 ② 一致，但值得在报告里显式点出，防止实施者低修）。
7. **建议（非报告修正）**：把本复现的两个用例改写成正式 guard/apitest 负例（跨队 admin 对 `EnrollNode/DrainNode/RestartComponent` 必须 403；跨队 `SetUserPassword` + `Login` 必须 403），并纳入 CI——当前 `authz_test`/`teamauth_test` 的矩阵缺口正是这两条 H 的成因。

## 7. 复现资产（位置与复跑）

| 文件 | 说明 |
|---|---|
| `verification\_tmp_r08repro_r08h1h2_test.go` | overlay 注入的测试源（package `apitest_test`；含 H1/H2 两用例与对照） |
| `verification\_tmp_r08repro_overlay.json` | overlay 映射（仓内不存在的 `r08h1h2_repro_test.go` → 上述文件） |
| `verification\_tmp_r08repro_go-test.log` | 复现命令完整输出（含 goose 迁移行，可用 `Select-String '\[H1\]|\[H2\]'` 过滤） |
| `verification\_tmp_r08repro_baseline.log` | 既有矩阵基线输出 |

复跑（在 `D:\Codes\qiulin\fleetly` 下，只读仓库）：

```powershell
go test -overlay=D:\Codes\qiulin\fleetly-review-2026-10-19\verification\_tmp_r08repro_overlay.json `
  ./internal/testsupport/apitest -run 'TestR08H1TeamAdminPlatformFace|TestR08H2IdentityTakeover' -v -count=1
```

预期：当前代码 **PASS**（漏洞在）；若未来加门修复，两用例会 **FAIL**——这是复现脚本的方向性，不是回归测试。清理只需删除 `verification\` 下四个 `_tmp_r08repro_*` 文件（仓内无副本）。

## 8. 附：完整复现日志（已滤掉迁移/时间戳噪音）

```
=== RUN   TestR08H1TeamAdminPlatformFace
[H1] non-default team created: id="h1-acme-2"
[H1] team-admin session (PUBLIC Login): user="h1-team-admin" team="h1-acme-2" role="builtin-admin"
[H1] WhoAmI: team="h1-acme-2" role="builtin-admin" user="h1-team-admin-1"
[H1] token scopes (32): alerts:admin app_variables:admin apps:admin audit:read builds:admin channels:admin configs:admin databases:admin deployments:admin events:admin exec:admin github_app:admin invitations:admin logs:admin metrics:admin networks:admin nodes:admin platform:admin projects:admin registry:admin revisions:admin roles:admin routes:admin secrets:admin shared_variables:admin tasks:admin teams:admin templates:admin tokens:admin users:admin volumes:admin workloads:admin
[H1] EnrollNode(team-admin, rotate=false) => err=<nil>
[H1] join material returned: "docker swarm join --token TESTTOKEN 127.0.0.1:2377"
[H1] EnrollNode(rotate=true) accepted; material: "docker swarm join --token TESTTOKEN 127.0.0.1:2377"
[H1] DrainNode(node-1) => err=<nil>
[H1] RestartComponent(traefik) with the same token => rpc error: code = NotFound desc = E_NOT_FOUND: component "traefik": unknown managed provider name (authn+scope passed, handler reached; no platform gate)
[H1] CONTROL SetGitHubApp(global) with the same token => rpc error: code = PermissionDenied desc = E_FORBIDDEN: this operation manages platform-scoped material; a platform-admin token (default team owner) is required (requirePlatformAdmin enforced)
--- PASS: TestR08H1TeamAdminPlatformFace (0.16s)
=== RUN   TestR08H2IdentityTakeover
[H2] attacker session: user="h2-attacker-admin" team="h2-acme-2" role="builtin-admin" users:admin=true
[H2] victim: user=h2-victim-owner-2 team=default role=builtin-owner password=unset
[H2] pre-reset Login(victim) => rpc error: code = Unauthenticated desc = E_UNAUTHENTICATED: name or password is incorrect (expected)
[H2] CONTROL cross-team CreateUser => rpc error: code = PermissionDenied desc = E_FORBIDDEN: target team default does not match the token's team h2-acme-2; tokens operate within their own team (E_FORBIDDEN; team axis enforced there)
[H2] SetUserPassword(teamB-admin -> default-team owner h2-victim-owner-2) => err=<nil>
[H2] Login(victim, attacker password) => team="default" role="builtin-owner" user="h2-victim-owner" token=tok-3
[H2] hijacked token WhoAmI: team="default" role="builtin-owner" scopes=[*]
[H2] IMPACT: hijacked token read team B project h2-acme-core-1 (row team=h2-acme-2)
[H2] IMPACT: captured token created platform-owner user h2-implant-owner-3
[H2] VARIANT Login(h2-gamma-admin after team-B reset) => team="h2-gamma-3" role="builtin-admin" (want "h2-gamma-3" / "builtin-admin")
--- PASS: TestR08H2IdentityTakeover (0.32s)
PASS
ok  	github.com/fleetlyrun/fleetly/internal/testsupport/apitest	6.154s
```
