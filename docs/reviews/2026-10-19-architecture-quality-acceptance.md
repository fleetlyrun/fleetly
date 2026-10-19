# fleetly 架构设计与代码质量验收报告（2026-10-19）

- **评审基线**：分支 `main`，HEAD `35257f18`，工作树干净（863 commits，2026-09-30 起，无 tag，pre-release）
- **评审方式**：Lead 建立项目画像并按职责切分模块（非目录机械对应）→ 16 路并行评审（12 模块 + 3 横切 + 1 核心声明式模型专项）+ 1 路独立 H 复现验证 → 统一证据标准（`文件:行` + 代码摘录；未亲验标"待复核"）→ Lead 汇总去重、逐条复核全部 H、校准严重度
- **零改动**：评审全程未修改仓库任何文件（`git status` 始终为空）；原始报告归档于 `docs/reviews/2026-10-19-architecture-acceptance/`
- **本机环境**：Windows + Go 1.26.6；Docker daemon 不可达 → e2e 未本机复跑；CI 为 Linux 口径。本机基线：`go test ./...` 79 包 ok，3 处环境/平台性失败（§10）；Console vitest 129/129 通过

> 快速阅读：判断"能否上线/继续演进"看 §0 + §3 + §8；复核结论看 §3 每条证据锚与 §9；原始逐条报告见附录 A。

---

## 0. 总判定

**架构骨架在同类项目中属优秀，代码质量高于平均；但当前不具备多租户生产可用的安全前提，且安装主路径/等待原语/备份与收敛等关键链存在会破坏核心承诺的缺陷。**

| 维度 | 判定 | 依据 |
|---|---|---|
| 架构设计 | **优（8.5/10）** | Clean 四环 + 依赖矩阵守卫真实执法（12 路独立阅读零方向违反）；Capability 端口窄面由 k3s 第二 Runtime 验证成立；写路径 / 事实发射 / 行级授权 / 错误面均单点化；核心声明式模型 29 项对抗裁决 **22 已落码 / 7 部分 / 0 未落码**，核心机制（缺席重投影/双通道/签名生命周期/冻结闸门/restart_seq 八步）按设计落码且钉测扎实；行为面承诺的缺口见 §6.4 |
| 代码质量 | **良（7/10）** | 命名/注释/测试/门禁密度高，事故驱动型测试文化真实；但存在静默失败族、非事务双真完整性缺口、资源无界增长、双 Provider 语义分叉，以及数条"看起来很严、实际拦不住"的空心守卫 |
| 生产就绪度 | **低（安全与交付主链不满足）** | 权限模型缺平台维：**任意 Team 管理员可在运行时复现地完成平台接管**（§3 H1/H2）；k3s RBAC 落后调用面；Browse ForwardAuth 跨会话越权；备份环可永久挂死、灾备恢复点可静默损坏；README 一行安装在原生 env 面下**无路由发布能力**；`deploy --wait` 对 >2 分钟部署必然假失败；声明式 restart 在失败/回滚后语义反转（H15） |

**一句话结论**：fleetly 把"PaaS 控制面复杂度"收束得很好——分层、端口、写路径、契约纪律都立住了；当前最大风险不是设计不对，而是**权限模型缺第二维（平台 vs Team）**、**若干关键路径的失败处理/并发互斥**，以及**"文档/验收锚声称的强度"高于"守卫实际执法强度"**。前三类修完（约 2-4 周）可进入生产试用；不修则只能停留在单 Team / dogfooding 形态。

---

## 1. 评审方法与覆盖

### 1.1 流程

1. Lead 建立项目画像（`PROFILE.md`：技术栈、分层、核心数据流、外部依赖、部署形态、本机基线），按职责/变化原因切 12 个模块 + 3 个横切面（`MODULES.md`）。
2. 16 路并行评审：R01-R12 模块（domain/engine 三片/persistence/双 Runtime Provider/服务类 Provider/接口层/组合根与 CI/CLI 与 SDK/Console/守卫与 e2e），R13 集成与事务，R14 安全与多租户，R15 验收声明与历史挂账，R16 核心声明式模型专项（ADR-0081/0082/0083 设计承诺 ↔ 落码 ↔ 29 项对抗裁决 ↔ 十行裁决表逐行核对，独立于 R02/R15）。统一要求：只读、每条发现带 `文件:行` + ≤12 行摘录、模块报告含覆盖清单/优点/未覆盖面/置信度。
3. Lead 逐条复核 H，抽查 M；对两条最严重的安全链（R08-H1/H2）另派**独立验证 agent** 用 `go test -overlay` 在进程内做运行时复现（不写仓），确认攻击链成立并给证据边界。
4. 汇总去重（模块/横切报出的同一根因合并）、严重度校准（含 1 条 H→M 降级与 1 条 M 的事实修正，见 §9）、给出机制层建议与行动顺序。

### 1.2 规模与产物

- 16 份模块/横切/专项报告（共约 **760KB**，原始发现 **216 条**：H 18 / M 112 / L 86，去重合并后 **H 15 / M ≈105 / L ≈80**）+ 1 份独立复现报告。
- 覆盖：internal 生产代码约 5.9 万行（12 模块边界内生产文件 100% 阅读）、proto 16/16、Console 3 万行（入口/数据层/安全面）、CLI 7.9k、e2e 13 脚本、guards 41 测试、persistence 37 聚合 + 33 迁移、CI/mise/install.sh、docs 真源 12 份 + ADR 抽样 83 篇、近 300 commit 的生成物纪律统计。
- 动态验证：模块评审在本机跑了目标包测试（domain/engine/guards/CLI(SDK)/Console/Persistence 等，均绿）；H1/H2 有进程内复现；e2e 因本机 Docker 不可达**未复跑**（不声称真机复验）。

### 1.3 证据标准与局限

- H 全部经 Lead 亲读代码链；M 抽查关键调用方；L 采信报告并在原文标注。
- 未覆盖：真机 e2e/升级/备份演练、staging 声明、GB 级备份恢复、Windows 原生 CLI、G1-G24 原文（仓内不可达，见 R15-M2）、上游二进制（traefik/restic/VL）行为细则。凡涉及真机的结论均标注"机制已验、触发待真机"。

---

## 2. 项目画像

- **产品形态**：轻量 PaaS。宿主 Linux 上 `fleetlyd`（控制面 daemon，gRPC :9080 / REST :9081）+ `fleetly`（瘦 CLI，只走 API）；单二进制、内嵌 SQLite（WAL + goose，**33 迁移**）、零外部 DB；Console = React SPA embed；五件受管自宿（zot / traefik / VictoriaLogs / VictoriaMetrics / cadvisor）；Runtime = swarm（默认）与 k3s（第二试点）；人类与 Agent 同为一级用户（全命令 `--json` + 477 golden + `skills/`）。
- **规模**：1841 tracked files；Go 774（含 genproto 生成物），生产 Go 约 5.9 万行 + 测试约 5.2 万行；proto 4.9k（16 文件 / 36 service / **153 RPC**）；Console 3 万行 TS/TSX；e2e 13 脚本；guards 6.7k（41 测试）；ADR 83 篇；docs 9.2k 行 + 239KB 真机 runbook。
- **分层（ADR-0075 Clean 四环）**：`domain（零外向，三红线守卫）→ application（engine 九域 + authn/idem/governance/composeintake/apptemplate/schema/transact）→ infrastructure（persistence 37 聚合 repo + 9 Provider + github/upload/keyring）→ interfaces（gRPC/REST/admin/console）`；assembly/config 组合根，guards/testsupport 元层。依赖方向由 `internal/guards/dep_matrix_test.go` 五行矩阵 + 双向保鲜执法。
- **核心机制**：proto 单源（gRPC+REST+OpenAPI+TS）；写路径 = 拦截器链 → Acceptance/`commit` → 四件一拍（状态 CAS + tombstone + Outbox + 审计，同事务，kernel 单点）→ 单写者收敛环（唯一 Loop 骨架）；Runtime 唯一写动词 `Ensure`（核心 6 方法窄面）；部署状态机 + admission + 蓝绿双代窗 + preDeploy jobs + 回滚=Replay；事件 Outbox 单调 seq（gRPC stream + SSE + 410 重同步）；审计只增不裁。
- **最新架构增量（本次验收重点）**：严格声明式模型（2026-10-18 设计 + ADR-0081 写类/作者类、ADR-0082 缺省收敛、ADR-0083 restart_seq），已进 main 并在 staging 换装（最后 6 个 commit）。

---

## 3. 高严重度发现（H，15 条；全部经 Lead 复核）

> 每条：位置 / 机制与影响 / 修复 / 复核状态。H1、H2 已做**运行时进程内复现**（证据在 `docs/reviews/2026-10-19-architecture-acceptance/R08-H1-H2-repro.md`）。

