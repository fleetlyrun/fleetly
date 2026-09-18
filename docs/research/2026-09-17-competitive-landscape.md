# 竞品调研：轻量自托管 PaaS 的六个关键问题

| 状态 | 日期 | 关联 |
|---|---|---|
| 已完成 | 2026-09-17 | 输入给 [平台架构设计](../design/2026-09-17-architecture.md)；第 10 节 13 条修订建议已于 2026-09-17 全部应用。**后续修订注记**：本文第 3 节结论 3/4 与第 10 节第 4/5 条中关于自研 `fleetly.yaml` spec（YAML+JSON Schema、SchemaStore）的建议已失效——应用模型最终选定 Compose 规范（D14 重写，见架构 §2.4） |

> 术语注（2026-09-17 追加）：本文写作时沿用行业通用词「agent」，按语境分别指基础设施的节点守护进程（现名 `fleetlyd node`）或 AI Agent。正式术语基线见架构文档 1.1 节；本文作为档案不改写正文。

> 事实更正（2026-09-17 追加，经专项验证）：第 2 节与 7.3 节中「Swarm 生态萎缩 / 活跃开发停止」的表述需修正——Swarm 仍处于引擎内置维护：Docker Engine 29.x（2025-11→2026-09）有约 21 条 Swarm/overlay 修复，未进 deprecated 清单，nftables 的 Swarm 支持在官方路线图上；swarmkit v2.1.2（2026-04）；Mirantis 2025-07 将支持延长至至少 2030（MKE 3）。准确表述是「低频维护、无新特性、依赖 Engine 升级、周边工具单薄」。详见 [Swarm 底座可行性评估](2026-09-17-swarm-substrate-assessment.md)。另：§10 第 12 条建议「不做 Swarm」及第 3、4 条建议已随 D12（采纳 Swarm）/D14（应用模型 = Compose）失效，以[平台架构设计](../design/2026-09-17-architecture.md)为准。同理，§2 结论 3/4、§7.1.4/§7.2.3 与借鉴项 A5/A6/A7 中「出站 agent」方向的建议亦已随 D12 失效——节点触达、心跳与重连由 Swarm 原生承担（2026-09-17 审核轮补注，档案保留原文）。

## 0. 边界与方法

**核心问题**：验证 fleetly 最未经验证、且决定成败的六个设计点，找收敛点（照搬）、分歧点（差异化）、死亡区（坑）。

**候选三层**：
- 直接竞品：Dokku、Coolify、Dokploy、CapRover、Kamal；
- 相邻实现：Portainer Edge Agent、Nomad、Rancher、Fly/Railway/Render、ArgoCD/Flux、Docker MCP Gateway、K8s MCP 生态；
- 先例与尸检：Flynn、Deis、Waypoint、Heroku Preboot、ECS+CodeDeploy 蓝绿、Nixpacks 维护模式、MinIO/Dokploy 许可证事件。

**方法**：6 个互不可见子代理并行调研，优先一手来源（官方文档、仓库代码、issue、changelog）；社区来源（Reddit/HN/论坛）用于差评。约 60 条可追溯来源。证据强度标注 [强/中/弱]：强 = 官方文档/代码/公开实测数字；中 = issue/讨论与多来源交叉；弱 = 单一样本或博客叙述。

**注**：本文中所有 URL 均为 2026-09-17 采集。

## 1. 零停机发布 / 回滚 / health gate

| 对象 | 切换机制 | health gate 语义 | 回滚 | 关键坑 |
|---|---|---|---|---|
| Dokku | 双容器 + nginx/proxy 热切 | 默认仅 10s 进程存活；HTTP check 失败 5 次判失败 | **核心无 rollback**（issue #8938 仍开放） | 路由标签在容器创建时注入 → gate 拦不住，实测每次部署约 4s 内 31 个 502 [强] |
| Kamal 2 | kamal-proxy 热切 + drain | GET /up，1s 间隔 / 5s 超时；cord 文件可强制不健康 | `kamal rollback <sha>`，本地缓存镜像零下载；旧容器保留 3 天 | `kamal proxy reboot` 官方承认小段停机；drain 后仅 10s 优雅期（#1768） |
| Coolify v4 | 双容器重叠（仅部分构建类型）；compose 退化为先停旧 | 无 check 则「启动即 ready」 | **手动**选保留镜像；官方明示不回滚 DB 迁移/持久数据 | compose 部署 20–60s 停机（#7313）；shallow clone 导致回滚失败（#8445） |
| Dokploy | Swarm `start-first` service update | Swarm health + StartPeriod 30s / Retries 3 | `FailureAction: rollback` 可自动；registry 模式可回任意版本 | Traefik keep-alive 越过 Swarm 成员变更 → 滚动期报错（#5281，2026-09 仍 open）；单个坏 app 的 Traefik YAML 搞挂全部 app 的 watcher（#5189） |
| CapRover | Swarm：无卷 start-first，**有卷必然 stop-first** | 完全外包给 Dockerfile HEALTHCHECK | 默认 `pause` 不自动回滚；rollback=重建旧版 | 官方承认有卷停机；用户「100% 会遇到 502」#661 |
| Render / Railway / Fly | 双实例重叠 + health gate + 60s 杀旧 | healthPath 或端口监听；Railway 默认 300s 超时 | 失败=取消部署保留旧版；`heroku rollback` 式补救 | **持久磁盘/volume 全行业禁用零停机**；Fly bluegreen/canary 不能挂 volume 且部署期约 2x 资源 |

