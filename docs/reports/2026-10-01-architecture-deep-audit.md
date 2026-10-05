# fleetly 核心架构深审报告（2026-10-01，N1 开工前）

| 项 | 值 |
|---|---|
| 审计基线 | HEAD = `ea12367`（N0 正式签收点，零代码改动，只读审计） |
| 审计性质 | 架构级深审：抽象对不对、设计全不全、实现好不好（功能正确性已由 N0 三轮验收闭环，不重验） |
| 方法 | 四路并行子审（契约层 / 引擎状态层 / 前瞻裂缝 / 工程质量）+ 主审交叉核对：全部 P0/P1 证据由主审逐条亲验 file:line，P2 证据抽样核验（抽样 ~15 处零误报） |
| 真源 | CONTEXT.md、设计书三份、ADR 0001~0023、功能清单 N1~N4、N0 验收记忆（挂账已吸收，见 §6） |

---

## 0. 总判定

**地基够夯，可以开 N1——但必须先过一个"前置裁决批 + 小修复批"，否则 F1.1/F1.5 开工即返工。**

三条正面结论（都有第二个实现/第二个用途的推演支撑，不是印象分）：

1. **Runtime 窄面契约真实成立**。拿 N4 k3s 逐方法推演：Ensure=apply、Remove=ns 删、Watch=informer、Addresses=Service DNS、DescribeCluster=NodeList、Enrollment=k3s node-token——核心 6 方法零改动，tsuru 17 方法的扩张路径被实际挡住。N1 的全部压力落在 **Workload/WorkloadEvent 两个数据契约的字段加宽**上（D-1），不是接口扩张。
2. **engine 骨架对 N1/N2 是真复用，不长第二套循环**。Loop 骨架（kick/tick/panic 可见）与聚合无关且守卫钉死唯一；Run 的外部完成信号（Watch 事件→Kick）、绝对 deadline（parseDeadline 模式）、期望-实际对账（managedStep 同构）三条模式都在，Task/Database 各加一条 Loop 实例即可。
3. **守卫文化是真执行不是口号**。四件一拍在 engine 内全部状态机写点均经组合点（grep 验证）；golden 双形态 150 文件、注册表三链咬合、hermetic 假时钟夹具、e2e 断言强度（状态序列/事件序列/否定断言）都在平均水准之上。

四块 P0 缺口：**三个是"设计未拍板"型**（Runtime 数据契约的生命周期表达、Task 载体拓扑、幂等键执法结构——不是做错了，是还没做，但 N1 第一周就要用），**一个是现行 N0 缺陷**（构建孤儿重启死循环）。都不动摇 N0 验收结论。

---

## 1. P0——地基缺陷（上层开工前必须改/必须裁决）

### D-1｜Runtime 数据契约缺"一次性生命周期 + 终态原因"表达

三个证据面合并为一条结构性缺口（N1 Task/Run 的地基）：

1. **Workload 无生命周期声明**：`internal/capability/runtime.go:115-151` 字段全集无 restart/stop-grace；swarm 硬编码 `RestartPolicy{Condition: RestartPolicyConditionAny}`（`internal/providers/swarm/translate.go:181-186`）——one-shot Run 投影成 Workload 后**进程退出即被无限重启**，TTL/停止原因/janitor 全部建立在错误载体行为上。k3s 无此问题（Job 原生），证明这是契约缺字段逼出来的硬编码，不是有意设计。firstBootJobs 的诚实拒绝（`internal/engine/drive.go:105-108`）解锁条件即本条。
2. **WorkloadEvent 无终态原因/退出码**：`internal/capability/runtime.go:238-258` 无 ExitCode/Reason；WorkloadState 五值无"完成"终态（failed 被 degraded 吞并，`runtime.go:250-259`）；swarm `pollTasks` 只统计 `DesiredState==Running` 槽位（`internal/providers/swarm/runtime.go:219-221`）——**已完成的 one-shot task 整个不可见**；container die/exit 事件被订阅（`runtime.go:154`）却在 `mapEvent` 丢弃（`runtime.go:263-287`）——swarm task 的 `Status.ExitCode/Err` 有数据无通道。ADR-0012 的七枚举停止原因（dogfooding 验收明列"停止原因分类"）在当前契约下无承载面；`watchdogBite`（`internal/engine/observ.go:141-150`）把 stopped@当前代一律当部署失败，Run 的正常终止会被同一套门误判。
3. **实例级观测缺位**：引擎观测槽 per-Workload 单槽 last-write-wins（`internal/engine/observ.go:216`），swarm task 级快照被折叠到 service 级 WorkloadID（`internal/providers/swarm/runtime.go:216-243`，task ID/slot/ExitCode 全丢）——resident 池的 N 个实例无法分别观测。

**修复形态**：全部是字段级加宽（Restart/StopGrace 入 Workload；ExitCode/Reason/实例身份入 WorkloadEvent），核心 6 方法签名不动；swarm 侧补 container 终态事件映射与已完成 task 观测；引擎观测模型分"Workload 级（现有 L1/L2/L3）与 Instance 级（Run 状态机）"两轨。**时点：F1.5 设计批的第一个决定，N1 前置。**

### D-2｜Task/Run 载体拓扑与归属轴未拍板（含一处 comment-code 漂移）

- `NamespaceRef{Team,Project,App}` 无 Task 轴（`internal/capability/runtime.go:92-98`）；`WorkloadID = appID+"-"+process` 是 App 形态公式（`internal/engine/projection.go:79-81`）；swarm 命名/标签/selector 全按 App 轴（`internal/providers/swarm/translate.go:47-57,119-140`）。
- **漂移实据**：`translate.go:148-150` 注释声称"taskGroup:<name> 前缀由 engine 已翻译为实际网络名"，但 `projection.go:38` 把 `p.GetNetworks()` **原样透传**，全仓无翻译代码——F1.8 落地时翻译责任没人背。
- 双级稳定 DNS 对应两种载体拓扑：swarm DNS/VIP 是 per-service（`internal/providers/swarm/runtime.go:373-397`）——per-Task 池级轮询 DNS = 1 service×N replicas（天然成立），per-Run 稳定 DNS = 每 Run 一个 service（torchwood 池 N 并发 = N 个 service，Ensure/Watch/收敛成本按 N 放大，且 `desired_concurrency` 变更在两种拓扑下语义完全不同）。
- 跨 Project 用户面挂靠目前只有受管形态 `NetworkRefs`（`runtime.go:142-146` 注释明言"受管面专用"），用户 Workload 用不了；`networks` 表是纯 project 字段（`internal/state/migrations/00004:41-49`），双向声明模型不存在。

**若不先拍板**：Task ID 塞进 `.App` 字段（词汇污染，ADR-0007 文化不可接受）或事后扩 NamespaceRef——swarm 标签集/selector/命名公式、Ensure/Remove/Watch 归属判定全要跟着动；N4 k3s 落地后再改 = 两个 Provider 一起返工。**时点：N1 前置裁决（与 D-1 同批），见裁决项 R-3/R-6。**

### D-3｜幂等键没有通用执法结构，且现有测试钉死了相反语义

- 现状：幂等键只是 `deployments.idempotency_key` 列 + **活跃态部分唯一索引**（`internal/state/migrations/00001_core.sql:64-68`）；`FindActiveByIdempotencyKey` 只查活跃（`internal/state/deployment/repo.go:162-170`）；同键命中即返回既有、**不比较请求体**（`internal/engine/admission.go:60-70`）；`admission_idem_test.go` 把"终态后同键→必须受理为新部署"钉死为 N0 口径——与领域模型场景 6 的目标语义（同键同体重放返回同一结果、同键异体 409、24h 保留）**方向相反**。
- 形态裂缝：`DeployRequest.idempotency_key` 是 body 字段（`proto/fleetly/delivery/v1/delivery.proto:176`），架构 §7 措辞是 `Idempotency-Key` **头**——两套真源并存，冲突规则（同键 body/头不一致）没有答案；torchwood 客户端先按哪个写就锁死哪个。