### H1｜平台/节点/治理面缺平台管理员门，内建 admin 角色跨 Team 下放 `platform:admin` / `nodes:*` / `users:*`
- **来源**：R08-H1 + R14-H2（合并；运行时已复现）
- **位置**：`internal/domain/identity/roles.go:70-91`（admin = 每个资源 `:admin`，仅 member 排除 platform）；`internal/assembly/policy.go:43-52`（platform 是"集群面资源 C3"）；`proto/fleetly/runtime/v1/runtime.proto:47-51`（EnrollNode=`platform:admin`）、`:56-74`（Drain/Cordon=`nodes:write`）；handler 无平台门：`fleetlygrpc/contexts.go:82-113,153-179`、`systemgrpc/systemgrpc.go:120-141`；现成范式 `requirePlatformAdmin` 只在 GitHubApps 使用（`authz.go:58-71`）
- **机制**：内建 admin 可在任意 Team 授予；其 scope 集含 `platform:admin`/`nodes:admin`；平台面 handler 只做 scope 检查。**运行时复现**：非 default Team（h1-acme-2）的 builtin-admin 用户经 PUBLIC Login 得到 Token，WhoAmI 32 scope 含 platform:admin+nodes:admin；`EnrollNode(rotate=false/true)` 均成功并回显 join material；`DrainNode` 放行；`RestartComponent` 越过授权层进入 handler。对照：同一 Token 调 `SetGitHubApp(global)` 被正确 403 → **门存在，只是没铺开**。
- **同根附加缺陷（Lead 复核新增）**：角色授予无特权上限——`acceptance.go:263-278` 的 `roleInTeam` 对内置角色一律放行，`CreateToken` 允许任意 `role_id`。因此 default Team 的 admin 可一步铸造 `builtin-owner` Token → 平台 owner；非 default 队 admin 经 H2 接管 default 队后再做同样一步。
- **影响**：任意 Team 管理员可铸/轮换 join 材料（rotate 作废全部节点材料、中断全平台 exec）、排空任意节点、重启受管组件、替换全局模板目录；叠加下面的角色授予缺陷可直达平台 owner。
- **修复**：平台面统一接 `requirePlatformAdmin`（或 scope 词表拆平台轴）；角色授予加"不得授予高于自身的角色"上限；补 role×scope 禁止矩阵 + "platform 资源方法必须调用 requirePlatformAdmin" AST 守卫。成本 S-M（机制层）。
- **复核**：**确认**（静态全链 + 运行时复现）。

### H2｜identity 写面缺平台/归属门：任意 Team admin 可重置任意用户密码并 Login 接管（可链到平台 owner）
- **来源**：R08-H2 + R14-H1（合并；运行时已复现）
- **位置**：`fleetlygrpc/identity.go:174-202`（SetUserPassword 唯一检查 = userExists）、`:204-214`（ListUsers 全平台无过滤）、`:120-170`（Login PUBLIC，取 `memberships[0]` 铸 Token，无 TTL）；`proto/identity/v1/identity.proto:45-57,62-69`（Login PUBLIC、SetUserPassword=`users:write`）；`cmd/fleetly/cmd/init.go:77-85`（初始化建 builtin-admin 用户）
- **机制（运行时复现）**：Team B admin → `ListUsers` 拿目标 user_id → `SetUserPassword` 覆写（无需旧密码、无归属校验）→ PUBLIC `Login` 返回被覆写用户的 Team+Role Token。复现中攻击者拿到 `team=default role=builtin-owner scopes=[*]` 的 Token，跨队读 Team B Project、再铸 platform-owner 用户均成功；对 Team C admin 的接管变体同样成立。对照：同一 Token 跨队 `CreateUser` 被 403（Team 轴存在，仅密码/登录面漏）。
- **影响**：跨租户账号接管；存在 default-team owner 时等价全平台接管（不依赖该前置也可通过 H1 的角色授予缺陷升级）。
- **修复**：SetUserPassword/DeleteUser/GetUser/ListUsers 纳入行级/平台授权（本人 or 同队 admin or 平台 owner）；Login Token 加 TTL；补 identity 跨队负例（全仓 SetUserPassword/Login 零测试，`identity_test.go:176-184` 反而把 admin 越权写成正向断言）。成本 S（机制层，随 H1 权限模型 ADR）。
- **复核**：**确认**（静态全链 + 运行时复现；证据边界：真实 join token 签发未验，RestartComponent 只证到越过授权层）。

### H3｜k3s RBAC 最小权限表落后于实际调用面：configmaps 整组缺失、networkpolicies 缺 update、pods 缺 deletecollection
- **来源**：R06-H1（真机 403 为 K8s RBAC verb 语义推演）
- **位置**：`providers/k3s/rbac.go:41-65`（规则表）vs 调用面 `k3s/network.go:132-141`（ConfigMaps Create/Get/Update）、`k3s/netisolate.go:179`（NetworkPolicies Update）、`k3s/restart.go:17`（Pods DeleteCollection）
- **影响**（真机 k3s）：①任何带 Config 材料的部署（compose configs / pre-deploy job bootstrap SQL）Ensure 403；②平台升级改 netpol 形状时 update 403，收敛被阻断；③`components restart` / `apps restart --runtime` 恒 403（k3s restart 零命中还会静默成功）。fake clientset 不执法 RBAC、`rbac_test` 只把表钉在自身、e2e can-i 仅抽样 6 条 → **CI 结构性全绿**。属 ADR-0055 已记录历史坑同型复发。
- **修复**：补三条规则 + e2e can-i 表驱动扩表（S）；机制层 AST 守卫"k3s typed client 调用面 (resource,verb) ⊆ desiredClusterRole"（M）。
- **复核**：**确认**（表与三处调用点亲读）。

### H4｜Browse ForwardAuth 只校验 cookie、不绑定目标会话 → 跨会话/跨租户越权
- **来源**：R04-H1（R14 交叉确认；运行时未复现）
- **位置**：`assembly/gateway_browse.go:113-125`（只按 cookie 内 sid+grant 校验）；`engine/browse.go:287-295`（所有会话 tool route 共用同一 `Auth.Address`）；session id 可推导（`entityid.Derive`）且审计 detail 明文携带（`databases.go:552`）
- **影响**：持任意有效 browse 会话 cookie 即可通过他人 browse host 的 ForwardAuth，进入对方工具容器（pgweb/adminer 等本身无认证）；只读会话可进读写会话（同队提权），跨队需 host 推导但枚举面存在。
- **修复**：authorize 校验 `X-Forwarded-Host == browse-<sid>.<suffix>`（或 Auth.Address 带 session 参数并比对）；`capability.RouteAuth` 契约补"会话绑定"语义。成本 S（机制层）。
- **复核**：**确认**（authorize/route 生成/traefik 渲染链亲读）。

### H5｜备份 ObjectStore 写失败时 `io.Pipe` 读端未关闭 → backup 环永久挂死 + maintenanceMu 读锁连坐
- **来源**：R04-H2
- **位置**：`engine/backup.go:237-264`（`pr/pw` + `<-runErr` 无界等待）、`localobjectstore/provider.go:87-123` 与 `s3objectstore/provider.go:183-195`（Put 各错误早退且不读不关 reader）、`swarm/utility.go:186-203`（pump 在 `<-pumpErr` 等待）
- **影响**：S3 断网/磁盘满/ctx 取消 → 工具容器写端阻塞 → pump goroutine 永不返回 → 备份环单写者永久停摆（备份/恢复/保留/Platform Backup 全停），maintenanceMu 读锁永持 → RebuildNetwork 写锁永久阻塞；无失败行/事件。fake ObjectStore 恒排空 reader，测试结构上抓不到。
- **修复**：`defer pr.Close()` + ctx 感知等待 + "未读即错"的 fake 测试；ObjectStore 端口写明 reader 生命周期。成本 S。
- **复核**：**确认**（backup/utility/provider 三段亲读）。

### H6｜Platform Backup 无单飞：并发触发可产出"损坏但 restic check 通过"的恢复点
- **来源**：R04-H3
- **位置**：`engine/platformbackup.go:119-181`（无互斥）、`:216-223`（`os.Remove`+`VACUUM INTO` 均在 restic 仓锁外）、`:365-372`（手动触发与 backup 环节拍并发）
- **影响**：升级前置/唯一控制面灾备恢复点可静默损坏（restic check 只校验仓结构）；并发还可能让手动触发误报 `E_PLATFORM_BACKUP_FAILED`（升级硬停假失败）。注释"仓锁兜底"不成立。
- **修复**：引擎级单飞（TryLock 显式"已在执行"）+ 唯一临时名原子 rename + backup 前 `PRAGMA integrity_check`。成本 S/M（机制层：导出-归档共同基座）。
- **复核**：**确认**（函数结构与两调用点亲读）。

### H7｜Route 全量发布缺就绪门：控制面重启后 traefik 可被空/部分快照覆盖，用户路由最长 ~60s 中断
- **来源**：R07-H1（机制已验；触发窗口待真机）
- **位置**：`providers/traefik/provider.go:57-72,113-119`（构造即 `emptyDynamicConfig`，首发布前 `ConfigSnapshot` 恒返回空全量）；`assembly/proxyconfig.go:64-69`（直接写出）；`engine/routepublish.go:44-49,61-68,84,122-145`（签名短路 + 冷缓存逐 route skip 但发布成功并 remember）；`engine/lifecycle.go:33-52`（环与基线重放并发启动）
- **影响**：控制面重启后 traefik 5s poll 可拉到空配置清空 router；或发布"部分路由"后签名固化直到 60s 慢通道重放；与 ADR-0015"升级路由零中断"承诺冲突。e2e 仅 2-3 App，重放通常先完成，CI 常绿掩盖规模触发。
- **修复**：首次成功发布前返回 503（traefik 保 LKG）+ 基线重放收口触发 `PublishRoutesNow` + skip 不 remember。成本 S+M。
- **复核**：**机制确认**；真实规模触发需 dind 多 App 慢重放实测。

