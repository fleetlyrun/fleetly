# R08 接口层与契约面 评审报告

> 评审范围：`internal/interfaces/**`（10,693 行生产代码）+ `proto/**`（5,298 行）+ `internal/assembly/{gateway*.go,grpc.go,errors.go,proxyconfig.go,policy.go}`（1,632 行），
> 以及 `internal/testsupport/apitest` 与 `internal/guards` 中与本模块强相关的测试/守卫（仅用于对账，其自身质量归 R12）。
> 基线：`main` @ `35257f18`，工作树干净；全程只读（唯一写入 = 本文件）。
> 本机实测：`go test ./internal/interfaces/... ./internal/assembly ./internal/guards -run ...` 全绿；`TestRowLevelTeamAuthorization`/`TestScopeEnforcementMatrix` 全绿。

## 1. 覆盖清单

### 1.1 完整读过（生产文件）

**proto 契约（16 个文件全部读完）**
`proto/fleetly/{structure,delivery,identity,automation,telemetry,runtime,proxy,system,shared/error,spec}/v1/*.proto`，含
`delivery/v1/{github_apps,registry,templates}.proto`、`runtime/v1/{exec,workloads}.proto`、`system/v1/governance.proto`。
另以脚本提取了全部 **36 service / 153 RPC** 的 `method_auth` scope + `google.api.http` 路径矩阵（逐条比对，见 §2、§4）。

**fleetlygrpc（9,889 行，44 个 .go）逐个读完**
`register.go, services.go, acceptance.go, authz.go, mapping.go, contexts.go, structure.go, networks.go, variables.go, delivery.go, automation.go, databases.go, alerting.go, identity.go, invitations.go, governance.go, platform.go, githubapps.go, hooks.go, webhook.go, templates.go, registry.go, uploads.go, exec.go, wait.go, eventsource.go, eventtickets.go, workloads.go, schemareg.go`

**interfaces 其余**
`grpc/{apivalidate/apperrstatus,systemgrpc}/*.go`（含 `exit.go`）、`interfaces/admin/rewrap.go`、`interfaces/console/console.go`

**assembly（R08 段）**
`gateway.go, gateway_hooks.go, gateway_uploads.go, gateway_events.go, gateway_exec.go, gateway_browse.go, grpc.go, errors.go, proxyconfig.go, policy.go`；测试 `grpc_test.go, errors_test.go, gateway_hooks_test.go, policy_test.go, schema_gate_test.go`

**依赖读取（用于排除误读，非本模块结论来源）**
`internal/domain/identity/{roles,scope,token}.go`、`internal/application/authn/{interceptor,seed,bootstrap}.go`、`internal/application/idem/enforcer.go`、`internal/application/governance/{freeze,ratelimit}.go`、`internal/domain/services/{anchor,writes}/**`、`persistence/{outbox,audit,team,database,migrations}/**`、`internal/guards/{gatewayguard,grpcguard,scopeguard,acceptanceguard,factdual,freezeguard,writeclassguard}_test.go`、`internal/testsupport/apitest/{harness,teamauth_test,authz_test,console_static_test,nodes_containers_test}.go`、外部依赖 `lynx-go/grpcapi@v0.1.0` 的 `gateway/{mux,marshaler,headers}.go`（REST 未知字段/头透传事实）。

### 1.2 抽样读过（测试/生成物）

- `genproto/fleetly/**/**.pb.go` 生成头与 swagger JSON（15 个 `.swagger.json`；对 `registry.swagger.json` 做了缺省错误响应专项比对）。
- apitest 其余 60+ 文件按主题抽样：`rest_errors_test.go, rest_gateway_test.go, events_team_test.go, exec_boundary_test.go, browse_test.go, freeze_test.go, templates_test.go, githubapps_test.go, networkpeer_test.go, delete_coverage_test.go, platform_test.go, nodes_containers_test.go, webhook_test.go`。
- `mise.toml` 的 `generate:all/generate:verify/lint:proto`、`buf.yaml/buf.gen.yaml`、`.github/workflows/ci.yml` 的 proto/gateway 相关 job（只读，对账生成物纪律）。

### 1.3 未覆盖 / 无法覆盖

- **不能运行 `mise run generate:verify`/`console:verify`**（会写生成物，PROFILE §4 禁止）——生成物纪律只能做「git 历史抽样 + 生成头比对 + 守卫阅读」，不能做零漂移实证。
- 未跑 Docker/e2e（本机 daemon 不可用）——`/v1/relay`、`/v1/exec/stream`、browse ForwardAuth 的原生入口只做代码与错误面阅读，未真机验证。
- `automation.proto`/`telemetry.proto` 后半段消息体、`structure.proto` 消息体只做「字段/约束/注释」通读，没有逐字段和 CLI/SDK 消费面交叉（归 R10）。
- `internal/interfaces/console/dist/**` 打包产物未逐个审（R11）。

## 2. 模块画像与边界

**职责**：proto 是唯一契约源（16 proto / 36 service / **153 RPC**），genproto 生成三件套（pb/grpc/gw）+ openapiv2 swagger；grpc-gateway 提供 REST/OpenAPI；`fleetlygrpc` 是 36 个 service 的实现（读路径直读聚合 repo、写路径经受理位 `commit` 或 engine）；`assembly` 装配 gRPC server（拦截器链）+ REST gateway + 原生入口（hooks 原始体验签 / uploads 流式转发 / SSE / exec WS / relay WS / platform binary / browse ForwardAuth）+ 统一错误信封；console embed 只服务非 `/v1` 路径。

**验证到的依赖方向**：`interfaces → application(engine/authn/transact/idem/governance) + domain`，无 persistence 构造、无 providers import；`dep_matrix_test.go` 覆盖（守卫非本模块，但读码与调用点一致：`services.go` 只注入 domain Repo 接口与 application 单例）。

**写路径纪律（实测核对）**：对 `fleetlygrpc` 全量 grep「`svc.s.<Repo>.Create/Update/SoftDelete/Upsert/Revoke/Put/Transit`」为 **0 命中**；全部写经 `Services.commit`（`acceptance.go:54-68`）或 engine 动词（Deploy/Rollback/Scale/Stop/Renew/TriggerSchedule/Teardown*）。`internal/guards/acceptanceguard_test.go` 对「创建/删除型 RPC 必经 commit」做 AST 反扫，豁免仅 Deploy/Rollback/DeleteTask（engine 写路径，带理由）。这一条是本模块最扎实的架构承诺。

**与 ADR/架构声明的偏差（证据见 §4）**：
1. **`platform` scope 语义过载**。`policy.go:43-44` 明确「platform 是集群面资源（C3）」，但 `governance.proto:36-56` 把 *Team 级* freeze 的 set/lift/list 挂在 `platform:*` 上；而内置 admin 角色 = 全资源 `:admin`（`identity/roles.go:70-91`，含 `platform`/`nodes`/`users`/`teams`）。结果是「平台面」与「每个 Team 的 admin」之间没有任何物理隔离（H1/H2 的根因）。
2. **平台面 handler 与既有 `requirePlatformAdmin` 范式不一致**：GitHub App 的 Global 行有明确门（`authz.go:58-71`、`githubapps.go:224/294/318/361/473/530/577`），但 `EnrollNode`/三个节点运维动词/`PlatformService`/`RestartComponent`/`RefreshTemplates` 都没有调用它，只靠 scope（H1）。
3. **identity 写面缺平台管理员门**：`SetUserPassword`/`DeleteUser`/`DeleteTeam` 等只有 scope 门（H2）。ADR-0035 决策 5（`docs/adr/0035-row-level-team-authorization.md:22`）显式把「Users/Teams/Roles 目录读面」排除在行级授权之外，但未覆盖 C6 批新增的**密码重置/登录**写面；该缺口构成跨 Team 账号接管链。
4. **鉴权/守卫覆盖层面**：`AssertAllRegisteredHavePolicy` 在 production（`assembly/grpc.go:160-168`）与 apitest 夹具（`harness.go:174-177`）都被调用，成立；但守卫只保证「注册的方法有 policy」，不保证「取行的 handler 调了行级授权」——本模块的 2 个 H 恰好落在这个空白。

**契约面整体判断**：错误信封/出口脱敏（§3）、注册与 policy 覆盖（§3）、gRPC↔REST 挂载对账、幂等执法面、生成物纪律都是高质量面；主要债务集中在 **scope 能力面没有 Team 维度**（平台/身份/观测资源）、**分页与必填注解不统一**、**长时同步动词与 30s 预算错配**、**流式/票据资源面无上限**。