**修复形态**：单表（key→RPC→请求体指纹→响应引用→expires_at）+ 拦截器级执法（一份实现覆盖全部创建型 RPC）+ janitor 24h 清理；聚合内活跃唯一索引保留为 ADR-0016 admission 去重（两层窗口语义写进 ADR）；A1 测试届时显式改口径。**时点：F1.1 是 N1 第一项，本条是其前置设计——N1 前置。见裁决项 R-4。**

### D-4｜现行 N0 缺陷：构建孤儿重启死循环（queued↔building 无限振荡，部署永久卡死）

证据链（主审逐环亲验）：

1. `buildInputs` 是进程内 map（`internal/engine/engine.go:148-149`），重启即丢；全仓唯一登记点在 `driveBuilding` 的"无 Build 行"分支（`internal/engine/buildsource.go:68-77`）。
2. 重启 → `resetOrphanBuilds` 把 building 回 queued（`engine.go:281-299`）→ `driveBuilding` 见既有 queued 行 `return nil, nil` 只等待（`buildsource.go:55-61`）——**永不重新登记输入**（注释"回 queued 等部署驱动重新登记"的假设不成立）。
3. `buildStep` 拾取 queued → CAS building → goroutine（`internal/engine/builder.go:150-165`）→ `executeBuild` 无输入 → 回 queued + Kick（`builder.go:177-187,216`）→ **无限热循环**（每循环 2 个事务 + 2 条审计行持续膨胀）。
4. 关联 Deployment 永停 `building`（building 态无 deadline，看门狗不咬）；**取消部署也不能停**——build 行不查部署态，孤儿 build 行持续振荡。

触发条件：fleetlyd 在 Build 进行中重启（F1.10 上传构建窗口长，概率放大）。**时点：N1 前置修复批必修。** 方向：driveBuilding 对既有 queued Build 幂等重建输入（`prepareBuildInput` 本身幂等），或孤儿回退计数后 fail；根治（输入落库）随 F1.10。

---

## 2. P1——上层返工风险（附返工场景与时点）