### H8｜Metrics 查询面无服务端租户域过滤：任意 member 可读全平台容器指标与租户标签
- **来源**：R07-H2 + R08-M2（合并；R14 裁为 H）
- **位置**：`fleetlygrpc/alerting.go:54-99`（PromQL 原样透传）；`capability/ports.go:390-403`（端口签名无作用域锚，对比 Logging.Query 带 Namespace）；`identity/roles.go:70-87`（member 默认含 metrics:read）；`telemetry.proto:85-98`（注释自称"行级隔离由调用方"）
- **影响**：任意 Team 成员读全平台序列与 `container_label_fleetly_ns_*` 租户标签（资源规模/镜像版本/上下线时刻侧信道）；无结果上限（R14 另指出 start/end 任意 + step=1 + `io.ReadAll` 无上限的 DoS 腿）。
- **修复**：端口加 NamespaceRef/AppIDs 并强制拼 matcher + handler 行级授权（M，机制层）；短期收归 platform admin（S）。
- **复核**：**确认**（handler/端口/角色/proto 亲读）。

### H9｜蓝绿双代窗旧代投影漏填 egress 事实 —— k3s 在服旧代窗口期失去 `egress:none` 强隔离
- **来源**：R02-H1
- **位置**：`engine/bluegreen.go:127`（fromWS 传 nil egress）对照 `:147`（新代带 `egressNetworkMap`）；`k3s/translate.go:187-188,708-710`（labelEgress 只由 EgressNetworks 产生，deny policy 以其为 podSelector）
- **影响**：k3s + blue-green + `egress:none` 的 App，每次 BG 部署窗口期**正在承载流量的旧代**失去 deny policy 选中（可自由出站）；窗口结束后恢复。ADR-0052 F4.1 明文验收锚；引擎测试对 BG+egress 零断言。
- **修复**：一行补 egress 参数 + 两代 union 的 egress 投影钉测 + k3s e2e 断言。成本 S。
- **复核**：**机制确认**（真机未复现）。

### H10｜`/v1/relay` 只校验共享 join token、`carrier_node_id` 客户端自报 → 可顶替任意节点中继，劫持/中断他队 exec 会话
- **来源**：R14-H3（运行时未复现；静态全链）
- **位置**：`assembly/gateway_exec.go:78-113`（凭证通过后 hello 首帧自报 `CarrierNodeID`）；`engine/execrelay.go:20-60`（`Nodes.ByCarrier` 后直接替换 `relays[nodeID]`，在途已附着会话被 finish）；`swarm/exec.go:80-92`（集群共享 join token，`==` 比较）；`runtime.proto:80-96`（carrier_id 对 nodes:read 开放）
- **影响**：持有集群 join token 者可伪装任意节点已锚定的 carrier id，顶替其 relay：收到发往该节点的 exec 帧（含 stdin 明文/交互输入），并终止在途会话。与 H1/H2 串联即成完整跨租户链（取 join token → 顶替节点 → 监听任意 exec）。
- **修复**：per-node 个体凭证（节点身份与 join token 分离）+ `ExecClusterToken` 反查锚定 `hello` 节点；R06/R14 同批；成本 M。
- **复核**：**确认机制**（gateway/execrelay/Provider 三处亲读）；真实劫持未动态复现。

### H11｜`idempotency_records` 键空间不按调用方隔离 → 同键同体重放可取他队创建响应（含明文 secret）
- **来源**：R14-H4（**条件性 H**：前置为按键可预测/跨队复用）
- **位置**：`application/idem/enforcer.go:137-159,176-186`（重放只比对 method+fingerprint，身份不参与）；`persistence/idempotency/repo.go:24-28`；`migrations/00007_idempotency.sql`（`idem_key` 全局主键，无 team 列）
- **影响**：两租户使用相同键 + 相同请求体时，后到者会拿到先到者的响应（`CreateToken`/`SetGitHook`/`CreateInvitation` 的响应含明文 secret）。仓库自己在部署面用 migration 00018 修过同族问题（app 维度），通用表没跟上。若键随机且不共享，风险显著下降。
- **修复**：键空间按调用方（team/token）隔离；迁移按 00018"先建后删"形态；补跨队同键测试。成本 S-M。
- **复核**：**确认键空间事实**；严重度依赖键可预测性（报告中已标条件）。

### H12｜`install.sh` 未物化 proxy 拉取端点 → README 一行安装后 Proxy 面静默停用，Route/Console/API 发布全链失效
- **来源**：R09-H1
- **位置**：`install.sh:186-218`（unit 只拼 registry/logging/metrics env）；`traefik/provider.go:57-60`（endpoint 空即工厂失败，只能来自 `os.Getenv`，见 `:204`）；`assembly/provides.go:96-100`（失败降级 Warn+nil）；`engine/managed.go:38-41`（nil proxy 直接跳过 publishRoutes）
- **影响**：按 README 安装的新控制面：没有受管 traefik 载体、`publishRoutes` 永不执行，Route/`console.host`/`api.host`/browse 路由全部无效果；用户侧无错误（Route CRUD 成功但公网永不应答）。staging 靠 unit 手工注入该 env，8 个 install e2e 腿中仅 5 个显式注入、其余不断言 Route → 该组合零 CI 覆盖。缓解信号=启动 Warn + doctor exposure fail。
- **修复**：把端点派生进 config 面（`server.proxy_config.addr` + advertise 地址在装配期算出，经与 registry/logging/metrics 同款 ctx 注入），`os.Getenv` 仅兜底；install.sh 物化该 env；机制层加"install.sh 物化 env ⊇ Provider 工厂必需 env"守卫。成本 S。
- **复核**：**确认**（全仓该 env 设置点仅 e2e/runbook/doctor，install.sh 无；四条代码链首尾相接）。

### H13｜Console 同一 Query 键承载两种不兼容形状 → App 详情页经特定导航链首帧崩溃
- **来源**：R11-H1
- **位置**：`console/src/features/projects/project-overview.tsx:115-121`（`["deployments", appId]` 返回 `Deployment | null`）；`console/src/features/deployments/hooks.ts:25-34`（同键返回 `Deployment[]`）；`console/src/features/apps-tabs/app-overview.tsx:217`（`rows.slice(0,5)`）
- **影响**：项目总览（AppTile 填充缓存）→ 侧栏 Nodes → 节点容器深链 → App 详情时，缓存命中对象，`rows.slice` 抛 TypeError，路由错误边界整页接管；反向（面包屑回总览）把数组当单条。UI 层无跨页缓存契约测试。
- **修复**：统一形状或独立 query key + queryOptions 工厂；补跨页缓存用例。成本 S。
- **复核**：**确认**（三处源码亲读；未做浏览器复现）。

### H14｜`deploy --wait` 把 unary 的 120s deadline 泄漏进等待流 → 超过 2 分钟的部署必然假失败
- **来源**：R10-H1（overlay 探针实测）
- **位置**：`cmd/fleetly/cmd/verbs_delivery.go:242-247`（ctx 无 deadline 时包 120s）→ `:254-255`（同一 ctx 交给 `waitDeploymentFrames`，而 `--wait` 本应 noDeadline）
- **影响**：任何 >2 分钟的部署（构建源部署必然）`--wait` 退出码 1、stderr 显示中间态（如 "ended in state queued"），服务端仍在继续 → 脚本/Agent 把成功部署判为失败；`--wait` 全包零测试覆盖。服务端 `subscribeEvents` 在 ctx 取消时 `return nil`（把断开呈现为干净 EOF）放大该表象。
- **修复**：deadline 只包 unary；wait 用无 deadline/显式长 deadline；服务端区分"客户端断开"与"业务终态"；补 `--wait` 集成测试。成本 S。
- **复核**：**确认**（代码链亲读 + reviewer overlay 实测）。

### H15｜`apps restart` 以"最新冻结 Revision"为源而非生效基线 —— 失败/取消/回滚后的"重启"会重新上线失败 spec 并重跑迁移
- **来源**：R16-H1（核心声明式模型专项；Lead 亲验）
- **位置**：`cmd/fleetly/cmd/verbs_structure.go:456,460-473,481,497-521`（`latestFrozenRevision` 取 R 号最大者，在其上 bump seq 后 `Deploy(spec_file)`）；`engine/restart.go:35-49`（执行时 seq-only 判定却对 **LatestSucceeded** 基线重算）；`engine/rollback.go:41-43`（回滚 = Replay 旧 Revision，**不冻结新号**）
- **机制**：失败部署 R2 已冻结（`succeeded=R1`），`rollback` 后生效态回到 R1 但最新冻结仍是 R2。此时 `apps restart` 取 R2 → 全进程 seq+1 → 冻结 R3（内容 = 失败 spec）→ 部署。执行时判定对实际 succeeded 基线 R1 做 diff(R1,R3)：包含 R2 的镜像/命令等变更 → **不是 seq-only** → preDeployJobs 不置 done → **迁移重跑**，且"重启"把已回滚掉的失败 spec 重新上线（失败部署再次发生）。这与 ADR-0083 步骤 8（迁移不得因重启重跑）、CLI synopsis "no migration re-run"、`skills/deploy-diagnose/SKILL.md:114-116` 的承诺直接冲突；取消/超时后的 restart 同形。
- **影响**：运维/Agent 的"重启"动线在发生过失败或回滚后语义反转：不是无扰动滚动，而是重新部署失败声明 + 重跑迁移（潜在数据面副作用）；且首次失败后任何 restart 都会重跑迁移（不依赖 rollback）。
- **修复**：CLI 取"当前生效基线"（LatestSucceeded 的 ToRevision）作为 restart 源；或在引擎侧把 restart 判定改为"对最新 succeeded 基线重算且源 spec 必须等于该基线，否则拒绝/降级为普通部署"；补"失败+回滚后 restart"钉测。成本 S（机制层：restart 语义源单点，写入 ADR-0083 追记）。
- **复核**：**确认**（CLI 源、rollback 不冻结、引擎判定三处代码亲读；未真机复现）。

