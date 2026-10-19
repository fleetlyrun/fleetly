# R15 验收声明 / 历史挂账 / 文档一致性 评审报告

> 评审基线：仓库 `D:\Codes\qiulin\fleetly`，HEAD `35257f18`，工作树干净（评审全程只读，未改动仓库任何文件；本报告是唯一写入）。
> 方法：把历史文档里的「已修 / 已关闭 / 已验收 / [x]」一律当**待核实断言**，按「文档位置 → 代码/测试锚 → 本机可跑则实跑」三级取证；无法亲验的标「无法核实」并说明缺什么。严重度按 PROFILE §0：H（正确性/数据安全/架构律实质违反且无守卫）、M（局部缺陷/设计债/守卫盲区）、L（文档-代码漂移/低风险改进）。
> 本轮结论：**H=0／M=3／L=5**。历史声明的兑现度整体高于同类仓库平均（抽样的 P0/P1 绝大多数真闭且有守卫/钉测承载），但存在 1 条**安全语义类承诺零落码**（恢复期只读）、1 条**审计证据链在仓内不可达**（G1-G24 执法手册）、1 条 **ADR 明文承诺未实现未挂账**（版本协商头），以及 5 条文档/纪律漂移。

---

## 1. 覆盖清单

### 1.1 完整读过（文档，含逐行核对）

- `PROFILE.md`（177 行）、`MODULES.md` R15 节
- `docs/reports/2026-10-01-architecture-deep-audit.md`（326 行，D-1~D-4 / P1-1~P1-15 / P2 全表 / §10 机制缺口）
- `docs/plan/2026-10-14-architecture-convergence-plan.md`（158 行，P0-P5 / 挂账 8 项 / 审计轮）
- `docs/design/2026-10-18-declarative-model.md`（268 行，含附录 A/B/C 29 项裁决）
- `docs/plan/2026-10-18-declarative-model-program.md`（38 行）
- `docs/plan/2026-09-30-feature-checklist.md`（111 行逐项过，长行按段落读；N0~N4 与 Backlog）
- `docs/design/2026-09-30-architecture.md`（193 行）
- `docs/reference/compose-dialect.md`（71 行）
- `CONTEXT.md`（关键节：§组织/§交付/§写类与作者/§状态语义；全文按需检索）
- `docs/reviews/2026-10-03-n1-independent-review.md`（73 行）、`docs/reviews/2026-10-05-n2-review.md`（185 行）
- `docs/adr/0082-converge-by-default.md`（97 行）、`0083-restart-seq.md`（60 行）、`0081-write-class-author-class.md`（282 行，前 120 行 + 附录抽读 + 验收锚）、`0015-platform-upgrade-version-skew.md`（5 行）
- `docs/runbooks/staging-fleetly.md` 关键节：头 40 行（手动干预/收敛共存纪律）、490-529（平台升级操作序）、1268-1283（记录·六十 最新换装）
- `internal/guards/transitionguard_test.go`（366 行）、`writeclassguard_test.go`（376 行，断言主体 + 表头）、`domainpurity_test.go`、`sizeguard_test.go`、`e2eciaccount_test.go`、`irguard_test.go` 头部；`nilfacelaw/perfthresholdanchor/migrationchainanchor/statekitlaw/aggregatetestbelt` 的渊源注释
- `internal/application/engine/converge.go`（568 行，逐段读）、`converge_test.go` 用例名 + golden + 冻结/冷却/Cancel 用例、`drift.go`（1-310）、`restart.go`（67 行）、`crashrecovery_test.go`（180 行）、`transition.go` 结构面
- `internal/domain/shared/capability/runtime.go` 契约字段段（240-460、575-618）、`faces.go`（136 行）
- `internal/application/composeintake/intake.go`（30-210 + 全文件检索）、`intake_test.go` configs 段
- `internal/infrastructure/persistence/statekit.go`（全文）、`internal/domain/services/writes/writes.go` 检索
- `cmd/fleetly/cmd/verbs_platform.go`、`verbs_structure.go` 相关段、`quickstart.go`、`sdk/go/fleetly/errors.go` + `errors_test.go`、`cmd/fleetlyd/admin.go`
- `proto/fleetly/spec/v1/spec.proto`（全 282 行）、`proto/fleetly/system/v1/system.proto` RPC 面、`proto/fleetly/telemetry/v1/telemetry.proto` 事件面

### 1.2 抽样读过

- `docs/adr/0005`（第 5 行）、`0041` 验收锚、`0042` 决策 3/后果、`0046` 第 74 行、`0077` 验收锚
- `docs/design/2026-10-14-architecture-convergence-report.html`（全文标签剥离 + 章节标题 + G 记号检索）
- `internal/engine` 历次路径 → 现树映射（`docs/reports` 提到的 `internal/capability/...`、`internal/state/...` 等已按 ADR-0075 映射到 `internal/domain/shared/capability`、`internal/infrastructure/persistence`）
- `internal/guards`：`teamaxisguard`、`gatewayguard`、`factappend/factdual`、`scopeguard`、`freezeguard`、`idemguard` 的存在性与断言名（未逐行读全文）
- `internal/interfaces/grpc/fleetlygrpc/{databases,structure,exec}.go` 局部；`internal/assembly/{grpc,gateway,policy,provides}.go` 局部
- 200~300 条 commit 的生成物耦合统计（见 §1.3）与 8 条具体 commit 的 `--stat`/diff

### 1.3 本轮实跑（只读命令）与结果

```
go version                                                                             → go1.26.6 windows/amd64
go test ./internal/guards -run 'TestWriteClass' -count=1                               → ok
go test ./internal/guards -run 'TestRegistrySourceAnchor|TestFact|TestTransition|TestGateway|TestIdem|TestWriteClass|TestFreeze|TestScope' -count=1 → ok
go test ./internal/application/engine -run 'TestConverge|TestFreezeSuspendsConverge|TestAbsenceRebuilds|TestGhost|TestCancelLegacy|TestSucceededWithoutExpect|TestSteadyStopped|TestGenLabelRollback|TestTerminalRolloutStall|TestBaselineReplayShares|TestPosture|TestTeardownRace|TestRestartSeq' -count=1 → ok
```

commit 纪律抽样（只读 `git show --stat`）：

- 300 条 commit：`proto/*.proto` 变更而 `genproto/*` 零变更 = **0 例**；`wire.go` 变而 `wire_gen.go` 零变 = **0 例**；`config.proto` 变而 `config.pb.go` 零变 = **0 例**（生成物同 commit 纪律在 proto 面执行度高）。
- 200 条 commit：`genproto/*` 变而 `proto/*` 零变 = 0 例；`internal/interfaces/console/dist/*` 变而 `console/src/*` 零变 = 1 例（198def08，消息自述「dist 对齐 verify 通道重建」，属构建序修复，非手改）；`console/src/api/*` 变而 swagger 零变 = 8 例 —— 逐一看 file list，均为「proto 批与 `console:gen` 分属两个 commit」的同一形态（见 R15-L4，两个实例已亲验）。
- 300 条 commit：`cmd/fleetly/cmd/*.go` 生产面变而 golden 零变 = 6 例，抽样 `7f0d72f1`（WaitRun 任务维度）其提交说明明写「CLI golden 零漂移（帧内 id 与预知 id 输出同形）」且 diff 证实输出字节不变 —— 属诚实零漂移，非漏更。