| # | 发现 | 证据（file:line） | 返工推演 / 时点 |
|---|---|---|---|
| P1-1 | **SSE 凭证通道与长流超时未定**：拦截器只认 `Authorization: Bearer`（`internal/authn/interceptor.go:245-256`），浏览器 EventSource 不能设自定义头——F1.2 的 SSE 一挂就是 401；`grpcTimeout=60s` 实为 lynx **优雅关停超时**（`internal/assembly/grpc.go:20-22` 注释错位；`go doc lynx/server/grpc WithTimeout` 自带同名双语义警示），gRPC 长流 60s 被杀，per-method 豁免机制未验证 | F1.2 中途发现 = 订阅面推倒一次。**N1 前置 ADR**（票据端点 vs query token 的安全权衡 + lynx 豁免验证），实现随 F1.2 |
| P1-2 | **API 面全链无请求级超时**：拦截器链仅 ClientInfo/Auth/Validate（`grpc.go:64-83`）；CLI 拨号无 deadline（`cmd/fleetly/cmd/dial.go:30-44`）；EnrollNode→SwarmInspect、TeardownApp→Remove 等 docker 调用无界——引擎循环自己有界（ManagedStepTimeout），API 面裸奔，docker hang 时客户端无限挂起 | 与 staging 实证的 daemon hang 故障形态正交。**N1 前置修复批**：unary 超时拦截器（流式豁免）+ 注释更正 + CLI 默认 deadline |
| P1-3 | **事件流断档三件套缺失**：outbox 只增无 trim、无 earliest-seq（`internal/state/outbox/repo.go` 全文）；`ListEventsRequest` 无过滤字段（`proto/fleetly/telemetry/v1/telemetry.proto:61-69`）；outbox 表无 (aggregate, aggregate_id) 索引（`migrations/00001_core.sql:85-92`）；无 410 语义码 | F1.2 SSE/F1.3 Wait/torchwood 订阅三消费方都要过滤与断档判定；Task 事件量下无 trim 无限增长。**F1.2 前出 ADR**（保留窗/trim 执行者/410 判定/快照重同步形态） |
| P1-4 | **List 分页/过滤惯例缺失**：全 proto 零 page_token/cursor；唯一游标先例是 ListEvents 的 after_seq/limit；`ListRoutes` 服务端全表+内存过滤（`internal/api/fleetlygrpc/contexts.go:164-177`） | F1.5 ListRuns（torchwood 高频创建 Run，全量返回爆炸）、F1.2 事件过滤、N2 Console 全部撞上；中途发明三种游标再统一 = 全 API 面 golden 翻新 + torchwood 客户端改两次。**N1 前定惯例**（推广 after_seq 形态），落地随各 F 项 |
| P1-5 | **managed reconciler 是 Proxy 专用实现**：受管面唯一入口硬编码 `e.proxy.(capability.Managed)`（`internal/engine/managed.go:66-67`）；"挂全部活跃 Project 网络"是 Proxy 特有语义住在通用 reconciler 里（`managed.go:73-78,148-164`）；单域单 Generation（`engine.go:121`） | F1.11 zot 若照抄单槽断言 = 第三次复制；单 gen 下任一受管组件变更 bump 全域 gen，ADR-0015"逐个 reconcile"无法表达。**随 F1.11 generalize**（多 Provider 迭代 + per-域 gen + 网络挂靠策略由 Provider 声明） |
| P1-6 | **四件一拍是纪律不是机制**：deployment `transit`（`admission.go:214-236`）与 build `transitBuild`（`builder.go:220-250`）已是两份拷贝（`stateIn`/`buildStateIn` 重复）；`repo.Transit` 公开无人拦——新写手绕过组合点忘事件/忘审计**无守卫会红** | N1 Run/Schedule + N2 Database/Backup 至少三个新状态机 × ~80 行拷贝。**N1 Task transit 落地前一次性抽**（最小 Knock 接口 + transitOne；特例超三成回退为约定+守卫反扫）。见裁决项 R-8 |
| P1-7 | **观测缓存五张 map 是"部署形状"**：键 = `appID-process`（`projection.go:79-81`）；唯一清理路径是 App 级 Teardown（`teardown.go:50-77`）；恢复真源只有 succeeded 部署重放（`drift.go:30-58`）；gen 语义绑定部署滚动（`observ.go:118-135`） | Run 粒度混入同 map = drift 扫描对 Run 键做 App 表解析报错、gen 语义错位、高频池伸缩下条目只增不清；**Run 恢复语义根本不同**（部署重放=幂等 Ensure，one-shot Run 重放=重跑作业——必须按 Task/Run 行 + deadline 判定，rebuildBaselines 不能抄）。**N1 Task 设计批**：automation 域独立缓存组 + per-Run 终态回收随 janitor |
| P1-8 | **治理刹车执法缝**：拦截器无 Project 上下文（`interceptor.go:43-54,134-179`）；词表现无 tasks/databases（`internal/assembly/policy.go:37-43`）；属主吊销→Task 排空的钩子位不存在（`identity.go` RevokeToken 为纯行更新）。先例在：PutConfig 配额（`structure.go:400-412`）、touchLastUsed 节流（`interceptor.go:172`） | F1.9/F1.6 落地时的分工：配额/freeze 归服务层受理位（PutConfig 模式）、per-Token 速率归拦截器、吊销排空走 task 环周期扫 revoked 属主（拉式、重启安全）；Task 行须记 `owner_token_id`（token ID 引用非明文）。**随 F1.9/F1.6 设计批** |
| P1-9 | **双级稳定 DNS 无契约承载**：`Addresses(ns)` 签名与 `Endpoint{Addr,Process,Port}`（`runtime.go:34-35,271-278`）表达不了两级；稳定 DNS 名是平台 API 面（用户/Agent 直连），名字公式住 Provider 则 N4 换 Runtime 名字即变——场景 3"无状态全语义保持"必含名字稳定 | **N1 前置设计裁决**：平台标准 DNS 名公式落 engine、Workload 加只增 addressing 声明字段（swarm 映射 alias；跨服务 alias 的 DNS RR 行为需 e2e 实证）。见裁决项 R-3 |
| P1-10 | **Team 轴未接实（主审自有发现）**：`appTeam` 硬编码返回 "default"（`observ.go:53-59`）、`resolveBackend`/`activeProjectNetworks` 同样硬编码（`managed.go:132,159`）；`projects.team_id` 有槽位但默认 'default'（`migrations/00001_core.sql:11`）；**`idx_projects_name` 是全局唯一不含 team_id**（`00001_core.sql:16`）——Team 1─* Project 的领域关系在 schema/引擎两层都未闭合，多团队时项目名跨团队互斥、载体域全落 default | F1.8 跨 Project 互通（双向声明要判Team 边界）与 N1 治理（per-Project 配额要按真实 team 归属）之前必须闭合。**N1 前裁决**：接实时点（建议随 F1.8 批）+ 唯一索引是否改 `(team_id,name)`（迁移裁决）。见裁决项 R-7 |
| P1-11 | **Platform Restore 只读/升级序零地基**：全仓无 readonly 实现（仅卷 ReadOnly 误命中）；无 Backup 聚合、无 ObjectStore 实现、`config.proto` 无 capability/provider 配置段（`internal/config/config.proto` 只有 Server/GRPC/HTTP/Data） | 真裂缝：N1 每个 F 项都在新增写入口（CreateTask/RenewTask/Schedule/Upload/Database/NetworkRef），每一处都是未来的只读执法点——无集中闸门则 F2.3 时逐 RPC 追一遍。**接口 N1 前定**（platform-state 单例 + 拦截器一行检查 + engine 漏斗检查），检查点随 N1 写面同步落，完整实现随 F2.3 |
| P1-12 | **DatabaseSpec 缺 storage 声明 + 单翻译线未钉死**：架构 §4（line 80）设计了 `storage`，proto 没落（`spec.proto:192-209` 只有 cpu/mem+backup）；`Project()` 只吃 AppSpec（`projection.go:16`） | N2 升级矩阵与 N4 PVC 都要容量声明，字段晚加过 schemaVersion 评审且历史 Revision 需默认值兜底；若 F1.12 图省事 DatabaseSpec→Workload 绕过 Project() 就是旧项目"第二份翻译实证漂移"重演。**F1.12 设计批**：补 storage + 钉死"dbtemplate 渲染产物 = AppSpec" |
| P1-13 | **gateway 缺 identity 六个服务的 REST 注册**：identity.proto 有 21 处 http 注解，gateway 注册清单（`internal/assembly/gateway.go:44-59`）无 Users/Teams/Roles/Tokens/Invitations/AuditQuery，Hooks 配置面同病（只挂了原生接收路径） | REST 面当前无法完成登录/token 操作（架构 §1"Console 只消费公共 REST API"的承诺面缺口）；F2.6 最小 Console 登录即撞。**F2.6 前置补挂**（纯加法）；注意别届时顺手改注解形态踩 buf breaking |
| P1-14 | **swarm secrets 遍历序不确定（现行缺陷面）**：`for platformName, carrier := range secretCarriers`（`translate.go:171-179`）map 遍历序随机，`container.Secrets` 顺序不稳定——对照 Env 特意排序（`translate.go:145-147`）"幂等 diff 稳定"的自我要求 | ≥2 个 secret_refs 的 App：L1 等待期每 tick 重 Ensure（`drive.go:131-137`）生成的 ServiceSpec Secrets 顺序约半数与上次不同 → ServiceUpdate 视为 spec 变更 → 无谓滚动替换/L1 反复重置。swarmkit 对仅顺序差异是否实际触发 task 重建**未验证（需 dind 实证）**，但代码层确定性破缺成立。**N1 前置修复批**（一行 sort + "多 secret 重放 spec 逐字节稳定"断言） |
| P1-15 | **StreamLogs follow 只能看到第一个容器（现行功能缺陷）**：串行循环容器（`internal/providers/swarm/logs.go:26-34`），单容器 Follow 永不 EOF（`logs.go:82-106`）——`fleetly logs --follow` 在 replicas>1/多进程 App 上静默丢其余容器；假底座明言不实现 Follow（`internal/apitest/harness.go:322-324`），缺口无测试覆盖 | F0.25 已交付功能的诚实性缺口；torchwood 依赖 logs。**N1 前置修复批**（goroutine fan-in 合并 + 多容器 follow 单测） |

---

## 3. P2——改进（分组列示）

### 3a. 契约与守卫补全

- **C-6** schemaVersion check-strategy 半落地：写面校验在（`internal/spec/spec.go:36,162,189`），读面 `loadSpec` 用 `DiscardUnknown: true` 无版本检查（`internal/engine/observ.go:39-49`），"可读旧版+提示"的 hint 路径不存在。→ 挂账至首个 schemaVersion 升级批（进 ADR-0002 执行清单）。
- **C-10** swarm 缺 RuntimeInspector 编译期断言（`internal/providers/swarm/provider.go:29-34` 自称"三个子面"，InspectWorkloads 实现在 `runtime.go:470`）——接口改名时静默降级 gen-only 无红灯。→ N1 前置顺手（一行）。
- **C-11** WorkloadObservation 无 Command 字段，ADR-0022 决策 1 原文承诺"（镜像/副本/命令）"，实现只比 Image/Replicas（`internal/engine/drift.go:226-232`）——`docker service update --command` 人工改载体不触发 drift。**与 ADR 冲突，以 ADR 为准**：属实现缺口非 ADR 该改。→ 随 N1 批补字段（swarm 侧 ContainerSpec.Command 可回读）。
- **C-12** 守卫覆盖面与 ADR-0001 文本不一致：`leafSubtrees` 仅 model/spec（`internal/guards/importguard_test.go:44`），架构 §2 明言 capability 也是叶子接口层；**主审补充：identity 包同样自称"域纯度与 model/spec 同级（守卫见 internal/guards）"（`internal/identity/scope.go:3-4`）却也不在守卫清单**——两个"事实叶子"靠自觉。→ N1 前置顺手：leafSubtrees 加 capability/identity；ADR-0001 或 irguard 注释澄清 Go 包豁免理由。可选：加"Runtime 接口方法数上限"守卫钉死窄面纪律（backlog）。
- **C-13** JobSpec 与 ProcessSpec 字段重复定义（`spec.proto:151-163` vs `:56-81`），且与 CONTEXT.md 2026-10-01 裁决（firstBootJobs 是 Task 部署期特例）不一致；TaskSpec 嵌 ProcessSpec 才是正确共享形态（`:167-182`）。当前 JobSpec 唯一消费者是拒绝路径（`drive.go:105`）——**零成本重塑窗口**。→ 随 D-1 生命周期字段同批定形。
- **C-14** ADR-0003/架构 §3"契约类型全部定义在 proto"与实况不符（Runtime 契约类型手写 Go 于 capability）。deletion test 成立（不是代码缺陷），属文档-代码漂移。**ADR 该改措辞**：建议修订为"IR 契约在 proto，端口契约 Go、proto 化延后至进程外插件需求出现"。→ backlog。
- **C-15** config_refs 只有 proto 字段无材料通道（`spec.proto:71-72`；Materials 仅 RegistryAuth+SecretFiles，`runtime.go:210-218`；resolveMaterials 无 config 面）。Config 有挂载路径语义，加通道时需定形 name→{path,content}。→ 随 N1 材料批（与 ADR-0023 点名的 secret 载体 GC 同批）。
- **C-8** Metrics.QuerySeries 单 Series 返回、无 step（`ports.go:148-153`）——PromQL range 天然多序列。→ N2 批前修签名（现在是零成本窗口）。
- **C-9** LogQuery 缺文本过滤字段（`runtime.go:313-324`；Logging.Query 复用同类型）——N2 `--grep` 基本面。→ 随 N2 批。
- **C-17/F-10** RuntimeExec 子面架构 §5 画了、代码无定义；`FLEETLY_CONTROL_GRPC_ADDR` 回拨物化零实现。→ 可选现在补空接口保持文档诚实；实现 N3 前置。