---

## 4. 中严重度发现（M，去重后约 105 条，按主题）

> 完整逐条见各模块报告 §4。以下为影响面较大的主题与代表条目；[来源-ID] 指向报告。

### 4.1 安全与多租户
- Login 是 PUBLIC 且不在限速面 → 密码可无限爆破/撞库；叠加 H1/H2 只需一次成功 [R14-M1]
- 节点读面对 member 全平台开放、无 Team 过滤 → 枚举所有 Team 的容器/镜像/归属 [R08-M1]
- 复合 handler 进程内直调其他服务方法绕过 scope（`templates:write` 可建 App/网络/Secret/Database/Route 并发部署；`apps:write` 的 RefreshGitHook 可触发部署）[R08-M3]
- `PutSecret` 无值大小上限 + gRPC 收包 32MiB（为 webhook 抬高）→ member 可灌 32MiB secret 无配额；85/153 RPC 无 protovalidate 注解，同族"缺 id" 有的 400 有的 404 [R08-M4/M5]
- 公开 `GetStatus` 无认证（service 默认 PUBLIC，方法无 override）→ 匿名并行探活全部 Provider + 内部地址/上游错误透传；hooks 原生入口先读 25MiB body 后验 token + ReadTimeout=0 [R08-M9/M10]
- 流式/票据面无并发/时长/数量上限；follow 绕过 unary 限流与超时；`IssueEventTicket` 每次 O(n) 清扫 [R08-M7]
- `factOf` 把 Project 归属解析失败吞成 `TeamID=""` → 事件落为平台级可见（跨队事件泄漏 fail-open）；审计 `''` 行对全员可见 [R08-M8/R13]
- GitHub 客户端出站无重定向约束与超时：api_base 私网拒绝可被 302/DNS rebinding 绕过（盲 SSRF）[R07-M4]
- 注入面：registry tags 代理仓名归属仅前缀匹配（`../` `?` `#` 路径段注入）[R03-M13]；zot content 直插 URL path [R07-L7]；VL/VM 查询构造与上游 4xx 未类型化 [R07-L1/M1]
- `install.sh` 下载物零校验和/签名 + `curl get.docker.com | sh`；Edge config 端点缺省无认证+通配绑定；KEK 轮换窗内缺失会静默生成新 KEK 并丢弃退役 KEK → 密文永久不可解 [R09/R14-M7/M6/M8]
- change freeze 可被"省略 `team_id`"绕过（CreateProject 冻结寻址字段为空即放行）[R14-M5]；非 default Team admin 省略 `team_id` 建 user/role/invitation 一律 403（ADR-0035 决策 5 退化）[R14-M4]

### 4.2 正确性与静默失败
- 非事务 `audit.Append` 并发撞 UNIQUE(seq) 丢审计行（-overlay 复现 205/256 失败），合并事实"有事件无审计" [R05-M1]
- outbox 保留窗按 `created_at` 裁剪：墙钟跳变产生 seq 中间洞，订阅方不触发 410 而静默丢事件 [R05-M4]
- `convergeVerdict` 是生产零调用的装饰函数，golden 不会因真实决策变化而变红（ADR-0082"表即断言"不成立）[R02-M2]
- 删除收口对 failed 待自动回滚窗失明（`ActiveByApp` vs 驱动权口径）→ 回滚重放可在收口后重建载体（孤儿复活）[R02-M4]
- 取消（Cancel）遗留载体回卷实际走慢通道：无审计无事件，ADR-0082 行 10 机制描述与实现不符 [R02-M5]
- 动作面进程重启对 BG 应用失效：swarm 报 not found、k3s 零命中静默成功 [R02-M3]
- compose 受理面静默丢声明：`replicas:0` 归一为 1 [R03-M6]、`$$` 二次转义（迁移命令可能错误执行）[R03-M5]、label job secrets 长语法丢 target/uid/gid [R03-M7]、external name 覆盖只对 volumes/secrets 生效 [R03-M8]、`cache_from` 死字段 [R03-M9]
- git 构建上下文以"目录存在"判完成（clone 超时/被杀留下半截目录被当有效源码）[R03-M2]；Build 终态对 Revision 粘滞（失败/cancel 后同 Revision 永久不可再部署）[R03-M4]
- 删除 Shared/App Variable 后 material push 仍沿用冻结值（ADR-0072 决策 6 不成立）[R03-M12]；git 批 compose 不冻结 source_text [R03-M11]
- 日志采集游标越过丢批 + flush 数据竞争（注释"重启重放兜底"不成立）[R04-M2]；Exec attach×finish 竞态可留节点侧孤儿进程 [R04-M4]；Owner Lease 首次布防缺失（创建后不 renew 的 resident 池永不排空）[R04-M1]
- redis browse 全链不可用：空用户名 × 方言 user 必填闸，且创建 API 假成功（R01-H1 经 Lead 校准降为 M；影响面限 redis browse 功能，无数据/安全后果）
- k3s ConfigMounts 静默丢弃（ConfigMap 只建不引用，config 文件在容器内不存在）[R06-M1]；k3s 副本数硬编码 1 致假 drift/假收敛 [R06-M2]；逐 pod 事件不聚合致多副本 degraded 被覆盖 [R06-M3]；k3s 日志帧时间/节点/until 不等价 [R06-M4]；egress netpol create-only 升级不收敛 [R06-M6]
- **声明式模型专项新增（R16，均已亲验/复现）**：
  - `RestartSeqOnly` 用 diff 路径**后缀**判定（`HasSuffix(".restart_seq")`）：用户 env 键名恰为 `restart_seq` 时，`processes[0].env.restart_seq` 被误判为 seq-only → 普通配置变更**静默跳过 preDeployJobs**（迁移不跑）；已用 overlay 实测确认 [R16-M1]
  - `posture=detect` 语义双向错位：baseline 源（启动重放、peer 撤销隔离）**绕过门限照写 Ensure 且无事件**；慢通道被拦导致 Cancel 遗留回卷在 detect 下永不发生；ADR §5/CONTEXT Posture 只写"只报事件不收敛" [R16-M2]
  - 期望账本被"未成功部署的物化"写脏（materialize 在部署成功前写 ensuredSpec/gen）→ Cancel 后检测面对遗留载体失明（rolling 形态），是 R02-M5"Cancel 只走慢通道"的根因 [R16-M3]
  - 慢通道/基线重放的成功收敛完全静默（`if src != convergeSourceFast { return }`）：ADR 行 8（域内多余载体）/行 10（Cancel 回卷）的唯一动作面可改载体而无审计、无事件；行 9（孤儿只登记）连 `WorkloadOrphaned` 事件码都不存在 [R16-M6]
  - ADR-0081 的 DECLARATION 定义与近半成员自相矛盾：identity/governance/alerting 无运行时收敛面；`SetUserPassword` 因 bcrypt 随机盐"未变重放 ≠ no-op"；守卫只查 scope 方向、语义判据零执法 [R16-M5]

### 4.3 可靠性与资源
- 收敛冷却解除径②在"无签名源"（基线/隔离重放）上恒真 → 冷却窗可被基础重放解除（钉测用注入态掩盖）[R02-M1]
- 单写者环 panic 终止无可观测面（`Loop.done` 无消费者、`Engine.Loop()` 无调用方）→ 写面静默永久停摆，health/doctor/告警均无感 [R02-M7]
- 期望账本没有退场面：BG 退役代条目永久残留 → 跨域附件隔离不变式进入永不自愈的 30s 重收敛环（无事件无审计）[R13-M1]
- 引擎"有界排水"实际被框架缺省 `StopTimeout=5s` 截断（ManagedStep 30s / NetworkRebuild 5m / Backup 15m 全部 >5s），关停后 DB/runtime 仍被 cleanup 关闭 [R13-M2 + R09-M5 合并]
- 破坏性外部动作无补偿原语：数据库改密成功后 Secret 写失败 = 凭证永久丢失（rotate 自述的 Browse 恢复路径依赖同一份被覆盖 Secret，实际不成立）[R13-M3]
- `RevokeNetworkPeer` 在 gRPC 处理器内**同步执行收敛写（Ensure）**，是环/管线之外的第二写入口（逐 App 串行 replay，RPC 可阻塞 N×30s），破坏"写永远经收敛机构"；同一隔离 30s 内另有 driftScan 兜底 [R16-M4]
- 资源无界增长：build inputs 表常驻推送凭证明文 [R03-M1]、contexts 目录无保留 [R03-M3]、swarm config 载体无清扫 [R06-M7]、进程内缓存/指标 lastCPU/backup stderr 无回收 [R04-M3]、收敛冷却记账不清理 [R02-L7]、已收敛签名漂移回归不清零 [R02-L1]
- 同步长动词与 30s unary 预算错配：`TriggerPlatformBackup`（引擎 15m）/`RefreshGitHook`/`RotateDatabasePassword` 可能 DEADLINE_EXCEEDED [R08-M11]
- `install.sh` 无升级语义：`enable --now` 不重启、覆盖运行中二进制、健康探测不分代次、无 `.bak` 回滚 [R09-M2]；无单实例约束（同数据根可双 daemon，SQLite WAL 接受双写）[R09-M7]；janitor Stop 不 join 在途 sweep [R09-M6]
- proxy 端点默认全接口 + 无认证；traefik 侧 token env 与 config 键不同名（注释误称"同键"）[R09-M8]；`data.root` 只经 env 进四个受管 Provider，config.proto 对该面无表达力 → 数据根可分裂 [R09-M4]

