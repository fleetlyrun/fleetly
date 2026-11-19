# fleetly Functions API Phase 2 设计（唤醒与缩零 · 伸缩决策环 · 构建源）

> 状态：**v2——双独立子代理交叉验证已过（2026-11-19，§6）；主线被双独立一致纠错两处（wake 标记形态/就绪门构型）+ 咬出 Phase 1 两缺陷（per-function 互斥未落码/发布指纹缺 replicas），已并入正文。**
> 前提：Phase 1（ADR-0102 九任务）已全量落地并 e2e 全绿（552c98bb 时点）；设计 v3（§8/§12/§15/§17）已把 Phase 2 拆为第一批（唤醒）与第二批（决策环）+ 同期件。本文是 Phase 2 的形态真源草稿：换轨升级路径（§15.1）在本文 design-it-twice 终裁后随实施 ADR 立法。
>
> 词汇红线在册：唤醒组件禁 gateway/edge/ingress/load balancer 与 relay（ADR-0077 批）；候选名 **activator**（Knative 同构，§8）。

## 0. 程序与批次

| 批 | 件 | 依赖 |
|---|---|---|
| **P2-1 唤醒** | 唤醒组件（activator）+ min=0 + 排队/快速失败语义 + 退出条件公式 | 无（可独立交付——CF Containers「有缩零唤醒、无 autoscaler」先例 §17） |
| **P2-2 决策环** | 通用伸缩决策环（App/Function 共享容量平面；swarm 环写副本 vs k3s 原生分叉在此裁） | P2-1 的观测信号（唤醒组件上报值就是伸缩信号）+ Phase 1 dogfood 负载数据 |
| **P2-3 构建源** | upload+Build 源与 Revision 就绪门（Pending/Active/Failed）+ 换轨升级路径终裁 | 与 P2-1 同批 design-it-twice（本文 §5）；实施可后置 |
| **P2-4 同期件** | git pull-only 源 · per-invocation 观测轴（public 先行）· revision 级预览 URL（private 优先）· `functions.base_domain` 派生 host | 各自独立，搭车批 |

P2-1 先行（价值最大：缩零是产品承诺的核心轴）；P2-2 其后（需要 P2-1 的信号与 dogfood 数据）；P2-3/P2-4 穿插。

## 1. P2-1 唤醒（activator + min=0）

### 1.1 方向终裁：乙案（fn-<id> 别名缩零期指唤醒组件）

v3 §8 留了双候选。调研（§17 要点 4）已给乙案三件套实证，本节终裁**乙案**，理由：

1. **甲案（唤醒只走公网受管面）把缩零按 exposure 分层**：private 函数被迫 min≥1——「private 不可缩零」是产品面减员，且与「容量与接入两平面正交」的三平面分立律冲突（缩零是容量平面语义，不该被接入平面约束）。
2. **乙案改的是解析点不是调用方行为**（Knative activator 同构）：调用方永远拨 `fn-<id>`，缩零期该名字解析到唤醒组件，唤醒完成后解析回函数载体——private 唤醒结构上可行，且零客户端配合。
3. 甲案的「Route 缩零期指唤醒组件」作为乙案的**公网半边自然包含**（public 合成路由的后端解析同样切换）——不是二选一，是乙案覆盖甲案。

### 1.2 唤醒组件形态

**activator 是受管平台组件**（Managed Workload，traefik/cadvisor 同族——不驻用户 Project 网，驻平台系统网）：

- **唯一职责**：缩零期的请求扣留 + 载体唤醒 + 观测上报。不做鉴权、不做载荷处理、不做协议理解（数据面仍是纯 HTTP 透传——「调用语义归函数自己」不变）。
- **触发面**：swarm 服务发现——activator 以**每项目网一个attachment**挂全部活跃项目网（traefik 同款挂网面），`fn-<id>` 的 DNS 别名在函数缩零期由 swarm DNS 层指向 activator 的服务任务（Knative 的 selectorless Service Endpoints 切换在 swarm 的等价物 = 别名归属切换，见 §1.5）。
- **唤醒路径**（Knative 三件套之一③——扣请求有 deadline）：
  1. 请求到达 activator（`fn-<id>` 解析命中）；
  2. activator 经引擎窄接口发出**唤醒拍**（KickFunctions + 唤醒意图登记——不是自己 Ensure 载体，Ensure 仍是收敛环唯一写动词）；
  3. activator 扣住请求（**默认 deadline 10s**，Knative/Cloud Run 实证区间），轮询函数观测至 `running` 且 `fn-<id>` 解析回函数载体；
  4. 就绪即**原样转发**（保留 method/path/body/header——activator 不解析内容，只透传字节）；超时未就绪 → **503 + Retry-After**（三段式语义见 §1.4）。
