# fleetly Functions API 设计（一等公民产品线，2026-10-16）

> 状态：**v2——细化轮 + 双独立设计交叉验证已过（2026-10-16，§16）**；余留问题不阻塞 Phase 1 开工，随各自实施/方向 ADR 终裁。Phase 0 已落（ADR-0077 词汇批），Phase 1 可直开。实施各批各自开 ADR 并携带守卫任务（AGENTS.md 纪律）。
>
> 裁决基础（2026-10-16 会话裁定，本文以此为前提，不再重议）：
> 1. **Function 是 fleetly 一等公民实体**（与 App/Database/Task 并列的产品线），独立设计、独立演化，不绑定 torchwood、不依赖 torchwood 代码。
> 2. **从通用 Functions API 出发**：不假设消费者协议、不假设 SDK、不假设触发方；数据面 = 纯 HTTP，调用语义（鉴权/载荷/幂等）归函数自己，平台语义（URL/伸缩/唤醒/观测）归 fleetly。torchwood 只是第一个 dogfood 消费者——它暴露平台短板，不塑造平台形状。
> 3. **exposure 双态、默认 private**：public 才有公网路径；private 仅网络成员资格可达（§6）。
> 4. 本设计**取代** `docs/proposals/2026-10-16-function-hosting.md` 的裁决框架（torchwood 视角原提案）：原 F1 否决、F2 解耦独立收口、F3 收编 Phase 2、F4 变为 Console Functions 页。原提案事实核查结论仍有效。
>
> 词汇依赖已解除：ADR-0077 词汇批已落地（Function 词条在册、Task 让渡 function、Spec 第四种、lambda 入 banned）。

## 1. 定位与动机

fleetly 是轻量 PaaS：源码/镜像 → 可插拔运行时上的工作负载 + 流量接入 + 托管数据 + 观测。现有三类一等执行产品：**App**（用户组装的长运行拓扑）、**Task**（程序化工作负载）、**Database**（托管有状态服务）。Functions 补第四类：**请求驱动的托管执行单元**——写一段代码，平台给出地址、按需伸缩、按调用观测。

动机不是 torchwood 的需求（那是 dogfood 负载，不是设计输入），而是产品面缺口：平台已有「长运行」与「一次性」两极，「请求来了才跑、跑完可缩零」的中间形态今天只能用常驻 App 硬扛（成本）或外部调度打 Task（无 URL、无伸缩语义）。业界同构先例：Cloud Run / Knative Serving（受管缩零服务）、AWS Lambda function URLs、Fly Machines（stop/start 语义）。

## 2. 领域边界（第一设计决策）

### 2.1 三轴钉死实体

| 轴 | Function | App | Task |
|---|---|---|---|
| 激活模型 | 请求驱动；可缩零再按请求唤醒 | 声明拓扑，副本 ≥1 由 Spec 承载 | 创建/调度驱动，TTL 或租约收口 |
| 计量轴 | per-invocation 观测是一等语义（分期落地） | per-runtime（实例×时间） | per-Run |
| 粒度 | 单代码入口（一个 handler 进程形态） | 多 Process 声明拓扑 | 镜像直投单形 |

用户判据：Function vs App = 「要不要常驻」；Function vs Task = 「要不要 URL 与请求语义」。**deletion test**：三轴组合是 App/Task 面没有也无法便宜补上的，撤案不成立；每期复盘重跑（per-invocation 观测轴 Phase 2 兑现前在文档诚实标注）。

### 2.2 走 Database 模式，不走 App 模式（地基裁决）

Function 是**特化受管聚合**：engine 直接投影 Workload——对齐 Database 先例（DatabaseSpec → engine 投影 + DNS 铸名 + 受管收敛环），不是 App 的用户组装拓扑。理由：①不产生影子 App 行；②机制沿既有收敛环/Ensure/配额扩轴；③「受管语义单元 vs 用户组装拓扑是两种聚合」Database 已裁（ADR-0029）。与 Template 的关系：未来可有函数模板进目录（InstantiateTemplate 产 Function 是 only-add 扩展点，Phase 3）——Template 生态不反向定义 Function。