### 4.4 持久化与容量
- ADR-0070 seq/id"构造性永不重发"被硬删行破坏（复现 alice-1 重发；8 个硬删聚合同构）[R05-M2]
- 迁移 00016 对存量跨项目同 host+path 无清数/前置校验 → 前滚失败阻断启动（-overlay 复现；升级纪律缺口）[R05-M3]
- 单连接池 + 缺索引/全表扫描：长扫描把 API/事件流/11 条 1s 节拍环串行化尾延迟；审计/outbox/runs/builds 等查询无索引 [R05-M5/M6]
- 审计读面无游标：永续账本 API 只能读最近 ≤1000 行，更老历史不可回读 [R05-M7]；statefacade 守卫表清单停在 00005（19/38 表不在反扫面）[R05-M8]

### 4.5 契约、CI 与守卫
- CI 无生成物零漂移门禁（`generate:verify` 只活在本地；console 有、Go 侧无）；`verify:hermetic` 亦不在 CI；生成物手改可全绿 [R09-M1/R12-M5]
- 守卫空心化（R12 关键结论，均本机实跑 guards 全绿前提下做红灯反推）：
  - `acceptanceguard` 的针仍是 `.DB.Tx(`，ADR-0080 后真形态 `s.Tx.Do(` → API 层手写四件一检测器**零断言** [R12-M1]
  - Runtime 端口覆盖反扫用裸子串（`Ensure`/`Watch` 等通用词）→ 永远绿 [R12-M2]
  - ADR-0082 §6"期望账本只由 Ensure 写"可静态执法却只有注释 [R12-M3]
  - 13 条 e2e 对 `workload.converged` 引用数 0 → 缺省收敛与 detect 在 CI 不可区分 [R12-M4]
  - 词汇冻结机械执行力约 1/3（55 banned vs 113 skipped，人工评审无流程载体）[R12-M6]
  - apitest 假 Runtime 恒返回空 EnsureReport → 内容等价记账/收敛判定在全栈夹具不可达 [R12-M7]
  - 另有 loopguard 只认字面 ticker、registry Source 锚不校验函数限定符、errmapping 单针、e2e↔CI 对账防盲余量过小等 7 条 L [R12]
- 85/153 RPC 无 protovalidate 注解；审计/identity/alerting 多个 List 面完全无分页 [R08-M5/M6]
- `deploy --wait` 的等待流继承 120s deadline（H14）；CLI usage 中 18 条旗标排在位置参数后（照抄 help 即 64）、两个 delete 的成功 `--json` 为空输出、连接参数面不一致（凭据 addr 只写不读）、exec 上行两条 goroutine 并发 `stream.Send` [R10-M1/M2/M5/M7]
- gateway 取消订阅时 `return nil`，把客户端断开当正常 EOF（放大 H14；WaitRun/WaitBuild/events follow/Console SSE 同族）[R10 跨模块 + R08]

### 4.6 Console 与验收声明
- Console：部署状态词表与控制面不一致（`canceled` vs `cancelled`、缺 building/rolling-back）；quickstart 对 cancelled 终态永不 resolve；wait 流"断线重连+轮询兜底"死代码；事件页重连用旧游标重复回放；反模式守卫扫描面与"全量 src/**"声明不符且目录缺失静默通过；首屏无条件预载 xterm（293KB）+ 无 gzip/CSP（1.22MB）；新 token 默认 admin 角色；蓝绿代窗卡片判式不完整 [R11-M1~M8]
- 验收声明：**"恢复期平台只读"与升级"解除只读"是六份文档的现行承诺，全仓无只读闸门、无 Platform Restore 动词**（P1-11 挂账随 F2.3 打勾失去载体）[R15-M1]
- 收敛计划的 G1-G24 执法手册在仓内不可达（报告无 G 表，24 项中 16 项全仓零提及，部分守卫注释自认"条目名失考、反推"）[R15-M2]
- ADR-0015 的"N-1 兼容：请求版本协商头"零实现、零守卫、零挂账 [R15-M3]

---

## 5. 低严重度（L，约 75 条，摘要）

集中在：文档-代码漂移（ADR-0052 liveness/ConfigFiles、ADR-0051 端口 8081 vs 8080、ADR-0074 拒面清单滞后、ADR-0082 翻案后三处"默认 opt-in"陈旧注释、skills/task-pool "run-scoped logs known gap" 已实现、Upsert "tombstone 复活"注释、SSE Last-Event-ID 注释）；命名/防御不一致（删除动词 scope 阶梯、Cipher 关态防御、limit 直传回落 50、重复删除 200 vs 404）；可观测细节（deploymentEventPayload.Kind 恒空、console 非哈希文件 immutable、sweep 注释-实现漂移）；生成物纪律（proto→`console:gen` 有 8 例晚一个 commit、F2.8 勾选文本含明确不做的 RustFS）；仓根提交 8.6MB Linux ELF `browseprobe`；README 状态节过期；`browseprobe` 等。

两条"候选 H 升级点"需真机确认：k3s 多副本 L1 假就绪的时序概率 [R06-M3]、traefik router 名可从合法 path 生成 `@`（若整快照拒载则可冻结全平台路由变更）[R07-M2]。

---

## 6. 架构总评

### 6.1 成立的设计承诺（有证据的强项）

1. **分层不是文档纪律，是测试执法**：dep_matrix 五行矩阵 + 双向保鲜 + 豁免带理由；domain 三红线（无 os/net-http、无 genproto、无 grpc）真实扫描；12 路独立阅读零方向违反；domain 生产码环外 fleetly import = 0。这正面解决了旧项目"包边界与依赖方向脱节"的根本失误。
2. **Capability 端口窄面真实成立**：k3s 逐方法推演核心 6 方法零改动；providers 互不 import；编译期断言 + 注册表单点；可选子面 FacesOf 可枚举降级。
3. **写路径与事实发射单点化**：153 RPC 全部经 `acceptance.commit`/engine 动词（grep 零直写 + AST 守卫）；状态迁移唯一序列（transitionguard）；kernel 单声明双真（outbox+audit，factdual/factappend 守卫）。R08/R13 均称其为最扎实的架构承诺。
4. **行级授权单源且数据面覆盖系统化**：authz.go + anchor 解析图；13 读面 + 10 写面他队 403 钉死；GitHub App Global 行有平台门范式。
5. **错误面三面单点**：E_INTERNAL 脱敏 + error_id + errcodeToHTTP 双向保鲜；CLI/SDK/REST 共用信封。
6. **生成物/契约纪律可执行**：proto 单源、buf lint/breaking、477 golden 双形态、console:verify 零漂移、hermetic 构建逐字节可复现。
7. **核心声明式模型落码忠实（专项结论见 §6.4）**：缺席检测按 LatestSucceeded 拍内重投影（B1 根治）、双复查（B2/B6）、签名生命周期（B4）、fail-closed 冻结闸门、restart_seq 八步与执行时判定（B8）、双 Provider 内容等价/升级零滚测试质量高于仓内平均；29 项对抗裁决 22 项有落码、0 项未落码。
8. **测试/运维文化密度高**：e2e 12 腿进 CI；真 SQLite + 假时钟 hermetic 夹具；事故根因多数有守卫/测试承载且注释带事故锚；golden/Skills/ADR 验收锚形成"文档-测试-代码"三角。

### 6.2 结构性问题（按根因）

**S1｜权限模型只有一维（resource:action），缺"平台 vs Team"第二维**（H1/H2/H10 共同根因）。行级授权补了数据面 Team 边界，但 platform/nodes/users/teams/roles/templates 是平台级或身份级资源，行级授权不可达；内建 admin 模板把平台权限一并授予；`requirePlatformAdmin` 范式已存在却只被用了一处；角色授予又无特权上限。修法应落 ADR（平台面清单 + scope 词表拆分 + role 授予上限 + AST 守卫），不是逐 handler 补丁。

