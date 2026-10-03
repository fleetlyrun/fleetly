# ADR-0035: 数据面 per-Team 行级授权

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-03 | N1 独立审查遗留首项（docs/reviews/2026-10-03-n1-independent-review.md）、ADR-0028（Team 轴挂账）、ADR-0013（网络 peer 归属）、ADR-0017 附录 A.3（冻结轴）、ADR-0026（API 惯例） |

## 背景

N1 审查确认：全部数据面 RPC 按**全局 ID 直取**资源，scope 门禁只回答"能不能用这个面"、不回答"能不能碰这行"——任何持有对应 scope 的 Token 可跨 Team 读写他人 Project/App/Secret/Config/Task/Run/Revision 等（GetConfig 回读 env 值、DeleteSecret 直删、ListRuns 无过滤全平台列 Run、DiffRevisions 跨租户读 spec）。身份链上 `Identity.TeamID`（Token 行）与 `projects.team_id`（资源行）都已存在（ADR-0028），唯独没有行级比对。唯一先例是 ApproveNetworkPeer 的 `requirePeerReceiverTeam`（安全批 P1）。

## 决策

1. **判定语义**：凡归属链落在 Project 上的资源（App/Revision/Deployment/Build/Route/Secret/Config/Volume/Network/NetworkPeer/Task/Run/Schedule/Upload/Database），资源归属 Team（经 Project 行解析）必须**等于**调用方 `Identity.TeamID`；不等 → `E_FORBIDDEN`。等式而非成员集合——Token 恰属一个 Team（tokens.team_id NOT NULL）。
2. **平台例外通道**：**default Team 的 `builtin-owner`**（即 bootstrap 引导 Token 所在档）跨 Team 全通——平台管理员。其他 Team 的 owner 角色是该队管理员，**不跨队**（networkpeer 安全批测试钉死的边界："他队 owner 不得自助批准"）。理由：引导 Token 是平台 onboarding 的起点（新 Team 的首枚 Token 由它铸），行级豁免只给引导面；Team 边界保护的对象是其余全部最小权限 Token（member/admin/自定义/CI，含他队 owner）。`*` scope 不是行级豁免——自定义角色若被授 `*`，能力面全通但行级仍限本队（能力与行级正交）。
   连带：**tombstone 行保持可授权**——GetProject 按设计可读已删行（"tombstone 是事实不是秘密"，ADR-0023），归属判定对已删行同样成立（ListDeployments/ListRevisions 等审计型读面在 App 删除后仍须过授权，app repo 增 `GetAnyByID` 授权专用行读）；活跃性拒绝仍由各写面事务内的 `requireActiveProject`/活跃行 Get 承载，读语义不在授权处分叉。
3. **执法点 = 受理位**（acceptance-check/服务层），不是拦截器：资源→Team 解析链 per-RPC 不同（project_id 直取 / app_id 二跳 / 全局 ID 三跳），FreezeGuard 的 scopeKind 解析表证明拦截器方案需要一张 40+ 方法的映射注册表且随 RPC 演进漂移；受理位是既有模式（requirePeerReceiverTeam/parentProjectAlive 家族）。新 helper 族落 `internal/api/fleetlygrpc/authz.go`（唯一判定真源，守卫对账对象）。
4. **读写分面**：
   - **Get/Delete/动词面（按全局 ID）**：先载行解析归属 Team，比对后放行——行不存在保持既有 `E_NOT_FOUND`（404 掩蔽不存在，403 只落"存在但非本队"）。
   - **List 面**：按调用方 Team 过滤而非逐行拒绝——ListProjects 按 Team 列（repo `ListByTeam`）；既有 project 锚 List（apps/tasks/secrets/...）先授权 project 再查询。
   - **ListRuns 收紧**：`ListRunsRequest` only-add `project_id`；**task_id 与 project_id 必须给其一**（无过滤的全平台 Run 列表是泄漏面本身），两者都给以 task 为准属 `E_INVALID_ARGUMENT`。
   - **写面受理**（CreateX/PutX）：创建在他人 Project 下 = 越权落子，受理前置拒绝（Project 的 Team 归属不可变——CreateProject 后无改 Team 的 API，受理位前置无 TOCTOU）。