- **幂等重入**（前置四问②；v2 修正——双独立一致否决独立标记列）：唤醒拍 = **条件容量行 CAS**：`UPDATE functions SET replicas=<rung> WHERE id=? AND replicas=0`（repo 新方法 RequestWake；rung = max(1, min_replicas)）。**容量列即唤醒标记**——独立布尔列是同一迁移的第二真源（谁清/何时清/崩溃残留全是要新立法的边），而 `WHERE replicas=0` 同时给出幂等（N 个并发请求恰一行落账，其余零行 no-op）与防踩踏（用户并发 scale 到 3 后 CAS 不命中，不会把 3 打回 1）。成功后 `KickFunctions()`——收敛环下一拍读到 replicas=1 → 投影指纹含副本 → scale-only Ensure（ADR-0064 零任务替换）。**清位责任 = 决策环 idle 重park**（§2），唤醒原因归事件载荷（`function.scaled` 携 cause=wake，零状态类事件文化不受扰）。activator **永不触 Runtime.Ensure**（Ensure 仍只由收敛环调用——单写者纪律原样成立；且 activator 持 Ensure 权即持 Cipher 解密面，结构性拒绝）。

### 1.3 min=0 语义

- 值域开放：`min_replicas ∈ [0, 16]`；`min=0` = 缩零使能（空闲判定后 replicas 归 0，`fn-<id>` 切指 activator）。
- **缩零判定**（前置四问③——观测信号源）：平台侧信号 = 函数 Workload 观测的持续零活跃（swarm: 无 in-flight 连接可观测——降级为「replicas 全 idle N 分钟」由决策环判定；k3s: 可用 pod 网络计数）。v1 判据：**收敛环观测 `running` 且无 wake 标记持续 `idle_timeout`（默认 300s，行面可调）→ 写 replicas=0**。此判定住决策环（P2-2）；P2-1 只承接「已经是 0 的请求面」。
- **唤醒后不自动回缩**：唤醒 Ensure 副本 1 后，缩零再判定走同一 idle 环（防抖：唤醒后 `idle_timeout` 重新计时——Knative scale-down-delay 同款）。
- 看门狗期望面：`replicas=0` 不登记期望（Phase 1 已立法——「故意缩零不是事故」）。

### 1.4 排队/快速失败语义（三段式，AWS Web Functions 模板）

| 状态 | 语义 | HTTP 面 |
|---|---|---|
| **唤醒中**（wake 标记在、载体未 ready） | 扣留等待（deadline 10s 内） | 请求挂起，就绪即透传（调用方视角只是首请求慢——冷启动） |
| **超限**（扣留数达 per-function 上限，默认 64） | 快速失败不扣留 | **429** + Retry-After（防唤醒风暴放大） |
| **超时**（deadline 到、载体仍未 ready） | 快速失败 | **503** + Retry-After（诚实：平台正在唤醒，稍后重试） |

- 三段式住在 activator 的请求路径上；E_ 前缀 errcode 不新增（数据面非 gRPC——HTTP 状态码即契约）。
- CLI `functions invoke` 对 429/503 的文案提示冷启动语义（Phase 1 已有 honest refusal 面的延伸）。

### 1.5 DNS/路由切换机制（per-runtime）

