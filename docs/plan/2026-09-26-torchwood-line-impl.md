# T 线实施方案（票据表）——torchwood/messageloop 迁移与动态工作负载

| 状态 | 日期 | 关联 |
|---|---|---|
| **待实现（分发式：每票由实现 agent 按 prompt 执行，第 0 步强制方案可行性审查；用户逐票验收后统一提交）** | 2026-09-26 | [设计档](../design/2026-09-26-torchwood-line.md)（DT/OT 裁决）；[分发 prompt](2026-09-26-torchwood-line-prompts.md) |

## 0. 纪律（每票生效）

1. **第 0 步 = 方案可行性审查，先于任何代码**：核实票内「现状锚点」file:line 是否仍成立、逐项取证「必查项」；发现矛盾/前提不成立 → 停止并输出审查报告等人工裁决，不猜测着改方案。审查结论与实现中的偏离决定记入票尾「实施记录」。
2. 守卫与验收条款逐条落回归测试（测试名体现条款）；全量 `go test ./...` 绿；console 改动另跑 vitest/typecheck/lint/build。
3. 硬约束：AGENTS.md（Git Bash 正斜杠、导出标识符不缩写）；用户可见文案英文、注释中文；镜像引用 digest 钉定（check-image-pins 门禁）；错误码/事件只增；proto 破坏性变更先停手；Console 既有 data-testid 锚点不破坏；bash 过 `sh -n`、禁 `2>/dev/null`。
4. 完成不 commit/push——输出变更清单 + 测试清单 + 审查结论 + 偏离清单，等用户验收。

## 1. 依赖图与波次

```
IMPL-T1-1(域名资源) ─┐
IMPL-T1-2(镜像代拉) ─┼─→ IMPL-T1-5(messageloop 割接)          [T1 出口]
IMPL-T1-3(init job) ─┤      └──────────────→ (mlbridge 暂走公网)
IMPL-T1-4(Config)   ─┘
IMPL-T15-1(项目网+recon扩面) ─┐
IMPL-T2-0(spike×4) ──────────┼─→ IMPL-T2-1(Tasks API) ─┐
IMPL-DB-1(PG目录化,可先行) ──┼─→ (T2 割接前置)          ├─→ IMPL-T2-3(dispatcher改造)
IMPL-T2-2(build-upload) ─────┘                         ─┴─→ IMPL-T2-4(torchwood割接) [T2 出口]
IMPL-T1-6(SDK TLS,独立)   IMPL-T3-*(P2,后置)
```

| 波次 | 票 | 合计预估 |
|---|---|---|
| T1 | T1-1 / T1-2 / T1-3 / T1-4（可并行）→ T1-5、T1-6 | 10-18d |
| T1.5（可并行 T1） | T15-1 | 3-5d |
| 独立先行 | DB-1 | 2-3d |
| T2 | T2-0 → T2-1、T2-2 → T2-3 → T2-4 | 16-26d |
| T3（P2） | T3-1 / T3-2 / T3-3（简票，启动前再细化） | 3-5d |

## 2. 票据

### IMPL-T1-1（OT-2）Domains state 资源与协议面

- **目标**：per-domain `{host, service, port, protocol(http|h2c), cert_mode(http01|wildcard)}` state 资源；API + Console 可写；Traefik 后端按域名出 `scheme=h2c`；label 降级 bootstrap + 单一写点仲裁；80→443 重定向维持不做。
- **现状锚点**（先核实）：`internal/engine/routes.go:29`（Port=expose 首端口）、`routes.go:169-191`（firstExposePort）；`internal/state/domains.go:22-24`；`internal/compose/domains.go`（label 解析、≤5/服务 ≤10/app）；`internal/ingress/dynamic.go:285-300`（http 后端）、`:238-241`（不重定向）；`internal/ingress/manager.go:349-367`；Console `AppDomainsPage.tsx`（只读+verify）。
- **改动点**：proto 域名 CRUD（新字段向后兼容）；state schema 迁移；engine routes 改消费 state 行；ingress 渲染 h2c；compose label→bootstrap 种子；Console 页可写（host/port/protocol/cert_mode，复用原子组件）；CLI 同步。
- **守卫与验收**：①同服务两域名不同端口不同协议 → dynamic config 各自正确（回归钉）；②state 存在时 label 忽略 + 事件（仲裁验收）；③protocol=h2c → Traefik service `scheme=h2c`（配置快照断言）；④host 冲突/超限 → 4xx 点名；既有 5/10 上限不回退；⑤ACME HTTP-01 签发路径 staging 真机复验。
- **必查项**：现行 Domains proto/RPC 与 state 表真实形态；console verify 读哪张表（与写路径一致性）；域名删除的证书清理语义；label→seed 触发时点。
- **依赖**：无。**预估** 3-5d。

