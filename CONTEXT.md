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
`resource:action` 形式的授权单元；write 蕴含 read。
_Avoid_: grant, capability(授权义)

**Token**:
携带 Scope 的凭证，服务人与 Agent。首启由 Bootstrap Token 引导，一切 Token 可吊销。
_Avoid_: API key, PAT, credential

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
一次性程序化工作负载，有属主 Token、TTL 与专属网络组。常驻实例池由 Task 承载。
_Avoid_: job, run, function, agent

**Run**:
Task 或 Schedule 的一次执行，产出结果与日志。
_Avoid_: execution, attempt

**Schedule**:
周期触发规则，按时生成 Run。
_Avoid_: cron(作实体名), timer

**Database**:
由模板渲染的托管有状态服务（Postgres、MySQL、Redis、MongoDB…）。
_Avoid_: DB instance, service instance, addon

### 交付

**Source**:
App 的来源，三种：Git 引用、镜像引用、上传产物。
_Avoid_: repo

**Build**:
从 Source 产出镜像的过程记录，输出不可变 digest。
_Avoid_: compile, pipeline, ci

**Builder**:
构建 Capability 的 Provider：dockerfile、railpack、static…。
_Avoid_: buildpack(泛指)

**Revision**:
冻结且不可变的规范化 Spec 快照，回滚与审计的单位。
_Avoid_: version, snapshot

**Deployment**:
从旧 Revision 到新 Revision 的受监督迁移。
_Avoid_: apply, release, rollout, deploy(名词单用)

### 运行时与中间表示

**Spec**:
规范化的期望状态中间表示，带 schemaVersion；三种：AppSpec、TaskSpec、DatabaseSpec。
_Avoid_: manifest, config(泛指), template

**Workload**:
Runtime 接受的最小执行单元，由 Spec 投影而来；平台不感知其载体形态（容器、pod、task）。
_Avoid_: service, container, pod, unit

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
Project 级互通附件；跨 Project 互通需显式声明。
_Avoid_: overlay, subnet

**Secret / Config**:
Compose 受控子集中的敏感与非敏感附件。

### 能力与路由

**Capability**:
可插拔子系统的端口：Runtime、Builder、Registry、Edge、Logging、Metrics、ObjectStore。
_Avoid_: plugin(标识符中), module, subsystem

**Provider**:
Capability 的具体实现（swarm、traefik、victorialogs…）。每个 Capability 同期恰有一个在册 Provider。
_Avoid_: driver, backend, engine, adapter(对外文案中)

**Edge**:
流量接入 Capability：Route 发布与证书管理。
_Avoid_: ingress, gateway, load balancer

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

**Logging / Metrics**:
日志与指标 Capability：采集、查询、保留、告警。
_Avoid_: observability(泛指单一系统)

**Managed Provider**:
由平台以普通 Workload 形式托管部署的 Provider 实例。
_Avoid_: component, addon, internal service

### 状态语义

**Generation**:
某次已下发 Spec 的单调编号，幂等与 Drift 判定的锚。
_Avoid_: epoch, revision number

**Drift**:
实际状态偏离已下发 Spec 的信号；检测默认开启。
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
数据面备份（Database、Volume）。
_Avoid_: dump, snapshot(混用)

**Platform Backup**:
控制面导出：数据库、密封密钥、Capability 配置。
_Avoid_: state backup, full backup

**Bootstrap Token**:
首启生成的初始管理员 Token，可吊销。
_Avoid_: initial password, admin key

**Event**:
状态迁移的既成事实，单调 seq，Agent 与 Console 的订阅面。
_Avoid_: notification, webhook(通知通道另指)
