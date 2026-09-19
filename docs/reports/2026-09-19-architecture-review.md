# 全项目架构评审与机制缺口报告（含修复计划）

| 状态 | 日期 | 关联 |
|---|---|---|
| 评审完成；S1-S12 全部修复落地（同日，含配对机制测试；验证：go build/test 19 包 + sdk 模块全绿、buf lint、vitest 26 用例、gofmt 干净） | 2026-09-19 | [架构](../design/2026-09-17-architecture.md)；[发布语义](../design/2026-09-17-release-semantics.md)；[状态模型](../design/2026-09-17-state-model.md)；[v0.1 验收](2026-09-19-v0.1-acceptance.md) |

评审方式：主会话建立项目画像（构建/测试全绿基线：`go build ./...`、`go test ./...` 19 包、console vitest），按职责切 9 个模块并行深评（状态层 / 发布引擎 / 构建底座 / API 面 / 入口 / git+日志 / CLI+SDK / Console / 交付装配），113 条原始发现汇总去重后**高严重度逐条打开代码亲核**（16 条报高全部亲验，确认 15 条、降级 1 条）。评审阶段零代码改动。

---

## 1. 架构总评

**架构纪律显著高于同规模 v0.1 项目**，评审确认的优点：

- **D13 核心-适配器边界执行彻底**：`go list` 验证 engine/compose/placement/state/secrets 核心包零 Docker/Traefik/Railpack/BuildKit 类型泄漏；Spike A/B 实证约束（`--provenance=false` 双保险、Traefik 空 `{}` 纪律、ForceUpdate 恒不递增）有注释可追溯且有测试钉住。
- **鉴权面无绕过**：42 个 RPC 的 scope 矩阵 41/41 全登记（Ping 豁免），未登记方法 fail-closed 拒绝；token sha256 哈希存储 + 恒定时间比较；流式拦截器全覆盖；`/ui/` 静态托管三层防穿越。
- **状态层基本功扎实**：三层状态模型落位干净（观测缓存与权威表零交集）、`BEGIN IMMEDIATE` + WAL + busy_timeout 并发选型正确、备份真回读校验（全量读 + integrity_check + 负面测试）、age envelope 无 nonce 复用。

**主要风险集中在四个系统性模式**（§3 机制缺口）：状态机生命周期幂等缺口、同步执行面无超时、注释承诺与实现脱节、出站字节无统一出口。

## 2. 确认的高严重度发现（15 条，全部经主会话亲核代码）

### 2.1 快速修复组（P0）

| # | 发现 | 位置 | 证据要点 | 波次 |
|---|---|---|---|---|
| H1 | 日志 hub 持锁阻塞回放死锁 | `internal/logs/hub.go:64-86` | `subscribe` 持 `h.mu` 做 `ch <- e`（缓冲 256 < ring 缺省 1000），消费方在 Follow 返回后才读 → ring 积压 >256 即整管线死锁（`ingest` 同锁连带停摆）；测试 ring 仅 8 条未覆盖 | S1 |
| H2 | SSH push 无分支过滤，任意分支即部署 | `internal/gitserver/repos.go:139-160`、`deploy.go:30` | 钩子注释承诺 daemon 过滤并转发所有 `refs/heads/*`；Ref "仅记录"，全链无比对；webhook 面有过滤（`webhook.go:277`）、SSH 面没有 | S2 |
| H3 | 域名跨服务迁移不更新 port | `internal/state/domains.go:128-131` | 迁移分支仅 `UPDATE ... SET service = ?`，port 只在 INSERT 写入 → 台账 `service=api, port=8080`，路由错误端口；测试只断言 service | S3 |
| H4 | moby client 选项顺序颠倒 | `internal/substrate/client.go:32-36` | `{WithHost(host), FromEnv}` 实际效果 = DOCKER_HOST 覆盖显式配置（moby v0.6.0 `WithHostFromEnv` 无条件覆盖），与注释相反 | S4 |
| H5 | Console 日志页跨应用状态残留 | `console/src/pages/AppLogsPage.tsx:72,226-229,164-178` | `entries` 仅在切 service 时清空；`name` 变化不清空且历史回填走 append（去重键不含 app）→ 换 app 后日志混排 | S5 |
| H6 | upgrade.sh UTF-8 BOM 破坏 shebang | `deploy/upgrade.sh:1`（实测 `ef bb bf`） | `sudo ./upgrade.sh` 直接 ENOEXEC；dind 验收用 `sh` 调起测不出；作为 release 制品分发 | S6 |
| H7 | plan 宣称的 `--confirm-destructive` 门控端到端不存在 | `internal/compose/diff.go:13-14,296` vs `cmd/fleetly/deploycmds.go`（仅 `--json` 标志） | plan artifact `requires_confirm_destructive` 与人读提示承诺的闸在 RPC/引擎层零实现 | S7 |

