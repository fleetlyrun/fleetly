# fleetly 多节点包（E1 / W2）专项设计

| 状态 | 日期 | 关联 |
|---|---|---|
| 草案（待裁决轮） | 2026-09-20 | [v0.2 规划](../plan/2026-09-20-v0.2-plan.md) §2 W2 行、§5 E1 行、V2-7（`--base-domain` 安装项）；[平台架构](2026-09-17-architecture.md) §2.6 多节点模型、§4.3 v0.2 多节点条目与验收三断言、D2/D12（Swarm 承担成员管理）、§2.2 选型表 zot 行、§4.2 底座端口加固；[放置专项](2026-09-17-stateful-placement.md)（意图/绑定/执行三层、平台节点 ID 为锚、选点三因子、卷前哨、rebind 语义、V6a/V6b）；[状态模型](2026-09-17-state-model.md) §2.2/§2.3（观测缓存、身份锚定、runtime_node_refs）、§4 v0.2 后置项；[安装器现状](../../deploy/install.sh)（单节点隐式 swarm init、引擎门禁、安装报告）；[placement.proto](../../proto/fleetly/server/v1/placement.proto) 与 `internal/placement/`（单机切面实现）；[资源校准](../reports/2026-09-19-resource-calibration.md)（预算基线）；[词汇表](../../UBIQUITOUS_LANGUAGE.md) |

本设计的锚定事实：v0.1 已实现单机同路径的 placement（候选集 = 本机常量 + `E_CAPABILITY_REQUIRES_MULTI_NODE` 守卫）、nodes 观测缓存（30s resync + observed_at/stale）、本机平台 ID 锚定（meta → node label → runtime_node_refs）、`GET /v1/system/nodes` 只读面（SystemService.ListNodes）、Traefik global 服务 + HTTP provider（明文 8422 + 证书本地卷 + seed 容器）、镜像本地 digest 引用免 registry（Spike A E5 实证）。E1 = 把这些单机切面扩到 2 节点拓扑，不引入第二写点、不自研成员协议（D12）、不建节点生命周期 API（D18）。

## 1. 现状与问题

| # | 现状（代码/文档事实） | 多节点下的问题 |
|---|---|---|
| 1 | `internal/placement/placement.go`：`Resolve` 候选集 = 本机直读快照的常量一项；`GuardMultiNode` 对节点数 >1 一律拒绝；`MoveBinding` 静态守卫拒绝 | 候选集必须扩为节点表；换点/rebind 无实现路径 |
| 2 | 平台节点 ID 仅本机有（`NodeIdentity` 只锚定 self，meta 单值） | worker 加入后没有平台身份——placement 绑定锚（`n_<ULID>`）不存在，`fleetly.placement.node` 无法解析、约束无法编译 |
| 3 | node.* 产品事件（joined/down/up/removed）在事件注册表预留但**从未发出**（observer 明确「不产生产品事件」） | 节点叙事（加入/失联/drain）对 UI/CLI/AI Agent 不可见 |
| 4 | Traefik 配置端点 = `http://<advertise>:8422/configs` 明文 + 静态 bearer token；证书经**本地命名卷**（seed 容器承载）挂载给 Traefik | ①命名卷是 per-node 本地卷，worker Traefik 挂到的是空卷——证书到不了 worker；②动态配置若按多节点需要内联证书私钥，明文跨公网不可接受（架构 §2.6 既定「跨公网走平台域名 HTTPS」） |
| 5 | 镜像 = 本地 `名称@sha256:` 零 pull（`internal/build/imagestore.go`）；构建不推送 | worker 节点无镜像，任务无法调度——必须引入 registry（架构 §2.6 zot 方案） |
| 6 | `deploy/install.sh` 无 `--base-domain`（V2-7 已裁决为可选项）；`--harden-firewall` 仍是 reserved 占位；安装报告无平台子域面 | 多节点启用缺安装入口；join 端口放行只有警示没有精确规则 |
| 7 | `SystemService.ListNodes` 已在 `/v1/system/nodes`（NodeView 含 platform_id/state/availability/observed_at/stale/labels），但无已钉应用、无 join 向导面；放置专项 §2.3 文档写的路径是 `GET /v1/nodes` | 文档路径与实现不一致（D-MN-9 裁决）；节点只读面缺 placement 交叉引用与向导入口 |
| 8 | 卷前哨 `VerifyVolumesOnNode` 代码路径完整但单机恒短路；`rebind` CLI 与跨节点迁移（restic）为 v0.2 后置（放置专项 §4） | 有状态应用换点无操作路径；孤儿卷跨节点不可见 |
| 9 | 2 节点验收演练（架构 §4.3 三断言）未脚本化 | 验收不可复跑 |

## 2. 目标设计

### 2.1 拓扑与组件面（谁在哪台机器上跑什么）

| 节点 | 常驻组件 | 说明 |
|---|---|---|
| manager | fleetlyd（systemd host 进程，v0.1 形态不变）、Docker Engine、Traefik（global 任务）、**zot registry（新增，replicated-1 钉 manager）**、BuildKit、fleetly-exec（W5） | 唯一写点（SQLite）；控制面单机形态不随多节点改变 |
| worker | Docker Engine、Traefik（global 任务）、fleetly-exec（W5，Swarm global 分发，**零安装动作**） | **零 fleetly 安装物**：无 fleetlyd、无配置文件、无 agent；成员管理/心跳/分发全归 Swarm（D12）；worker 侧唯一的「接入动作」= `docker swarm join` + 防火墙/DNS |

worker 加入即被 `fleetly-ingress`（global）覆盖：入口冗余随之成立（DNS A 记录指向全部节点，连接级重试语义）。构建仍在 manager（BuildKit + docker.sock 在 manager 本地），worker 不构建。

