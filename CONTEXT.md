# fleetly

fleetly 是一个轻量 PaaS：把源码或镜像变成运行在可插拔运行时上的工作负载，并提供流量接入、托管数据服务与观测。人类与 AI Agent 同为一等用户。

本文件只承载词汇，不承载架构。结构与决策见 `docs/design/` 与 `docs/adr/`。

## Language

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
App 级变量；Project 级共享变量称 Shared Variable，注入时 Project 层在下、App 层覆盖。
_Avoid_: env var(标识符中), parameter, setting

**Task**:
程序化工作负载，双形态：one-shot（一次性执行）与 resident（常驻实例池）；有属主 Token、TTL 与专属网络组。部署期一次性工作负载（Spec 的 firstBootJobs/JobSpec）是 Task 的部署期特例——词条归 Task，不另立 Deployment Job 实体（ADR-0007 词汇冻结裁决，2026-10-01）。
_Avoid_: job, run, function, agent, one-off

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

**Source**:
App 的来源，三种：Git 引用、镜像引用、上传产物。
_Avoid_: repo

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
规范化的期望状态中间表示，带 schemaVersion；三种：AppSpec、TaskSpec、DatabaseSpec。
_Avoid_: manifest, config(泛指), template

**Workload**:
Runtime 接受的最小执行单元，由 Spec 投影而来；平台不感知其载体形态（容器、pod、task）。
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
OCI 镜像仓库 Capability，拉取来源与推送目标。
_Avoid_: mirror, hub

**ObjectStore**:
S3 兼容对象存储 Capability，承载 Backup 与产物。
_Avoid_: storage, bucket, S3(泛指)

**Logging**:
日志 Capability：控制面集中采集（runtime 容器日志）与 build 日志承载；查询双径（Runtime 实时 / 持久化检索），保留窗可配（ADR-0040）。
_Avoid_: observability(泛指单一系统), log pipeline

**Metrics**:
指标 Capability：cadvisor 全局采集（每节点端点）+ 控制面集中抓取入库；PromQL 查询面；阈值告警评估在引擎原生完成（ADR-0041）。
_Avoid_: monitoring(泛指), telemetry(另指事件面)

**Alert Rule**:
per-App 阈值规则（cpu_percent | memory_working_set_bytes；持续窗后迁移 firing，回落归位；状态迁移沿才通知，ADR-0041）。
_Avoid_: alarm, monitor(动词泛指)

**Notification Channel**:
通知通道实体（webhook | telegram；配置 age 信封只写不读——URL/bot_token 是凭证材料，ADR-0041）。
_Avoid_: notifier, sink

**Managed Provider**:
由平台以普通 Workload 形式托管部署的 Provider 实例。
_Avoid_: component, addon, internal service

### 状态语义

**Generation**:
某次已下发 Spec 的单调编号，幂等与 Drift 判定的锚。
_Avoid_: epoch, revision number

**Drift**:
实际状态偏离已下发 Spec 的信号；检测默认开启。口径（ADR-0022）：Generation 偏离与 spec 失配（人工改载体）双路径；启动按 succeeded 基线重放重建对照锚。
_Avoid_: skew, divergence, dirty

**Converge**:
把 Drift 拉回 Spec 的动作；默认 opt-in。
_Avoid_: sync, heal, repair

**Replay**:
重新应用某条旧 Revision；回滚的唯一实现。
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
状态迁移的既成事实，单调 seq，Agent 与 Console 的订阅面。
_Avoid_: notification, webhook(通知通道另指)