### 1.4 未覆盖 / 无法核实（及原因）

- **e2e dind 全矩阵**（`e2e/{dind-*,upgrade,backup,template,browse,bluegreen,k3s*}`）：本机 Docker daemon 不可达（PROFILE §1.1），无法复跑。凡「e2e 全绿 / 零重启零滚动」类声明，本轮只能核到**脚本/断言存在 + CI job 定义**，真机结果标「无法核实」。
- **staging 真机声明**（runbook 记录·六十等 1283 行实录）：无宿主访问，不宣称复验；只做「runbook 与代码/契约互证」。
- **`mise run test/lint/generate:verify/console:verify`**：`console:verify`/`generate:*` 会写仓，禁止；Windows 下 `cmd/fleetly` SIGWINCH 构建失败（PROFILE 基线），全量 `go test ./...` 不可得。故「全门禁绿」类声明标「无法核实」，本轮只跑上列目标包测试。
- **G1-G24 审查项原文**：仓内不可达（见 R15-M2），无法逐项复核，只能核到被显式引用的 G5/G7/G8/G13/G17/G19/G20 的守卫落点。
- **生成物是否被手改**：无法从 git 历史证明（只能证明耦合统计）；本轮未发现「proto 变而 genproto 不变」的反例。
- **ADR 验收锚全量 83 篇**：只抽了 0015/0041/0042/0077/0081/0082/0083 与声明式模型 29 项裁决；其余标未覆盖。

---

## 2. 模块画像与边界

R15 不拥有生产代码；本模块的对象是**声明与实现之间的证据关系**：历史深审（2026-10-01）的 P0/P1/P2、收敛计划（2026-10-14）的 P0-P5/G1-G24/挂账 8 项、声明式模型程序（2026-10-18）的 ADR-0081/82/83 与附录 29 项裁决、功能清单 [x]、runbook/架构/CONTEXT 的现行承诺。边界：模块级正确性归 R01-R14；R15 只在「文档承诺 vs 代码/守卫落点」处取证，并把与其他报告重叠的事实显式标注（§2.4）。

### 2.1 2026-10-01 深审 D/P1/P2 逐条核对（抽样结论表）

> 旧路径（`internal/capability`、`internal/state`、`internal/engine`、`internal/api`）已按 ADR-0075 映射到现树；路径迁移本身不报。

| 项 | 现树核对锚 | 判定 |
|---|---|---|
| D-1 生命周期/终态原因/实例观测 | `capability/runtime.go:399,402,431`（Restart/StopGrace/RestartSeq）、`:581-613`（completed/failed + ExitCode/Reason/Instance）、`Terminal()`:587 | **已闭**（字段级） |
| D-2 Task 轴与命名翻译 | `runtime.go:301-308`（Task/Database/Browse 轴）、`projection.go:164-165`（taskGroup 翻译在投影层）、`swarm/translate.go:190` 注释与实现一致 | **已闭** |
| D-3 幂等键通用执法 | `application/idem/enforcer.go:1-11,35-43,48-97`、双源冲突 `:129-135`、重放/在途/24h `:140-206`；ADR-0024 | **已闭** |
| D-4 构建孤儿死循环 | `engine/crashrecovery_test.go:53-156`（在途重启/取消孤儿/硬杀三形态）+ `TestStopDuringBuildDrainsBounded`:160 | **已闭**（crash-recovery 测试类别承载） |
| P1-1 SSE 票据 + 长流超时 | `fleetlygrpc/contexts.go:439-451`、`assembly/gateway_events.go:27`、`assembly/grpc.go:30-41,79-87` | **已闭** |
| P1-2 API 面无超时 | `assembly/grpc.go:36-41,66-87`（unary 30s + longRunningUnary 豁免；注释已更正 60s=关停） | **已闭** |
| P1-3 事件断档三件套 | `outbox/repo.go:23-29`（EarliestSeq/TrimBefore）、`telemetry.proto:39,49,190-208`（410/earliest/last）；**残留**：`ListEventsRequest` 仍无 aggregate/name 过滤（`telemetry.proto:179-183`），outbox 无 `(aggregate,aggregate_id)` 索引（`migrations/00001_core.sql:98` 仅主键/seq） | **部分残留**（过滤/索引无消费者，低影响） |
| P1-4 List 分页惯例 | 39 个 List RPC 中 `limit` 字段 23、`after_*` 字段 24；`cmd/fleetly/cmd/rpc.go:42-58` pageFlags 单源；ADR-0026 | **已闭** |
| P1-5 managed reconciler 泛化 | `engine/managed.go:69-76`（per-域 gens map）、`:162-181`（多受管 Provider 迭代） | **已闭** |
| P1-6 四件一拍机制化 | `engine/transition.go:92-292`（commitTransition/transit 前门）、`guards/transitionguard_test.go:317-365`（Transit 只在 transition.go；手搓配对红） | **已闭** |
| P1-7 观测缓存分家 | `engine/observdomain.go:22-34` + `engine/task.go:7-15`（Task 独立缓存组/裁决路由） | **已闭** |
| P1-8 治理刹车执法缝 | `assembly/policy.go:45-48`（tasks/databases 入词表）、`engine/hygiene.go:28`（SweepOrphanSecrets）、`engine/task.go:62` + `options.go:54-56`（吊销排空/TTL 模式） | **已闭** |
| P1-9 双级稳定 DNS | `runtime.go:403-406`（Addressing）、`projection.go:343-354`（App/Task/RunDNSName 单源） | **已闭** |
| P1-10 Team 轴 | `guards/teamaxisguard_test.go:21-49`（engine/assembly 零 `"default"` 字面量）；`drift.go:150` 注释 | **已闭** |
| P1-11 只读/升级序 | 全仓无平台只读态（见 R15-M1） | **未闭** |
| P1-12 DatabaseSpec storage + 单翻译线 | `spec.proto:257-268` 无 storage；`engine/database.go:130-136` 走 `ProjectDatabase`（`projection.go:286`）而非 `AppSpec→Project`；无挂账条目 | **部分残留且无落期** |
| P1-13 gateway identity 挂载 | `assembly/gateway.go:41-46`（六服务全挂）+ 注释「P1-13/A-9」+ `guards/gatewayguard_test.go` | **已闭** |
| P1-14 swarm secrets 顺序 | `swarm/translate.go:247`（sortedKeys）+ `runtime_contentequiv_test.go`/`translate_test.go:177-195`（逐字节确定性） | **已闭** |
| P1-15 StreamLogs follow 多容器 | `swarm/logs.go:33-139`（goroutine fan-in，注释自述「非 Follow EOF 收尾/Follow 至 ctx 取消」） | **已闭** |
| C-6 schemaVersion 读面 | `engine/observ.go:22-33` 仍 `DiscardUnknown` 无版本检查；单版本不可证伪，深审本来判「挂账至首个升级批」 | **残留＝已挂账**（可接受） |
| C-10/RuntimeInspector 断言 | `swarm/provider.go:88` `_ capability.RuntimeInspector = (*Provider)(nil)` | **已闭** |
| C-11 drift 对照 Command | `engine/drift.go:304-307,354-356`（sameCommand） | **已闭** |
| C-12 叶子纯度守卫 | `guards/domainpurity_test.go:16,56-66`（全 `internal/domain/**`，含 capability/identity） | **已闭** |
| C-13 JobSpec 重塑 | `spec.proto` JobSpec 五字段整删 + reserved（挂账 #3，`e60563b`） | **已闭** |
| C-14 ADR-0003 措辞 | `architecture.md:58` 已加 2026-10-14 勘误（端口契约手写 Go，非 proto 化） | **已闭** |
| C-15 config_refs 通道 | `spec.proto:73-74` reserved；`spec.proto:96-100` config_mounts；`intake.go:49,68,153` | **已闭** |
| C-8 Metrics step | `capability/ports.go:402` 带 `step` 参数 | **已闭** |
| C-9 LogQuery 文本过滤 | `capability/runtime.go:708-716`（Text/Source） | **已闭** |
| C-17 RuntimeExec | `capability/runtime.go:68-81`、`faces.go:24`、exec 全链（F3.2） | **已闭** |
| Q-5 Transit RowsAffected | `persistence/deployment/repo.go:201-239`（显式归一 ErrConflict） | **已闭** |
| Q-6 loadSpec 脱离 ctx | `engine/observ.go:22` 签名带 ctx | **已闭** |
| Q-7 构建 goroutine 排水 | `crashrecovery_test.go:160-180`（有界 Stop） | **已闭** |
| Q-12/Q-13/Q-24 错误面 | `mapping.go:59-61`（中性冲突文案）、`engine/rollback.go:27-28` + `fleetlygrpc/delivery.go:298-299`（`ErrNoSuccessfulBaseline` 哨兵 + `E_NO_BASELINE` 映射）、`authn/interceptor.go:171-195` 有日志 | **已闭** |
| Q-15 DeleteProject 守卫 | `fleetlygrpc/structure.go:238-263`（无锁预检 + 事务内 noActiveApps/noActiveDatabases 复查） | **已闭** |
| Q-16 role/team 交叉校验 | F1.8 落地（`roleInTeam` 三面），本轮未逐行复核 | **按 F1.8 注记（未亲验）** |
| Q-14 gRPC recovery | 仅 gateway/proxyconfig 的 HTTP Recovery；gRPC 拦截器链无 recovery（`assembly/grpc.go:214-259`） | **残留**（P2 加固项，未挂账） |
| Q-18 指纹 pepper / Q-23 索引 / Q-26 Routes.List 全表 / Q-22 容量 | `swarm/network.go:104-108` 仍 sha256 fp8；无 `(app_id,generation)` 索引；`structure.go:501` 仍 `Routes.List(ctx)`；managedStep 30s 预算 | **残留**（深审列为 backlog/容量，未声称已修） |
| E-8/E-10/E-11/其他 | 本轮未抽样复核 | **未覆盖** |

