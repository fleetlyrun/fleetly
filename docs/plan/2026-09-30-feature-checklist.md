# fleetly v1 功能清单（2026-09-30）

实现契约：每项带验收口径；批次验收 = 该批全部项打勾。上游依据：两份设计书、ADR 0001~0020、ADR-0012 dogfooding 能力清单。

**本期裁定（2026-09-30）**：①账号体系完整 Team/Role 从 N0 就有；②Git 集成 v1 = 通用部署 Token + GitHub 原生 webhook；③dogfooding 接管现有 staging 双节点（归档版退役，不做数据迁移）；④Console N2 最小只读观察面、N3 全功能。

---

## N0 心脏（单节点可用 + 完整账号体系）

**安装与引导**
- F0.1 一行安装：`curl | bash` 检测/安装 Docker → 单节点 swarm init → fleetlyd（容器形态优先）→ 数据根初始化。验收：全新 Ubuntu VPS 一条命令到 fleetlyd healthy。
- F0.2 首启引导：Bootstrap Token 落盘（journal+文件，可吊销）+ 管理员初始化**全 CLI 可完成**（`fleet init`）。验收：无浏览器环境可完成全部初始化。
- F0.3 `fleet quickstart`：样例应用 + sslip.io 零 DNS 域名 + 自动 TLS。验收：安装完成后 2 分钟内公网 HTTPS 可访问。
- F0.4 `fleet doctor`：Docker 版本/端口/磁盘/时间同步诊断，输出处置建议。

**账号与权限（完整 Team/Role）**
- F0.5 User/Team/Role/Token 全模型：多用户、内置角色（owner/admin/member）+ 自定义 Role（Scope 集合）、邀请流（一次性链接）。验收：双用户双角色权限差异 e2e。
- F0.6 Token 全套：创建/列出/吊销（含 Bootstrap Token）；`resource:action` Scope、write⇒read；前缀化随机串、sha256 存储、last_used_at 节流记录。验收：吊销后进行中请求的下一个调用即 401。
- F0.7 审计：全部写操作留痕（操作者/来源枚举 manual|api|cli|webhook|schedule/前后值指纹）。

**应用与部署**
- F0.8 Project/App CRUD（API+CLI）；Spec 归一化两源：Compose 受控子集（白名单+受管字段显式拒绝+拒绝原因精确）与镜像直部署。
- F0.9 Build（dockerfile 构建器）：BuildKit、控制面节点执行、并发上限可配、缓存本机、构建日志实时流。
- F0.10 Deployment 状态机（preparing→building→releasing→observing→succeeded|failed|rolling-back→superseded）+ admission 队列：同幂等键/commit 去重、latest-wins、显式 supersede、queue_full 反馈、排队与在途可取消。
- F0.11 回滚 = Revision Replay 一等动词（`fleet rollback`）+ Revision diff（`fleet revisions diff R1 R2`）。
- F0.12 健康门与优雅退出：http/tcp/exec 探针 + L1 门 + L2 看门狗 + L3 观察窗（60s 默认）；SIGTERM 宽限；被杀后按 Generation 幂等重放（场景 1/2 回归）。
- F0.13 Git 触发：per-App 通用部署 Token URL（可再生成、token 不进 URL 路径段日志）+ GitHub 原生 webhook（push 自动部署、`[skip deploy]`、watchPaths monorepo 过滤、HMAC 验签+delivery 去重）。

**网络与路由**
- F0.14 per-Project overlay；跨 Project 默认隔离；`egress:none` 声明（swarm v1 弱隔离，明示）。
- F0.15 Edge：traefik 受管自宿（通用 ManagedProvider reconciler 首个实例）+ Route（host/path/port + protocol http|h2c|tcp + TLS 模式）+ LE HTTP-01 自动证书 + 默认 sslip.io 域名。验收：h2c 后端路由可通（messageloop 形态）。
- F0.16 Volume：受控子集 + 默认钉住节点（Placement 以平台节点 ID 为锚）。

**材料与安全**
- F0.17 Secret/Config：age 信封加密（KEK 数据根、轮换 runbook）、值永不回显回指纹、注入 App/Database；Config 版本化可回读、配额。
- F0.18 镜像凭证分发：私有 registry 凭证存 Secret，Ensure 解析后按节点分发，不落 label/明文 env。验收：私有镜像双节点拉取成功（场景 13）。

**集群（Runtime）**
- F0.19 swarm Provider 全契约：Ensure/Remove/Watch/Addresses/DescribeCluster/Enrollment + 节点身份锚定（铸造/写回/`node.joined`）+ RuntimeLogs 子面 + RuntimeAdmin（drain/cordon）。
- F0.20 多节点就绪：`fleet nodes enroll` 输出加入材料。验收：双节点部署同一 App、卷钉住正确。

**API/CLI 面**
- F0.21 proto 单源：gRPC + REST gateway + OpenAPI 生成；buf breaking 门禁进 CI。
- F0.22 CLI 核心：init/login/projects/apps/deploy/rollback/logs/events/secrets/configs/routes/nodes/tokens/audit——全命令 `--json`（golden 双形态钉死）+ 稳定退出码 + 错误信封（errcode+处置提示）。
- F0.23 事件地基：Outbox+seq 落库、`fleet events list`；流式 follow 在 N1。