## 3. 词汇与治理

ADR-0077 已落地（2026-10-16 同批守卫绿）：Task 词条 Avoid 删 function；Function 词条入册（定义 + _Avoid_: lambda + torchwood Functions 双名共存条款）；Spec 词条四种；wording 分诊同步。Phase 2 唤醒组件命名随彼 ADR 入册（词汇红线在册：禁 gateway/edge/ingress/load balancer 与 relay；候选 activator）。

## 4. 实体与状态模型

### 4.1 表与状态机（交叉验证裁决：两表、无 Deployment 实体）

- **functions 表**（聚合根）：`id = <name>-<seq>`（entityid.Join，ADR-0070 名基可读 id）、seq（游标轴）、project_id、name（dns 标签，活跃期唯一，同名重建新 seq 新 id 永不复用）、**state**（`active|deleted` 幂等 tombstone，仅此一条生命周期边）、**status**（观测缓存：`pending|running|degraded|stopped`，收敛环按 wire-gen 匹配推进——Database 同款两层分立，零状态类事件）、`current_revision_id`（在服 Revision，原子换轨）、`generation`（行持久单调号，重启安全）、`spec_fingerprint`（Ensure 输入指纹，**含副本数**）、`replicas / min_replicas / max_replicas`（容量三列，行面）、`exposure / public_host`（接入两列，行面）、created/updated/deleted_at。唯一索引 `(project_id, name)` 限活跃行。
- **function_revisions 表**（冻结体）：id、函数内 seq、digest（内容寻址去重）、冻结 FunctionSpec（protojson，与 minttask 同口径钉死选项）、created_at。**回滚 = Replay**：current_revision_id 原子指回旧冻结行（复用行、不新增——内容寻址断言）。
- **不立 function_deployments（交叉验证最大反转，理由）**：受监督迁移状态机整台是 App 键控（admission/L1 门/蓝绿窗），Function 复用须先泛化键控、自建则是第二份状态机（反拷贝纪律）。换轨诚实性由三层承载：ADR-0064 内容锚定（内容异才滚动，滚坏的是副本不是状态机）+ 观测 status（degraded 可见）+ 显式 RollbackFunction（秒级换轨再收敛）。坏 Revision 爆炸半径 = 单函数副本渐进损失（与 App「切流前全量就绪」不同量级）。**Phase 2 决策点**：若真机需要健康门控零中断换轨，在「泛化 Deployment 状态机」vs「function 环双期望集窗（ADR-0048 最小移植）」间 design-it-twice，不预发半个面。
- Repo 按 ADR-0075 决策 3：domain 限界上下文持行 + `Repo` 接口，persistence 实现。

### 4.2 FunctionSpec（第四种 Spec，冻结面 = 材料半边 + 形状半边）

```proto
message FunctionSpec {
  int32 schema_version = 1;
  FunctionRef function = 2;                 // { id, project }
  fleetly.spec.v1.Source source = 3;        // v1 值域 image 直投（Phase 2 加 upload/git，oneof 前向兼容）
  BuildSpec build = 4;                      // Phase 2 构建源预留（v1 恒空）
  ProcessSpec handler = 5;                  // 嵌 ProcessSpec（TaskSpec 嵌套先例）：name 归一 "handler"、
                                           // ports 恰一且 protocol∈{http,h2c}；replicas/strategy/placement/
                                           // volumes/networks/firstBootJobs 禁面（叶子 ValidateFunction 执法）
}
```

**不冻结**（行面运行时旋钮，交叉验证裁决「三平面分立」）：`replicas/min/max`（容量平面，Runtime 面）与 `exposure/host`（接入平面，Proxy 面）——理由：①ADR-0064 已把副本数从内容等价 modulo 出去，Revision 不应承载运营旋钮；②缩零产品回滚语义 = 「换代码不复活容量决策」；③ScaleTask 先例（desired_concurrency 行面可变）；④exposure 翻转不应触发重部署。回滚只换 current_revision_id，容量/接入不动。Phase 2 引 git 源时 ADR-0072 所有权分裂逐字适用（hook 拥有材料半边），前向兼容。

### 4.3 命名与删除级联