### 2.2 平台域名与安装项（V2-7 落地）

`deploy/install.sh` 增加可选参数 `--base-domain <domain>`：

- 单节点不填 = 维持 v0.1 现状（本地 digest、明文 8422 provider、证书卷模型同现状口径）。
- 填写 = 写入配置 `base_domain: "<domain>"`，安装报告增列平台子域与 DNS 指引；**多节点（join）启用时必填**（D-MN-13，否则 join 向导拒绝）。

**平台子域三件套**（均出自平台证书，见 §2.4）：

| 子域 | A 记录 | 承载 | 说明 |
|---|---|---|---|
| `ctrl.<base>` | **仅 manager** | fleetlyd 配置端点 TLS 面（8423，§2.4） | 直连 manager 端口，**不经 Traefik**（避免冷启动循环，D-MN-3）；指向 worker 会连接拒绝 |
| `registry.<base>` | 全部节点 | Traefik → zot（overlay 内 `fleetly-registry:5000`） | worker dockerd 零配置信任（公信 CA），拉取经任意入口节点进 overlay 到 zot（架构 §2.6 原文） |
| `console.<base>` | 全部节点 | Traefik → `http://<advertise>:8420`（manager 上的 gateway 面） | 面板经 443 反代；直连 8420 路径保留（回退/排障） |

应用域名的 A 记录矩阵沿用既有 DNS 契约（全部节点、TTL ≤300s、`fleetly domains verify` 校验）；join 向导把平台子域的 DNS 增改纳入步骤清单（复用 domains verify 机制）。

**数据流（多节点部署全链）**：push/webhook → 构建（manager）→ **推送 `registry.<base>/apps/<app>:b<buildid>`，取 digest** → service 以 `registry.<base>/apps/<app>@sha256:<digest>` 创建（`--with-registry-auth` 分发 zot 凭据）→ worker 拉取（443 → 本/任一节点 Traefik → overlay → zot）→ health gate → 路由发布（控制面经 provider 下发全集群 Traefik）→ 观察窗。

### 2.3 join 向导（含端口精确放行清单）

**入口**：CLI `fleetly nodes join-guide [--worker-ip <ip>] [--json]`（admin scope）与 Console 节点页「添加节点」向导；服务端 RPC `GetJoinGuide`（§5）。前哨：`base_domain` 为空 → `E_MULTI_NODE_REQUIRES_BASE_DOMAIN`（D-MN-13）。

**向导输出**（服务端生成，`JoinGuideView`）：

1. **join 命令**：`docker swarm join --token SWMTKN-… <manager-addr>:2377`。manager-addr 取 swarm info NodeAddr，支持 `--manager-addr` 覆盖（advertise 为私网而 worker 跨公网的场景，安装报告已警示的暴露面口径衔接）。
2. **worker 前置门禁命令**（复制到 worker 执行；平台不建远端通道）：`docker version`（≥29.8.1）+ iptables legacy 判定（与 install.sh §4.2 同判据的命令形态）。
3. **精确放行规则**（按 `--worker-ip` 生成文本，**只生成不自动应用**——平台不静默改用户防火墙；`--harden-firewall` 自动应用维持 reserved，不入 E1）：

| 方向 | 端口/协议 | 用途 | 规则 |
|---|---|---|---|
| worker → manager | 2377/tcp | 集群管理 | manager 侧 `iptables -A INPUT -p tcp -s <worker-ip> --dport 2377 -j ACCEPT`（其余默认策略不动） |
| 双向 | 7946/tcp + 7946/udp | gossip | 两侧各放行对端 IP |
| 双向 | 4789/udp | overlay VXLAN | 两侧各放行对端 IP |
| worker → manager | 8423/tcp | Traefik 配置端点 TLS 面（token 鉴权，§2.4） | manager 侧按 worker IP 放行 |
| 公网 → 全部节点 | 80,443/tcp | 应用入口（Traefik host 端口） | 既有基线，向导复述 |

4. **DNS 步骤**：既有应用/平台域名 A 记录追加 worker IP（TTL ≤300s）；`ctrl.<base>` 保持仅 manager；`fleetly domains verify` 复核。
5. **完成判定**（向导自动推进）：节点观测拍出现新 swarm 节点 → 锚定 duty 收编（§2.6，`node.joined`）→ 节点 ready + 该节点 Traefik 任务 running → 向导终态；随后**自动 rotate worker join-token**（`join.token_rotate=auto` 默认；批量加节点场景配置 `manual`，全部完成后手动 `fleetly nodes rotate-token`）。

### 2.4 配置与证书同步通道（Traefik ↔ 控制面）

既定裁决（架构 §2.6）：路由与证书一律由控制面经 HTTP provider 下发、跨公网走平台域名 HTTPS、取不到配置时 Traefik 保留上一份成功配置。落地细则：

**端口分面（新增 8423）**：

| 端口 | 协议 | 内容 | 消费方 |
|---|---|---|---|
| 8422（既有） | HTTP | `/healthz` + `/.well-known/acme-challenge/*`（无秘密载荷；挑战 token 是 ACME 契约公开物） | Traefik 静态挑战路由后端（bootstrap 期明文可达即可） |
| 8423（新增，`ingress.config_tls_addr`，默认 `0.0.0.0:8423`，**仅 base_domain 非空时启用**） | HTTPS（平台证书，SAN 含 `ctrl.<base>`） | `/configs`（全量动态配置 + 内联证书；静态 bearer token 鉴权沿用） | 各节点 Traefik provider endpoint |

