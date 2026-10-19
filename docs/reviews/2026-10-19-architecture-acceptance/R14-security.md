# R14 横切：安全、授权、密钥与多租户隔离 评审报告

> 评审基线：`fleetly` HEAD `35257f18`（2026-10-19 快照，只读）。主审 `internal/application/{authn,idem,governance}/**`、
> `internal/assembly/policy.go`；跨读 identity 域内核、`interfaces/grpc/**` 授权面、`assembly` 原生入口、providers 的
> exec/relay/keyring、`install.sh` 与 config 默认值。全程零仓库改动（临时实验未落盘：无法在仓外编译引用 `internal/` 的探针）。
> 本机复核命令（只读）：`go test ./internal/domain/identity/... ./internal/application/{authn,governance,idem}/...` → 全绿；
> `go test ./internal/testsupport/apitest -run 'TestRowLevelTeamAuthorization|TestScopeEnforcementMatrix' -count=1` → 全绿
> （确认本报告引用的跨队夹具——他队 `builtin-admin` Token——是**真实可用身份**，而非纸面构造）。
> 两件指定裁决：见 §4.3（platform 轴）与 §4.8（Metrics 透传）。

---

## 1. 覆盖清单

**完整读过（生产文件）**
- `internal/application/authn/{interceptor.go,seed.go,bootstrap.go}`（逐行）
- `internal/application/idem/enforcer.go`（逐行）
- `internal/application/governance/{freeze.go,ratelimit.go,freezestream.go}`（逐行）
- `internal/assembly/{policy.go,grpc.go,gateway_exec.go,proxyconfig.go}`（逐行）
- `internal/domain/identity/{scope.go,roles.go,token.go}` + `identity/**` 各聚合 repo 接口
- `internal/interfaces/grpc/fleetlygrpc/{authz.go,acceptance.go,identity.go,invitations.go,contexts.go,platform.go,governance.go,exec.go,uploads.go,eventtickets.go,delivery.go,webhook.go,hooks.go,alerting.go(1-130)}`
- `internal/interfaces/grpc/{systemgrpc/systemgrpc.go,apivalidate(抽样),apperrstatus(未读)}`
- `internal/infrastructure/keyring/keyring.go`、`internal/domain/services/material/material.go`
- `internal/infrastructure/providers/swarm/{exec.go,runtime.go(Enrollment/describe),noderelay.go(抽样)}`、`.../victoriametrics/provider.go(QuerySeries)`
- `internal/application/engine/{execrelay.go,buildsource.go(执行面/上传解包),platformbackup.go(1-360)}`
- `internal/infrastructure/persistence/{idempotency/repo.go,membership/repo.go,audit/repo.go,migrations/00007,00018}`
- `proto/fleetly/{identity/v1,runtime/v1,system/v1,delivery/v1/templates}/**.proto`（授权注解全量核对）
- `install.sh`（逐行）、`internal/config/defaults.go`、`internal/config/config.proto`（绑定/auth 字段）
- `cmd/fleetly/cmd/{init.go,idem.go}`（凭据链与幂等键来源）

**抽样读过**
- `internal/testsupport/apitest/{authz_test.go,teamauth_test.go,teamaxis_test.go,events_team_test.go,platform_test.go,exec_test.go}`
- `internal/application/governance/freeze_test.go`、`internal/domain/identity/identity_test.go`
- `internal/application/authn/{narrowing_test.go 未逐行}`（结论以 interceptor.go 的实现为准）
- `internal/infrastructure/providers/traefik/config.go`（快照内容面，仅 38-60/96-160 段）

**未覆盖/无法覆盖**
- Console 前端 token 存取（localStorage/XSS）→ R11 主审，本报告不重复。
- Browse ForwardAuth 跨会话越权（R04-H1 已报，本报告只做交叉确认，不重复计数）。
- 真机复验：Docker daemon 本机不可达（PROFILE §1.1），`/v1/relay` 劫持、`EnrollNode` 轮换断服均**未做动态复现**，结论为静态全链证据。
- 漏洞 DB/CVE（不要求联网）未查。
- `apitest` 全量跑（约 10k 行夹具）未整包执行，只读断言代码。

---

## 2. 模块画像与边界

| 面 | 职责 | 信任边界 |
|---|---|---|
| `application/authn` | Bearer 解析（sha256 查表）、scope 执法、Identity 进 ctx、creator 实时收窄（ADR-0038）、bootstrap 首启 | **唯一认证点**；PUBLIC 面按匿名放行 |
| `application/idem` | `Idempotency-Key` → 请求指纹 → 响应引用（24h 重放） | 位于 authn 之后，**不含调用方身份** |
| `application/governance` | change freeze 拦截器（按资源 Team）+ per-Token 创建限速 | 冻结寻址失败即放行 |
| `interfaces/grpc/fleetlygrpc/authz.go` | 行级 Team 授权唯一真源（ADR-0035）；`owner` 判定 = `RoleID==builtin-owner && TeamID==default` | 只有平台 owner 跨队 |
| `assembly/policy.go` | proto 注解 → PolicySet；`ScopeResources` 词表（含 `platform`） | 启动期 fail-closed 断言 |
| 原生入口（`gateway_exec.go`/`proxyconfig.go`） | `/v1/relay`、`/v1/exec/stream`、`/v1/platform/binary`、`:9082/proxy/config` | 不走 gRPC 拦截器链，自带鉴权 |

**与声明的偏差（带证据）**
1. `assembly/policy.go:43-44` 声明 "`platform` 是集群面资源（C3）：… 独立于 nodes 运维面（… join 材料不可逆——白送即失守）"；
   实际 `builtin-admin` 角色天然持有 `platform:admin`（`domain/identity/roles.go:74-79` 对词表每个资源发 `:admin`），
   而任何 Team 都可授予内置角色（`acceptance.go:263-278`）。**声明与机制相反**，见 H2。
2. ADR-0035 决策 5 "目标 Team 缺省 = 调用方 Team" 在 `CreateUser/CreateRole/CreateInvitation` 三处退化
   （handler 在 `resolveTargetTeam` 之前预填 `default`），非 default Team 的 admin 省略 `team_id` 即 403，见 M4。
3. ADR-0036 N2 兑现 §1 把 `server.proxy_config.auth_token` 定为"多租户启用前置项 = 置值"；多租户（Team/Invitation）
   已在 main 落地，而缺省仍空、`:9082` 仍通配绑定，见 M6。

---

## 3. 优点（带证据）

1. **认证链 fail-closed 且纵深充分**：无策略方法拒并大声记日志；SIG 分诊不把存储故障伪装成"凭证无效"；
   `E_INTERNAL` 走出口脱敏单点。
   `internal/application/authn/interceptor.go:168-212`、`:224-233`。
2. **Token 收窄语义落地为纯域算子并逐请求求交**（ADR-0038）：`Meet` 阶梯降档、`*` 收敛、空交集 403 带 `reason`
   + 60s 节流审计；同角色走免读快路径。`interceptor.go:286-329`、`domain/identity/scope.go:160-212`。
3. **票证面设计干净**：purpose+payload 绑定、单用途即删、失败兑换不烧票（防错会话试兑 DoS），
   SSE 票把可见域**铸在票据里**（兑换面无 ctx 时的正确做法），并有跨队不泄漏的验收测试。
   `interfaces/grpc/fleetlygrpc/eventtickets.go:65-105`、`contexts.go:326-341`、`apitest/events_team_test.go:195-224`。
4. **exec 会话有行级门 + 票据双径同律**：gRPC 直连消费径显式 `teamAuthorized(info.TeamID)`（会话 ID 可枚举，
   这是唯一闸），Console 票据径在签发时已鉴权。`fleetlygrpc/exec.go:188-203`。
