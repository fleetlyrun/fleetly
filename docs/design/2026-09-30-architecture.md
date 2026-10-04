# fleetly 架构设计（2026-09-30）

配套文档：`2026-09-30-domain-model.md`（领域模型）、`../adr/`（决策记录）。本文回答"代码长什么样、边界在哪、插件怎么插"。**2026-09-30 约束面重置（ADR-0010）后：硬约束只剩定位（轻量 PaaS、人类+Agent 一等）、torchwood 一期 dogfooding、DX 优先三条；文中"继承"字样的条款均为参考默认，待竞品深调以 DX 透镜逐项重估。**

## 0. 旧项目诊断 → 结构修正对照

| # | 归档项目根本失误 | 本设计的结构性修正 |
|---|---|---|
| 1 | 把 Swarm 的形状当成领域模型（命名公式/label 契约/ServiceSpec 投影住在 engine） | ADR-0001：领域与引擎零编排器概念；IR（Spec）为唯一边界；载体命名与标记是 Provider 私有 |
| 2 | 共享原语靠拷贝（6 份 Docker client、6 份循环骨架、6 个组件部署器、4 份收敛链） | ADR-0004：唯一通用 ManagedProvider reconciler + 唯一循环骨架；"每段只许有一份"入包规则 |
| 3 | 包边界与依赖方向脱节（engine=2.1 万行共享内核，state=2.9 万行门面） | §3 分层树 + 叶子包规则 + 守卫测试在 CI 执行（架构由测试承载，不靠文档纪律） |
| 4 | 文档真源与代码真源漂移（需要 impldocscan 抓"记录虚报"） | 契约进代码：常量与谓词同文件、注册表 + golden + usage 反扫；CONTEXT.md 只当词汇表 |
| 5 | 机制先于词汇（十余次全库更名） | ADR-0007 词汇冻结：更名即设计缺陷；词汇表先于第一行代码（本次已做到） |

## 1. 总体形态

- **单二进制 `fleetlyd`**（控制面）+ **单二进制 `fleetly`**（CLI，瘦客户端，只走 API，无本地特权操作）。
- 存储：**内嵌 SQLite（WAL）+ goose 迁移**，无外部数据库依赖（继承；换库不是插件面，控制面存储属于平台私有实现）。
- Console：React SPA，构建产物 embed 进 `fleetlyd`，**只消费公共 REST/WS API**，无私有服务端面（旧 P0-3 教训）。
- 部署形态双轨等价：原生 systemd 二进制 或 容器形态（host 网络 + bind 数据根 + 镜像内 docker-cli，继承旧 ADR-0013 三适配）。
- 工作节点**零平台安装物**：加入材料由 Runtime Provider 生成（swarm=join 材料；k8s=节点 kubelet 既有；nomad=client 配置）。Node 身份锚定 Provider 载体上的平台 ID 标记，`nodes` 表只是观测缓存（继承）。
- 平台对回拨型 Workload（日志/指标采集、exec 中继等）一律**物化控制面地址进环境**（`FLEETLY_CONTROL_GRPC_ADDR` + TLS 名），不依赖 DNS；端点 scheme 用社区约定 `grpc://` / `grpcs://`（继承）。

## 2. 分层与依赖规则