**S2｜失败处理存在"诚实失败"与"静默降级"两套文化**。优秀失败面很多（错误信封、honest 501/503、ErrConflict 归一、专门修过的假成功），但仍有静默失败族（browse 创建假成功、k3s restart 零命中成功、日志游标越过丢批、事件 team fail-open、收敛慢通道无审计、Build 终态粘滞、install 缺路由面）。根因是失败语义没有统一立法 + 守卫只在部分面反扫。

**S3｜非事务/外部副作用路径缺统一基座**。四件一拍在事务内很干净；但非事务 Emit（收敛事实、登录审计）、ObjectStore 流式写、Platform Backup 归档链各自手搓 → 审计 seq 竞态丢行（已复现）、pipe 挂死、快照并发损坏、rotate 无补偿。需要"事务外事实发射契约 / 导出-归档单飞与完整性基座 / 外部动作补偿原语"三件。

**S4｜双 Runtime 靠"两份实现 + 各自单测"维护，缺共享一致性套件**。k3s RBAC、ConfigMounts、副本数、pod 事件聚合、日志帧——每条都是"swarm 有、k3s 漏"，而两边单测各自全绿。需要 testruntime 驱动的 Provider 契约一致性套件。

**S5｜"声称的守卫"与实际执法面有落差（守卫空心化）**。convergeVerdict 装饰 golden、FacesOf 只断言 11/14、statefacade 清单停在 00005、k3s RBAC 只钉表自身、acceptanceguard 针失配、coverageguard 裸子串、wording 1/3 执行率。**架构由测试承载是真的，但"声明→执法"链条仍有断点**，且没有元守卫发现"测试名不副实"。

**S6｜升级/灾备/安装链只在"小规模 + 顺序执行 + 手工 env"下验证**。H7/H12/H6、迁移 00016、构建源 restart、install 无升级语义、无单实例——共同点是 happy-path/单触发/低规模验证。建议把"重启/升级/并发触发/存量数据形态/原生安装"作为独立测试维度。

**S7｜文档与实现的双向漂移缺少"验收锚执法"**。ADR/README/runbook 承诺密度极高是优点，但"恢复期只读/Platform Restore"六份文档承诺却全无实现、G1-G24 证据链仓内不可达、ADR-0015 N-1 协商零实现、F2.8 勾选含明确不做的项——**验收锚本身没有守卫**。建议 ADR 模板增加"可执行锚"字段并由 R12 式核查定期对账。

**S8｜可观测性盲区在"控制面自己坏了"**。单写者环 panic 死亡无消费面、收敛慢通道/失败冷却事件化不足、日志采集游标丢批、GetStatus 语义分裂（traefik Health 恒 true）、备份失败无行。控制面失去自诊断能力比单个业务 bug 更危险。

### 6.3 演进建议（投入产出排序）

1. 权限模型 ADR + 守卫（S1）——不做谈不上多租户。
2. 失败语义立法 + 静默失败守卫（S2）。
3. 三个共同基座：事务外事实发射、导出-归档链、外部动作补偿（S3）。
4. Provider 契约一致性套件 + RBAC 调用面 AST 守卫（S4）。
5. 守卫元审计：对每个 ADR 验收锚/守卫做反向验证（删守卫应红），把本次 R12/R15 的核查方式固化为 CI 任务（S5/S7）。
6. 升级/灾备/安装压测维度（S6）。
7. 控制面自观测（S8）。
8. 可移植性显式化：Windows 下 `go build ./...` 因 `syscall.SIGWINCH` 失败、keyring 权限测试恒红；如无 Windows 支持计划，应在 README/CI 显式声明"仅 Linux"，否则开发体验与测试基线不可复现。

### 6.4 核心声明式模型专项结论（R16）

**总判定：核心机制通过，外围承诺部分不成立。** 设计母本 + 三份 ADR 的骨架在代码里真实存在（不是文档工程）：29 项对抗裁决 **22 已落码 / 7 部分 / 0 未落码**；四不变量中"Ensure 唯一写动词""内容寻址等价""观测与收敛分面"均有执法点；十行收敛裁决表中行 1-7、9 有行为落点，行 8/10 只靠慢通道副作用兑现。

**但"验收锚声称的强度"高于实际**，主要集中在四处：

1. **判定表是装饰**：ADR-0082 验收锚①声称"十行落码 + golden 钉死 = 表即断言"，实际 `convergeVerdict` 生产零调用（仅测试引用），真实决策分散在 `compareSpecs`/`attemptConverge`；行 9（孤儿）连 `WorkloadOrphaned` 事件码都不存在。改动真实决策不会让 golden 变红（R02-M2 + R16 复核）。
2. **执行面承诺未兑现**：行 8（域内多余载体）/行 10（Cancel 遗留回卷）的唯一动作面走慢通道，成功收敛不发 `workload.converged`、不落审计（`convergeSucceed` 仅 fast 源发射）；`posture=detect` 下 baseline 源反而静默照写，与"只报事件不收敛"相反（R16-M2/M6）。
3. **restart_seq 语义源错位**：执行时判定正确地用 LatestSucceeded 基线（B8），但 CLI `apps restart` 的输入是"最新冻结 Revision"——失败/回滚后会把失败 spec 重新上线并重跑迁移（H15，已亲验）；另有 `RestartSeqOnly` 路径后缀可被 env 键名 `restart_seq` 欺骗、静默跳过 preDeployJobs（R16-M1，已 overlay 实测）。
4. **学说/语义无执法**：Author Class 三铁律与"未见变重放=no-op"的 DECLARATION 定义没有可执行载体（ADR 明示推迟到 autoscale 是合理取舍，但 `SetUserPassword` 的 bcrypt 随机盐反例说明定义本身需收敛）；写类守卫三盲点（ACTION/DECLARATION 不查互斥、READ 是名字前缀余集、FrozenVerbs 只 Logf）（R16-L4/L6）。

**建议**：把本模型纳入"ADR 验收锚可执行化"试点——十行裁决表改为单一判定函数并让 `attemptConverge` 真实调用（R02-M2 的 a 案）；行 8/10 收敛成功补事实双真；`posture` 语义写清 baseline 例外与 detect 的反向后果；restart 源统一为"生效基线"并在 ADR-0083 追记；`RestartSeqOnly` 改精确 proto 路径匹配（如 `processes[*].restart_seq`，拒绝任意后缀）。这四项都是 S-M 成本，但决定了"声明式"三个字是否名副其实。

---

## 7. 机制层建议（同类问题多处出现 → 机制补齐，而非逐点修）

| # | 机制 | 覆盖问题 | 落地形态 | 成本 |
|---|---|---|---|---|
| M-1 | **权限模型第二维**：平台面资源清单 + `requirePlatformAdmin` 全量接线 + role 授予特权上限 + role×scope 禁止矩阵 + "platform 资源方法必须调 requirePlatformAdmin" AST 守卫 | H1/H2/H10、R14-M3/M4/M5 | 新 ADR + 守卫 + e2e 矩阵 | M |
| M-2 | **失败语义清单 + 静默失败守卫**：写路径/后台环/外部副作用按"必须可见"分类；fake 端口错误注入测试基座 | 静默失败族（S2） | guards + testsupport 基座 | M |
| M-3 | **事务外事实发射基座**：非事务 Emit 只允许 StreamOnly 或"只含事实的 Tx"；audit seq 改 AUTOINCREMENT 或有界重试；outbox trim 改 seq 前缀 | R05-M1/M4、R08-M8、R02-L4 | domain/writes + persistence + 守卫 | S-M |
| M-4 | **导出-归档链基座**：单飞 + 唯一临时名原子 rename + SQLite integrity_check + reader 生命周期契约 | H5/H6、R13-M3 | engine 基座 + capability 契约 | S-M |
| M-5 | **Provider 契约一致性套件**：testruntime 驱动同一批断言跑 swarm/k3s（观测字段/日志帧/ConfigMounts/事件聚合/restart 语义） | H3、H9、R06-M1~M4、R02-M1 | testsupport + CI | M |
| M-6 | **RBAC 调用面 AST 守卫**：k3s typed client 调用 → (resource,verb) ⊆ desiredClusterRole；e2e can-i 表驱动生成 | H3 复发 | guards | S-M |
| M-7 | **守卫元审计**：对 ADR 验收锚/守卫做反向验证（删守卫应红、golden 应因行为变化红）；CI 增加"门禁 step 覆盖 mise 验证任务"对账 | R12-M1~M7、R02-M2、R05-M8、R15 | 定期 CI 任务 + checklist | M |
| M-8 | **升级/灾备/安装压测维度**：多 App 慢重放、并发触发、存量数据形态、GB 级备份、install.sh 原生 env systemd 分支 | H6/H7/H12、R05-M3、R09-M2 | e2e 矩阵扩展 | L |
| M-9 | **控制面自观测**：环 panic 死亡 → health/doctor/alert 可见；收敛慢通道/失败冷却事件化；备份失败行 | R02-M7/M5/L7、R04-M2 | engine + alerting | S-M |
| M-10 | **索引/容量基座**：查询 EXPLAIN 门禁 + 扫描预算；审计/outbox 游标统一；读池分离首个对象 | R05-M5/M6/M7 | persistence + 守卫 | M |
| M-11 | **Provider 构造参数入 config 通道**：禁止工厂直接读 `os.Getenv` 作为唯一注入面；install.sh 物化 env ⊇ 工厂必需 env 守卫 | H12、R09-M4/M8 | assembly/install + 守卫 | S |
| M-12 | **关停预算单源**：WithStopTimeout ≥ ManagedStepTimeout，engineService.Stop 收自带 deadline ctx；janitor Join | R13-M2、R09-M5/M6 | assembly | S |
| M-13 | **外部破坏性动作补偿原语**：`ExternalAct{pre-record, act, commit/compensate}`，rotate/platformbackup/restore/teardown 共用 | R13-M3 | engine + domain 基座 | M |
| M-14 | **声明式模型一致性套件**：十行裁决表由单一判定函数承载并被执行面真实调用；行 8/10 收敛补事实双真；restart 源=生效基线；`RestartSeqOnly` 精确路径匹配；posture 语义补 baseline 例外与反向后果 | R02-M2、R16-H1/M1/M2/M6 | engine + ADR 追记 + 钉测 | S-M |
| M-15 | **声明式语义面守卫**：`restart` 输入必须等于当前生效基线（非最新冻结）；"慢/基线收敛必须发事实或显式标注 silent"；写类守卫补 ACTION/DECLARATION 互斥与精确前缀枚举 | R16-H1/M5/M6、R12 | guards + apitest | S |

