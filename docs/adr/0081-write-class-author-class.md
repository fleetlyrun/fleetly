# ADR-0081: 写面立法——Write Class 三桶与 Author Class 学说

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-18 | 设计母本 `docs/design/2026-10-18-declarative-model.md` §2/§3（唯一规格源，附录分类表由 proto 全集重编）；ADR-0007（词汇冻结：Declaration/Action/Write Class/Author Class 同批入册 CONTEXT.md）；ADR-0017（变更冻结——FrozenVerbs ⊆ DECLARATION∪ACTION 断言，附录 A.3）；ADR-0024（幂等执法面——创建型前缀⊆DECLARATION∪豁免断言）；ADR-0035/0038（行级授权与 Scope——单向 scope 断言）；ADR-0069（RestartProcess 动作面）；ADR-0046（RebuildNetwork 修护动词）；ADR-0073（GitHub App 接入仪式三件）；ADR-0049/0051/0026（铸造短命凭证/会话族）；**修订 2026-10-19（S12b2）**：DECLARATION 判据分「有运行时面 = 可重放 no-op」与「状态即事实 = upsert 幂等、不承诺 no-op」两子类（§1 + §1.1 域级收敛面表）；`SetUserPassword` 归后者并写明 bcrypt 随机盐使其「未变重放 no-op」不成立（§5 / 附录 A.6）；统计 153→154（S10a 的 `QueryScopedMetrics` 未入法源，守卫早已断言 154——修法源而非改守卫降级） |

## 背景

严格声明式模型的前提是写面有一部统一立法：哪些写是「落期望态」（引擎欠你一个收敛），哪些写是「过渡/仪式/修护动作」（效果即事实，无终态可持有）。现状是三个守卫各自为政——freezeguard 按读面前缀+冻结表分桶（`freezeguard_test.go:109` 三桶完备对账）、scopeguard 按动词前缀推 op 方向、idemguard 按创建型前缀收网——没有一份全域写类学说回答「这个动词为什么是这个类」。设计对话沉淀为 P1/P2 两问（CLI/Console/将来配置面只声明期望状态；三种来源的期望态如何建模），答案立法化为本 ADR：**写类三桶（Write Class）+ 作者权属三分类（Author Class）**。

proto 全量实数：**14 个服务文件、154 个方法**（`grep -n "  rpc " proto/` 逐文件清点；spec.proto/error.proto 两个文件无服务；S10a 增只读 `QueryScopedMetrics`（READ 余集），本节修订把附录台账从 153 补到 154）。本 ADR 附录即 154 方法的完整分类表——「未分类即红」的唯一清单，由 proto 全集机械枚举重编（设计文档 §3.2 是底稿，对抗审查 A 路曾抓出漏项与错名，以 proto 全集为准）。

## 决策

### 1. Write Class 三桶

| 桶 | 定义 | 判据 |
|---|---|---|
| **READ** | 只读无副作用；检测、观测、审计是只读面 | 不改变任何行/载体/会话状态 |
| **DECLARATION**（声明写） | 落**可 diff 的持久期望态**（Revision 或实体行）。按**有无运行时面**分两个子类：①**有运行时面**——引擎欠收敛，对照该期望态持续纠正运行时，未变声明重放 = 零扰动（内容等价 skip）；②**状态即事实**——该域无运行时基底可收敛（identity / governance / alerting 全族），行内容即事实，重放语义是**行级 upsert 幂等**（同一最终状态），**不承诺 no-op**：写入不可复现内容（bcrypt 随机盐、时间戳、随机 ID）的动词归此类 | 写后存在可 diff 的期望态（Revision 或实体行），且其收敛面归属（有/无）经域级表登记（§1.1） |
| **ACTION**（动作写） | 无终态可持有的过渡/仪式/修护动词 | 效果是发生的过渡事实，不留下被持有的期望态 |

默认归类规则：`Create/Put/Delete/Set/Revoke` 前缀的实体行写 = DECLARATION；`Get/List/Wait/Stream/Diff/Query/Download/Explain/WhoAmI` 前缀 = READ；ACTION 是**例外清单**——每一入桶成员带一句理由（附录），例外不再命中即红（双向保鲜）。实体行写的重放语义是**按行内容的 upsert 幂等**，不是「声明重放零扰动」——两个子类的重放承诺不混用（§1.1、§5）。

### 1.1 收敛面归属（域级表）

