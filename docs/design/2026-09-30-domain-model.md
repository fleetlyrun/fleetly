# fleetly 领域模型（2026-09-30）

本文是 fleetly 从零重设计的领域基准。词汇以根目录 `CONTEXT.md` 为唯一真源，本文只承载实体关系、状态机与场景语义，不重复词条定义。旧项目（`D:/Codes/qiulin/fleetly-archived`）的教训与用户裁决以"继承"名义吸收，出处标注归档 ADR 编号（旧 ADR-N）。**2026-09-30 约束面重置（ADR-0010）后，文中一切"继承"条款降级为参考默认，待竞品深调以 DX 透镜逐项重估。**

## 1. 定位与不变量

**产品定位**：轻量 PaaS。用户声明意图（源码/镜像 + 期望状态），平台负责构建、调度、接入、观测与数据服务。

**四条不变量**：

1. **运行时中立**：领域模型、Spec、API 中不出现任何编排器概念（service/task/pod/label 公式/节点约束语法）。编排器概念只存在于对应 Provider 内部（继承自旧项目的头号失误反推，见新 ADR-0001））。
2. **人类与 Agent 同为一等**：Console / API / CLI 三面同等能力，同源 proto，无私有服务端面（继承旧 P0-3 教训）；对 Agent 的支持走 CLI + 专属 Skills，**不建 MCP 面**（2026-09-30 用户直裁：MCP 体验不佳，见 ADR-0006）。
3. **DX 优先 + 轻量定位**：开发者体验是一切取舍的第一权重（ADR-0010）；轻量是产品定位的组成部分。"多容器重栈红线"与 idle 预算数字降级为参考默认，重估中。
4. **状态语义保守**（参考默认，ADR-0010 后待重估）：单写者、快照重放式回滚、漂移检测默认开收敛默认关、恢复期只读、孤儿只登记不删（旧 ADR-0002/0003/0004/0014，见新 ADR-0005）。

## 2. 上下文映射

八个限界上下文，关系只经端口与事件，禁止跨上下文 import 内部实现：

| 上下文 | 职责 | 核心实体 |
|---|---|---|
| **Identity & Access** | 身份、凭证、授权 | User, Team, Role, Token, Bootstrap Token |
| **Structure** | 组织结构：项目/应用骨架与材料 | Project, App, Process, Variable, Secret, Config |
| **Delivery** | 源接入、构建、Revision 冻结 | Source, Build, Revision, Deployment |
| **Runtime** | 期望状态投影与集群观测 | Spec, Workload, Generation, Cluster, Node, Enrollment, Placement |
| **Edge & TLS** | 流量接入与证书 | Route, Certificate |
| **Data Services** | 托管数据服务与备份 | Database, Backup, Restore |
| **Automation** | 程序化工作负载面 | Task(one-shot/resident), Run, Schedule, Owner Lease |
| **Telemetry** | 日志与指标消费 | Logging 查询面, Metrics 查询面, Event 流 |

上下文间关系：

- Delivery → Runtime：Deployment 在 releasing 阶段把 Spec 投影为 Workload 交 Runtime Provider。
- Delivery → Edge：发布成功后发布 Route；失败回滚不触碰 Route。
- Automation → Runtime：Run 同样投影为 Workload（短生命周期 + TTL）。
- Data Services → Runtime：Database 模板渲染成 Spec 后走同一条 Runtime 通道（不建第二条翻译线——旧项目第二份翻译已实证漂移）。
- Telemetry ← 各上下文：一切状态迁移写 Event（Outbox）；日志/指标查询经 Capability 端口转发。
- Identity & Access 被所有 API 面消费：Scope = `resource:action`，write 蕴含 read。

## 3. 实体与关系

```
Team 1─* User（经 Role）
Team 1─* Project
Project 1─* App（环境即项目：staging = 独立 Project，如 `shop` / `shop-staging`）
App 1─* Process（web/worker/…）
App *─1 Source（git 引用 | 镜像引用 | 上传产物）
App 1─* Build ──> 产出 digest
App 1─* Revision（不可变 Spec 快照，R1..Rn）
Deployment：R(n-1) → Rn 的受监督迁移（同 App 串行）
Project 1─* Route ──> 指向某 App.Process + 端口
Project 1─* Database 1─* Backup
Project 1─* Network（Project 级互通；跨 Project 显式声明；环境即项目由此天然隔离）
Project 1─* SharedVariable；App 1─* Variable（两级变量，Project 层在下、App 层覆盖）
App/Process *─* Volume（Volume 默认钉住节点）
Project 1─* Task 1─* Run；Project 1─* Schedule 1─* Run；Task 有属主 Token、TTL 与 Task Network Group；resident Task 维持并发 Run 池
Project 1─* Secret（加密存储，值不回显）；Project 1─* Config（版本化挂载文件）
Cluster 1─* Node（观测缓存，非权威）
Capability 1─1 Provider（swarm/traefik/victorialogs…，同期唯一在册）
```