- 命名单源住 engine：**`FunctionDNSName(id) = "fn-" + id`**（如 `fn-resize-img-7`——db-/task- 同族截短先例；交叉验证纠正了初稿 `fn-<seq>`：ADR-0070 是名基 id，`fn-<seq>` 违背该先例）；Workload ID = **函数行 id**（Database 先例：跨 Revision 稳定，Ensure 走 update 而非重建）；swarm 载体名 `fleetly-fn-<id>`（Provider 私有）。
- **DeleteFunction 收口序（FB-1 教训前置，零孤儿不变量）**：①受理位（scope/freeze/活跃行）→ ②engine `TeardownFunction`：per-function 互斥（与收敛环 Ensure 串行，消灭竞写窗口）+ maintenanceMu 读锁（与网络重建全序串行）→ Runtime.Remove（唯一拆除通道，幂等）→ 清 ensureMemo/归属/看门狗期望 → 若 public 即时重发布路由面 → ③落账一事务（tombstone + `function.deleted` 事件 + 审计）。**不级联/保留**：function_revisions 行（**永不删**，对齐 App 侧 Revision 保留口径——实施批复核 ADR-0023 表述）、Secret/Config/上传产物（Project 级材料各自生命周期）、日志/事件/审计（保留窗纪律）。在途请求诚实切断（函数语义 = 可重试请求面，调用方重试归调用语义半边）。Project 删除 → 项目内函数逐条走同收口序（复用同一函数，禁止第二份拷贝）。

## 5. API 契约面

proto 归 **delivery 上下文**（新文件 `proto/fleetly/delivery/v1/functions.proto`，templates.proto 第二文件先例；一等实体配专属服务，不新开顶层上下文）。**创建即首部署**（无空函数行；Database 创建即收敛先例）：

```
service FunctionsService {                        // scope resource: "functions"
  CreateFunction(project_id, name, image, command?, env?, secret_refs?,
                 port, port_protocol?, healthcheck?, resources?,
                 min_replicas?, max_replicas?, exposure?, host?)   // idem 面 + freeze 封禁
  GetFunction / ListFunctions(project_id 必填, after_function_seq, limit,
                 exposure?/state? 过滤)                            // 读
  DeployFunction(function_id, 全量 source+shape 字段)              // **全量替换**（非 overlay——v1 无
                                                                  // 双物主，map/repeated 的缺席语义零歧义）；
                                                                  // 冻结新 Revision + 原子换轨；idem 面
  ScaleFunction(function_id, replicas)                             // 行 CAS，钳 [min,max]，出界拒（ScaleTask 镜像）
  SetFunctionExposure(function_id, exposure, host?)                // 行写 + 路由面即时重发布，零载体动作
  RollbackFunction(function_id, revision_seq?)                     // 缺省回上一成功 Revision（Replay）
  DeleteFunction(function_id)                                      // freeze 封禁；幂等 tombstone
  ListFunctionRevisions(function_id, after_revision_seq, limit)
  WaitFunction(function_id, expect_revision_seq?)                  // server-streaming：事件订阅为信号、行重读为
                                                                  // 真源（F1.3 形态）；终态帧后收流；CLI --wait
}
```

- **幂等面**：+CreateFunction/+DeployFunction；Scale/SetExposure/Rollback 不进（ScaleTask/StopTask 先例——幂等由行 CAS 承载）。**冻结桶**：create/deploy/scale/exposure/rollback/delete 全 frozen，读与 Wait 豁免（freezeguard 三桶反扫）。
- **受理位（Acceptance）**：父 Project 存活、配额（§10）、名字形状与活跃唯一、**活跃网络在场**（零网项目的 private 函数不可达——fail-closed 拒绝优于静默孤岛，ADR-0029 同款）、ValidateFunction 叶子校验、public host 语法与占用预检（E_CONFLICT 快速反馈；引擎合成期 TOCTOU 窗口冲突 = 事件诚实报）。
- 响应行含 `dns_name`、`private_url`（`http://fn-<id>:<port>`）、public 时 `public_url`；Create/Deploy 响应复用 **Admission outcome**（queued|merged|superseded|deduplicated，P10 形态——per-Function 串行语义）。
- **不设平台中转 InvokeFunction RPC**——控制面不是数据面；`fleetly functions invoke` = CLI 客户端直发 HTTP（public）；CLI 是控制面瘦客户端不进集群网，private 调用提示内网可达形态（不设控制面转发端点，防流量放大器）。