DECLARATION 的「有/无运行时面」是**域属性**，逐域登记（不为 154 行逐行加列——列不产生新信息，只会给 53 条声明写制造噪音；逐 RPC 的判据仍由附录「注」列承担）：

| 域（proto 包族） | 运行时面 | 理由 |
|---|---|---|
| delivery（Revision/Deployment/Build/Hook/GitHub App） | **有** | 部署管线 → Ensure 收敛，材料经指纹接棒 |
| structure（Project/App/Secret/Config/Variable/Volume/Database/Network/Peer 行） | **有** | 实体行是 Ensure 的输入面（行集收敛/材料注入/网络附件） |
| proxy（Route 行） | **有** | 行集指纹 → Route 全量发布 |
| runtime（Node 行） | **有** | schedulable 期望态可对照可收敛 |
| automation（Task/Schedule 行） | **有** | desired_concurrency 与周期规则由域内环/调度器对照（不是 Ensure——收敛机构在域内环） |
| identity / governance / telemetry（identity 23、governance 3、alerting 4 条声明写） | **无** | 无运行时基底；行即事实，重放只有行级 upsert 幂等 |

> 守卫：`TestWriteClassConvergenceFaceByDomain`（`internal/guards/writeclassguard_test.go`）按 proto 包登记——新增服务文件/域而未分类即红；表项不再承载 DECLARATION 方法同样红（双向保鲜）。`fleetly.system.v1` 的 DECLARATION 全部来自 `governance.proto`，故表中按 governance 域登记。本表**不是语义证明**，而是强制把「该域有无收敛面」写成可对账的声明，新域出现时逼一次裁决（与字段权属的「推迟但记账」同款策略）。

### 2. Author Class 学说（设计 §2 全量搬入）

期望态是**实体联邦**（不是一份合并大文档），三类作者各持自己的期望态形态与收敛节律：

| 作者类 | 定义 | 现役代表 | 权威/节律/持久化 |
|---|---|---|---|
| **Document** | 部署事务的输入文档，冻结成 Revision | compose（ADR-0074 受理 + 原文冻结）；模板实例化产出的 spec | 事务性（部署时刻）；内容寻址；不常驻不参与每拍收敛 |
| **Entity** | 持续在线的平台实体行 | Databases / Routes / Configs / Variables / Volumes / Networks / Secrets | 持续性（每次编辑 = 实体自身变更）；行级权属 + 独立收敛环或 Ensure 时输入 |
| **System** | 控制环与平台决策 | 受管域代码声明、pin 决策、凭证铸造、模板版本、（将来）缩扩容执行 | 派生槽位；可从 succeeded 基线重放重建 |

**SYSTEM 三铁律**：

1. **授权槽位**：系统只写用户经策略委托的字段（无缩扩容策略，系统永不碰 replicas——现状即如此，replicas 纯用户声明）。
2. **drift 对照 effective 而非 authored**：自动调整后，收敛/漂移判定对照合成后期望态，否则控制环输出每拍都是漂移假阳性。
3. **声明重放不清洗系统态**：声明面重跑不回滚系统调整；显式删除策略才让槽位回落用户值并收敛。

**冲突语义表**：

| 冲突 | 裁决 | 执法点 |
|---|---|---|
| 文档 vs 实体（字段重叠） | 分界即所有权：workload 形状归文档、平台资源归实体 | 受理闸门 |
| 用户 vs 用户（push vs console） | ADR-0072 已裁（push 合并 / spec_file overlay；只读观测面见 ADR-0071） | 在役 |
| 用户 vs 系统 | 无授权 = 用户赢；有授权 = 系统在策略区间内赢 | 策略实体 + 合成函数 |
| 任何来源 vs 运行时 | effective 赢（这是收敛的定义） | drift + Ensure |

App spec 与实体的关系是**按引用绑定，不按值内嵌**（compose external 声明律，`composeintake` 受理闸门）；跨实体合成以 AppVariable 三级合成为现役先例（Shared 底 < 源声明 env < AppVariable 顶，冻结咽喉执行），将来配置面不新造合成器，是把它升格为通用律。

字段/实体级权属注册表与守卫**推迟到 autoscale 落地同批**——不造无人消费的机制（与第 3 条裁决同一原则）。

### 3. 执法面选型：Go 表守卫先行，一方 proto 注解推迟（对抗审查 C3 裁决）

