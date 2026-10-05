# 竞品架构深潜对比报告——2026-10-03

与 `2026-09-30-competitive-deep-dive.md`（DX/产品功能面）互补，本文是**架构与实现面**的深潜对比：六家竞品源码（dokploy/coolify/caprover/tsuru/porter/zane-ops，均在 `D:/Codes/paas/`）各一路深潜 + fleetly 实现现状一路盘点，七路并行、结论均带代码路径证据。视角：项目在开发阶段，**从最优设计出发、不考虑向后兼容，有硬理由可推翻既有决策**。

---

## 1. 一页结论

1. **fleetly 的八根结构支柱全部获得竞品反向验证，无一需要推翻。**六家竞品的架构伤痕恰好逐条落在 fleetly 立柱的位置：dokploy 把 Swarm 语义焊进 schema（198 个迁移）、coolify 长出 5806 行 God job、tsuru 词汇漂移（unit→pod 翻译层）、porter 进程外插件全方位成本、zane 三状态存储双真源漂移、caprover read-modify-write 活 Spec。这些不是风格差异，是六条独立演化路径各自撞出来的同一批墙。
2. **"状态 CAS+tombstone+Outbox+审计同事务四件一拍"是六家全缺的能力，且每家都为此付出了看得见的代价**：dokploy 内存队列重启丢 job、coolify 崩溃窗口留下永久 QUEUED 行、zane 取消信号静默丢失 DB 停在 CANCELLING、porter 自己注释承认"no built-in optimistic concurrency"。
3. **zane-ops 是对"要不要上工作流引擎"的终审样本**：11 个容器、约 3.5C/4.5G 静态足迹、三个状态存储、双轨 DTO、手写心跳/补偿/取消监控——换来的部署流本质是"12-14 步线性序列"，这正是手写单写者循环的舒适区。结论：暂停/审批成为产品功能、数百并发编排、跨编排器 saga 之前，外部 workflow 引擎不划算。
4. **tsuru 用 13 年验证了一个 fleetly 尚未做的高级形态：事件文档同时是锁、审计、授权载体**（Kind=PermissionScheme，行级可见性在查询层完成）。这是六家对手最值得敬畏的单一资产，与 ADR-0037"事件/审计声明面维持现状"裁决构成张力——本文裁为观察项（§6.3）。
5. **最值得立即偷的是三件测试形态**：dokploy 的升级矩阵集成测试（对刚被 staging 咬出血的升级链是直接解药）、dokploy 的执行型注入守卫（真 shell 执行 payload 断言，比 golden 硬）、porter 的 re-exec 假命令测试法。
6. **两个真实的增量 ADR 候选**（非推翻）：① App 部署策略面——rolling 默认 + bluegreen 可选（zane 的 per-deployment slot 别名 + Proxy 切换是成熟参照，fleetly 的进程网络别名 ADR-0034 已有基建先例）；② Token 语义补强——"token 权限随 creator 实时收窄 + fail-safe scope 门"（zane 模式）。
7. **对手的教训对 fleetly 的唯一结构性警示：警惕我们自己长出 God module。**fleetly 的 `Engine` 结构体汇聚 20+ repo、9 个域子结构、3 把锁；coolify 的 God job 就是从"部署先跑起来"开始长出来的。守卫体系管住了 import 方向，还管不住**体积聚集**——这是守卫文化下一步的执法面。

## 2. 总体形态对比矩阵