### 3b. 错误面一致性（N1 前置修复批顺手收口）

- **Q-8** resolveMaterials 把 registry 凭证查询一切错误当"无凭证"（`materials.go:36-39` `if err != nil { continue }`）——DB 故障静默降级匿名拉取，pull 阶段以难懂错误失败。应区分 `state.ErrNotFound`。
- **Q-12** mapStateError 把 ErrConflict 一律渲染 "already exists"（`mapping.go:39-40`）——CAS 前置不符/FK RESTRICT 全部误导文案；DeleteApp 已用 E_CONFLICT，错误面不一致。
- **Q-13** Rollback no-baseline 映射靠错误文本 Contains（`delivery.go:172`）——应提哨兵（engine.go 已有哨兵先例）。
- **Q-24** authn resolve 把存储故障混为 "invalid token" 且无日志（`interceptor.go:191-194`）。
- **Q-19** swarm watchLoop 静默吞锚定/轮询错误（`runtime.go:189-190` `_ =`）——pollTasks 是 L1 权威数据源，持续失败时全部部署无诊断卡到超时；`runtime.go:170` 重连 sleep 不感知 ctx（小）。
- **Q-20** ensureNetworks/ensureSecrets 不区分 NotFound，inspect 瞬时错误走 create 撞名（`network.go:49-51,87-89`；service 路径有 isNotFound 判定，此处缺失）。
- **Q-9** driveBuilding 读 Revision 失败静默用 revSeq=0（`buildsource.go:47-50`）→ 镜像 tag 撞 `r0`。应硬失败。

### 3c. 状态层与 SQL

- **Q-5** deployment Transit 不检查 RowsAffected（`deployment/repo.go:239-249`），"即使放开连接池也安全"的注释声明不成立（对照 secret/hook repo 做对了）。→ 3 行修复，随修复批。
- **E-8** Volume.Pin 写状态无审计/事件（`materials.go:96-105` Runner 直写）——卷钉住是永久调度绑定却查不到"谁/何时钉的"。→ 随 N1 材料批。
- **E-10** node.joined 事件与 nodes 行非原子（`observ.go:227-246` Tx 只包 Upsert）。→ 顺手。
- **Q-23** NextGeneration 的 MAX() 无 (app_id,generation) 覆盖索引（`00001_core.sql:62`）。→ 随下个迁移批。
- **Q-26** DeleteApp tombstone 事务内全表 Routes.List + Go 过滤（`structure.go:247-258`），idx_routes_app 在册未用；ListRoutes 同构（`contexts.go:164-177`）。
- **E-11** 单连接 SQLite（`state.go:108-110`）在 Run 事件量下的吞吐上限——容量意识项：N1 验收做一次事件量压测锚点，超限再议读池分离。

### 3d. API/装配与安全

- **Q-11** webhook 25MiB 上限（`gateway_hooks.go:31-32`）与 gRPC 默认 4MB 接收上限错位（全仓无 MaxRecvMsgSize 配置）。→ 取齐。
- **Q-14** gRPC 拦截器链无 recovery（`grpc.go:64-83`；HTTP 侧有）——handler panic 拖死进程，apperr.New 未注册码 panic 会把拼写错误升级为进程崩溃。→ 请求期 recovery + 启动断言保留。
- **Q-15** DeleteProject 无任何守卫（`structure.go:115-132` 直接软删，不查活跃 App/部署/路由）——与 ADR-0023 收口语义不一致，删后 App 仍在跑、路由继续发布。→ N2 前收口（对称三道复查或诚实文档标注）。
- **Q-16** CreateToken/CreateUser 不校验 role 与 team 归属一致（`identity.go:318-330,80-86`）——多团队批前必须收口（与 R-7 联动）。
- **Q-17** Secret 值无大小/数量配额（configs 有 256KB/100 条，secrets 无——`structure.go:387-390` vs `:314-345`）。
- **Q-18** secret 值的 sha256 短指纹进 docker secret 载体名（`swarm/network.go:69-77`）——可列举载体者可离线验证候选值；可用平台 pepper 的 HMAC。→ backlog。
- **F-14** `fleetly.local` 本地域 → zot 上线后存量镜像引用迁移口径未定（`buildsource.go:22-29` 注释自认 N1 替换）——staging 现役双节点会直接撞上。→ F1.11 设计带存量口径。
- **F-15** quickstart 私有轮询（120×1s，`quickstart.go:178-194`）将与 F1.3 --wait 形成两份等待语义。→ F1.3 收编。

### 3e. 并发与生命周期

- **Q-6** loadSpec 深处 `context.Background()` 脱离取消链（`observ.go:40`）——全部驱动路径受累，Stop 有界排空打不断。→ 签名加 ctx（调用方全有）。
- **Q-7** 构建 goroutine 不入 wg（`builder.go:164` `go e.executeBuild` 无 wg.Add；runCtx=Background+Timeout 从不被 Stop 取消）——Stop 排水不含在途构建，OnPostStop 关 DB 时 transitBuild 可能报错（重启自愈存在：resetOrphanBuilds 兜底）。→ 计入 wg + WithoutCancel 树。
- **Q-25** engine 锁序为隐式约定未文档化（唯一嵌套点 `drift.go:216-246` obsMu.RLock 内取 driftMu；已排查无反序获取、当前无死锁面）——新增嵌套代码易引入反序。→ 循环骨架注释处写一句锁序。
- **Q-21** webhook 去重锚先于副作用独立提交（`webhook.go:84-91`）——两步间崩溃时 GitHub 重投被判 duplicate、部署丢失（窗口极小）。→ 根治=去重与效果同事务或以幂等键承担去重（随 D-3）。
- **Q-22** managedStep 单一 30s 总预算 + publishRoutes 每 route 一次全量 ServiceList + pollTasks 每 service 一次 ServiceInspect（`managed.go:22-32,110-123`；`swarm/runtime.go:204-215`）——规模隐患非当下 bug。→ N2 容量批。

---

## 4. 抽象裁决表