立法执法面 = **本附录完整分类表 + Go 侧表守卫**（`writeclassguard_test.go`，freezeguard「proto 全方法 × 桶完备对账」的既定形态：扫描 proto 反射全集、逐方法落桶、未分类即红、死条目红）。一方 proto 注解（classification 消息 + MethodOptions 扩展号）**推迟到配置面立项同批**。核心理由：**注解面在本程序内唯一消费者是守卫自身，运行时零消费——不造无人消费的机制**。届时注解只是把已由守卫保鲜一轮的分类表机械搬运进 proto；扩展号现状 MethodOptions 已占用 1042（openapiv2）与 52301（grpcapi method_auth），届时取 **5240x** 段。

### 4. 守卫断言族（Batch B 落地 `writeclassguard_test.go`）

1. **完整分类**：proto 全部 154 方法逐个落桶（附录表为法源），未分类即红；表内出现 proto 不存在的方法即死条目红。
2. **ACTION 双向保鲜**：例外清单不再命中 proto 即红（freezeguard 豁免面同款机制）。
3. **单向 scope 断言**：DECLARATION ⇒ scope 注解为 WRITE/ADMIN（声明写必是授权写面）；**ACTION 允许 READ-scope**——不设「ACTION ⇒ WRITE」反向断言（VerifyBackup 携 databases:read 等先例在案），READ-scope 的 ACTION 经 writeclassguard 自有豁免面显式登记（初值三条：VerifyBackup/BrowseDatabase/IssueEventTicket，按 PolicySet 在役 scope 实况对账；`scopeOpExemptions` 既有五条是 scopeguard 的动词方向豁免面，同形不同轴）。
4. **FrozenVerbs 从属断言**：`governance.FrozenVerbs ⊆ DECLARATION∪ACTION`（冻结只拦写面；现表 42 条逐一可对账——其中 RestartProcess/RebuildNetwork/RotateDatabasePassword/RotateHookToken/RefreshGitHook/TriggerSchedule/ReceiveWebhook/CompleteManifestFlow 八条是 ACTION，余为 DECLARATION）。
5. **idem 前缀从属断言**：`idem.EnforcedMethods` 的创建型前缀集成员 ⊆ DECLARATION∪豁免——表中 ReceiveWebhook/TriggerBackup/TriggerPlatformBackup/RebuildNetwork 四条 ACTION 经豁免通道显式登记（各自「重放安全」理由已在 enforcer 表内注记）。

### 5. 边界案例钉死

- **SetUserPassword = DECLARATION（状态即事实子类）**：判据 = **有无收敛面接棒**。密码行落定即事实（identity 域无运行时基底可收敛，与全部 identity 实体同律）。**重放语义显式降级**：`bcrypt.GenerateFromPassword` 每次新盐（`internal/interfaces/grpc/fleetlygrpc/identity.go:181`），同明文重放产生**新 hash**——行内容必变，**不是 no-op**；它的「幂等」只成立在「行最终状态 = 最后一次设置」这一语义层。这正是「状态即事实」子类必须与「可重放 no-op」子类分立的反例，入册为本 ADR 的判据样本。Rotate 系（RotateDatabasePassword/RotateHookToken）铸新凭证后效果要经材料指纹进入收敛面接棒（下一拍 Ensure 携新材料）——动作改事实 + 收敛面接棒的混合范式，是 ACTION 与 DECLARATION 协作的标准形态。
- **铸造短命凭证/会话族（成组裁决）**：CreateExecSession、StreamExecSession、IssueEventTicket、BrowseDatabase——铸短命会话/一次性票据，非资源行（不可列表回读，台账 = 审计），ACTION 语义；scope 方向豁免沿用 `scopeOpExemptions` 既有文案。
- **DrainNode = ACTION vs CordonNode/UncordonNode = DECLARATION**（同域两动词分属两类）：Cordon 落节点行 schedulable 期望态（引擎可对照、可收敛回真）；Drain 是迁移语义归编排器的过渡过程（驱散负载即完成，节点行不承载 draining 期望态）。
- **RebuildNetwork = ACTION**：期望态是网络行本身（不变），重建是对 swarm overlay 病灶的修护过程（ADR-0046），修护完成即消隐。
- **Rollback = DECLARATION**：向旧 Revision 的声明性回归，经部署管线 Replay——回卷的是期望态，不是动作。
- **ScaleTask = DECLARATION vs StopTask = ACTION**：ScaleTask 落 desired_concurrency 行（池期望态，补足环对照它）；StopTask 是停止指令（一次性语义域，行不承载「已停止」期望态）。
- **RestartProcess = ACTION（--runtime 逃逸通道）**：ADR-0069 零 lineage 动作面；其声明面（restart_seq）见 ADR-0083，双面并存语义各自诚实。
- **UploadSource = DECLARATION**：上传产物落材料库（材料实体），供部署声明引用。
- **InstantiateTemplate = DECLARATION**：蓝图展开为实体声明集（App/Secret/Database/Route 声明），产出走各自实体写径。
- **CancelDeployment = ACTION**：终止在途迁移的过渡动词（其遗留载体的收敛归属见 ADR-0082 裁决表 Cancel 行）。