**结论**：
1. 骨架收敛：**新容器先起 → health gate → 切流 → 旧容器保留窗口（60s 量级）**，7 个产品无一例外 [强]。
2. 「**失败 = 不切流量**」比「切了再自动滚回」更普遍；真正默认自动回滚的只有 Dokploy（Swarm FailureAction），其余靠显式命令 [强]。
3. 回滚语义收敛：**重放旧镜像（immutable artifact）**，不反向执行；DB 迁移不归部署系统回滚，均要求用户自担 [强]（Coolify 文档、Heroku Preboot 建议 schema 变更前关闭）。
4. 死亡区：**持久卷/固定端口/固定容器名与双实例重叠天然互斥**（CapRover/Railway/Render/Fly/Coolify 四条件全部佐证）——「通用零停机」到 stateful 应用必失效，必须文档化降级 [强]。
5. 死亡区：**路由注册不得早于 health 通过**（Dokku #8974 的直接反例）；**单应用配置错误不得影响全局入口**（Dokploy #5189 全站故障）[强]。
6. 死亡区：**外挂一套独立编排实现蓝绿**——AWS 2025-07 用原生 ECS 蓝绿取代 CodeDeploy 方案，官方措辞是让「complex workaround」不再必要 [强]。

来源：https://dokku.com/docs/deployment/zero-downtime-deploys · https://github.com/dokku/dokku/issues/8974 · https://kamal-deploy.org/docs/commands/rollback · https://coolify.io/docs/applications/deployments/rolling-updates · https://github.com/coollabsio/coolify/issues/7313 · https://docs.dokploy.com/docs/core/applications/rollbacks · https://github.com/Dokploy/dokploy/issues/5189 · https://caprover.com/docs/zero-downtime.html · https://render.com/articles/how-render-handles-zero-downtime-deploys.md · https://www.infoq.com/news/2025/07/aws-blue-green-ecs

## 2. 多节点与 agent 模型

| 对象 | 控制面→节点 | 失联行为 | 镜像分发 | 关键坑 |
|---|---|---|---|---|
| Coolify 4.3.x | SSH 推 + 2026-08 新增 **Sentinel 出站心跳**（v4.3.19 起强制） | 应用继续跑；连续 2 次检查失败才标 unreachable；Sentinel 失效回落 SSH | 默认目标机本地构建；多机需 registry | SSH 掉线/密钥/防火墙问题长期高发（#5284/#3664）；Sentinel 是对 SSH 弱点的官方补救 |
| Dokploy | 单机 / SSH Remote / Swarm 三模式 | 应用继续跑，管理与部署失败 | 多机必须 registry | **registry 凭据不同步到 worker 导致拉取失败**（#3111/#3886 仍 open）；Swarm manager 单点 |
| Kamal | 纯 SSH 命令式，无 agent | 无节点状态概念 | 必须 registry | 多机自动 TLS 不可用（官方限单机）；无调度 |
| Portainer Edge Agent | **agent 每 5s 出站轮询 + 反向隧道（8000）**；空闲 5 分钟关隧道并吊销凭据 | 自动重建；快照回传 | registry 拉取 | 服务端一次流错误进入永久死锁需重启（#13198）；心跳绿但无法交互（#8689） |
| Nomad | client 出站心跳；阈值随规模放大（10 节点 10–20s） | **默认 lost + 重调度**；要「不迁移」必须显式 `disconnect.lost_after` + `replace=false` | registry 拉取 | 心跳误判导致任务重启是社区常见工单；永久下线节点滞留 disconnected |
| Rancher | agent 出站 WebSocket（remotedialer） | UI Disconnected，负载继续跑 | registry 拉取 | **中间 LB 断 WebSocket 后 agent 不自动恢复**（#55931）[强] |

**结论**：
1. 方向验证 [强]：**「节点主动出站」是控制面可达性的主流解法**——Portainer（推→拉 官方架构决策）、Rancher、Nomad、Coolify Sentinel 四条独立路径收敛；未发现任何「agent→SSH」的成功反向迁移案例。
2. 出站模型的主流失败面是**长连静默假死**：Rancher 不自动恢复、Portainer 服务端死锁。必须在协议层做心跳+超时判死+agent 主动重建+服务端请求级超时 [强]。
3. 「degraded 不迁移」需要显式设计：Nomad 的对应物是 `disconnect.lost_after` + `replace=false` + 恢复后对账防双跑；默认行为恰好相反（lost+重调度）[强]。
4. 心跳阈值宁宽勿窄：秒级心跳 + 数十秒判定窗 + **连续 N 次失败**（Coolify 两次）；阈值越短越容易误判（Nomad 官方表格）[强]。
5. 多机镜像分发的事实标准是 **registry pull**（Dokploy/Kamal/Dokku-k3s/Coolify 全部要求）；凭据应经控制面/agent 通道下发，避免用户手工逐机 docker login（Dokploy 的反复坑）[强]。
6. 每节点本地反向代理（而非中心入口 LB）是轻量阵营共识，与 fleetly 设计一致 [强]。
7. 死亡区：**全自建（agent + overlay + 自托管状态存储）的运维与支持成本**是 Flynn 死亡的重要背景；Deis v1 的 fleetd/etcd 自研编排被 K8s 路线取代 [中，历史复盘]。

