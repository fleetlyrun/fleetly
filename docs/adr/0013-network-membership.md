# Network 成员模型：挂靠、跨 Project 引用与 egress 语义

Network 保留 Project 级默认互通；成员分三类：①Project 内 Process（默认全通）；②Task Run 加入所属 Task Network Group——**创建时刻**挂靠（per-run 运行时动态挂网的禁令维持，源自 2026-09-14 队列卡死事故）；③显式跨 Project 引用（双向声明、接收方批准，服务 messageloop ↔ torchwood 投递互通）。App Process 可显式挂靠指定 Task Network Group（torchwood dispatcher↔runner 通道，继承旧 DT-5：每网一次性摊销挂靠、失败 fail-closed 重声明）。

网络可声明 `egress: none`（不可信代码隔离语义）。诚实边界：swarm Provider v1 只实现弱隔离（独立 overlay + 不发布端口 + 不注入跨网 DNS，出网不阻断，文档与 Console 明示）；k8s Provider 用 NetworkPolicy 原生满足。编排器没有的语义不假装有。

## Consequences

- `processes[].networks[]` 引用三种目标：project-local 名 / `taskGroup:<name>` / 跨 Project 引用；跨 Project 挂靠计入双方审计。
- k8s Provider 落地时 egress:none 从弱隔离升级为强隔离，API 语义不变（能力发现端点暴露差异）。

## 附录 A：跨 Project 引用的载体、批准与隔离语义（F1.8 落地，2026-10-02）

正文成员③（显式跨 Project 引用：双向声明、接收方批准、撤销即时隔离）的
执行细化。四项裁决：

### A.1 载体形态：Network peer 声明面（独立表 + NetworksService 加宽）

**NetworkPeer 是独立实体（`network_peers` 表），不是 Network 行上的内嵌
字段**——批准是有生命周期的双向同意（pending → approved → revoked，撤销
后可重新声明进新审批环），嵌入列会把状态机藏进材料行。行键
`(network_id, peer_project_id)` 在**非 revoked 行间唯一**（部分唯一索引）；
`network_id` 是接收方（网络归属方）的网络行，`peer_project_id` 是挂靠方
（声明方）项目。

- **双向声明 = 一行两拍**：挂靠方 `DeclareNetworkPeer`（落 pending 行，
  即挂靠方声明）+ 接收方 `ApproveNetworkPeer`（pending → approved，即
  接收方声明）。两侧同意才成立，缺任一不可投影。
- RPC 全部挂 **NetworksService**（5 个新动词：declare/approve/revoke/
  get/list）；scope 复用 `networks` 资源——SchedulesService 复用 `tasks`
  同款先例（授权面等价于"管理网络"）。List 带 `after_peer_id` + `limit`
  游标（ADR-0026）；Declare 在幂等执法面（Idempotency-Key，ADR-0024）。
- **挂靠计入双方审计**：declare/approve/revoke 每拍落两条审计行——网络
  侧 `network/<name>/peers/<id>` 与挂靠项目侧 `project/<id>/peers/<id>`，
  双方各自可从审计面回答"谁挂在我的网上/我挂在哪里"。
- 事件三枚：`network.peer_declared` / `network.peer_approved` /
  `network.peer_revoked`（aggregate=network，挂接收方网络 ID）。
- v1 批准动作不区分操作者所属项目（authz 是全局 scope 面，ADR-0028
  "读默认开放、写显式授权"）；per-Project 授权随治理批（F1.9+）。

### A.2 引用形态：`project:<project-id>/<network-name>`

`processes[].networks[]` 第三形态，与 `taskGroup:<name>` 同款前缀形态。
**project 段一律用平台 ID**：项目名在 (team_id, name) 唯一口径下跨 Team
可同名（ADR-0028），不可作锚。spec 包是叶子（只校验形态——26 位大写
字母数字的平台 ID 段 + 非空名段；比 Crockford 全集宽一位字母面以容纳
仓内 fixture 惯用形），存在性与批准态由受理/投影面裁决。

### A.3 投影与 fail-closed（DT-5 继承）

投影层把跨 Project 引用翻译为 `Workload.NetworkRefs`（跨域引用形态，
Namespace.Team 从目标 Project 行实取——Team 轴接实的直接联动）；同域
`Networks` 不混入。**两种投影模式**：

- **strict（部署链：Deploy 受理预检、prepare、release、rollback）**：
  引用未 approved（未声明/待批/已撤销/网络不存在）→ 投影错误，部署被
  拒或失败——fail-closed 重声明（DT-5）：用户须先取得批准或从 Spec 移除
  引用。不静默跳过（跳过 = 拓扑静默漂移，用户以为挂上了其实没有）。
- **isolate（隔离收敛：撤销剥离、基线重放、隔离扫描）**：未 approved 的
  引用**剥离**（omit）——投影产物不含该网络附件，Ensure 全量替换语义
  把载体收敛到无附件形态。

swarm Provider 侧无新机制：跨域引用已按引用自身域解析载体名
（B1 形态），ensureNetworks create-or-get 一次性摊销挂靠，失败上抛 =
Ensure 失败 = 部署失败重声明。

### A.4 撤销即时隔离：断存量（service update 剥离附件）

**断存量，不是只禁新建**。"即时隔离"的落地语义：

- 撤销事务落账后，引擎立即对**受影响 App**（挂靠方项目下、当前成功基线
  引用该网络的 App）以 isolate 模式重投影重 Ensure——swarm service
  update 全量替换 spec、移除网络附件即断存量连接（滚动替换到无附件任务）。
  隔离生效时点 = 该次 Ensure 完成，不等待下一次用户部署。
- **在途部署的 App 不抢 Ensure**（驱动器拥有 Ensure 权，基线重放同款
  裁决）：其驱动投影走 strict，引用已撤销 → 部署失败终态（回放目标仍
  引用则回滚也失败）——诚实失败，用户更新 Spec 后重部署。
- 剥离 Ensure 失败（网络错误等）不回滚撤销行（受理面已生效：新部署
  全部被拒）；**自愈兜底 = 漂移扫描拍的隔离检查**（对缓存中带跨域附件
  的 App 复核批准态，失配即 isolate 重收敛）与重启基线重放（isolate
  模式）——隔离是收敛不变式，不是一次性动作。
- 撤销幂等：已 revoked 的行再撤销返回行现状（不重复发事件、不重复剥
  离）。重新挂靠走新声明行（新审批环），旧 revoked 行留作审计事实。