### 2.2 结构性缺陷组（P1）

| # | 发现 | 位置 | 证据要点 | 波次 |
|---|---|---|---|---|
| H8 | Traefik spec 漂移收敛丢失全部 app 网络挂载 | `internal/ingress/traefik.go:344,409-457,512-546` | `buildTraefikSpec` 不含 Networks、更新路径整体替换、`traefikSpecEqual` 不比较网络、sweep 不重挂——`attachNetwork` 注释（356-359）明言的实机回归在更新路径复发 → 任一字段漂移即全路由 502 无自愈 | S11 |
| H9 | 空视图拒绝使路由撤销永远不生效 | `internal/ingress/manager.go:236-244` | 台账行已删但 `Validate` 拒绝空合成 → 旧视图保留（测试钉死）；`DeleteAppDomains` 无生产调用方 → 删最后一个域名后边缘永久服务旧路由、sweep 每 12h 永久 warn | 设计项（本报告挂账） |
| H10 | 构建无恢复无超时，building 行永久卡死 | `internal/build/builder.go:151-157,216-236`、`queue.go` | 终态写复用可取消 ctx（全文件无 `WithoutCancel`，ctx 取消连失败终态都落不了库）；`BuildBuilding` 无恢复路径；build 包零 `WithTimeout` → 挂起 solve 永久占并发槽 | S8 |
| H11 | 排队时长计入准备预算，整批部署假失败 | `internal/engine/engine.go:244,287` | 预算锚点 = `CreatedAt`（入队时间）；排队 >300s（前序 releasing 300s + observing 60s，或 blocked_waiting 无上限）的部署拾取后第一拍即 E_RUNTIME_UNAVAILABLE | S9 |
| H12 | 重启丢失 blocked_waiting 子状态，等待中的部署被误判失败并归位 | `internal/engine/recovery.go:186-228` | `classifyRecovering` 不检查 `rec.Phase == PhaseBlockedWaiting` 也不做放置前哨 → 落 default 分支 E_DEPLOY_INTERRUPTED + 归位；违反 release-semantics 场景 15 | S9 |
| H13 | 入队与事件/审计分属两个事务 | `internal/state/deployments.go:189`（仅 Store 级，无 Tx 变体） | 调用方（`api/deployments.go:100-122` 等）另起 InTx 写事件+审计 → 两事务间崩溃出审计黑洞，违反 state-model §2.9 fail-closed | S10 |
| H14 | 构建上下文无包含性校验——宿主任意目录可打进镜像 | `internal/api/builds.go:53-56,78`、`internal/build/solve.go:102`、`railpack.go:33` | `base_dir` 为调用方任意提供 + compose 校验只白名单键不校验值 + 执行侧只查非空 → deploy-scope token 可令构建读宿主敏感目录成镜像外带（M3/M4 两模块独立报出同一根因） | S12 |
| H15 | 安装/升级链验签默认降级 | `deploy/install.sh:281-296` | cosign 缺席（干净 VPS 接近 100%）或 .sig 下载失败均只 warn；checksums 强制但来自同一 origin（不防 release 投毒）——"生产强、消费弱"供应链落差 | 设计项（挂账） |

### 2.3 中低严重度（90 余条，按模块归纳）

