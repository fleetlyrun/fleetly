# ADR-0052: k3s 第二运行时试点（场景 3 验收）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-07 | 领域模型 §5 场景 3（换 Runtime 验收基准）、架构 §3/§5/§10（Capability 机制/Runtime 契约/跨运行时诚实边界）、ADR-0001（运行时中立终审）、ADR-0003（注册表机制）、ADR-0014（材料分发）、ADR-0021（技术栈钉版）、ADR-0025（Task 词汇/生命周期声明）、ADR-0036 决策 4（宿主 IP 级发布原语挂账重估）、ADR-0039（RuntimeUtility）、ADR-0041（受管采集面 Workload 扩展）、ADR-0046（网络重建动词）、ADR-0049（Exec 子面/E_EXEC_UNSUPPORTED 先例）、checklist F4.1 |

## 背景

F4.1 是 N4 唯一项，也是 v1 功能清单收官项：k3s Provider 作为 Runtime Capability 的第二实现，验收 = 领域模型 §5 场景 3（对 ADR-0001 运行时中立的终审）——同一 App 在 swarm → k3s 迁移，App/Revision/Route/ID 全部保持；无状态 Workload 语义全保持；有状态 Workload 经 Backup/Restore + 显式数据处置迁移；egress:none 由 swarm 弱隔离升级为 NetworkPolicy 强隔离（checklist F4.1 明文）。

前置事实（2026-10-07 预研实证，dind 容器 docker:29-dind / Alpine / privileged / --cgroupns=host 内）：

1. k3s v1.36.5+k3s1（stable 通道，2026-10-07）二进制直跑 `k3s server` 6s 就绪；`--disable=traefik --disable=servicelb` 与 fleetly 受管 Proxy 分立（k3s 默认 ingress 与 servicelb 会争 80/443 hostPort，与受管 traefik 冲突）。
2. **坑：containerd overlayfs snapshotter 在 dind 上不可用**（overlay-on-overlay 挂载被内核拒绝，`failed to mount overlay: invalid argument`）——解法 `--snapshotter=native`（零额外依赖，性能换确定性，e2e 环境够用；生产形态节点为原生文件系统不受影响）。
3. **坑：容器内直拉外网镜像不可靠**——预载通道 `docker image save \| k3s ctr images import -` 实证可用（import 后 pod 26s Running）；k3s 系统组件镜像（coredns/metrics-server）同样需要预载，否则系统 pod 滞留 ContainerCreating。
4. **NetworkPolicy 无需换 CNI**：k3s 内嵌 kube-router 的 netpol 控制器库（`--disable-network-policy` 才关闭）——默认 flannel 即支持。实证：deny-egress policy 应用后，原本可达的跨 pod 连接即刻拒绝。
5. **local-path PVC 即开即用**：PVC Bound、数据落盘（`/var/lib/rancher/k3s/storage/` 宿主路径），自带 StorageClass——Volume 有状态面零外部依赖。

## 决策

### 1. 试点形态：整集群声明迁移（config 选名），e2e 为主验证场

- **Runtime 选择 = 集群整体切换，不做 per-部署/per-App 双 Runtime**。真源锚：场景 3 的验收动作是"同一 App 在 swarm → k3s 迁移"（换 Runtime = 声明迁移，架构 §3）；Cluster 词条"由一个 Runtime 管理的 Node 集合"——同集群双 Runtime 分治与归属判定/网络模型/观测流全面冲突，否决。
- **config 新键 `runtime.provider`（缺省 `swarm`，升级零扰动）**。装配经 `capability.Build(ctx, KindRuntime, name)` 既有通道（ADR-0003 注册表已预留：多在册者要求配置选择）。swarm 与 k3s 编译期双注册（注册表是候选池）；**词汇口径调和（记录，非更名）：CONTEXT.md Provider 词条"每个 Capability 同期恰有一个在册 Provider"的"在册"= 装配生效（in-service）**，与 Builder 家族三 Provider 全在册（ADR-0032 spec 路由家族）同款理解，不构成词条违反。
- **未注册的 provider 名 = 启动 fail-fast 红**（config 解析面语义，`ParseScheduleOverlap` 先例），不引入新 errcode（config 错误不是 API 面）。
- **主验证场 = e2e dind 专用腿**（k3s in dind 二进制直跑）。staging 不换装：现役 swarm 集群是在役 dogfooding 环境（torchwood/messageloop），换装属破坏性动作且场景 3 全链在 e2e 可完整承载（跨 Runtime 迁移在同一 dind 内先 swarm 后 k3s 两段式）；staging k3s 专用环境/换装走查若需再做，另批裁决。
- **场景 3 迁移序（e2e 承载）**：Platform Backup 前置 → 旧 Runtime 数据面 Backup（Database dump）→ k3s 就绪 → `runtime.provider=k3s` 重启 fleetlyd → Revision 基线重放（既有 rebuildBaselines 链，App/Revision/Route/ID 全保持）→ 新 Runtime Restore（含 preseed 卷恢复）→ 断言语义与数据。**placement 绑定不跨 Runtime 复用、节点 ID 永不复用**：k3s 集群节点是新节点（新铸平台节点 ID）；旧 swarm 载体对 k3s Provider 不可见——平台失明即"显式数据处置"的诚实形态：平台不自动搬也不自动删，处置动作（排空旧集群、删旧卷）是运维序，runbook 记录；挂账的载体登记面（旧 Runtime 孤儿报告）不在本批。