### IMPL-T1-2（DT-2）镜像代拉与凭证下发

- **目标**：部署时 tag→digest 经 registry API 解析钉定；swarm 逐节点按 digest 拉取；平台级 registry credentials（envelope 加密）；Redeploy 重解析可变 tag；airgap 本地镜像快路径不回归。
- **现状锚点**：`internal/engine/engine.go:720-735`（E_IMAGE_PULL_FAILED）、`:771-777`（digest 钉定）；`internal/substrate/services.go:52-90`（本机 inspect）；`internal/substrate/images.go:58-79`（ImagePull 仅 buildkitd 路径）。
- **改动点**：registry resolver（registry v2 token flow：ghcr 匿名 + 凭证两种）；platform settings 增 registry credentials；engine 部署路径改 resolved digest + auth 传递；错误语义（解析失败 → 点名 image+原因）。
- **守卫与验收**：①resolver 单测（mock registry：匿名/凭证/404/限流退避）；②staging 真机：ghcr 公共镜像零预拉部署成功；③digest 进 revision spec 且 drift 对账可见；④本地已有镜像走 inspect 快路径（airgap 回归）；⑤私有镜像 + 凭证逐节点拉取成功（依赖必查项结论）。
- **必查项**：**swarm service create/update 的 EncodedRegistryAuth 下发语义（update 不重携是否丢 auth——SwarmKit 行为实证）**；resolver 限流/重试；平台 zot 自建引用路径零回归。
- **依赖**：无。**预估** 2-4d。

### IMPL-T1-3（DT-4）部署期一次性作业（init job）

- **目标**：compose label `fleetly.job: init` 声明 init 服务；发布管线晋级前以新 spec 跑 one-shot job，失败即发布失败；超时看门狗；日志入 VL。
- **现状锚点**：`internal/compose/domains.go:118-164`（cron label 解析先例）；`internal/cron/manager.go`（one-shot job 机器）；`internal/compose/validate.go:47`（depends_on 拒绝注释——顺序由发布管线管的口径出处）。
- **改动点**：compose 解析+校验（job 服务禁 expose/与 cron label 互斥）；job runner 从 cron 机器抽出复用；release 管线挂钩（EnterPhase 单写点纪律内）；事件注册表增 release.job_*；naming 公式扩展。
- **守卫与验收**：①job 失败 → release 失败（回归）；②job 超时 → timed_out 事件 + release 失败；③多 init job 并行全过才晋级；④job 容器零残留（janitor 路径）；⑤无 init job 的 release 路径行为零变化（回归）。
- **必查项**：swarm one-shot job 约束的 label 公式（Docker29 坑记忆）；与 release 相位机挂钩点（不得绕开 EnterPhase）；job 的 env/secret/config 投影与 service 同源。
- **依赖**：无。**预估** 2-3d。

### IMPL-T1-4（OT-3）Config 资源