| 维度 | dokploy | coolify | caprover | tsuru | porter | zane-ops | **fleetly** |
|---|---|---|---|---|---|---|---|
| 语言/形态 | TS Next.js 单体 | PHP Laravel+Livewire | TS Express | Go 单二进制 | Go 二进制家族 | Python Django+Temporal | Go 单二进制 |
| 状态存储 | 外部 PG，198 迁移 | 外部 PG+Redis，422 迁移 | configstore JSON 单文件 | Mongo，零事务 | Mongo 形状插件协议 v3 | 业务 PG+Temporal PG+Redis | **内嵌 SQLite，18 只前滚迁移** |
| 部署执行模型 | 内存队列 + bash 拼串 | Redis 长驻 job（一部署占一 worker 至 1h） | promise 链 + 全局互斥 | 同步 in-request（HTTP 连接占全程） | 同步两段式 CLI | Temporal workflow（15 个 wf/60 个 activity） | **单写者收敛循环 + 四件一拍同事务** |
| 契约面 | tRPC + 社区 OpenAPI fork | swagger-php 注解手写（~1MB） | 无 schema，Postman 即文档 | 注释 yml + 手写 swagger 6915 行，双源 | cobra CLI 为主，proto 是 3 RPC 补丁 | DRF + drf-spectacular 生成物 | **proto 唯一契约源（gRPC+REST+OpenAPI+TS）** |
| 运行时抽象 | 无（dockerode 散布 16 文件，104 处 `if(serverId)`） | 无（拼 shell + fork ssh） | 无（dockerode 过程式封装） | 17 可选能力接口 + 运行时类型断言 | 进程外插件（fork go-plugin，365 行生命周期管理） | 无（docker-py 散布） | **编译期 7 端口 + 注册表 + SDK 只进 providers** |
| 依赖治理 | 无（biome 只管风格） | 无（Model 反向 import Livewire） | madge 只防循环 | 无 | 无（循环依赖注释自嘲） | 无 | **27 个守卫测试（import/词汇/序列/注册表/面对账）** |
| 一致性 | 多步 UPDATE 无事务 | 准入事务（行锁）但执行态无事务 | JSON 整文件 read-modify-write | 唯一索引当锁 + hash 乐观锁（局部） | 无 CAS（TODO 自白） | 三存储人肉同步，无 reconciler | **CAS 单写者，同事务原子** |
| 测试 | 116 文件（含执行型注入套件），CI 真集成 | 883 文件 12.4 万行，**CI 不跑** | 31 文件全 mock | 231 文件 + 真集群 integration | 951 函数三层 + golden | 33 文件 + 时间跳跃环境 | 单测+apitest(bufconn)+e2e(dind)+264 golden |
| 部署足迹 | 1 巨镜像 + PG | 3 容器（s6 多进程） | ~4 swarm service | 开发环境 6 件套 | CLI + mongo 服务器 | **11 容器 / ~3.5C 4.5G** | 1 二进制 + 按需受管组件 |

一个总观察：**六家没有一家同时做到"内嵌存储 + 类型化契约 + 依赖治理执法"三件**。fleetly 的结构性投入（spec/capability/守卫）在六家对照下不是过度设计，而是这个品类在 1~10 万行规模后的存活前提——coolify 17 万行、dokploy 4.6 万行 server 包、zane 11.6 万行时，无治理的代价都已显性化。

## 3. 八大支柱的竞品验证（逐条：我们的设计 ↔ 伤痕证据）

### 3.1 IR 编排器无关（ADR-0001/0002）——强验证

- dokploy 把 `HealthCheckSwarm/PlacementSwarm/UpdateConfigSwarm` 直接做成 PG 列（`packages/server/src/db/schema/shared.ts`），换运行时要动 schema+198 迁移；application 宽表 554 行 100+ 列内联四家 Git provider 字段。
- tsuru 基础 Provisioner 接口的 `AddUnits/RemoveUnits/Units` 把容器词汇冻结进接口，K8s 化后长出 `podsToUnitsMultiple` 翻译层 + CrashLoopBackOff 特判（`provision/kubernetes/provisioner.go:695-818`）。
- caprover `DockerApi.updateService`（`src/docker/DockerApi.ts:1362-1675`）以 Docker 现场态为基准 read-modify-write，需要 `uuid` label hack 强制更新。

**裁决：维持。spec 纯度的守卫（irguard 编排器字段黑名单）是六家教训的集中免疫。**

### 3.2 单写者循环 + 四件一拍（ADR-0024 及 transition 收口）——六家全缺，独家能力

- dokploy：队列在内存（`apps/dokploy/server/queues/in-memory-queue.ts`），web 重启丢等待 job；状态多步 UPDATE 非事务，崩溃靠启动扫表把 running 改 cancelled。
- coolify：`ApplicationDeploymentQueue::create` 提交后才 dispatch（`bootstrap/helpers/applications.php:66-117`），崩溃窗口留下永久 QUEUED 行，靠 `cleanup:stucked-resources` 定时兜底。
- zane：取消信号 `except RPCError: pass`（`temporal/client.py:157-158`），DB 停在 CANCELLING；无 stuck 扫描 reconciler；view 直改 CANCELLED 可被迟到 workflow 覆写为 HEALTHY。
- porter：`SaveOperationResult` 三次独立写靠 multierror，代码注释自白 "no built-in optimistic concurrency"（`pkg/storage/installation.go:303-307`）。