| # | 抽象 | 裁决 | 理由（证据锚） |
|---|---|---|---|
| 1 | Runtime 契约（核心 6 方法） | **保留** | k3s 逐方法推演零核心改动；Enrollment 对 k3s 语义成立（node-token 生成/轮换+agent 命令；轮换对存量 agent 的影响未验证）；Admin 的 Drain/Cordon 本身是 k8s 原语。N1 压力全在数据类型加宽（D-1），无一处需要加方法 |
| 2 | Runtime 子面族（Logs/Admin/Inspector） | **保留 + 补 Exec 定义** | 可选子面+诚实降级模式经 ADR-0022 检验；Exec 架构画了代码无（C-17），补空接口保持文档诚实 |
| 3 | Workload 类型 | **调整** | 加 lifecycle（Restart/StopGrace）与 addressing 声明（D-1/P1-9）；其余字段与 swarm+fake 两实现贴合良好 |
| 4 | WorkloadEvent | **调整** | 加终态原因/退出码/实例身份是 N1 硬前置（D-1）；流模型本身（单 channel、ID 归属过滤、gen 携带）经受住 ADR-0022 两轮口径修订 |
| 5 | NamespaceRef | **保留（注释调整 + Task 轴裁决）** | 三元组机械够形；`.App` 实为"域主体 ID"的语义漂移需注释澄清（Task/Database 复用时）；k3s 映射 label 而非真 namespace 的策略入契约注释 |
| 6 | Capability 端口族 + 编译期注册表 | **保留** | Provider 三件套（Describe/Health）经 Proxy/Builder/Runtime 三实现检验成立；Registry.Endpoint 够 F1.11 zot 用；Metrics/Logging 签名各自落地前修（C-8/C-9） |
| 7 | Managed 子面 | **保留接口 / 调整 reconciler** | 接口声明面足够；引擎侧从 e.proxy 单硬编码泛化为多 Provider 迭代+per-域 gen+排序策略（P1-5），属引擎工作非契约改动 |
| 8 | Spec IR 三形态 | **保留（局部重塑）** | TaskSpec 嵌 ProcessSpec 是正确共享；JobSpec 趁零消费者窗口重塑（C-13）；DatabaseSpec 补 storage（P1-12）；三形态共用 schemaVersion/Source/Resources 的整体形状成立 |
| 9 | schemaVersion | **保留字段 / 调整读路径** | 写面校验在；读面检查与旧版提示缺失（C-6），挂账至首个版本升级 |
| 10 | 归一化管道（compose 白名单） | **保留** | 白名单只增+拒绝理由精确到字段路径的模式被 B2/P2-3 两轮修复验证可演进；不引抽象管道接口是对的（每源一个 AppSpec 构造器足矣） |
| 11 | 单写者 Loop 骨架 | **保留** | 四环已复用+守卫钉死唯一；N1/N2 各加实例不长第二套（E-2 结论性验证） |
| 12 | 四件一拍 transit 组合点 | **调整（裁决项 R-8）** | 两份拷贝已在、第三/四份在即、repo.Transit 公开无人拦；抽最小 Knock 接口 vs 约定+守卫反扫，见正反方 |
| 13 | repo 聚合分包 | **保留** | 全部 repo 形态一致（New(clock)+Runner）；statefacade 守卫双向保鲜；task/schedule/run、database/backup 照此落 |
| 14 | 实体住 state 包（model 只剩注册表） | **维持现状（裁决项 R-1）** | 架构 §2 已明文记录该实况（"实体与状态机随 N0 实况落在 spec/state/engine"）——用户疑似的文档漂移实际已对齐；争议在长期形状，见正反方 |
| 15 | 观测缓存五张 map | **调整** | App 面保留（rebuildBaselines 闭环）；automation 域分家：独立缓存组 + per-Run 回收 + 恢复真源走 Task 行（P1-7） |
| 16 | appLocks（App 级互斥） | **保留语义** | N0.1 P1-3 串行化价值实证；Task 需同构互斥，终态清理与已知"不回收"挂账一并设计 |
| 17 | admission 六步判定序 | **保留（部署域）** | ADR-0016+N0 修订已验收；Task 只迁移幂等/配额两步到受理位，latest-wins/supersede 不复制 |
| 18 | rebuildBaselines（启动基线重放） | **保留 for App / Run 不抄** | 部署重放=幂等 Ensure；one-shot Run 重放=重跑作业，恢复必须按 Task 行+deadline 判定 |
| 19 | outbox 只增模型 | **调整** | N1 SSE 批定保留窗+earliest-seq+410+快照重同步（P1-3）；只增保证正确性，缺的是消费契约 |
| 20 | Materials（RegistryAuth+SecretFiles） | **保留（增量扩面）** | 载体无关；Task/Database 只加 collector；ConfigFiles 通道（C-15）与 env 注入形态（若 torchwood 需要 env 而非文件——需显式裁决）字段只增 |
| 21 | 拦截器 per-method scope 执法 | **保留** | 治理刹车不整体进拦截器：per-Token 速率进（touchLastUsed 天然位），配额/freeze 归服务层受理位（P1-8） |
| 22 | 守卫体系 | **调整** | 覆盖面补全：capability/identity 叶子纯度、swarm Inspector 断言、（可选）Runtime 方法数上限守卫（C-10/C-12） |

---

## 5. 裁决项（需用户或新 ADR 拍板，本报告不自行改设计）