- base_domain 为空（单节点 v0.1 形态）：provider endpoint 维持 `http://<advertise>:8422/configs`，行为与现状逐字一致。
- base_domain 非空：Traefik 静态参数改为 `--providers.http.endpoint=https://ctrl.<base>:8423/configs`（+ 公信 CA 校验，无需自定义 CA 池）。

**证书分发模型切换（多节点硬前提）**：动态配置 `tls.certificates` 改用**内联内容**（certContent/keyContent，PEM 随配置 JSON 下发），v0.1 的「证书本地卷 + seed 容器」路径退役（单节点亦统一，消灭双轨）；既有单节点安装的迁移（卷内证书导入台账 + 挂载收敛移除）进票据分解，v0.1.x 升级前快照为回退路径。落盘仍为 `ingress.cert_dir` PEM 0600 + 独立备份边界（状态模型 §2.1 不变）。

**Bootstrap 次序（无循环依赖）**：

```
① fleetlyd 启动（base_domain 已配置；DNS 已指向 manager）
② 部署 Traefik（global，静态参数含挑战路由 → http://<advertise>:8422 与 provider=https://ctrl.<base>:8423）
   —— provider 此刻不可达（无证书）：Traefik 容忍，仅静态路由（80 挑战面）可用
③ ensure-平台证书 duty（启动重试，退避 ~30s 直到成功）：HTTP-01 → 任意节点 :80 → 静态挑战路由 → 8422 应答器 → 签发
④ 8423 TLS 面就绪 → Traefik 首拉成功（动态配置 + 内联证书到达全部节点）→ 子域/应用路由生效
⑤ zot 部署（不依赖证书：overlay 内 5000 明文）+ registry/console 路由进动态配置 → 对外可用
```

「平台域名证书先于 zot **对外**可用、但不先于 zot **部署**」——zot 服务创建与卷初始化在 ④⑤ 间任意时刻幂等收敛即可。

### 2.5 zot 平台部署与镜像管线

**Swarm service 形态**（`internal/ingress` 旁新增 registry 部署器，同款幂等收敛模式）：

| 项 | 值 |
|---|---|
| 服务名 | `fleetly-registry`（集群全局命名空间，`fleetly.*` 前缀纪律） |
| 镜像 | zot 钉 digest（R7 门禁 + `deploy/check-image-pins.sh` 覆盖 + `docs/runbooks/image-prepull.md` 增行） |
| 模式/约束 | replicated-1，`node.labels.fleetly.node-id == <manager 平台 ID>`（placement 绑定同一锚——**zot 自身就是平台的第一个多节点绑定用户**） |
| 存储 | 本地命名卷 `fleetly-registry-data` → `/var/lib/registry`；**不进控制面备份**（state_backups 不含；镜像可重建 = 重建-重部署，恢复阶梯文档注明） |
| 网络 | 新增平台 overlay `fleetly-system`（label `fleetly.managed=true`）；Traefik 常挂（spec 基础网络集），zot 单挂 |
| 鉴权 | HTTP Basic（平台生成随机 user/pass，`<data>/fleetly-registry.auth` 文件 0600——与 ingress token 同形，不入 SQLite；轮换 = 重新生成 + zot 配置更新 + 服务重建） |
| 路由 | `Host(\`registry.<base>\`)` → `http://fleetly-registry:5000`（动态配置的平台路由段；控制面自有，不属任何 app） |

**部署时机**：`base_domain` 配置即部署（D-MN-5 ⚖️，预算取舍）——join 前已就绪，避免「join 后首部署前多一步部署依赖」；单节点不填域名则零成本。

**镜像管线改造点**（`internal/build`）：

- 推送：solve 输出 `type=image,name=registry.<base>/apps/<app>:b<buildid>,push=true`（凭据经 BuildKit secret 注入）；digest 取自 solve 响应，`builds.image_ref` 记 `registry.<base>/apps/<app>@sha256:<digest>`。本地模式（无 base_domain）管线不变。
- 服务创建/更新：registry 模式经 `--with-registry-auth` 携带 zot 凭据（Swarm 原生分发到拉取节点）。
- 部署前哨双模式：本地模式 = 现行 `PreflightImage`（本机 inspect）；registry 模式 = manifest HEAD（不可达 → `E_REGISTRY_UNAVAILABLE`；缺 manifest → `E_IMAGE_UNAVAILABLE` 复用，语义同「回滚目标镜像不可得」）。
- 回滚：revision 快照内 digest 从 zot 拉取，同前哨；zot 数据不删（平台永不自动清理镜像的 v0.1 纪律平移）。
- 构建缓存：维持本地缓存（构建只在 manager，cache 局部性成立）；**registry cache 导出后置**（§7）。

### 2.6 placement 多节点：候选集、解析、选点、换点

`internal/placement` 改造（概念模型三层不动——意图 label / 绑定 placements / 执行约束编译）：

**候选集**：`self()` 单机直读替换为 `candidates(ctx)` —— 底座直读全量节点快照（`ListNodeObservations`，写前直读纪律不变，禁读观测缓存），每项 = {swarm_node_id, platform_id（取 `fleetly.node-id` label，未锚定为空）, hostname, ready}。

**label 解析（`fleetly.placement.node` 值域）**：显示名（hostname，**集群内必须唯一**——同名即歧义 → `E_PLACEMENT_NODE_INVALID` 422 + 候选清单提示改用平台 ID）或平台 ID（`n_<ULID>`）。适配器对 swarm node ID 形态的宽松接受维持现状（无害）。多服务 label 指向不同节点 → `E_PLACEMENT_LABEL_CONFLICT`（引擎聚合后前置校验，v0.1 模式不变）。

**解析与选点（`Resolve`，放置专项 §2.5 落地）**：