---

## 8. 建议行动顺序（影响 × 修复成本）

**P0（安全与交付阻断，1-3 天；不做不建议任何多租户/生产使用）**
1. H2 身份面接管链（SetUserPassword/ListUsers/DeleteUser 归属门 + Login TTL）——S，最高风险（已运行时复现）
2. H1 平台面门禁 + 角色授予上限（requirePlatformAdmin 全量接线 + roleInTeam 特权上限）——S-M（已运行时复现）
3. H4 Browse ForwardAuth 会话绑定——S
4. H12 install.sh 物化 proxy 端点（README 主安装路径无路由）——S
5. H5 备份 pipe 挂死——S

**P1（1 周内；核心承诺/可用性）**
6. H10 /v1/relay 节点个体凭证（与 H1/H2 串联成跨租户链，优先级视多租户启用时点）——M
7. H11 idem 键空间按调用方隔离（迁移按 00018 形态）——S-M（条件性 H）
8. H3 k3s RBAC 三条规则 + can-i 扩表（同批修 M1，避免"403 变静默丢配置"）——S
9. H6 Platform Backup 单飞 + 原子快照 + integrity_check——S/M
10. H7 路由发布就绪门 + 冷缓存失效——S/M
11. H8 Metrics 租户过滤（先收归 platform admin 止血，再端口改造）——S→M
12. H9 BG egress 投影一行修复 + 钉测——S
13. H13 Console Query 键拆分——S
14. H14 deploy --wait deadline 修复 + `--wait` 集成测试——S
15. **H15 `apps restart` 语义源**：CLI/引擎统一取"生效基线"（LatestSucceeded 的 ToRevision），失败+回滚后 restart 不得重新上线失败 spec/重跑迁移；补钉测——S
16. 审计/事件完整性三项（audit seq、outbox trim、factOf fail-closed）——S-M
17. 单写者环死亡可观测 + 收敛慢通道事件化——S-M

**P2（2-4 周；架构/机制债）**
18. 权限模型 ADR + 守卫（M-1）；失败语义立法（M-2）；Provider 一致性套件（M-5）；守卫元审计（M-7）；声明式模型一致性套件（M-14/M-15）；CI 生成物门禁（R09-M1）
19. 资源无界增长四件（build inputs/contexts/config carriers/进程缓存）+ 索引/容量基座（M-10）+ 期望账本退场（R13-M1）+ 关停预算（M-12）+ rotate 补偿（M-13）
20. 安装/升级语义（install.sh 升级分支 + .bak + 代次探测 + 单实例 flock）——R09-M2/M7
21. k3s ConfigMounts/副本数/事件聚合/日志帧四项（同批）——R06-M1~M4

**P3（随版本演进）**
22. 静默失败族清零、文档-代码漂移批量修正（ADR-0052/0051/0074/README/F2.8 勾选/ADR-0005/0022 已翻案但本体仍 Accepted）、词汇/契约注释过期（R15-L1）、Console M/L 清理、Windows 支持矩阵显式化、生成物同 commit 纪律补守卫（R15-L4）。

---

## 9. 复核记录（Lead 对子代理结论的确认/剔除/校准）

| 项 | 动作 | 说明 |
|---|---|---|
| R08-H1/H2（→H1/H2） | **确认 + 运行时复现** | 独立验证 agent 用 `go test -overlay` 进程内复现：32 scope 含 platform:admin/nodes:admin、EnrollNode/DrainNode 放行（对照组 GitHubApp 403）；Team B admin 覆写 default-owner 密码 → Login 得到 `scopes=[*]` Token。仓库零改动。 |
| R06-H1 | 确认 | rbac.go 全表 + network/netisolate/restart 三处调用点亲读 |
| R04-H1/H2/H3 | 确认 | browse authorize/route 链、backup pipe、platform backup 并发路径亲读 |
| R07-H1 | **机制确认、触发待真机** | 空快照/部分发布+签名固化逐行核实；规模触发需 dind 多 App 实测 |
| R07-H2 → H8 | 确认并合并 R08-M2 | R14 裁为 H；修复归属移出 R07（端口 + handler） |
| R02-H1 | **机制确认（真机未复现）** | bluegreen nil egress 与 k3s label/podSelector 链亲读 |
| R09-H1 → H12 | 确认 | 全仓 env 设置点仅 e2e/runbook/doctor，install.sh 无；四条代码链首尾相接 |
| R10-H1 → H14 | 确认 | 代码链亲读 + reviewer overlay 探针实测 2m0s 假失败 |
| R11-H1 → H13 | 确认（静态） | 三处源码亲读；未做浏览器复现 |
| R14-H3 → H10 | **机制确认、动态未复现** | gateway/execrelay/swarm 三处亲读；真实劫持需 k3s/swarm 真机 |
| R14-H4 → H11 | **确认事实、降为条件性 H** | 键空间无租户维度成立；严重度依赖键可预测/跨队复用（报告已注明前提） |
| R16-H1 → H15 | **确认** | CLI 取最新冻结 Revision、rollback 不冻结新号、引擎判定对 LatestSucceeded 三处亲读；失败+回滚后 restart 会重新上线失败 spec + 重跑迁移 |
| R16-M1 | 确认 | `RestartSeqOnly` 用 `HasSuffix(".restart_seq")`；env 键 `restart_seq` 的 diff 路径 `processes[0].env.restart_seq` 同样命中（代码亲读，独立复现路径一致） |
| R16 对 R15 §5#8 的修正 | **采纳** | R15 称"TestPostureDetectSkipsConverge 不覆盖启动基线路径"事实有误（converge_test.go:530-536 恰好覆盖并把矛盾钉成期望）；posture detect 的 baseline 逃逸/慢通道反向后果升 M（R16-M2） |
| R16 对 R15 的 ADR 锚结论修正 | **部分采纳** | R15 认为 ADR-0081/0082 锚"全部真实"；R16 复核为"计数与从属断言真实，但 DECLARATION 语义与行 8/10 动作面/可发现面有实质差距"——本报告按后者表述（§6.4） |
| R01-H1（redis browse） | **降级 H→M** | 代码链确认（空用户名 × validateConn × 只 log 不 Ensure → 创建假成功）；但影响面限 redis browse 功能，无数据/安全后果 |
| R14-M2（init 后无 owner，平台功能永久不可达） | **事实修正** | "init 只建 admin、不建 owner"成立；但"永久不可达"不成立——`roleInTeam` 对内置角色一律放行，default Team admin 可直接铸 builtin-owner Token（Lead 亲验）。该缺陷并入 H1 根因（角色授予无特权上限），R14-M2 降为背景说明 |
| R06-M3（k3s 多副本 L1 假就绪） | 保持 M（候选 H） | 机制成立、时序概率待真机；若复现则升 H |
| R07-M2（traefik 名字含 @） | 保持 M（候选 H） | 是否整快照拒载待上游行为确认 |
| 其余 M | 抽查采信 | 抽查代码路径与调用方；未逐条亲验的标注"来源=子代理报告，Lead 抽查" |

> 本轮未发现"编造/无证据"级别的误报；主要校准是 R01-H1 降级、R14-M2 事实修正、R14-H4 条件化，以及若干 H 的"真机待验证"限定。

---

## 10. 覆盖与置信度