### 2. k8s 客户端：client-go 钉版，仅 providers/k3s

- 引入 `k8s.io/client-go`/`k8s.io/api`/`k8s.io/apimachinery`（版本与 k3s v1.36 对齐的 v0.36.x 线，go.mod 钉版，ADR-0021 口径）。watch 的 resourceVersion 语义/410 Gone 重试/exec 的 SPDY 通道是深坑，手写 REST 客户端 = 重复发明 + 坑面自担，否决。
- **import 守卫既有规则天然覆盖**：编排器 SDK 只准出现在 `internal/providers/**`（架构 §2/§11）——client-go 只进 `internal/providers/k3s`。
- 连接面：kubeconfig 路径可配（缺省 `/etc/rancher/k3s/k3s.yaml`，e2e 内 fleetlyd 与 k3s 同容器即达）；`runtime.k3s.kubeconfig` config 键。

### 3. 域与载体映射（Provider 私有，平台不解析）

- **Namespace = per-Project**（`fleetly-<projectID>` 净化公式）。隔离域的六轴（App/Task/Database/Browse）在 Namespace 内以 **label 选择器**承载（`fleetly.ns.*` 同 swarm 公式，+ `fleetly.ns.browse`）；与 swarm 的 label 选择器域收敛语义同构（域内多余载体随 Ensure 移除）。
- **载体类型映射**：

| Workload IR | k8s 对象 |
|---|---|
| 长运行（App Process / Database / Browse / 受管域） | Deployment（replicas） |
| `Global=true`（受管采集面） | DaemonSet |
| one-shot Run（`RestartNever`） | Pod（restartPolicy=Never，退出即终态） |
| `Addressing`（平台 DNS 名） | Service（ClusterIP；Service 天然 RR = 池级轮询的原生等价） |
| `Volumes` | PVC（local-path StorageClass）+ Deployment 卷挂载 |
| `Ports`/`Addresses` 端点 | Service ports（Addresses 期望集端口注入语义同 swarm） |
| `Placement.NodeIDs` | nodeSelector（`fleetly.node.id` 节点 label，锚定公式同 swarm D-MN-8） |
| `Healthcheck` | readiness+liveness probe（http/tcp/exec 三形态原生） |
| `Resources` | container resources.limits |
| `StopGrace` | terminationGracePeriodSeconds |
| `HostBinds` | hostPath 只读挂载 |
| `Publish`（Mode=host 与 mesh） | container hostPort（见决策 5） |
| `Materials.SecretFiles` | k8s Secret（fleetly-managed）+ volumeMount `/run/secrets/<名>`（值不落 env/label，ADR-0014） |
| `Materials.RegistryAuth` | imagePullSecrets（per-registry Secret） |
| `Generation`/`GenerationScoped` | label `fleetly.generation` + Deployment 名 `-g<gen>` 代次后缀（双代窗两代独立 Deployment） |