关键身份规则：

- 平台 ID（ULID 或等价）是一切归属与 Drift 判定的锚；Provider 贴在 Workload 载体上的标记只是 ID 的搬运，归属判定永远查平台权威表，不解析载体命名（旧 W2-S3 漂移死腿的根因修复）。
- 载体命名公式（如 swarm 上的 `fleetly-<team>-<prj>-<app>-<proc>`）是 Provider 私有实现细节，可随 Provider 更换而改变。
- Revision 全局不可变；Deployment 只引用两个 Revision，不内嵌状态。
- Spec/Revision 目标无关：变量按名引用，部署时对目标 Project 的变量绑定解析——跨项目 promote 与未来任何环境形态都不需要改 Spec 结构（ADR-0011 对冲）。

## 4. 核心状态机

单写者 goroutine，四件一拍（状态 CAS + tombstone + Outbox 事件 + 审计）同事务落库（继承旧 ADR-0002）。

### Deployment

```
preparing → building → releasing → observing → succeeded
     └────────任意阶段失败────────┘ → failed → (自动) rolling-back → observing → succeeded|failed
```

（N0 实况修正 2026-10-01：rolling-back 不直落终态——Replay 也要过 L1 健康门与 L3 观察窗，路径是 rolling-back → observing → succeeded|failed。）

- preparing：Spec 归一化、Placement 解析、变量/Secret 装配、前置 Job。
- building：可跳过（镜像引用 Source）。
- releasing：投影 Workload 下发 Runtime；健康门 L1（就绪探针）。
- observing：L3 观察窗（默认 60s）+ L2 看门狗常驻（稳态亦常驻观测，ADR-0022）。
- 失败回滚 = **Replay** 上一成功 Revision，永不使用编排器原生回滚（旧 ADR-0003）；Replay 必须重建缺失对象而非跳过（旧 spike B2）。
- 同 App 的 Deployment 串行；新 Deployment 默认拒绝排队外的并发（显式 supersede 标记才允许抢占）。被抢占的旧 Deployment 终态为 `superseded`，观察窗与 Route 发布权立即移交新 Deployment，在途 Generation 由新 Deployment 收口。

### Task 与 Run（程序化工作负载，ADR-0012）

Task 双形态：`one-shot`（创建即一次执行）与 `resident`（维持期望并发数的常驻实例池——torchwood dispatcher 形态）。resident Task 的实例退出后按重启策略补足，直到属主停止或 Task 撤销。

```
Run: pending → running → stopping → stopped | failed
```

- **stopping = 排空**（SIGTERM + 宽限）：TTL 到期、属主主动停止、租约失联都先进 stopping 再落终态。
- **停止原因**（终态必带）：`completed`（自然退出）/ `failed` / `stopped_by_user` / `ttl_expired` / `lease_expired` / `owner_revoked` / `platform_drained`。
- **Owner Lease**：resident Run 靠属主 Token 心跳续期保温（RenewTask）；失联超宽限 → stopping(lease_expired) 并补足新实例（Task 仍 active 时）。TTL 与租约落库为绝对 deadline（ADR-0018），上限 86400s。
- **平台契约**：per-Task 稳定 DNS（池级轮询全部活 Run）+ per-Run 稳定 DNS；镜像直部署与 secretRefs 注入为一等能力。
- Run 经创建时刻挂靠加入 Task Network Group；per-run 运行时动态挂网维持禁令（旧 09-14 事故直裁）。看门狗对超 TTL/失租约的平台自建残留直接收口；用户手工载体仍是孤儿只登记、永不自动删。

### Database

七态收敛（provisioning → running → …→ failed/maintenance），写点单入口 + kick 事件驱动，模板渲染成 DatabaseSpec 后复用 Runtime 通道。备份/恢复/升级/迁移走等序原则（旧 ADR-0014：恢复期禁自动收敛，只读观察）。

### Managed Provider（受管能力实例）

`desired(镜像+参数) → ensuring → ready | degraded`，由**唯一一份**通用 reconciler 驱动（新 ADR-0004），Provider 只声明自己的部署形态（一个 Spec），不再各写一套部署器。

### Build