- **swarm**：`fn-<id>` 别名是 swarm service alias（Phase 1 已在载体上）。缩零 = 载体 service scale-to-0 但**保留 alias 空壳？**——swarm service 0 副本即无 DNS 解析。方案：**缩零期不拆 service、只 scale 0**，同时 activator 的 per-project attachment 上由平台写入 `fn-<id>` 虚拟别名（swarm 网络别名支持多 service 共享 alias——两个 service 同 alias 时 DNS 轮询返回两者地址；缩零期函数 service 0 任务 = 只剩 activator 地址，**免切换**）。唤醒后函数 service 有任务 → DNS 自动含函数地址 → activator 见到函数地址即停止扣留新请求（透传中的旧请求排完）。**退出条件**（三件套之二）：activator 每拍检查 `fn-<id>` 解析集含函数任务地址 → 本函数退出唤醒态。别名双挂是免改 DNS 的关键——比 Knative 的 Endpoints 编程更廉价，swarm 原生支持。
- **k3s**：同构用 Service selector 切换（Knative selectorless 原样）：`fn-<id>` Service 的 selector 在缩零期指向 activator pods，唤醒后指回函数 pods（k3s 原生 endpoint 切换，无 DNS TTL 问题）。
- **public 半边**：合成路由后端解析（Phase 1 functionCapabilityRoutes）在 replicas=0 时解析到 activator 的项目网地址（resolveFunctionBackend 的兜底分支——期望集为空即缩零态）。

### 1.6 组件装配（growth-law；v2 修正两处）

- **组件形态**（独立 A 裁决）：activator 是共享受管平台组件（双副本无状态，挂全部活跃项目网——traefik 同款 `activeProjectNetworks` 挂靠面），**不是 per-project 实例**（成本倒置）也**不收编 traefik**（traefik 受管面是 JSON 动态配置，plugin 需外网+实验旗标破 e2e 离线矩阵；ForwardAuth-hold 路扣连接的仍是 traefik 且合成路由 0 副本时整条消失；**决定性理由：traefik 根本不在 private 数据路径上**——fn-<id> 是项目网 L3 直拨，private 缩零唤醒结构上需要一个网内应答点，这个组件反正要建）。**落点 = functions 应用模块自持第二条 LoopSpec**（contract.Module 多环 + ringregistry 登记——activator Workload 投影环：投影输入 = 缩零函数行集 + 活跃项目网集，System 作者权），不新增 capability Kind（词条七端口闭集）。
- **窄接口**：`WakeFunction(id)`（引擎门面 → repo RequestWake CAS + Kick）、`FunctionServing(id)`（退出判定查询：Addresses 端点 + 观测 wire-gen 匹配 + healthy，§1.8）、activator stats 端点（in-flight/唤醒计数——决策环 §2 的信号源）。鉴权 = 专用服务 Token（窄 scope + 审计 actor 真实）。
- **Change Freeze 豁免**（独立 A 裁决）：wake 的容量 CAS 是 System 作者的平台内部写，**不被 Team 级冻结拦截**（冻结拦用户变更动词；冻结期 parked 函数唤醒 503 = 治理刹车误伤成可用性事故）——ADR 显式登记豁免与理由。
- **Phase 1 缺陷二（本批必修，独立 A 咬出）**：`functionsPublicFingerprint` 的 fpRow **不含 replicas**——park/wake 不改指纹会短路吞掉合成路由合并集变更（§6 mandatory① 漏并的同型漏洞）；fpRow 加 replicas 位 + park/wake 落账后 public 函数 `PublishRoutesNow()`。

### 1.6a Phase 1 欠账前置批（独立 B 咬出，P2-1 开工前必修）

设计 §4.3 立法的 **per-function 互斥未落码**：`reconcile` 与 `TeardownFunction` 均只持 maintenanceMu **读**锁（functions.go:140 / teardown.go:27）——互不串行。竞态：环 mid-reconcile Ensure 重建刚被 Teardown 拆除的载体 → tombstone 落账后该载体成永久孤儿。唤醒把 Ensure 频率放大一个量级，此缺口从理论变现实。补 `lockFunction`（sync.Map 惰性建，lockTask/lockApp 同款先例 ensure.go:99-118）。

### 1.7 容量平面的诚实性