5. **webhook 验签形态正确**：`hmac.Equal` 常量时间、签名格式先校验长度、失败文案不区分"不存在/已删/错签"
   且不带 token 材料；审计覆写（actor/source）**先于**冻结注入（时序 bug 已修）。`webhook.go:85-95`、`:139-147`。
6. **Secret 面结构性止血**：写侧只有 `Cipher.Produce`（密文+指纹同源）、读侧只回指纹，`SealedValue.String()` 只露指纹；
   KEK 0600/目录 0700、退役 key 只进解封面。`domain/services/material/material.go:90-114`、`keyring/keyring.go:38-62`。

---

## 4. 发现清单（按攻击面组织）

### 4.1 认证与账户面

#### R14-H1｜`SetUserPassword` 无 Team/owner 门 + `ListUsers` 全平台目录 → 任一 Team 的 admin 可接管任意用户（含平台 owner）
- 维度：安全（认证绕过 / 多租户隔离）
- 严重度：**H** — 跨租户账户接管 + 可升级为平台 owner 接管；链路全部由已认证低权租户 token 起步，无需任何
  预置机密，是本次评审唯一"两跳拿全平台"的链。
- 位置：`internal/interfaces/grpc/fleetlygrpc/identity.go:174-197`（`SetUserPassword`）、`:204-214`（`ListUsers`）、
  `:120-170`（`Login`，`memberships[0]` 在 `:136`）；`proto/fleetly/identity/v1/identity.proto:45-48,51-57`；
  `internal/domain/identity/roles.go:74-79`；`internal/interfaces/grpc/fleetlygrpc/authz.go:36`
- 证据：
  ```go
  // identity.go:174-197 —— 唯一受理检查是"用户行存在"，没有 Team 比对、没有 owner 门
  func (svc *UsersService) SetUserPassword(ctx, req *identityv1.SetUserPasswordRequest) (...) {
      hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
      ...
      err = svc.s.commit(ctx, writeFact{
          checks: []acceptanceCheck{svc.s.userExists(req.GetUserId())},
          write: func(ctx context.Context, st transact.Stores) error {
              return st.Users.SetPassword(ctx, req.GetUserId(), string(hash))
          },
  ```
  ```go
  // identity.go:204-214 —— ListUsers 无 Team 过滤（ADR-0035 决策 5 显式不盖 identity 目录读面）
  func (svc *UsersService) ListUsers(ctx context.Context, _ *identityv1.ListUsersRequest) (...) {
      list, err := svc.s.Users.List(ctx)          // 全平台
  ```
  ```go
  // identity.go:136-144 —— Login 用首个 membership 铸 Token（PUBLIC 档，无限速）
  m := memberships[0]
  t := &token.Token{Name: "password session", TeamID: m.TeamID, UserID: u.ID,
      RoleID: m.RoleID, SHA256: material.SHA256, Prefix: material.Prefix}
  ```
  ```proto
  // identity.proto:51-57 —— 门槛是 users:write（builtin-admin 的 users:admin 蕴含 write）
  rpc SetUserPassword(SetUserPasswordRequest) returns (SetUserPasswordResponse) {
    option (grpcapi.v1.method_auth) = { access: ACCESS_LEVEL_SERVER
      api_key_scope: { resource: "users" op: SCOPE_OP_WRITE } };
  ```
- 攻击链（前提 → 后果）：
  1. 前提：平台存在 ≥2 个 Team（ADR-0028/0035 的产品形态），攻击者持有**他队 admin token**
     （`builtin-admin`：`users:admin`/`users:read` 均在手；`apitest/teamauth_test.go:75` 就是这种夹具）。
  2. `ListUsers`（users:read）→ 拿到 default Team 全部用户的 `id` 与 `name`（含平台 owner 用户，
     若其角色为 `builtin-owner`）。
  3. `SetUserPassword{user_id: <victim>, password: "x"}` → **覆写**受害者密码（无需旧密码；无 Team 校验、无 owner 门、
     无 confusable 保护）。
  4. `Login{name: <victim>, password: "x"}`（PUBLIC，无限速无锁定）→ 得到 `TeamID=victim.TeamID`、
     `RoleID=victim.RoleID` 的 Token。
  5. 后果：若 victim 角色为 default Team 的 `builtin-owner`，则 `callerTeam` 返回 `owner=true`
     （`authz.go:36`）→ **平台管理员**（跨队全通、`requirePlatformAdmin` 面全开、EnrollNode/RestartComponent 可用）。
     若 victim 只是 default Team 的 `builtin-admin`（`fleetly init` 的产物就是这种，见 `cmd/fleetly/cmd/init.go:77-79`），
     攻击者仍完成 **team B → default Team 的跨租户提权**（默认队承载平台主租户的全部资源）。
  6. 同一缺陷族：`DeleteUser`（`identity.go:218-250`，`users:admin`）同样无 Team/owner 门 → 跨租户删用户
     （FK 会挡住"名下有 Token"的用户，故对 owner 是 DoS 无效，但对其他队普通用户直接生效）；
     `GetUser`（`:107-113`）同样无过滤。
- 影响：任何 Team 的 admin ≈ 平台所有用户账户的密码控制权；在有 `builtin-owner` 用户的部署上等价平台接管。
  取证面也弱：`user.password_set` 是 `LedgerOnly` 审计（无事件孪生），受害队的事件流看不到改密动作
  （`identity.go:186-196`）。
- 建议（机制层，而非逐点补）：
  (a) `SetUserPassword` 受理位加 `requirePlatformAdmin(ctx)` 或"本人 or 同队 admin"三元判定
      （需要先有"用户→Team"解析助手；`Memberships.ListByUser` 已够用）；
  (b) `DeleteUser`/`GetUser`/`ListUsers` 纳入 ADR-0035 决策 5 的行级面（至少 ListUsers 按成员队过滤；
      平台 owner 全见）；
  (c) 新守卫：`TestIdentityAdminMutationsRequireRowAuthz`（AST 反扫 `identity.go` 的写 handlers 是否引用
      `requirePlatformAdmin`/`teamAuthorized`），与 `scopeguard` 同构；成本 S（三处 handler + 一条守卫）。
- 状态：**已亲验（静态全链）**——handler/受理检查/scope 注解/角色种子/Login 铸 Token 路径逐行核对，且
  `TestRowLevelTeamAuthorization` 通过证明"他队 builtin-admin Token"是可用身份；
  未做动态复现（无法在仓外编译 `internal/` 探针，且不得在仓内建文件）。测试为何没拦住：`teamauth_test.go`
  只覆盖"资源行"族（App/Secret/Task/…）与 `CreateToken` 的目标队校验，**identity 目录面与改密面完全无跨队用例**。

#### R14-M1｜`Login` 是 PUBLIC 且不在限速面 → 密码面可无限爆破/撞库
- 维度：安全（认证）
- 严重度：M — 无锁定、无限速、无失败计数；控制面默认 `:9080/:9081` 通配绑定（见 M3 同族），VPC 内任一主机可打。
  叠加 H1 后，攻击者只需一次成功 `Login` 即成。
- 位置：`internal/application/governance/ratelimit.go:64-78`（只对 `idem.EnforcedMethods` 计数）、
  `internal/application/idem/enforcer.go:48-97`（`Login` 不在表内）、`identity.proto:42-48`、`identity.go:115-128`
- 证据：
  ```go
  // ratelimit.go:64-78 —— 限速面 = 幂等执法面；Login 两者都不在
  func (l *RateLimiter) Unary() grpc.UnaryServerInterceptor {
      return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
          if !idem.EnforcedMethods[info.FullMethod] {
              return handler(ctx, req)
          }
          id, ok := authn.FromContext(ctx)
          if !ok || id.TokenID == "" {
              return handler(ctx, req)   // 匿名/公开面零计数
          }
  ```
  ```go
  // identity.go:115-128 —— 注释自认"无限速是多租户前诚实边界"
  // 失败形态不区分哪半边错（用户名枚举面收窄）；PUBLIC 端点无限速是多租户前诚实边界
  u, err := svc.s.Users.GetByName(ctx, req.GetName())
  if err != nil || u.PasswordHash == "" ||
      bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.GetPassword())) != nil {
  ```