- **目标**：app 级明文配置资源（版本化、可回读、审计），compose `type: config` 任意只读挂载路径；面向少数运行时配置文件，**不映射目录级文件树**。
- **现状锚点**：`internal/api/secrets.go`（secrets 形态）；`internal/compose/validate.go:256-263`（secret target 固定 /run/secrets）、`:663-668`（volume type 白名单）；`internal/metrics/spec.go:35-36`（swarm config 内容寻址分发先例）。
- **改动点**：state（app_configs 表）；API CRUD（Get 回读明文，admin scope；配额沿 secrets 口径）；compose volume 校验（type: config，target 绝对路径 ro，禁撞 /run/secrets 前缀）；engine 渲染 swarm config（`fleetly-<app>-config-<name>-<sha8>` 内容寻址，服务 update 整体换引用）；Console AppConfigsPage（镜像 Secrets 页去加密）；CLI。
- **守卫与验收**：①未知 config source 解析期拒绝；②内容变更 → 新 config 对象 + 服务滚动 + 旧对象回收；③配额超限 4xx；④target 撞 /run/secrets 拒绝；⑤Get/List 权限矩阵与 secrets 同构（List 不出值？Config 明文可回读但 List 仍不带值，回读走 Get）。
- **必查项**：swarm config 引用更新的服务滚动语义（configs 列表整体替换先例）；旧 config 对象 GC 时机。
- **依赖**：无。**预估** 2-3d。

### IMPL-T1-5 messageloop 割接（messageloop 仓库 + staging 运维）

- **目标**：messageloop 栈（redis+messageloop+mlbridge）落 fleetly staging；dokploy 栈并行保留至验收。
- **内容**：messageloop 仓库新增 `docker/fleetly/`（受控子集 compose：删 ports/depends_on/external network/插值；`command` 保留 `--appendonly yes`；mlbridge.yaml → 平台 Config；MESSAGELOOP_* 全走平台 env）；三域名经 OT-2 API 声明（WS 9080 http / gRPC 9090 h2c / API 9091 h2c）；`MLBRIDGE_TORCHWOOD_BASE_URL` 临时公网形态；割接 runbook（BGSAVE 回灌或明示清零 + recover 探针，DT-8 验收）；docs/dokploy README 增 fleetly 章。
- **守卫与验收**：compose 过 fleetly validate 零告警；割接探针通过；WebSocket 域名真机连通；回滚 = dokploy 栈未拆。
- **依赖**：T1-1~4 全部。**预估** 1-2d（+真机窗口）。

### IMPL-T1-6 SDK DialGRPC TLS（messageloop 仓库）

- **目标**：`sdk.DialGRPC` 支持 TLS 凭据（现 insecure 硬编码，README §4.2 自认欠账）；insecure 本地路径保留。
- **守卫**：SDK 集成测试对 staging TLS 域名（h2c）握手 + 一次往返成功；本地 insecure 路径回归。
- **依赖**：T1-1（h2c 域名就绪后可验）。**预估** 1d。

### IMPL-T15-1（OT-1）Project 资源与项目网 + recon 扩面

- **目标**：Project = 网络共享作用域（身份轴仍归 Team，两轴正交，app ∈ 恰一 project 可选）；per-project overlay；成员服务双挂 + 项目网别名 `<app>-<service>`；substrateRecon 扩 networks。
- **现状锚点**：`internal/naming/naming.go`（`fleetly-<app>-net`、别名=服务名仅 app 网、用户自报 aliases 拒绝）；recon 现状（substrateRecon duty 30s，services only——T0-V2.2）。
- **改动点**：proto（projects CRUD + attach/detach）；state（projects 表 + app.project_id）；swarm overlay 生命周期（平台建 `fleetly-project-<id8>`）；服务双挂投影（engine/naming 扩展）；删除语义（project 空 app 才可删；detach 滚动）；recon 对 networks 的对账（孤儿网 → 派生修正+事件）；Console 最小面（app 归属显示 + 项目列表卡）；CLI。
- **守卫与验收**：①跨 app DNS 回归（exec 内 `ping <app>-<service>` 通）；②**孤儿网注入（state 外 `fleetly-` 前缀网）一个对账周期内暴露**（机制验收条款）；③project 删除非空拒绝；④短名跨 app 不混流（别名隔离断言）；⑤naming 契约：app 名保持全局唯一不变（表驱动测试不破）。
- **必查项**：**service update 增/摘网络的任务重启语义**（swarm 会重建任务——Console 与 runbook 诚实标注，或评估滚动窗口）；别名与 naming 契约评审记录；staging UDP 未放行期跨节点项目网的 placement 同节点指引。
- **依赖**：无（可并行 T1）。**预估** 3-5d。