**裁决：维持并继续加码——transitionguard 的"手搓四件一扫描"是六家对手最需要的那个测试。**

### 3.3 proto 唯一契约源（ADR-0006）——强验证

- tsuru 双源：注释 yml（`make check-api-doc` 门禁）+ 6915 行手维护 swagger（`docs/reference/api.yaml`），TS 客户端手写。
- coolify：swagger 注解手写模型字段（`#[OA\Schema]`），无 breaking 检测。
- caprover：数字状态码信封永远 HTTP 200，Postman 集合当文档，CLI 异仓漂移。
- zane：OpenAPI 是 drf-spectacular 生成物不是真源。
- porter：proto 只有 3 个只读 RPC 且 ldflags 门控，转换走 `json.Marshal→protojson.Unmarshal` 有损双跳。

**裁决：维持。buf breaking 门禁 + golden 双形态 + `--json` 全命令覆盖的完整性继续守住。**

### 3.4 编译期 Capability 端口（ADR-0003）——porter 成本账反向验证

porter 为进程外插件付出：fork 版 go-plugin 带四个未上游补丁、插件协议 v3 还兼容 v1 双协议、365 行连接生命周期管理、**连默认 mongodb 存储都要 spawn 自身再 gRPC 握手**（`pkg/plugins/pluggable/connection.go:96-102`）、客户端拿 `interface{}` 断言丢编译期检查。

dokploy 的 104 处 `if (serverId)` 本地/远程双分支（含 `writeTraefikConfig`/`writeTraefikConfigRemote` 成对函数）是"没有执行端口"的直接代价。

**裁决：维持。且 porter 提示了未来抽进程外插件时的一条军规：进程内默认实现快路径必须保留。**

### 3.5 内嵌 SQLite（架构 §1）——形态优势确认

porter 单用户 CLI 却要求 Mongo 服务器（存储接口是 Mongo 方言、bson 泄漏进插件协议——"接口形状被首个后端绑架"的警示）；tsuru/zane/coolify/dokploy 各拖 1~2 个外部有状态依赖。caprover 的 configstore JSON 是"无数据库"路线的极限形态（无事务、无并发、整读整写）。SQLite WAL + goose 是"零外部依赖"与"真事务"的唯一交集。

**裁决：维持。`IsUniqueViolation` 文本匹配等驱动妥协面已知、已注释钉死。**

### 3.6 守卫文化（架构 §11）——六家零执法的反面群像

六家竞品的 import 治理全是零（caprover 仅 madge 防循环）；coolify 12.4 万行测试**CI 不跑**（workflow 只有构建发布）——"守卫不在门禁上红等于没有"的最强实证；zane 3700 行单文件 models + 350 个迁移的改名史；porter `pkg/manifest/manifest.go` 1750 行。

**裁决：维持。但守卫体系有盲区（见 §6.2 候选 G）。**

### 3.7 Runtime 窄面 6 方法（架构 §5）——tsuru 教训的再校准

tsuru 当前已 ISP 化（基础 12 方法 + 17 可选能力接口，运行时类型断言发现能力）——方向与 fleetly 的"基础面 + 可选子面"同构，证明拆分方向正确；其教训有二：能力断言无编译期保证（运行时才报 ProvisionerNotSupported）、多 provisioner 终局是只剩一个实现却背着全套注册表（docker→K8s 迁移完成后**抽象没有收缩**）。

**裁决：维持。补一条长期纪律：若 N4 k3s 试点后 Runtime 抽象兑现完战略价值，要敢于收缩而非永续背负。**

### 3.8 不上外部 workflow 引擎——zane 终审样本

zane 为 Temporal 付出：11 个部署单元、~3.5C/4.5G 预留、debug_mode=True 关沙箱、Django ORM 连接排异拦截器、双轨 DTO（DB 模型→DRF→快照→dataclass 四张皮）、2400 行测试基建；换来的部署流是"12-14 步线性序列 + 条件分支 + 手写补偿"——手写循环的舒适区。**其引入没有消除双真源，只是把"进程内存 vs DB"变成"Temporal 事件史 vs DB"，且失去一事务四件一拍的可能。**

划算阈值（zane 样本反推）：① 暂停/审批成为产品级人机协作功能（可挂起数天）；② 数百并发编排实例需 worker 池横向扩展；③ 跨节点/跨编排器分布式 saga；④ 用户级执行历史审计成为卖点。当前一个都不成立。

**裁决：维持（无 ADR，本报告即裁决依据）。**