```
label pin → 在候选集中解析（名或平台 ID）；失败 → E_PLACEMENT_NODE_NOT_FOUND（422 + 全量候选清单）
已有绑定 → 保持（绑定优先于 label 的缺失）
无绑定且无卷 → 不钉（自由调度）；显式 pin → W_PLACEMENT_STATELESS_PIN
无绑定且有卷 → 自动选点：候选 = 已锚定 + ready + active；
  评分 = 数据引力（卷注册表所在节点） > 已钉应用数少（placements 计数，权威 SQLite） > 平台 ID 字典序
  无候选 → E_PLACEMENT_NO_ELIGIBLE_NODE
```

**守卫退役**：`GuardMultiNode`/`MultiNodeUnsupported` 删除（F1 式悬空码清理）；`E_CAPABILITY_REQUIRES_MULTI_NODE` 码**保留在注册表**（永不复用、永不删码），其 summary 的「v0.1 single-node」措辞维护为拓扑/配置语义（注册表文案维护，非码变更，契约轮追认）。

**换点（显式、破坏性确认）**：新 RPC `UpdatePlacement`（`PUT /v1/apps/{app}/placement`，admin scope）：

```
请求 {app, node /*显示名或平台 ID*/, data_ack /*restored|discarded*/, confirm}
校验：目标节点存在且 ready（直读；否则 NOT_FOUND/UNAVAILABLE）
  有卷应用：data_ack 必填——缺省 → E_VOLUME_NODE_MISMATCH（前哨语义前置到换点面）
  discarded：admin + confirm；卷行 status 不变、登记 prev 节点；事件 volume.discarded + 审计
  restored：volumes 行 platform_node_id → 目标节点，prev_platform_node_id 记原节点（残留指引，§2.8）
落库：placements 换绑（state=ok）+ 事件 placement.changed + 审计（同事务，fail-closed）
后续：由用户发起部署收敛（换点不自动部署）
```

**卷前哨多节点化**：`VerifyVolumesOnNode` 逻辑不变（对比 volumes 行节点 vs 目标节点）——单机恒短路的路径在多节点成为真实闸门：跨点部署/换点未声明数据处置 → 409。绑定节点 drain/down/paused → `Preflight` 快速失败（`E_PLACEMENT_NODE_UNAVAILABLE`）语义已在 v0.1 实现，多节点直接生效。

### 2.7 节点观测接入与身份锚定（nodes 表多节点化）

**锚定 duty（新，`state` 包）**：观测拍（observer resync / 事件驱动失效）后运行 `ClusterAnchor.Reconcile(snapshot)`：

1. 快照中存在无 `fleetly.node-id` label 的节点 → 铸造 `n_<ULID>` → 写 label（写前直读版本令牌 + 冲突重试，沿用 `NodeIdentity` 的既有纪律）→ `UpsertRuntimeNodeRef` → 审计 `node.identity_created/anchored` → 事件 `node.joined`。
2. label 已存在 → 以 label 值登记 ref（**labels 是持久载体（raft 复制），SQLite ref 可整表重建**——L1/L2 恢复后由本 duty 从 label 反建，与状态模型 §2.3 一致）。
3. 冲突（swarm node 已映射到另一平台 ID / label 值撞已有映射）→ **不自动消解**：事件 + 管理面警示，人工走 rebind 路径（D-PLC-6 人工 rebind 语义的集群版）。

**平台身份不落新表**：worker 平台 ID 的权威载体 = swarm node label；`runtime_node_refs` 既有表承载映射；nodes 观测缓存行从 labels JSON 提取 platform_id（现状即如此，零 schema 变更）。

**node.* 产品事件接入**：observer 快照差分产生（v0.2 解除「不产生产品事件」的 v0.1 注记，事件名全部为注册表既有预留）：`node.joined`（新 swarm 节点 + 锚定完成）、`node.down`/`node.up`（state ready↔down 转移，Swarm 失联判定语义，文案纪律沿用）、`node.removed`（快照消失）、`node.availability_changed`（active↔drain/pause，**新增码**，§5——drain 维护窗口叙事的载体）。逐转移发事件、不做防抖（诚实：每拍转移都可见）。

**stale 语义**：不变——`observed_at/stale` 读契约、`last_seen_at` 平台观测语义、不承诺心跳、底座不可达整表置陈旧（状态模型 §2.2 逐字沿用）。

**NodeView 增补**（`/v1/system/nodes`，proto 加法）：`pinned_app_ids`（读时 join placements 权威表，UI「已钉应用」交叉引用；无迁移）。

### 2.8 restic 迁移 + rebind CLI 与孤儿卷指引

**操作流（D-MN-10：平台半 + 用户数据半，平台不编排远端数据移动）**：

```
fleetly placement migrate <app> --to <node>     # 服务端 GetPlacementMigrationPlan 生成（read scope）
  ↓ 打印 runbook（真实卷名/节点名/镜像钉版填充）：
  1. 停写建议：docker service scale fleetly-<app>-<svc>=0（manager 执行；漂移检测会如实告警，维护窗口语义）
  2. 源节点：docker run --rm -v <vol>:/data -v <repo>:/repo restic:<pin> backup …（repo = S3〔E3 后〕或 SFTP/本地路径〔E3 前，用户参数〕）
  3. 目标节点：docker run --rm -v <vol>:/data -v <repo>:/repo restic:<pin> restore …
  4. fleetly placement rebind <app> --node <node> --data-restored      # UpdatePlacement（admin）
  5. fleetly deploy（回岗）；验证后按指引清理源节点残留卷（docker volume rm，§孤儿指引）
```