| # | 议题 | 正方 | 反方 | 建议 |
|---|---|---|---|---|
| R-1 | **实体住 state 聚合包 vs 集中 model 包** | 维持：State 谓词与行定义同文件内聚、model 保持纯叶子、依赖图最简；架构 §2 已如实记录 | 集中：横切领域规则（如"Run 终态必带 stopReason"）无家可归，聚合包变相公共类型层 | **维持现状**；横切校验住 spec 校验面或 transit helper；N1 Task 实体落 state/task（强聚合可含 Run）。真正该改文档的是 ADR-0003 措辞（C-14），不是结构 |
| R-2 | **事件负载要不要版本化**（user 点名） | 版本化：订户=Console+Agent+Skills+torchwood，一次字段改名就是静默断炊；payload 加 version 字段成本极低 | 不加版本号：payload 本就是自由 JSON 且"字段只增"注释自律 + N-1 CLI 兼容已覆盖跨版本；版本号解决不了"改名"（只增原则下改名本就禁止），真正缺的是**形状钉扎** | **不引入版本号字段**；随 F1.4（`fleetly schema`）把 payload JSON Schema 反射暴露 + golden 钉形状——形状钉扎比版本号对症。若采纳需新 ADR 记录"事件 payload 只增 + Schema 暴露"口径 |
| R-3 | **稳定 DNS 名公式住 engine 还是 Provider** | engine 铸名：名字是平台 API 面（用户/Agent 直连），场景 3"无状态全语义保持"必含名字稳定——公式住 Provider 则换 Runtime 名字即变；engine 算好作为声明下发，Provider 映射为自己的原语（swarm alias / k8s Service） | Provider 铸名：保持 Addresses 纯观测、不引入平台命名语义到 Runtime 契约；DNS 名走平台 DNS 组件——但引入新受管组件，违背轻量 | **engine 铸名**（Workload 加只增 addressing 声明字段）；swarm 侧跨服务 alias 的 DNS RR 行为需 e2e 实证后再定稿 |
| R-4 | **幂等键形态：header 为主还是 body 字段** | header 为主（架构 §7 原文）：拦截器级一份实现覆盖全部创建型 RPC，20+ RPC 不逐个加字段；`DeployRequest.idempotency_key` 降级为部署专锚（commit 去重锚）或 deprecated 别名 | body 字段：proto 显式可见、CLI 旗标直填现状即此；但逐 RPC 重复且"同键异体 409"逐个手写 | **header 为主 + 双源冲突规则进 ADR**（同键 body/头不一致 → 拒绝）；通用表+拦截器随 F1.1 |
| R-5 | **两级变量（SharedVariable/Variable）合成时机** | (a) 归一化期合成、Revision 冻结最终形态：与 spec.proto:67-68 现注释及 ADR-0002"基线永远以 Revision 为准"一致；改共享变量需重部署才生效（可解释、可 diff） | (b) Ensure 前动态解析：改共享变量即时生效（DX 好）；但 Revision 不再是行为真源，Drift/diff 语义被掏空 | **(a)**——与 Revision 冻结语义一致；(b) 的 DX 收益可用"改变量时提示受影响 App"补足。需新 ADR（当前注释押注 (a) 代码没写，N1 引入 Variable 前必须显式裁决） |
| R-6 | **resident 池载体拓扑**：pool=1 service×N replicas vs Run=每 Run 一个 service | 混合：池级 DNS 用 pool service（天然 RR）、per-Run 控制（TTL/排空/per-Run DNS）用 per-Run service——语义最正但 Ensure/Watch 成本×N | 纯 pool service：成本最低，但 per-Run DNS/独立排空无法表达 | **倾向混合**，但这是 D-2 设计批的核心裁决——torchwood 实际用量（池规模）应作输入；per-Run=service 时 swarm API 压力需压测 |
| R-7 | **Team 轴接实时点 + project 名唯一性口径** | 随 F1.8（跨 Project 互通要判 Team 边界）接实：appTeam/resolveBackend/activeProjectNetworks 三处从 Project 行实取；`idx_projects_name` 改 `(team_id, name)`（加法迁移建新索引） | 延后（单团队现状无实害）：team_id 槽位在、默认 default 不破功能；但多团队窗口一旦打开，全局唯一项目名是隐式约束收紧，且载体域全落 default 时跨团队隔离在 Runtime 层不存在 | **F1.8 前接实**；索引改不改随接实批一并裁决（注意存量唯一索引的迁移顺序） |
| R-8 | **四件一拍：抽 helper 还是纪律+守卫** | 抽最小 Knock 接口 + transitOne：错误面（忘事件/审计）从"纪律"变"机制"；deployment/build 先迁移验证表达力，Run 作第三个消费者 | 保持每线一份 + 守卫反扫 repo.Transit 直调点：各线细节不同（rollback afterFP 弯折、markTerminal 钩子），过早抽象恐造参数爆炸的 God-helper——旧项目教训是**无组合点的六份拷贝**，不是"一份共享"本身 | **先抽最小接口试一个批次**；若每线特例超三成立即回退为约定+守卫。迁移窗口=N1 Task transit 落地时（第三份拷贝出现前） |

---

## 6. 已有挂账合并列示（N0 记忆在册，不重报）

- swagger/openapi 远端生成通道翻回（本地通道已钉，c09261a）。
- B2/C2/C4 追认（N0 验收修复项的正式追认，fa6da15 已判"通过"）。
- appLocks 惰性锁条目永不回收（engine.go:126-130；Task 同构互斥设计时一并收口，见裁决表 #16）。
- 审计 outcome 细节丰富化、探针升级注意（http path 校验/端口 fallback 已修，ba6823a）。
- `E_IDEMPOTENCY_KEY_CONFLICT` 入册随 F1.1（checklist 已挂账，D-3 结构先行）。

---

## 7. 整改批次切分建议

### 批 0：N1 前置修复批（小批，纯代码，~D-4 + 现行缺陷面）

1. D-4 构建孤儿死循环（幂等重建输入或回退计数 fail）。
2. P1-14 swarm secrets 排序 + spec 逐字节稳定断言。
3. P1-15 StreamLogs 多容器 follow（fan-in）+ 单测。
4. P1-2 unary 超时拦截器 + grpcTimeout 注释更正 + CLI 默认 deadline。
5. 顺手收口（3b 全部 + Q-5/Q-6/Q-9）：错误面一致性七项、Transit RowsAffected、loadSpec ctx。
6. 守卫补全（C-10/C-12）：swarm Inspector 断言、leafSubtrees 加 capability/identity。

### 批 0.5：前置设计裁决批（纯 ADR/设计文档，不写码，可与批 0 并行）

R-2（事件负载钉扎）、R-3（DNS 归属）、R-4（幂等键形态）、R-5（变量合成时机）、R-6（池拓扑）、R-7（Team 轴）、R-8（transit helper）+ D-1/D-2 的契约定形（Workload/WorkloadEvent 字段集、NamespaceRef Task 轴注释或扩展、taskGroup 翻译责任点=engine 投影层）+ P1-4 List 惯例 + P1-1 SSE 凭证通道 ADR。产出物：2~3 篇 ADR + spec.proto/capability 字段集评审。

### 随 N1 各 F 项落地（不前置，按依赖序）

- F1.1 幂等：D-3 结构（通用表+拦截器；A1 测试改口径；Q-21 webhook 去重随此收口）。
- F1.2 事件流：P1-3 断档三件套 + P1-1 SSE 实现；P1-5 不动。
- F1.3 Wait：F-15 quickstart 轮询收编。
- F1.5/F1.6 Task/Run：D-1 契约加宽落地、P1-7 独立缓存组、P1-8 吊销排空钩子、R-8 transit 抽取。
- F1.8 互通：R-7 Team 接实、taskGroup 翻译、NetworkRefs 用户面放开、networks 表双向声明模型。
- F1.9 刹车：P1-8 配额/速率/freeze 分层执法。
- F1.10 upload：构建输入落库（D-4 根治）+ 流式面上限。
- F1.11 zot：P1-5 reconciler generalize + F-14 fleetly.local 存量口径。
- F1.12 Database：P1-12 storage+单翻译线钉死 + C-15 ConfigFiles 通道（材料批含 E-8/E-10）。

### 随 N2 / backlog

- N2 前：C-8/C-9 签名、P1-13 gateway 补挂、Q-15/Q-16/Q-17、P1-11 完整实现、Q-22 容量批。
- Backlog：C-6（挂账至首个 schemaVersion 升级）、C-14（ADR-0003 措辞修订）、Q-18（指纹 pepper）、Q-23（索引）、E-11（压测锚点）、Runtime 方法数守卫。

---

## 8. 重分类：真缺陷 vs 待办未实现（2026-10-01 补，主审复核）

§1~§3 的 P0/P1 按"返工风险"切级，混了两类性质不同的发现。本节按**性质**重切（补验三处：CreateProject 持久化 team_id 于 `structure.go:73-76`；config_refs 经两条现役源均不可达——`spec_file` 标注"随 API 扩展批"未开放（`delivery.proto:169`）、compose 白名单不含 configs；webhook 25MiB 错位坐实——原生 handler 收 25MiB 后经共享 gRPC conn 调 ReceiveWebhook（`gateway_hooks.go:5,47-56`），过 4MB 默认上限）。

### A. 真缺陷——已交付功能/契约今天就错（批 0 修复对象）

