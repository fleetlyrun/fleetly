# 批 0 修复实施计划（2026-10-01，N1 前置）

| 项 | 值 |
|---|---|
| 底稿 | `docs/reports/2026-10-01-architecture-deep-audit.md` §8（真缺陷 10 组）+ §10（机制守卫 A~F） |
| 口径 | **以报告 §8 为准**：批 0 = §8.A 全部 10 组真缺陷 + 守卫 A~F + C-10/C-12 守卫补全 + Q-5/Q-6/Q-7/Q-9 卫生项。§7 的窄口径由本文件取代 |
| 非目标（严禁顺手做） | 批 0.5 设计裁决（D-1/D-2/D-3 契约字段、R-1~R-8）、一切 N1 功能、容量类 P2（Q-22/23/26/E-11）、Q-21（随 F1.1 幂等批）、C-6/C-13/C-14 |
| 工程纪律 | 注释中文、用户可见文本英文；新测试与修复同 commit；golden 用 `go test <包> -update` 联动；proto 零改动（本批全部不动 proto）；errcode/eventcode 注册表只增（本批预计零新增——E_CONFLICT 已在册） |

## 阶段切分（5 段串行，每段一个子代理，段间验收门）

### 阶段 1：引擎修复 + crash-recovery 测试类别

范围（`internal/engine` + `internal/state/deployment` + `internal/state/build`）：

| 项 | 修复方向 | 验收 |
|---|---|---|
| D-4 构建孤儿死循环 | driveBuilding 对既有 queued Build 且进程内无输入登记者**幂等重建输入**（prepareBuildInput 本身幂等：检出目录已存在即跳过）；executeBuild 无输入分支改为一跳到终态（deployment 已不在时的孤儿 build，不得回 queued 弹跳）——终态选 failed/cancelled 由实现者定，消息英文、注释说明 | 新 crash-recovery 测试两条：①构建在途→停 Engine→同库新 Engine→DriveOnce 有界拍内 Build 到终态且部署继续推进；②部署取消后遗留 Build 一拍内到终态、无 queued↔building 振荡 |
| Q-5 Transit 不查 RowsAffected | deployment 与 build 两个 repo 的 Transit 检查 RowsAffected==0 → `state.ErrConflict`（secret/hook repo 是仓内正例） | 单测：并发/前置不符路径返回冲突 |
| Q-6 loadSpec 脱离取消链 | `loadSpec(ctx, …)` 签名加 ctx，调用方全部有 ctx 透传 | `grep context.Background internal/engine` 仅剩合法处（构建 goroutine 根） |
| Q-7 构建 goroutine 不入排水 | Engine 记录 runCtx；executeBuild 的 ctx 从 runCtx 派生（Stop 可取消，超时独立）；goroutine 计入 wg。注意：入 wg 后 Stop 需能经取消信号让长构建退出，不得让 Stop 默等 15m | 停机测试：Start→构建中→Stop(ctx 有界)→构建行回 queued 且 wg 排水完成 |
| Q-9 revSeq 静默 0 | Revision 读取失败硬失败（同驱动步内 revision 必在） | 单测：Revision 读失败→部署 failed 带精确错误 |
| 守卫 D（crash-recovery 类别） | hermetic 夹具新测试形态：真 SQLite + 新 Engine 同库重启 + 有界拍断言确定终态。落成可复用 helper（如 `restartEngine(t, e)`），build 线第一条用例即上表 D-4 两条 | 下一个内存态恢复缺陷在此层红 |

明确不做：不动 admission/observ 语义；不重构 Loop。

### 阶段 2：swarm Provider 修复 + 确定性守卫

范围（`internal/providers/swarm`）：

| 项 | 修复方向 | 验收 |
|---|---|---|
| P1-14 secrets 遍历序 | toServiceSpec 对 secretCarriers 按 platformName 排序后再遍历 | 守卫 E（下行） |
| 守卫 E（翻译确定性） | 单测：同输入 Workload（≥2 secrets、≥2 networks、≥2 volumes、≥2 ports）两次 `toServiceSpec` 的 json.Marshal 逐字节相等 | 任何非确定性来源（含将来新字段）在此红 |
| Q-4/P1-15 StreamLogs follow | 多容器 fan-in（每容器一 goroutine 合流到单 writer；ctx 取消回收全部）；非 follow 与 follow 统一走同一合流路径。测试需要窄缝（容器枚举/日志流两函数抽 interface 或等价 seam）——测试不依赖真 docker | 单测：双容器 follow，两容器帧都到达、取消后 goroutine 回收（goexit 断言或泄漏检查） |
| Q-19 吞错 | anchorNodes/pollTasks 错误 Warn 日志（pollTasks 是 L1 权威源，静默不可接受）；`time.Sleep(time.Second)` 改 ctx 感知等待 | 代码审阅 + 现有测试全绿 |
| Q-20 不辨 NotFound | NetworkInspect/SecretInspect：NotFound→create；其余错误上抛（service 路径 isNotFound 是正例） | 单测：注入非 NotFound 错误→Ensure 失败带原因（不撞"已存在"） |
| C-10 Inspector 断言 | provider.go 断言补 `_ capability.RuntimeInspector = (*Provider)(nil)`，注释"三个子面"改实数 | 编译期红即达意 |

明确不做：不动 Watch 事件模型（D-1 批 0.5）、不加 container 终态映射。

### 阶段 3：装配/API 超时与限额

范围（`internal/assembly` + `cmd/fleetly/cmd/dial.go` + `internal/authn`）：