- **前哨检查**：rebind 前平台侧校验目标节点 ready、卷行变更登记（§2.6）；rebind 后首次部署经 `VerifyVolumesOnNode`（数据声明已由 `--data-restored` 表达，放行）。
- **失败回退**：rebind 前失败 = 放弃迁移（卷未动、绑定未变，零成本）；rebind 后回退 = 再 `rebind --node <原节点> --data-restored`（原节点物理卷仍在——残留不删除纪律）+ 重新部署。
- **CLI 动词**（词汇表纪律：资源单条 get/复合 show/列 list）：`placement rebind`、`placement migrate`（复合视图）、`volumes list [--orphaned]`。

**孤儿卷清单与清理指引**：新 RPC `ListVolumes`（`GET /v1/volumes?status=`，read scope）——跨 app 卷清单：active 行、orphaned 行（删除应用保留）、**残留行**（`prev_platform_node_id != ''` 的 active 行派生 `residual` 标记，指向源节点待清理）。CLI/Console 输出附清理指引（在所属节点上 `docker volume rm`，平台对账消失）——**不建生命周期 API、不做远端删除**（D18；放置专项 §7 例外纪律不扩张）。

**迁移 00010（唯一 DB 迁移，只加）**：`volumes` 增列 `prev_platform_node_id TEXT NOT NULL DEFAULT ''`。

### 2.9 HA 边界向导（诚实口径，无新 API）

Console 节点页固定卡片 + join 向导终态页 + `fleetly nodes join-guide` 输出尾部，内容 = 架构 §2.6 口径的 UI 化（静态文案 + 运行时拓扑填充）：

| 2 台得到 | 2 台得不到 |
|---|---|
| 无状态服务进程级 HA（失联 ~13s 判定、~19s 完成重调度；重调度窗口内该 app 短暂不可用，如实口径） | 管理面 HA（1 manager；quorum=2 时任一台失联管理即不可用，**不做 2 manager**；管理面 HA 需 3 manager） |
| 节点可 drain，无状态负载维护新连接零失败（连接级重试语义；在途连接可能中断一次，指引先摘 DNS） | 有状态 HA（local 卷不跟随；DB 所在节点失联 = 该库不可用，恢复走备份重放 + rebind） |
| 控制面故障不影响应用运行（应用运行不依赖控制面） | 镜像分发 SPOF（zot 钉 manager：manager 失联期间新任务/回滚拉取失败，运行中应用不受影响） |
| — | 配置通道 SPOF（manager 不可达时各节点 Traefik **冻结最后一份好配置**——入口不坏但配置不变，设计使然） |
| — | 入口冗余 = 连接级重试，非健康驱动故障转移、非 VIP |

口径纪律：**「2 台 ≠ 全面 HA」**必须在 join 向导与节点页显式呈现，防止有状态单点被当产品缺陷（架构 §2.6 原文要求）。

### 2.10 资源预算（600MB 红线多节点口径）

| 项 | 口径 |
|---|---|
| zot | **计入 manager 平台组件 idle 总量（<600MB，V2-1）**；引入前实测 idle（FZ-9 纪律：先测再定；预估 30-60MB）；实测超顶触发降级讨论（降级选项：zot 退「join 前惰性部署」形态，即 D-MN-5 的被否方案回升为降级路径） |
| Traefik-on-worker | 计入 **worker 侧单列预算**（≈ dockerd+swarmkit+containerd+Traefik ≈ 250MB 量级，对照[校准报告](../reports/2026-09-19-resource-calibration.md)基线），**不与 manager 600MB 合并计算**——worker 不跑 fleetlyd，引擎常驻非平台选择 |
| fleetlyd | 不变（单写点、无新增常驻 Go 进程；zot/ Traefik 均为 Swarm service） |
| 验收门 | E1 验收含 2 节点形态全栈 idle 实测（manager 含 zot <600MB 断言 + worker 侧如实读数入报告） |

## 3. 关键裁决（D-MN-*）