### 2.2 收敛计划（2026-10-14）抽样核实

| 声明 | 核对结果 |
|---|---|
| P0-P3 全部勾选、任务项 38/38 全勾 | 计划文内 38 个任务行确为 `[x]`；抽 P0-1（`exec.go:37-38` 硬 TTL + `fleetlygrpc/exec.go:192-196` team 比对）、P0-4（`intake.go` job 白名单/拒绝理由）、P3-8（`engine.go` 211 行 < 300；`options/construct/lifecycle/ensure` 四文件在场）、P4-2（`cmd/fleetly/cmd/rpc.go:45-74` pageFlags + preValidateUsage）、P4-4（`sdk/go/fleetly/errors.go` FromError + 测试）均成立 | **真** |
| 依赖规则 5 行守卫 | `guards/dep_matrix_test.go` 在场且本轮 guards 子集全绿 | **真** |
| P1b 37 聚合上移 | `internal/domain/**/repo.go` 恰 37 个；`statertest` 全仓 0 残留 | **真** |
| P5-1 statekit 六原子 | `persistence/statekit.go` 五函数 + `TransitWhere` 常量（六原子）；`guards/statekitlaw_test.go` 在场 | **真** |
| P5-3 G19/G20 | `aggregatetestbelt_test.go` 白名单为空 map；`e2eciaccount_test.go` 白名单为空；CI 12 个 e2e job 对账 | **真** |
| 挂账 8 项 #1/#2/#3/#4/#5/#6 | #1 `rpc.go` pageFlags + 11 站点；#2 apitest `CreateProject/CreateApp` 消费面；#3 JobSpec 删；#4 ADR-0076 在册；#5 聚合带白名单清零；#6 `ci.yml` 含 e2e-template/browse/bluegreen | **真** |
| 挂账 #7 环境 / #8 staging 应用栈 | #7 外部环境（mongo 内核/ node2 已移除）无本机证据；#8 自述「torchwood server/worker boot 待 app 侧」＝部分 | **无法核实 / 部分（文内已如实）** |
| G1-G24「审查证据与执法手册在 v4 报告 §二-§四」 | **不成立**：v4 报告无 G 表（详见 R15-M2） | **未闭** |
| 审计轮「八项收官声明全部独立复证通过、无虚报」 | 八项抽样多为真；但 G1-G24 证据不可达使「复证」本身不可复核 | **部分可复核** |

### 2.3 声明式模型程序（2026-10-18）核对结论

- **ADR-0081（写面立法）**：`guards/writeclassguard_test.go:183-234` 真以 proto 全集对账 153 方法（27 ACTION/53 DECLARATION/73 READ 计数断言 + 未分类红 + 死条目红）；`:241-376` 三条从属断言（scope 单向 / FrozenVerbs / idem 前缀）。本轮实跑 `TestWriteClass*` 绿。**验收锚全部真实**。
- **ADR-0082（缺省收敛）**：`engine/converge.go` 有快通道四类信号、`ReconcileReplayInterval` 慢通道、签名生命周期绑定结果（`convergeSucceed` 才落签名，`convergeFail` 计冷却）、fail-closed 冻结闸门（`:304-314`）、冷却三解除径（`:362-396`）、posture 缺省 all（`options.go:208,257`，`TestConvergePostureDefaultsToAll`）、事件/审计（`:428-436`）、doctor/内置告警（`doctor.go:252-258`、`converge.go:40-41`）。钉测族：`converge_test.go` 19 个 Test 覆盖冻结/驱动/stopped/缺席/幽灵/teardown/gen/停摆/冷却/Cancel；本轮实跑 13 个相关 Test 绿。**行为面承诺基本真实**。
- **附录 29 项裁决（A1-A12/B1-B12/C1-C10）**：逐条找落码的抽样结论——A1/A3/A9/A10/A11 是文档修订型（已在 ADR/CONTEXT 落文）；A2/A4/A5/A6/A8/A12 落文 + 部分由 ADR-0082/0081 正文承载；B1（重投影而非缓存）/B2（驱动权同源）/B3（gen 下沉 driftScan）/B4（签名生命周期）/B6（teardown 标记）/B7（fail-closed）/B8（执行时重算）/B9（停摆行）/B10（内存态冷却）/B11（succeeded 无缓存触发）/B12（文档化不加执法）/C1（seq=0 不铸标签）/C2（runbook 逃生序）/C3（Go 表非注解）/C4（doctor+告警）/C5（SourceSystem 审计）/C6（skills 翻向）/C7（golden 族）/C8（写点纪律注释）/C9（双面契约，`verbs_structure.go:418-488`）/C10（SetUserPassword 判据）**均有可指认落点**；唯一未闭的是 C1 的 e2e:upgrade 首拍零滚断言（ADR-0083 验收锚第 8 项仍 `[ ]`，属诚实未勾）与程序计划「收尾门禁 `[ ]`」（但 runbook 记录·六十已记真机跨越与 e2e 矩阵绿，属**进度真源未回写**，非虚报，未列发现）。
- **ADR-0083（restart_seq）**：`spec.proto:560-568` 字段、`Workload.RestartSeq`（`runtime.go:421-431`）、投影（`projection.go:130`）、swarm/k3s 标签与 canonical 归零（`swarm/translate.go:35-38,145-146`、`runtime.go:438-444`、`k3s/translate.go:35-39,174-175`、`k3s/runtime.go:970-971`）、执行时 seq-only（`engine/restart.go:31-66` + `drive.go:123` + `spec/diff.go:140-146`）、compareSpecs 不比对（`drift.go:368-383`）、CLI 双面（`verbs_structure.go:410-488`）、console src/api（`5b97134f`）。验收锚前 7 项真实；第 8 项 `[ ]` 诚实。