## 4. 值得偷的架构决策清单（按落点排序）

### 4.1 立即可做（guards/测试形态，不动物件）

| # | 决策 | 来源与证据 | fleetly 落点 |
|---|---|---|---|
| G1 | **升级矩阵集成测试**：CI 生成"旧稳定版→新版"配对，验证升级期间用户负载（PG/双 web/静态站）存活 | dokploy `.github/workflows/upgrade-integration-test.yml` | N2 升级数据安全修复批的验收形态（ADR-0015 的"零重启零中断"从真机 staging 走向 CI 常态）；staging pg WAL 事故的系统性防线 |
| G2 | **执行型注入守卫**：把生成的命令放进真 shell 跑恶意 payload，用标记文件断言未触发 | dokploy `__test__/compose/compose-command-injection.test.ts` 等 | guards 新增：凡生成 shell/SQL/配置的地方（join 材料、goose、traefik toml、dbtemplate BackupCommand——N2 落地前就把测试位占住） |
| G3 | **re-exec 假命令测试法**：测试进程自我 re-exec 成假命令，按环境变量断言收到的命令行 | porter `pkg/test/helper.go:24-66` TestMainWithMockedCommandHandlers | exec 子面/备份链（外部进程调用面）落地时的 golden 化手段 |
| G4 | **单机假设审计**：zane 的单机假设渗透到健康检查（读本节点容器 DNS）、端口探测（busybox 绑 0.0.0.0）、volume 测量 | zane `temporal/helpers.py:71-83`、`main_activities.py:1629-1647` | 一次性审查任务：列出 fleetly 全部隐含"控制面所在节点"的代码路径（构建是显式裁决 ADR-0019 除外），入 e2e 双节点矩阵逐项覆盖 |

### 4.2 N2 批次吸收（已有排期，补强形态）

| # | 决策 | 来源与证据 | fleetly 落点 |
|---|---|---|---|
| N1' | **digest 确定性纪律**：投影结构体全字段排序后再哈希（注释明确仅按 Name 排序会跨调用漂移）、带算法前缀、免重解析 | porter `pkg/storage/run.go:282-320` ParametersDigest | AppSpec digest/漂移检测（drift 环 ADR-0022）与 dbtemplate 镜像 digest 钉定（DT-9）实现时照此 |
| N2' | **check-strategy 三细节**：warnOnly 二元组语义、区间共存（资源级独立版本比单一全局 schemaVersion 便宜）、检查结果缓存 | porter `pkg/schema/check-strategy.go`、`migrations/init_cache.go` | schemaVersion 演进策略（ADR-0002 落地细节） |
| N3' | **准入=显式协议**：准入返回结构化结果（queue_full/skipped/queued + 既有 deployment 引用），UI/API/webhook 三入口行为一致 | coolify `bootstrap/helpers/applications.php:34-64` | ADR-0016 已有语义，对齐**返回形态**：受理侧响应体把排队位置/去重命中显式化（Agent 可判定） |
| N4' | **工具链=可版本化容器**（helper 容器模式）：构建工具封容器、控制面只发指令，升级工具链不动控制面 | coolify `prepare_builder_image`（`ApplicationDeploymentJob.php:2657-2703`） | N2b 构建多节点/独立构建机批次的天然形态；当前内嵌 buildkit 的单二进制红利吃完前不动 |
| N5' | **错误只打一次 + 进程级脱敏**：log.Error 只进 span 不打印、CensoredWriter 包装全部 stdout 实时替换敏感值 | porter `pkg/tracing/traceLogger.go:67-70`、`pkg/portercontext/context.go:438-474` | 构建日志面（N2b Logging Provider 批）与 exec 子面的输出纪律 |

### 4.3 信任与安全批（与 N2 数据安全修复批同族）

| # | 决策 | 来源与证据 | fleetly 落点 |
|---|---|---|---|
| T1 | **token 权限实时收窄**：token 有效权限 = min(creator 当前权限, token 声明)，creator 被降权立即生效；token 永不超过 creator | zane `zane_api/permissions.py:106-159` | ADR-0035 行级授权的自然补全（当前 token 吊销链已有，收窄语义核对该批） |
| T2 | **fail-safe scope 门**：view/处理器忘声明 required_scopes 则一律拒绝（默认关闭而非默认开放） | zane `permissions.py:254-273` | API 写面守卫补一条：新增 RPC 未声明 scope 即红（与 idem 覆盖反扫同构的"注册表只增+反扫"） |
| T3 | **token 三级 ability**（read/write/write:sensitive）：敏感字段可见性按能力 makeVisible/makeHidden | coolify `app/Http/Middleware/ApiAbility.php` | Secret/Config 读取面的粒度候选——`secret:read` 是否应再分"读引用"与"读明文"，进信任批 ADR 讨论 |