- 影响：口令爆破/撞库；bcrypt cost 10 只降低速率，不构成锁定。注释里的"多租户前"前提已被推翻（多租户在 main）。
- 建议：Login 走独立 per-name+per-IP 固定窗（复用 `RateLimiter` 形态，键 = 归一化用户名 + 客户端地址），
  超限仍返回统一 `E_UNAUTHENTICATED` 语义（避免枚举）+ 失败计数进审计（source=api）。成本 S。
- 状态：已亲验。

#### R14-M2｜`fleetly init` 之后平台可能不存在任何 `builtin-owner` 身份——平台级功能（Global GitHub App/全局冻结/跨队管理）永久不可达
- 维度：功能 / 架构一致性（治理可用性）
- 严重度：M — 不是越权而是"合法权力无人持有"：`init` 用 `builtin-admin` 建 admin 用户并**默认吊销**唯一 owner Token。
- 位置：`cmd/fleetly/cmd/init.go:77-85`（RoleAdminID）、`internal/application/authn/bootstrap.go:75-79`（RoleOwnerID 仅 bootstrap）、
  `internal/interfaces/grpc/fleetlygrpc/authz.go:36`（owner 判定）、`:61-71`（`requirePlatformAdmin`）
- 证据：
  ```go
  // init.go:77-85 —— admin 用户与 CLI Token 都是 builtin-admin，不是 owner
  user, err := c.Users.CreateUser(auth, &identityv1.CreateUserRequest{
      Name: userName, RoleId: identity.RoleAdminID,
  })
  tok, err := c.Tokens.CreateToken(auth, &identityv1.CreateTokenRequest{
      Name: userName + "-cli", RoleId: identity.RoleAdminID, UserId: user.GetUser().GetId(),
  })
  ```
  ```go
  // authz.go:36 —— 平台管理员 = default Team 的 builtin-owner，非"admin 角色"
  platformAdmin := id.RoleID == identity.RoleOwnerID && id.TeamID == identity.DefaultTeamID
  ```
- 影响：默认安装（`init` 不带 `--keep-bootstrap`）后，无人能写 Global GitHub App（`githubapps.go:224` 等
  7 处 `requirePlatformAdmin`）、无人能设全局冻结（`governance.go:49-54`）；且**无法自愈**：只有 owner 能在
  default Team 建用户（`resolveTargetTeam`），而 owner 已不存在 → 只能重建库或回滚 bootstrap。
- 建议：`init` 增 `--platform-owner`（或默认建 owner 用户 + 同事务铸其 Token，再吊销 bootstrap）；
  文档与 runbook 明确"owner 身份的载体"。成本 S。守卫：`TestInitLeavesPlatformOwner`（apitest：init 后 WhoAmI 的
  role 必须能过 `requirePlatformAdmin`）。
- 状态：已亲验（代码路径 + 权限判定）；`--keep-bootstrap` 形态可用是唯一逃生门。

### 4.2 集群成员权与节点面

#### R14-H2｜`platform:admin` 不隔离平台权力：任一 Team 的 admin/owner 都持有它 → 可铸/轮换 join 材料、重启全平台受管组件（裁决 ①）
- 维度：安全（授权模型缺陷 / 多租户隔离）
- 严重度：**H** — 活 join token ≡ 集群成员权（代码自己的 C3 口径），而它被"每队 admin"白送；
  同一 scope 还打开 `RestartComponent`（全平台断服）与 Platform Backup/模板目录刷新。ADR-0035/0038 的
  "能力面与行级正交"在这里恰好失效：`platform` 是**没有行级轴**的资源，scope 就是全部门。
- 位置：`internal/assembly/policy.go:43-52`；`internal/domain/identity/roles.go:70-92`；`internal/domain/identity/scope.go:104-118`；
  `internal/application/authn/interceptor.go:193-205`；`proto/fleetly/runtime/v1/runtime.proto:43-53`；`system.proto:44-50,75-81`；
  `delivery/v1/templates.proto:58-67`；handler 无门：`fleetlygrpc/contexts.go:82-113`、`systemgrpc/systemgrpc.go:120-141`、
  `fleetlygrpc/platform.go:25-31`；反例（有门）：`githubapps.go:224` 用 `requirePlatformAdmin`
- 证据：
  ```go
  // roles.go:73-79 —— admin = 对词表"每个"资源发 :admin；词表含 platform ⇒ builtin-admin 持 platform:admin
  for _, res := range sorted {
      if res == "audit" { admin = append(admin, Scope{Resource: res, Action: "read"}) } else {
          admin = append(admin, Scope{Resource: res, Action: "admin"}) }
      if memberExcludedResources[res] { continue }   // 只有 member 被排除 platform
  ```
  ```go
  // policy.go:43-51 —— 自己的注释宣称 platform 是为了"不白送集群钥匙"，与上面的种子直接矛盾
  // platform 是集群面资源（C3）：EnrollNode 的活 join token 等价集群成员权，
  // 独立于 nodes 运维面（drain/cordon 可逆，join 材料不可逆——白送即失守）。
  "users", "teams", "roles", "tokens", "invitations", "audit", "platform",
  ```
  ```go
  // interceptor.go:193-205 —— 唯一执法是 scope 求值；没有"平台行"维度
  rule := a.policy.ScopeRule(fullMethod)
  ...
  if !id.scopeSet.Satisfies(*rule) {
      return nil, apperr.New("E_FORBIDDEN", "token %q lacks required scope %s:%s", ...)
  }
  ```
  ```go
  // contexts.go:82-113 —— EnrollNode 直接下发 join_command/relay_command，无 requirePlatformAdmin
  func (svc *NodesService) EnrollNode(ctx, req *runtimev1.EnrollNodeRequest) (...) {
      action := "node.enroll"
      if req.GetRotate() { action = "node.rotate_join_tokens" }
      ...  // 只有审计前置，没有平台 owner 判定
      kit, err := svc.s.Runtime.Enrollment(ctx, req.GetRotate(), capability.EnrollmentOptions{GatewayPort: ...})
      return &runtimev1.EnrollNodeResponse{JoinCommand: kit.Command, RelayCommand: kit.RelayCommand}, nil
  ```
- 攻击链与后果（他队 admin = `builtin-admin` 成员）：
  1. **铸 join 材料**：`EnrollNode{}` → worker join token 明文（`swarm/runtime.go:644-653`）。持有它即可
     `docker swarm join` 把自有节点插入集群；节点上的 docker daemon 使其可枚举/接入**全部 overlay 网络**，
     调度到的容器拿到对应租户的 secret env → 跨租户数据面进入。
  2. **轮换即全平台断服**：`EnrollNode{rotate:true}` → `SwarmUpdate{RotateWorkerToken,RotateManagerToken}`
     （`swarm/runtime.go:613-626`）→ 全部已部署中继的 `--join-token` 立刻失效（`/v1/relay` 与
     `/v1/platform/binary` 的鉴权对照是"活 join token"，`swarm/exec.go:80-92`）→ 所有节点 relay 重连 401，
     exec 面全平台停摆，直到运维逐节点重跑 `RelayCommand`。`EnrollNode` **不在幂等/限速面**
     （`idem.EnforcedMethods` 无此方法），可无限重复。
  3. **重启受管组件**：`RestartComponent{name:"traefik"|"zot"|"victorialogs"|"victoriametrics"}`（`platform:admin`，
     `systemgrpc.go:120-141` 无门）→ 全平台路由/镜像仓/日志/指标断服；k3s 侧组件重启会重建 ServiceAccount 等材料。
  4. **平台备份与模板目录**：`TriggerPlatformBackup`（`platform:write`，`platform.go:25-31` 无门）可反复触发
     restic 全量快照（数据根 + 远端仓写入放大）；`RefreshTemplates`（`templates.proto:61-67`）可改写**全平台**
     模板目录（跨租户的一键部署输入面）。
  5. **为什么"owner 才该有"没生效**：ADR-0035 的平台例外只实现在 `callerTeam()` 行级判定上（资源有 Team 轴），
     而 `platform` 资源没有行 → 不存在行级比对；`requirePlatformAdmin` 这个正确的门只被 GitHubApp 用了，
     Nodes/System/Platform/Templates 四族都没用。