```
cmd/fleetlyd/            # 装配入口（wire 在 internal/assembly：注册 Provider、选 Capability、启动引擎）
cmd/fleetly/             # CLI（API 客户端；全部动词经 internal/api 的 proto 契约）
internal/spec/           # IR：AppSpec/TaskSpec/DatabaseSpec + schemaVersion + 归一化 + 校验（叶子，纯类型）
internal/model/          # errcode/eventcode 注册表（叶子，纯类型；实体与状态机随 N0 实况落在 spec/state/engine）
internal/state/          # SQLite 持久化 + Outbox。按聚合分包（project/ app/ deployment/ build/ …），
                         #   禁止门面 store.go；每个聚合包自己的 repo；statetest/ 是 hermetic 夹具
internal/capability/     # Capability 端口：Go interface，消费 spec 与 model 类型（叶子接口层）
internal/providers/      # Provider 实现：swarm/ traefik/ dockerbuild/…
                         #   全仓唯一允许 import 编排器/基础设施 SDK 的地方
internal/engine/         # 单写者收敛循环：deployment/ build/ managed/ drift/ teardown/ + 共享循环骨架（唯一一份）
internal/identity/       # 身份域内核（角色/Token/邀请的纯类型；叶子）
internal/authn/          # 身份执法应用层（种子、Bootstrap、拦截器 scope 执法）
internal/material/       # Secret 信封加密（age；指纹与 Cipher）
internal/config/         # fleetlyd 运行配置（config.proto → config.pb.go）
internal/api/            # gRPC 服务 + gateway REST + apperr 信封（fleetlygrpc/ systemgrpc/）
internal/apitest/        # bufconn 全链测试夹具（引擎真库 + FakeRuntime/Builder）
internal/guards/         # 架构守卫测试（import/IR/词汇/循环骨架/门面）
```

依赖方向（违反即 CI 红，守卫见 §11）：

```
api → engine → { model, state, capability, spec }
providers → { capability, spec, model }
state → { model, spec }
model / spec：不 import 任何 internal 包（叶子）
providers 之间互不 import；除 cmd 外无人 import providers（经注册表间接装配）
```

## 3. Capability 系统（插件模型）

**七类 Capability 端口**：Runtime、Builder、Registry、Edge、Logging、Metrics、ObjectStore。

- **机制（ADR-0003）**：编译期 Go interface + 注册表；Provider 自注册（init），配置选定每 Capability 同期唯一在册 Provider。**先不做进程外插件协议**（deletion test：当前没有第三方插件作者，gRPC 插件协议是 porter 生态规模才配付的成本）；但契约类型全部定义在 proto（经 genproto 落 `internal/spec`），未来抽进程外插件是机械工作，不需要改语义。
- **版本化**：Spec 带 `schemaVersion`，读入时按 check-strategy（默认：可读旧版+提示，拒绝跳代）——继承 porter 的演进教训。
- **Provider 契约三件套**：①能力接口（做事）；②`Describe() ProviderDescriptor`（名称、版本、所需配置 schema、部署形态——若是 Managed Provider 则返回自己的部署 Spec）；③`Health()`（供降级矩阵）。
- **配置**：`fleetlyd` 配置文件按 Capability 名给 Provider 与参数；换 Provider = 声明迁移（Runtime 更换的验收场景见领域模型 §5 场景 3 与 §12 N4）。
- **受管 Provider 自宿（ADR-0004）**：traefik、victorialogs、zot 等以普通 Workload 形态跑在 Runtime 上，由唯一一份通用 managedprovider reconciler 部署升级。平台自己的能力组件与用户 Workload 走**同一条** Runtime 通道——旧项目六个组件部署器六份翻译的历史不再发生。

## 4. IR：Spec 设计

```
AppSpec {
  schemaVersion
  app: { id, project }
  source: { git{repo,ref} | image{ref} | upload{id} }
  processes: [ { name, image|fromBuild, command, env, secretRefs, configRefs,
                 ports[{port, protocol(http|h2c|tcp)}], healthcheck{http|tcp|exec, grace}, resources{cpu,mem},
                 replicas, placement, volumes[], networks[] } ]
  build: { builder, dockerfile|railpack{pinnedVersion}|static{outputDir}, cacheFrom }   # F1.14
  firstBootJobs: [...]            # 部署期 init job（迁移等）
}
TaskSpec   { …同 processes 单元素 + ttl + ownerToken + networkGroup }
DatabaseSpec { engine, version, credentialsRef, storage, backupPolicy }
```