来源：https://docs.portainer.io/advanced/edge-agent · https://github.com/portainer/portainer/issues/13198 · https://github.com/rancher/rancher/issues/55931 · https://developer.hashicorp.com/nomad/docs/job-specification/disconnect · https://coolify.io/docs/core/observability/monitoring/sentinel · https://docs.dokploy.com/docs/core/deployment-options · https://github.com/Dokploy/dokploy/issues/3111 · https://kamal-deploy.org/docs/configuration/proxy/ · https://news.ycombinator.com/item?id=26295065

## 3. 声明式配置 / plan-apply / 漂移检测

| 对象 | spec 位置 | plan/diff | 漂移检测/收敛 | UI 与 Git 冲突 |
|---|---|---|---|---|
| Railway | `.railway/railway.ts`（TS DSL，从 toml/json 迁移） | **完整 plan/apply/pull/migrate**；`--json`、三态 exit code、plan artifact + etag 防 stale、`--confirm-destructive` | plan 时显式比对（非后台） | **主动禁止双真源**：被 CaC 管的服务阻塞 plan 直到迁移 |
| Render | render.yaml（项目级） | 仅 validate + Dashboard 变更预览 | sync 时收敛 | 冲突的 UI 改动下次 sync 被覆盖；文档明示 |
| Fly | fly.toml（部分声明式） | 仅 validate/show | 无 | deploy 重置 UI/CLI 手改，长期混乱 [差评] |
| Kamal | deploy.yml | 无（**官方明确拒绝 reconcile**） | 无 | 无 UI |
| Coolify / Dokploy | compose 文件（compose 部署）/ 平台 DB（service 模板） | 无 | 无 | Coolify 声明 compose 为唯一真源；Dokploy UI/DB 优先 |
| ArgoCD / Flux（相邻） | Git manifests | diff / OutOfSync | **持续对账是标配**，`selfHeal` 自动 revert | selfHeal 在事故中被用户强制关闭（#13598）[强差异教训] |

**结论**：
1. **自托管 PaaS 阵营没有任何产品做持续漂移检测与收敛**——这是差异化空白区，同时是未规模化验证的风险区 [强]。K8s GitOps 证明需求存在，也暴露代价（self-heal 事故、用户误读 Synced）。
2. plan 的黄金语义已被 Railway 做全：`--json`、无变化/有变化/错误三态 exit code、变量脱敏、plan artifact + configEtag 防 stale、破坏性操作 `--confirm-destructive`。**照抄这套语义**，面向 agent 优先 [强]。
3. spec 形态：**YAML + JSON Schema 是业界默认**（Render 把 schema 发布到 SchemaStore 供 IDE 校验）；Railway 转 TS DSL 是表达力驱动的少数派路线，社区仍有迁移抱怨。agent 对 YAML/JSON Schema 的训练覆盖最好 [强]。
4. 双真源必须硬处理：反例（Fly 混乱、ArgoCD self-heal 事故）与正例（Render 明示覆盖、Railway 阻塞）都指向同一结论——**要么禁止、要么标注来源，不能静默双向合并** [强]。
5. 漂移检测与自动收敛应**拆成两个机制**（Terraform #35382 的规模化教训）：默认「检测 + 通知」，收敛 per-app opt-in，支持字段级豁免与事故窗口 [中-强]。
6. 死亡区：**Waypoint**——把「声明式应用描述」本身当价值，没有 plan/apply、定位抽象层错了（客户痛点在其上游的模板/目录），2024-01 归档 [强，官方复盘]。
7. 死亡区：**YAML 地狱是长期反弹趋势**（HN 多年热帖）；K8s 的回应是 KYAML 严格子集而非换语言——暗示 fleetly.yaml 应严格控制表达力，不做图灵完备 [中]。

来源：https://docs.railway.com/infrastructure-as-code · https://docs.railway.com/cli/config · https://render.com/docs/blueprint-spec · https://kamal-deploy.org/ · https://github.com/argoproj/argo-cd/issues/13598 · https://github.com/hashicorp/terraform/issues/35382 · https://www.hashicorp.com/blog/a-new-vision-for-hcp-waypoint · https://community.fly.io/t/clarification-on-machine-configuration-persistence-cpu-autostop-regions-vs-fly-toml/26955

## 4. MCP / Agent-native