| # | 裁决 | 理由 | 被否方案 | 终裁 |
|---|---|---|---|---|
| D-MN-1 | join 安全通道 = 标准 swarm join-token 手动复制 + 节点锚定完成后**自动 rotate**（`join.token_rotate=auto` 默认；批量场景 manual） | Swarm 原生无 join 钩子，平台无法拦截成一次性语义；auto-rotate 把泄露窗口收敛到分钟级，且零新机制 | 自研一次性短时效 token broker（需要平台没有的远端接入通道，违反 D12/D18）；长期不 rotate（token 泄露 = 永久 join 能力） | — |
| D-MN-2 | worker 组件面 = 零 fleetly 安装物（Engine + Traefik global 任务；exec 中继 W5 经 global 自然覆盖） | 成员管理/心跳/分发全归 Swarm（D12）；「加节点」的全部动作 = join + 防火墙/DNS | worker 侧 agent/配置文件（自研 node 协议回潮，D12 明确否决）；worker 跑 fleetlyd 副本（多写点，红线 2） | — |
| D-MN-3 | Traefik 配置通道 = `https://ctrl.<base>:8423/configs`（fleetlyd 自持 TLS、平台证书、A 记录仅指 manager、不经 Traefik）；8422 保留明文挑战面 | 冷启动无循环（Traefik 容忍 provider 不可达 + 静态挑战路由明文无秘密）；多节点动态配置含内联证书私钥，明文跨公文不可接受（架构 §2.6「跨公网走平台域名 HTTPS」的落地形态） | 穿 Traefik 443 反代（冷启动循环：无配置则无路由则无配置）；明文 8422 公网（私钥泄露）；overlay 内通道（fleetlyd 是 host 进程进不了 overlay；容器化控制面 = 部署形态重构，超 E1 范围） | — |
| D-MN-4 | 证书分发 = 统一切动态配置内联（certContent/keyContent），v0.1 卷 + seed 容器退役（含既有安装迁移票） | 命名卷是 per-node 本地卷，证书到不了 worker——provider 是唯一配置通道；统一单轨消灭单/多节点双模型漂移 | 双轨并存（单节点卷/多节点内联，两套分发长期漂移）；per-node 卷同步（无远端通道） | — |
| D-MN-5 | zot = Swarm service 钉 manager + 本地卷 + `fleetly-system` overlay + Basic Auth（平台生成凭据，`--with-registry-auth` 分发）；**`base_domain` 配置即部署** | join 前已就绪（避免 join→首部署间多一步依赖）；zot 是 placement 绑定的第一个平台级用户（同一锚）；Basic Auth + 经 443 公网暴露面收敛 | 仅多节点时部署（join 流多一步部署依赖、单/多管线分叉）；无鉴权 zot（公网任意推拉镜像）；自签 + insecure-registries（架构已否：需远端 daemon 配置通道） | ⚖️（单节点填域名即付 zot idle，预算取舍需用户确认） |
| D-MN-6 | 证书次序 = ensure-平台证书 duty（启动重试）先行，Traefik 空配置容忍期仅静态路由；zot 部署不依赖证书 | 打破次序循环的唯一无状态排列；平台证书（多 SAN： ctrl+registry+console）一次签发覆盖三子域，续期同批 | 等首个应用域名证书顺带（次序耦合到用户行为）；每子域独立证书（LE 请求配额浪费、续期风暴——每节点独立 ACME 被否理由的同族） | — |
| D-MN-7 | placement 候选集 = 底座直读全量节点快照；label 值域 = 唯一显示名或平台 ID（歧义 → INVALID + 提示平台 ID）；选点三因子（数据引力 > 已钉数 > 平台 ID 序） | 决策路径禁读观测缓存（状态模型 §2.2 逐字）；三因子可解释可测试（D-PLC-7 沿用，仅末位从「名称序」明确为「平台 ID 序」——显示名可重名） | 读 nodes 缓存做决策（违反读契约）；多因子加权评分（无实测收益，D-PLC-7 已否） | — |
| D-MN-8 | worker 平台身份 = manager 锚定 duty 收编（铸造 `n_<ULID>` → label → ref → `node.joined`）；持久载体 = swarm label（raft），SQLite ref 可重建；冲突不自动消解 | 平台 ID 是绑定锚，必须先于任何 placement 存在；label 经 raft 复制天然多副本；worker 无组件无法自铸 | worker 自铸身份上报（违反 D-MN-2）；hostname 锚定（重名，D-PLC-2 已否）；全量入 SQLite 权威表（双写者，D-STM-1 已否） | — |
| D-MN-9 | 节点只读面 = 复用 `/v1/system/nodes`（SystemService.ListNodes）+ NodeView 增补 `pinned_app_ids`；**不建 `/v1/nodes` 路由** | ListNodes v0.1 已实现且含全部读契约字段；同数据双路由 = 契约 duplication；proto 加法即可满足 E1 面 | 新增 `/v1/nodes`（放置专项 §2.3 文档路径——重复路由，需修订文档）；不动 NodeView（缺已钉应用交叉引用，UI 需求缺口） | ⚖️（文档路径不一致，需裁决轮追认 + 修订放置专项 §2.3） |
| D-MN-10 | 迁移 = `placement migrate`（服务端生成 runbook，用户在两节点执行 restic）+ `placement rebind`（平台半：绑定/卷登记/前哨/审计）；平台不编排远端数据移动 | manager 无远端 docker.sock 通道（放置专项 §7 纪律）；exec 中继在 W5 且仅 exec 不扩张；数据安全处置必须是用户显式声明（D-PLC-5） | 平台远端编排 restic（需远端通道，纪律否决）；等 E3 S3（迁移不应依赖 S3，SFTP/本地路径已可用）；只做 rebuild（丢弃数据的隐式迁移，危险） | — |
| D-MN-11 | registry 模式部署前哨 = manifest HEAD；新码 `E_REGISTRY_UNAVAILABLE`（不可达）/`E_REGISTRY_PUSH_FAILED`（推送失败）；manifest 缺失复用 `E_IMAGE_UNAVAILABLE` | 修复建议分层：代码错→改代码、registry 错→查 zot/网络/凭据、镜像缺→重建（错误信息即产品）；缺 manifest 与「回滚目标不可得」同语义 | 全复用 E_BUILD_FAILED/E_RUNTIME_UNAVAILABLE（诊断建议混淆） | — |
| D-MN-12 | 预算口径：zot 计入 manager <600MB；worker 侧单列（dockerd+containerd+Traefik，无 fleetly 组件） | V2-1 的 600MB 定义「平台组件总量」——zot 是平台组件；worker 引擎常驻非平台选择，合并计算会虚化红线 | zot 不计预算（开洞）；worker 并入 600MB（平台红线被非平台组件稀释） | ⚖️（600MB 为用户直裁，多节点口径扩展需确认） |
| D-MN-13 | join 门禁：`base_domain` 为空 → `E_MULTI_NODE_REQUIRES_BASE_DOMAIN`（409） | V2-7「启用多节点时必填」的执行点；配置缺失显式拒绝、不静默降级 | 静默允许无域名 join（provider 通道/zot 均不可用，join 后入口残缺）；隐式从应用域名猜测（猜测即事故面） | — |
| D-MN-14 | 证书/ACME 私钥 age 封装：**不引入**（W2 挂账评估闭环） | 多节点同步已由传输 TLS（8423）+ token 覆盖；落盘面 0600 PEM + 主密钥文件形态与 v0.1 同护栏；封装把解密依赖链引入启动路径，收益低于复杂度 | cert_dir age 信封加密（启动路径新依赖 + 恢复流程新前置，v0.2 无对应威胁模型增量） | — |