- 影响：单租户部署无感；多租户部署里"邀请进来一个队管理员"≈ 给出集群半权（节点加入能力 + 全平台可用性开关）。
  这正是 C3 想避免而机制没做到的那件事。
- 建议（机制层，一次修四族）：
  (a) 在 `EnrollNode`/`RestartComponent`/`TriggerPlatformBackup`/`ListPlatformBackups`/`RefreshTemplates` 五处受理位
      加 `svc.s.requirePlatformAdmin(ctx)`（`RefreshTemplates` 在 templates handler，其余按包内 Services）；
  (b) 更好：把 `adminExcludedResources` 概念补上（`platform` 不进 `builtin-admin`，只留 `owner` 的 `*`），
      使 scope 层就表达"平台资源不是 admin 面"——一处改动覆盖未来新增 RPC；
  (c) 守卫：`TestPlatformScopedMethodsRequirePlatformAdmin` —— 以 `method_auth.resource=="platform"` 为源，
      对每个方法断言其 handler 文件出现 `requirePlatformAdmin`（AST 或文本反扫 + 豁免表），与 idem/scopeguard 同构。
  成本 S（(a)+(c)）或 M（(b) 含种子与测试面调整）。
- 状态：**已亲验（静态全链）。裁决 ①**：按 ADR-0035/C3 判——C3 的意图是"join 材料只有平台 owner 能给"，
  当前实现**不满足**该意图：`platform:admin` 由 `builtin-admin` 与每队 owner 的 `*` 恒满足
  （`*` 在库层恒真：lynx `authz.ScopeSet.Satisfies` 对 `"*"` 直接返回 true；`DotForm` 见 `scope.go:106-118`）。
  他队 admin **可以**铸 join 材料、可以排空/封锁共享节点。测试为何没拦住：`apitest/authz_test.go:84-89` 只钉了
  "member 被拒 / owner 通过"，**没有一条"他队 admin 被拒"**；`identity_test.go:151-209` 的 `TestBuiltinRoles`
  用不含 `platform` 的词表，天然测不到这个扩张。

#### R14-M3｜节点运维三动词（drain/cordon/uncordon）只按 `nodes:write` 门禁、节点行无 Team 轴 → 他队 admin 可排空共享节点（跨租户可用性）
- 维度：安全（多租户隔离 / 可用性）
- 严重度：M — C3 明确把可逆运维面定为 `nodes:write`（决策在案），所以这是"决策后果未经跨租户评估"，
  不是实现走偏；但冲击是跨租户的：drain 会驱逐该节点上**所有队**的载体。
- 位置：`proto/fleetly/runtime/v1/runtime.proto:54-76`；`internal/interfaces/grpc/fleetlygrpc/contexts.go:116-179`；
  `internal/domain/identity/roles.go:74-79`（admin 的 `nodes:admin` ⊇ write）
- 证据：
  ```proto
  // runtime.proto:56-68 —— 可逆运维面 = nodes:write（任何 builtin-admin 都有）
  rpc DrainNode(DrainNodeRequest) returns (DrainNodeResponse) {
    option (google.api.http) = { post: "/v1/nodes/{node_id}/drain" body: "*" };
    option (grpcapi.v1.method_auth) = { access: ACCESS_LEVEL_SERVER
      api_key_scope: { resource: "nodes" op: SCOPE_OP_WRITE } };
  ```
  ```go
  // contexts.go:153-165 —— nodeAdmin 只做子面协商与错误映射，没有 Team/归属判定
  admin := capability.FacesOf(svc.s.Runtime).Admin
  if admin == nil { return apperr.New("E_RUNTIME_FACE_UNSUPPORTED", ...) }
  if err := op(admin, nodeID); err != nil {
  ```
- 影响：`ListNodes`（nodes:read，member 亦有）给出全部 `platform_id/carrier_id/hostname`；他队 admin 选定节点
  `DrainNode` → 该节点上所有队的载体被驱逐/重排（有卷钉住的直接 Pending）；`CordonNode` → 新调度被拒，
  他队的部署失败/卡 preparing；`UncordonNode` 可解冻。审计只留一行 `node.drain`（无 Team 轴，落 `''` 平台级）。
- 建议：给 drain/cordon 加 `requirePlatformAdmin`（与 H2 同批），或引入节点归属（Node→Team）后在
  `nodeAdmin` 里做 `teamAuthorized`；若维持现状，至少在 ADR 里写明"任何队 admin 可影响全集群调度"这一诚实边界，
  并让 `nodes` 资源从 `builtin-admin` 的 admin 面降到"平台专属"（同 H2(b)）。成本 S/M。
- 状态：已亲验（注解 + handler + 角色种子 + ListNodes 暴露面）。

#### R14-H3｜`/v1/relay` 只校验**共享** join token，`hello.carrier_node_id` 由客户端自报 → 可顶替任意节点的中继，劫持/中断他队 exec 会话
- 维度：安全（认证 / 多租户隔离 / 可用性）
- 严重度：**H** — 凭证是集群级共享串，节点身份由连接方声明；附着即**替换**在连中继并终止在途会话。
  与 H2 串联时，任一队 admin ≈ 可监听/伪造任意节点上的 exec 交互（含交互式 shell 的 stdin 明文）。
- 位置：`internal/assembly/gateway_exec.go:76-132`（`serveRelay`）；`internal/application/engine/execrelay.go:20-60`（`RelayAttach`）；
  `internal/infrastructure/providers/swarm/exec.go:77-92`；`proto/fleetly/runtime/v1/runtime.proto:80-96`（`carrier_id` 在 ListNodes 可见）
- 证据：
  ```go
  // gateway_exec.go:81-108 —— 先验共享集群 token，再接受客户端自报的节点身份
  if err := eng.ExecValidateClusterToken(r.Context(), bearerToken(r)); err != nil { 401 }
  ...
  var hello capability.RelayHello
  if err := json.Unmarshal(helloRaw, &hello); err != nil || hello.Type != "hello" || hello.CarrierNodeID == "" { ... }
  token := bearerToken(r)
  feed, detach, err := eng.RelayAttach(ctx, token, hello.CarrierNodeID, hello.RelayVersion, &wsRelayWriter{conn: conn})
  ```
  ```go
  // execrelay.go:28-55 —— 只按 carrier 反查平台节点；随后"顶替旧连接"，在途已附着会话被收口
  n, err := e.repos.Nodes.ByCarrier(ctx, carrierNodeID)
  if err != nil { return nil, nil, fmt.Errorf("%w: carrier %s", ErrExecNodeUnanchored, carrierNodeID) }
  a := &relayConn{nodeID: n.PlatformID, version: relayVersion, w: w}
  e.exec.mu.Lock()
  old := e.exec.relays[n.PlatformID]
  e.exec.relays[n.PlatformID] = a          // 直接替换
  ...
  for _, s := range attached { e.exec.finish(s, relayGoneFrame(nil)) }   // 已附着会话被终止
  ```
  ```go
  // swarm/exec.go:88 —— 接受 worker OR manager；用 == 逐个比较（非常量时间，次要）
  if token == inspect.Swarm.JoinTokens.Worker || token == inspect.Swarm.JoinTokens.Manager { return nil }
  ```