## 3. 优点（带证据）

**3.1 行级授权有单一真源，数据面覆盖系统化**
`authz.go:31-56`（`callerTeam`/`teamAuthorized`：默认 Team owner = 平台管理员；他队 owner 不豁免）+ `authz.go:126-179`（按全局 ID 直取的授权族，anchor 解析图单源）。逐 handler 抽样核对：`GetApp/GetProject/ListApps/ListSecrets/GetConfig/GetTask/GetRun/GetDeployment/GetRevision/DiffRevisions/ListBuilds/StreamBuildLogs/GetGitHook/SetGitHook/CreateExecSession/StreamExecSession/GetAppWorkloadObservations/Wait*/DownloadBackup/VerifyBackup/BrowseDatabase/...` 均在读/写前调用对应 `authorize*`；`teamauth_test.go:109-192` 用 team B admin token 对 13 个读面 + 10 个写面钉死 `E_FORBIDDEN`。这是本模块最好的安全资产。

**3.2 写路径单点 + 受理位事务语义**
`acceptance.go:51-68` 的 `commit` 把「受理检查（事务内）→聚合写→事实扇出（outbox+audit 双真）」收成一个原语；`checks` 覆盖父资源存活、配额、FK 归属（`acceptance.go:96-300`）。没有 handler 绕过它直写 repo（§2 实测）。删除守卫的「预检→锁内→事务内三重复查」在 `structure.go:439-543` 体现得很完整（DeleteApp teardown-aborted 残余面显式留痕）。

**3.3 错误信封与 E_INTERNAL 脱敏是三面单点**
`apperrstatus/exit.go:61-107`：只对 `E_INTERNAL` 统一换文案 + 铸 `error_id` + cause 全文只进服务端日志；非内部错误按等价 status 放行（wire 零漂移）；REST gateway 复用同一 `SanitizeExit`（`errors.go:120-164`）。`errcodeToHTTP` 是错误码级显式映射而非 gRPC 机械映射，且有**双向保鲜守卫**（`errors_test.go:19-37`：注册表每个码必须在表内且 4xx/5xx；表内不得有注册表外码）。

**3.4 注册/契约漂移守卫是 AST 级且双向**
REST 挂载 ↔ `*.pb.gw.go` 注解面双向对账（`guards/gatewayguard_test.go:121-174`，含豁免双向保鲜与「扫描面可疑偏小」防盲）；gRPC `RegisterAll` ↔ `*.grpc.pb.go` 服务面同款（`grpcguard_test.go:70-123`）；scope 方向守卫按动词表反扫「写动词标 READ 即红」（`scopeguard_test.go:72-124`）。`AssertAllRegisteredHavePolicy` 缺失即启动 fail-closed（`assembly/grpc.go:160-168`），apitest 夹具同链（`harness.go:157-177`）。

**3.5 流式/票据面纪律**
一次性票据按 purpose+payload 绑定、单用途、兑换即删、换错不烧票（`eventtickets.go:65-105`）；410 断档在写出 200/SSE 头**之前**判定（`contexts.go:384-410`、`gateway_events.go:54-59`），避免 SSE 里丢状态码；gRPC 流与 SSE 共用 `subscribeEvents` 单实现（`contexts.go:454-485`）；exec 会话附着按会话铸造时 Team 校验，挡跨队抢附着（`exec.go:188-202`，`exec_boundary_test.go` 钉测）。