## 4. v0.2 切面与验收

### 4.1 票据分解（每步验证方式 + 回滚路径）

| 票据 | 内容 | 验证 | 回滚 |
|---|---|---|---|
| E1-1 安装项与配置 | install.sh `--base-domain`（写入 config + 安装报告子域/DNS 行）；config `base_domain`/`registry.*`/`join.token_rotate`/`ingress.config_tls_addr` 键位 | deploy/test-install.sh 增用例（含缺省不填 = v0.1 逐字不变断言） | 删配置键即回单节点形态 |
| E1-2 证书内联切换 | 动态配置内联证书；seed 容器/卷挂载退役；既有安装迁移（卷证书导入台账） | ingress 单测（视图含内联 PEM）+ 实机：既有 v0.1 安装升级后证书仍服务 | 升级前快照恢复（v0.1 二进制 + 配置） |
| E1-3 配置端点 TLS 面 | 8423 HTTPS（平台证书）；Traefik endpoint 切换；ensure-平台证书 duty（重试） | 实机 bootstrap 次序演练（§2.5 五步）+ Traefik 容忍期断言 | 配置回退 8422 形态（单节点行为） |
| E1-4 zot 部署器 | service 幂等收敛 + `fleetly-system` 网络 + 凭据文件 + 路由段 + prepull 台账 | 实机 push/pull 经 `registry.<base>`；spec 漂移收敛测试 | `docker service rm fleetly-registry`（卷保留；镜像引用回本地模式） |
| E1-5 构建推送与前哨 | solve push + digest 记账 + `--with-registry-auth` + manifest HEAD 前哨 | 多节点部署：worker 任务拉取成功；zot 停机 → 前哨 `E_REGISTRY_UNAVAILABLE` 快速失败 | base_domain 清空 → 本地管线（v0.1 路径常驻） |
| E1-6 锚定 duty 与 node.* 事件 | `ClusterAnchor.Reconcile` + 五事件接入（含 availability_changed） | 单测（冲突/无 label/反建）+ 实机 join 收编断言 | 事件只增；duty 独立，可配置禁用（降级面） |
| E1-7 placement 多节点 | candidates/解析/三因子选点/守卫退役/UpdatePlacement + rebind CLI + migrate plan + ListVolumes + 迁移 00010 | 放置专项 §6 单测面扩展 + V6a/V6b 多节点复跑 | 00010 只加列（空串 = 现状）；守卫删除经还原点提交可回退 |
| E1-8 join 向导 | GetJoinGuide/RotateJoinToken + CLI/Console 向导 + HA 边界卡片 + 端口规则生成 | 向导全流实机走查 + token rotate 断言（旧 token join 失败） | 纯增量面（API/CLI/UI），无状态迁移 |
| E1-9 演练脚本化 | `e2e/multinode-rehearsal.sh`（§4.2；真 VPS 复用 W1 路径，dind 代演进 nightly） | 三断言全绿 + 预算实测 | — |

### 4.2 验收（锚定架构 §4.3 三断言）

| # | 断言 | 通过标准 |
|---|---|---|
| A | 2 节点拓扑上线 | worker join → 锚定（`node.joined` + 平台 ID）；无状态 app 双节点可调度；zot 可达（push/pull 经 `registry.<base>`）；三平台子域 DNS verify 通过；manager 全栈 idle 实测 <600MB（含 zot）并落报告 |
| B | 无状态节点 drain 新连接零失败 | 摘 DNS（TTL 生效后）→ `docker node update --availability drain` → 外部探测循环（带 A 记录重试语义）**新连接 0 失败**；在途连接允许中断一次（如实记录）；回岗后任务回迁不受阻（无自动回迁，Swarm 语义） |
| C | 有状态节点 drain→回岗自动回绑 | 写入数据 marker → drain → 应用 `blocked`（`placement.blocked` + 任务 PENDING）→ availability active → `placement.recovered` 自动回绑 → marker 读出一致（本地卷数据不丢） |

前置门（Spike 验证项，V-MN）：Traefik http provider + 动态配置内联证书（certContent/keyContent）组合验证——不通过则 §2.4 证书通道需复议（盲点 §之首）。

## 5. 契约面

### 5.1 proto 加法（buf breaking FILE 规则内，只加）

| 文件 | RPC / 字段 | HTTP | scope |
|---|---|---|---|
| system.proto | `SystemService.GetJoinGuide` → `JoinGuideView`（join_command / manager+worker 防火墙规则列表 / DNS 步骤 / 完成判据） | `GET /v1/system/nodes/join-guide?worker_ip=` | admin（含 token 材料） |
| system.proto | `SystemService.RotateJoinToken` | `POST /v1/system/nodes/join-token:rotate` | admin |
| system.proto | `NodeView` 增 `repeated string pinned_app_ids = 10` | —（ListNodes 增强） | read（既有） |
| placement.proto | `PlacementService.UpdatePlacement`：`{app, node, data_ack, confirm}` → Decision 投影 | `PUT /v1/apps/{app}/placement` | admin（破坏性） |
| placement.proto | `PlacementService.ListVolumes`：`{status?}` → VolumeView 集合（增 `residual`/`prev_platform_node_id` 字段） | `GET /v1/volumes` | read |
| placement.proto | `PlacementService.GetPlacementMigrationPlan`：`{app, to}` → runbook 步骤结构 | `GET /v1/apps/{app}/placement/migrate-plan` | read |