## 6. 访问模型（exposure）

- **private（缺省）**：可达域 = **项目网络成员资格**——函数 Workload 挂全部活跃 Project 网（Database 同款语义），`fn-<id>` 别名只在本项目网络上可解析；跨 Project 走既有 NetworkPeer。调用契约 = `http://fn-<id>:<port>`。
- **public**：**合成受管路由，无 Route 行**（交叉验证裁决，机制 = publishRoutes 合并面 +1：对每个 `active ∧ exposure=public` 行合成 `Route{Host, Port=声明口, Protocol=http|h2c, TLS=auto}`，后端经 Runtime.Addresses 解析——console 合成路由先例 ADR-0061 的发布面复用）。两条 mandatory 细节（browse 事故课）：函数公网集指纹并入发布签名（漏并 = 短路吞掉 exposure 变更）；SetExposure/删除后 `PublishRoutesNow()` 即时发布。**结构性无孤儿**：路由存在性纯派生自函数行（不变量 I-2）——比 Route 行 + 级联钩子（FB-1 风险面）强。能力天花板诚实记录：无逐路由行 = 无逐路由日志页/证书绑定/Console Route 页集成（需求实锤时走 routes 表 owner-kind 泛化的显式 ADR）。
- SetFunctionExposure 只写行 + 事件 + Kick 发布面：**不推 Generation、不动载体**——接入/容量/内容三平面经三个动词独立演化。
- **诚实边界**：①private ≠ 集群内认证（swarm v1 弱隔离：同网可直调、无 mTLS；k3s netpol 成员隔离是强执法——两 Runtime **语义**一致（有无公网路径）、**强度**不同，e2e 双腿断言）；②缩零态后端不可达 = 既有诚实降级（backend unresolved, skipping route）；③**private × scale-to-zero 是 Phase 2 显式设计件**（网内直拨无拦截点；方向候选见 §8）。

## 7. 部署链与运行时投影

- **v1 源 = image 直投**（交叉验证裁决）：平台通用分母（Task 即 image-only）、dogfood 自产镜像、零新机制。**upload+Build 延后 Phase 2 首项**——构建源需要「Revision 就绪门」（pending → Build 终态 → 换轨）迷你状态机，与 §4.1 Phase 2 换轨决策点是同一族问题，同批设计；git 更后（pull-only 固定 ref 冻结形态倾向）。私有仓凭证经 Secret `registry:<host>` 既有通道。
- **投影公式（`ProjectFunction`，住 projection.go，与三既有投影同族）**：FunctionSpec(冻结) + 行事实(replicas) + 项目事实(team/networks/egress) → Workload{ID=行 id, Process="handler", Image=解析后引用, Command/Env/Resources 直映, Ports=[{声明口, http|h2c}], Healthcheck=缺省 **tcp 探声明口**（不假设函数配合 /healthz——调用语义归函数）, Replicas=行值, Restart=Always, Networks=全部活跃项目网, Addressing=[fn-<id>], StopGrace 缺省 30s（在途请求排空）}。**平台事实 env 注入**（only-add）：`FLEETLY_FUNCTION_ID / _NAME / _REVISION(digest 前 12) / _PORT`（FLEETLY_DB_* 同族先例）。secret_refs 文件注入 /run/secrets/<名>（ADR-0014——**不是 env**，镜像侧自理文件→env 自举）。
- **NamespaceRef 增 `.Function` 轴**（Task/Database 轴两次先例；核心 6 方法签名零改动，swarm/k3s 各补归属 label 与载体命名分支）。
- **收敛环（functionLoop，databaseLoop 同族新环，复用全仓唯一 Loop 骨架）**单拍：per-function 互斥 → 读行 + 冻结 spec → 项目事实解析（读失败跳拍绝不空集代偿）→ 投影 + 材料解密 → ensureFresh 签名短路（WorkloadSetSignature 全集 + ns + 材料指纹）→ maintenanceMu 读锁 + boundedStep → EnsureGeneration（指纹含副本数）→ Runtime.Ensure → 归属登记 + 看门狗期望登记（**replicas>0 才进期望面**——故意缩零不是事故，否则假告警）→ ensureRemember + status 推进（wire-gen 匹配才采信）。失败清签名下拍重试；重启 = 行 + 持久 gen 幂等重放。
- **Ensure/Drift/Converge 不变量**：Ensure 是唯一载体写动词（用户动词只写行 + Kick）；gen 恒同移（纯伸缩走 scale-only 保 wire-gen、零任务替换）；Drift 的 Generation 路径接入（按 functionOwner 归属过滤）、**spec 对照 P1 对 `function/` 前缀跳过**（`database/` 先例——稳态看门狗面保留）；Converge opt-in 不变；观测两轨（Watch/期望缓存 + Revision 只读重放）。