- **API 面**：退化信封透传内部错误原文（`apperr.go:210`）；Deploy 忽略 `req.GetApp()`（REST 路径参数与实际资源静默错位）；匿名请求无限流；`revoked_at` 返回字面量 "revoked"。
- **入口**：`m.user` 无锁竞态 + 并发双重签发（LE 限额风险）；证书/ACME 私钥明文落盘（age 未覆盖）；`markServed` revision 竞态；`renewDue` 空 appID 落空登记。
- **git+日志**：SSH git 子进程无超时不回收；X-Fleetly-Timestamp 不参与签名可省略（时间窗防线对攻击者无约束）；拉源失败 git stderr 全文进响应/审计/日志；build 来源日志绕过 redact；hook token TOCTOU。
- **构建底座**：substrate 无 per-call 超时；buildkitd "running≠可连接" 首建竞态；在途构建对 resolveImage 不可见（部署静默用旧镜像）；artifacts/cache/builds 台账无界增长；`Request.Secrets` 契约将明文落库（当前未激活）。
- **发布引擎**：env pending 消费点两套相反契约（引擎行为正确、state/envlayer 注释及测试钉死错误一侧）；revision 与终态 CAS 分两事务（重启可产生重复版本行）；compose 只存 OS 临时目录无持久副本；漂移投影遗漏 update 配置；secrets 白名单放行但规划层拒绝且文档示例照抄即失败；用户 label 静默丢弃；tick 无 panic 隔离；machine.go 转移表不在写路径。
- **CLI/SDK**：无 SIGINT/SIGTERM 处理（流式命令退出码落契约外，原报高降级为中）；用法错误与 plan/diff 共用退出码 2；`--json` 错误输出非机器可读；webhook secret 经 argv；SDK 恒 insecure 且无 CLI TLS 开关；一元 RPC 无超时。
- **Console**：流式 401 不接全局登出；login 先于凭据校验（坏 token 留 localStorage）；types.ts 手写镜像已实证漂移（`seq` 应为 string）；重连固定 1.5s 无退避；NDJSON 坏行杀整流；无 CSP。
- **交付装配**：pr.yml 缺顶层 permissions；deploy/** 在 PR 轨道零验证；升级回退不回退 schema（迁移已应用时回退必然 DEGRADED）；换件窗口 mv 无原子性兜底；Dockerfile CMD 指向不存在配置；敏感 workflow 的 actions 全 tag 引用未钉 SHA；nightly 取证截断不上传；config-example 缺 logs.*/drift_interval_seconds 键；bootstrap token 进 INFO 日志/journald。

## 3. 机制缺口分析（四类系统性 + 个案簇）

### 类 A：状态机生命周期幂等缺口（H10/H11/H12/H13 + revision 双事务 + watermark 内存化 + hook token TOCTOU）

**逃逸路径**（以 builds 卡死为例）：数据结构——`building` 中间态无"进程归属/阶段锚点"，无主 building 行可表示；静态检查——golangci（standard+gosec）不覆盖"终态写复用可取消 ctx"语义；测试——无"执行中杀进程再重启"类别，`-race` 只抓数据竞态；CI——绿灯放行；运行时——builds 非终态无任何告警（对比 deployments 有 300s 看门狗）。

**判定：一类**（同仓内 deployments 有恢复扫描而 builds 没有、预算锚错位、子状态重启即丢、双事务写入——全部是"跨进程/跨事务边界无幂等锚"实例；v0.2 的 cron_runs 会继续繁殖此类）。

**补齐**：① 数据结构层——阶段锚点列 + store 单写点（`EnterPhase` 同事务写锚点 + CAS + 事件），预算从锚点起算；② 运行时断言层——非终态超龄扫描（> 2×最大预算 → 事件 + 红告警），兜住未来任何新状态机；③ 测试层——crashpoint 注入重启测试类别，每个状态机至少一条。
**验收**：下一个生命周期缺陷被超龄扫描在运行时层告警；已知回归被 crashpoint 测试在 CI 拦截；锚点误用因单写点不可发生。

### 类 B：出站字节无统一出口（信封透传 / git stderr 外泄 / build 日志绕 redact / 手拼审计 JSON / token 进日志）

**逃逸路径**：类型层——`apperr.New(..., "%s", err.Error())` 允许任意串进对外 message；出口层——`EnvelopeFromGRPCStatus` 存在但退化分支显式透传（注释自认待确认）；测试——golden 只锁正路径，无"内部细节不外泄"负路径；运行时——泄漏零信号。

**判定：一类**（五个模块独立报出同构问题）。

**补齐**：① 出口收口——对外 message 模板化、原始错误只进 `WithCause`/日志；审计摘要 `json.Marshal` 构造器；build 日志并入容器日志同一 redactor；② 测试层——泄漏扫描负路径（断言 message 不含路径/stderr/git 片段/`flt_` 前缀）。
**验收**：出口函数无直拼位置（构造层）；绕过则泄漏扫描测试红（CI 层）。