**工程守卫（先行）**
- F0.24 CI 守卫全套：编排器 SDK 仅限 providers/、model/spec 叶子纯度、禁词扫描（ADR-0007 清单）、errcode/eventcode 注册表三链咬合、CLI golden、e2e dind smoke。

**可观测最小**
- F0.25 `fleet logs`：运行/构建日志经 RuntimeLogs 直读（时间窗/tail/容器过滤）；持久化检索 N2 前诚实标注"仅实时+最近缓冲"。

## N1 Agent 面 + torchwood 线（验收 = ADR-0012 能力清单全绿）

- F1.1 幂等键：创建型 RPC + CLI `--idempotency-key`；同键同体重放、异体 409、24h 保留。
- F1.2 事件流 follow：SSE + gRPC stream + `fleet events follow --json`；断档 410 + 快照重同步。
- F1.3 Wait 原语：WaitDeployment/WaitBuild/WaitRun + CLI `--wait`。
- F1.4 能力自描述：`fleet explain <资源>` / `fleet schema`（JSON Schema 反射生成）。
- F1.5 Task API：one-shot/resident 创建（镜像直部署、env+secretRefs、TTL、资源上限）、期望并发数、排空停止、列表/watch、per-Task + per-Run 双级稳定 DNS。
- F1.6 Owner Lease：`fleet task renew`（RenewTask）+ 宽限；lease_expired 排空并补足；属主吊销 → 名下 Task 宽限排空（可配置跑完 TTL）。
- F1.7 Schedule：带时区 cron（ADR-0018）、手动触发、重叠 skip 策略。
- F1.8 互通：Task Network Group（创建时刻挂靠）+ App Process 跨挂（`taskGroup:<name>`）+ 跨 Project 双向声明/批准/即时隔离。
- F1.9 治理刹车：per-Project Task/Workload 数量配额、per-Token 创建速率、change freeze（按资源/动作封禁，拒绝带原因）。
- F1.10 build-from-upload：流式 tar + 大小上限。
- F1.11 zot 受管自宿：多节点镜像分发（digest/tag 直存）、构建推送目标。
- F1.12 Database 最小集：postgres（含 percona/pgvector 模板）+ redis 模板、默认本地备份目标开箱即用、连接串注入 Secret、`fleet db` 命令组。
- F1.13 首批 Skills：`skills/`（deploy-diagnose / task-pool / database-provision），随版本演进说明。
- F1.14 构建器扩展：railpack（钉版本）+ static。
- F1.15 **dogfooding 上线**：staging 双节点重装新 fleetly（归档版退役）；torchwood/messageloop 从零部署。验收：ADR-0012 清单逐项打勾 + messageloop 经 h2c Route 对外服务 + dispatcher 池租约/补足/回收全语义真机回归。

## N2 数据观测信任 + 最小 Console

- F2.1 Database 全矩阵：mysql/mongo 模板、版本升级路径、跨节点迁移。
- F2.2 备份深化：restic、外置 S3 目标、保留策略；**恢复演练**（verify + 试恢复到临时实例）为验收必过项。
- F2.3 平台升级工具：升级序（Platform Backup 前置→替换→goose 前滚→Managed Provider 逐个 reconcile→解除只读）+ 失败回滚路径；**升级零扰动 e2e**（用户 Workload 零重启/路由零中断，ADR-0015 验收）。
- F2.4 Logging Provider：VictoriaLogs 受管自宿；日志持久化、检索（时间/文本/容器过滤）、脱敏。
- F2.5 Metrics Provider：victoria 系受管自宿；基础图表（容器 CPU/内存）+ 阈值告警 + 通知通道（webhook/telegram 起步）。
- F2.6 最小只读 Console：部署状态/日志/事件流三页（React+Vite，仅消费公共 API）。
- F2.7 dbtemplate 目录化 + 镜像 digest 钉定门禁（DT-9）。
- F2.8 ObjectStore：外置 S3 兼容配置 + RustFS opt-in 自宿；未配异地持续告警"同机备份非灾备"。

## N3 体验全量

- F3.1 Console 全功能：全部读写操作、终端页、quickstart 向导 UI 化、审计/设置页。
- F3.2 exec 子面：RuntimeExec + 反向中继（节点零入站端口）+ `fleet shell` + Web 终端（票据鉴权、限额、审计）。
- F3.3 模板库：模板 schema（语义变量/自动生成密码/域名）+ 一键部署 + CDN 热更新 + 竞品迁移钩子（`create-from-dokploy` 形态）。
- F3.4 Git 集成扩展：GitLab/Gitea 原生 webhook（届时按需求确认）。

## N4 第二运行时试点

- F4.1 k3s Provider：场景 3 验收（无状态全语义保持 + 有状态 Backup/Restore + 显式数据处置；egress:none 升级为 NetworkPolicy 强隔离）。

## Backlog（v1 外，触发条件见对应 ADR）

promote 跨项目原语（ADR-0011 触发条件）；预览环境（同上）；自动伸缩；独立构建节点/多节点构建缓存（ADR-0019）；SSO/2FA；GitLab/Gitea 深度集成与 PR 预览；旧版数据迁移工具（本期裁定：不需要）。

---

**重量提示**：完整 Team/Role 进 N0 后，N0 ≈ 原 N0 + 0.5 批；实施顺序建议 F0.24（守卫）→ F0.19/21（地基）→ 部署链 → 账号 → 安装引导，安装向导类（F0.1-F0.4）可随 N0 尾部收口。