- 攻击链（前提 → 后果）：
  1. 前提：持有活 join token（H2 给出路径；或合法拿到 `/v1/platform/binary` 的运维材料泄漏）+ 目标节点 carrier id
     （`ListNodes` 的 `carrier_id` 字段对 `nodes:read` 全开放，member 即有权）。
  2. 拨 `GET /v1/relay`，Bearer=join token；首帧 `{"type":"hello","carrier_node_id":"<受害节点>"}`。
  3. engine 判定"该节点重连"→ 替换 `relays[platformID]`，并把受害节点上已附着的 exec 会话以
     `relay_disconnected` 收口（**可用性攻击**，用户侧看到 shell 突然断开）。
  4. 之后任何发往该节点的 exec 会话（他队用户的 `fleetly exec/shell`）都被投递给攻击者的连接：
     攻击者收到 `ExecWorkload` 请求帧（workload/instance/argv）与 **stdin 帧**（交互式终端输入，可能含密码），
     并可返回任意 stdout/stderr/exit —— 伪造命令输出（诱导运维复制错误内容）。
  5. 后果：跨租户凭据窃取面（stdin 明文）+ exec 面可用性 + 输出完整性。审计面无法区分真假中继。
- 影响：exec 子面的"节点身份"事实上不是认证出来的，而是声明出来的；ADR-0049 把安全承载放在 join token 上，
  但 join token 是**集群级**的，无法表达"这个连接属于哪个节点"。
- 建议（机制层）：relay 握手改成**节点个体凭证**——`EnrollNode` 为每节点铸一次性/可续期的 relay 凭证
  （服务端存 `nodes.relay_cred_hash`，附着一个节点只能用该节点的凭证），或复用 mTLS/host-key 双因素；
  过渡期最小举措：`RelayAttach` 校验 `hello` 携带的 token 与该节点上次注册的凭证绑定（一次性 enrollment
  材料换长期 relay secret），并对"同节点二次附着"加人工可见告警（`log.Warn` + 审计行）。
  成本 M。守卫：e2e/apitest 加"用 A 的 join token 以 B 的 carrier id 附着必须 401"。
- 状态：已亲验（握手/附着/替换/收口四段代码）；未做动态复现。

### 4.3 幂等执法面

#### R14-H4｜`idempotency_records` 的键空间不按调用方隔离：跨租户可用同键同体重放，取出他队创建响应（含明文 Token/webhook secret/邀请串）
- 维度：安全（多租户隔离 / 数据安全）
- 严重度：**H** — 重放决定只比对 `method + 请求指纹`，**不含调用方身份/Team**；而落在表里的响应体包含
  `CreateToken.Secret`、`SetGitHook.Secret`、`CreateInvitation.Secret` 三类明文凭证。仓库自己在部署面上
  修过同族问题（migration 00018），但通用幂等表没跟上。
- 位置：`internal/application/idem/enforcer.go:137-159`（认领）、`:176-186`（重放）；`.../persistence/idempotency/repo.go:26-31`（按键直读）；
  `.../migrations/00007_idempotency.sql:6-15`（`idem_key TEXT PRIMARY KEY`，无 team 列）；
  对照 `.../migrations/00018_idempotency_app_scope.sql:2-7`（部署面收窄的理由原文）；密钥型响应：
  `fleetlygrpc/identity.go:496`、`hooks.go:121`、`invitations.go:93`
- 证据：
  ```go
  // enforcer.go:140-159 —— 记录读取与认领只用 key；没有 principal/team 参与
  rec, err := e.repo.Get(ctx, key)
  switch {
  case err == nil && rec.ExpiresAt.After(now):
      return e.dispatchRecorded(ctx, rec, info.FullMethod, fp)
  ...
  if err := e.repo.Claim(ctx, key, info.FullMethod, fp, ClaimTTL); err != nil {
  ```
  ```go
  // enforcer.go:176-186 —— 重放条件 = 同方法 + 同请求指纹（调用方未参与指纹）
  func (e *Enforcer) dispatchRecorded(ctx context.Context, rec *idempotency.Record, method, fp string) (any, error) {
      if rec.Method != method || rec.Fingerprint != fp {
          return nil, conflictErr("this Idempotency-Key was already used by a different request; ...")
      }
      switch rec.State {
      case idempotency.StateCompleted:
          return replay(rec)          // 原样返回首次响应体（可能含 Secret）
  ```
  ```sql
  -- 00018_idempotency_app_scope.sql:5-7 —— 同一类缺陷在部署面被识别并修过
  -- idempotency_key，服务端不加随机后缀），客户端模板复用导致跨 App 撞键
  -- 是现实形态，作用域必须由平台钉死在 App 维。
  ```
- 攻击链（前提 → 后果）：
  1. 前提：可预测的幂等键。CLI 不自造键（`cmd/fleetly/cmd/idem.go:17-25` 纯用户输入），golden 夹具用的就是
     `freeze-set`/`platform-backup-1`/`peer-declare` 这类语义词（`verbs_golden_test.go:170,223,249`）——
     真实客户端/模板同样倾向用稳定人写字串，正是 00018 描述的现实形态。
  2. 受害队 A 用 `Idempotency-Key: ci-token-1` + 体 `{name:"ci", role_id:"builtin-member"}` 调 `CreateToken`；
     响应 `secret` 落 `idempotency_records.response_body`（24h）。
  3. 攻击队 B 发同方法、同键、**同体**（`team_id` 缺省 = 各自队，故两侧请求字节相同）→ `fingerprint` 相同
     → 走 `replay` → 攻击者拿到**队 A 的明文平台 Token**（`identity.go:496` 的 `Secret`）。
  4. 同法可取：`SetGitHook` 的 webhook secret（`hooks.go:121`）、`CreateInvitation` 的邀请串
     （`invitations.go:93`，可直接兑换成队 A 的 membership）。
  5. 后置：持该 Token 直接以队 A 身份作业（若 A 的 Token 是 member → 部署/材料权限；若是 admin → 见 H2）。
- 影响：以"猜一个字符串"为前提的跨租户凭证泄露；即便键不可预测，**语义上仍是共享命名空间**（同一键被两队使用
  会互相 409 干扰，即跨租户 DoS/串台）。
- 建议（机制层）：把幂等记录键收窄为 `(principal_scope, idem_key)`，`principal_scope` 取 `Identity.TeamID`
  （无身份面用空串），或把 `TeamID+TokenID` 并入 `fingerprint`；迁移 `ALTER TABLE idempotency_records ADD
  team_id TEXT NOT NULL DEFAULT ''` + 唯一索引改 `(team_id, idem_key)`（前滚，参考 00018 的建-删序）；
  同时守扩展 `TestIdempotencyKeysArePrincipalScoped`（apitest：A/B 两队同键同体 → B 必须 409 而非重放）。成本 M。
- 状态：已亲验（拦截器/存储/迁移/响应体四处）。测试为何没拦住：`idem/enforcer_test.go` 一律单调用方，
  没有"第二个 principal 同键"的用例。

### 4.4 Team 轴贯通（缺省与冻结）

#### R14-M4｜非 default Team 的 admin 省略 `team_id` 建 user/role/invitation 一律 403（ADR-0035 决策 5 的"缺省=调用方 Team"在该三处退化）
- 维度：功能 / 多租户
- 严重度：M — 直接违反 ADR-0035 决策 5 的成文承诺；多租户部署里"队管理员给队友发邀请"这条最基本的动线断裂，
  且错误文案把调用方指向"用平台 owner"，误导运维。