| 对象 | 工具面 | 读写 | 鉴权/scope | 安全机制 | 事故/差评 |
|---|---|---|---|---|---|
| Dokploy 官方 | **546 工具 ≈74k tokens**（2026-08-08 实测） | 全写 | 实例级 x-api-key | 仅注解 + 类别 allow-list | Anthropic 因 draft-07 nullable 拒收全部 524 工具；**#46「我的 agent 删掉了整个 app」** |
| Dokploy 社区收敛版 | 27 工具（action 枚举）≈8.6–12.1k；gateway 4 工具；Code Mode 3 工具 ≈1.7k | 读写分离 | 同上 | 注解诚实；env 走单独逃生舱 | 官方版占 200k 上下文 1/3+ |
| Coolify 内置 | 起步 10 只读；PR #11000 后 45（含 deploy/stop） | 只读→受控写 | 平台 token 能力 read/deploy；lifecycle 需 admin/owner 角色；敏感日志 `read:sensitive` | `stop` 需 `confirm=true`；enable/disable 审计；响应默认脱敏 | 内置较新，无重大事故 |
| Coolify 社区 | 44 合并工具（~6.6k tokens）至 89 工具（被批） | 全写 | 平台 token | elicitation 播报爆炸半径 + `destructiveHint` | 89 工具帖被评「这就是 MCP 变贵的原因」 |
| Railway | 远程 **7 工具**；本地 32 | 读写（多步委托 railway-agent） | OAuth 2.1 按工作区/项目授权、可撤销 | **`confirm` 字段从 schema 隐藏** + 二次调用 | — |
| Vercel | 审核客户端白名单（12 个） | 读 + deploy + 购买 | OAuth 2.1 | **quote（idempotencyKey，5 分钟过期）→ confirm 回显价格**；官方警告 prompt injection | — |
| Portainer | 生成自 ~380 操作；社区 fork 98→15 meta-tool | `PORTAINER_READ_ONLY` 限 GET | 每客户端自带用户 API key + RBAC | env 值默认脱敏 | 官方 issue 承认 98 单工具对 LLM 太多 |
| K8s 生态 | toolset/allowlist 制 | `--read-only` 等 | OAuth scope + K8s RBAC | Kubernetes-MCP-Guard：plan→approve→execute | **CVE-2026-46519：过滤只在 tools/list 生效，tools/call 可绕过** [强反例] |
| Docker Gateway | 聚合网关 | 取决于接入 server | secrets/OAuth 集中托管 | 容器隔离、响应 secret 拦截、调用日志 | **工具名遮蔽 GHSA**：可把凭据静默转给恶意 server（v0.43.1 修复） |

**结论**：
1. 「**精选手写工具面**」取代「一端点一工具」已是公开收敛：Dokploy 546→27、Coolify 10 只读起步、Railway 远程 7、Portainer 98→15。Anthropic 官方指导同向 [强]。≤30 工具 + action 枚举的既有决策被验证。
2. **scope 必须在执行层强制**（每次 tools/call），只做 tools/list 过滤 = 形同虚设（CVE-2026-46519 直接反例；Coolify 在 middleware 与能力校验两处执行）[强]。
3. 破坏性操作 = **预览 → 显式确认 → 执行**两段式；`confirm` 字段从 input schema 隐藏防模型自填（Railway）；交易型操作带 idempotencyKey 与价格回显（Vercel）；客户端不支持 elicitation/MRTR 时优雅降级（保留显式 confirm 参数）[强]。
4. 审计成为一等能力：每次调用记录 actor/token/资源/结果；顺带提供只读审计查询工具（Dokploy 少见的正面案例）[中]。
5. 上下文预算双管齐下：服务端裁剪（profile/toolset/allowlist）+ 响应脱敏截断；GitHub 官方数据：只加载 3–10 个常用工具可省 60–90% 上下文 [中-强]。
6. 死亡区：**无确认的全写权限导致真实破坏**（Dokploy #46）；**自动生成工具的 schema 缺陷会拖垮整个 server**（Anthropic 全拒）；MCP 服务器本身被打穿（nginx-ui 无鉴权 CVE 已进 KEV、mcp-atlassian RCE、Context7 提示注入）；聚合网关跨 server 信任问题（Docker GHSA）[强]。
7. 传输与鉴权：自托管 ≤10 台优先 Streamable HTTP + Bearer scope token 复用平台 token 体系；OAuth 2.1 留到托管/多租户；工具注解只作客户端提示，不进安全假设（MCP 2026-07-28 规范：注解不可信、scope 最小化、MRTR 支持中途确认）[中-强]。

来源：https://github.com/Dokploy/mcp/issues/37 · https://github.com/Dokploy/mcp/issues/46 · https://github.com/sapientsai/dokploy-mcp-server · https://coolify.io/docs/integrations/mcp · https://docs.railway.com/ai/mcp-server · https://vercel.com/docs/agent-resources/vercel-mcp/tools · https://github.com/portainer/portainer-mcp/issues/46 · https://github.com/advisories/GHSA-CR22-WJX7-2W6M · https://github.com/docker/mcp-gateway/security/advisories/GHSA-m5m2-mrxf-7j7q · https://modelcontextprotocol.io/specification/2026-07-28/basic/security_best_practices

## 5. 构建管线