- **网络模型 = Namespace 即互通域**：swarm 的 per-Project overlay ↔ k8s per-Project Namespace（域内全通）；跨 Project 隔离 = Namespace 间默认不通（k8s 原生）。`Networks`/`NetworkRefs` 平台名在 k3s 翻译为 Namespace 域归属（不建独立网络载体）；受管 Proxy 的 NetworkRefs（挂全部活跃项目网）= system Namespace 到项目 Namespace 的入站可达（k8s netpol 有状态回程放行，egress:none 项目对 Proxy 后端服务零影响，见决策 6）。
- Watch 流：pod/deployment/node 事件 → WorkloadEvent 映射；节点锚定同 swarm 模式（观察无 `fleetly.node.id` label 的 Node → 铸 ULID → label 写回 → NodeJoined 事件；锚定扫描节拍同款）。
- Ensure 收敛语义：create-or-update（server-side 等价比对——期望对象与回读对象做 canonical 比对跳过 no-op，幂等重放不产生滚动；swarm lastIssued 断路器账本同款自激防护）。挂卷/单副本负载的滚动顺序争用面：k8s Deployment 默认 RollingUpdate maxSurge——挂卷负载显式 `maxSurge=0, maxUnavailable=1`（stop-first 等价，防双任务争 RWO 卷；swarm rolloutOrder 同款语义锚）。

### 4. 子面实现矩阵（先裁后做）

| 子面/方法 | 裁决 | 形态 |
|---|---|---|
| Ensure / Remove / Watch / Addresses / DescribeCluster / Enrollment | **实现** | 决策 3 映射；Enrollment = k3s node token + server URL 的 agent 装载命令（AgentCommand 附 kubeconfig 形态挂账，见决策 9） |
| RuntimeLogs | **实现** | pod logs API（Follow 流；采集环 + `fleetly logs` 实时径） |
| RuntimeAdmin | **实现** | cordon/uncordon 原生；drain = cordon + evict（单副本 PVC pod 受 PV 节点亲和在同节点重建，卷钉住语义不破） |
| RuntimeInspector | **实现** | 读 Deployment/Pod spec 还原 WorkloadObservation（drift spec 对照 + 受管 gen 播种） |
| RuntimeUtility | **实现（k3s Pod 形态）** | 一次性工具 Pod（同项目 Namespace → 域内 DNS 达 db-<id>），等待退出 + logs 流式转 stdout/stderr；**不可用 docker daemon 工具容器**：docker 网络与 k3s pod 网络不互通（Backup/Restore 是场景 3 验收硬项，本子面为硬依赖） |
| RuntimeExec | **诚实失败（挂账）** | E_EXEC_UNSUPPORTED（ADR-0049 先例：子面缺席诚实失败）。k8s 有原生 exec（apiserver SPDY/WS），非编排器缺失——缺席理由是**试点实现排序**，不是能力边界；错误文案如实（provider 未实现），Notes 声明。后续批补（relay agent 节点形态 = kubeconfig 持有） |
| RuntimeNetworkMaintenance | **诚实失败（挂账）** | ADR-0046 动词面是 swarm overlay attachable flag-day 的修复原语；k3s 网络即 Namespace，无载体网络对象/attachable 概念——动词面的前置病灶不存在（Namespace 恒存在无 flag-day）。engine 侧按子面缺席诚实失败（ADR-0046 既有降级语义） |
| RuntimeHygiene | **诚实失败（静默跳过，挂账）** | engine 侧"未实现时卫生清扫静默跳过"降级语义已内建；k8s Secret/PVC 的孤儿判据（现役引用集）后续批设计 |

**架构 §10"能力只增不减"的口径**：egress 隔离是"增"（本批兑现）；Exec/NetworkMaintenance/Hygiene 是试点实现排序缺口，以 `Describe().Notes` 诚实声明 + E_EXEC_UNSUPPORTED 语义正确的错误面过渡；k3s 恒为显式 opt-in（config 缺省 swarm），生产现役集群零影响。三子面补齐前 k3s 不升格为可缺省 Runtime。

### 5. 受管域形态与 ADR-0036 宿主 IP 发布重估