- min=0 且无请求时成本 = 0 载体（产品承诺兑现）；activator 本身是常驻受管组件（单实例多租共享，成本摊薄——非 per-function）。
- `replicas=0` 的行事实与观测（v2 裁决——独立 B 提出「status 停留 running 说谎」，显示层映射解之）：**缓存层维持四值不动**（pending|running|degraded|stopped——§4.1 观测缓存法），**响应层加 parked 显示映射**：replicas=0 → GetFunction/ListFunctions/WaitFunction 的 status 字段渲染为 `parked`（行事实优先于过期观测缓存——映射住 functionMsg 单源，CLI/Console 免改）。缩零时缓存里残留的 running 被映射遮蔽，唤醒后 replicas>0 即恢复观测真值。

## 2. P2-2 通用伸缩决策环

- **平面立法**（ADR-0102 收编条款兑现）：「容量 = 行面旋钮」是 App/Function 共享平面。决策环读行（replicas/min/max）+ 观测 → 写行（CAS）→ Kick。App 侧接入 = 后续批（先 Function 单消费者验证环形状）。
- **环形态**：engine 新环 "autoscale"（或并入 function 环的分段——design 交叉验证定），单拍：列 active 函数 → 读观测（CPU/内存/请求数——public 函数有 traefik 面，private 无请求观测只有载体资源） → 目标副本计算（v1 规则：请求数/副本 > 阈值 → +1；持续低于 → -1（不低于 min）；缩零判定独立走 idle_timeout）→ 行 CAS + Kick。
- **swarm/k3s 分叉**：swarm 无原生 autoscale → 平台环写 replicas（本文形态）；k3s 可选 HPA 原生——**v1 两 Runtime 都走平台环**（单源纪律；k3s HPA 的原生接入作为后续优化批，避免两套定律）。此裁随实施 ADR。
- **防抖**：扩容立即、缩容延迟（scale-down-delay 默认 300s 同 idle_timeout；Knative max-scale-down-rate=2 同款限速：单拍最多减半）。
- **守卫**（§15.5 兑现）：引擎自发写统一 defer 语义——本拍放弃扩容 + 事件明示，绝不静默超限（配额 CAS 与用户动词同定律）。

## 3. P2-3 构建源与就绪门（换轨决策点 design-it-twice）

- **Revision 就绪门**（v2 重写——独立 B 设计取代主线「A 子集」倾向）：**revision 保持不可变冻结体（不加状态列）**；就绪态的载体是 **Build 行**（Builds 面既有状态机 queued→building→succeeded|failed 全量复用，不平行造第二份）。机制三件：
  1. **意图锚**：`functions` 行加 `pending_revision_id`（DeployFunction(upload 源) 事务 = 冻结 revision + 铸 queued Build + 落 pending，不 SwapCurrent；幂等重放返回同一 pending 态）。pending 指向行**并入 ADR-0101 保留窗 protect 集**（漏并 = pending 指 GC 悬空——决策 1 已预埋此扩面）。
  2. **环内兑现**（无回调面）：function 环加推进步——`pending != ""` 且其 Build 终态：succeeded → 一事务 SwapCurrent + 清 pending + `revision_changed(cause=deploy)`；failed → pending 保留 + E_FUNCTION_NOT_READY 呈现（设计预留码在此兑现）。用户连发 v2/v3 → latest-wins（pending 被覆写——意图显式锚定）。**daemon 重启后 queued/building Build 行由推进步重拾输入登记**（复刻 App 侧 D-4 幂等重建——独立 B 点名的悬挂风险）。
  3. **健康门控换轨 = pending 清位时机的延展点**（§15.1 终裁）：第一刀清位 = build succeeded（渐进容量损失 + Rollback 承载，§4.1 既有立场）；真机需要健康门时，把清位推迟到「观测 running@新 wire-gen」、失败自动指回（Rollback 复用）——**function 环内的换轨监督窗，不引 deployment 行、不动状态机构型**。双期望集窗（ADR-0048）对 Function 形态**结构性不成立**（双代窗靠代次化载体名，Function Workload ID = 行 id 跨 Revision 恒定——ADR-0048 自己明文受管域不适用）；泛化 Deployment 状态机维持不立（Function 只缺一个「等 build」的门，撑不起整台泛化）。
