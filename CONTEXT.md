# fleetly

fleetly 是一个轻量 PaaS：把源码或镜像变成运行在可插拔运行时上的工作负载，并提供流量接入、托管数据服务与观测。人类与 AI Agent 同为一等用户。

本文件只承载词汇，不承载架构。结构与决策见 `docs/design/` 与 `docs/adr/`。

## Language

_Avoid_ 词条执法的是 **fleetly 域内语义混用**（同义词绕开冻结命名）。第三方专有词汇不在禁列——按其本义使用合法，如 GitHub 的 Organization 实体（安装目标、属主名）。此类用法经守卫白名单（`internal/guards` wordingExemptions）带理由豁免，双向保鲜。

### 组织与访问

**Team**:
权限轴。User 与 Token 通过 Team 内的角色获得资源访问权。
_Avoid_: organization, tenant, workspace

**User**:
人类身份。
_Avoid_: account, member

**Role**:
命名的 Scope 集合，在 Team 内授予 User 或 Token。
_Avoid_: permission, policy

**Scope**:
`resource:action` 形式的授权单元；write 蕴含 read。`platform` 资源承载集群面权力（活加入材料轮换等，C3）。
_Avoid_: grant, capability(授权义)

**Token**:
携带 Scope 的凭证，服务人与 Agent。首启由 Bootstrap Token 引导，一切 Token 可吊销。有属主（user）Token 的有效授权 = min(声明, creator 当前授权)，逐请求求交（ADR-0038）。
_Avoid_: API key, PAT, credential

**Invitation**:
一次性、限时、绑定角色的加入链接；兑换后成为 User。管理面入队的唯一途径（开放注册不设）。
_Avoid_: signup link, invite code, share link

### 结构

**Project**:
归属与网络隔离轴，属于一个 Team；资源名只在 Project 内唯一。
_Avoid_: namespace, group, workspace

**Environment**:
被否决的实体（ADR-0011）：不设 Project 内环境层；环境即 Project（如 `shop` / `shop-staging` 命名约定）。
_Avoid_: stage, profile, environment(作实体名), env(作实体名缩写)

**App**:
长运行可部署单元，由一个或多个 Process 组成。
_Avoid_: service, stack, workload, site

**Process**:
App 内的进程模板（如 web、worker）。
_Avoid_: service, container, dyno

**Variable**:
App 级变量是一等平台聚合（AppVariable，按 App 存储明文非敏感值，敏感值走 Secret）；Project 级共享变量称 Shared Variable。三级合成在冻结咽喉（ADR-0072，承 ADR-0043 两级）：Shared Variable 在下、源声明 env 居中、AppVariable 在顶（同键覆盖）——env 不写死在源/配置文件里，Revision 冻结的即最终生效集；变量轮换需显式部署生效。
_Avoid_: env var(标识符中), parameter, setting

**Function**:
请求驱动、可缩零的托管执行单元；单代码入口（一个 handler 进程形态）。走受管模式（Database 先例：FunctionSpec 为第四种 Spec，engine 直接投影 Workload，不产生 App 行）；平台语义 = URL/伸缩/唤醒/观测，exposure 双态 = private（缺省，仅网络成员资格可达）| public（合成受管 Route）；调用语义（鉴权/载荷/幂等）归函数自己（ADR-0077）。第三方产品同名实体（如 torchwood Functions）按第三方本义豁免引用。
_Avoid_: lambda

**Task**:
程序化工作负载，双形态：one-shot（一次性执行）与 resident（常驻实例池）；有属主 Token、TTL 与专属网络组。部署期一次性工作负载（Spec 的 preDeployJobs/JobSpec，原名 firstBootJobs——ADR-0078 改名）是 Task 的部署期特例——词条归 Task，不另立 Deployment Job 实体（ADR-0007 词汇冻结裁决，2026-10-01）。
_Avoid_: job, run, agent, one-off