- **ADR-0036 决策 4 挂账重估结论：k8s 有宿主 IP 级发布原生原语**（hostPort/hostIP）——swarm routing mesh 无宿主 IP 原语的编排器缺口在 k3s 侧不存在。`PortPublish.Mode=host` → container hostPort（cadvisor 每节点端点 8080，DaemonSet + hostPort）；`Mode=mesh`（traefik 80/443、zot 5000）→ 同样落 hostPort（k8s 无 routing mesh 对应物；受管域端口本就需要节点可达，hostPort 是最小诚实形态）。zot 的宿主侧通配发布问题（ADR-0036 遗留）在 k3s 侧经由 hostPort 显式声明承载，doctor 自证口径不变。
- 受管域翻译（全部经 ManagedWorkloads 同一 Workload IR，通用 reconciler 零改动）：traefik = Deployment + hostPort 80/443 + `--providers.http` 配置端点（不变）；zot = Deployment + hostPort 5000 + PVC（数据卷钉住控制面节点 = nodeSelector）；VL/VM = Deployment + Service（域内 ClusterIP 9428/8428）+ PVC；cadvisor = DaemonSet + hostPort 8080 + hostPath 四条（HostBinds）。
- 受管域 Namespace = `fleetly-system`（与用户项目域分立；swarm 的受管域 `fleetly/system/...` 载体名前缀对应物）。Proxy 到项目后端的可达性：k8s netpol 有状态语义下项目域无需为 Proxy 开入站例外（无 ingress policy = 域内互通域指 Namespace 内；跨 Namespace 的 Proxy→后端连接由后端所在 Namespace 的 netpol 决定——试点形态项目域不设 ingress policy，Proxy 可达；egress:none 只约束出站方向，见决策 6）。

### 6. egress:none → NetworkPolicy 强隔离（F4.1 验收明文）

- spec/API/受理面**零变更**（Network 实体 `egress:none` 声明既有；swarm 侧弱隔离语义不动）。
- k3s Provider：`egress:none` 的项目域 → Namespace 级 **deny-egress NetworkPolicy**（`policyTypes: [Egress]`，规则集 = 放行同 Namespace 流量 + 放行 kube-system DNS（UDP/TCP 53）+ 拒绝其余出站）。**域内互通与 DNS 必须显式放行**（deny-all 立即断服务发现，预研实证 + k8s netpol 语义）；入站方向不设 policy（Proxy/域内消费不受影响；netpol 有状态回程自动放行）。
- 能力差异暴露：`Describe().Notes` 声明（"network isolation enforced by NetworkPolicy; egress:none is strong isolation"——与 swarm Notes 的弱隔离声明对照，能力发现端点既有面）。
- 跨 Project peer / Task Network Group 隔离语义：k3s 单 Namespace 模型下域内全通（App/Task/Database 同项目域互通）——**Task Network Group 的独立网络组隔离弱化**（swarm 语义：Task 组独立，App 显式跨挂才通）记 Notes + 挂账；跨 Project peer 声明/批准（ADR-0013）在 k3s = Namespace 间 netpol 互放行，挂账后续批。场景 3 验收面不含此两项。

### 7. 有状态面：PVC + 天然卷钉住 + 显式数据处置

- Volume → PVC（名 = `fleetly-vol-<volumeID>` 净化公式，平台 Volume ID 跨 Runtime 保持）；StorageClass = k3s 自带 local-path（**默认钉住节点语义天然成立**：local-path PV 的节点亲和在首次绑定时锚定，后续 pod 重调度受 PV 亲和约束回同节点——与 swarm"卷钉住=节点约束"语义等价）；显式 `Placement.NodeIDs` = pod nodeSelector 补充约束。
- 镜像 VOLUME 匿名卷遮蔽坑（F2.3 staging 事故）在 k3s 无对应面（PVC 挂载路径显式）；PGDATA 显式化材料不变（Database 模板零改动）。
- Backup/Restore = RuntimeUtility k3s Pod 形态（决策 4）；preseed 卷恢复（redis RDB）= UtilityVolumeMount → 临时 PVC 挂载（local-path 控制面节点锚定 = preseed 的节点约束等价）。
- **显式数据处置（场景 3）**：跨 Runtime 迁移不搬卷——旧 swarm 卷对 k3s 不可见（数据处置 = Backup 对象经 ObjectStore 随平台走 + 旧卷运维处置）；e2e 断言锚 = 数据经 Backup/Restore 完整到达 + 平台行 ID 全保持。

### 8. k3s 钉版与 e2e 通道（ADR-0021 口径）