- 改动面：Build 行 owner 泛化（AppID 外加 FunctionID/owner kind，迁移 + repo 查询面）；「铸 Build + inputs 登记」从 DriveBuilding 提为 intake 导出入口，functions 经 contract.Intake 端口调用（PinVolumes 先例——模块间禁 import）。
- git pull-only 源：Phase 3 前置，本批只留 FunctionSpec.source oneof 位（已留）。

## 4. P2-4 同期件

- **per-invocation 观测轴（public 先行）**：traefik 访问日志/指标按 host 分账 → per-function 请求数/延迟/冷启计数（activator 上报唤醒次数）。private 无观测点（结构性，Phase 1 已诚实标注）——Phase 3 binding 式寻址再议。
- **revision 级预览 URL**：`<rev-seq>.<fn-<id>>.internal`（private 形态，项目网内）——保留窗内 revision 的只读冒烟面；投影 = 临时 Workload 或流量标注（v1 简单形：不起载体，Console 的 revision diff 面承载内容对照——URL 面挂 Phase 3 需求触发）。**主线砍掉 v1 预览 URL 载体面**（诚实：价值密度低、成本高），保留 diff 面已有能力。
- **`functions.base_domain`**：config 新增（console/api host 同族）；public 函数 host 缺省 = `<fn-name>-<project>.<base_domain>`（sslip 缺省之外的第二缺省层——配置在场时优先）。

## 5. 开放问题（交叉验证输入）

1. activator 扣留转发 vs 307 重定向：透传字节 vs 让客户端重连函数地址？（主线：透传——重定向泄漏内部地址且多一跳 RTT；代价=activator 承载带宽——多租共享下单点风险，交叉验证质疑此点）
2. swarm 别名双挂的 DNS 轮询：函数唤醒后 DNS 同时含 activator+函数地址，连接级负载均衡会把部分新请求仍送 activator——activator 需对「函数已 ready」的请求做纯转发（已是设计），但**长期双挂**意味着 activator 永远在数据路径上（带宽单点）——是否唤醒后显式摘别名（一次 service update）？主线：先双挂（零切换成本），带宽实证后议。
3. wake 标记的存储：行列（持久、重启安全）vs 内存（快、丢标记=请求面仍扣留可重发）——主线：**行内 wake_pending 布尔**（重启安全；写入频率=唤醒次数，低频可承受）。
4. 决策环与收敛环的写竞争：两环都写 replicas 行——单写者纪律：决策环写、收敛环只读行面（Phase 1 已立法「收敛环盲目」）；唤醒 Ensure 的 max(row, wake) 在收敛环内——三方交互需交叉验证。
5. activator 与冻结 Revision 的交互：唤醒不需要读 spec（Ensure 由收敛环做）——activator 只碰行与观测，零 spec 依赖（确认此边界干净）。

## 6. 交叉验证记录（2026-11-19）

**流程**：主线 v1 完成后派两份独立设计（A=唤醒组件与数据面侧重；B=环与不变量侧重），同源输入 = CONTEXT/ADR-0064·0068·0077·0082·0088·0101·0102/设计 v3 允读节/engine contract 与 functions 模块/providers translate 与挂网/routepublish/e2e 脚本。**披露**：两份独立均误读了设计 v3 的 §8/§12（超读）——锚定暴露在案；两份的机制推导均锚在代码真源（文件:行号级），降权后采用。裁决已并入正文 v2。

**收敛面（三方一致）**：乙案方向（fn-<id> 解析点切换，甲被乙包含）；activator 共享受管平台组件双副本（否 per-project/否 traefik 收编——traefik 不在 private 路径是决定性理由）；hold-and-forward（否 307——同 host 重定向结构性死循环）；三段式 429/503 + Retry-After；行写 + Kick 唤醒路径（activator 永不触 Runtime.Ensure——单写者纪律 + Cipher 解密面不扩散）；控制面真值退出判定（否 DNS 探测）；k3s 不走 HPA（统一平台决策环）；决策环独立于 function 执行环；容量列即唤醒标记（**双独立一致否决主线 wake_pending 布尔列**）；upload 门 = Build 谓词 + pending 锚 + 环内兑现（**双独立一致否决主线「泛化 A 子集」倾向与 revision 状态列**）。

**分歧裁决表（主线被纠正处显式标注）**：