**Task Mint（铸造）**:
Task 行的受控铸造点：身份（TaskRef/属主）注入、TaskSpec 冻结与行装配的唯一所在；受理位与各域铸造调用方只供组装好的 TaskSpec。铸造产行，落账仍走各自写路径（ADR-0062）。
_Avoid_: provisioning, spawn(泛指)

**Owner Lease**:
resident Run 的属主心跳租约；失联超宽限即排空回收。
_Avoid_: heartbeat, keepalive, renewal(泛指)

**Task Network Group**:
Task 专属网络组，Run 于创建时刻挂靠；App Process 可显式跨挂。
_Avoid_: sandbox net, task net(标识符中)

**Run**:
Task 或 Schedule 的一次执行，产出结果、日志与停止原因。
_Avoid_: execution, attempt, instance

**Schedule**:
周期触发规则，按时生成 Run。
_Avoid_: cron(作实体名), timer

**Database**:
由模板渲染的托管有状态服务（Postgres、MySQL、Redis、MongoDB…）。
_Avoid_: DB instance, service instance, addon

### 交付

**Template**:
一键部署蓝图：变量声明 + compose 受控子集 + 平台扩展键；实例化（Instantiate）产出 App 部署与伴生 Secret/Database/Route。平台全局目录文档（内嵌 + 刷新快照），非资源行。与 Database 模板（引擎钉版知识，dbtemplate）、Spec（规范化 IR）分立（ADR-0050）。
_Avoid_: blueprint, recipe

**Compose Intake**:
App 部署声明的受理语言：compose-spec 官方语法 + `x-fleetly-*` 扩展键（`deploy.x-fleetly-strategy`、`healthcheck.x-fleetly-http-path`/`x-fleetly-tcp-port`、顶层 `x-fleetly-pre-deploy-jobs`，原名 `x-fleetly-first-boot-jobs`——ADR-0078 改名），compose-go 官方解析与 schema 校验（非 x- 自定义键拒）；引用 secrets/volumes 走官方 external 声明（名即平台实体名）；官方插值环境 = 平台变量（Shared ∪ AppVariable，未消解即拒）。归一化产物 = Spec（ADR-0074）。
_Avoid_: Procfile

**Source**:
App 的材料来源，三种：Git 引用、镜像引用、上传产物。Spec 所有权分裂（ADR-0072）：材料半边（source、build）归部署来源所有，运行时半边（processes 的形状字段）归声明方所有——配置了 Git hook 的 App 材料归 git：push 合并（新材料 + 冻结运行时），Deploy(spec_file) 是运行时 overlay（冻结材料 + 提交运行时），材料源切换需 override 或先删 hook。
_Avoid_: repo

**GitHub App**:
私有 Git 仓库的一等接入材料（ADR-0073）：GitHub App 安装三元组（App ID / Installation ID / PEM 私钥）与 API 基址（支持 GHES）。私钥 age 信封只写不读，读面只回指纹；受理即经 GitHub API 验证并自动解析安装属主。双作用域：Project 级或平台级（Global），构建期按仓库 host+属主解析，Project 级遮蔽 Global；命中即现铸短时效 installation token 供 clone，材料永不进 Revision 冻结体与任何读面。
_Avoid_: credential(GitHub 侧)

**Build**:
从 Source 产出镜像的过程记录，输出不可变 digest。
_Avoid_: compile, pipeline, ci

**Builder**:
构建 Capability 的 Provider：dockerfile、railpack、static…。
_Avoid_: buildpack(泛指)

**Build Log**:
构建执行日志帧流：live 经最近缓冲、持久经 Logging 承载；出口单点脱敏（Secret 值→指纹短形态，P11/ADR-0040）。
_Avoid_: build output

**Revision**:
冻结且不可变的规范化 Spec 快照，回滚与审计的单位。
_Avoid_: version, snapshot

**Deployment**:
从旧 Revision 到新 Revision 的受监督迁移。
_Avoid_: apply, release, rollout, deploy(名词单用)