CLI 新动词：`nodes join-guide`、`nodes rotate-token`、`placement rebind`、`placement migrate`、`volumes list [--orphaned]`（全 `--json`；rebind 强制 `--confirm-destructive` + 回显）。MCP 面（E2）后置，nodes/volumes 读工具计入 ≤30 预算。

### 5.2 错误码（注册表只增）

| 码 | HTTP | 语义 | 复用/新增 |
|---|---|---|---|
| `E_MULTI_NODE_REQUIRES_BASE_DOMAIN` | 409 | 多节点未启用（base_domain 缺失），join 面显式拒绝 | 新增（D-MN-13） |
| `E_REGISTRY_UNAVAILABLE` | 503 | registry 模式部署前哨：zot 不可达（快速失败不排队） | 新增（D-MN-11） |
| `E_REGISTRY_PUSH_FAILED` | 500 | 构建推送平台 registry 失败（网络/凭据/registry 故障） | 新增（D-MN-11） |
| `E_IMAGE_UNAVAILABLE` | 500 | registry 模式 manifest 缺失（同「回滚目标不可得」语义） | 复用 |
| `E_IMAGE_PULL_FAILED` | 500 | 任务拉取失败（worker 侧） | 复用 |
| `E_PLACEMENT_*` 八码 + `E_VOLUME_NODE_MISMATCH` | — | 多节点解析/前哨/换点全部复用（词面已多节点就绪） | 复用 |
| `E_CAPABILITY_REQUIRES_MULTI_NODE` | 400 | 码保留永不复用；summary「v0.1 single-node」措辞维护为拓扑/配置语义（注册表文案维护项，契约轮追认） | 退役面 + 保留码 |

### 5.3 事件与审计

| 事件 | 状态 | 说明 |
|---|---|---|
| `node.joined` / `node.down` / `node.up` / `node.removed` | 既有预留，v0.2 起由 observer 差分发出 | 措辞纪律沿用（「Swarm 失联判定」，非心跳） |
| `node.availability_changed` | **新增** | active↔drain/pause 转移（drain 维护窗口叙事载体；载荷 old/new） |
| `placement.changed` / `volume.discarded` / `volume.orphaned` | 既有预留，rebind 路径发出 | — |
| zot 部署 / 证书签发与续期 / join-token rotate | 审计记录，**不设事件**（与 Traefik 部署、证书签发同纪律） | 新审计动作：`registry.deployed`、`node.join_token_rotated`、`placement.rebind`、`node.join_guide_issued` |

DB 迁移：仅 `00010_volumes_prev_node.sql`（加列，只加纪律）。

## 6. 与既有文档一致性声明（逐条）

| 纪律 | 本设计切面 | 确认 |
|---|---|---|
| 单写点（红线 2 / D-STM-1） | fleetlyd 仍为唯一 SQLite 写者；worker 平台身份载体 = swarm label（Swarm 自己的对象），锚定 duty 是唯一写 label 的平台方（沿用 v0.1 NodeIdentity 纪律）；zot 状态不进 SQLite（substrate 实况 + service inspect） | 不违反 |
| 诚实契约（§4.2 状态诚实） | nodes 观测缓存 stale 语义逐字沿用；无心跳承诺；HA 边界向导如实呈现得不到项；E_REGISTRY_* 快速失败不静默；drain 停机窗口如实 | 不违反 |
| 轻单核（红线 2） | 零新增常驻 Go 进程；零自研协议/调度/成员管理（D12）；新组件仅 zot（Swarm service，计预算）；worker 零 fleetly 物 | 不违反 |
| 只增（红线 3） | proto 全加法；错误码/事件只增（E_CAPABILITY… 保留退役面）；DB 仅加列；`fleetly.*` label 契约不新增用户可见键（`fleetly.node-id` 既有） | 不违反 |
| 绑定模型（放置专项 §2） | 意图/绑定/执行三层不动；平台节点 ID 为锚（worker 侧由锚定 duty 扩展，同一机制）；节点消失不迁移；跨点唯一路径 = 备份恢复 + 显式数据处置 | 不违反 |
| 恢复等序（状态模型 §2.7） | 平台身份从 label 反建（L1/L2 均可重建锚定）；zot 数据明确不进控制面备份且不阻碍恢复（镜像重建路径文档化） | 不违反 |
| 端口加固（架构 §4.2） | join 向导按 worker IP 生成精确规则（该条的 v0.2 兑现）；`--harden-firewall` 自动应用维持 reserved | 兑现且不越界 |

## 7. 明确不做（v0.2 多节点切面外）

- 多 manager / 管理面 HA / 多写点（quorum=2 陷阱；3 manager 成本另计，v0.3+）
- 节点生命周期 API（drain/remove/rename 用 `docker node` + 向导/文档指引，D18）；自动故障转移/自动换点/自动迁移/自动回迁/rebalance
- 自研 join token broker（一次性短时效 token）、节点 adopt/身份自动消解/machine-id 判定
- 远端卷维护作业（restic 编排、卷删除校验——迁移 runbook 用户执行；执行中继例外条款不扩张）
- registry cache 导出（构建缓存维持本地；优化后置评估）、多架构应用镜像一次构建（v0.2 评估项，混合架构集群的 app 部署限制如实文档化）
- 强入口可用性（keepalived/VIP/云 LB 进产品——文档配方口径）；DNS-01/通配符证书（v0.3）；面板强绑定子域（8420 直连保留）
- 证书/ACME 私钥 age 封装（D-MN-14 评估结论：不引入）
- 控制面容器化为 Swarm service（host 进程形态维持；如未来触发另立设计）