- **来源归一化**：Compose 受控子集（白名单只增、受管字段显式拒绝，继承旧 ADR-0015）/ API 直接创建 / 上传产物 → 统一归一化成 AppSpec；归一化结果即 Revision 冻结体，部署基线永远以 Revision 为准，不回读源文件。
- **投影**：engine 把 Spec 编译为 Runtime 无关的 Workload 集（§5 契约的输入）；探针、卷钉住、网络附件在 IR 里是声明，映射成编排器原语是 Provider 的事。
- **构建执行面**（ADR-0019）：Build 恒在控制面节点——BuildKit + 本机 daemon，并发上限可配，缓存只在本机；多节点构建缓存与独立构建节点延后；上传构建走流式 tar + 大小上限，断点续传延后。
- IR 不含：编排器 label、载体命名、约束语法、namespace、探针的编排器方言。守卫测试静态扫描 `internal/spec` 的 proto，字段名黑名单取**编排器语义的字段/类型名**（service/pod/unit/label 等；平台自有实体名如 Task/TaskSpec 不在此列）。

## 5. Runtime 契约（保持窄面）

tsuru 的 17 方法 Provisioner 是反面教材；核心面 6 个方法 + 三个可选子面：

```go
type Runtime interface {
    Ensure(ctx, ns NamespaceRef, ws []Workload, gen Generation) error // 幂等：同 gen 重放安全
    Remove(ctx, ns NamespaceRef) error
    Watch(ctx) (<-chan WorkloadStatus, error)     // 全集群状态流（Workload + 节点加入/离开/转移事件；engine 按 ID 归属过滤）
    Addresses(ctx, ns NamespaceRef) ([]Endpoint, error)
    DescribeCluster(ctx) (ClusterView, error)
    Enrollment(ctx, rotate bool) (EnrollKit, error) // 活加入材料；rotate 先作废旧材料（C3）
}
type RuntimeLogs interface { StreamLogs(...) }      // 子面，按需实现
type RuntimeInspector interface { InspectWorkloads(...) } // 子面（ADR-0022 spec 对照 drift）
type RuntimeExec interface { Exec(...) }            // 子面；经反向中继，节点零入站端口（继承 execrelay 模式）
type RuntimeAdmin interface { Drain/Cordon/... }    // 子面，CLI 管理操作
```

- **期望状态式**：唯一写动词 Ensure；副本数变化、健康检查变化都是新 Generation 的 Ensure。没有 update/scale/rollback 三个动词——回滚在平台层是 Replay（新 ADR-0005）。（动词用 Ensure 而非 apply：apply 是 ADR-0007 禁词，且 ensure 与幂等重放语义一致。）
- **节点身份锚定**（Provider 契约义务，继承归档 D-MN-8）：观测到无平台 ID 标记的节点 → 铸造平台节点 ID → 写回载体标记 → 审计 + `node.joined` 事件（经 Watch 流上报）；Volume 钉住与 Placement 绑定一律以平台节点 ID 为锚，节点 ID 永不复用。
- **材料分发**（ADR-0014）：Ensure 携带平台已解析的镜像拉取凭证与 Secret 注入材料，Provider 按节点分发（swarm `--with-registry-auth` 等价）；凭证不落载体 label 或明文 env（旧 DT-2 真机 404 教训）。
- **Drift 判定**：Provider 在 Watch 流里对照最近 Ensure 的 Generation 报 `drift` 信号；平台以 ID 查权威表判定归属（§3 已述），不解析载体命名。
- **载体命名/标记**：Provider 私有。swarm Provider 自持命名公式（`fleetly-<team>-<prj>-<app>-<proc>`，受 64 字符上限约束时可截断策略，唯一性以平台 ID 标记兜底）与 `fleetly.*` 标记；换 k8s Provider 时换成 annotation，平台语义不变。

## 6. 状态与事件