| # | 发现 | 触发条件 | 性质 |
|---|---|---|---|
| A-1 | **D-4 构建孤儿重启死循环**（唯一 P0 级真缺陷） | fleetlyd 在 Build 进行中重启/崩溃 | 行为错：无限热循环 + 部署永卡 building + 取消不停 |
| A-2 | P1-15 StreamLogs follow 只见第一个容器 | 任何多副本/多进程 App + `--follow` | 行为错：已交付功能静默丢日志（F0.25 面） |
| A-3 | P1-14 swarm secrets 遍历序不确定 | ≥2 个 secret_refs 的 App + 重放（L1 期每 tick 重 Ensure/重启基线重放） | 确定性破缺（代码层确证）；是否实际触发滚动需 dind 实证 |
| A-4 | Q-10 traefik routeKey 有损归一化碰撞 | 同 Project 内 `(host="a.b", path="/c")` 与 `(host="a.b-c")` 类命名 | 行为错：一条路由静默不生效（用户可构造） |
| A-5 | Q-11 webhook 25MiB/4MB 错位 | 4MB~25MiB 的 GitHub payload | 行为错：设计内大小死于不透明 ResourceExhausted |
| A-6 | Q-15 DeleteProject 无守卫 | 删有活跃 App/路由的项目 | 语义错：删了项目、App 继续跑路由继续发（ADR-0023 精神未覆盖） |
| A-7 | P1-2/Q-3 API 面无请求超时 + grpcTimeout 注释错位 | docker hang（staging 实证过的形态）落在同步 API 路径 | 行为错：客户端无限挂起；注释谎报语义（`go doc` 证实 WithTimeout=优雅关停） |
| A-8 | C-11 drift spec 对照缺 Command | `docker service update --command` 人工改载体 | 口径收窄：已交付功能低于 ADR-0022 承诺（镜像/副本/命令） |
| A-9 | P1-13 REST 契约已发布未挂载 | 任何人按 OpenAPI 调 identity REST 面 | 契约错：proto 注解/openapi 已发布、运行时 404 |
| A-10 | 错误面小缺陷群 | 各自低频 | 误导/观测缺失：Q-8（凭证查询吞错降级匿名拉取）、Q-12（ErrConflict 文案一律 "already exists"）、Q-24（存储故障报 invalid token 无日志）、Q-19（L1 权威数据源失败无诊断）、Q-20（inspect 瞬时错误走 create 撞名）、Q-9（revSeq=0 撞 tag）、Q-21（webhook 去重窗口破坏 at-least-once）、Q-6（loadSpec 脱离取消链）、Q-7（构建 goroutine 不入排水 wg） |

### B. 灰色——API 已暴露但语义未闭合（不是错，但不是诚实的"未实现"）

- **P1-10 Team 轴**：CreateTeam/`CreateProjectRequest.team_id`（`structure.proto:192`）已收数据并持久化，但引擎三处硬编码 "default"、scope 执法不含 team 维度——用户能建"什么都不隔离"的团队；连带 Q-16（token 的 role/team 不交叉校验）。处置：要么文档/Console 明示"v1 单团队执法"，要么 R-7 裁决接实时点。
- **D-2 内的 taskGroup 注释漂移**：`translate.go:148-150` 注释声称"engine 已翻译"，实际 `projection.go:38` 原样透传；行为面是响亮失败（挂不存在的网 → 部署失败），可接受，但注释撒谎。随 F1.8 落地或先改注释。

### C. 待办未实现——规划内功能尚未到实现时点（不是 bug；报告列为 P0/P1 是"前置设计裁决/结构前置"性质）

D-1（Workload 生命周期/WorkloadEvent 终态字段——swarm 硬编码 ConditionANY 对**已交付的长运行 App 是正确行为**）、D-2 主体（Task 载体拓扑）、D-3（幂等键 24h/同键异体语义——现状符合 ADR-0016 N0 修正口径，`admission_idem_test` 钉的正是 N0 语义）、P1-1 的 SSE 部分（EventSource 401 是 F1.2 未落地的自然结果）、P1-3（outbox trim/410——"只增"是 N0 有意设计）、P1-4（分页）、P1-5（managed 泛化——deletion test 文化本就等第二个实例）、P1-6（transit 抽象）、P1-7（Run 观测缓存）、P1-8（治理刹车）、P1-9（双级 DNS）、P1-11（只读闸门）、P1-12（DatabaseSpec storage——proto 零消费者）、C-6（schemaVersion 读面——单版本不可证伪）、C-13（JobSpec 重塑——零消费者窗口）、C-15（config_refs 通道——**经补验两条现役源均不可达**，纯待办）、C-16（两级变量）、C-8/C-9（N2 签名）、C-17（RuntimeExec）、F-14（fleetly.local 迁移口径）、F-15（quickstart 轮询收编）。

### D. 防护/加固——今天不出错，缺护栏或容量余量

C-10（Inspector 断言）、C-12（叶子纯度守卫覆盖 capability/identity——现状无违例，纯护栏）、Q-5（Transit RowsAffected——单连接掩蔽）、Q-13（错误文本 Contains 耦合——现工作）、Q-14（gRPC recovery——无已知 panic 路径）、Q-17（secret 配额不对称）、Q-25（锁序文档）、E-8（Volume.Pin 审计——审计口径本限"Token/人/Agent 写操作"，pin 是系统内部写，属口径裁量）、E-10（node.joined 原子性——观测面小窗口）、Q-18（指纹 pepper）、Q-22/Q-23/Q-26/E-11（容量与效率余量）。

**重分类结论**：真缺陷共 10 组（A-1 唯一 P0 级，A-2~A-9 为已交付面上的确定缺陷，A-10 为低频误导/观测缺失）——全部落在批 0 修复范围，与 §7 切分一致；原报告的 D-1/D-2/D-3 P0 定级**维持**（它们是"N1 开工前必须拍板/补字段"的前置裁决，不是 bug），但读法应是"还没做"，不是"做错了"。

---

## 9. 审计方法与可信度说明

- 四路子审各自独立读完全部指定真源与代码后产出；主审合并前对**全部 P0/P1 的 file:line 证据逐条亲验**（含 D-1 的 swarm 重启策略硬编码与 container 事件丢弃、D-4 的四环证据链、P1-2 的 lynx `WithTimeout` 语义经 `go doc` 官方文档证实、P1-14/P1-15/Q-10/Q-15 的源码复核），P2 抽样核验约 15 处零误报。
- 两处子审标"未验证"的运行时行为如实保留标注：swarmkit 对 Secrets 仅顺序差异是否触发 task 重建（P1-14）、swarm 跨服务 alias 的 DNS RR 行为（R-3）、k3s Enrollment 轮换对存量 agent 的影响（裁决表 #1）。建议分别以 dind e2e 实证后关闭。
- 与既有 ADR 的冲突处理：ADR-0022"命令对照"（C-11，以 ADR 为准、实现缺口）；ADR-0003"契约类型全在 proto"（C-14，本审认为 ADR 措辞该改，已列裁决项）；其余发现不与 ADR 冲突。
- 干净面（已查无问题，防后续重扫）：事务内无外部调用、rows.Close/rows.Err 纪律、webhook HMAC 恒时比较、secret 生命周期（age 信封/KEK 0600/值不进日志）、bootstrap token 双锚、token 材质、CLI 契约（退出码/render 单点/150 golden）、注册表三链咬合、authz fail-closed 链、Loop 骨架唯一性、日志脱敏、SQLite 配置、测试金字塔分层与断言强度。

---

## 10. 机制缺口分析（2026-10-01 二补：真缺陷为什么会穿过防线）

对 §8.A 的 10 组真缺陷跑机制缺口排查：逐组还原逃逸路径（问题途经了哪些本可拦截的层、为何失守），判类别（个案/一类），机制按**元类**归并配给——同一元类的实例共享一个守卫。本仓守卫文化（架构 §11）是现成底盘：eventcode usage 反扫、irguard 对 ADR-0001 黑名单的执法都是正例，说明机制不是没有，是**没制度化为"每个承诺/契约面配守卫"**。

### 10.1 元模式与逃逸路径