### 6. 词汇消歧（对抗审查 A10：接受消歧、驳回改名）

Write Class 的 **ACTION** 与 Scope 词条 `resource:action`（read/write 授权轴）同词异轴：授权 action 见 CONTEXT.md Scope 词条；本 ADR 的 ACTION 是写类轴（Write Class 的桶名）。CONTEXT.md Declaration 词条内一句话消歧，不改名（IMPERATIVE 等改名成本高收益低）。

## 附录 A：完整分类表（proto 全集 154 方法，未分类即红的唯一清单）

统计：**ACTION 27 + DECLARATION 53 + READ 74 = 154**。实数对账：automation 16 + delivery 22 + github_apps 7 + registry 2 + templates 4 + identity 23 + proxy 3 + exec 3 + nodes/runtime 6 + workloads 1 + structure 42 + governance 3 + system 7 + telemetry 15 = 154。

### A.1 automation/v1（TasksService 7 + RunsService 4 + SchedulesService 5 = 16）

| RPC | 类 | 注 |
|---|---|---|
| CreateTask | DECLARATION | Task 行落定 |
| GetTask / ListTasks | READ | |
| ScaleTask | DECLARATION | desired_concurrency 落行（池期望态，补足环对照它） |
| StopTask | ACTION | 停止指令：一次性语义域，行不承载「已停止」期望态 |
| DeleteTask | DECLARATION | 实体行删除 |
| RenewTask | ACTION | Owner Lease 租约续期仪式（过渡语义，ADR-0012） |
| GetRun / ListRuns / WaitRun | READ | |
| StopRun | ACTION | 终止一次执行的过渡动词（终态由 Run 状态机承载） |
| CreateSchedule | DECLARATION | 周期规则行落定 |
| GetSchedule / ListSchedules | READ | |
| DeleteSchedule | DECLARATION | 实体行删除 |
| TriggerSchedule | ACTION | 仪式动词：按时立即生成一次 Run，不改规则行 |

### A.2 delivery/v1/delivery.proto（DeploymentsService 7 + RevisionsService 4 + BuildsService 5 + HooksService 6 = 22）

| RPC | 类 | 注 |
|---|---|---|
| Deploy | DECLARATION | 部署管线声明写（冻结 Revision → 管线收敛） |
| GetDeployment / ListDeployments / WaitDeployment | READ | |
| CancelDeployment | ACTION | 终止在途迁移的过渡动词（遗留载体收敛归属见 ADR-0082） |
| Rollback | DECLARATION | 向旧 Revision 的声明性回归，经管线 Replay |
| RestartProcess | ACTION | ADR-0069 零 lineage 动作面（--runtime 逃逸通道）；不动 Spec/Revision |
| ListRevisions / DiffRevisions / GetRevision / GetAppWorkloads | READ | |
| ListBuilds / StreamBuildLogs / WaitBuild / ListUploads | READ | |
| UploadSource | DECLARATION | 上传产物落材料库（材料实体，供部署声明引用） |
| SetGitHook | DECLARATION | hook 配置行落定 |
| GetGitHook | READ | |
| RotateHookToken | ACTION | Rotate 系：铸新 token 材料，经材料指纹进收敛面接棒 |
| RefreshGitHook | ACTION | 修护动词：向 GitHub 重取 hook 配置对齐在册态 |
| DeleteGitHook | DECLARATION | hook 配置行删除（解除通道，W-8） |
| ReceiveWebhook | ACTION | 仪式动词：外部事件摄取入口（ADR-0009 pull-only） |