- **存储**：SQLite WAL 单文件；按聚合分包的 repo；goose 加法迁移。
- **写路径**：每条状态机线一个单写者 goroutine；四件一拍 = 状态 CAS + tombstone + Outbox 事件 + 审计，同事务（继承旧 ADR-0002）。序列与规则的双真源：api 侧 `internal/api/fleetlygrpc/acceptance.go` 的 `commit`（受理位原语，ADR-0024）与 engine 侧 `internal/engine/transition.go` 的 `commitTransition`/`commitWrite`（驱动域原语，2026-10-03 收口五份手搓拷贝；反手搓守卫在 internal/guards，Transit CAS 与"行写×事实发射"配对只许住在该文件）。
- **部署队列 admission**（ADR-0016）：同 App 部署请求去重（幂等键/commit）、默认 latest-wins 合并、显式 supersede 抢占、per-节点并发上限可配、queue 满显式反馈、排队与在途可取消；409 收窄为幂等键冲突与互斥锁。
- **读路径**：写前直读（冲突 409）；节点/载体状态是观测缓存，不参与决策（参与决策前必直读）。
- **事件**：Outbox 单调 seq；消费面 = gRPC server-streaming + SSE（Console/Agent 同一队列）；断档返回 410 + 快照重同步端点（继承）。
- **审计**：Token/人/Agent 的一切写操作留痕；触发来源枚举内建（manual / api / cli / webhook / schedule——学 zane-ops）。

## 7. API、CLI 与 Agent 面（ADR-0006）

- **proto 是唯一契约源**：一份服务定义生成 gRPC + REST(gateway) + OpenAPI(spec) + TS 客户端类型（Console schema 漂移门禁，继承）。服务面按领域模型八个上下文切分，读写分离（旧"一文件三 service 含写面"教训）。
- **三面同等能力**：Console / API / CLI。Console 只消费公共 REST/WS API，无私有服务端面（旧 P0-3 教训）；CLI 是瘦 API 客户端，写面与 API 对等，全命令 `--json`。
- **Agent 面 = CLI + 专属 Skills，不做 MCP**（2026-09-30 用户直裁：MCP 协议体验不佳）。机器契约在 CLI：稳定 JSON 字段（golden 钉死）、错误信封（errcode + 处置提示）、稳定退出码、`--wait`（等待原语的 CLI 形态）、`events follow`（事件流的 CLI 形态）。程序性知识在 Skills：`skills/` 随仓版本化，每个 Skill 包装一段 CLI 工作流（部署诊断、数据库开通与备份、Task 池管理、Platform Restore 等），与平台版本同批演进。
- **Token**：`resource:action` Scope（write 蕴含 read），团队/项目两级绑定；Bootstrap Token 首启生成、**可吊销**（旧项目缺口，这次补上）。
- **治理刹车**（ADR-0017）：per-Project 的 Task/Workload 数量配额、per-Token 创建速率限制、change freeze 变更冻结窗（命中返回带原因的拒绝）、属主 Token 吊销 → 名下 Task 默认宽限排空。Scope 维持"读默认开放、写显式授权"。
- **能力自描述**：`fleetly explain <资源>` 与 `fleetly schema` 输出 JSON Schema（Spec 与契约由 Go 类型反射生成，扩展面各自注入合并）——Agent 的零文档发现面（porter 模式）。
- **幂等**：创建型写 RPC 接受 `Idempotency-Key`（CLI `--idempotency-key` 透传）；同键同体重放，同键异体 409，记录保留 24h。这是六家参考 PaaS 的共同空白，也是 CLI 脚本化与 Agent 自动化的共同地基。
- **等待原语**：API `WaitDeployment / WaitBuild / WaitRun`（事件流过滤实现）+ CLI `--wait`；Agent 编排"部署-等待-验证"循环不必自写轮询（继承 sdk WaitBuild 经验）。
- **事件订阅**：全量或过滤后的 Event 流（gRPC stream / SSE / `fleetly events follow --json`），配合幂等键构成可靠的声明式自动化。