### 2.4 与已交模块报告的边界（去重说明）

已独立复核并**不再重复报告**的交叉线索（本报告只在 §5 引用）：

- **R02-M2**（`convergeVerdict` 生产不可达、ADR-0082「表即断言」不成立）与 **R02-M5**（Cancel 遗留走慢通道、无审计无事件）——本轮以全仓 grep 独立复证一致（`convergeVerdict` 仅 `converge.go:176` 定义 + `converge_test.go:95` 调用；`converge.go:416-418` 仅 fast 源发事实），归 R02，不重报。
- **R03-L3**（`compose-dialect.md:62` 仍把 `configs` 列为未开放）——本轮独立复证一致（`intake.go:49,68,153-169` + `intake_test.go:697-751`），归 R03，不重报。
- **R02-L3**（ADR-0082 翻案后陈旧注释：`observ.go`/`networkrebuild.go`/ADR 行锚）——本报告 R15-L1 只补其未覆盖的三处**契约/注册表注释**（capability 契约、engine 事件 payload、eventcode 注册表），并注明互补关系。

---

## 3. 优点（带证据）

1. **ADR-0081 是「可静态执法的 ADR」样板，不是口号**：`guards/writeclassguard_test.go:183-234` 从 proto 反射枚举全部 153 方法，分别断言 27/53/153 三个台账实数，活条目缺失与死条目双向红；`:241-376` 另把 scope/FrozenVerbs/idem 三张旧表挂到同一法源。本轮 `go test ./internal/guards -run TestWriteClass` 绿。这是 ADR-0001→irguard 范式在新立法上的真实复现。
2. **2026-10-01 深审的批 0 行为修复基本真闭，且有「恢复类」测试类别承载**：`engine/crashrecovery_test.go:53-156` 用真 SQLite + 同库新 Engine 覆盖构建在途重启/取消孤儿/硬杀三形态（`rebuild` 幂等、禁振荡、永不触达 builder）；P1-6 的机制化由 `transitionguard_test.go:317-365` 反扫承载（Transit 直调只许在 `transition.go`，手搓「行写×事实发射」配对红）。这正好是深审 §10 提出的 M-3/M-1 元类的机制补强。
3. **收敛主体（ADR-0082）的语义钉测异常扎实**：冻结挂起/驱动权含 failed 窗/stopped 不收敛/缺席重建/BG 幽灵负向/进程缩减负向/teardown 竞态/gen 标签回滚/终态停摆/冷却三解除径/基线重放共用账本/posture detect/Cancel 遗留回卷——`converge_test.go` 19 个 Test 逐类承载，本轮实跑 13 个绿；`testdata/converge-verdicts.golden` 十个裁决行在场。对「防自激 + 防死锁」两个失败形态有明确测试面。
4. **审计 → 修复的闭环是真的，不是记账**：N2 评审 P1-3 要求的两节点 cadvisor 断言已落 `e2e/dind-two-node.sh:315-330`（VM 1/1 + cadvisor 每节点一 task + node 标签双值）；P1-5 的受管观测失败假滚动有 `engine/managed_obsfail_test.go`；P2-4 的匿名卷清扫有 `engine/hygiene.go:31` + `SweepOrphanVolumeCarriers`。反观 N2 评审判定的「记账缺陷」（F2.2/F2.3 缺 [x]）也确已补勾并带实录。
5. **runbook 的「诚实偏差」纪律可指认**：记录·六十（`staging-fleetly.md:1272`）主动记「install 前未先留旧版 .bak」并给出补救与教训；手动干预与收敛共存纪律（`:13-22`）把 freeze 优先标准逃生序逐条落到 scale-0/docker rm -f/service update/replicas 四族——正是 ADR-0082 审查 C2 承诺的收尾项，不是纸面承诺。
6. **守卫白名单未见腐烂**：抽样 `e2eciWhitelist`（`e2eciaccount_test.go:24`）、`sizeguardWhitelist`（`sizeguard_test.go:23-25`）、`aggregateTestWhitelist`（`aggregatetestbelt_test.go:26`）均为空表；`transitionguard` 两条豁免均命中且带理由（`:50-58`）——双向保鲜机制在抽样面有效，未发现「理由腐烂但守卫沉默」的条目。

---

## 4. 发现清单

> 组织：4.1 挂账类（历史清单未闭/验收锚空心）、4.2 声明类（ADR/文档承诺 vs 落码）、4.3 纪律类（生成物/勾选纪律）。组内按严重度。

### 4.1 挂账类

### R15-M1｜「恢复期平台只读」与升级「解除只读」是六份文档的现行承诺，全仓无只读闸门、无 Platform Restore 动词；P1-11 挂账随 F2.3 收口而未闭

- 维度：架构一致性（兼安全/可靠性）
- 严重度：**M**。校准：这是「安全语义类承诺零落码」而非现行 bug——Platform Restore 动词本就不存在（恢复靠 runbook 手工 restic），平台也没有任何运行期只读态；但没有闸门意味着文档宣称的「恢复窗口写保护」不可依赖，且该扣分项在 F2.3 打勾后失去挂账载体。非 H 的理由：目前没有任何自动化 restore 入口会把用户写操作暴露在恢复窗口内。
- 位置（文档）：
  - `CONTEXT.md:293-295`：`**Restore**: 从 Backup 重建数据；恢复期平台只读。`（词汇真源）
  - `docs/adr/0005-state-semantics-inherited.md:5`：`... Platform Restore 期间只读；孤儿只登记永不自动删。`
  - `docs/adr/0015-platform-upgrade-version-skew.md:3`：升级序末步 = `解除只读`
  - `docs/design/2026-09-30-architecture.md:148`：`Platform Restore 期间只读`；`:152` 升级序 `→ 解除只读`
  - `docs/runbooks/staging-fleetly.md:494`：`起新版（goose 前滚 + Managed Provider 逐个 reconcile + 解除只读，全自动）`
  - `docs/plan/2026-09-30-feature-checklist.md:80`：F2.3 `[x]` 条目文本含 `解除只读`
  - `docs/reports/2026-10-01-architecture-deep-audit.md:81`：P1-11 处置 =「接口 N1 前定，完整实现随 F2.3」