### IMPL-DB-1（DT-9）PG 模板目录化（独立可先行）

- **目标**：dbtemplate 目录化；词表增 `postgres-18`（vanilla）与 `percona-postgresql-18`（含 pgvector）；发行版零新增适配器；大版本升级不做（创建钉死）。
- **现状锚点**：`internal/dbtemplate/dbtemplate.go:23-46`（注册表与 `E_DB_TEMPLATE_UNSUPPORTED`）、`internal/dbtemplate/render.go`、`internal/database/adapters.go`、E4 35/35 真机矩阵。
- **改动点**：注册表目录化重构（engine 通用 descriptor）；两新模板镜像 digest 钉定 + check-image-pins 台账登记；CreateDatabase 校验走既有路径；Console create-database-dialog 增模板选择器；文档（大版本升级 = dump/restore 新实例）。
- **守卫与验收**：**每个目录条目过 create→backup→restore 回归矩阵**（把「发行版不新增适配器」从断言钉成事实）；percona 镜像与官方镜像 env/entrypoint 兼容性实证（PGDATA/init 语义）；未知模板 4xx 既有错误码不破。
- **依赖**：无。**预估** 2-3d。

### IMPL-T2-0 Spikes（报告落 docs/reports/）

1. **Docker 29 internal overlay 出网实证**（DT-7）：对照 bridge internal 语义矩阵（NAT/DNS/跨节点）；不过 → 宿主 nft 兜底方案写进报告。
2. **digest registry 直拉真机**（T1-2 必查项的真机腿）：含 EncodedRegistryAuth update 语义。
3. **swarm service 生命周期时延基线**：spawn/healthy/stop 计时，对照 dispatcher 现役容器直操；常驻池模型余量结论。
4. **多服务编排顺序语义观测**：同 app 多服务发布时各服务启动时序与健康门行为，对照 depends_on 需求，诚实结论（顺序 or 并行+自愈窗口）。
- **预估** 1-2d。T2-1 前置 ①；T2-4 前置 ④。

### IMPL-T2-1（DT-5）Tasks API

- **目标**：程序化动态工作负载面——swarm service 承载、`restart: none`、owner/TTL label + janitor 回收、平台默认加固、机具令牌 scope + 配额、日志入 VL、三作用域网络（app|project|task-group + internal 变体）；MVP 不做 exec；池语义不进平台。
- **改动点**：proto `tasks.v1`（CreateTask/GetTask/ListTasks/StopTask/DeleteTask + EnsureNetwork）；state（tasks 表、配额表）；机具令牌新 scope `tasks`；engine 执行面（hardening 默认：CapDrop ALL/只读 rootfs/非 root/pids 限额——服务端强制；网络 = scope 引用，**API 面不存在 attach 入参**，task-group 网长活 + 控制面每网一次性挂靠）；janitor（TTL/到期/孤儿，recon 扩 tasks）；事件 + 审计；CLI `fleetly tasks run/ls/rm/logs`；Console 后置 backlog。
- **守卫与验收**：①hardening 默认落 service spec（spec 快照回归断言）；②配额超限（并发/资源）fail-closed 拒绝；③TTL 到期 janitor 回收 + 事件；④跨 scope 越权（他令牌的 task）拒绝；⑤API 面无 attach 入参（类型层不可表示——机制验收）；⑥networks 对账含 task（孤儿 task 暴露）。
- **依赖**：T15-1（作用域机器）、T2-0①。**预估** 5-8d。

### IMPL-T2-2（DT-6）build-from-upload API