- **覆盖**：16 路评审（含 R16 核心声明式模型专项）+ 1 路独立复现。internal 生产代码约 5.9 万行（模块边界内 100%）、proto 16/16、Console 3 万行、CLI 7.9k、e2e 13 脚本、guards 41 测试、persistence 37 聚合 + 33 迁移、CI/mise/install.sh、docs 12 份真源 + ADR 抽样、近 300 commit 生成物纪律统计；R16 另做 153 RPC/写类 27/53/冻结 42=34+8/豁免 5 的机械对账与 29 项对抗裁决三态核查。各报告 §1 有"完整/抽样/未覆盖"三清单。
- **本机实测基线**：`go test ./...`（root module，无 -race）79 包 ok；失败 3 处均环境/平台面：`cmd/fleetly` [build failed]（`syscall.SIGWINCH`，Windows）、keyring 0600/0700 断言（Windows ACL）、builders railpack（Docker daemon 不可达）。`go test ./genproto/... ./sdk/go/...` 通过；`go test ./internal/guards/` ok 12-17s；Console vitest 129/129。模块评审另独立跑通 domain/engine/persistence/interfaces/CLI(SDK) 目标包测试。
- **置信度**：H 全部高（代码全链亲读；H1/H2 另有运行时复现）；M 中高（调用方核读 + 关键项 overlay 复现）；e2e/上游行为相关项已标注待复核。整体：**静态架构与代码质量结论置信度高；真机触发概率与上游语义置信度中**。
- **未覆盖**：真机 e2e/升级/备份演练、staging 声明（无宿主访问）、多进程/长跑性能曲线、上游镜像/traefik/restic/VL 行为细则、Console 浏览器级 UI、v1-v27 全迁移前滚矩阵、k3s 真机 403 复现、G1-G24 原文（仓内不可达）。

---

## 11. 修复执行状态（2026-10-19，S1-S11；已 push `origin/main`）

排期与逐阶段验收记录：`docs/plan/2026-10-19-review-remediation.md`；H8/H10 设计：`docs/design/2026-10-19-security-backlog-design.md`。执行方式：子代理逐阶段实现 + Lead 亲验验收门 + 每阶段还原点 commit + push（S1-S11 共 14 个功能/文档还原点，HEAD 见 git log）。

| 阶段 | commit | 覆盖发现 | 验收证据（Lead 亲跑） |
|---|---|---|---|
| S1 工程基线 | `01bd214e` | R10-M6（Windows SIGWINCH） | 双平台 build 0；cmd 74 PASS；guards 16s |
| S2 安全边界 | `94a5267f` | H1、H2（+R14-M2/M4） | 攻击链反转用例全 PASS；apitest/guards/cmd 绿；golden 同批 |
| S3 交付主链 | `eb852068` | H12、H4、H5、H6、H14、H13 | 6 项钉测 PASS；install guard；engine/assembly/apitest 绿；console 130/130 |
| S4 声明式/路由 | `c925ba3b` | H15、R16-M1、H9、H7 | 4 项钉测 PASS；golden 逐行核对 |
| S5 Runtime/守卫/完整性 | `cdecabe0` | H3、R12-M1、R05-M1、R05-M4、R09-M1 | RBAC 调用面守卫/审计并发/outbox 前缀；persistence 全包绿 |
| S6 k3s 材料 | `b37a61c1` | R06-M1（H3 配套） | 三载体 ConfigMount 卷/挂载 + 引用名一致；k3s/guards 绿 |
| S7 幂等作用域 | `f312687c` | H11（R14-H4） | 迁移 00034 复合主键；跨 Token/跨队 7 测试 PASS；apitest 绿 |
| S8 安全设计 | `5be1527b` | H8/H10 设计 | 方案矩阵 + 25 条验收锚 + 分步计划 |
| S9 H8 止血 | `d581c60b` | H8 阶段 A | raw 收平台管理员（403 先于 503）+ 8MiB/1000 序列上限 |
| S10 H8 阶段 B | `e9654b8c` + `b22d6cc3` | H8 | QueryScopedMetrics（固定模板/行级授权/新错误码）+ Console scoped 图表 + CLI `metrics series`；capability/apitest/guards/console 138 全绿；生成物零漂移 |
| S11 H10 | `9fe85c4e` + `f756ed70` | H10（R14-H3） | 迁移 00035 + 个体 relay 凭证 + 两步 enroll + 握手双向绑定 + 即时断连 + node.left 清理/顶替审计 + CLI/Console/runbook/ADR 追记；engine 25.9s/apitest 31.2s/guards 21.3s/console 138 全绿 |

**H 状态：15/15 条 H 已在本地修复并推送**。机制级真机余量：H7（BG/route 就绪门）与 H9（BG 旧代 egress）为代码+单测级修复，e2e 触发待 dind/staging；H3 的 k3s 真机 403 反转与 S11a/b 的 relay 新流程待 dind/staging 台账（本机无 Docker daemon）。同批修复的 M/R：R16-M1、R14-M2/M4、R10-M6、R05-M1/M4、R12-M1、R09-M1、R06-M1。

**仍余 backlog（非 H）**：
- **S12 收敛语义深水区**：R02-M2/M5、R16-M2/M3/M5/M6 + ADR-0083/0081/0082 追记（下一阶段，未开始）
- k3s CPU% 量纲/真机（H8 阶段 C）、`node.left` 抖动宽限窗裁决
- project 级 scoped 指标与 `ListAlertRules`/`ListAlertStates` Team 过滤（已裁决单列小批）
- S2 残留：ListUsers/GetUser 读面授权、Login TTL/限速、平台面 AST 守卫
- R06-M7 ConfigMap 孤建清扫、R08-M1 节点读面、以及 §4 其余 M/L
- `cmd/fleetly` golden 假时钟 flake（基线既有：manual-drive 多跑一拍导致 `created_at` 00:02/00:04 漂移；单跑绿、并发/`-count=3` 偶发）；`console:gen` Windows shim 问题（本地转发器绕过，建议单列修 gen.mjs 跨平台）

**修复边界（诚实）**：代码与生成物已 push，`generate:verify`/`console:verify` 零漂移；e2e/真机（Docker daemon 不可达）与 GitHub Actions 未运行，CI codegen job 仅静态核对；里程碑全量回归 `go test ./...` 仅 2 项本机环境失败（keyring Windows ACL、builders 无 Docker daemon）+ 上述既有 golden flake，其余全绿。

---

## 附录 A：分模块报告索引（归档于本目录）

| ID | 模块 | 报告 | H/M/L |
|---|---|---|---|
| R01 | domain 层与端口契约 | `R01-domain.md` | 1/3/2 |
| R02 | engine 部署/收敛/状态机 | `R02-engine-deploy.md` | 1/7/8 |
| R03 | engine 构建/材料/intake | `R03-engine-build.md` | 0/13/5 |
| R04 | engine 数据/自动化/观测/备份 | `R04-engine-data.md` | 3/5/3 |
| R05 | 持久化与状态层 | `R05-persistence.md` | 0/8/6 |
| R06 | Runtime Providers（swarm+k3s） | `R06-providers-runtime.md` | 1/8/4 |
| R07 | 服务类 Providers 与外部适配 | `R07-providers-services.md` | 2/5/7 |
| R08 | 接口层与 proto 契约 | `R08-interfaces-api.md` | 2/11/8 |
| R09 | 组合根/配置/安装/CI | `R09-assembly-ci.md` | 1/8/7 |
| R10 | CLI/SDK/Agent 面 | `R10-cli-sdk.md` | 1/9/4 |
| R11 | Console 前端 | `R11-console.md` | 1/8/7 |
| R12 | 守卫/测试设施/e2e | `R12-guards-e2e.md` | 0/7/7 |
| R13 | 横切：集成/事务/并发 | `R13-integration.md` | 0/3/3 |
| R14 | 横切：安全/授权/密钥 | `R14-security.md` | 4/8/4 |
| R15 | 横切：验收声明/历史挂账 | `R15-acceptance.md` | 0/3/5 |
| R16 | **核心声明式模型专项**（ADR-0081/0082/0083 + 设计母本 + 29 项裁决） | `R16-declarative-model.md` | 1/6/6 |
| 复现 | H1/H2 独立运行时验证 | `R08-H1-H2-repro.md` | — |

> 原始 216 条发现在上述报告中逐条带证据；本报告做了跨模块去重与严重度校准（H 18→15：R08-H1+R14-H2、R08-H2+R14-H1、R07-H2+R08-M2 三组合并；R16-H1 新增入列；R01-H1 降 M）。完整计数：H 15 / M≈105 / L≈80（主题归并后）。R16 的 29 项对抗裁决三态与十行裁决表落点表见其报告 §5/§6 与本报告 §6.4。

## 附录 B：本机复现命令

```sh
# 仓库零改动验证
git -C D:/Codes/qiulin/fleetly status --porcelain   # 应为空

# 基线（见 §10；3 处失败为环境/平台性）
go test ./...
go test ./internal/guards/
cd console && pnpm test

# H1/H2 运行时复现（仓外测试文件 + overlay，零仓内写入）
go test -overlay=D:/Codes/qiulin/fleetly-review-2026-10-19/verification/_tmp_r08repro_overlay.json \
  ./internal/testsupport/apitest -run 'TestR08H1TeamAdminPlatformFace|TestR08H2IdentityTakeover' -v -count=1

# 已知 Windows 构建缺口
go build ./...     # 失败：cmd/fleetly/cmd/verbs_exec.go:159 syscall.SIGWINCH
```

## 附录 C：诚实边界

- 本报告不声称 e2e/真机验收通过；所有 e2e/上游相关结论均标注"脚本/断言存在"或"机制已验、触发待真机"。
- Console 未做浏览器级复现（H13 为静态证据链）；R06-H1 的 403 为 K8s RBAC verb 语义推演（置信高、缺真机台账）。
- 评审只读：未修改仓库、未提交、未运行任何会写仓的生成/构建/e2e 命令；H1/H2 复现通过 `go test -overlay` 在仓外注入测试完成。