- 位置（代码/测试）：无。证据链：
  - `proto/fleetly/system/v1/system.proto` 全部 RPC = `GetVersion`(31) `GetStatus`(36) `RestartComponent`(44) `GetSchema`(54) `Explain`(60) `TriggerPlatformBackup`(75) `ListPlatformBackups`(84)——**没有 Restore**；`internal/interfaces/grpc/systemgrpc/` 全目录 grep `Restore` 零命中。
  - CLI 只有 `newPlatformBackupVerb`/`newPlatformBackupsVerb`（`cmd/fleetly/cmd/verbs_platform.go:20,51`；注册于 `cmd/fleetly/cmd/app.go:142`）——**没有 `platform restore`**。
  - 全仓 `read-only/readonly/E_READ_ONLY` 的 123 处命中全部是「卷只读 / browse 只读 / freeze 文案」；**无平台级只读态、无 restore-pending 全局闸门**（`internal/application/engine/backup.go:390` 的 `ListRestorePending` 只作用于单库还原）。
  - 最接近的既有机制是**全局 change freeze**（`proto/fleetly/system/v1/governance.proto:64-65`：`team_id 空串 = 全局冻结（全部 Team 的变更型动词封禁）`），但需人工 set/lift、升级 runbook 未使用、且不覆盖「Platform Restore 期间」。
  - 反向证据：同一 runbook `:528` 把该语义改写成「Platform Restore 期间控制面只读语义 = 恢复窗口内不引导服务」——即**同一个 runbook 内两种口径**（自动解除只读 vs 不引导服务），且最新记录·六十的换装步骤（`:1272`：stop → install → start）没有任何只读步。
- 影响：①运维/Agent 读 CONTEXT/architecture/ADR-0015 会以为恢复窗口有写保护或存在 `platform restore` 能力，实际恢复是手工 restic 流程（`:510-528`），且平台在恢复窗口内照常接受写；②F2.3 的 `[x]` 使 P1-11 失去挂账载体，后续评审无法从清单发现该缺口；③架构 §7 承诺的 skills 面含「Platform Restore」技能（`docs/design/2026-09-30-architecture.md:125`），`skills/` 实为三个（database-provision/deploy-diagnose/task-pool），无 platform-restore。
- 建议：二选一并同批改文——(a) 实现最小平台态闸门（`platform_state` 单例：restore/upgrade 窗口拒绝写动词，返回新 `E_PLATFORM_READ_ONLY`）+ `platform restore` 动词 + skill；(b) 若 v1 不做，则同批修订 CONTEXT/ADR-0005/ADR-0015/architecture §8/§7/F2.3 条目文本/runbook，统一改口为「restore = 离线手工序，无运行期只读态」，并把它列入 Backlog 带触发条件。成本：文档口径统一 S；实现闸门+动词 M-L。
- 状态：**已亲验**（文档 7 处、代码/CLI/proto 缺失面、runbook 内互斥口径均逐条读过；真机恢复窗口行为无法复验）。

### R15-M2｜收敛计划自称「审查证据与执法手册 G1-G24 在 v4 定稿报告 §二-§四」，该报告没有 G1-G24 表；守卫自述条目名「失考」并反推，审计证据链在仓内不可达

- 维度：文档一致性（兼可维护性/测试与守卫）
- 严重度：**M**。校准：不影响运行时，但它使一个已完成架构程序的 24 条审查裁决不可复核，且 6 个守卫是按「反推」执行的——可能拦的不是审查当初要求的那条规则。对「架构由测试承载」的文化，这是制度层缺口而非排版问题。
- 位置（文档）：
  - `docs/plan/2026-10-14-architecture-convergence-plan.md:3`：`审查证据与执法手册（G1-G24）在其中 §二-§四`
  - `docs/design/2026-10-14-architecture-convergence-report.html`：章节只有「一、终态分层」「二、六刀」「三、P0 案卷」「四、路线图」「五、否决清单」；全文对 `G1..G24` 的唯一命中是 P5 路线图行 `G5/G13/G19/G20`（HTML 第 152 行）；无任何 G 条目表/证据表。
  - `docs/plan/2026-10-14-architecture-convergence-plan.md:152-158`：「审计轮(2026-10-16)：八项收官声明全部独立复证通过、无虚报」。
- 位置（代码注释，自认失考）：
  - `internal/guards/nilfacelaw_test.go:5-6`：`G17 条目名存于 docs/design/2026-10-14-architecture-convergence-report.html，其底稿（原始执法承诺文本）已失效；形态由 P4-3 码律反推` —— **该报告无 G17 条目名**（grep 确认；报告里只有 E0/E_INTERNAL 案件叙述与「未配置」字样，无 G17 条目/律名）。
  - `internal/guards/perfthresholdanchor_test.go:5-6`：`G5 条目名已随执法手册底稿失考...形态由 P5-5 阈值锚批反推`
  - `internal/guards/migrationchainanchor_test.go:5-7`：`G13 条目名已随执法手册底稿失考...按 P5 批内真实缺口反推钉定`
  - `internal/guards/statekitlaw_test.go:5-7`：`G7/G8 条目名已随执法手册底稿失考...反推`
  - `internal/guards/aggregatetestbelt_test.go:4`：`同 G17 先例——条目名失考时以执法对象反推守卫骨架`
- 影响：①G1-G24 是本程序「审查证据/执法手册」的全部载体，v1-v3 底稿自述随 /tmp 失效（plan:3），v4 又没有 G 表——**24 条裁决在仓内零证据**；计划正文只触到 G5/G7/G8/G13/G15/G17/G19/G20 八项，其余 16 项（G1-G4/G6/G9-G12/G14/G16/G18/G21-G24）在全仓文档与代码中**零提及**，连条目名都没有；②G5/G7/G8/G13/G17 的守卫是反推产物，`nilfacelaw` 注明的出处对不上（报告无 G17 条目名），说明连「G 条目 ↔ 执法对象」的对应关系已不可考证；③「审计轮八项复证通过」失去可复核对象。
- 建议：把 G1-G24 的原始条目（或至少每条：条目名 + 要求 + 对应守卫/测试 + 若反推则注明原意不可考）作为附录补进 `docs/design/...convergence-report.html` 或独立 `docs/reviews/` 文件并 commit；修正 `nilfacelaw_test.go:5` 的错误出处；对五个反推守卫逐条登记「反推假设」，防止未来被误当原始要求。成本 S-M。
- 状态：**已亲验**（报告全文标签剥离 + G 记号/章节标题检索；五处守卫注释逐行读）。无法核实的是 G 原文内容（不存在于仓）。

### 4.2 声明类

### R15-M3｜ADR-0015 的「N-1 兼容：请求带版本协商头 + 超偏差明确升级提示」零实现、零守卫、零挂账