- **目标**：通用「上下文 tar + Dockerfile 入口 → buildkitd → zot，返回 digest 引用」；机具令牌 scope `build`；大小/时长配额；与 git 构建同信任级（无特赦）。
- **改动点**：proto（流式上传 + BuildFromUpload）；buildkitd 管线复用（单平台构建）；zot push（平台侧，调用方无需 push 凭证）；配额与并发上限；临时上下文落盘清理。
- **守卫与验收**：①超限（大小/时长）拒绝；②产物 digest 返回且可被 CreateTask/部署引用（端到端回归）；③并发构建上限生效；④上下文临时文件零残留。
- **依赖**：zot（W2 已有）。**预估** 3-5d。

### IMPL-T2-3 dispatcher 改造（torchwood 仓库）

- **目标**：docker.sock 交互面清零——BuildImage→build API；SpawnInstance→CreateTask；回收→Stop/Delete；网络→EnsureNetwork + scope 引用；容量键/节点心跳/细胞模型删除；池语义（租约/保温/熔断/TW_MAX_REQUESTS）保留。
- **锚点**：`torchwood/dispatcher/daemon.go`（docker client 唯一入口）、`pool.go`（池语义，不动）、`nodes.go`/`capacity.go`（删除对象）；config `functions.docker.host` 段改 fleetly endpoint + 机具令牌（scope: tasks,build）。
- **守卫与验收**：①集成测试对 staging fleetly 端到端：spawn→health 握手→请求分发→TW_MAX_REQUESTS 自退→平台回收；②无任何手动 docker 网络操作（事故类回归——断言代码路径零 docker client）；③配额触顶时 dispatcher 语义（fail-open 改 fail-closed 上抛）。
- **依赖**：T2-1、T2-2、T15-1。**预估** 5-8d。

### IMPL-T2-4 torchwood 栈割接（torchwood 仓库 + staging 运维）

- **目标**：七常驻 + init job 栈落 fleetly；postgres 落托管 percona-postgresql-18；dokploy 栈并行保留至验收。
- **内容**：受控子集 compose（config.yaml → 平台 Config ×3 服务；migrate → `fleetly.job: init`；bootstrap SQL → Config；删 dokploy-network/ports/插值）；域名 OT-2 声明（9080 http + 9060 h2c）；postgres dump/restore 进托管实例；redis/minio 卷迁移或明示重置（DT-8 口径 + 割接记录）；mlbridge 切项目内网别名；dispatcher 挂 server 项目网（受控回访）。
- **守卫与验收**：全栈健康门过；函数端到端（部署 zip→执行→回收）；割接记录含数据决策；回滚 = dokploy 栈未拆。
- **依赖**：T2-3、DB-1、T2-0④。**预估** 2-3d（+真机窗口）。

### T3（P2 简票，启动前细化）

- **T3-1** 宿主回环端口 opt-in：label `fleetly.ports`（仅回环绑定，平台端口登记防冲突，UDP 供 QUIC/KCP）。
- **T3-2** 托管 redis AOF 选项：目录参数变体（同 DB-1 机器）。
- **T3-3** 通配证书沿 v0.3 W5 排期，不在本线重复立票。

## 3. 分发与验收流程

1. **分发**：按 §1 依赖图取票，prompt 见 [分发 prompt 文件](2026-09-26-torchwood-line-prompts.md)（每票一条，自包含，内置第 0 步可行性审查）。
2. **验收（用户）**：每票完成后核对——①变更文件清单与方案偏离清单；②测试清单与验收条款对应关系；③全量测试绿证据；④staging 真机项（T1-1⑤/T1-2②⑤/T1-5/T2-*）；⑤实施记录。验收通过后统一 commit（沿用仓库还原点惯例，一票一还原点）。
3. **阻塞上报**：实现 agent 第 0 步审查发现矛盾即停，方案修正走设计档裁决（回本档改票），不带病实现。

## 4. 实施记录

### IMPL-T1-1 方案可行性审查（2026-09-26，实现会话）

**结论：通过（无阻塞前置矛盾），进入实现。**

