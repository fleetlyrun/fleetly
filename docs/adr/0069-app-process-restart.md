# ADR-0069: 用户 App 的 Process 级重启动词

日期：2026-10-12；状态：已采纳（同批落地：delivery 契约 + engine + console + CLI）。

## 背景

排障动线的缺口：进程卡死（如内部状态腐化、需要就地重启）时，用户唯一的合法手段是重新 Deploy。但 ADR-0064 内容寻址 Ensure 的语义是**内容未变零替换**——重发未变更 Spec 的新 Deployment 会零滚动收敛（这是当初为治"全员替换"事故而定的正确语义），副作用是"重启"没有显式入口。Console IA v3 设计文档的二期挂账清单明确列有"App restart/scale 专用动词"（restart 半边随本 ADR 收口，scale 半边另账）。

平台受管域已有先例：`RestartComponent`（system 治理面，IA v3 二期④）经 `capability.RuntimeRestart` 可选子面（swarm = `ForceUpdate++` 滚动重排；k3s = pod 驱逐由 Deployment 自愈重建）强制重排受管载体。用户 App 域的载体同属 Runtime 管（每进程一个载体对象，`WorkloadID = appID + "-" + process` 投影公式单源），子面能力现成，缺的只是路由与授权面。

## 决策

1. **restart = runtime-only 强制重排，零 lineage**：不动镜像不动 Spec、不产生 Revision/Deployment、代戳不动（swarm ForceUpdate 计数与 `fleetly.generation` 代戳分立；k3s pod 删除后 Deployment 自愈重建副本，模板字节不动）。重启不进部署叙事（无 diff、无蓝绿窗），与"重新部署"是两个动词——Console 上 Redeploy（重发冻结 Spec）与 Restart（本动词）并存且语义各自诚实。
2. **复用 `capability.RuntimeRestart` 可选子面，缺席精确拒绝**：引擎侧类型断言，未实现者返回既有哨兵 `ErrRuntimeNoRestart`（swarm/k3s 在册；第三 runtime 缺席时 honest error）。不新增 capability 面。
3. **进程存在性引擎侧先验**：对当前冻结 Spec（最新 Revision）校验进程名，未知/未部署同归哨兵 `ErrProcessUnknown` → E_NOT_FOUND。这是防假成功的唯一防线——k3s 的 pod `DeleteCollection` 对零命中静默返回成功，裸驱逐会让 typo 得到假绿。
4. **授权与冻结归族**：授权面与 Rollback 同款（`authorizeAppIDOnly`，app 域写面）；`FrozenVerbs` 入表（`field: app_id`, `KindApp`）——重启虽不动记录，载体重排即 Workload 变更族，冻结期照拒（拦截器表是唯一执法清单，反扫守卫双向对账）。
5. **无 audit/event（与 `RestartComponent` 同口径）**：audit 承载记录面变更（谁改了什么），restart 不产生记录差异；事件面同理不加码。真机观测走既有实例观测原语（ADR-0067）与 Provider 侧滚动。
6. **粒度 = 单 Process**；App 级整体重启不做专用动词（N 个进程 = N 次调用，或走 Redeploy 语义）——避免为罕见形态扩 RPC 面。

## Consequences

- 排障闭环：Console Process 行 / `fleetly apps restart` 一键就地重启，短抖动（rolling 逐副本），镜像与 Spec 零变更。
- "按了没反应"类困惑被两个动词的诚实文案消化：Redeploy 捞共享变量（未变进程不滚）、Restart 真滚动（有抖动）。
- 第三 runtime 落地时须实现 RuntimeRestart 子面，否则该域 restart 精确拒绝（既有哨兵，golden 已钉）。

## 验收锚

- [x] engine 单测：目标 Workload ID = 投影公式（appID-process）、ns = app 域三元组；未知进程 / 未部署 / 空参同归 `ErrProcessUnknown`；无重启子面精确拒绝（`internal/engine/processrestart_test.go`）。
- [x] apitest 全链：gRPC → handler → engine → 子面真达（`Runtime.RestartCalls` 断言）；零 lineage 断言（restart 不触发 re-Ensure）；冻结期 `E_CHANGE_FROZEN` 拒绝（`internal/apitest/processrestart_test.go`）。
- [x] CLI 双形态 golden：人类行式回执 + `--json` 空体 + 未知进程 E_NOT_FOUND 错误信封（`cmd/fleetly/cmd/apps_restart_golden_test.go`）。
- [x] `FrozenVerbs` 反扫守卫对账通过（新增 RPC 未入表即红的守卫语义保持）。