- 位置：`internal/interfaces/grpc/fleetlygrpc/identity.go:56-66`（CreateUser）、`:355-366`（CreateRole）、
  `invitations.go:50-60`（CreateInvitation）；对照 `authz.go:231-246`（`resolveTargetTeam` 本已实现缺省语义）
  与正确写法 `identity.go:457-460`（CreateToken 直接传 `req.GetTeamId()`）
- 证据：
  ```go
  // identity.go:56-66 —— 先把空串填成 default，再交给 resolveTargetTeam：
  // 于是"缺省"变成一个显式的他队目标，非 owner 必被拒
  teamID := req.GetTeamId()
  if teamID == "" {
      teamID = identity.DefaultTeamID
  }
  resolvedTeam, err := resolveTargetTeam(ctx, teamID)   // target="default" != callerTeam ⇒ E_FORBIDDEN
  ```
  ```go
  // identity.go:457-460 —— CreateToken 的正确形态（不预填，交给助手归一）
  teamID, err := resolveTargetTeam(ctx, req.GetTeamId())
  ```
- 影响：team B 的 admin（`users/roles/invitations` 的 write 都在手）执行 `fleetly users create bob --role builtin-member`
  或 `fleetly users invite --role builtin-member` → `E_FORBIDDEN: target team default does not match the token's team <B>`。
  唯一绕过是显式 `--team <B id>`（CLI 默认不传）。`CreateProject`/`CreateToken` 无此问题（对照
  `structure.go:123`），正好说明这是三处复制粘贴式预填，不是设计意图。
- 建议：删掉三处预填，统一 `resolveTargetTeam(ctx, req.GetTeamId())`；并补 apitest：
  team B admin 省略 `team_id` 建 user/role/invitation 成功且落在 B（当前 `teamauth_test.go` 只测了
  `CreateToken`/`CreateProject` 的缺省，恰好漏了这三处）。成本 S。
- 状态：已亲验（三处代码 + 助手语义 + 对照点）。

#### R14-M5｜change freeze 可被"省略 `team_id`"绕过：`CreateProject` 的冻结寻址字段为空即放行
- 维度：安全（治理刹车绕过）
- 严重度：M — 冻结是事故窗的变更刹车；绕过后仍能在被冻结的队里建项目（并顺带建 default 网络）。
  其它封禁动词都以行 ID 寻址（空值 = 404 侧），只有 `CreateProject` 以请求字段寻址，因此只有它可绕。
- 位置：`internal/application/governance/freeze.go:44`（`CreateProject` 的 `field: "team_id"`）、`:160-173`（空引用即 `resolved=false`）、
  `:139-141`（放行）；`fleetlygrpc/structure.go:117-127`（handler 把空 `team_id` 归一到调用方队）
- 证据：
  ```go
  // freeze.go:160-173 —— 请求字段为空 → 视为"寻址失败" → 放行（受理位自会拒绝）
  func (g *FreezeGuard) resolveTeam(ctx context.Context, req any, scope freezeScope) (string, bool, error) {
      ref := reqField(req, scope.field)
      if ref == "" { return "", false, nil }
      team, err := g.anchor.TeamOf(ctx, scope.kind, ref)
      ...
  ```
  ```go
  // structure.go:123 —— handler 随后把空值归一到调用方 Team：项目落进被冻结的队
  teamID, err := resolveTargetTeam(ctx, req.GetTeamId())
  p := &project.Project{Name: req.GetName(), TeamID: teamID}
  ```
- 影响：队 A 被冻结（事故窗口）时，A 的任何 admin/member（`projects:write` 需 admin）用
  `fleetly projects create x`（不带 `--team`）即可建成项目 + default 网络；冻结的"全变更停摆"承诺出现单点缺口。
- 建议：`FreezeGuard` 对"目标 = 调用方 Team 的创建型动词"在 `ref == ""` 时回退到调用方 Team 解析（新增
  `freezeScope.fallbackToCaller bool`），或把 `CreateProject` 改为校验 `team_id` 必填（契约面 only-add 收窄会破坏兼容，
  前者更稳）。补测：`freeze_test.go` 新增 `CreateProjectRequest{Name:"x"}`（无 TeamId）在冻结下必须 `E_CHANGE_FROZEN`。
  成本 S。
- 状态：已亲验（守卫与 handler 两侧）。测试为何没拦住：`freeze_test.go:122` 只测了显式 `TeamId: tTeamA` 的形态。

### 4.5 暴露面与默认安全

#### R14-M6｜Edge config 拉取端点缺省无认证且通配绑定——ADR-0036 自己定为"多租户启用前置项"的那一步仍未做
- 维度：安全（暴露面 / 多租户隔离）
- 严重度：M — 端点返回**全平台**路由快照（host/path、后端地址、forwardAuth 地址）；ADR 已把"置 auth_token"
  写成多租户启用前置条件，而多租户已在 main。
- 位置：`internal/assembly/proxyconfig.go:34,50-71`；`internal/config/defaults.go:11-15`；`internal/config/config.proto:97,124`；
  `docs/adr/0036-bind-surface-configurable.md`（N2 兑现 §1）
- 证据：
  ```go
  // proxyconfig.go:57-63 —— authToken 为空即完全放行（缺省就是空）
  if authToken != "" {
      got := r.Header.Get(proxyAuthTokenHeader)
      if subtle.ConstantTimeCompare([]byte(got), []byte(authToken)) != 1 {
          http.Error(w, "proxy config endpoint requires a valid token (X-Fleetly-Proxy-Token)", http.StatusUnauthorized)
          return
      }
  }
  w.Header().Set("Content-Type", "application/json")
  body := src.ConfigSnapshot()
  ```
  ```go
  // defaults.go:11-15 —— 缺省通配绑定 + "无认证端点，公网必须由防火墙封死"
  DefaultProxyConfigAddr = ":9082"
  ```
  ```
  // ADR-0036 N2 兑现 §1 原文："缺省关闭（收窄是显式动作、升级零扰动）；**多租户启用前置项 = 置值**"
  ```
- 影响：VPC 内任意主机（含被攻陷的租户容器，若网络策略不隔离）可 `GET :9082/proxy/config` 取得全部租户的域名/
  路径拓扑与内部后端地址 → 侦察面 + 他队 Route 的接管前提（对 H2 的 join token 攻击者尤其有用）。
- 建议：多租户形态下把 `auth_token` 变必填（启动期：`teams>1 && auth_token==""` → 拒绝启动或 `Warn` + doctor `fail`），
  或把缺省绑址收窄到回环并把"通配 + 无认证"组合视为启动错误。与 R09 的默认安全面同批。成本 S。
- 状态：已亲验（代码 + 默认值 + ADR 原文）。

#### R14-M7｜`install.sh` 从 GitHub Releases 取 tarball/railpack/restic 全无校验和或签名，root 安装并 root 运行
- 维度：安全（供应链）
- 严重度：M — 现行通道是 HTTPS + GitHub，风险来自发布侧被攻陷/账号被盗与 TLS 终止点；安装产物直接进
  `/usr/local/bin` 由 systemd 以 root 拉起（`install.sh:201-215` 无 `User=`）。
- 位置：`install.sh:63-70`（fleetly tarball）、`:141-150`（railpack）、`:157-169`（restic）、`:201-215`（unit）
- 证据：
  ```sh
  # install.sh:63-70 —— 下载即解包安装，无 sha256 文件、无 cosign/minisign 校验
  URL="https://github.com/$REPO/releases/download/$VERSION/fleetly-$OS-$ARCH.tar.gz"
  curl -fsSL "$URL" -o "$TMP/fleetly.tar.gz" || die "download failed: $URL"
  tar -xzf "$TMP/fleetly.tar.gz" -C "$TMP"
  install -m 0755 "$TMP/fleetlyd" "$BIN_DIR/fleetlyd"
  install -m 0755 "$TMP/fleetly" "$BIN_DIR/fleetly"
  ```