`queued → building → succeeded | failed | cancelled | expired(超时看门狗)`，输出 digest；Railpack 等外部构建器必须钉版本（旧 spike：plan 漂移）。

## 5. 场景压测（语义基准）

实现与测试必须对这些场景给出一致答案：

1. **发布中途被杀**（旧 spike V4）：进程在 releasing 中途被 SIGKILL，重启后按 Generation 幂等重下发；in-flight 请求必须优雅退出，否则 502 真实发生。
2. **回滚遇到对象缺失**：Replay R(n-1) 时若载体已被人工删除，必须重建而非报成功（旧 spike B2）。
3. **换 Runtime**：同一 App 在 swarm → k3s 迁移，App/Revision/Route/ID 全部保持；载体命名、探针实现、Enrollment 方式全部更换。无状态 Workload 语义全保持；有状态 Workload（Volume/Database）经 Backup/Restore + 显式数据处置迁移——placement 绑定不跨 Runtime 复用，节点 ID 永不复用。这是检验运行时中立的验收场景。
4. **Provider 降级**：Logging Provider 宕机 → 部署照常、日志查询报"能力不可用"；Edge Provider 宕机 → 存量路由继续服务，Route 变更失败且明示。降级矩阵见架构文档 §9。
5. **节点失联 + Volume**：节点 DOWN（旧实测 ~13.5s 检出）期间钉住卷的工作负载不迁移、不重建（旧 spike C3a：无钉住跨节点 = 数据丢失）；node rm 后卡 PENDING 的工作负载登记为孤儿，人工裁决（旧 C4b）。
6. **Agent 重试幂等**：同一 Idempotency-Key + 同请求体 → 返回同一 Deployment；同 Key 不同体 → 409。
7. **漂移与人工干预**：人工改了载体配置 → Drift 事件可见，默认不自动 Converge；挂起（suspend）永不被静默撤销（旧 ADR-0004）。
8. **Platform Restore**：恢复期只读观察：拒绝新 Deployment/Converge；看门狗收口、TTL 到期、Drift 事件只登记、不改变决策状态（防恢复窗内 Drift 事件洪水，继承旧 ADR-0014 只读观察语义）；基线确认后解除。启动顺序 = 控制面 → Platform Backup → Managed Provider → 用户 Workload（旧结论：备份启动序）。
9. **并发部署 admission**（ADR-0016 修订）：同 App 部署请求入队判定——同幂等键/同 commit 去重返回既有；默认 latest-wins 合并；显式 supersede 抢占；queue 满显式反馈；排队与在途可取消。409 仅保留给幂等键冲突与互斥资源锁。
10. **常驻实例池伸缩**：Task 池扩容 = 新建 Run，缩容 = TTL 提前 + 排空；不动 App 状态机。
11. **租约失联**（ADR-0012）：dispatcher 进程死亡 → 心跳停止 → 宽限后 Run 进 stopping(lease_expired)，池自动补足；属主 Token 被吊销 → 名下 Task 默认宽限排空（ADR-0017），处置进审计。
12. **跨项目投递**（ADR-0013）：messageloop（Project A）声明接收 torchwood（Project B）的 Task Network Group → 双向批准后挂靠生效；撤销声明即时隔离。
13. **私有镜像多节点分发**（ADR-0014）：App 引用 GHCR 私有镜像 → Ensure 携带解析凭证按节点分发 → 各节点拉取成功（旧 DT-2 回归用例；凭证不落 label/明文 env）。
14. **升级不扰动**（ADR-0015）：fleetlyd 升级（含 SQLite 迁移与 Managed Provider 逐个 reconcile）期间，用户 Workload 零重启、路由零中断。

## 6. 互通与材料模型

### Network 互通模型（ADR-0013）

- **默认**：Project 内 overlay 全通；跨 Project 默认隔离。
- **成员三类**：①Project 内 Process（默认）；②Task Run 在**创建时刻**挂靠所属 Task Network Group（per-run 运行时动态挂网维持禁令）；③显式跨 Project 引用——双向声明、接收方批准，撤销即时隔离。
- **App Process 跨挂**：`processes[].networks[]` 可引用 `taskGroup:<name>`（torchwood dispatcher↔runner 通道）：每网一次性摊销挂靠、失败 fail-closed 重声明（继承旧 DT-5）。
- **egress 语义**：网络可声明 `egress: none`（不可信代码隔离）。诚实边界：swarm Provider v1 = 弱隔离（独立 overlay + 不发布端口 + 不注入跨网 DNS，出网不阻断，明示）；k8s Provider = NetworkPolicy 原生强隔离。能力差异经能力发现端点暴露。