### A.3 delivery/v1/github_apps.proto（GitHubAppsService 7）

| RPC | 类 | 注 |
|---|---|---|
| SetGitHubApp | DECLARATION | 接入材料行落定（受理即验证，ADR-0073） |
| ListGitHubApps / ListGitHubAppRepos | READ | |
| DeleteGitHubApp | DECLARATION | 接入材料行删除 |
| StartManifestFlow | ACTION | 仪式三件：签发跳转 URL，零行写入（ADR-0073 决策 8） |
| ConsumeManifestCode | ACTION | 仪式三件：一次性 code 兑换进进程内流存储，零行写入 |
| CompleteManifestFlow | ACTION | 仪式三件：流态收口落行步（接入材料的声明面是 SetGitHubApp） |

### A.4 delivery/v1/registry.proto（RegistryService 2）

| RPC | 类 | 注 |
|---|---|---|
| ListRegistryCatalog / ListRegistryTags | READ | |

### A.5 delivery/v1/templates.proto（TemplatesService 4）

| RPC | 类 | 注 |
|---|---|---|
| ListTemplates / GetTemplate | READ | |
| InstantiateTemplate | DECLARATION | 蓝图展开为实体声明集（App/Secret/Database/Route 声明） |
| RefreshTemplates | ACTION | 上游模板目录刷新动词（目录重建，非实体行写入，ADR-0050） |

### A.6 identity/v1（UsersService 7 + TeamsService 4 + RolesService 4 + TokensService 4 + InvitationsService 3 + AuditQueryService 1 = 23）

| RPC | 类 | 注 |
|---|---|---|
| WhoAmI | READ | |
| CreateUser | DECLARATION | 身份实体行落定（状态即事实，identity 域无收敛面） |
| Login | ACTION | 仪式动词：身份验证铸会话凭证，无平台期望态 |
| SetUserPassword | DECLARATION | 密码行即事实（无收敛面接棒——与 Rotate 系的分界）；**重放非 no-op**（bcrypt 随机盐，`identity.go:181`），属「状态即事实」子类 |
| GetUser / ListUsers | READ | |
| DeleteUser | DECLARATION | 身份实体行删除 |
| CreateTeam / DeleteTeam | DECLARATION | 身份实体行落定/删除 |
| GetTeam / ListTeams | READ | |
| CreateRole / DeleteRole | DECLARATION | Role 行落定/删除 |
| GetRole / ListRoles | READ | |
| CreateToken | DECLARATION | 令牌行落定（状态即事实） |
| GetToken / ListTokens | READ | |
| RevokeToken | DECLARATION | 吊销落行（状态即事实，无收敛面接棒） |
| CreateInvitation | DECLARATION | 邀请行落定 |
| ListInvitations | READ | |
| AcceptInvitation | ACTION | 仪式动词：一次性兑换成 User（兑换即完成，无终态可持有） |
| ListAudit | READ | |

### A.7 proxy/v1（RoutesService 3）

| RPC | 类 | 注 |
|---|---|---|
| CreateRoute | DECLARATION | Route 行落定（行集 + 签名短路的收敛面输入） |
| ListRoutes | READ | |
| DeleteRoute | DECLARATION | Route 行删除 |

### A.8 runtime/v1/exec.proto（ExecService 3）

| RPC | 类 | 注 |
|---|---|---|
| CreateExecSession | ACTION | 铸造短命会话族：铸 exec 会话（非资源行，ADR-0049） |
| StreamExecSession | ACTION | 铸造族：会话双向流传输本体（携 stdin，ADR-0049） |
| ListExecInstances | READ | |

### A.9 runtime/v1/runtime.proto（NodesService 6）

| RPC | 类 | 注 |
|---|---|---|
| ListNodes / ListNodeContainers | READ | |
| EnrollNode | DECLARATION | 节点实体落地落行 |
| DrainNode | ACTION | 迁移语义归编排器的过渡过程（节点行不承载 draining 期望态） |
| CordonNode | DECLARATION | 节点 schedulable 期望态落行（可对照、可收敛回真） |
| UncordonNode | DECLARATION | 同上（期望态翻回） |

### A.10 runtime/v1/workloads.proto（WorkloadsService 1）

| RPC | 类 | 注 |
|---|---|---|
| GetAppWorkloadObservations | READ | 观测层只读透出（ADR-0071） |