**Deployment Strategy**:
Process 级部署切换策略：rolling（默认，健康门内逐代替换）| blue-green（双 Generation 并存窗，先验后切；ADR-0048）。Database/受管域不适用。
_Avoid_: canary, slot

**Admission**:
创建型请求的入队判定：去重、latest-wins 合并、supersede 抢占、queue 满反馈。受理响应附注（P10）：outcome=queued|merged|superseded|deduplicated + position（per-App 串行位次，1=队头）+ existing_deployment（deduplicated 时的既有引用）；幂等层命中（ADR-0024）重放原始响应、不标 deduplicated——两机制分立。
_Avoid_: throttle(另指限流), gate

**Acceptance**:
受理位——创建型/删除型写请求的受理判定面：父资源存活、配额、删除守卫、归属校验；api 层可枚举 module。与 Admission 分立：Acceptance 答"收不收"，Admission 答"怎么排"（ADR-0024）。
_Avoid_: gate, middleware(泛称), validation layer

**Change Freeze**:
变更冻结窗——按资源所属 Team 封禁变更型动词的治理刹车：全局或 per-Team、带原因拒绝、停止族豁免（ADR-0017）。
_Avoid_: maintenance window, block, lock

### 运行时与中间表示

**Spec**:
规范化的期望状态中间表示，带 schemaVersion；四种：AppSpec、TaskSpec、DatabaseSpec、FunctionSpec。AppSpec 的 Process 级携带 `restart_seq`（声明式重启序数——单调性是调用方约定、平台保证最终态为最后执行的声明，经部署管线滚动生效，ADR-0083）。
_Avoid_: manifest, config(泛指), template

**Workload**:
Runtime 接受的最小执行单元，由 Spec 投影而来；平台不感知其载体形态（容器、pod、task）。可观测面：Workload 视图按 Revision 只读重放（与部署投影单源）+ 载体级在服观测（ADR-0071）；编排器原生对象不透出。
_Avoid_: service, container, pod, unit

**Exec Session**:
进入运行中 Workload 载体的会话；双形态：tty 交互（Console 终端页、`fleetly shell`）与 one-shot 命令（`fleetly exec`）。会话绑定受理时的实例与节点，非资源行（不可列表回读，台账 = 审计）（ADR-0049）。
_Avoid_: ssh, tunnel, remote shell

**Browse Session**:
进入 Database 数据面的托管浏览器按需会话（pgweb/redis-commander/Adminer/Mongoku 四件按引擎方言选择）；受理铸造、TTL 回收、非资源行（ADR-0051）。
_Avoid_: db console, data explorer, admin panel

**Launcher Ticket**:
Browse Session 的一次性进入票据（短 TTL、单用途、绑会话）；兑换铸 host-only cookie 会话凭证，经 Proxy ForwardAuth 持续校验（ADR-0051）。
_Avoid_: launch token, magic link, access url

**Relay**:
节点中继（node relay）到控制面的出站长连通道（WebSocket over 公共 gateway），exec 会话帧经其多路复用送达；节点零入站端口（ADR-0049；节点侧端点定名见其附录 B，2026-10-08）。
_Avoid_: tunnel, mesh, agent net, relay agent

**Runtime**:
编排器 Capability，Provider 实现（swarm、k8s、nomad…）。
_Avoid_: substrate, engine, orchestrator, docker(指平台概念时)

**Cluster**:
由一个 Runtime 管理的 Node 集合。
_Avoid_: fleet, pool

**Node**:
集群内一台机器，平台视角下是观测对象。
_Avoid_: host, server, worker

**Enrollment**:
节点加入材料，由 Runtime Provider 生成与轮换。
_Avoid_: join token, bootstrap agent

**Placement**:
调度意图：节点选择约束与卷钉住。
_Avoid_: constraint, affinity

**Volume**:
持久存储附件；无显式 Placement 时默认钉住节点。
_Avoid_: disk, mount(指 Volume 本体)

**Network**:
Project 级互通附件；成员为 Project 进程、Task 网络组挂靠或显式跨 Project 引用；可声明 egress:none。
_Avoid_: overlay, subnet, vpc