| # | 议题 | 主线 v1 | 独立 A | 独立 B | v2 裁决 |
|---|---|---|---|---|---|
| 1 | wake 标记形态 | 行内 wake_pending 布尔 | 条件容量 CAS（replicas 即标记） | **同 A**（双独立一致纠主线） | **从 A/B**：`WHERE replicas=0` CAS；清位归决策环 idle 重 park；原因归事件载荷 |
| 2 | upload 就绪门构型 | A 子集（revision 生命周期谓词） | — | Build 终态谓词 + pending_revision_id 意图锚 + 环内兑现 | **从 B**：revision 不加状态列；Builds 面复用；双期望集窗结构性不成立（ID 恒定） |
| 3 | activator 落点 | 新应用模块 activator | functions 模块第二条 LoopSpec（Workload 投影环） | — | **从 A**：不新增 capability Kind；投影环在 functions 模块内 |
| 4 | 决策环落点 | 未定（新环 or function 环分段） | — | 新应用模块 capacity + 新环（决策与执行分离） | **从 B**：独立模块（App 收编后同环服务两聚合）；CAS 让位仲裁（用户赢） |
| 5 | park 与缩容 | 两个判定（idle_timeout 独立） | — | 一个规则族（clamp 公式，idle 是下界输出；park 前置门加扣留=0） | **从 B**：统一公式 + 两参数段（down 窗/park 窗） |
| 6 | status 缩零态 | 显示层 parked 映射 | — | 行事实直落 stopped（缓存例外） | **维持主线**（映射遮蔽过期缓存，四值法不动）；B 的「说谎」担忧由映射消解 |
| 7 | 信号通道 | 未定 | — | HTTP pull per 拍（否 websocket——Knative 规模前提不成立） | **从 B**：pull + 信号缺席=保守维持；观测呈现轴与决策输入轴分家 |
| 8 | 退出机制 | 先双挂后议 | 显式翻转 + 迟滞 + X-Fleetly-Wake 头 e2e 断言（**结构性拒绝永久双挂**——Fly 教训） | EBC 公式三条件 | **A+B 合并**：控制面真值判定 + 显式翻转 + 迟滞两拍 + 自证头缺席断言 |
| 9 | capability 扩面 | 未识别 | NetworkRef 跨域别名缺口 + Address.parked 位（translate.go:334-341 实证缺口） | — | **从 A**：两处 only-add 扩展为公共前置，随 ADR 立法（指纹 omitempty 纪律） |
| 10 | 冻结交互 | 未识别 | wake 写冻结豁免（Team 刹车不误伤可用性） | — | **从 A**：ADR 显式登记 System 作者豁免 |

**独特捕获吸收**：A——swarm DNS 行为实证列为唤醒 ADR 验收锚第一项（translate.go:222 挂账：0 副本 service 别名形态/跨 service 同别名 RR）；413 体帽 + per-activator 持有帽；`functionsPublicFingerprint` 缺 replicas（**Phase 1 真缺陷**，本批必修）；唤醒面 errcode 两枚（429/503 语义非 gRPC，HTTP 状态即契约——码位随 ADR 定）；带宽单点量化（冷窗流量 × 双帽钳死）。B——**Phase 1 真缺陷：per-function 互斥未落码**（§1.6a 前置批）；min=0 七不变量清单（I-1~I-6 全数吸收进 §1.3/§1.5）；配额 defer 第四模态 + `function.scale_deferred` 事件；决策环 CAS 让位矩阵表驱动契约测试；private park 误杀防线（长连接保守窗/行面 no-park 旋钮/e2e 断言）；daemon 重启 Build 输入重拾（D-4 复刻）。

**残余风险共识**：乙案别名交接窗（双主/空窗）是 swarm 腿最大开放面——独立 B 建议第一批可 public-only 先行（路由面指唤醒组件零别名编排），乙案 private 随独立批 + 专属 e2e 腿；主线接受为**实施批内的排序选项**（P2-1a public 唤醒 → P2-1b private 别名交接，DNS 实证结论决定 P2-1b 形态）。activator 软单点（常驻函数不受影响故告警面弱）——双副本 + 健康行 + 看门狗期望 + e2e「activator 停机：常驻 200/缩零诚实 503」断言。