现状锚点核实（票内 file:line 逐条）：
- `internal/engine/routes.go:29`「Port 是后端端口（compose expose 首端口）」✓（现行 28-30 行；`firstExposePort` 现于 186-196，票内 169-191 为撰写时行号，语义未变）。
- `internal/state/domains.go` 现行表结构：`domains(id, app_id, service, domain UNIQUE, created_at)`（00001）+ `port/cert_sha256/cert_not_after/cert_updated_at`（00006，加法迁移）✓。
- `internal/compose/domains.go` label 解析与上限：`parseDomainsLabel`（≤5/服务，`maxDomainsPerService`）+ `checkDomainContracts`（≤10/app，`maxDomainsPerApp`，跨服务冲突 E_DOMAIN_CONFLICT）✓。
- `internal/ingress/dynamic.go:285-300`（http 后端合成）✓（现行 282-289）；`:238-241`（不重定向）✓；`internal/ingress/manager.go:349-367`（证书保障段）✓（现行 348-357）。
- Console `AppDomainsPage.tsx` 只读 + verify ✓。

必查项取证：
1. **Domains proto/RPC 与 state 表真实形态**：proto 只读（ListAppDomains GET、VerifyAppDomains POST），`DomainView{service, domain, port, cert_sha256, cert_not_after, created_at}`；写路径唯一 = `ingress.ReplaceAppDomains`（发布时按 engine 提取的 compose 声明对账，spec 不可读时走台账兜底）。
2. **console verify 读哪张表**：`VerifyAppDomains`（internal/api/read.go:280）经 `st.ListAppDomains` 读同一 `domains` 表，与写路径一致 ✓。
3. **域名删除的证书清理语义**：删行不触碰证书库；`ensureCertificate` 以「域名集变化即重签」判据（`needsRenewal`）收敛——被删域名的 SAN 会保留到下一次重签（续期窗口或下次域名集变化）。API 删除路径须显式触发重发布 + 证书保障，与发布路径同链。
4. **label→seed 触发时点**：`PublishRoutes` 由引擎在「首健康后」与「终态」两个挂点调用（sweep 只做全量重发布，不携带声明输入）。种子语义落位：state 无行且声明可用（spec_hash 匹配 + 有 expose 首端口）时播种；state 有行时 label 忽略 + 事件。

补充前提（本票实现依据）：
- 路由键 = Swarm 服务名（`fleetly.RouterName` 公式），access-log 经候选集反解（internal/logs/access.go）；多后端分组键需扩展后缀并使反解剥后缀（单后端服务公式零变化）。
- `internal/apitest` 以 `NewDomainsService(st, nil)` 注册（mgr 可空）——API 写面须容忍无 ingress 装配（跳过收敛）。
- 事件注册表在 `internal/eventcode`（注册 + golden 快照），新事件 `route.label_ignored` 按只增纪律登记。
- proto 生成：`mise exec -- buf generate`（remote 插件可用）；console 类型经 `pnpm gen:api` 从 genproto swagger 再生成。
- W5 已实现平台证书 DNS-01（`internal/ingress/dns01.go`），但 **app 级证书恒 HTTP-01**（W5 设计原文）；wildcard 主机（`*.`）本票仍拒绝（签发链未就绪，纳入 W5 app 级 DNS-01）；`cert_mode` 本票只落存储与校验，不改变现行签发路径。

### IMPL-T1-1 实施记录（2026-09-26，实现会话）

**状态：实现完成，待用户验收（未 commit）。**