**Secret**:
Project 级敏感值实体，加密存储，值永不回显只回指纹；注入 App/Task/Database。
_Avoid_: credential(泛称), vault entry, password store

**Config**:
版本化的非敏感挂载文件实体，可回读。
_Avoid_: file, mount, profile

### 能力与路由

**Capability**:
可插拔子系统的端口：Runtime、Builder、Registry、Proxy、Logging、Metrics、ObjectStore。
_Avoid_: plugin(标识符中), module, subsystem

**Provider**:
Capability 的具体实现（swarm、traefik、victorialogs…）。每个 Capability 同期恰有一个在册 Provider。
_Avoid_: driver, backend, engine, adapter(对外文案中)

**Proxy**:
流量接入 Capability：Route 发布与证书管理（ADR-0047 由 Edge 更名；即反向代理层，traefik 为其在册 Provider）。
_Avoid_: edge, ingress, gateway, load balancer

**Route**:
`host/path → Process 端口` 的映射，附协议（http/h2c/tcp）与 TLS 模式。
_Avoid_: domain, vhost, endpoint, route rule

**Certificate**:
TLS 证书资产，ACME 托管或上传。
_Avoid_: cert(标识符中), SSL

**Registry**:
OCI 镜像仓库 Capability，拉取来源与推送目标（zot 为其在册 Provider）。
_Avoid_: mirror, hub

**ObjectStore**:
S3 兼容对象存储 Capability，承载 Backup 与产物。
_Avoid_: storage, bucket, S3(泛指)

**Logging**:
日志 Capability：控制面集中采集（runtime 容器日志）与 build 日志承载；查询双径（Runtime 实时 / 持久化检索），保留窗可配（ADR-0040；VictoriaLogs 为其在册 Provider）。
_Avoid_: observability(泛指单一系统), log pipeline

**Metrics**:
指标 Capability：cadvisor 全局采集（每节点端点）+ 控制面集中抓取入库；PromQL 查询面；阈值告警评估在引擎原生完成（ADR-0041；VictoriaMetrics 为其在册 Provider）。
_Avoid_: monitoring(泛指), telemetry(另指事件面)

**Alert Rule**:
per-App 阈值规则（cpu_percent | memory_working_set_bytes；持续窗后迁移 firing，回落归位；状态迁移沿才通知，ADR-0041）。
_Avoid_: alarm, monitor(动词泛指)

**Notification Channel**:
通知通道实体（webhook | telegram；配置 age 信封只写不读——URL/bot_token 是凭证材料，ADR-0041）。
_Avoid_: notifier, sink

**Managed Provider**:
由平台以普通 Workload 形式托管部署的 Provider 实例。
_Avoid_: addon, internal service（component 经 ADR-0059 复裁为在册页面名词 Components，移出本表）

### 写类与作者

**Write Class**:
写面立法三桶：READ（只读无副作用）| DECLARATION（声明写）| ACTION（动作写）；proto 全部 RPC 逐个落桶、未分类即红——完整分类表见 ADR-0081 附录，执法面为 Go 侧表守卫（一方 proto 注解推迟到配置面立项）。
**Action**（动作写）:
无终态可持有的过渡/仪式/修护动词（重启、触发、轮换、铸造短命凭证等）：效果是发生的过渡事实，不留下被持有的期望态；效果需进收敛面时经材料指纹接棒（动作改事实 + 收敛面接棒，ADR-0081）。
**Declaration**（声明写）:
经部署管线或实体行落期望态的写类。按**有无运行时面**分两子类：有运行时面 = 引擎负责把运行时收敛到该状态，未变声明重放 = 零扰动 no-op；状态即事实 = 该域无运行时基底可收敛（identity/governance/alerting），行内容即事实，重放语义是行级 upsert 幂等、**不承诺 no-op**（写入不可复现内容如 bcrypt 随机盐者每次必变）。与 Scope 词条的 `resource:action` 同词异轴——授权 action 见 Scope 词条（授权轴），本词是写类轴（Write Class 的 DECLARATION 桶）。判据与域级收敛面表见 ADR-0081 §1/§1.1。
**Author Class**:
期望态的作者权属学说，三类：Document（部署事务的输入文档，冻结成 Revision）、Entity（持续在线的平台实体行）、System（控制环与平台决策——只写经策略委托的授权槽位、漂移对照合成后期望态、声明重放不清洗系统态）；学说与冲突语义表见 ADR-0081 附录。