- 影响：供应链单点（发布账号/CI token 被盗即全量装机；`railpack`/`restic` 同理且被 root 执行）。
- 建议：发布流水线产出 `SHA256SUMS` + cosign 签名；install.sh 下载后 `sha256sum -c`（缺清单即 die）；
  restic 上游有官方校验和文件可直接用。成本 S/M（依赖发布链批次，与 R09 同批）。
- 状态：已亲验（脚本逐行）。注意：这是 R09 主审范围，本报告按"依赖与平台面"横切提交，计数归 R14。

### 4.6 密钥面

#### R14-M8｜KEK 轮换窗内现役文件缺失会静默生成新 KEK 并**丢弃已装载的退役 KEK** → 密文永久不可解
- 维度：安全（密钥管理 / 数据安全）
- 严重度：M — 触发条件是 runbook 轮换第 1 步（改名现役 → 写新现役）之间发生重启/崩溃，或数据根被部分恢复；
  后果是"轮换窗口内新写的 Secret 用一把随后被覆盖的 key 封"，且退役 key 不在内存里 → `Open` 全失败。
  与 R07-L3（写非原子）是不同缺陷：那条讲落盘崩溃，这条讲**语义**（丢弃退役身份）。
- 位置：`internal/infrastructure/keyring/keyring.go:42-62`（`LoadCipher`）、`:82-97`（`loadCipherDir` 返回 nil active + retired）
- 证据：
  ```go
  // keyring.go:42-62 —— loadCipherDir 已装载 retired；HasActive()==false 时生成新 key 并
  // 只返回 NewCipher(id, nil)：retired 被静默丢弃
  c, err := loadCipherDir(dataRoot)
  if c.HasActive() { return c, nil }
  id, err := age.GenerateX25519Identity()
  ...
  return material.NewCipher(id, nil), nil      // retired 丢失，且无任何告警
  ```
- 影响：轮换窗口内的运维不停止 → 重启即"新 KEK 生效、旧行全不可解"；`fleetlyd admin rewrap` 之后才发现
  （`LoadExistingCipher` 会认这把新 key 为现役，把真正的轮换 key 当外来文件）。恢复只能靠离线备份的 KEK 文件。
- 建议：`HasActive()==false && len(retired)>0` 视为**硬错误**（"active KEK missing while retired keys exist —
  the rotation is incomplete; refusing to mint a new active key"），永不静默生成；`LoadCipher` 的生成分支
  在 `keys/` 目录非空时同样拒绝。成本 S。守卫：keyring 单测加"只有 master-*.agekey 时 LoadCipher 必须报错"。
- 状态：已亲验（两函数对照 + 调用点 `assembly/provides.go:415`）。

### 4.7 低风险与加固项

- **R14-L1｜identity 目录读面全平台可见（ADR-0035 显式不盖）**：`ListUsers/ListRoles/ListTeams/GetUser`
  无 Team 过滤，是 H1 的枚举前提，也让租户名册（用户名/团队名/角色定义）跨租户可读。
  `identity.go:107-113,204-214,289-307,391-409`。建议至少把 `users` 读面按"本队成员 + 平台 owner"过滤。成本 S。
- **R14-L2｜relay/join token 用 `==` 逐个比较**：`swarm/exec.go:88`，非常量时间；网络面可利用性低，
  但与同仓 `subtle.ConstantTimeCompare`（webhook/proxyconfig）纪律不一致。并入 H3 修复。成本 S。
- **R14-L3｜exec 会话事件载荷含完整 argv**：`fleetlygrpc/exec.go:32-43,118-128` 把 `Command` 写进
  `exec.session_opened` 事件（队内全员可读事件流）。命令里若含临时口令（`psql -c "..."`）即入流。
  建议 argv 只进审计 Detail（audit:read 面）或按参数位置脱敏。成本 S。
- **R14-L4｜平台备份仓密文件对换行敏感**：`engine/platformbackup.go:270-288` 用 `len(existing) != 64` 判定，
  运维手写/带换行即静默铸新密码并覆盖 → 既有 restic 仓永久不可开。建议 `strings.TrimSpace` + 非 hex 即硬错。成本 S。

### 4.8 指定裁决 ②：Metrics `QueryMetrics` 原样透传 PromQL 的利用面与修复归属

- **结论**：**确认 R07-H2 成立，不重复计数**（本报告并入该条，H 级）。利用面比 R07 报告更宽一档，修复归属
  不在 R07。
- 利用面（已亲验）：
  1. `QueryMetrics` 的授权只有 `metrics:read`（`proto/fleetly/telemetry/v1/telemetry.proto:85-98`），而
     `builtin-member` 对全部业务资源都有 `:read`（`domain/identity/roles.go:80-83` + `assembly/policy.go:45-52`）——
     即**最普通的租户成员**即可发起。
  2. **proto 注释自己承认没有隔离**：
     ```proto
     // telemetry.proto:85-87
     // MetricsService：指标查询面（ADR-0041）：PromQL 透传（Console F2.6 图表
     // 的数据源；CLI 诊断面）。Metrics 面停用时精确失败。行级隔离由调用方
     // 查询构造承载（App 域标签过滤）——存储是平台单租户。
     ```
     "由调用方查询构造承载"= 服务端零执法；"存储是平台单租户"= 一个 VM 装全平台样本。
  3. handler 无任何作用域构造：`fleetlygrpc/alerting.go:83-87` 原样把 `req.GetQuery()` 交给
     `svc.s.Metrics.QuerySeries`；端口签名没有 Namespace/AppIDs 锚（`domain/shared/capability/ports.go:396-402`），
     与 `Logging.Query(ctx, LogQuery, w)`（`ports.go:381-388` 明确"行级隔离由调用方查询构造执法"）形成刺眼对照。
  3. 结果可读全平台序列与租户标签（`container_label_fleetly_ns_project/_ns_app/_workload_id`、实例名、镜像名），
     等价跨租户遥测读。
  4. **额外腿（R07 未强调）**：`start/end` 由调用方任意 RFC3339 给（`alerting.go:64-82` 只校验先后），
     `step_seconds` 可为 1；provider 侧 `io.ReadAll(resp.Body)`（`providers/victoriametrics/provider.go:314-317`）
     无大小上限、无行/点上限 → 一条 `{__name__=~".+"}[90d]` 即把控制面堆内存打满（member token 的 DoS）。
- **修复归属裁决**：
  - **主修在接口/授权层（R08/R14），不在 R07**：provider 只持平台凭证做 PromQL 透传，PromQL 是不可解析的
    任意表达式，provider 侧无法在不引入 PromQL 解析器的前提下做租户过滤；把过滤责任留在 provider 等于要求它
    重写查询语义。
  - **协作点一（R01，端口签名）**：`Metrics.QuerySeries` 增 `NamespaceRef`（或 `Scopes []MetricScope`）入参，
    与 `LogQuery.Namespace` 同构；engine 采集环与告警评估的既有调用点补零值。这是根因层修复。
  - **协作点二（R08，handler）**：`QueryMetrics` 先按 `project_id/app_id` 做行级授权（`authorizeProjectID`），
    再拼 `container_label_fleetly_ns_*` matcher 强制收敛；平台级诊断查询另开 `platform:read` 面。
  - **短期止血（S）**：`QueryMetrics` 收归 `platform:admin`（`requirePlatformAdmin`），并对 `end-start`、`step`、
    结果规模加上限；可与 H2 同批的"平台资源门"一次落地。
  - **归属结论**：R07-H2 的"修复建议 (a) 端口加作用域 + (b) handler 行级授权"我方**同意**；但 R07 报告把
    它记在"服务类 providers"模块下不合适——它不是 R07 实现缺陷，而是**授权模型缺一个作用域锚**，
    验收应挂在 R01（端口）+ R08（handler）+ R14（授权语义）三处，R07 只补一条 provider 侧上限（体积）。