- 维度：文档一致性（兼架构一致性）
- 严重度：**M**。校准：部署形态是同装的双二进制（`install` 同批替换），现网没有跨版本客户端，故非 H；但这是 ADR 明文承诺且**没有任何挂账/backlog 条目**，属设计债。
- 位置：`docs/adr/0015-platform-upgrade-version-skew.md:5`：`CLI 与 server 版本偏差策略 = **N-1 兼容**：server 接受上一版 CLI；请求带版本协商头，超出偏差返回明确的升级提示；proto buf breaking 门禁保持。`；`docs/design/2026-09-30-architecture.md:152` 同款。
- 证据（无实现）：
  ```go
  // cmd/fleetly/cmd/dial.go:76 —— CLI 出站 metadata 只有 client-source
  ctx = metadata.AppendToOutgoingContext(ctx, authn.HeaderClientSource, "cli")
  ```
  全仓 grep `version.negotiat|x-fleetly-version|client_version` 零命中（`ClientVersion` 仅命中 `doctor.go` 的 docker 版本探测；`N-1` 仅命中迁移链守卫的检查点断言，与版本偏差无关）；`assembly/grpc.go` 拦截器链无版本校验（`:214-259`）；server 无「版本过旧」错误码（`errcode` 注册表无对应条目）。
- 影响：新 CLI 打旧 server（或反之）时，超出偏差的表现是 proto 解码错误/未知方法错误，而不是承诺的「明确的升级提示」；跨版本排障成本落在用户侧。
- 建议：二选一——(a) 实现最小面：CLI 在 metadata 带 `x-fleetly-cli-version`（buildinfo 已在），server 入口拦截器比对 major/minor 与 `N-1` 政策，超差返回带处置建议的新 errcode（成本 S-M）；(b) 若 v1 不做，同批修订 ADR-0015 的该句为「开发期无兼容负担（2026-10-14 用户裁决）；版本协商头随配置面/正式发布批次」，并登记 backlog（成本 S）。无论哪条，都该有 ADR 验收锚或守卫，避免下次再被当已实现。
- 状态：**已亲验**（ADR/架构原文、CLI metadata、拦截器链、注册表均检索）。

### R15-L1｜ADR-0082 翻案后，三处**契约/注册表注释**仍写「收敛默认 opt-in，ADR-0005」（其中一处还与已实现行为直接相反）

- 维度：文档一致性（兼架构一致性）
- 严重度：**L**。校准：注释错误无运行时影响，但本仓文化是「注释即契约」（深审 §10 M-1 明言「错注释比没注释更糟」），且其中一处挂在 Runtime 契约字段与 eventcode 注册表上，是读者/Agent 的第一入口。与 R02-L3 互补（R02 覆盖 observ.go/networkrebuild.go/ADR 行锚，本条覆盖契约/注册表三处，不重复）。
- 位置 + 证据：
  - `internal/domain/shared/capability/runtime.go:600-601`（WorkloadEvent.Drift 字段注释）：
    ```go
    // Drift 标记实际状态偏离已下发 Spec（检测默认开，收敛默认 opt-in，
    // ADR-0005）。
    ```
  - `internal/application/engine/events.go:60-61`（workload.rollout_stalled payload 注释）：
    ```go
    // 故与 drift 分立成事件；只观测不纠正（ADR-0005 opt-in），处置 = 人工
    // resume（docker service update --force）或重部署。
    ```
    ——但 ADR-0082 裁决表第 4 行与 `TestTerminalRolloutStallConverges`（`converge_test.go:386`）表明终态后停摆**已改为收敛**（重放途经 resumeStalledRollout）。
  - `internal/domain/shared/model/eventcode/events.go:33-34`（`workload.drift_detected` 注册表注释）：
    ```go
    // Drift 信号（...；检测默认开、收敛默认 opt-in，ADR-0005；ADR-0022 升级...）
    ```
  - 对照新法：`CONTEXT.md:282-284`（Converge「缺省开启（posture 缺省 all...翻案 ADR-0005 旧『收敛 opt-in』条款）」）、`docs/adr/0082-converge-by-default.md:22`（翻案登记行）。
- 影响：新读者/Agent 从契约类型与事件注册表读到的仍是旧法；按「注释即契约」文化，这会诱导实现者恢复 opt-in 语义（且 `workload.rollout_stalled` 注释与现行收敛行为相反，排障指引会把可自愈场景写成必须人工 resume）。
- 建议：同一 commit 修三处注释（rollout_stalled 改成「终态后由收敛重放 resume；部署窗内归 L1」），并在 wording/守卫面加一条「翻案清单登记后禁词」检查（例如 ADR-0082 翻案后 `收敛默认 opt-in|ADR-0005 opt-in` 在非 ADR 历史文档中命中即红）。成本 S。
- 状态：**已亲验**（三处注释 + 对照法源 + 相关测试名）。

### R15-L2｜架构文档 §4 的 IR 草图与 proto 现形不符（`configRefs` 已删、`DatabaseSpec.storage` 从未存在）；P1-12 的「补 storage + 单翻译线」两项都无落期

- 维度：文档一致性（兼架构一致性）
- 严重度：**L**。校准：读者面向的架构图错误，且其中一半对应一个**未闭环 P1**（P1-12）——没有挂账条目，属于「静默蒸发」而非明确 defer。
- 位置 + 证据：
  - `docs/design/2026-09-30-architecture.md:71`：`processes: [ { name, image|fromBuild, command, env, secretRefs, configRefs,` —— `configRefs` 已在 P4-1 删除：`proto/fleetly/spec/v1/spec.proto:73-74`：`reserved 7; // config_mounts 替代（ADR-0030），P4-1 删除；号位防复用。reserved "config_refs";`；现行字段是 `config_mounts`（`spec.proto:96-100`）。
  - `architecture.md:78`：`DatabaseSpec { engine, version, credentialsRef, storage, backupPolicy }` —— `proto/fleetly/spec/v1/spec.proto:257-268` 的 DatabaseSpec 只有 `schema_version/database/engine/version/credentials_ref/resources/backup_policy`，**无 `storage`**。
  - 深审 P1-12（`docs/reports/2026-10-01-architecture-deep-audit.md:82`）要求「F1.12 设计批：补 storage + 钉死『dbtemplate 渲染产物 = AppSpec』」；F1.12 已 `[x]`（checklist:71）但两项均未做：`engine/database.go:130-136` 走 `DatabaseSpec → specir.ValidateDatabase → ProjectDatabase`（`projection.go:286`），**独立于** `AppSpec → Project`（`projection.go:103`）的第二条翻译线；`storage` 字段不存在。
- 影响：①架构文档宣称的 IR 形状（configRefs/storage）与实际契约不符，新读者会按错形状写代码；②P1-12 的担忧（「第二份翻译实证漂移」）在 Database 域以 `ProjectDatabase` 形态客观存在，且没有 ADR 裁决说明「这是有意的第二投影」或「为什么不需要 storage」——深审 P1 的处置链断在 F1.12 批次里，后续评审无账可查。
- 建议：同批修 `architecture.md:71/78`（configRefs→config_mounts；storage 标注「未实现/backlog」或删除）；为 P1-12 补一条显式裁决（ADR-0029 追记或 convergence plan 活清单）：要么补 storage 字段 + ProjectDatabase 与 Project 的共享件收敛，要么成文「Database 投影为受管域专用第二线，共享件清单 + 漂移守卫」。成本 S（文档）/M（共享件）。
- 状态：**已亲验**（架构文档、spec.proto、database.go/projection.go、深审 P1-12、checklist F1.12 均读）。