### 状态语义

**Generation**:
某次已下发 Spec 的单调编号，幂等与 Drift 判定的锚。载体代戳与内容恒同移（ADR-0064）：未变载体跨部署保留其下发代戳。
_Avoid_: epoch, revision number

**Drift**:
实际状态偏离已下发 Spec 的信号；检测默认开启。口径（ADR-0022）：Generation 偏离与 spec 失配（人工改载体）双路径；启动按 succeeded 基线重放重建对照锚。
_Avoid_: skew, divergence, dirty

**内容锚定等价（Content-Addressed Equivalence）**:
Ensure 的等价判定口径：modulo 载体代戳（及副本数）比较内容——跨代未变载体零触碰、仅副本异走 scale（零任务替换）、内容异才滚动；EnsureReport 逐载体回传 wire-gen 供观测归因（ADR-0064）。
_Avoid_: in-place update, 增量部署

**Converge**:
把 Drift 拉回 Spec 的动作；缺省开启（posture 缺省 all，收敛姿态见 Posture 词条，ADR-0082——App 域与环域同律，翻案 ADR-0005 旧「收敛 opt-in」条款）。
_Avoid_: sync, heal, repair

**Posture**:
收敛姿态旗标：`all`（缺省——检出漂移即收敛）| `detect`（**零运行时写**：只报事件，不收敛、不物化、不隔离；启动面只重建期望账本——`ensuredSpec` + 归属，不写 gen 锚）。配置键切换，裁决与语义见 ADR-0082 §5。**成文降级**（detect 期间）：gen 偏离与稳态 stopped 在进程重启后失明（gen 锚只能由 Ensure 回执提供）；撤销 peer 的隔离不执行，被拦时落 Warn，恢复 `all` 后下一拍剥离。

**Replay**:
重新应用某条旧 Revision；回滚的唯一实现。系统侧的基线重放（启动重建、慢通道兜底、非成功终态回卷）是同一原语的非操作者触发形态，触发者是收敛而非回滚动词。
_Avoid_: restore, revert

**Restore**:
从 Backup 重建数据；恢复期平台只读。
_Avoid_: recovery(泛指), replay

**Backup**:
数据面备份（Database、Volume）；产物经 ObjectStore 承载（ADR-0039）。
_Avoid_: dump, snapshot(混用)

**Platform Backup**:
控制面导出：数据库、密封密钥、Capability 配置（restic 承载，ADR-0039）。
_Avoid_: state backup, full backup

**Bootstrap Token**:
首启生成的初始管理员 Token，可吊销。
_Avoid_: initial password, admin key

**Event**:
状态迁移的既成事实，单调 seq，Agent 与 Console 的订阅面。事件行携资源轴 team_id（P0-2）：订阅/列表按 本队 OR 平台级（''）过滤；与 Audit 的双轴分野见 Audit 词条（ADR-0075）。
_Avoid_: notification, webhook(通知通道另指)

**Audit**:
一切写操作的留痕（取证面）：操作者轴 Team（ADR-0035 决策 6，ctx 铸入）、行严格只增、不随保留窗裁剪。与 Event 分立：Event 面向订阅与重同步（资源轴 Team——资源所属 Project 的 Team，跨队 owner 写入亦归资源方）且保留窗可裁；Audit 面向取证与追责，永不裁剪。写路径 kernel 双拍同事务，事件/审计配对由 kernel 扇出保证（ADR-0066 A 化路线，ADR-0075）。
_Avoid_: log(泛指), trail, history(泛指), activity feed