### 类 C：注释即契约、实现不认账（H7 / H2 / env pending 两套契约 / secrets / moby 注释反了 / 假启动顺序不变量）

**逃逸路径**：注释承诺存在 → 测试测的是实现现状而非承诺（最危险形态：`envlayer_test` 钉死与架构文档相反的语义）→ 文档清单与校验白名单无人 diff → 死代码（导出符号 `unused` linter 管不到）。

**判定：一类**，且与项目"诚实性"自我要求直接冲突。

**补齐**：① 契约测试锁定承诺（分支过滤、confirm 闸、env pending 按架构语义钉 + 修正注释）；② 文档-实现 diff 门禁（架构 §2.4 清单文件化，与 `validate.go` 白名单集合断言相等）；③ CI 死代码门禁（`golang.org/x/tools/cmd/deadcode` 抓导出未达符号）。
**验收**：下一个"文档写了、代码没做"的差异在契约测试或清单 diff 红；无人调用的半成品 API 被 deadcode 门禁拦。

### 类 D：无超时预算的阻塞链（webhook 绑请求 ctx / 构建无超时 / substrate 无 per-call 超时 / CLI 无超时 / console 无超时）

**逃逸路径**：context 到处在传但从不带 deadline；无"上游挂起"故障注入测试类别；挂起 = 静默无响应零信号。五处实证，成因是**无超时约定**。

**补齐**：① 出口收口——substrate 非流式调用 client 单点 `WithTimeout(30s)`；构建 Execute 包 per-build 预算；webhook 拉源移出请求生命周期；CLI 非流式动词缺省 deadline；② 测试层——挂起注入类别（fake 上游永不返回，断言预算内失败）。
**验收**：走收口出口的调用自带预算；新直连点在挂起注入测试下暴露。

### 个案簇（力度 = 回归测试，不上重机制）

| 发现 | 拦截层与守卫 |
|---|---|
| H1 hub 死锁 | 修复（回放缓冲 ≥ ring 深度，保序不持锁阻塞）+ 容量边界测试类别 |
| H3 域名 port | 对账测试断言全列（service **和** port） |
| H6 BOM | deploy/ 纳入 PR 门禁（`sh -n` + 首字节 BOM 断言） |
| H5 Console 残留 | 路由参数变化重置 state 的组件测试 |
| H8 Traefik 网络 | spec 构造单点化（更新分支复用 cur.Networks 基准）+ 保留网络断言 |
| H9 空视图 | 设计决策先行（常驻兜底 router 使合法空态可下发），后测试钉"删最后域名后配置确实撤销" |

## 4. 复核记录

- 高严重度：子代理报 16 条 → 亲核全部属实 → 确认 15 条（H7 原报"CLI 无信号处理"降级为中：真实但属契约/可靠性而非数据安全）。
- 合并 3 组：M4-3+M3-2 → H14；M4-11+M1-4（每请求同步写 `last_used_at`）→ 一条中；M1-6+M6-9（手拼审计 JSON）→ 机制类 B。
- 剔除 1 条（主会话横切疑点，非子代理）："domains 变更无审计"——核实 v0.1 域名无独立写 API（随部署 revision 走，已有审计），非缺陷。
- 注释与实现矛盾组全部属实（env pending、secrets、label 静默丢弃、启动顺序、选项顺序、死代码清单：`SetAppServicePorts`/`Queue.Enqueue`/`EffectiveAppEnv`/`EmptyConfig`/`DeleteAppDomains`）。

## 5. 修复计划（波次与机制任务配对）

止血与补机制并行：每个修复必须携带其配对机制任务（回归测试/门禁），禁止"补丁打完即关闭"。