### A.11 structure/v1（ProjectsService 4 + AppsService 5 + SecretsService 3 + ConfigsService 3 + SharedVariablesService 3 + AppVariablesService 3 + VolumesService 3 + DatabasesService 10 + NetworksService 8 = 42）

| RPC | 类 | 注 |
|---|---|---|
| CreateProject / DeleteProject | DECLARATION | 实体行落定/删除 |
| GetProject / ListProjects | READ | |
| CreateApp / DeleteApp | DECLARATION | 实体行落定/删除 |
| GetApp / GetAppSpec / ListApps | READ | |
| PutSecret / DeleteSecret | DECLARATION | 实体行落定/删除（值永不回显，ADR-0014） |
| ListSecrets | READ | |
| PutConfig | DECLARATION | 实体行落定（版本化冻结新版本） |
| GetConfig / ListConfigs | READ | |
| PutSharedVariable / DeleteSharedVariable | DECLARATION | 实体行落定/删除 |
| ListSharedVariables | READ | |
| PutAppVariable / DeleteAppVariable | DECLARATION | 实体行落定/删除（三级合成顶层，ADR-0072） |
| ListAppVariables | READ | |
| CreateVolume / DeleteVolume | DECLARATION | 实体行落定/删除 |
| ListVolumes | READ | |
| CreateDatabase / DeleteDatabase | DECLARATION | 实体行落定/删除 |
| GetDatabase / ListDatabases | READ | |
| TriggerBackup | ACTION | 过程性动作：产出备份产物，非期望态（ADR-0039） |
| ListBackups | READ | |
| VerifyBackup | ACTION | 产出验证事实（一次性校验结果，无终态可持有） |
| DownloadBackup | READ | 流式读面 |
| BrowseDatabase | ACTION | 铸造族：Browse Session 受理铸造（短命会话，ADR-0051） |
| RotateDatabasePassword | ACTION | Rotate 系：铸新凭证经材料指纹进收敛面接棒（混合范式） |
| CreateNetwork | DECLARATION | 实体行落定 |
| ListNetworks | READ | |
| RebuildNetwork | ACTION | 修护动词：期望态是网络行本身，重建是对病灶的修护过程（ADR-0046） |
| DeclareNetworkPeer | DECLARATION | peer 行声明（pending 行落定） |
| ApproveNetworkPeer | DECLARATION | peer 行裁决落定 |
| RevokeNetworkPeer | DECLARATION | peer 行吊销落定 |
| GetNetworkPeer / ListNetworkPeers | READ | |

### A.12 system/v1/governance.proto（GovernanceService 3）

| RPC | 类 | 注 |
|---|---|---|
| SetChangeFreeze | DECLARATION | 治理态落行（ADR-0017） |
| LiftChangeFreeze | DECLARATION | 治理态解除落行 |
| ListChangeFreezes | READ | |

### A.13 system/v1/system.proto（SystemService 5 + PlatformService 2 = 7）

| RPC | 类 | 注 |
|---|---|---|
| GetVersion / GetStatus / GetSchema / Explain | READ | |
| RestartComponent | ACTION | 受管域治理动词：编排器强制重排语义，无用户 spec 面（ADR-0069 先例） |
| TriggerPlatformBackup | ACTION | 过程性动作：控制面导出过程（ADR-0039） |
| ListPlatformBackups | READ | |

### A.14 telemetry/v1（EventsService 4 + LogsService 1 + MetricsService 2 + AlertingService 8 = 15）

| RPC | 类 | 注 |
|---|---|---|
| ListEvents / StreamEvents / GetEventStatus | READ | 事件面恒只读 |
| IssueEventTicket | ACTION | 铸造族：一次性只读 SSE 短票（ADR-0026） |
| StreamLogs | READ | |
| QueryMetrics | READ | |
| QueryScopedMetrics | READ | 行级 scoped 查询（S10a，ADR-0071 观测面；READ 余集，S12b2 补入法源） |
| CreateNotificationChannel / DeleteNotificationChannel | DECLARATION | 通道实体行落定/删除 |
| TestNotificationChannel | ACTION | 诊断仪式：发一帧出站测试载荷，通道行不变（ADR-0041） |
| ListNotificationChannels | READ | |
| CreateAlertRule / DeleteAlertRule | DECLARATION | 规则实体行落定/删除 |
| ListAlertRules / ListAlertStates | READ | |

## 后果