| 方案 | 机制 | 状态（2026-09） | 关键事实 |
|---|---|---|---|
| Railpack | Go，analyze → plan JSON → BuildKit LLB frontend；mise 管版本；MIT | v0.39.0（2026-09-03），pre-1.0 | **Coolify PR #9117「Defaults to railpack since nixpacks is being problematic for most people」**；Dokploy/Easypanel 已加入；15 个 provider；plan 可审计 |
| Nixpacks | Rust，检测→生成 Dockerfile；Nix 依赖 | **维护模式**（2025-11 公告），仅依赖更新 | Railway 2026-03 删除全部文档页面；仍是 Dokploy 默认、Coolify 标注支持 |
| CNB / Paketo / Herokuish | buildpack detect/build/export | CNCF 2026-08 毕业（基础设施活跃）；**Heroku 2026-02 维持模式**，herokuish/EOL shim 连环退役 | 自托管单体 PaaS 生态正在离开它（GitLab 已迁 CNB、Fly 迁自研 BuildKit/Depot）；基准可靠成功率最低（Paketo 48.7%） |
| Dockerfile / 预构建镜像 | 显式构建 | 全行业兜底共识 | Kamal 纯 Dockerfile；Portainer 纯镜像；Dokploy 四种 build type 并存 |

**结论**：
1. 选型被业界验证 [强]：**自托管 PaaS 的新默认构建器正在收敛到 Railpack**（Coolify 已合并改默认、Dokploy/Easypanel 跟进），Dockerfile 兜底是所有路线共识。
2. Railpack 是 pre-1.0 活跃项目（半年内 v0.30→v0.39），必须**钉版本 + 归档 railpack-plan.json 与构建日志**；Dokploy 已验证「默认版本升级对已装服务器静默不生效」是真实坑 [强]。
3. 缓存要显式设计，不能默认：Railpack frontend 曾完全不注入 CacheImports（2026-05 才修，issue #557/#595）；env 变化必须用 `secrets-hash` 触发失效（Dokploy PR #4557 的教训：env 变了命中旧缓存）[强]。
4. 私有依赖 = BuildKit secrets + 声明式 secrets 数组（只有声明的 secret 变化才失效对应层）；Spike 必须验证凭证不进最终镜像 [强]。
5. 构建隔离是 Spike 前置项：官方快速开始用 `--privileged` BuildKit；rootless 需内核 ≥5.11 + fuse-overlayfs；Coolify 因「同机构建压死服务器」专门提供独立 Build Server，并明确构建崩溃是常见事故 [强]。
6. 检测失败根因跨方案高度一致（运行时版本选择、精确 patch、包管理器误判、缺 start command、monorepo、构建上下文假设）；Coolify 长尾 issue 是现成测试用例库（SPA 输出目录 #5261、run image 缺 curl 导致健康检查永远失败 #2875、变量 null 全站部署挂 #6798）[强]。
7. 死亡区：**「检测魔法」黑盒**（buildpacks 被批「魔法耗尽时更痛」）与**平台耦合 buildpack 版本**（2025-12 Fly 的 buildpack 构建因 builder 镜像与 daemon API 不兼容全挂，官方建议改用 Dockerfile）[中-强]。Railpack 的 plan JSON 可审计是对此的改良。

来源：https://github.com/coollabsio/coolify/pull/9117 · https://github.com/railwayapp/railpack · https://railpack.com/architecture/secrets/ · https://github.com/railwayapp/railpack/issues/557 · https://github.com/Dokploy/dokploy/pull/4557 · https://github.com/railwayapp/nixpacks/blob/main/README.md · https://blog.railway.com/p/introducing-railpack · https://blog.bult.ai/comparative-analysis-of-default-buildpack-behavior-across-modern-web-frameworks/ · https://coolify.io/docs/core/infrastructure/servers/build-servers

## 6. 用户体验与差评（对「用户友好」承诺的直接输入）

**抱怨频率排序（约 60 条来源，GitHub issue 天然偏向故障，频率=重复出现次数）**：

| 类别 | 频率 | 代表事实 |
|---|---|---|
| 升级破坏 / 数据丢失 | **高** | Coolify 升级后控制面 DB 损坏（#7519）、APP_KEY 丢失导致全部应用「MAC is invalid」（#3687）、自动更新默认开启反复出事；Dokploy 升级按钮失效、PG checkpoint 损坏（#3989/#3790） |
| 备份/恢复不可靠 | **高** | 备份显示绿色成功但 S3 根本没上传（Coolify #9035）；恢复脚本删掉 postgres 库（#7987）；密钥不随备份，恢复后「登录正常但一切 500」 |
| 默认模板持久化缺陷 | 高 | PG 18 数据目录与模板挂载路径不一致，重启即丢库（#8735）；停止超时默认 10s（PG 建议 ≥60s）导致 WAL 损坏（Dokploy #3595） |
| 调试黑盒 | **高** | 「Coolify 里我完全不知道它在干什么、为什么错」；异步任务 stderr 随临时容器消失；Portainer「500 而不是你的 compose 格式错误」 |
| 安全与不安全默认 | **高** | Coolify 2025-01→2026-07 至少 8 个 CVE，模式一致：前端做了权限后端没做，面板=root；默认诱导暴露 DB 端口 |
| 资源占用超预期 | 中 | 单一样本实测 Coolify 空转 800MB vs CapRover 200MB；构建把最便宜 VPS 打到整机冻结 |
| SSL/域名/多机路由 | 中 | 证书续期失败、通配符需手写 Traefik labels；远程节点域名路由要手改动态配置 |
| 许可证信任 | 中（发生即严重） | Dokploy DSAL 混目录 + 删除「will always be free」措辞；MinIO 静默拆解社区版 |
| 迁移困难 / 维护负担 | 中-高 | 平台配置沉在 DB 与 UI 里，无一键导出；「每次用自托管 PaaS，最后都是在维护 PaaS 而不是维护应用」 |