## 8. 失效、降级与运维语义

**降级矩阵**（Provider 宕机时平台行为，逐 Capability 声明，`Health()` 驱动）：

| Capability | 引导期依赖 | 故障影响 |
|---|---|---|
| Runtime | 必须 | 平台不可部署；已运行 Workload 不受影响（控制面单点诚实暴露） |
| Edge（配置发布） | 否 | 受管 Edge Workload 存活时存量路由继续服务，Route 变更失败并明示；发布前 schema 级预检（P9：traefik 无配置校验面——2026-10-04 真机核对 v3.5.6，子命令仅 healthcheck/version、API 全只读、坏快照**整份拒载且零日志**、last-known-good 继续服务），预检红 = 整快照拒绝、旧快照继续服务（控制面侧提前闭合 traefik 拒载语义，消除 5s poll 窗口与静默面）；发布后加载确认未落地（traefik 只读 API 面的暴露是安全权衡，随 Console/证书观测批裁决——IssueCertificate 观测同批）；受管 Edge Workload 自身宕机 = 全量路由中断（独立事故等级，单列通报） |
| Logging/Metrics | 否 | 部署照常；查询面报"能力不可用" |
| Registry | 多节点强烈建议 | 构建推送失败；未预拉到节点的 digest 新部署同样失败（已运行 Workload 不受影响） |
| ObjectStore | 否 | 备份失败；运行不受影响 |
| Builder | 部署期需要 | 镜像引用 Source 的部署不受影响 |

**HA 口径**（继承旧 ADR-0005，不改）：控制面单实例诚实暴露；数据面多 A 记录 + 连接级重试；有状态走 Backup/Restore；三 manager 管理面 HA 延后；明确不做 VIP/keepalived。

**顺序与恢复**：启动序 = 控制面 → Platform Backup → Managed Provider → 用户 Workload 观测；Platform Restore 期间只读；优雅退出是必要条件（in-flight Ensure 必须可安全中断重放，旧 spike V4）。

**孤儿**：一切对不上账的载体只登记、永不自动删（继承；平台自建残留如超 TTL 的 Run 由看门狗收口，不属孤儿）。

**升级与版本偏差**（ADR-0015，"升级永不弄坏你的东西"的设计支撑）：fleetlyd 升级序 = Platform Backup 前置 → 二进制/镜像替换 → SQLite 迁移（goose 前滚，失败=恢复备份重放）→ Managed Provider 逐个 reconcile（镜像钉版、逐个升级）→ 解除只读；验收 = 升级期间用户 Workload 零重启、路由零中断。CLI/server 版本偏差 = N-1 兼容 + 请求版本协商头；proto buf breaking 门禁保持。

## 9. 默认 Provider 选型（v1）

| Capability | 默认 Provider | 理由 |
|---|---|---|
| Runtime | swarm | 继承真机经验与 Docker29 坑清单；节点零安装 |
| Edge | traefik | swarm 上经 HTTP provider 下发配置已被旧项目验证；控制面强制全量配置防裸 `{}` 清空（旧 spike 教训） |
| Logging | victorialogs | 单核轻量、ES bulk 协议（旧 ADR-0006 实测裁决，OpenObserve 340MB 出局） |
| Metrics | victoria 系（vmsingle+vmalert） | 同族裁决 |
| Registry | zot（受管自宿） | 单二进制轻量；多节点镜像分发刚需（旧"预拉"痛点）；可切外置 registry |
| ObjectStore | 默认本地备份目标（开箱即用，ADR-0020）；外置 S3 兼容可配；RustFS opt-in 自宿 | 未配异地目标时持续告警"同机备份非灾备"；恢复演练是验收标准 |
| Builder | dockerfile + railpack(钉版) + static | 继承 |

默认捆绑面守 idle 预算（600MB 为参考默认值，ADR-0010 起以实测校准）；受管 Provider 单核轻量组件不算红线。