- 状态：已亲验（handler/端口/角色种子/注解/上游读体五处）。

---

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **给 R08（接口层）**：本报告的 H1/H2/M4 都落在 `fleetlygrpc` 的 handler 面——建议 R08 的"逐 handler 归属校验"
   清单把**非资源行面**（identity 目录/改密、nodes/system/platform/templates 的平台资源）单列一节，
   因为 ADR-0035 的行级判定天然覆盖不到它们；`requirePlatformAdmin` 已是现成机制，缺的是覆盖面。
2. **给 R04**：`R04-H1`（Browse ForwardAuth 只校验 cookie 不绑会话）**确认成立**，与 H1 无重叠；
   另注意 browse 的 launcher ticket 面（`databases.go:576-583`）是正确形态，可作 R04 修复参照。
3. **给 R06**：`RelayAttach` 的替换语义在 engine（R14-H3），但 `ExecClusterToken` 的"worker OR manager 皆可"
   与 `==` 比较在 swarm provider（`swarm/exec.go:80-92`）；k3s 侧同理走 node token（`k3s/exec.go:61`），
   修复需要双 Provider 同步（节点个体凭证）。R06 的 ADR-0052/0053 验收锚未覆盖"伪造 carrier id"。
4. **给 R07**：`R07-L3`（KEK 写非原子）与本报告 M8（丢弃退役 KEK）是同一文件的两个不同缺陷，
   修 M8 时不要顺手把 `LoadExistingCipher` 的"绝不生成"纪律破坏。
5. **给 R09**：`install.sh` 无校验和（M7）+ 缺省通配绑定/无认证 9082（M6）与"默认配置安全"同批；
   另 `systemd` unit 无任何加固项（`NoNewPrivileges`/`ProtectSystem`/`PrivateTmp`），root 运行是有意的（docker.sock）。
6. **给 R12（守卫）**：本报告的三条"测试为何没拦住"都指向同一类缺口——**守卫只对账"方法有没有策略"，
   不对账"策略粒度是否等于声明意图"**。建议新增两条机制守卫：
   `TestPlatformScopedMethodsRequirePlatformAdmin`（资源=platform ⇒ handler 必须调用 `requirePlatformAdmin`）、
   `TestIdentityAdminMutationsRequireRowAuthz`（identity 写 handler 必须引用 `teamAuthorized`/`requirePlatformAdmin`）。
   这两条能覆盖未来新增 RPC，比逐点修更值。
7. **给 R05**：H4 的修复需要迁移（`idempotency_records` 加 `team_id` + 唯一索引改序），按 00018 的"先建后删"序做；
   另外 `idempotency_records` 无 `team_id` 也意味着 R05 的"team 轴贯通"清单里漏了这张表。
8. **给 R13**：`withRequestIdentity` 对匿名 PUBLIC 面不写 team（`interceptor.go:158-164`）→ 审计落 `''` 平台级、
   非 owner 也可见（`audit/repo.go:76`）。目前 PUBLIC 面只有 webhook/邀请/Login/WhoAmI，其中 webhook 自行
   注入 App 归属 Team，风险可接受；但"''=全员可见"的口径值得在 R13 的数据流图里显式标注。

---

## 6. 测试与守卫质量评估（本模块）

**强的地方**
- `apitest/teamauth_test.go`（265 行）是真正的跨队矩阵：读/写/动词/上传首帧/List 过滤/审计轴/冻结面全覆盖，
  且用**真 SQLite + 真 repo**（`statetest`）而非 mock；`events_team_test.go` 把 SSE 票据的跨队不泄漏钉死。
- `authn` 的收窄语义有域纯函数级测试（`identity/scope_meet_test.go` 的输入序无关/幂等）。
- `idem/enforcer_test.go` 覆盖同键同体/异体/跨方法/在途四条主路径与失败释放。

**真实缺口（按危害排序）**
1. **"第二个 principal" 维度整体缺失**：幂等（H4）、identity 管理面（H1）、platform 资源（H2）三类缺陷的共同点，
   都是"同一请求换一个调用方"或"换一个角色档"才暴露，而现有测试全部在单一主体（bootstrap owner 或
   单一 member）内闭合。建议把 `apitest` 的跨队矩阵扩成"**角色 × 队 × 资源类别**"三维表，
   资源类别显式包含"无行级轴的平台面"。
2. **角色种子被词表无意识扩张**：`TestBuiltinRoles` 用自造小词表（`identity_test.go:13-15`），
   `platform` 从未出现在断言里；而生产词表（`assembly/policy.go:45-52`）含它。守卫应在**生产词表**上做
   "admin 不得含 platform" 的断言（当前无）。
3. **冻结反扫只对账"方法分桶"，不对账"寻址字段是否可能为空"**：`freezeguard_test.go` 把 `CreateProject`
   归到封禁桶即算通过，M5 的空字段绕过因此不可见。建议给每个封禁动词加一条"最小请求也必须被寻址到"的用例。
4. **缺省值语义只在一个动词上验过**：ADR-0035 决策 5 的"缺省=调用方队"在 `CreateToken`/`CreateProject` 有测试，
   `CreateUser/CreateRole/CreateInvitation` 无测试 → M4 长期潜伏。
5. **`authn` 的 PUBLIC 面按匿名放行**有测试，但缺"PUBLIC 面携带**已吊销**凭证"的形态（当前实现会按匿名放行，
   语义正确但无钉）。

---

## 7. 未决与置信度

**最不确定的 3-5 点**
1. **H2 的"应由 owner 独占"是否真是项目意图**：`policy.go:43-44` 的注释与 C3 口径支持这一读法，但
   `roles.go` 的"admin = 全资源 :admin"从未把 `platform` 排除。若项目其实认为"队 admin 可管集群"，
   则 H2 降级为文档-代码漂移（M）。我按"注释/C3 是意图声明，代码未能表达"判 H——请 Lead 与用户口径裁决。
2. **H4 的现实可利用性**：依赖"受害方使用可预测幂等键"。键随机则不可利用，此时它是共享命名空间的健壮性缺陷
   （跨队 409 干扰）。我判 H 的理由是响应体含明文凭证 + 仓库自己在 00018 承认"客户端模板复用导致撞键是现实形态"。
3. **H1 的终点是平台 owner 还是 default-team admin**：取决于部署里是否存在 `builtin-owner` **用户**。
   `fleetly init` 只建 `builtin-admin`（M2），所以默认安装下链路止于"跨队到 default Team 的 admin"；
   一旦运维按 ADR 建了 owner 用户，终点即平台接管。两个终点都够 H。
4. **H3 的动态可复现性**：`RelayAttach` 的替换语义在单测覆盖之外（`execrelay` 无测试文件），我没有真机 dind
   可跑；代码路径无歧义，但"是否所有 exec 帧都会落到替换后的连接"只做了阅读推演（`deliver`/`send` 都经
   `s.relay` 与 `relays[nodeID]`）。
5. **M6 的多租户前提**：若产品的多租户启用宣告仍未正式发布（pre-release），M6 可视为"未到期挂账"；
   但 Team/Invitation API 已在 main 且可用，我按"条件已满足"计。

**整体置信度**：**高**（H1/H2/M4/M5/H4 均为 5-15 行内的直接代码证据 + 调用方核对；H3 为同等级静态证据，
仅缺动态复现）。已覆盖 authn/idem/governance/policy 四个主审文件组的 100% 生产行、identity 与 nodes/system/
platform/templates 的授权注解全量、密钥与安装面逐行。
**未覆盖面**：Console 前端 token 面（R11）、browse ForwardAuth（R04）、SQL 注入面（R05）、
构建/备份 argv 全量（R03/R04，抽样处未见注入面：`git` 用 `--` 与 `protocol.*.allow=never`、restic 凭证走 env）、
CVE/依赖漏洞（未联网）。