**「10 分钟」核实**：各产品安装宣称基本成立，但那只覆盖「平台装好」；真实 TTFW（首个带域名 HTTPS 的应用）取决于 DNS/TLS/DB/密钥四项，恰是差评最集中处 [中]。

**未被满足的需求（需求空位证据）**：
1. 「K8s 以下、Compose 以上」的中间层：单 VM、反代、密钥注入、零停机、Postgres 管好、不要 K8s——现有回答只有 Swarm/K3s/Nomad，用户明确拒绝 [中]。
2. **可信的恢复**：备份在桶里但 UI 不认、无法演练；「恢复最后几小时的数据才是真正花钱的一半」。
3. 内置可观测性（用户被迫另配 Sentry/uptime 工具）。
4. 配置可导出/可迁移/可进 git（有人为此外迁 Ansible）。
5. OSS 侧的小团队 RBAC/SSO/审计（Dokploy 放进付费企业版）。
6. 安全默认（DB 不暴露、面板不裸奔）比更多功能更被需要。

来源：https://github.com/coollabsio/coolify/issues/7519 · https://github.com/coollabsio/coolify/discussions/3687 · https://github.com/coollabsio/coolify/issues/9035 · https://github.com/coollabsio/coolify/issues/7987 · https://github.com/coollabsio/coolify/issues/8735 · https://github.com/Dokploy/dokploy/issues/3595 · https://nvd.nist.gov/vuln/detail/cve-2025-34159 · https://github.com/coollabsio/coolify/discussions/11115 · https://www.reddit.com/r/coolify/comments/1ivslne/nothing_works_on_coolify/ · https://hamy.xyz/blog/2025-08_coolify-to-ansible · https://news.ycombinator.com/item?id=43555996

## 7. 三类模式汇总

### 7.1 收敛点（业界已验证的默认选项，可直接照搬）

1. 双容器重叠 + health gate + 旧实例保留窗口（7 产品一致）。
2. 失败 = 不切流量；回滚 = 重放旧镜像；数据不回滚。
3. 持久卷/固定端口 = 零停机例外，须降级并告知。
4. 多节点 = 节点出站连接 + registry 分发 + 每节点本地代理。
5. 「degraded 不迁移」需显式判定窗与恢复对账（Nomad 语义为蓝本）。
6. 构建器 = Railpack + Dockerfile 兜底；Dockerfile 是一等路径。
7. MCP = 精选工具（≤30 量级）+ action 枚举 + 只读默认 + 预览确认 + scope 执行层强制 + 审计。
8. spec = 仓库内 YAML/文件 + schema 校验 + 单一真源。
9. plan/apply 面向 agent：--json、三态 exit code、脱敏、artifact + etag、破坏性确认。
10. 面板默认不裸奔、DB 默认私有、后端强制鉴权（Coolify CVE 群的反面教训）。

### 7.2 分歧点（差异化机会 / 有代价的路线）

1. **持续漂移检测与收敛**：K8s GitOps 有、PaaS 阵营全部没有 → fleetly 的空白区机会；代价是 ArgoCD 式 self-heal 事故与「Synced ≠ desired」的用户教育成本。建议检测默认开、收敛 opt-in。
2. **spec 语言**：YAML（多数）vs TS DSL（Railway，表达力换复杂度）。我们选 YAML + JSON Schema，被 agent 训练覆盖最好。
3. **节点触达**：SSH（Coolify/Dokploy/Kamal，简单但 NAT/密钥脆弱）vs 出站 agent（Portainer/Rancher/Nomad/Coolify Sentinel）。我们选出站，与最新演化方向一致。
4. **写权限边界**：全写（Dokploy 官方）vs 只读起步（Coolify 内置）vs 受控写+引导 UI（Render）。我们选 scope 分级。
5. **是否提供任意 API 逃生舱**：覆盖 vs 安全的取舍（Anthropic 对超大类 API 反而建议 search+execute 两工具）。我们暂不提供，用精选面 + 后续按需评估。
6. **零停机的宣称尺度**：Coolify 新文档主动降调「不保证零停机」，Railway 只保证握手——诚实声明是趋势，营销口径的差异明显。

### 7.3 死亡区（明确的坑，避开）