**3.6 Console embed 的 `/v1` 旁路安全**
`console.go:28-36` 只把 `/v1` 与 `/v1/` 前缀交给下层，其余路径才进 embed 静态面；所有原生入口（hooks/uploads/SSE/exec/relay/binary/browse）都挂在 `/v1/...` 下，不会被 SPA fallback 吞；`console_static_test.go:20-51` 用生产同清单钉住「未知 /v1/* 仍 JSON 404」，`assetMaxAge`/`no-cache` 的缓存策略（索引不可缓存、hashed 资产长缓存）方向正确。

## 4. 发现清单

### R08-H1｜平台/节点/治理面没有平台管理员门禁，内建 admin 角色把 `platform:admin`、`nodes:*` 下放给每个 Team——任意 Team 管理员可取活 join token（可轮换作废全部节点材料）、排空/封锁任意节点、重启受管组件、替换全局模板目录
- 维度：安全（多租户隔离 / 权限边界）
- 严重度：H（平台级破坏性动作只由 scope 把关；内建 admin 模板在**任意 Team** 都授予 `platform:admin`，与 ADR-0035 决策 2「他队 owner 不跨队」及 C3「platform 是集群面资源」直接冲突）
- 位置：
  - `internal/domain/identity/roles.go:70-91`（admin = 全资源 `:admin`，含 `platform`/`nodes`）
  - `internal/assembly/policy.go:40-53`（词表把 `platform` 定义为集群面；`nodes` 在列）
  - `internal/interfaces/grpc/fleetlygrpc/contexts.go:82-113`（`EnrollNode` 无平台门）、`contexts.go:153-179`（`nodeAdmin` 无平台门）
  - `internal/interfaces/grpc/fleetlygrpc/platform.go:25-40`、`internal/interfaces/grpc/systemgrpc/systemgrpc.go:120-141`、`internal/interfaces/grpc/fleetlygrpc/templates.go:349-393`（`RefreshTemplates` 无平台门）
  - 对照范式：`internal/interfaces/grpc/fleetlygrpc/authz.go:58-71`（`requirePlatformAdmin` 的注释与语义）
  - proto：`proto/fleetly/runtime/v1/runtime.proto:43-53`（EnrollNode=`platform:admin`）、`:54-76`（nodes:write）
  - 测试盲区：`internal/testsupport/apitest/authz_test.go:84-90` 只测 member 与 owner，**未测 builtin-admin**
- 证据：
  ```go
  // roles.go:73-91（内建角色模板）
  for _, res := range sorted {
      if res == "audit" { admin = append(admin, Scope{Resource: res, Action: "read"}) }
      else              { admin = append(admin, Scope{Resource: res, Action: "admin"}) }
      ...
  }
  return []BuiltinRole{
      {ID: RoleOwnerID, Name: "owner", Scopes: []Scope{{Resource: "*", Action: "*"}}},
      {ID: RoleAdminID, Name: "admin", Scopes: admin},   // ← 含 platform:admin / nodes:admin / users:admin
  ```
  ```go
  // contexts.go:82-93（EnrollNode：只做 scope，全无平台管理员判定）
  func (svc *NodesService) EnrollNode(ctx context.Context, req *runtimev1.EnrollNodeRequest) (*runtimev1.EnrollNodeResponse, error) {
      action := "node.enroll"
      if req.GetRotate() { action = "node.rotate_join_tokens" }
      ...
      kit, err := svc.s.Runtime.Enrollment(ctx, req.GetRotate(), capability.EnrollmentOptions{GatewayPort: svc.s.GatewayPort})
  ```
  ```go
  // authz.go:58-71（范式存在：平台级行写门，但只被 GitHubApps 消费）
  func (s *Services) requirePlatformAdmin(ctx context.Context) error {
      _, owner, err := callerTeam(ctx)
      ...
      if !owner { return apperr.New("E_FORBIDDEN", "this operation manages platform-scoped material; ...") }
  ```
- 影响：Team admin（产品中不是平台所有者，只是「该队管理员」——ADR-0035 决策 2）可：
  1. `EnrollNode` 取**活 join token**（等价集群成员权，C3 自述"不可逆"）；`rotate=true` 会作废全部既有节点入群材料（升级/扩容通道中断，集群面 DoS）；
  2. `DrainNode/CordonNode/UncordonNode` 排空/封锁任意平台节点 → 跨 Team 工作负载可用性；
  3. `RestartComponent`（platform:admin）重启 traefik/zot/VL/VM 等全平台受管组件 → 全平台中断；
  4. `TriggerPlatformBackup/ListPlatformBackups`（platform:write/read）与 `RefreshTemplates`（替换全局模板目录，后续所有模板实例化的输入端被污染）；
  5. 结合 `/v1/relay` 的 join-token 鉴权模型（ADR-0049：hello 自报 `carrier_node_id`，engine 只按该 ID 查节点行，`application/engine/execrelay.go:20-46`），join token 持有者还可**顶替任意节点的 relay 身份**，接管/收流他队 exec 会话帧，并顶掉该节点既有 relay（relay 替换即断）。
- 为什么守卫没拦住：`scopeguard` 只校验「方法声明的 scope 方向」；没有任何守卫/测试对「role × platform-face」做禁止矩阵；`authz_test` 的 C3 用例只对 member 断言，admin 的 platform:admin 从未被测试触碰。
- 建议：①在 `EnrollNode`/`nodeAdmin`/`PlatformService`/`RestartComponent`/`RefreshTemplates` 统一调用 `requirePlatformAdmin`（与 GitHubApps 单点对齐，S）；②清理 `BuiltinRoles`：`platform`/`nodes` 不进「任意 Team 可授」的 admin 模板，或引入平台管理员专用角色（M，需 ADR 决策）；③补 `apitest`：他队 admin 对这些方法必须 `E_FORBIDDEN`；④新守卫（AST/反射）：SERVICE 面中 scope 为 `platform:*` 或资源属 `nodes` 的方法必须在 handler 内出现平台判定（M）。**建议机制层补齐而非逐点修**。
- 状态：已亲验（静态路径完整：role→scopeSet→Satisfies→handler 无门；运行时无测试覆盖 = 无可跑证据，见 §7）

### R08-H2｜identity 写面（密码重置 + Login）没有平台/归属门：任意 Team 管理员可重置任意用户密码并以该用户身份登录，构成跨 Team / 平台级账号接管链
- 维度：安全（身份鉴权 / 跨租户越权）
- 严重度：H（账号接管 = 拿到受害用户所在 Team 的完整 Token：资源/密钥/部署；若受害用户在 default Team 且持 admin/owner membership，可继续链到平台 owner）
- 位置：
  - `internal/interfaces/grpc/fleetlygrpc/identity.go:174-202`（`SetUserPassword`：只 `userExists`，无调用者 Team/角色检查）
  - `internal/interfaces/grpc/fleetlygrpc/identity.go:120-170`（`Login`：按 membership[0] 铸全权 Token，无过期/无二次因子）
  - `internal/interfaces/grpc/fleetlygrpc/identity.go:218-250`（`DeleteUser`）、`:309-336`（`DeleteTeam`）同族无门
  - `proto/fleetly/identity/v1/identity.proto:49-57`（`SetUserPassword`=`users:write`）、`:72-78`（`DeleteUser`=`users:admin`）、`:107-113`（`DeleteTeam`=`teams:admin`）
  - 根因：`internal/domain/identity/roles.go:70-91`（admin = `users:admin`/`teams:admin` 等全部 identity 资源）+ `internal/interfaces/grpc/fleetlygrpc/acceptance.go:263-278`（`roleInTeam`：内置角色在任意 Team 可授）
  - ADR 边界：`docs/adr/0035-row-level-team-authorization.md:22` 只豁免「Users/Teams/Roles 目录读面」，未涉及 C6 新增的密码/登录面
- 证据：
  ```go
  // identity.go:181-197（SetUserPassword：只有用户存在性，没有"你是否有权改这个用户"）
  err = svc.s.commit(ctx, writeFact{
      checks: []acceptanceCheck{svc.s.userExists(req.GetUserId())},
      write: func(ctx context.Context, st transact.Stores) error {
          return st.Users.SetPassword(ctx, req.GetUserId(), string(hash))
      },
  ```
  ```go
  // identity.go:124-144（Login：密码对即按 membership[0] 铸 Token）
  u, err := svc.s.Users.GetByName(ctx, req.GetName())
  if err != nil || u.PasswordHash == "" ||
      bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.GetPassword())) != nil {
      return nil, apperr.New("E_UNAUTHENTICATED", "name or password is incorrect")
  }
  memberships, err := svc.s.Memberships.ListByUser(ctx, u.ID)
  ...
  m := memberships[0]
  t := &token.Token{Name: "password session", TeamID: m.TeamID, UserID: u.ID, RoleID: m.RoleID, ...}
  ```
- 影响：攻击者是 team X 的 builtin-admin（`users:admin` ⊇ `users:write`）：
  1. `SetUserPassword(team Y 的任意 user, 任意口令)` → `Login(name)` → 得到 **team Y 的该 role Token** → 跨租户读密钥/部署/数据库；
  2. 若 default Team 存在任一 admin/owner membership 的用户（owner 用 `CreateUser` 建 admin 用户是产品支持路径，`identity_test.go:37-45` 即此形态）：接管它 → `CreateUser(role_id=builtin-owner, team_id 缺省)`（`resolveTargetTeam` 视 default 为自己的 Team，`roleInTeam` 对 builtin 放行）→ 登录新用户 = **平台 owner**；
  3. 同族 `DeleteUser`（users:admin）可跨队删用户，`DeleteTeam`（teams:admin）可跨队删空 Team（有限，FK 兜底）。
  另注：`Login` 铸出的 "password session" Token **无 TTL**（domain `token.Token` 无 expires 概念，identity.proto `Token` 无 expires_at），一次接管后的访问是永久的（只能靠吊销），放大该链的后果。
- 为什么守卫没拦住：identity 目录面被 ADR-0035 显式排除，但 C6 批新增的密码/登录面没有任何新守卫；`apitest` 对 `Login`/`SetUserPassword` **零测试**（全仓 grep `SetUserPassword(`）= 仅 handler 自身），teamauth 矩阵只测 CreateToken/CreateProject 的 team 目标校验（`teamauth_test.go:221-228`）。
- 建议：①`SetUserPassword/DeleteUser/DeleteTeam`（以及其余 identity 写面）加平台管理员门（`requirePlatformAdmin`）或改为「仅本人/admin 在本队 membership 内」的显式授权族（S/M）；②`CreateUser` 对 `role_id=builtin-owner|builtin-admin` 也要求平台管理员（当前任意 admin 都能授）；③补 apitest：两 Team × admin/member 的 identity 面矩阵 + Login 链路；④ADR-0035 追加决策记录并对齐（安全面不应只靠"v1 定位"）。成本 S/M，强烈建议机制层（授权族 helper + 守卫反扫）。
- 状态：已亲验（静态路径与 fixture 语义完整；无测试覆盖）

### R08-M1｜节点读面对普通 member 开放且无 Team 过滤：每个 member 可枚举全平台节点与所有 Team 的在跑容器归属
- 维度：安全（跨租户信息泄漏）
- 严重度：M（元数据泄漏：team/project/app/image/instance/node；不含值载荷，但足以做跨租户侦察与定向攻击）
- 位置：`internal/interfaces/grpc/fleetlygrpc/contexts.go:33-52`（ListNodes）、`contexts.go:57-80`（ListNodeContainers，直接返回 provider 归属投影）；`proto/fleetly/runtime/v1/runtime.proto:26-42`（两方法均 `nodes:read`）；`internal/domain/identity/roles.go:50-54,74-87`（member 排除表不含 nodes → member 自动获得 `nodes:read`）
- 证据：
  ```go
  // contexts.go:57-80（ListNodeContainers：无 callerTeam、无 Team 过滤；Team 字段原样透出）
  rows, err := inspect.NodeContainers(ctx, req.GetNodeId())
  ...
  for _, row := range rows {
      out.Containers = append(out.Containers, &runtimev1.NodeContainer{
          Instance: row.Instance, WorkloadId: row.WorkloadID, Domain: row.Domain,
          Team: row.Team, ProjectId: row.Project, AppId: row.AppID, Process: row.Process,
          Image: row.Image,
      })
  }
  ```
  ```go
  // roles.go:74-87（member 对未排除资源一律拿 read；nodes 不在排除表）
  if memberExcludedResources[res] { continue }
  member = append(member, Scope{Resource: res, Action: "read"})
  ```
- 影响：任何团队的最普通 member token 可 `GET /v1/nodes`（节点清单+relay 在线状态）与 `GET /v1/nodes/{id}/containers`（全平台 `team/project_id/app_id/image/instance`），即跨租户工作负载目录。owner 之外无任何限制。测试只覆盖 owner 视角（`nodes_containers_test.go:15-58`），无 member/跨队负例。
- 建议：节点观测面按平台管理员门（与 H1 同修），或 ListNodeContainers 对非 owner 按 `callerTeam` 行过滤（S）。并补成员/跨队负例。
- 状态：已亲验

### R08-M2｜观测/告警读面对 member 全平台开放：QueryMetrics 原样透传任意 PromQL（平台凭证）、ListAlertRules/ListAlertStates 无 Team 过滤
- 维度：安全（跨租户信息泄漏）
- 严重度：M（指标里含容器标签/命名空间/资源用量；规则行含他队 app_id/阈值。ADR-0041 承认「查询隔离由平台查询构造」并把多租户收窄挂账，但 API 把"构造权"交给任意持 `metrics:read` 的调用方，等于把挂账变成既成泄漏面）
- 位置：`internal/interfaces/grpc/fleetlygrpc/alerting.go:55-99`（QueryMetrics 透传）、`:277-298`（app_id 空 → `ListAll`）、`:338-371`（ListAlertStates 全量）；`proto/fleetly/telemetry/v1/telemetry.proto:85-98,424-427`；`roles.go:74-87`（member 有 metrics/alerts read）
- 证据：
  ```go
  // alerting.go:84-98（PromQL 原样交给平台凭证的 Metrics 端口）
  series, err := svc.s.Metrics.QuerySeries(ctx, req.GetQuery(), start, end, step)
  ```
  ```go
  // alerting.go:283-289（app_id 为空时全平台规则）
  if req.GetAppId() != "" { rows, err = svc.s.Engine.AlertRuleRepo().ListByApp(ctx, req.GetAppId()) } else {
      rows, err = svc.s.Engine.AlertRuleRepo().ListAll(ctx)
  }
  ```
- 影响：member 可读全平台指标序列（cadvisor 原始透传标签含 fleetly_ns/project/app 归因）与他队告警规则/现行状态。Console 前端为图表构造查询，但 API 本身不限制 query 内容。
- 建议：①QueryMetrics 服务端按调用方 Team 强制注入/校验 namespace 过滤（或对非 owner 拒绝裸 PromQL，改结构化查询）；②ListAlertRules/States 非 owner 按 Team 过滤（S/M）。与 R14/R15 的挂账核对（ADR-0041:28,62）。
- 状态：已亲验

### R08-M3｜复合 handler 进程内直调其他服务方法绕过 scope 门禁：`templates:write` 可创建 App/网络/Secret/Database/Route 并发起部署；`apps:write` 的 RefreshGitHook 可触发部署
- 维度：安全（权限提升 / 授权语义）
- 严重度：M（scope 是"能力面"的执法点，只在 RPC 边界检查一次；复合编排面把内层动词的能力一并对调用方开放，自定义角色可借此越出声明能力）
- 位置：`internal/interfaces/grpc/fleetlygrpc/templates.go:196-212,214-229,247-261,263-278,286-293,297-320`（InstantiateTemplate 内直调 `CreateApp/CreateNetwork/PutSecret/CreateDatabase/Deploy/CreateRoute`）；`proto/fleetly/delivery/v1/templates.proto:51-57`（整方法只有 `templates:write`）；`internal/interfaces/grpc/fleetlygrpc/webhook.go:209-242`（RefreshGitHook → `Engine.SubmitSpec`）；`proto/fleetly/delivery/v1/delivery.proto:232-238`（只有 `apps:write`）；scope 执法点：`internal/application/authn/interceptor.go:193-204`
- 证据：
  ```go
  // templates.go:204-224,255-257,286-290（内层服务直调，无二次 HasScope）
  created, err := (&AppsService{s: svc.s}).CreateApp(ctx, &structurev1.CreateAppRequest{...})
  ...
  if _, err := (&NetworksService{s: svc.s}).CreateNetwork(ctx, &structurev1.CreateNetworkRequest{...})
  ...
  if _, err := (&SecretsService{s: svc.s}).PutSecret(ctx, &structurev1.PutSecretRequest{...})
  ...
  if _, err := (&DatabasesService{s: svc.s}).CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{...})
  dep, err := (&DeploymentsService{s: svc.s}).Deploy(ctx, &deliveryv1.DeployRequest{...})
  ```
- 影响：只被授予 `templates:write` 的自定义角色（一个合理的"模板使用者"角色）实际获得 `apps:write + networks:write + secrets:write + databases:write + deployments:write + routes:write`；只被授予 `apps:write` 的角色可通过 RefreshGitHook 触发部署（部署链路=构建+替换工作负载）。行级授权仍在（内层各有 authorizeProjectID/authorizeAppRow），故是**能力面越权**而非跨队越权，但违反「scope 面 = 声明能力」的契约直觉；Agent 侧按 scope 做最小权限分配会失效。
- 建议：①复合 handler 在关键内层动作前显式 `identity.HasScope(...)`（`interceptor.go:460-465` 已有 `HasScope` 可复用）；或②把复合 RPC 的注解 scope 改成其能力并集（contract 上更诚实），并同步 scope 守卫/文档；③补自定义角色授权测试。成本 S/M。
- 状态：已亲验

### R08-M4｜输入大小上限缺位：`PutSecret.value` 无长度约束，而 gRPC 收包上限为 webhook 抬到 32MiB——任一持有 `secrets:write` 的 member 可写入 32MiB 级秘密行，无配额
- 维度：安全（资源耗尽）/ 契约（校验分界）
- 严重度：M（member 默认持 `secrets:write`；secrets 无数量/字节配额，SQLite 单文件 + age 信封膨胀；磁盘/备份/恢复面被放大）
- 位置：`proto/fleetly/structure/v1/structure.proto:559-588`（`PutSecretRequest.value` 无 `cel` 上限，同文件 `PutConfigRequest.content` 256KiB、变量 16KiB 都有）；`internal/interfaces/grpc/fleetlygrpc/structure.go:579-632`（无手写上限）；`internal/assembly/grpc.go:43-48`（`grpcMaxRecvMsgSize = 32MiB`，为 25MiB webhook 抬高，影响全部 unary）；`internal/domain/identity/roles.go:42`（member 写 secrets）
- 证据：
  ```proto
  // structure.proto:586-587（value 只有注释，无任何校验注解）
  // value 只进不回（请求侧明文；响应只回指纹）。
  string value = 3;
  ```
  ```go
  // assembly/grpc.go:43-48（单一真源：32MiB 全局收包上限）
  const grpcMaxRecvMsgSize = 32 << 20
  ```
- 影响：任何 member 可重复写 32MiB secret（同名覆盖或新名累积；secret 无 per-project 配额、`PutSecret` 非 Create 配额检查）→ DB/磁盘膨胀、平台备份体积与时长放大（与 M11 的 30s 预算叠加会直接让平台备份失败）、重建/恢复变慢。gRPC 单请求 32MiB 内存 × 并发也是内存放大面。
- 建议：`PutSecretRequest.value` 加 `cel` 上限（对齐下游载体上限，swarm secret 约 500KiB；取 256KiB 与 config 同档）+ per-Project secret 数量/字节配额（受理位，`acceptance.go` 已有 quota 族范式）；同时复核 `spec_file`/`compose_yaml`/`private_key_pem` 是否都需要显式上限（当前都靠 32MiB 传输兜底）。成本 S。
- 状态：已亲验

### R08-M5｜必填性注解矩阵不一致：85/153 RPC 的请求消息无任何 protovalidate 注解，同族「缺 id/缺 project_id」有的 400、有的 404
- 维度：架构一致性 / 契约设计
- 严重度：M（契约不可发现：Agent/CLI/SDK 无法从 descriptor 判断必填；同一类用户错误在不同端点呈现不同错误码与文案，重试/修复建议分叉）
- 位置（示例，完整清单见脚本统计）：`proto/fleetly/automation/v1/automation.proto:324`（`GetTaskRequest { string id = 1; }`）、`:214-215`（StopTask/DeleteTask/RenewTask 同款）、`proto/fleetly/delivery/v1/delivery.proto:503`（`RollbackRequest`）、`:494-500`（`ListDeploymentsRequest.app_id` 无 required）、`proto/fleetly/identity/v1/identity.proto:336-340`（DeleteUser）、`:381-385`（DeleteTeam）、`:484-490`（GetToken/RevokeToken）；对照有 required：`proto/fleetly/structure/v1/structure.proto:523-536`（ListApps project_id required）、`delivery.proto:376-383`（Deploy.app_id required）
- 证据：
  ```proto
  // automation.proto:324（空 id → authorizeTaskID → anchor NotFound → 404）
  message GetTaskRequest { string id = 1; }
  // delivery.proto:503
  message RollbackRequest { string app_id = 1; string to_revision = 2; }
  // 对照 structure.proto:524-531（同族 List 面的 project_id 反而有 required → 400）
  string project_id = 1 [ (buf.validate.field).cel = {
      id: "string.required" message: "must not be empty" expression: "this.size() > 0" } ];
  ```
  P4-1/P4-2 批把「手写形状检查」迁到了 descriptor，但历史上 Get/Delete/Wait 类路径**本来就没有手写检查**，迁移未补齐 → 语义分裂（缺参 404「app  not found」 vs 400「app_id: must not be empty」）。
- 影响：CLI `--json` 错误信封/退出码与 Console 表单校验的 UX 不一致；Agent 按错误码做自愈分支时把「参数缺失」误判为「资源不存在」；REST 400/404 语义漂移。统计：153 RPC 中 85 个请求消息零 protovalidate 注解（含合理无参面，但 id/project_id 类必填面占多数）。
- 建议：对 id/app_id/project_id/database_id/build_id/deployment_id 等寻址字段统一补 `string.required`（对齐 `entityid` 长度/形态）；在 guards 增加「RPC 请求寻址字段必须带 required 注解或登记豁免」反扫（M）。成本 M。
- 状态：已亲验（脚本枚举 + 逐一对照）

### R08-M6｜审计「只增不裁」但查询面没有游标；identity/alerting 多个 List 面完全无分页
- 维度：可观测性 / 契约（分页惯例 ADR-0026）
- 严重度：M（审计永久保留，但 API 只能取最近 ≤1000 行，更早的取证行**不可达**；ListTokens/ListInvitations/ListAlertRules 等全量载入 + 内存过滤，随规模线性劣化）
- 位置：`internal/interfaces/grpc/fleetlygrpc/identity.go:581-616`（ListAudit：只有 `Limit`，无游标）；`proto/fleetly/identity/v1/identity.proto:582-588`（`ListAuditRequest` 无 after_*）；`internal/infrastructure/persistence/audit/repo.go`（`ORDER BY seq DESC LIMIT ?`，无 `seq < ?`）；同族无分页：`identity.go:204-214`（ListUsers）、`:297-307`（ListTeams）、`:399-409`（ListRoles）、`:511-528`（ListTokens 全量 + 内存过滤）、`invitations.go:98-115`（ListInvitations 全量 + 内存过滤）、`alerting.go:190-200/277-298/338-371`
- 证据：
  ```go
  // identity.go:597-602（limit 钳 1000，无游标可翻页）
  limit := int(req.GetLimit())
  if limit <= 0 || limit > 1000 { limit = 100 }
  filter.Limit = limit
  list, err := svc.s.Audits.ListFiltered(ctx, filter)
  ```
  ```go
  // persistence/audit/repo.go ListFiltered（新→旧，仅 LIMIT；无 cursor 谓词）
  query += " ORDER BY seq DESC LIMIT ?"
  ```
- 影响：①违反 ADR-0035 决策 6 / ADR-0075「审计严格只增、不裁剪」的消费面承诺——只增的数据没有完整读取面；②List 面 full-load 内存过滤在 Token/Invitation 增长后成本线性；③`ListAudit` 的 limit>1000 时回落 100（而非钳 1000），与 `listLimit()` 的「钳上界」惯例又是一处不一致。
- 建议：`ListAuditRequest` only-add `after_id`/`after_seq`（`WHERE seq < ? ORDER BY seq DESC`，与事件游标同款）；identity/alerting List 面统一补 `after_* + limit`（复用 `listLimit`）；guard 增加「List 面必须有分页或登记豁免」（M）。
- 状态：已亲验

### R08-M7｜流式面与票据面没有并发/时长/数量上限：follow 流绕过 unary 限流与超时；`IssueEventTicket` 无限制且每次 O(n) 清扫
- 维度：可靠性 / 性能
- 严重度：M（任一 member 持 `events:read`/`logs:read` 即可放大 SQLite 与 CPU；这是可自我核实的、无需特权的资源耗尽面）
- 位置：`internal/assembly/grpc.go:250-259`（stream 链只有 errorExit/authn/freeze，无 limiter、无超时）；`internal/interfaces/grpc/fleetlygrpc/contexts.go:375-382`（follow 每 250ms 轮询 outbox）、`:458-485`（subscribeEvents 轮询循环）；`internal/interfaces/grpc/fleetlygrpc/eventtickets.go:52-63`（issue：无 per-token/总量上限）、`:107-115`（sweepLocked：每次 issue 全量遍历）；限流器只覆盖 `idem.EnforcedMethods`（`governance/ratelimit.go:64-77`），`IssueEventTicket` 不在其中
- 证据：
  ```go
  // eventtickets.go:52-62（每次铸造都对全部在册票做 O(n) 清扫；无上限）
  func (s *eventTicketStore) issue(purpose, payload string) (string, time.Duration, error) {
      ...
      s.mu.Lock()
      s.sweepLocked()
      s.tickets[token] = ticketEntry{expiry: s.clock.Now().Add(eventTicketTTL), purpose: purpose, payload: payload}
      s.mu.Unlock()
  ```
  ```go
  // contexts.go:477-483（每流每 250ms 一次 outbox 查询）
  if len(batch) == 0 {
      select {
      case <-ctx.Done(): return nil
      case <-time.After(streamEventsPollInterval):
      }
  }
  ```
- 影响：①`StreamEvents/StreamLogs/Wait*/StreamBuildLogs` 无并发上限：N 条 follow 流 = 4N 次 outbox 查询/秒（注释只给"≈50 并发"分析容量，无执行面限制，`contexts.go:377-381`）；②`IssueEventTicket` 不在限流面，循环铸造 N 次 → 总清扫 O(N²) CPU 且 map 内存增长（60s 窗口内）；③SSE 每连接 1 个 goroutine + 15s ping；④无单流最大时长（客户端可不取消）。
- 建议：流式链加 per-token/per-team 信号量（例如每 Token 4、每 Team 16）+ 单流最大时长（如 30min，与 exec TTL 同量级）；ticket store 加容量上限（如 1024，满即拒）并改惰性批量清扫（S/M）。
- 状态：已亲验

### R08-M8｜`factOf` 把 Project 归属解析失败吞成 TeamID=""：该次写路径的 outbox 事件落为「平台级可见」（fail-open 跨队事件泄漏）
- 维度：安全（多租户隔离）/ 可靠性（错误处理）
- 严重度：M（触发条件为写入前一次 `Projects.Get` 失败：瞬时 DB busy/取消；但隔离轴设计为 fail-closed，此处是唯一 fail-open 单点，且事件行 team_id="" 对**所有 Team** 可见——`outbox.ListAfter` 的过滤是 `team_id = ? OR team_id = ''`）
- 位置：`internal/interfaces/grpc/fleetlygrpc/acceptance.go:75-88`（`factOf`：`if p, err := s.Projects.Get(ctx, projectID); err == nil { team = p.TeamID }`，error 被静默吞掉）；`internal/interfaces/grpc/fleetlygrpc/contexts.go:454-460`（订阅注释与过滤口径）；`internal/infrastructure/persistence/outbox/repo.go:43-53`（`AND (team_id = ? OR team_id = '')`）；第二个实例：`structure.go:550-570`（`recordTeardownAbort` 刻意 `TeamID` 留空但 payload 携带他队 `project_id`）
- 证据：
  ```go
  // acceptance.go:80-88（归属解析失败 → 空串 → 事实落"平台级可见"；注释明说"不为归属解析新增失败面"）
  func (s *Services) factOf(ctx context.Context, projectID string, row func(team string) writes.FactRow) func() writes.FactRow {
      team := ""
      if projectID != "" {
          if p, err := s.Projects.Get(ctx, projectID); err == nil {
              team = p.TeamID
          }
      }
      return func() writes.FactRow { return row(team) }
  }
  ```
  ```go
  // persistence/outbox/repo.go:52-53（'' 行对所有 Team 可见）
  if team != "" { query += ` AND (team_id = ? OR team_id = '')` }
  ```
- 影响：在 DB 瞬时故障窗口内成功完成的那次写（如 task/create、hook.push、template.instantiate 等）其事件 payload（含 project_id/资源 ID、branch/commit 等）对所有 Team 的 `events` 订阅者可见；这违反 CONTEXT「事件行携资源轴 team_id」与 ADR-0035 资源轴语义。`recordTeardownAbort` 是设计内 ''（系统动作），但其 payload 也把受害 Team 的 `project_id` 广播给所有 Team。
- 建议：`factOf` 改为返回 error 并在解析失败时整单拒绝（`E_INTERNAL`，fail-closed）；`recordTeardownAbort` 改为同样走归属解析（调用点已有 projectID，无需不可得）；补 factdual/acceptance 守卫：事实 TeamID 为 '' 的声明点必须显式带理由且只允许平台级聚合（S）。
- 状态：已亲验

### R08-M9｜公开的 `GetStatus` 无认证：匿名可触发全部 Provider 并行探活，且把内部地址与上游原始错误透传给匿名方
- 维度：安全（信息泄漏 / 匿名放大）
- 严重度：M（`SystemService` 整体 `PUBLIC`；system.proto 自述"诊断信息不含敏感细节"，实际 details 含内部 endpoint/数据根路径/原始错误；且每请求 ~9 个 Provider 的 docker/apiserver/TCP/HTTP 调用，无限速 → 未认证放大）
- 位置：`proto/fleetly/system/v1/system.proto:27-38`（SystemService 默认 PUBLIC，GetStatus 无 method_auth）；`internal/interfaces/grpc/systemgrpc/systemgrpc.go:56-115`（并行探活 + 2s 超时 + panic recover；超时路径 `done` 缓冲后若 `Health` 不响应 ctx 则 goroutine 泄漏）；provider details 例：`internal/infrastructure/providers/s3objectstore/provider.go:173-177`（`s3 target %s unreachable: %v`/bucket/endpoint）、`localobjectstore/provider.go:63`（`local backup target writable at <root>`）、`victorialogs/provider.go:198-201`（`managed log store ... at <addr>`）；`system.proto:3-6`（"诊断信息不含敏感细节"的自我承诺）
- 证据：
  ```go
  // systemgrpc.go:78-112（每个 checkers 一个 goroutine + 独立 Health goroutine）
  go func(i int, provider capability.Provider) {
      ...
      go func() { done <- provider.Health(checkCtx) }()
      select {
      case report := <-done: ...
      case <-checkCtx.Done(): component.Healthy = false; component.Details = "health check timed out"
      }
  }(i, provider)
  ```
- 影响：匿名 `GET /v1/system/status` 可读内部拓扑（受管 VL/VM/zot/objectstore 地址、数据根）、上游错误原文（可能含主机/证书/驱动细节）；同时是可放大的探活触发器（无 token、无限速、每次全量 Provider 探测）。
- 建议：GetStatus 拆成「公开最小态（healthy/degraded + version）」与「需 token 的 components 详情」；或对 details 做白名单/脱敏；加未认证面限速或缓存（探活结果 TTL 缓存，避免每请求打 Provider）（S/M）。
- 状态：已亲验

### R08-M10｜原生入口先读请求体后验凭证，且 REST server ReadTimeout=0：匿名慢速大 body 可长期占用连接/内存
- 维度：可靠性 / 安全（匿名 DoS）
- 严重度：M（ADR-0079 已把 gateway 整面前置到公网；`/v1/hooks/<token>` 是 PUBLIC，token 是路径段但**读取 body 发生在 token/HMAC 校验之前**；ReadTimeout 被显式置 0 以兼容 SSE，但没有 per-route 读预算）
- 位置：`internal/assembly/gateway_hooks.go:38-57`（先 `io.ReadAll(io.LimitReader(...))` 至 25MiB，再把 token 交给 gRPC 验签）；`internal/assembly/gateway.go:141-154`（`ReadTimeout=0; WriteTimeout=0; ReadHeaderTimeout=10s`）；`internal/assembly/gateway_uploads.go:44-113`（原生上传流无前置大小/速率预算，靠服务端上传存储计数）
- 证据：
  ```go
  // gateway_hooks.go:49-57（先读满 body，再进入验签；坏 token 也要先读 25MiB）
  payload, err := io.ReadAll(io.LimitReader(r.Body, hookPayloadLimit+1))
  ...
  if len(payload) > hookPayloadLimit { ...413... }
  // 之后：client.ReceiveWebhook(callCtx, &deliveryv1.ReceiveWebhookRequest{Token: token, ...})
  ```
- 影响：未认证攻击者用若干连接以极慢速率发送 25MiB body，即可长期占用 goroutine 与最多 25MiB 缓冲/连接；`ReadTimeout=0` 使 http.Server 不会切断。hooks 入口在公网可达（Console 同 gateway），uploads 同理（但需 Authorization 才进入有效处理，body 仍会被转发读取）。
- 建议：为原生入口加 per-request 读预算（`http.MaxBytesReader` + `ReadHeaderTimeout` 之外的 `context.WithTimeout` 包裹 body 读取，或 `IdleTimeout`/`MaxHeaderBytes` + 反向代理 request-body 限制）；hooks 可先做 token 形态快速拒绝（`flthook_` 前缀/长度即可 401，不读 body）（S）。
- 状态：已亲验

### R08-M11｜同步长动词与 30s unary 预算错配：`TriggerPlatformBackup` 的引擎硬上限是 15m，但 handler ctx 30s 先被切断；同类 `RefreshGitHook` 克隆、`RotateDatabasePassword` 数据面操作也没有长动词豁免
- 维度：功能 / 可靠性
- 严重度：M（`TriggerPlatformBackup` 的契约是"同步执行、成功即事实"（`platform.go:22-31`、CLI synopsis "synchronous"），升级序前置动词；只要 restic 快照超过 30s（GB 级数据根/慢对象存储）经 API 必失败，且失败发生在引擎仍在跑/被取消的中途）
- 位置：`internal/assembly/grpc.go:36-41`（`grpcUnaryTimeout=30s`）、`:71-73`（`longRunningUnary` 仅 `RebuildNetwork`）、`:79-88`（超时拦截器对 handler ctx `WithTimeout`）；`internal/interfaces/grpc/fleetlygrpc/platform.go:25-31`（同步调用 engine）；`internal/application/engine/platformbackup.go:370`（`context.WithTimeout(ctx, BackupTimeout)`）+ `options.go:77-80,180-181`（`BackupTimeout` 缺省 15m）；同类：`webhook.go:209-242` + `engine/gitmaterial.go:197-240`（RefreshGitHook 内 git clone 无自身超时，完全依赖 30s ctx）、`engine/rotate.go`（`RotateDatabasePassword` 无自身超时）
- 证据：
  ```go
  // assembly/grpc.go:41,71-73,81-86
  const grpcUnaryTimeout = 30 * time.Second
  var longRunningUnary = map[string]bool{
      "/fleetly.structure.v1.NetworksService/RebuildNetwork": true,
  }
  ctx, cancel := context.WithTimeout(ctx, timeout); defer cancel(); return handler(ctx, req)
  ```
  ```go
  // engine/platformbackup.go:370（引擎自认为有 15m；父 ctx 30s 先死）
  execCtx, cancel := context.WithTimeout(ctx, e.opts.Backup.BackupTimeout)
  ```
- 影响：①大平台升级前置备份永远拿不到成功回执（返回 DEADLINE_EXCEEDED/504，引擎侧被 cancel）；②大仓库 `RefreshGitHook`（compose 源需 clone）在 30s 处必断，错误形态是传输层超时而非可编程错误；③`RotateDatabasePassword` 在慢镜像拉取/慢库上可能在"数据面已改密、Secret 未重写"窗口被切断（轮换一致性风险，细节归 R04）。
- 建议：把 `TriggerPlatformBackup`/`RefreshGitHook`（结构化 long-running 面）加入 `longRunningUnary`（S；与 RebuildNetwork 先例同款），或改为「触发 + Wait/Get 观测」的异步契约；`RotateDatabasePassword` 补引擎级 timeout 并评估原子序。成本 S。
- 状态：已亲验（静态；CLI 侧已 `noDeadline()` 但服务端拦截器不受客户端 deadline 影响）

### R08-L1｜删除型动词的 scope 阶梯不一致：`routes:admin`、`volumes:admin`、`secrets:admin` 等与 `tasks:write`、`schedules:write` 并存；member 可创建 route/volume/secret 却不能删
- 维度：架构一致性 / 契约设计
- 严重度：L（可用性/一致性；不构成越权——admin 更严）
- 位置：`proto/fleetly/proxy/v1/proxy.proto:40-46`（DeleteRoute=routes:admin）vs `proto/fleetly/automation/v1/automation.proto:72-79`（DeleteTask=tasks:write）；`structure.proto:122-128`（DeleteSecret=secrets:admin）、`:239-245`（DeleteVolume=volumes:admin）vs `roles.go:29-43`（memberWriteResources 含 routes/volumes/secrets）→ member 可建不可拆。注释只解释了 member「能部署不能拆家」，但写面清单与删除面清单没有可执行的裁决规则。
- 证据：见上（scope 矩阵逐条抽取）。另一处：`structure.proto:305-312` VerifyBackup 是 POST 但 `databases:read`（只读动词的写方法）。
- 影响：自定义角色若照 member 模板授权会出现"建得出来删不掉"；Agent 最小权限分配需逐资源枚举，无法从规则推导。
- 建议：立一条裁决（删除=admin 仅限"拆家"资源，其余=write）并写进 ADR/词条，或在 scopeguard 增加「Create 与 Delete 同资源 scope 阶梯关系」守卫。
- 状态：已亲验

### R08-L2｜`Cipher` 显式关态防御不一致：`SetGitHubApp`/`SetGitHook`/`RotateHookToken` 直接解引用，未见 `E_MASTER_KEY_UNAVAILABLE`
- 维度：可靠性 / 一致性
- 严重度：L（生产 wire 的 `NewMaterialCipher` 失败即启动失败，Cipher 恒非 nil；但同族 handler 把 nil 当支持的显式关态并在测试/装配变体里可达，三处会 nil-panic 而非 503）
- 位置：`internal/interfaces/grpc/fleetlygrpc/githubapps.go:247`、`hooks.go:91`、`hooks.go:179`；对照：`structure.go:585-589`（PutSecret 判空返回 `E_MASTER_KEY_UNAVAILABLE`）、`githubapps.go:364-368/582-586`（ListRepos/CompleteManifestFlow 判空）
- 证据：
  ```go
  // githubapps.go:247（无 nil 检查；同文件 364 行有）
  sealed, err := svc.s.Cipher.Produce(pemBytes)
  // hooks.go:91 / :179 同款
  sealed, serr := svc.s.Cipher.Produce([]byte(material.Secret))
  ```
- 影响：若未来出现"无主密钥装配"（该代码路径已明确设计为合法 off 态），CreateHook/RotateHook/SetGitHubApp 会 panic（gRPC 面只靠服务端 Recovery 兜底，且错误面丢失 503 语义）。
- 建议：三处统一前置 nil 检查，与其它面同文案；或引入 `Services.cipher()` 单点 helper（S）。
- 状态：已亲验

### R08-L3｜分页 limit 直传 repo：`ListDatabases`/`ListBackups`/`ListNetworkPeers` 绕过 `listLimit()`，显式大页（>200）被回落 50 而非钳 200
- 维度：架构一致性
- 严重度：L
- 位置：`internal/interfaces/grpc/fleetlygrpc/databases.go:177-178`、`:409-410`、`networks.go:398-399`；对照范式 `automation.go:712-724`（`listLimit` 注释明确「n>200 钳到上界 200 而非回落缺省 50」，N1 C21 裁决）；repo 侧 `persistence.ClampLimit` 的语义是「越界回落 def」
- 证据：
  ```go
  // databases.go:178（raw int 直传；>200 时 ClampLimit 回落 50）
  req.GetProjectId(), req.GetAfterDatabaseSeq(), int(req.GetLimit()))
  ```
- 影响：显式请求大页的 Console/CLI 得到 50 条（静默吃掉显式意图），与其它 List 面行为不一致。
- 建议：三处改用 `listLimit(req.GetLimit())`（S）。
- 状态：已亲验

### R08-L4｜SSE 注释声称 `id` 供 `Last-Event-ID` 重连续用，但 handler 不读该头；断线重连从旧 query 游标重放（重复帧）
- 维度：文档一致性 / 可靠性
- 严重度：L（EventSource 自动重连会重复投递已见事件；被裁断时仍能走 410 重同步，不丢语义但重复）
- 位置：`internal/assembly/gateway_events.go:140-151`（`id: seq` + 注释「供 Last-Event-ID 重连续用」）；`:48-53`（只从 query 取 `after_seq`，全文无 `Last-Event-ID` 读取）；`:52-53`（`after_seq` 非法时静默归 0）
- 证据：
  ```go
  // gateway_events.go:151（写 id 并声称支持重连续用）
  if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.GetSeq(), ev.GetName(), buf.Bytes()); err != nil {
  // gateway_events.go:48-53（读的是 query，不是 Last-Event-ID；坏值静默 0）
  afterSeq, err := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
  if err != nil { afterSeq = 0 }
  ```
- 影响：注释-实现漂移；重连重复帧需要消费方按 seq 幂等；坏游标静默全量重放而非 400。
- 建议：优先读 `Last-Event-ID`（回退 query），非法游标显式 400；同步修注释（S）。
- 状态：已亲验

### R08-L5｜`registry.proto` 缺文件级 openapiv2 默认错误响应（其余 15 个 service proto 都有）
- 维度：契约设计 / 文档一致性
- 严重度：L（OpenAPI/Console 生成类型对该服务没有 `ErrorResponse` 契约）
- 位置：`proto/fleetly/delivery/v1/registry.proto:10-17`（无 `protoc-gen-openapiv2` import、无 `openapiv2_swagger` 选项）；生成物 `genproto/fleetly/delivery/v1/registry.swagger.json` 无 `"default"`/`ErrorResponse`（实测 `templates.swagger.json` 有）；`genproto/fleetly/**/**.swagger.json` 其余 14 个均有
- 证据：
  ```proto
  // registry.proto:10-14（对照 templates.proto:17-25 有 default → .fleetly.shared.v1.ErrorResponse）
  import "buf/validate/validate.proto";
  import "google/api/annotations.proto";
  import "grpcapi/v1/authz.proto";
  ```
- 影响：Console/SDK 由 swagger 生成类型时，registry 端点无错误体类型可引用；OpenAPI 消费者面对这两个端点的错误面不可发现（运行时行为正常，仍走统一信封）。
- 建议：补齐文件级 swagger option 并同批 `generate:all`（S）；加守卫「每个含 service 的 proto 必须有默认错误响应」。
- 状态：已亲验

### R08-L6｜Console 静态面对**非内容哈希**文件也发 `immutable` 一年缓存（favicon.png/logo.png）
- 维度：性能 / 正确性
- 严重度：L（`dist/favicon.png`、`dist/logo.png` 是无哈希文件名，按 immutable 缓存后浏览器一年不取新版本）
- 位置：`internal/interfaces/console/console.go:22-23,43-66`（`assetMaxAge = 365d`；只要 `distFS` 命中且非 `index.html` 就 `Cache-Control: public, max-age=31536000, immutable`）
- 证据：
  ```go
  // console.go:62-63（任何可打开的非 index 文件都 immutable）
  w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", int64(assetMaxAge/time.Second)))
  http.ServeContent(w, r, upath, time.Time{}, seeker)
  ```
- 影响：favicon/logo 更新后老客户端长期看不到；若未来新增无哈希静态文件同样中招。
- 建议：仅对 `assets/`（vite 哈希命名）发 immutable，其余（根目录未哈希文件）用 `no-cache`/短 max-age（S）。
- 状态：已亲验

### R08-L7｜超过 72 字节的密码走 `bcrypt` 错误 → 被映射成 `E_INTERNAL` 500（应为参数错误）
- 维度：错误处理 / 契约一致性
- 严重度：L（`golang.org/x/crypto/bcrypt` 对 >72 字节返回 `ErrPasswordTooLong`；handler 把它当内部故障）
- 位置：`internal/interfaces/grpc/fleetlygrpc/identity.go:67-76`（CreateUser）、`:174-180`（SetUserPassword）
- 证据：
  ```go
  // identity.go:70-75
  if pw := req.GetPassword(); pw != "" {
      hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
      if err != nil {
          return nil, apperr.New("E_INTERNAL", "password hashing failed").WithCause(err)
      }
  ```
- 影响：可编程错误面被污染（客户端拿到 500 + error_id，而实际是输入超限）；同时契约没有密码长度/强度上限（另一面归 R14）。
- 建议：先显式校验长度（≤72 字节/最小长度，加 CEL 或手写 `E_INVALID_ARGUMENT`），再调用 bcrypt（S）。
- 状态：已亲验

### R08-L8｜`InstantiateTemplate` 的 Route 复用是「全平台 host 映射」：命中他队已有 host 时记录 reused 并回显他队 route id（跨队信息泄漏 + 静默不建路由）
- 维度：安全（跨租户信息）/ 功能
- 严重度：L（不获得访问权，但泄漏 host 存在性与 route ID；受害调用方得到"成功"回执而其 App 实际无路由）
- 位置：`internal/interfaces/grpc/fleetlygrpc/templates.go:295-320`
- 证据：
  ```go
  // templates.go:297-309（全平台 Routes.List → byHost，无项目/Team 归属比对）
  allRoutes, err := svc.s.Routes.List(ctx)
  ...
  for i := range allRoutes { byHost[allRoutes[i].Host] = allRoutes[i].ID }
  for _, r := range rendered.Routes {
      if existingID, ok := byHost[r.Host]; ok {
          record("route", existingID, r.Host, true); continue
      }
  ```
- 影响：模板 domain 值来自用户 `values` → 用户可探测任意 host 是否已被全平台占用（返回他队 route id）；路由冲突被"复用"掩盖而非拒绝，实例化语义失真。
- 建议：命中既有 route 时校验其 `ProjectID == req.project_id`，否则回 `E_CONFLICT`（host taken）；或改为按项目过滤查询（S）。
- 状态：已亲验

## 5. 交叉观察（不在本模块结论内，供其他模块/Lead 去重）

1. **scope 模型缺 Team 维度是 H1/H2/M2 的共同根因**：`Scope` 只有 `resource:action`，没有"作用域"（Team/平台）维度；所有平台资源与身份资源都靠内建角色模板"恰好只发给平台 owner"来隐性保证，而模板是全局可授的。修法涉及 ADR（建议 R14/R01/R15 一起裁决）。
2. **`platform` scope 语义过载**：`EnrollNode`（集群钥匙）与 `SetChangeFreeze/LiftChangeFreeze/ListChangeFreezes`（Team 级治理）共用 `platform:*`（`governance.proto:33-57`）；一旦收窄 platform scope，team 级 freeze 需要新的 `governance` 资源词。归 R01/R14。
3. **relay 鉴权模型**：`/v1/relay` 仅校验 swarm join token，节点身份由 hello 自报 `carrier_node_id`（`gateway_exec.go:78-131`；`engine/execrelay.go:20-46` 只按该 ID 查 nodes 行）。任何 join token 持有者可顶替任意节点 relay、收/发他队 exec 会话帧并顶掉既有 relay。ADR-0049 把它归为"集群成员权等价"的可接受边界；与 H1 叠加后，Team admin 即可拿到该能力。R06/R14 请评估是否需要在节点/中继侧加 per-node 凭据。
4. **`GetStatus` 的 Provider 健康检查无缓存**：`systemgrpc.go:75-115` 每次请求全量探活（docker info/k3s api/TCP/HTTP），并发 9 goroutine；若 `Health` 实现忽略 ctx，超时分支的探活 goroutine 会滞留（`done` 有缓冲不阻塞，但 goroutine 不回收）。R07（Provider Health 实现）与 R09（server 装配）可核对。
5. **`Login` 铸出的 "password session" Token 无 TTL**（`identity.go:141-144`、`identity.proto:445-455` 无 expires_at 字段）：密码登录的会话永久有效，只能吊销；结合 H2 放大后果。多租户/会话管理批归 R14。
6. **ADR-0035 决策 5 的「identity 目录显式不盖」是记录在案的债务**（`docs/adr/0035-row-level-team-authorization.md:22`），H2 的密码/登录面是 ADR 之后新增的 C6 面；R15 核对 checklist 时应确认是否有意留白。
7. **观测面挂账**：ADR-0041（`:28,:62`）承认 VM 单租户与「多租户启用前收窄」，M2 是它的 API 侧投影；R15 可据 ADR 原文判定"挂账是否仍诚实"。

## 6. 测试与守卫质量评估

**强项**
- 注册/策略漂移有 AST 级双向守卫（`gatewayguard`、`grpcguard`、`scopeguard`），且带「扫描面偏小即失败」的防盲断言与豁免双向保鲜；`AssertAllRegisteredHavePolicy` 在生产与 apitest 双侧 fail-closed。
- 写路径有 `acceptanceguard`（创建/删除型 handler 必经 commit，AST 反扫）与 `factdual`（双真旗必须带理由注释）；本模块没有一条绕过。
- 错误面有 `errcodeToHTTP` 全量注册表 + 反向保鲜（`errors_test.go`）；`apperrstatus`/`exit.go` 单点脱敏有强断言（`assembly/grpc_test.go:91-133` 钉链头与日志）。
- 行级授权有 `teamauth_test.go` 的真实两 Team 矩阵（team B admin token 打 team A 资源），及 `events_team_test.go`（SSE 不串队）、`exec_boundary_test.go`（跨队抢附着拒绝）等专项；apitest 夹具复用生产同一条拦截器链与收包限额（`harness.go:157-177`）。

**盲区（本模块暴露的正是这些）**
- **没有「取行 handler 必须调用行级授权」的守卫**：`authz.go` 的 authorize 族是手工调用约定，没有 AST/反射反扫；H1/H2 与 ListNodeContainers 都属于「只要有人漏调就静默通过」的类别。建议新增守卫：对声明了全局 ID/（project/app/team）入参的 SERVER 方法，方法体必须出现 `authorize*`/`requirePlatformAdmin`/`callerTeam` 调用或登记豁免（AST，成本 M）。
- **内建角色 × scope 面没有禁止矩阵测试**：`authz_test.go:84-90` 只测 member/owner；builtin-admin（每 Team 都有）的 platform/nodes/users/teams 能力从未被负例钉住。
- **identity 密码/登录链零测试**：全仓无 `SetUserPassword(` / `UsersService.Login` 的 apitest；ADR-0035 的跨队矩阵只覆盖 CreateToken/CreateProject。
- **无分页惯例守卫**：ADR-0026 的 `after_* + limit` 在 8+ 个 List 面缺失，没有守卫对账（而幂等/冻结/scope 方向都有）。
- **无必填注解守卫**：85/153 请求无 protovalidate，P4-1/P4-2 的"迁移到 descriptor"只覆盖了原本有手写检查的路径。
- **长动词超时清单无守卫**：`longRunningUnary` 是手写 map，新增同步长动词（如 TriggerPlatformBackup）没有"必须显式分类"的反扫（对照 `freezeguard_test.go:33-88` 的方法分类范式）。
- 测试结构本身不错（apitest 用真 SQLite + 假 Runtime、与生产同链），问题不是设施弱，而是**覆盖的负面矩阵窄**（只测 member/owner、只测一个方向）。

## 7. 未决与置信度

**最不确定的 3-5 点**
1. **H1 的"Team admin 是普通角色"这一前提**：代码与 ADR-0035 决策 2 都支持（他队 user 的 builtin-owner 在该队只是管理员；team X 的 admin 是 team X 管理员），但若产品方实际把所有 team 的 admin 都视为平台运维，则 H1 的威胁模型需下调（我仍认为 scope 面与平台面应有物理门）。置信：高（代码事实）/ 产品意图：中。
2. **H2 的完整提权链需要"default Team 存在 admin 用户"**：跨 Team 用户接管（重置 Y 队用户密码并登录）不依赖它，确定性高；链到平台 owner 依赖该前置（owner 用 `CreateUser` 建 admin 用户是文档化路径，`identity_test.go:37` 即此形态）。置信：中高。
3. **M9 的 Provider 健康检查 goroutine 泄漏**：取决于各 Provider 的 `Health(ctx)` 是否真正响应 ctx（R07 范围）；我读了 vlogs/s3/local 的 Health 实现是短路径，但 docker/k3s 的 SDK 调用未逐一验证取消传播。结论只对"2s 超时后仍有 goroutine 在跑"这一事实成立。置信：中。
4. **M7 的实际放大倍率**：SQLite 单写者 + 250ms 拍的实际容量未压测（代码注释也只给"≈50 并发"无实测）；我确认的是"无上限"这一事实与机制，不是具体吞吐数字。置信：高（机制）/ 具体影响：中。
5. **R08 契约设计质量的主观判断**：153 RPC 的 scope/path/分页风格整体一致，但「删除=admin vs write」「PUT 集合体 vs 路径寻址」（`PutSecret`/`PutConfig` 对 `DELETE /.../{project_id}/{name}`）等 REST 惯例混用是历史累积；我按"可发现性/一致性"报为 L，Lead 若认为属于风格偏好可降级。置信：中。

**整体置信度：高**。
- 已亲验：全部 H/M/L 的代码位置、调用链与守卫/测试覆盖对我报告的每条都是当场读过（含依赖包 authn/roles/anchor/outbox）；本机可运行测试全绿，说明我所依赖的代码路径处于可工作状态。
- 未亲验（环境限制）：①无法运行 `generate:verify` 做生成物零漂移实证（改为 git 历史抽样：近 8 个涉及 proto 的提交都同批再生成 `genproto`，生成头与 mise 钉版一致）；②无法真机/容器验证 relay/exec/browse 原生入口；③无测试覆盖的场景（H1/H2）无法用现成测试复验——这本身是我报告的一部分（守卫盲区），不是证据缺口。