### Secret / Config 与材料分发（ADR-0014）

- **Secret**：Project 级敏感值实体；age 信封加密（KEK 在数据根、可轮换）；值永不回显、只回指纹；读写全审计；注入面覆盖 App Process、Task、Database（`credentialsRef` 落 Secret）。
- **Config**：版本化明文挂载文件，可回读、有配额（继承旧 OT-3）。
- **镜像凭证分发**：Registry 凭证存 Secret；Runtime Ensure 携带平台已解析的凭证与注入材料，Provider 按节点分发（swarm `--with-registry-auth` 等价）；凭证不落载体 label 或明文 env（旧 DT-2 真机 404 教训）。
- **密封闭环**：Platform Backup 含密封密钥；恢复走解封流程，KEK 单独保管提示入 runbook。

## 7. 明确不做（继承的 no-list）

- CI 流水线（归 Git 托管方，旧 ADR-0012）；push 收包面（同上，webhook + 拉源唯一轨）。
- 函数运行时 / serverless；Task 就是"程序化工作负载面"，不是 FaaS。
- VIP / keepalived / 健康驱动 VIP 转移（旧 ADR-0005）：控制面诚实单点、数据面多 A + 连接级重试、有状态走 Backup/Restore；三 manager 管理面 HA 明确延后。
- Swarm 上不虚构 NetworkPolicy 级强隔离：`egress: none` 在 swarm 为弱隔离（§6 诚实边界）；项目资源配额不做，v1 只做 Task/Workload 数量配额与 per-Token 速率限制（ADR-0017）。
- 自建 DNS、服务网格、多租户计费。

## 8. 从归档项目继承的裁决对照

（ADR-0010 起：本表全部降级为参考默认，逐项待重估；推翻时以新 ADR 记录。）

| 归档裁决 | 新设计落点 |
|---|---|
| ADR-0002 三层状态模型 / 单写点四件一拍 | 新 ADR-0005 原样继承 |
| ADR-0003 回滚 = Revision Replay | 状态机 §4 原样继承 |
| ADR-0004 漂移检测开 / 收敛 opt-in | 场景 7 原样继承 |
| ADR-0005 HA 三层口径 | 架构 §9 原样继承 |
| ADR-0008 易用性 = 轻量元裁决 | 不变量 3；新 ADR-0008 |
| ADR-0010 Team ⊥ Project 正交 | 实体关系继承；"新增 Environment 为 Project 内分区"已被 ADR-0011 否决（环境即项目） |
| ADR-0012 webhook + 拉源唯一轨 | 新 ADR-0009 原样继承 |
| ADR-0013 容器形态三适配 / 控制面地址物化 | 架构 §7 部署形态 |
| ADR-0014 备份等序 / 恢复只读 / 孤儿不删 | 状态机与场景 5/8 |
| ADR-0015 Compose 受控子集 | Source 归一化输入面（架构 §5） |
| ADR-0016/0017/0018 不抽象三连 + deletion test | 新 ADR-0003 的裁尺 |
| torchwood 直裁：docker 直连部署形态并存；per-task attach 明禁 | Automation 上下文语义（attach 禁令维持；创建时刻挂靠合法，ADR-0012/0013） |
| DT-2 镜像拉取凭证随 spec 下发（真机 404 教训） | ADR-0014 材料分发；场景 13 回归用例 |
| DT-5 任务网挂靠（App 进程每网一次性摊销挂靠、fail-closed） | §6 Network 模型（ADR-0013） |
| OT-2 路由协议维度（http/h2c） | Route 协议字段（审查修复恢复） |
| OT-3 Config 版本化资源（可回读/配额） | §6 Secret/Config 模型 |
| D-MN-8 平台节点 ID 先于 placement 存在、永不复用 | 架构 §5 节点锚定契约义务 |
| DT-9 dbtemplate 目录化 + 镜像 digest 钉定（percona/pgvector） | N2 前置设计，批次表已纳入 |

## 9. 可搬运资产（语义或代码）

从归档仓可整体搬运/改写的资产：errcode/eventcode 注册表三链咬合模式（注册表 + golden + usage 反扫）、Compose 受控子集归一化器、ACME/lego 双 challenge 装配、e2e dind 套件骨架与 nightly 拓扑、golden CLI 测试模式、守卫测试模式（枚举红线 + 白名单双向保鲜）。**不搬运**：substrate/dockerapi 及六个组件部署器（被新 ADR-0001/0004 取代）、engine 共享内核结构、state 门面结构。