### R15-L3｜README「状态」节严重过期：三项已交付能力仍写「排期中」，staging 口径与 runbook 相反

- 维度：文档一致性
- 严重度：**L**。校准：README 是仓库门面与 Agent 首读，错误会直接造成「能力不存在」的误判；但不影响运行时。
- 位置 + 证据：`README.md:85`：
  ```markdown
  pre-release（未发 tag）。N0.../ N1.../ N2 数据观测主线已落地并在 staging 双节点真机持续验收；
  排期中：ObjectStore 外置目标、只读 Console（N3）、k3s 第二运行时试点（N4）。
  ```
  对照现树：①ObjectStore 外置 S3 已落地 `[x]`（`docs/plan/2026-09-30-feature-checklist.md:85`，ADR-0042、`providers/s3objectstore` 在册、`e2e-backup` S3 离机腿）；②Console 早已不是「只读 Console」：F3.1 全功能写面 `[x]`（checklist:90）、F3.2 终端、F3.7 UI v2 均 `[x]`；③k3s 已 `[x]`（checklist:103，ADR-0052/0053），CI 有 `e2e-k3s`/`e2e-runtimeswitch`/`e2e-k3s-tw`/`e2e-k3s-ha` 四个 job（`.github/workflows/ci.yml:309,344,378,412`）。④「staging 双节点」与 runbook 头（`staging-fleetly.md:5`：2026-10-08 收缩为一台）及 `:27`（worker 退 swarm 改 k3s 单机）不符；`README.md:79` 的 e2e 清单也只列 5 个任务，现役 mise 有 12 个。
- 影响：新用户/Agent 读到「ObjectStore/k3s/Console 写面排期中」会按不存在规划；「staging 双节点」会误导排障拓扑。
- 建议：重写 README「状态」节为当前 N0-N4 实际状态 + 链接 checklist/ADR；e2e 清单改为 `mise.toml` 单一真源引用（避免双份清单）。成本 S。
- 状态：**已亲验**（README、checklist 对应行、ci.yml、runbook 头）。

### 4.3 纪律类

### R15-L4｜`proto` 变更与 `console:gen` 未同 commit：抽 200 commit 有 8 处 `console/src/api` 晚一个 commit 才再生成，至少 2 个 commit 的 `console:verify` 必红

- 维度：文档一致性（工程纪律）
- 严重度：**L**。校准：窗口一个 commit、下一 commit 自愈；但违反 AGENTS.md 硬规则且 CI console job 明写「红即『改了 proto/前端没再生成』」，属可复现的纪律执行缺口。
- 规则位置：`AGENTS.md:26`：`dist 是提交进仓的生成物，改 proto/前端后必须 console:gen && console:build 同 commit`；`.github/workflows/ci.yml:57-74` console job 跑 `mise run console:verify`（再生成+构建+零漂移）。
- 证据（两个亲验实例）：
  - `69a1294e`（ADR-0083 契约面）改 `proto/fleetly/spec/v1/spec.proto`、`proto/fleetly/delivery/v1/delivery.proto` 与 `genproto/.../delivery.swagger.json`、`structure.swagger.json`，**不含** `console/src/api/*`；后续 commit `5b97134f`（中间隔 a16d3b29）才加 `console/src/api/delivery.ts` 的 `restart_seq?: number`（diff 逐行核对）。→ `69a1294e` 时 `console:gen` 会产出该 diff，`console:verify` 必红。
  - `e9223043`（ListNodeContainers 契约面）改 `proto/fleetly/runtime/v1/runtime.proto` + swagger，**不含** `console/src/api/runtime.ts`；后续 commit `ad8cb7a5` 才加 `/v1/nodes/{node_id}/containers` 类型与 dist。
- 影响：commit 级 `console:verify` 红窗；bisect/回滚到该 commit 时类型面与 swagger 不一致；也说明「proto 批」与「console 批」的提交边界没有按 AGENTS.md 的原子性执行（CI 若按 push/PR head 跑，这个红不会被观察到）。
- 建议：把 `console:gen`（及需要时的 `console:build`）作为 proto 批 commit 的必带步骤；可在 `guards`/CI 增加「commit 内 proto/swagger 变更 ⇒ console/src/api 同 commit 变更」的检查（本地 hook 或 CI 对 base..head 逐 commit 断言）。成本 S。
- 状态：**已亲验**（两次 `git show --stat --name-only` + 5b97134f 的 diff）。

### R15-L5｜F2.8 `[x]` 的条目文本含「RustFS opt-in 自宿」，实现按 ADR-0042 明确不做；勾选未回改条目文本（半落地打勾）

- 维度：文档一致性（兼验收记账）
- 严重度：**L**。校准：ADR-0042 决策 3 与后果节已把 RustFS 挂账，属「有据的 defer」；但功能清单的约定是「每项带验收口径、批次验收 = 该批全部项打勾」，读者扫 `[x]` 会认为 RustFS 已交付。与 N2 评审批对 F2.1/F2.2/F2.3 的记账标准（P1 级）同源，故不并入「措辞」。
- 位置 + 证据：
  - `docs/plan/2026-09-30-feature-checklist.md:85`：`- F2.8 [x] ObjectStore：外置 S3 兼容配置 + RustFS opt-in 自宿；未配异地持续告警...`
  - `docs/adr/0042-objectstore-s3-provider.md:43`：`3. **RustFS 不随批（挂账）**：v1 只做外置 S3。...RustFS 受管 Provider（install.sh 物化 + 受管域 + opt-in）走后续批。`；`:91` 后果节再列「挂账：RustFS 受管自宿（决策 3）」。
  - 代码：全仓无 RustFS Provider/受管域（`rustfs` 仅命中 `internal/config/config.pb.go:375` 一句注释「S3 兼容端点（... 或自宿 RustFS）」）；`providers/` 下只有 `s3objectstore`/`localobjectstore`。
- 影响：批次验收声明与条目文本不一致；下游评审如果只读 checklist 会漏掉 RustFS 挂账（尽管 ADR 有载）。
- 建议：照 F2.1 的「移期」写法回改条目文本（`外置 S3 兼容（RustFS 受管自宿挂账，ADR-0042 决策 3）`），或在条目上拆子项分别勾。成本 S。
- 状态：**已亲验**（checklist、ADR-0042、providers 目录、grep）。

---

## 5. 交叉观察（不在本模块报告范围，供 Lead 去重/汇总）