| 项 | 修复方向 | 验收 |
|---|---|---|
| P1-2 无请求超时 | unary 链加超时拦截器（流式豁免；默认值建议 30s，常量注释说明选值）；`grpcTimeout` 常量更名 grpcShutdownTimeout 并更正注释（lynx WithTimeout=优雅关停超时，历史命名警示）；CLI 拨号默认 deadline（流式动词豁免——follow 不被杀） | apitest：注入 sleep 的 handler 在限期内切 DEADLINE_EXCEEDED；`fleetly logs --follow` 路径不受影响（读代码+测试） |
| Q-11 限额错位 | 显式 `grpcMaxRecvMsgSize`（≥ hookPayloadLimit，建议 32MiB）为单一真源，装配设置 server option；启动断言或常量关联 `hookPayloadLimit ≤ grpcMaxRecvMsgSize` | 测试：5MB webhook payload 经原生 handler 到达 ReceiveWebhook（不再死于 4MB 不透明错误）；>25MiB 仍走设计 413 |
| Q-24 authn 吞错 | GetBySHA256 区分 `state.ErrNotFound`（→invalid token）与其余（→E_INTERNAL + ERROR 日志） | 单测：注入 DB 错误→E_INTERNAL 且有日志 |

### 阶段 4：API/错误面/残留缺陷修复

范围（`internal/engine/materials.go`、`internal/api/fleetlygrpc`、`internal/providers/traefik`、`internal/capability/runtime.go` + swarm InspectWorkloads、`internal/engine/drift.go`）：

| 项 | 修复方向 | 验收 |
|---|---|---|
| Q-8 凭证查询吞错 | registry Secret 查询区分 ErrNotFound（匿名）与其余（上抛）；applyVolumePinning 的 GetByName 错误改硬失败（漏合并钉住=无钉住调度，数据风险） | 单测：DB 错误→部署失败带原因，不静默匿名拉取 |
| Q-12 ErrConflict 文案 | mapStateError 对 `state.ErrConflict` 用 E_CONFLICT + 中性文案（"conflict: …"），不再一律 "already exists" | apitest 断言错误码/文案 |
| Q-13 文案 Contains | engine 加哨兵（如 ErrNoBaseline，engine.go 哨兵区）；delivery.go 改 errors.Is 映射 | 单测：哨兵路径 E_INVALID_ARGUMENT（或现有码）稳定 |
| Q-10 routeKey 碰撞 | 键生成改单射（替换发生时追加原始键短哈希，或保序编码）；traefik config_test 补碰撞对用例 | 碰撞对单测红转绿：`(a.b,/c)` 与 `(a.b-c)` 两键不同 |
| C-11 drift 缺 Command 对照 | capability.WorkloadObservation 加 Command 字段（只增）；swarm InspectWorkloads 回读 ContainerSpec.Command；compareSpecs 加 Command 比对；fake 底座同步 | drift 测试：改 command→drift 事件带明细；不改→零 drift（对翻否定断言沿用） |
| Q-15 DeleteProject 无守卫 | 有存活 App（未 tombstone）→ E_CONFLICT（提示先删 App）；无存活 App 才软删（路由已随 App 删除走 ADR-0023 ③，无需级联）；Project 级材料（Secret/Config/Volume/Network）随自身生命周期不动——注释写明该边界 | apitest：有活 App 拒删/删 App 后可删；CLI golden（projects delete 双形态）-update 联动 |

### 阶段 5：守卫 A/B/C + 叶子纯度 + REST 挂载

| 项 | 方向 | 验收 |
|---|---|---|
| P1-13/A-9 REST 未挂载（根修） | gateway 注册清单补 identity 六服务 + Hooks 配置面（SetGitHook/GetGitHook/RotateHookToken——ReceiveWebhook 保持原生挂法）；apitest 走一条 REST identity 路由验证 | 守卫 A（下行）绿；REST 调 GetVersion/whoami 类冒烟过 |
| 守卫 A（注解对账） | guards 新测试：枚举 proto http 注解面（解析 `*.pb.gw.go` 或 proto）↔ assembly 注册清单一一对应；不匹配即红并列缺口；豁免须带理由注释且双向保鲜 | 摘掉任一注册行→CI 红 |
| 守卫 B（禁 Contains 映射） | guards/scan 加规则：`internal/api` 内 `strings.Contains(err.Error()` 禁止（阶段 4 已消除现存实例） | 人为加一处→红 |
| 守卫 C（覆盖反扫） | guards 新测试：①capability.Runtime 方法×子面×关键旗标（Follow）↔ 测试覆盖存在性；②proto 全部 `Delete*` RPC ↔ apitest 存在"活跃下级拒绝或显式级联断言"用例。豁免登记表带理由、双向保鲜（吸收 P2 尾巴 drain/Follow 缺测项——若 drain 仍缺，登记豁免或本批补测） | 新增 Runtime 面/子面/Delete RPC 无覆盖→红，列缺口名 |
| C-12 叶子纯度 | leafSubtrees += `internal/capability/`、`internal/identity/`（先跑守卫确认现状零违例——capability 只 import 标准库、identity 只加 lynx 外部库，预期干净） | 守卫绿；人为让 capability import state→红 |
| 流程 G（弱层） | AGENTS.md ADR 纪律段补一句："ADR 含可静态执法的承诺，须同批开守卫任务（ADR-0001→irguard 为范式）" | 文档落地 |

## 验收门与还原点

- 每阶段：子代理执行汇总 → 主会话**一手取证**（跑所涉包 `go test -race`、读 diff、跑 lint）→ 全局一致性检查（不破坏前序、不越界改批 0.5 对象）→ commit（注明阶段号）为还原点。
- 终验：`mise run lint` 0 issues + `mise run test`（三 module -race）全绿 + `mise run generate:verify` 零漂移（本批不动 proto，应零漂移）+ 改动文件清单与本计划核对 + e2e 可跑则跑（dind smoke 至少编译/静态面）。