| 波次 | 任务 | 内容 | 配对机制 | 状态 |
|---|---|---|---|---|
| S1 | H1 | hub 回放缓冲 ≥ ring 深度（保序、不持锁阻塞） | MG-T1 容量边界并发测试 | ✅ 已修（TestHubReplayBacklogBeyondBuffer） |
| S2 | H2 | DeployFromCommit 分支过滤（daemon 侧权威，哨兵 ErrBranchNotTracked → API 映射 skipped + 审计） | MG-C1 分支过滤契约测试 | ✅ 已修（TestDeployFromCommitBranchFilter 等） |
| S3 | H3 | 域名迁移 UPDATE 带 port；service 不变 port 变化也触发更新 | MG-T2 对账全列断言 | ✅ 已修（TestReplaceAppDomainsPortOnlyChange 等） |
| S4 | H4 | moby opts 顺序换为 `{FromEnv, WithHost}`（显式覆盖 env） | MG-C2 host 优先级行为测试 | ✅ 已修（TestNewClientHostPrecedence） |
| S5 | H5+M8-3+M8-8 | Console 日志页按 name 重置 + source 过滤下放 visible + 回放 disposed 守卫 | 组件测试（路由参数重置） | ✅ 已修（AppLogsPage.test.tsx） |
| S6 | H6+M9-4 | upgrade.sh 去 BOM；pr.yml 加 deploy-scripts 门禁（sh -n + BOM 断言）+ 顶层 permissions | MG-E1 deploy PR 门禁 | ✅ 已修 |
| S7 | H7 | confirm-destructive 门控端到端（proto `confirm_destructive` 字段 → API guardDestructiveDeploy（复用 compose.DestructiveChanges 单源判定）→ E_DEPLOY_CONFIRM_REQUIRED(409) → CLI flag） | MG-C3 confirm 闸契约测试 | ✅ 已修（TestDeployConfirmDestructiveGate 等） |
| S8 | H10+类D | builds 启动复位（ResetInterruptedBuilds）+ 终态 WithoutCancel + build.timeout_seconds（缺省 1800s） | MG-A3 crashpoint 测试（builds 首条） | ✅ 已修（TestQueueStartupResetsInterruptedBuilds 等） |
| S9 | H11+H12 | 预算锚点迁移 00009（phase_started_at，CAS 同拍写入，存量行回落 CreatedAt）+ classifyRecovering 分流 blocked_waiting | crashpoint 测试（recovery 首条） | ✅ 已修（TestPrepareBudgetAnchoredAtPickupNotEnqueue、TestRestartRecoveryPreservesBlockedWaiting） |
| S10 | H13 | Tx.CreateDeployment；三个入队调用方（api/gitserver/engine rollback）合单事务（建行→事件→审计 fail-closed） | 原子性测试（事件失败 → 无部署行） | ✅ 已修（TestDeployFromCommitFailClosedAtomic 等） |
| S11 | H8 | specWithNetworks 构造单点化；EnsureTraefik 更新分支以 cur.Networks 为基准合并；attachNetwork 复用同构造 | 网络保留断言 | ✅ 已修（TestEnsureTraefikUpdatePreservesAttachedNetworks） |
| S12 | H14 | TriggerBuild 升 admin scope + API 层 base/context 包含性校验 + 执行侧受管根校验（ContextRoots = temp + git 根 + build.context_roots 配置） | scope/包含性测试 | ✅ 已修（TestTriggerBuildScopeAdminOnly 等） |

**挂账设计项**（本轮不修，需设计决策）：已于 2026-09-20 完成**决策完备的完整解决方案**并冻结为 [遗留问题完整解决方案（S13-S20）](../design/2026-09-20-remediation-complete.md)——含 H9/H15 方案裁决、类 A-D 机制收口、S18-S20 全部中低严重度项的分波次方案与机制验收，后续修复以该文档为任务源。

**修复验证基线（2026-09-19 收口）**：`go build ./...`、`go test ./...`（19 包）与 `go test ./sdk/go/...` 全绿；`buf lint` 通过；console `vitest run` 8 文件 26 用例 + `tsc --noEmit` 通过；`gofmt -l` 干净；deploy/ 23 个脚本 `sh -n` + 无 BOM。dind 全链（journey/nightly）未在本机重跑，由 CI nightly 轨道回归。

## 6. 评审边界

- 评审全程零代码改动；本报告为唯一产出物，修复以本表为任务源。
- e2e/nightly 全链（dind）未在评审中重跑（基线取 CI 绿灯与 2026-09-19 验收记录）；S 波次修复以包级测试 + `go build` 为验收门，dind 全链留 nightly。
- 未覆盖：`spike/`（历史取证，非生产代码）、`genproto/`（生成物，buf 门禁管辖）。