1. 路由注册早于 health 通过（Dokku caddy #8974 每次部署 31 个 502）。
2. 单应用配置错误影响全局入口（Dokploy #5189）。
3. 为多节点引入 Swarm 滚动语义（update-order/registry-auth/Traefik 成员变更四类坑 + Docker v29 断裂 + CapRover raft WAL 损坏）。
4. 外挂独立编排层实现蓝绿（ECS/CodeDeploy 被官方取代）。
5. 自动生成全量 MCP 工具面（74k tokens、Anthropic 全拒）。
6. 仅 tools/list 过滤做权限（CVE-2026-46519）。
7. 静默双向合并 UI 与 spec（Fly 混乱、ArgoCD 事故）。
8. 把「检测魔法」做成黑盒（buildpacks 批评；Fly buildpack 平台耦合翻车）。
9. 承诺 stateful 应用零停机（全行业例外）。
10. 升级 force-recreate / 默认自动更新 / 无快照（Coolify 反复事故模式）。
11. 备份假成功（绿色勾 + S3 静默失败）。
12. 许可证混目录 / 回收功能（Dokploy DSAL 危机、MinIO 从明星到反面教材）。
13. 全自建一切（agent + overlay + 自托管状态）带来的运维/支持负担（Flynn 死因背景）。

## 8. 借鉴清单（照搬 / 适配成本 / 证据强度）

| # | 借鉴项 | 落到 fleetly | 适配成本 | 强度 |
|---|---|---|---|---|
| A1 | 路由注册严格晚于 health gate；配置写入原子且隔离 | Spike B 验收标准第 1 条；发布状态机不变量 | 设计约束，零额外成本 | 强 |
| A2 | 复制成熟默认值：health 1s/5s、drain 60s、stop grace 30s、start-first | 平台默认参数；可在 spec 覆盖 | 低 | 强 |
| A3 | 回滚 = 旧 digest 重放 + 保留 N 版本；明确数据不回滚 | v0.1 回滚实现与文档措辞 | 低 | 强 |
| A4 | 持久卷/固定端口的零停机降级表 | spec 文档 + 部署时显式警告 | 低 | 强 |
| A5 | 出站 agent + 重连自愈一等公民（心跳/超时/重建/请求级超时） | Spike C 验收标准；agent 协议设计 | 中 | 强 |
| A6 | degraded 判定窗 + 恢复对账 + 防双跑（Nomad disconnect 语义） | 多节点状态机文档化 | 中 | 强 |
| A7 | registry 凭据经 agent 通道下发（一次性 token） | v0.2 设计输入 | 中 | 强 |
| A8 | plan/apply 全套 agent 语义（json/exit code/脱敏/etag/confirm-destructive） | CLI 与 API 设计 | 中 | 强 |
| A9 | spec 发 JSON Schema 到 SchemaStore；UI 字段标注来源、禁止直改 | v0.1 UI 与 spec 实现 | 低-中 | 强 |
| A10 | 漂移检测默认开、收敛 per-app opt-in、字段归属表 | 对账器设计 | 中 | 中-强 |
| A11 | MCP 确认两段式 + confirm 字段隐藏 + 执行层 scope + 审计 + 响应脱敏 | v0.2 MCP 设计（已有决策的细化） | 低 | 强 |
| A12 | Railpack 钉版本 + 归档 plan JSON + 缓存三情形验证 + Dockerfile 一等路径 | Spike A 验收标准 | 低 | 强 |
| A13 | 控制面资源预算 <200MB idle（对标 CapRover）+ 构建与运行隔离限额 | v0.1 硬指标；避免 Coolify 800MB 教训 | 中 | 中-强 |
| A14 | 升级原子化：预拉镜像+快照+失败自动回退，禁止 force-recreate | v0.1 升级路径 | 中 | 强 |
| A15 | 备份按灾难日设计：密钥随备份、S3 失败红色告警、恢复演练、桶内可发现 | v0.2 备份设计 | 中 | 强 |
| A16 | 结构化错误（错误码 + 原始 stderr + 修复建议 + 事件流） | API/CLI/UI 全局规范，同时服务 agent 与人类 | 低（习惯） | 强 |
| A17 | 一键导出/迁移（卸载可带走一切） | 反黏性承诺，agent 友好 | 中 | 中 |
| A18 | 安全默认：DB 私有、面板不裸奔（Tunnel/SSO 可选）、后端强制鉴权 | v0.1 安全基线 | 中 | 强 |
| A19 | 许可证不可回收承诺；商业边界公开（Open-Core 走 Coolify 模式） | 已在设计中，补充「不删措辞、不混目录、不回收功能」 | 低 | 中-强 |
| A20 | 构建失败可解释：输出 plan + provider 选择理由 | Railpack plan 天然支持 | 低 | 中 |

## 9. 与业界不同之处（区分差异化与无知）