变更文件清单（每文件一句）：
- `proto/fleetly/server/v1/domains.proto`：DomainsService 增 Create/Update/Remove 三 RPC；DomainView 增 `protocol/cert_mode`（新字段向后兼容）；Get/List 读面不变。
- `genproto/fleetly/server/v1/domains.{pb,pb.gw,swagger.json,grpc.pb}`：buf generate 生成物（swagger 供 console 类型再生成）。
- `internal/state/migrations/00023_domains_endpoint_columns.sql`：domains 增 `protocol/cert_mode` 两列（加法迁移 + Down 演练；既有行取默认 = 现行行为）。
- `internal/state/domains.go`：Domain 增两列；新增 `CreateAppDomain/UpdateAppDomain/RemoveAppDomain/GetAppDomain`（配额 ≤5/服务、≤10/app fail-closed；host 全局独占 `ErrDomainConflict`；`DomainLimitError` 带 scope/count）；`ReplaceAppDomains` 整组对账原语随 label 降级整体删除（死代码清理 + 语义退役），新增 `SeedAppDomainsIfEmpty`（单事务「检空才播种」原子仲裁——并发 API 写行不被覆盖/删除）。
- `internal/state/domains_test.go` / `internal/state/testdata/migrations.golden`：CRUD/默认值/配额/冲突回归 + 迁移 golden 再生成。
- `internal/compose/domains.go`：新增导出 `NormalizeDomain`（API 与 label 解析共用同一形态契约：trim/小写/IDN→punycode；通配主机拒）。
- `internal/compose/spec.go`、`internal/compose/validate.go`、`internal/state/labels.go`：注释口径更新（label = 首部署种子，state 行为真值）。
- `internal/engine/routes.go`：`RoutePublishInput.Services` → `Declared`（只承载 label 种子候选）；删除台账兜底直推（真值读取移到 ingress 发布点现读）。
- `internal/engine/routes_test.go`：种子提取回归（文件丢失无种子 / hash 匹配给候选）。
- `internal/ingress/manager.go`：`PublishInput.Declared`；`PublishRoutes` 单一写点仲裁（state 有行 → label 忽略 + `route.label_ignored` 事件；无行 → 播种）；新增 `ConvergeAppDomains`（API 写面收敛入口）；`routesFromLedger` 多后端分组（键 = app×service×port×protocol）。
- `internal/ingress/dynamic.go`：`Route` 增 `Protocol/KeySuffix`；合成按协议出后端 scheme（h2c:// 直出）、分组第 2..n 组键附 `~<port>[~h2c]` 后缀（后端 DNS 恒为 RouterName 本体）。
- `internal/ingress/manager_test.go` / `acme_test.go` / `s3public_test.go`：守卫①②③回归、省略=删除退役、API 收敛、ACME 代码链取证。
- `internal/logs/access.go`：access-log 路由键反解剥离分组后缀（候选集仍按 app×service 构造）。
- `internal/runtime/provides.go`：engine→ingress 适配器字段映射。
- `internal/api/read.go`：DomainsService 写面三方法与视图新字段、审计（domain.created/updated/removed）、写后收敛（best-effort 披露：失败落审计 error + `route.publish_failed` 事件，资源行保留）。
- `internal/api/scope.go`：三写 RPC 登记 deploy（读面 read 不变）。
- `internal/api/domains_test.go`：scope 登记、CRUD/归一化/默认值、守卫④、更新局部语义、删除与审计回归。
- `cmd/fleetly/cmd/domains.go`：`list` 增 protocol/cert_mode 投影；新增 `add/set/rm` 子命令（set 空 flag = 保持现值）。
- `cmd/fleetly/cmd/domains_test.go` / `cmd/fleetly/cmd/testdata/golden/domains_list.golden`：CLI 全链回归与 golden 再生成。
- `internal/eventcode/{events.go,eventcode_test.go,testdata/events.golden}`：`route.label_ignored` 登记（只增纪律）。
- `console/src/pages/AppDomainsPage.tsx`（+`.test.tsx`）：只读页升级为可写（新增/编辑/删除对话框；写控件走 deploy 门；平台管理员资源面只读说明行；既有 data-testid 锚点保留）。
- `console/src/api/{endpoints.ts,types.ts,schema.d.ts}`：三个写 RPC 封装、词表类型与 swagger 再生成。