- **k3s 版本平台常量单源：v1.36.5+k3s1**（2026-10-07 stable 通道核对 + dind 实证）。同 commit 纪律：常量、e2e 下载段（含 sha256 校验）、守卫（TestK3sPinConstant 同款静态断言，railpack/restic 先例）三者一致。
- e2e 形态（dind 专用腿）：宿主下载 k3s 二进制（sha256 校验）→ dind 容器内直跑 `k3s server --disable=traefik --disable=servicelb --snapshotter=native`（预研实证形态）→ 镜像预载 `docker image save | k3s ctr images import -`（应用镜像 + k3s 系统镜像清单）→ fleetlyd（`runtime.provider=k3s`）全链。
- mise 任务：`e2e:k3s`（k3s 全链冒烟：deploy→succeeded→零 drift→rollback→egress:none 强隔离断言→受管域 ready）+ `e2e:runtimeswitch`（场景 3 两段式：swarm 部署+Database backup→切 k3s→Revision/ID 保持断言→restore 数据断言→egress 强隔离）。

### 9. 试点诚实清单（挂账，后续批）

1. RuntimeExec（k8s 原生 exec + relay agent kubeconfig 形态）；2. RuntimeHygiene（Secret/PVC 孤儿判据）；3. Task Network Group 隔离语义（netpol 细分）；4. 跨 Project peer（Namespace 间互放行）；5. Enrollment AgentCommand 的 worker 节点 kubeconfig 装载（两节点 e2e 腿）；6. kubeconfig RBAC 最小权限面（试点用 cluster-admin，生产形态收敛）；7. 多 server HA（k3s embedded etcd/sqlite 单 server 试点）；8. 旧 Runtime 孤儿载体登记面（平台失明的诚实形态，runbook 承载）；9. `--snapshotter=native` 为 e2e 形态（生产节点原生文件系统用默认 overlayfs）。

## 后果

- swarm Provider 零改动（试点零回归；swarm 全套测试是回归底座）；engine 消费面经 capability 契约零改动（Runtime/子面探测既有形态）。
- config `runtime.provider` 缺省 swarm = 现网零漂移；双 Runtime 编译期在册（注册表候选池），装配期恰选一。
- go.mod 新增 k8s.io 三 module（client-go/api/apimachinery v0.36.x 线）；依赖只进 `internal/providers/k3s`（import 守卫）。
- apitest 夹具零改动（FakeRuntime 已覆盖契约面；k3s 真链走 e2e——交接既定口径）。
- 词汇零新增（k3s 是 Provider 名，Runtime 词条预期内；"在册"口径调和不构成更名）。
- Console/CLI/API 面零感知（Runtime 是 Capability 内部选择；能力差异经能力发现端点 Notes）。

## 验收锚

- [ ] config `runtime.provider` 缺省 swarm 行为逐位一致（装配测试 + 升级零扰动断言）；显式 `k3s` 装配生效（faces 启动日志含 k3s 核心+子面矩阵）；未知名启动 fail-fast 红
- [ ] k3s Provider 契约断言齐全（核心六面 + Logs/Admin/Inspector/Utility；Exec/NetworkMaintenance/Hygiene 缺席断言 = 编译期不实现 + 运行期诚实失败面）
- [ ] 翻译单测：Workload IR → k8s 对象全字段映射（Deployment/DaemonSet/Pod/Service/PVC/hostPort/probe/nodeSelector/双代窗代次名/egress netpol）
- [ ] e2e `e2e:k3s` 全绿：deploy(image) → succeeded → 零 drift → rollback → Database（PVC 卷钉住）→ egress:none 强隔离断言（netpol 应用前后可达性对比）→ 受管域（traefik/zot/VL/VM/cadvisor）ready
- [ ] e2e `e2e:runtimeswitch` 场景 3 全绿：swarm 侧部署 + Database Backup → 切 k3s → App/Revision/Route/ID 全保持断言 → Restore 数据完整断言（跨 Runtime 数据经 Backup 对象迁移，不搬卷）
- [ ] 能力发现面：k3s Describe().Notes 含强隔离声明（与 swarm 弱隔离 Notes 对照）
- [ ] swarm 全套测试零回归 + `mise run test`/`mise run lint`/`go test -count=1 ./internal/guards/` 全绿
- [ ] k3s 版本常量与 e2e 下载段同 commit 一致（守卫静态断言）