### 4.4 演进候选（开 ADR 再动）

| # | 决策 | 来源与证据 | fleetly 落点 |
|---|---|---|---|
| E1 | **App 部署策略面：rolling 默认 + bluegreen 可选**。每个部署独立载体（`srv-{app}-{hash}`）+ slot 网络别名（`{proc}.{blue|green}`）+ Proxy upstream 切换；回滚=指回旧 slot 零重建；先验后切 | zane `temporal/helpers.py:157-162`、`models/main.py:1823-1831`（slot 交替）、Caddy ETag read-modify-write（`temporal/proxy.py:308-352`） | **增量 ADR 候选**（§6.2）。fleetly 已有的基建：进程网络别名（ADR-0034）、Route 发布行集指纹、Revision Replay。注意蓝绿对有状态负载的数据分叉问题——数据库轨不适用（数据面另有 stop-first 裁决） |
| E2 | **Proxy 配置"生成→校验→激活→失败回滚"结构化**：写临时文件→原子 rename→校验→热载，校验失败自动回滚上一份配置与 DB 定义 | caprover `LoadBalancerManager.ts:104-241`（.fut→rename→nginx -t→HUP→失败回写） | traefik HTTP provider 形态下"控制面是真源"已天然免疫配置丢失；可偷的是**发布前校验**（traefik 配置 dry-run 校验端点）进 Route 发布步 |
| E3 | **事件=锁=权限三位一体**（观察，见 §6.3） | tsuru `event/event.go:752-760,294-320` | — |
| E4 | **存储契约测试套件**：一套行为测试钉死任何存储实现 | tsuru `storage/storagetest` | 仅当出现第二存储后端诉求时启用；SQLite 单后端下 YAGNI |

## 5. 对手最值得敬畏的资产（不偷但要知道差距）

1. **tsuru 事件系统**：lock-as-data（sparse unique 索引即锁）+ 30s 心跳租约 + 过期回收 + 节流 + 维护窗（change freeze 的成熟形态）+ Kind=PermissionScheme（授权/审计/可见性同源一份数据模型，行级过滤在查询层）。fleetly 的对应物分散在 freeze/audit/outbox 三处且相互不认识。
2. **zane 三维修权**：workspace 角色 × 项目集 × token scope 一次算成 frozen EffectiveAccess 挂 request，119 处调用点零重复计算。
3. **porter 契约演进纪律**：Stamp（产物内嵌 manifest+工具版本+全文件哈希，判旧与可还原兼得）——build 产物自描述是 fleetly build 元数据面的下一步参照。
4. **coolify 准入事务**：用 `lockForUpdate` 锁稳定行（application/server）制造串行点——"空队列也有锁目标"是对 fleetly 单写者模型的补充视角（我们天然单写者，但跨进程未来多实例时的备选）。
5. **dokploy 升级矩阵 CI**：把"升级不弄坏用户的东西"做成 CI 常态而非发布检查单。

## 6. 对既有决策的重审（挑战过→裁决）

### 6.1 挑战过、维持的项（含挑战理由与驳回依据）

| 既有决策 | 挑战理由 | 驳回依据 |
|---|---|---|
| 单实例控制面（HA 延后，架构 §8） | tsuru 多实例 + lock-as-data 证明 DB 互斥可行 | 六家无一以轻量形态做到控制面 HA；tsuru 的多实例以 Mongo 副本集+Redis+多组件为前提；定位（小微团队单环境 prod）不变则需求侧不成立 |
| 环境实体砍掉（ADR-0011） | 三家竞品都有 Project→Environment | DX 报告已终裁（成本全体付收益归少数）；本次架构面未出现新反证——dokploy/coolify 的环境模型都是 schema 复杂度的主要来源之一（宽表/JSON 列分层） |
| Agent 面=CLI+Skills 不做 MCP（ADR-0006） | coolify 已内建 laravel/mcp 20+ 工具 | 单点反证不改裁决：Railway 归档 MCP server 并入 CLI 的结构性证据（DX 报告 §5）更强；coolify 的 MCP 恰是 UI>API>MCP 三级衰减的延续 |
| 构建恒在控制面节点（ADR-0019） | zane 每环境独立 buildx builder、coolify 构建服务器角色 | 两者都以多机运维为前提；fleetly 的升级路径（N2b 构建容器化 → 远期构建机）已排好序，当前内嵌 buildkit 的单二进制红利真实存在 |
| swarm 默认 Runtime | dokploy Swarm 口碑一般、zane 名义 Swarm 事实单机 | 口碑差恰是"Swarm 用得糙"（dokploy 远程执行拼 shell、zane 单机假设写死）；fleetly 的节点锚定/材料分发/期望集 Addresses 是把 Swarm 当一等 Runtime 认真做的对照；Docker29 坑清单已在仓 |