1. **R02-M2 + R02-M5（ADR-0082 裁决表不可达 / Cancel 遗留无审计无事件）**：本轮独立复证一致——`convergeVerdict` 全仓仅 `converge.go:176` 定义 + `converge_test.go:95` 调用；`converge.go:416-418` 只有 fast 源发 `workload.converged`+审计行。建议 Lead 合并为一条「ADR-0082 验收锚①空心」。
2. **R03-L3（compose-dialect configs 漂移）**：本轮独立复证一致（`intake.go:49,68,153-169`；`intake_test.go:697-751`；schema 亦无 configs）。归 R03，R15 不重报。
3. **R02-L3（翻案后陈旧注释）**：R15-L1 是其补充（契约/注册表三处），建议 Lead 合并呈现。
4. **R07-H1/H2、R08-H1/H2、R10 的 `--wait` H、R12/R13 的 ADR-0082/0083 守卫缺口等模块级发现**：R15 只提示其**文档面**——若修复，需同批更新 ADR-0015（"路由零中断"）、ADR-0041（"查询隔离由平台查询构造执法"）、ADR-0035 决策 2/5 的验收锚，否则会形成新的「声明 vs 实现」缺口。R15 不重复取证。
5. **R09 的 README 安装链 H**：与 R15-L3（README 状态节）同文件不同段，建议 Lead 合并为「README 全面回写」一项。
6. **程序/进度真源未回写（L 级观察，未列发现）**：`docs/plan/2026-10-18-declarative-model-program.md:19,33-34` 的「收尾门禁」「文档收口」仍 `[ ]`，而 runbook 记录·六十（`:1268-1281`）已记 staging 换装 + e2e 矩阵真跨越绿；ADR-0083 第 8 锚亦 `[ ]` 但 runbook 已给真机结论。方向是「少报」不是「虚报」，建议 Lead 提醒补齐勾选/回写，避免下次评审无法区分「未做」与「已做未记」。
7. **ADR-0083「kind=restart 入 lineage」的形态变化（L 级观察）**：决策 7 原文「Deployment 行 + kind=restart」，落码注记自认「Deployment 行无 Kind 列，kind 只活审计 AfterFP 后缀」。ADR 文内自洽（注记在案），但用户面 `deployments` 读不到 kind；若后续 Console 要展示「本次是重启」，需新面。已由 R02-L2 覆盖字段从未填充，不重报。
8. **ADR-0082 detect 模式的启动例外（L 级观察）**：`converge.go:299` 对 `convergeSourceBaseline` 绕过 posture=detect 闸门（注释「ADR-0005 旧行为保留」），而 ADR-0082 §5 的 detect 定义是「只报事件不收敛」。§4/§6 暗示基线重放共用收敛记账，但 §5 没有写出这个例外；建议 ADR 补一句或补钉测（当前 `TestPostureDetectSkipsConverge` 不覆盖启动基线路径）。

---

## 6. 测试与守卫质量评估

**覆盖强度**：本模块相关的守卫面是本仓最强的一层——`writeclassguard`（153 方法全分类 + 三从属断言）、`transitionguard`（四件一拍反扫 + 防盲自检 150 函数门槛）、`gatewayguard`、`idemguard`、`converge` 的 19 个语义钉测、`crashrecovery_test.go` 的恢复类测试；抽样的 `e2eciWhitelist`/`aggregateTestWhitelist` 为空表、`sizeguardWhitelist` 零豁免。本轮实跑的 guards 子集与 engine 收敛/重启子集全绿（§1.3），说明这些守卫在当前树上不是「常年 skip」形态。

**断言强度**：抽样守卫普遍有三件好形态：①防盲自检（`transitionguard_test.go:191-202` 扫到 <150 函数即 Fatal；`writeclassguard:185-187` 同样）；②双向保鲜（豁免/白名单不再命中即红）；③红灯实验（多处注释记录「摘掉即红」）。`converge_test.go` 的负向用例（幽灵不重建、进程缩减不重建、teardown 竞态不重建、stopped 不收敛）质量高于平均。

**盲区/假阴性风险**（按影响排序）：
1. **ADR 验收锚的「空心」形态**：ADR-0082 锚①的「表即断言」由 R02-M2 与本轮共同证明是**死函数 golden**（改动真实判断不会红）；ADR-0015 的版本协商头没有任何守卫；P1-11 的只读承诺没有任何锚。→ 建议机制层：ADR 模板的验收锚若要打勾，必须指向**生产可达**的符号或守卫名（可加一个 `guards/adr_anchor_reachability` 静态检查：验收锚里出现的 Go 标识符必须在非 `_test.go` 中出现）。
2. **文档-代码漂移缺守卫**：`compose-dialect.md`/`schema.json`、`architecture.md` IR 草图、README 状态、CONTEXT Function 词条都没有「文档 ↔ 契约」对账；本仓已有 `registrysourceanchor` 这种「注册表锚必须指向现树函数」的范式，可扩成「契约文档里的 proto 字段名必须存在于现 proto」。
3. **commit 原子性无执法**：proto→console:gen 的耦合只在 CI head 上验证，提交序列内部可以红（R15-L4）。
4. **G1-G24 证据缺失**使六个守卫的「执法对象」不可考（R15-M2）。
5. **抽样未覆盖**：`Q-19/Q-20/Q-9/E-8/E-10/E-11` 等 P2 未逐条复核，不排除个别已修/未修判断需校正。

**守卫是否真能拦住本模块典型错误**：能拦住「新增 RPC 不分类」「跳过 transition 前门」「gateway 漏挂载」「白名单腐烂」四类；拦不住「ADR 写了没实现」「文档契约字段过期」「验收锚指向测试内死符号」三类——本报告 8 条发现里 6 条落在后三类。

---

## 7. 未决与置信度

**最不确定的 5 点**：

1. **R15-M1 的「只读」是否可用全局 change freeze 解释**：`governance.proto:64-65` 的全局冻结确实封禁全部 Team 的变更型动词，语义上接近「平台只读」。我的判断是它**不等价**（需人工 set/lift、升级 runbook 未使用、不覆盖自动 restore 窗口、且 CONTEXT/ADR-0015 的措辞是自动步骤），但若作者本意是「用全局冻结实现只读」，则本条应降级为「文档未写清实现载体」。置信度：中高。
2. **G1-G24 是否存在于仓外**（会话产物/对话记录）：本仓 docs 全树 grep 无 G2x 条目；按「入库才算证据」标准判 M。置信度：高（仓内），无法排除仓外。
3. **F2.8 RustFS 勾选是否被作者视为「ADR 已裁决即为已处理」**：若按「ADR 挂账 = 条目闭合」的口径，L5 可降为观察项。置信度：中（口径判断）。
4. **console:gen 的「同 commit」是否被团队解释为「同批（相邻 commit）」**：AGENTS.md 原文是「同 commit」，我按字面判 L；若团队惯例是同批，本条可降为观察。置信度：中高（规则文本明确，CI 描述也明确）。
5. **P1-12 的 `ProjectDatabase` 是否属有意的第二投影线**：仓内没有 ADR 明说；我按深审原意判「未落期的残留」。置信度：中。

**整体置信度：高**（对已亲验的 8 条发现，均有文档位置 + 代码/测试位置 + 本轮实跑或全仓检索；对无法亲验的 e2e/staging/全门禁声明均标「无法核实」，未据其下结论）。

**本次评审未做的事（防误读）**：未复跑 e2e；未验证 staging 真机声明；未跑 `console:verify`/`generate:verify`/全量 `mise run test`；未逐条复核 G1-G24（证据不在仓）；未系统阅读 R10-R14 的全文（只按关键词扫过其与 R15 可能的重复面：R02/R03/R07/R08/R09 的相关条目已引用并在 §2.4/§5 标注去重；若 R10-R14 另有同事实，以 Lead 去重为准）。