## 8. 伸缩语义

- **v1（常驻函数）**：`min_replicas` 缺省 **1**（0 服务端拒——0 的语义位即 Phase 2 scale-to-zero，届时开放值域）、`max_replicas` 缺省 1、`replicas` 初值 = clamp(1, min, max)；校验 `0 ≤ min ≤ max ≤ 16`。ScaleFunction 行 CAS 钳制（出界 E_INVALID_ARGUMENT + suggestion），**受理位单点执法、收敛环盲目**（数值定律单源纪律，ADR-0068/F1.9 同款——边界若在环上二次仲裁即两处定律可漂移）。
- **与 ADR-0064 交互（结构性红利）**：副本数进 spec_fingerprint → 每次伸缩推 gen 一次、走三分决策中间支 **scale-only**（在服任务零替换——存量任务 ID 不变，e2e 断言）；内容等价且副本同 = 指纹同 = 跳过零写；回滚只回放差异、未变载体自动跳过。
- **Phase 2（scale-to-zero + 唤醒）**：开放 min=0 + 区间自动伸缩 + 共享受管唤醒组件（Managed Workload 形态；候选名 activator，词汇红线在册）。**方向双候选在案（交叉验证产出，Phase 2 ADR 终裁）**：甲 = 唤醒只走公网受管面（Route 缩零期指唤醒组件；private 函数 min≥1 缺省校验 + 显式逃生门）——诚实但把缩零按 exposure 分层；乙 = 唤醒组件挂项目网、**`fn-<id>` 别名在缩零期指向唤醒组件**（调用方仍拨同名，解析点切换——private 唤醒结构上可行，Knative activator 同构）。开工 ADR 前置四问不变：排队/快速失败语义、与 Ensure 幂等重入的交互、观测信号源实证、组件命名。

## 9. 触发面

- **v1 唯一触发面 = HTTP 入口**（public 路由或 private DNS）。平台不做任何调度。
- **单源规则**：每个 Function 的触发调度源恒恰一个——要么将来的平台 cron 触发，要么消费者自调 URL，二者互斥、文档钉死；执法面 = 反模式文档 + 平台触发不复制外部调度器。Phase 3 触发生态 = 既有组合（Schedule 拍 one-shot Task 调 HTTP），平台不另立触发实体（deletion test 待真机需求）。

## 10. 观测、计量与治理