### 6.2 增量 ADR 候选（建议开 ADR，属新增非推翻）

- **候选 A（部署策略面）**：App 部署可选 bluegreen（per-deployment 载体 + slot 别名 + Proxy 切换）。动机：零停机后进者被罚是行业公认痛点（DX 报告）；staging 升级事故证明"滚动替换对启动慢/有状态倾向的负载是危险默认"；fleetly 的 Revision Replay + 网络别名基建使边际成本可控。范围：仅无状态 App 进程；数据库轨维持 stop-first+宽 grace 裁决。
- **候选 B（token 语义补强）**：T1 实时收窄 + T2 fail-safe 门 + T3 三级 ability 讨论，一个 ADR 收口。
- **候选 C（守卫下一步：体积执法）**：当前守卫全部执法"方向"（import/词汇/单源），不执法"聚集"。coolify 的 God job（5806 行）、zane models（3700 行）、porter manifest（1750 行）都是从功能正确的代码长出来的。建议：guards 增加文件级规模红线（如单文件非生成物 >1200 行即红，例外白名单带理由）——把"每段只许有一份"的既有包规则延伸到"每段不许长成怪物"。

### 6.3 观察项（不动，但挂重开触发器）

- **事件/审计/授权同源**（tsuru 模式） vs ADR-0037（2026-10-03 刚裁决维持现状）。重开触发器：① Console 批次需要事件流做行级可见性过滤时；② 引入跨团队共享 Project 的真实需求时。tsuru 证据（`event.go:752-760`）已入本报告存档。
- **Engine 结构体域汇聚**（盘点线索 1）：20+ repo + 9 域子结构 + 3 锁。候选 5 机械批已缓解，但"跨域共享 observDomain 是所有域写热点"提示下一步拆分方向是**观测域垂直切**而非继续平铺。与候选 C 同批考虑。
- **canonical JSON 手工剥离 9 类归一化产物**（swarm no-op 断路器的等价判定）：与 daemon 方言耦合最深的点，docker 大版本升级必重验——已有注释钉死，列此存档提醒。

## 7. 行动建议（按批次归位）

1. **立即**（可与 N2 开工同批）：G1 升级矩阵 CI 骨架（先钉"单节点 dind 升级前后 Workload 存活"最小形态）；G2 注入守卫占位；G4 单机假设审计清单。
2. **N2 数据安全修复批**：N1' digest 纪律、E2 Route 发布前校验、G1 全量形态（对齐 ADR-0015 验收）。
3. **N2 信任面**：候选 B 一个 ADR 收口 token 语义（T1/T2/T3）。
4. **N2b 观测自宿批**：N5' 错误/脱敏纪律随 Logging Provider 落地。
5. **N3 前**：候选 A 部署策略面 ADR（蓝绿先验后切是 Console"部署详情页"的高价值叙事）。
6. **随守卫批**：候选 C 体积执法红线。
7. **长期纪律**：Runtime 抽象在 N4 兑现战略价值后敢于收缩（tsuru 教训）。

---

## 附：调研方法与证据源

七路并行子调研（2026-10-03）：六竞品各一路源码深潜（dokploy v0.30.6 canary / coolify v4 Laravel 12 / caprover HEAD a45cce6 / tsuru 1.23.1 / porter 主干 / zane-ops HEAD b494431e）+ fleetly 实现现状盘点（N0+N1 完成态，HEAD 5f9538a 附近）。本文引用的竞品路径均为各子报告原始证据，引用格式为竞品仓内相对路径；fleetly 侧引用为仓内路径。产品功能面结论（promote 空白、模板生态、市场口碑等）见 `2026-09-30-competitive-deep-dive.md`，本文不重复。