**M-1 平行真源漂移**（实例：A-9 gateway 清单、A-8 ADR-0022 缺 Command、C-14 ADR-0003 措辞、D-2 taskGroup 注释——已四例）
逃逸路径：gateway 注册清单是手维护的平行列表，proto 注解是契约真源，二者无对账检查，CI 只验 openapi 生成不验挂载 → 注解加了、挂载漏了，红灯永不亮。ADR 面：ADR-0001 有 irguard 执法（正例），ADR-0022 写了"镜像/副本/命令"却没有验收锚也没有守卫，同批自裁自实现无对账 → 承诺缩水不可见。注释面：`translate.go:148` 声称"engine 已翻译"、`builder.go` 注释声称"回 queued 重放"、`grpc.go:20` 注释把关停超时当请求超时——**注释即契约的文化下，错注释比没注释更糟，而注释无执法**。
类别：一类（四例在案）。

**M-2 契约面覆盖不对称**（实例：A-2 StreamLogs follow、A-6 DeleteProject、P2 尾巴的 drain/Follow 缺测试——后者是**同类第二例**，已被识别过但没机制化）
逃逸路径：假底座注释诚实写"Follow 不实现"（harness.go:322-324），但没有任何机制让"fake 未实现的面 = 生产面零测试覆盖"这件事可见；Delete* 的收口测试只有 App 有（三道复查+竞态风暴，很强）、Project 零覆盖——同构操作面测试强度随机取决于谁先被验收盯着。
类别：一类（有再犯证据）。

**M-3 进程内状态的恢复声明无验证**（实例：A-1 buildInputs；同类已修先例：ADR-0022 的重启缓存清零=rebuildBaselines，N0 已为此付过修复成本）
逃逸路径：结构层允许（内存 map）→ 注释声称恢复路径（"重启丢失即回 queued 重放"）→ 该恢复路径**从未被任何测试走到**（单测无"重启"形态；e2e 无故障注入腿）→ 运行时振荡无信号（唯一的痕迹是审计行无限膨胀，无人看）。
类别：一类（第二例）。

**M-4 确定性不变式无执法**（实例：A-3 secrets 遍历序；同函数内 Env 排序是正例）
逃逸路径：类型层无约束（Go map 遍历序随机）→ 无"同输入两次翻译逐字节相等"的确定性测试 → drift e2e 是自对照（对照的缓存 spec 与下发 spec 同源），照不出翻译非确定性 → 显形需要"多 secret + 重放"的特定组合。
类别：一类（不变式已被自己的注释声明，"幂等 diff 稳定"，但只在一半字段上执行）。

**M-5 跨层限额隐式默认**（实例：A-5 webhook 25MiB/4MB；将来同类面：F1.10 上传上限、F1.2 SSE 消息上限）
逃逸路径：gRPC 4MB 是**隐式默认**（不写即存在、无真源可引用）→ 两层上限常量分居两文件互不感知 → 无跨层上限测试。
类别：一类（N1 还会新增三个同类面）。

**M-6 错误契约字符串化与吞错**（实例：A-10 主体）
判**个案群，不配新机制**（机制过敏）：Q-12/Q-13（错误分类靠 Contains/文案一律化）成因同 M-1 的"契约无类型化"，并入守卫 B；Q-8/Q-24/Q-19/Q-20/Q-9（吞错降级/误报）成因分散、单点修复便宜、注入式错误夹具性价比一般——随批 0 修复，仅把"期望错误显式判（errors.Is / isNotFound），其余上抛"补进 AGENTS.md 编码约定（明示这是弱层）；Q-21 并入 D-3 幂等批（去重与效果同事务）；Q-6/Q-7 生命周期卫生修复即止。

### 10.2 机制补齐方案（每条指认拦截层 + 验收）

| # | 机制（层） | 拦截的元类 | 验收：下一个同类问题在哪被拦 |
|---|---|---|---|
| 守卫 A | **gateway 挂载面 vs proto 注解面对账守卫**（guards 新测试：解析 `*.pb.gw.go` 或 proto 注解枚举带 http 面的服务，断言 assembly 注册清单一一对应——usage 反扫同款） | M-1（REST 契约漂移） | "注解加了、忘挂载"在 CI 守卫层红，列出缺口服务名 |
| 守卫 B | **api 层禁 `strings.Contains(err.Error(), …)` 错误映射**（guards/scan 加一条规则；哨兵错误是既有正例） | M-1/M-6（错误契约字符串化） | 下一个靠文案匹配的跨层错误分支在守卫层红 |
| 守卫 C | **契约面覆盖反扫**（枚举 Runtime 接口方法×子面×关键旗标（Follow 等）与全部 `Delete*` RPC，断言 apitest/engine 测试中存在对应用例——fake 未实现的面必须显式登记豁免并带理由，双向保鲜） | M-2（覆盖不对称；吸收 drain/Follow 挂账） | 新增 Runtime 面或 Delete RPC 而无对应用例时 CI 红，列缺口名 |
| 测试形态 D | **crash-recovery 测试类别**（hermetic 夹具支持：真 SQLite + 新 Engine 同库重启 + 有限拍内断言确定终态；build 线第一条：孤儿 build 必须离开 queued/building 到达终态，禁止振荡。此后每个带进程内状态的线必配） | M-3（恢复声明无验证） | 下一个"内存态 + 注释声称恢复"的缺陷在 recovery 测试层红 |
| 确定性测试 E | **翻译纯函数确定性**（同输入 Workload——多 secrets/networks/volumes/ports——两次 `toServiceSpec` 序列化逐字节相等） | M-4（确定性无执法） | 下一个把无序集合直写载体 spec 的改动在该测试红 |
| 限额显式化 F | **单一 `grpcMaxRecvMsgSize` 常量为真源**（装配显式设置；启动断言 `hookPayloadLimit ≤` 它；apitest 注入 5MB payload 断言走设计 413 路径；F1.10/F1.2 新上限引用同源） | M-5（隐式默认错位） | 下一个跨层限额错位在启动断言/测试层红 |
| 流程 G（弱层，A/B 兜底） | ADR 模板补**验收锚**小节：可测承诺逐条列为可勾选项；AGENTS.md 补一句"ADR 含可静态执法的承诺，须同批开守卫任务（ADR-0001→irguard 为范式）" | M-1（ADR 承诺缩水） | 评审层（弱）；硬兜底是承诺类照 ADR-0001 范式配守卫 |

### 10.3 力度裁量（防机制过敏）

- **配机制的**：M-1/M-2/M-3/M-4/M-5——均为一类且多数有第二例在案（M-2 的 drain/Follow 挂账、M-3 的 rebuildBaselines 先例、M-1 的四例），守卫形态全部复用仓内既有模式（usage 反扫/双向保鲜/irguard），边际成本低。
- **不配新机制的**：M-6 吞错群（个案性强、注入夹具性价比低，修复+约定一句即止）、Q-6/Q-7 卫生项（修复即止）、容量类 P2（Q-22/23/26/E-11——等 N1 压测锚点，现在上监控是过敏）。
- **补丁姿态**：批 0 的行为修复（A-1 driveBuilding 幂等重建输入等）是根修不是止血；守卫 A~F 与对应行为修复**同批落地**（新测试与功能同批是 AGENTS.md 既有纪律），不出现"补丁打完问题关闭"形态。

### 10.4 与 §7 批次切分的关系

守卫 A/B/C/E/F + 测试形态 D 全部归入**批 0**（与其拦截对象的行为修复同 commit），流程 G 随批 0.5 的 ADR 惯例修订。批 0 的定义因此从"修复十个缺陷"升级为"修复十个缺陷 + 关闭五个逃逸元类"——后者才是这十个发现里可复利的部分。