- **v1 观测 = 全复用**：函数 stdout + 收敛环日志走既有 Logging（`StreamLogs` 增 function_id 过滤轴，only-add）；实例级指标走既有 Metrics（函数 Workload 即普通 Workload）；GetFunction 聚合 status/replicas/URL。per-invocation 观测轴（调用数/延迟/冷启）= Phase 2（唤醒组件请求日志起步）——deletion test 复审义务在册。
- **事件五枚**（eventcode 只增 + Source 锚 + golden + usage 反扫同批）：`function.created / revision_changed（携 from/to/cause: deploy|rollback，Rollback 复用此码）/ scaled / exposure_changed / deleted`。**零状态类事件**（status 是观测缓存非状态机；零事件风暴面）。
- **审计**：create/deploy/scale/exposure/rollback/delete 六动词经受理位 commit 留痕（kernel 双拍配对）。
- **errcode 零新码**：复用 E_INVALID_ARGUMENT/E_NOT_FOUND/E_ALREADY_EXISTS/E_CONFLICT（零网项目）/E_QUOTA_EXCEEDED/E_CHANGE_FROZEN。唤醒面两码（E_FUNCTION_NOT_READY/PARKED 类）随 Phase 2。
- **配额族**（F1.9 对齐，受理位事务内执法 + 引擎自发写点同定律 defer 语义）：`MaxFunctionsPerProject = 50`（活跃行数）+ `MaxFunctionReplicasPerProject = 64`（活跃行 replicas 之和——Workload 数量真源口径，MaxTaskConcurrency 同义）；单一定律函数 `CheckFunctionQuota`（CheckTaskQuota 镜像，单源纪律）。per-Token 速率沿 RateLimiter 既有面自动覆盖。
- **scope** +`functions`（assembly/policy.go）；行级授权沿 project 链（ADR-0035）；冻结拦截器 Team 解析链 +function 节。

## 11. CLI / Console / Agent 面

- CLI 动词组（双形态 golden；旗标前置铁律入组内自查）：

```
fleetly functions create <name> --image REF [--command c1,c2] [--port N] [--env K=V]… [--secret NAME]…
                              [--cpu-millis N] [--memory-mb N] [--min-replicas N] [--max-replicas N]
                              [--exposure public --host H]        # host 缺省 sslip 拼装（templates 先例）
                              [--idempotency-key K] [--wait]
fleetly functions list [--exposure public|private] | get <fn> | url <fn>          # url：private_url/public_url 分行
fleetly functions deploy <fn> --image REF [--wait]                                # 全量替换
fleetly functions scale <fn> <n> | exposure <fn> public --host H | exposure <fn> private
fleetly functions rollback <fn> [--to-revision R] | revisions <fn> | logs <fn> [--follow] | delete <fn>
fleetly functions invoke <fn> --data @file.json                                   # 客户端直发（public）；private 提示内网形态
```

- Console：**Functions 页**（Apps/Databases/Tasks 平级，ADR-0044 最小面起步）——列表（状态/exposure 徽章/replicas/revision/updated）+ 详情（URL CopyButton、窗口与 current、受管路由链接、revision 史）+ 写面 sheet（create/deploy/scale 步进器）；**exposure 翻转确认框披露可达域变化**（本页最有价值的一句安全文案）。调用流/实时面随 Phase 2。
- Agent：`skills/functions-deploy/SKILL.md`（字面命令围栏守卫喂食；public 函数验收即一条 curl）。

## 12. 分期

- **Phase 0（已落）**：ADR-0077 词汇批（2026-10-16 守卫绿）。
- **Phase 1（MVP「常驻函数」）**，任务序：①proto+生成（delivery/functions.proto、scope、幂等/冻结/gateway 挂载）②state 迁移（functions/function_revisions 两表 + 迁移链锚）③engine functions 域（投影/收敛环/Teardown/看门狗期望；复数普通名词 struct 白盒测试，ADR-0075 决策 4）④api 层（受理位/审计/事件五枚/WaitFunction）⑤CLI+双形态 golden ⑥Console Functions 页 ⑦e2e dind-functions 腿 ⑧skills ⑨staging dogfood 迁移。**torchwood dogfood 自本 Phase 第一天可迁入**（其函数 = 自产镜像，image-only 源恰好覆盖）。
- **Phase 2**：唤醒组件 + min=0 + 区间自动伸缩（方向双候选 §8；ADR 前置四问）；upload+Build 源与 Revision 就绪门（与换轨决策点同批 design-it-twice）；git pull-only 源；per-invocation 观测轴；`functions.base_domain` 派生 host。
- **Phase 3**：触发生态、token 鉴权（Proxy ForwardAuth 先例）、invocation 台账、函数模板目录条目、路径路由丰富度。