测试清单与验收条款对应表：
| 条款 | 回归测试 |
|---|---|
| ① 同服务两域名不同端口不同协议各自正确 | `TestPublishMultiBackendServicePerDomainRouting` |
| ② state 存在时 label 忽略 + 事件（仲裁） | `TestPublishIgnoresLabelsWhenStateRowsExist`；种子里程 `TestPublishSeedsFromLabelsWhenStateEmpty`；「省略=删除」退役 `TestPublishEmptyDeclarationKeepsStateRows`；播种原子仲裁 `TestSeedAppDomainsIfEmptyArbitration`（并发 API 写行不被覆盖） |
| ③ protocol=h2c → service scheme=h2c | `TestPublishMultiBackendServicePerDomainRouting`（h2c:// 快照断言） |
| ④ host 冲突/超限 4xx 点名；5/10 上限不回退 | `TestDomainsServiceValidationAndLimits`（API 层）+ `TestCreateAppDomainConflictAndLimits`（state 层） |
| ⑤ ACME HTTP-01 路径不回退 | `TestConvergeAppDomainsIssuesHTTP01CertificateForAPICreatedDomain`（代码链）；**staging 真机复验待执行窗口** |
| API 写面完整性 | `TestDomainsServiceScopeRegistration` / `TestDomainsServiceCreateNormalizesAndDefaults` / `TestDomainsServiceUpdateKeepsOmittedFieldsAndRemove` |
| CLI 同步 | `TestDomainsCRUDSurface` + `domains_list` golden |
| Console 写面与角色门 | `AppDomainsPage.test.tsx`（4 测：viewer 无写控件 / admin 新增 / developer 编辑删除 / 平台管理员只读说明） |
| access-log 反解不回归 | `TestStripAccessRouterKey`（分组后缀用例） |
| 迁移纪律 | `TestPlatformSettingsMigrationUpDown`（00023 Up/Down）+ additive golden |

验证证据（一手）：`go test ./... -count=1` 全绿；变更包 `-race` 全绿（state/ingress/api/logs/engine/compose/eventcode/cmd）；`go vet ./...` 净；`golangci-lint run` 无新增问题（存量欠账不计）；console `pnpm test` 321 全绿（基线 313 + 新 4 测 + 既有计数）、`pnpm typecheck`/`pnpm lint`/`pnpm build` 净。

偏离清单（实现中的决策，均按票面「实施记录」纪律登记）：
1. **对账写入语义修正（含孤儿代码清理）**：「声明集对账（省略=删除）」随 label 降级整体退役——`ReplaceAppDomains` 原语删除，首部署播种改走 `SeedAppDomainsIfEmpty`（单事务检空才播种，消除「检空↔插入」竞态下并发 API 写行被对账删除的窗口）；API 删除是唯一撤销路径（与 OT-2 单一写点仲裁一致，`route.label_ignored` 事件披露）。退役语义的旧回归用例（`TestReplaceAppDomains*`）同步删除，夹具迁移到 `CreateAppDomain`/`SeedAppDomainsIfEmpty`。
2. **state 真值的读取位置**：票面「engine routes 改消费 state 行」落实为「发布链路在 ingress 发布点现读 state 行」；engine 只提取 label 种子候选。理由：消除「engine 读快照 ↔ 写点」竞态，且 domains 表的写与读消费点统一在 ingress。
3. **多后端分组键**：第 2..n 组路由键附 `~<port>[~h2c]` 后缀（'~' 不在服务名字符集内，结构防撞键；单后端服务公式零变化）；后端 DNS 名恒为 RouterName 本体；access-log 反解同步剥离后缀。
4. **API 收敛语义**：写面成功即返回资源行；入口收敛 best-effort（DNS 未就绪期的 ACME 失败不把资源写入报成错误；失败落审计 error + `route.publish_failed` 事件，下次部署/续期扫描恢复）。
5. **scope 解释**：写面取 deploy（与 SetEnv/SetScalingPolicy 同级的应用运行面写语义，非平台凭据面；票面「admin/deploy scope」按此落位）。
6. **golden 再生成**：事件注册表/迁移哈希/CLI 列表三处 golden 按各仓纪律显式再生成（非静默漂移）。

待真机项（本环境无 staging 访问权，未虚构结果）：
- ⑤ ACME HTTP-01 staging 真机复验（建议：加域名 `fleetly domains add` → 观察 80/443 签发与 Verify）；
- Traefik 对 `~` 后缀路由键的实机接受性复核（HTTP provider 为 JSON map 键，paerser 无字符集限制；staging 部署时一并确认）。