**差异化（有证据支撑的主动选择）**：
1. 声明式 spec + plan/apply **+ 漂移检测**三件套：PaaS 阵营无人做全，K8s GitOps 证明需求。风险：无同规模先例，须小步验证（检测先行、收敛后置）。
2. **统一集群 + 出站 agent 的轻量 PaaS**：Exact 组合在轻量 PaaS 无同类（Portainer/Rancher/Nomad 有 agent 但非 PaaS；Coolify 才补 Sentinel）。
3. **Go 全栈单二进制**：无直接同类（Dokku=bash、Coolify=PHP、Dokploy=TS、CapRover=JS）。
4. **MCP 从第一天按安全模型设计**（scope 执行层 + 审计 + 确认两段式）：Coolify 只读起步、Dokploy 工具爆炸，无人从 v0.2 就系统化。

**务实取舍（非差异化，与主流一致）**：
5. 无跨节点 overlay：与 Coolify/Dokploy-remote/Kamal 一致；跨节点私网是公认缺口（Dokploy 用 Tailscale 指南补位），后置 WireGuard 合理。
6. 手动/简单调度 + 不自动迁移：与轻量阵营一致，牺牲 HA 换可预测性。

**无知/待验证（不可当优势宣传）**：
7. 漂移收敛的用户接受度无 PaaS 先例；MCP 写操作的确认机制在旧客户端上的行为未定（MRTR 2026-07 才发布）；无 overlay 下的多节点 TLS 分发复杂度未被我们实测。

## 10. 对架构文档的修订建议

以下为对 `2026-09-17-architecture.md` 的具体修订（编号对应上文证据）：

1. §2.5 发布流程：补充不变量「路由注册严格晚于 health gate」「入口配置原子写入且单应用故障隔离」；补充默认参数表（A2）；补充「失败=不切流、回滚=镜像重放、数据不回滚」措辞（A3）。
2. §2.5 新增「零停机降级边界」小节：持久卷/固定端口/固定容器名 → stop-first + 显式警告（A4）。
3. §2.6 多节点：补「agent 重连自愈为验收项」（心跳+超时判死+主动重建+请求级超时）与「degraded 判定窗（秒级心跳 + 连续 N 次 + 数十秒窗口）、恢复对账、防双跑」（A5/A6）；补 registry 凭据经 agent 下发（A7）。
4. §2.4 spec：明确 YAML + JSON Schema 发布到 SchemaStore；UI 字段来源标注；删除/新增语义完整（spec 即完整期望态）（A9）。
5. §2.5/§2.4：新增 plan 语义清单（--json/三态 exit code/脱敏/plan artifact + etag/--confirm-destructive）（A8）。
6. §3 新增决策 D11：漂移检测默认开、收敛 per-app opt-in、字段归属表（A10）。
7. §3 D10（MCP 精选）：细化确认两段式、confirm 字段隐藏、scope 执行层强制、审计内容、响应脱敏截断（A11）。
8. §4.1 Spike A 验收标准：补钉版本、plan JSON 归档、缓存三情形（本地层/registry cache/secrets-hash）、私有依赖 secrets、rootless/特权与资源限额（A12）。
9. §4.1 Spike B 验收标准：补「路由晚于 gate」「失败路径（health 永不通过/容器启动即崩）」「坏配置全局隔离」「WS/长连接在途请求语义」（A1、§1 结论 5）。
10. §4.2 v0.1 范围与验收：新增「升级原子化（预拉+快照+失败回退）」与「备份基线（如平台自身状态快照）」（A14）；新增「控制面 idle 内存 <200MB」硬指标（A13）；新增「安全默认基线：DB 不暴露、面板默认不裸奔、后端鉴权」（A18）。
11. §5 风险表：新增三行——控制面升级破坏（第一信任杀手）、备份假成功、错误信息黑盒（A14/A15/A16）；RustFS 行补充「备份密钥随备份」（A15）。
12. §7 明确不做：补「不做 stateful 应用零停机承诺」「不做 Swarm」「不做版权/功能回收（许可承诺不可撤回）」（A19）。
13. §2.1 设计原则：新增第 5 条「错误信息即产品：错误码 + 原始上下文 + 修复建议 + 事件流」（A16）。

## 11. 不确定项与证据弱点

1. GitHub issue 天然偏向故障样本；频率排序未按用户基数归一化（Coolify 用户数是 Dokploy 数倍，可见缺陷自然更多），两者真实故障率不可直接比较。
2. 多处关键数字为单一来源或维护者自测：Coolify 800MB/200MB 为 n=1；MCP token 数为维护者自测，跨对象不可直接互比；Portainer 压测为官方口径。
3. Coolify 抱怨多针对 beta 版本（v4 至 2026-04 才出正式版）；部分已修问题可能仍被当作现状。
4. 「漂移检测是空白区」基于我们所查范围（主流 PaaS + GitOps）；不排除小众产品已有实现。
5. MRTR/elicitation 的客户端实际支持度不明（规范 2026-07 发布），依赖「用户确认」的设计在旧客户端行为需实测。
6. 「10 分钟安装」类宣称的第三方来源含 SEO/半 AI 生成内容，仅用于交叉印证趋势。
7. Coolify Sentinel 的具体心跳参数、Fly rolling 默认值等细节未取到官方确认。
8. Flynn/Deis 死因的「技术 vs 商业」占比无定量归因（创始人自述以商业为主）。