v1 诚实边界：无唤醒/缩零（min≥1）、无自动扩缩、无 per-invocation 观测、无平台侧调用鉴权/限流/超时（函数自理）、无 upload/git 源、无换轨健康门（坏 Revision = 渐进容量损失可见 + 显式回滚）、private 隔离强度 swarm 软/k3s 硬、合成路由无逐路由行面、数据面无 HA。

## 13. torchwood dogfood 映射（消费者视角，非设计输入）

- torchwood 函数 = 「自带鉴权的普通 HTTP 服务」（X-Tw-Signature/twx_ 在它自己的产品层，坐在 fleetly 不管的调用语义半边）：http 触发函数 public 承载、cron/event 触发函数 private + 同网/Peer 互访——双模式并用验证 per-Function 粒度。
- torchwood 的调度、身份、幂等、记账、FDK 全留 torchwood；要不要收敛到平台触发/token 是它的自由，fleetly 不依赖、不等待。dogfood 价值 = 真实多租户常驻负载暴露平台短板。

## 14. 验收锚（实施完成后逐条核）

- [ ] Phase 0：ADR-0077 批落地 + 守卫双向保鲜绿（2026-10-16 已勾）。
- [ ] e2e 全链腿（dind-functions，swarm 常驻 + k3s 变体腿）：create --wait → running；**private 可达**（同网探针 fn-<id> 200）；**private 零公网路径红线**（发布快照零条目 + 网外 DNS 不可解析 + 对 Proxy 任意 Host 打 404；k3s 加 netpol 断言）；public 合成路由 200 → 翻 private 条目消失即时重发布；scale 1→2 **任务 ID 不变零替换**（scale-only 断言）；换镜像换轨 + rollback 内容复原且 **Revision 行不增**（Replay 复用断言）；删除级联零孤儿（载体零残留 + 快照零条目 + DNS 消亡 + revisions 保留）；项目删除级联；配额超限诚实拒；幂等重放逐字节同响应；**控制面重启任务 ID 不变**（同 gen 重放零滚）。
- [ ] 守卫批：幂等覆盖反扫、冻结三桶、受理面反扫、scope 词表、eventcode 三链、CLI 双形态 golden、投影确定性 fixture、（词法零新增——ADR-0077 已执法）。
- [ ] CLI/Console/skills 三面 + dist 同 commit。
- [ ] dogfood：torchwood ≥1 public + ≥1 private 函数迁入 Function 行全链 200（staging 真机锚）。
- [ ] 每期复盘重跑 §2.1 deletion test（per-invocation 轴兑现义务在册）。

## 15. 开放问题（交叉验证后余留）

1. **Phase 2 换轨升级路径**：upload 构建源的就绪门 + 健康门控换轨是否泛化 Deployment 状态机（App/Function 两消费者撑一次泛化）vs function 环双期望集窗——design-it-twice，优先评估泛化，防 bluegreen 逻辑抄第二份。
2. **Phase 2 唤醒组件**：命名（红线在册，候选 activator）+ 归属（独立 Managed vs 收编 Proxy Provider）+ 方向双候选（§8 甲/乙）。
3. **合成路由的能力天花板触发器**：逐路由日志/证书绑定/Console Route 页需求实锤 → routes 表 owner-kind 泛化显式 ADR。
4. **min/max 行面旋钮与 App「Replay 恢复一切」直觉的域间不一致**：两域行为差异是文档/心智成本——真机反馈后必要时显式 ADR 收敛口径。
5. **autoscale/唤醒的 replicas 写点执法**：受理位定律已单源，引擎自发写统一 defer 语义（本拍放弃扩容 + 事件明示），绝不静默超限——Phase 2 落地时进守卫。

## 16. 交叉验证记录（2026-10-16）

**流程**：主线（本文档细化版）完成后，派两份**禁读主线与原提案**的独立设计（A=API 契约与 DX 侧重；B=引擎与运行时不变量侧重），同源输入 = CONTEXT/ADR-0077 绑定裁决/架构文档/ADR-0024·0025·0026·0029·0044·0048·0064·0072·0075/proto 契约惯例/engine 投影先例。本节记录三方对照与裁决；裁决已并入正文 v2。