- 写面从此有一部统一学说：新 RPC 落地时先答「哪个桶、哪句理由」，freezeguard/scopeguard/idemguard 三面守卫获得同一法源（写类表），三表从属断言防止各表漂移。
- 分类表由守卫双向保鲜：proto 演进（加/删方法）时表不同步即红——立法文件与代码零漂移。
- proto 注解面推迟不欠账：配置面立项时注解只是机械搬运已保鲜的分类表；扩展号 5240x 预留在案。
- ACTION 例外清单带理由逐条可审计：未来争议（如某动词该不该是 ACTION）以「有无终态可持有」与「有无收敛面接棒」两条判据裁决。
- Author Class 学说为将来的缩扩容策略实体提供立法地基（授权槽位 + effective 对照 + 不清洗三铁律），实现面维持 v1-out backlog 不动。

## 验收锚

- [x] `writeclassguard_test.go` 完整分类：proto 全部 154 方法逐个落桶（本附录为法源），未分类即红、死条目即红；批内对账 27 ACTION + 53 DECLARATION + 74 READ（2026-10-18 Batch B 兑现；S12b2 补 `QueryScopedMetrics` 行并同步 154 计数）
- [x] ACTION 例外双向保鲜：清单成员不再命中 proto 即红（2026-10-18 Batch B 兑现）
- [x] 单向 scope 断言：DECLARATION ⇒ WRITE/ADMIN；ACTION 的 READ-scope 成员经豁免通道显式登记（writeclassguard 豁免面，初值 VerifyBackup/BrowseDatabase/IssueEventTicket 三条，2026-10-18 Batch B 兑现）
- [x] FrozenVerbs ⊆ DECLARATION∪ACTION 断言（42 条逐一可对账，含 8 条 ACTION 成员：RestartProcess/RebuildNetwork/RotateDatabasePassword/RotateHookToken/RefreshGitHook/TriggerSchedule/ReceiveWebhook/CompleteManifestFlow，2026-10-18 Batch B 兑现）
- [x] idem 创建型前缀 ⊆ DECLARATION∪豁免断言（ReceiveWebhook/TriggerBackup/TriggerPlatformBackup/RebuildNetwork 四条经豁免通道，2026-10-18 Batch B 兑现）
- [x] DECLARATION 判据分两子类（有运行时面 = 可重放 no-op / 状态即事实 = upsert 幂等、不承诺 no-op）+ 域级收敛面表登记（`TestWriteClassConvergenceFaceByDomain`；新域未分类即红、表项双向保鲜，S12b2 兑现）
- [x] ACTION/DECLARATION 互斥断言（同方法两表命中即红；补完备性扫描 `continue` 序的盲点，`TestWriteClassBucketsMutuallyExclusive`，S12b2 兑现）
- [x] `SetUserPassword` 的「重放非 no-op」反例在案（附录 A.6 + §5；bcrypt 随机盐 `identity.go:181`，S12b2 兑现）
- [x] proto 零触碰（本 ADR 执法面是 Go 表；注解面随配置面立项另批——Batch B 守卫批实证）

## 修订记录

- **2026-10-19（S12b2，文档修订；对应 S12b1 代码 commit `2740a09d`）**：
  1. §1 DECLARATION 判据拆两子类（「有运行时面 = 可重放 no-op」与「状态即事实 = upsert 幂等、不承诺 no-op」），并把缺省归类规则中「实体行写的重放语义」一句补明；
  2. 新增 §1.1 域级收敛面表（delivery/structure/proxy/runtime/automation = 有；identity/governance/telemetry = 无），不为 154 行逐行加列；
  3. §5 边界案例与附录 A.6 把 `SetUserPassword` 明确归「状态即事实」子类，并写明 bcrypt 随机盐使「未变重放 no-op」不成立（`identity.go:181`）；
  4. 统计 153→154：§1 背景、附录标题与统计、§4 断言、验收锚四处同批修正；附录 A.14 补 `QueryScopedMetrics | READ` 行（S10a 遗漏入法源；`writeclassguard_test.go:201` 早已断言 154——修法源而非改守卫降级）；
  5. 守卫补强：`TestWriteClassBucketsMutuallyExclusive`（ACTION/DECLARATION 互斥）与 `TestWriteClassConvergenceFaceByDomain`（域级收敛面）；三桶定义与 Author Class 学说未改，历史决策不删。