5. **Identity 面**：CreateToken/CreateUser/CreateRole/CreateInvitation 的目标 Team 必须等于调用方 Team（owner 豁免）；**缺省从 `identity.DefaultTeamID` 改为调用方 Team**（对 default Team 调用方等价，对其他 Team 严格更合理）。GetToken/RevokeToken 按 Token 行 Team 比对；ListTokens 按 Team 过滤。Users/Teams/Roles 的目录读面（Get/List/Delete）维持 scope 门禁不变——identity 目录是平台管理面（v1 定位：审计与 Console RBAC 随 N2 重估），本 ADR 显式不盖。
6. **Audit team 轴**：迁移 00017 给 `audit` 加 `team_id TEXT NOT NULL DEFAULT ''`；写入时 `audit.Repo.Append` 从 ctx 统一铸入（authn 拦截器为已认证请求注入调用方 Team；webhook 面经 `audit.WithTeam` 显式注入 App 归属 Team；system/无身份动作落 `''`）。ListAudit 非 owner 过滤 `(team_id = 调用方 OR team_id = '')`——`''` 行是 system 动作与迁移前存量（平台级可见：内容为动作名+资源 ID+指纹，无值载荷）。**不回填存量**：旧行无归属事实，回填即编造。
7. **Freeze 面**：SetChangeFreeze 的 team_id 必须等于调用方 Team（owner 豁免）；空 team_id（全局冻结）仅 owner 可设（跨 Team 影响的平台动作）。Lift/List 对非 owner 限本队行 + 全局行可见（全局冻结影响调用方，需可读 reason）。FreezeGuard 执法（资源 Team 轴）不变。
8. **PUBLIC 面不适用**：ReceiveWebhook（HMAC 即凭证，App 归属即边界）、AcceptInvitation（邀请 Token 即凭证）、WhoAmI（自身投影）不经本判定。

## 后果

- 单 Team（default）部署零行为变化：全部现有 Token 与资源同队；golden/apitest 夹具用 bootstrap owner（豁免通道）亦零漂移。
- 多 Team 语义闭合：member Token 只见/只动本队资源；平台 owner 保持全通运维能力。
- 既有依赖全局 List 的消费者（quickstart/golden/skills）全部已带 project/task 过滤或走 owner——本批核对面记录在审查实录。
- ListRuns proto 是 only-add（buf breaking 过）；旧客户端无过滤调用将收到 `E_INVALID_ARGUMENT`（CLI 同批加 `--project` 并前置要求 `--task`/`--project` 给其一）。
- StreamLogs 的 NamespaceRef.Team 硬编码 `"default"` 随本批复实为 App 归属 Team（同调用点授权顺带修——引擎 `resolveBackend` 同族问题已在 ADR-0028 修过，api 面漏网）。

## 验收锚

- [ ] 跨 Team 夹具：team B member Token 读/写 team A 资源（GetApp/GetTask/GetRun/GetConfig/DiffRevisions/GetDeployment 等）→ `E_FORBIDDEN`；同队访问不受影响
- [ ] ListProjects/ListTokens 非 owner 只见本队；owner 全见
- [ ] ListRuns 无 task_id 且无 project_id → `E_INVALID_ARGUMENT`；`project_id` 过滤可用且跨队拒绝
- [ ] CreateToken/CreateUser/CreateRole/CreateInvitation/CreateProject 目标 Team ≠ 调用方 Team → `E_FORBIDDEN`（owner 豁免）；缺省 = 调用方 Team
- [ ] PutSecret/DeleteSecret/GetConfig/Deploy/Rollback/UploadSource/Hooks 面：跨队目标 Project/App → `E_FORBIDDEN`（Upload 在首帧后、落盘前拒绝）
- [ ] 审计行带 team_id（迁移 00017）；ListAudit 非 owner 过滤本队 + `''`
- [ ] SetChangeFreeze 跨队/非 owner 全局 → `E_FORBIDDEN`
- [ ] 单 Team 全量回归零漂移（apitest + golden + engine 门禁绿）