**收敛面（三方一致，置信度最高）**：一等实体 + Database 受管模式；创建即首部署；exposure 双态 private 缺省；image 直投为首源；FunctionSpec 嵌 ProcessSpec（恰一 http/h2c 端口 + 禁面叶子校验）；NamespaceRef 增 Function 轴；自有 revision 表 + 内容寻址/冻结纪律复用；scale-only 零替换（ADR-0064 红利）；无 InvokeFunction RPC；e2e 零公网路径红线断言。

**分歧裁决表（主线被纠正处显式标注）**：

| # | 议题 | 主线 v1 | 独立 A | 独立 B | v2 裁决 |
|---|---|---|---|---|---|
| 1 | Deployment 实体 | 自有三态表 | 自有表 + 13 事件全状态机 | **不立**（换轨原子 + 收敛环） | **从 B**：无 Deployment；Deploy = 冻结+换轨，诚实性由 ADR-0064 + status + Rollback 承载；Phase 2 决策点。A 的 Wait 价值保留 → WaitFunction 走事件+行重读（无部署行也可流） |
| 2 | 伸缩/接入字段归属 | 进 Spec 冻结 | 窗口进 Spec、current 在行 | **全在行面**（三平面分立） | **从 B**：revision 只冻材料+形状；Scale/SetExposure 行写零重部署——主线「ScaleFunction = 新 Revision」是自我矛盾的半步，B 的正交分离消解了它 |
| 3 | DNS/ID 公式 | `fn-<seq>` ❌ | `fn-<id>`（id=名基） | `fn-<id>` | **双独立纠主线**：ADR-0070 是名基 id，主线公式违背先例——采纳 `fn-<id>` |
| 4 | v1 源 | image+upload | image+upload+spec_file | **仅 image** | **从 B**：upload 需就绪门迷你状态机（A 的方案印证了代价：状态机加了 building 相位），与换轨决策点同族应同批；dogfood 恰是镜像消费者 |
| 5 | public 路由 | Route 行 + owner 标记 | Route 行（activator 前排） | **合成入发布面，无行** | **从 B**：结构性无孤儿 > 行面集成（天花板诚实记录 §15.3）；主线方案正是 FB-1 风险面 |
| 6 | 唤醒/缩零时点 | Phase 2、min≥1 | **Phase 1 全量**（per-project activator + min=0 缺省） | Phase 2、min≥1 | **维持主线/B**：A 自己把 activator 列为最不确定项并备退路；A 的 per-project activator 设计与「fn-<id> 别名指唤醒组件」的 private 唤醒解法作为 Phase 2 双候选入册（§8 乙案——它反驳了 B 的「private 唤醒结构性不可行」：改的是解析点不是调用方行为） |
| 7 | 事件/errcode/配额 | 4 枚/0 码/50+8 | 13 枚/2 码/100+200 | 5 枚/0 码/32+64 | **从 B 为主**：5 枚/0 码；配额轴取 count+replica-sum（B/A 同轴），缺省 50/64（A 的 16 上限入校验） |
| 8 | Deploy 动词语义 | —（未显式） | Update overlay（自己列为不确定 #2） | 未显式（缺口） | **主线补裁**：全量替换（v1 无双物主，overlay 的 map/repeated 缺席歧义零收益） |

**独特捕获吸收**（单向吸收进 v2）：B——看门狗期望面按 replicas>0 登记（缩零假告警防线）、spec_fingerprint 含副本数、per-function 互斥 + maintenanceMu 全序、受理位「活跃网络在场」fail-closed、drift spec 对照跳过 function/ 前缀、重启安全 e2e 腿、revisions 永不删；A——Admission outcome 复用、host 占用预检、FLEETLY_FUNCTION_* env 注入、删除在途请求诚实切断、skills 文件与 curl 验收配方、exposure 翻转确认文案、唤醒面两 errcode 预留。

**残余风险共识**（三方各自的最不确定处交集）：换轨无健康门的公网函数风险（B #1，dogfood 数据复核义务）；缩零与唤醒的 private 半边（A #1/B #3，Phase 2 双候选在案）；泛化 vs 域内的抽象切口（A #3/B #2，实施批以 golden 双轨执法：App 部署 golden 零漂移 + Function golden 新增）。