## 10. 跨运行时的诚实边界

编排器没有的语义不虚构：swarm 无 NetworkPolicy → 项目网络隔离靠 per-Project overlay + 命名归属（环境即项目：staging 独立 Project 独立网络，ADR-0011），文档明示"软隔离"；无项目配额 → v1 只做每 App 资源上限。k3s Provider 落地时能力只增不减，API 不因 Provider 不同而变形（Capability Descriptor 声明各自支持的面，API 用统一的能力发现端点暴露差异）。

## 11. 工程实践守卫（架构由测试承载）

继承旧项目被验证有效的全套，移植为可执行红线：

- **import 守卫**：编排器/基础设施 SDK 只准出现在 `internal/providers/*`（mobyscan 后继）；`model/spec` 叶子纯度；providers 互不 import；聚合 repo 无门面。
- **词汇守卫**：禁词扫描（duty、apply(动词部署义)、ingress、substrate、缩写词……基于 ADR-0007 冻结表）。
- **注册表三链咬合**：errcode/eventcode 注册表 + golden 快照 + usage 反向扫描（条目不命中即红）。
- **守卫白名单双向保鲜**：例外条目带理由注释，不再命中即红（旧 impldocscan 思想推广）。
- **golden**：CLI 全命令输出（默认与 `--json` 双形态，`--json` 全命令覆盖本身是守卫——归档项目 23 命令缺 `--json` 的缺口不重演）、Spec 归一化、Compose 白名单。
- **e2e**：dind shell 套件 + nightly 多拓扑（单/双/三节点）+ 真机 staging（复用归档仓 deploy/staging 经验）。
- **CI**：golangci-lint、race、wire 同步、buf breaking、Console schema 漂移门、deadcode 门禁——继承归档仓 pr.yml 骨架。
- 用户可见文本英文、注释中文（既有约定，随 AGENTS.md 落地）。

## 12. 演进路线（建议批次）

- **N0 心脏（单节点可用）**：spec/model/state + Runtime(swarm，含节点锚定) + Deployment 状态机与 admission 队列 + Build（控制面节点）+ Edge(traefik，含 h2c) + Secret/Config + git webhook 触发 + gRPC/REST + Token/Scope + CLI 核心命令（`--json`）+ 一行安装 + quickstart（sslip.io 零 DNS 首部署）；e2e dind 骨架与守卫先行。
- **N1 Agent 面 + torchwood 线**（验收 = ADR-0012 能力清单全绿）：幂等键 + 事件流(SSE) + Wait 原语 + `events follow` + `fleetly explain/schema` + Task/Run（one-shot/resident + Owner Lease + 双级稳定 DNS）+ Schedule + Task Network Group + App 跨挂 + 跨 Project 互通 + 治理刹车（配额/速率/change freeze）+ build-from-upload + zot 受管自宿（多节点镜像分发）+ Database 最小集（postgres[含 percona/pgvector]/redis 模板 + 本地备份）+ 首批 Skills。
- **N2 数据与观测 + 信任**：Database 全矩阵（mysql/mongo）+ 升级/迁移 + restic 备份 + ObjectStore（外置 S3/RustFS）+ 恢复演练 + Logging/Metrics Provider 受管自宿 + 平台升级工具（ADR-0015 验收：升级零扰动）+ dbtemplate 目录化与镜像 digest 钉定（DT-9）。
- **N3 体验**：Console（消费同一 API）+ exec 子面 + 终端 + 模板库。
- **N4 第二运行时试点**：k3s Provider，验收场景 = 领域模型 §5 场景 3（换 Runtime：无状态全语义保持 + 有状态 Backup/Restore + 显式数据处置）——这是对 ADR-0001 的终审。

重量提示：N2 ≈ N0+N1 之和，实施时可拆 N2a（数据+信任）/N2b（观测自宿）；每批以 deletion test 复审新增抽象（继承不抽象三连文化）。
