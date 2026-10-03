# ADR-0030: firstBootJobs 部署链接线——releasing 前半串行执行、deployment 行游标、回滚永不重跑

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-02 | CONTEXT.md Task 词条（部署期特例裁决，ADR-0007）、ADR-0012（Task 双形态/Owner Lease）、ADR-0016（admission/状态机）、ADR-0018（墙钟 deadline）、ADR-0017 附录 A.1（Task 配额）、ADR-0025（决策 8 解锁条件/C-13 JobSpec 重塑）、ADR-0029 决策 5（挂全部活跃项目网先例） |

## 背景

F1.12 评估结论：firstBootJobs 接线需要独立的状态机设计批——observing 态等待
语义、失败/超时回滚与 job 重放幂等、Task 属主锚，非"量小独立 commit"。本
ADR 收口全部裁决。执行机制已备：一次性 Run（F1.5/F1.6）落地即"job 的执行体
存在"（ADR-0025 决策 8）；JobSpec 已重塑为嵌 ProcessSpec（C-13，旧标量字段
退役保留）。词汇冻结（ADR-0007）：部署期 init job = Task 的部署期特例，
不另立实体——本 ADR 只接线，不造新词。

## 决策

1. **执行序 = releasing 态前半（jobs 子相位），materialize 之前，串行**。
   job i 成功才铸 job i+1；全部成功才进 carrier 子相位（现有 L1/L2/L3 不变）。
   依据三点：
   - spec.proto 冻结注释（`first_boot_jobs` 词条）："releasing 前串行执行，
     失败即回滚"；
   - canonical 用例（数据库迁移）要求迁移在**切换流量前**跑：jobs 子相位期
     间旧 Revision 载体原样在服，迁移失败时回滚对载体是 Ensure no-op（零
     滚动）；"observing 后置"方案否决——新代码先服流、迁移后跑是反直觉序，
     失败窗口内新代码面对旧 schema；
   - from_build job 依赖构建产物 digest：building 完成才可解析（preparing
     不可行——StatePreparing 注释的"前置 Job"设想在 C-13 from_build 语义下
     不成立，以本 ADR 为准）。
   部署停留在 StateReleasing 等 job 终态（tick 再进，幂等）。**不新增状态**：
   状态机 widening 是领域词汇变更；子相位由行游标（决策 3）完全可观测，
   API/CLI 暴露锚定 Task。
2. **铸造形态与属主（schedule 先例，spawnScheduleTask 同款）**：Task 行
   `Name=""`（不占项目名位——终态 Task 永久占名）、`owner_token_id=""`（无
   Owner Lease：部署链是系统属主，生命周期由部署状态机拥有）、
   form=one-shot、desired_concurrency=1、DNS 公式照常。事件流 `task.*` /
   `run.*` 即 job 的生命周期观测面，零新观测词。
3. **持久锚 = deployments 行新列 `first_boot`**（TEXT NOT NULL DEFAULT ''）：
   - `''`：未开始（或 spec 无 job）；
   - `<idx>:<taskID>`：等待第 idx 个 job 的 Task 终态（0 基）；
   - `done`：全部完成——此后 carrier 子相位，重启不再重铸。
   铸造 + 游标推进 + 等待 deadline **同一事务**（spawnScheduleTask + Fire
   CAS 同款防御：游标非空 ⇒ Task 行必在）。重启重放零重铸：'' → 铸、
   `i:task` → 查行等待、`done` → carrier。API/CLI 暴露
   `first_boot_task_id`（当前锚定 Task；由游标解析，无 job/已完成为空）。
4. **等待语义**：
   - job 成功判定 = 锚定 Task 的唯一 Run 终态 `(stopped, completed)`；其余
     一律部署失败（error 文本点名 job 名与原因）：Run `failed`（exit ≠ 0）、
     `stopped/ttl_expired`（作业超时）、`stopped` 其他起因（stopped_by_user /
     platform_drained…——被人工终止也是"没有完成迁移"）、Task 行被删。
   - 等待 deadline = **铸造时刻 + ttl + FirstBootWaitGrace**（新引擎选项，
     缺省 2m：覆盖 mint→Run 创建滞后 + 终态观测滞后；镜像拉取计入 Run 自身
     TTL——Run deadline 从创建起算）。落 `observe_deadline`（RFC3339 墙钟，
     ADR-0018 重启续算）；到期未终态 → failDeployment + best-effort 强停锚定
     Task（防超窗迁移 job 继续写库与回放竞态）。`done` 迁移时清
     observe_deadline（carrier 子相位首拍自设 L1 截止——两子相位共用该列，
     游标是消歧真源）。
   - **JobSpec.ttl 必填**（0 < ttl ≤ 86400s）：无界等待的部署不是合法状态，
     校验面 fail-closed 拒绝（理由精确）。
   - Task 配额（ADR-0017 附录 A.1）命中 = **有界等待重试**（deadline 收口），
     不立即失败——配额是暂态压力，job 是 Task、配额即配额。
5. **回滚与重放幂等**：回滚（自动 rolling-back / 显式 `fleetly rollback`）
   走 materialize(from_revision)，**永不重跑任何 firstBootJobs**——失败
   job 已终态；旧 Revision 的 job 属于旧部署的既成历史（迁移已应用，重跑
   反而破坏）。同 Revision 再部署（新 Deployment 行）= jobs 重跑：**job 体
   须幂等**（at-least-once per deployment attempt，迁移工具的标准假设；
   修订 spec.proto 注释与本 ADR 同口径）。
6. **取消/抢占收口**：Cancel / 显式 supersede / latest-wins 合并命中
   jobs 等待中的部署时，游标锚定的活跃 Task best-effort `StopTask(force)`
   （审计携带操作者——迁移 job 对新部署无意义，且防与新部署 job 的并行
   写库竞态；已终态不动；错误容忍记日志）。已无界风险为零：job 自带 ttl。
7. **job 网络挂靠（铸造时解析，冻结进 TaskSpec）**：job 需要连数据库
   （`db-<id>` 在项目网上），而 TaskSpec 的 API 面只有 network_group——
   - 缺省 = **项目全部活跃网络**（`projectNetworkNames` 单源；ADR-0029
     决策 5 同款：job 是 App 部署期特例，App 连得上什么 job 就连得上什么；
     无路由暴露面，挂靠不授入站）；
   - 声明时 = job process.networks 三形态（项目网名 / `taskGroup:<name>` →
     `taskgrp-<name>` / `project:<id>/<name>` 跨 Project **严格**解析——部署
     链恒 strict，未批准 fail-closed）；解析结果（平台网络名）落冻结 spec；
   - ProjectTask 加宽渲染 `process.networks`（原样平台网络名，铸时已解析）。
     `validateTaskCommon` 的禁令维持 **API 受理面**执法不变；engine 铸造面
     豁免由本 ADR 记录（豁免面 = 部署期 job 一种，无第二消费者）。
8. **校验面（spec 叶子包 `ValidateJob`，ValidateApp 挂钩）**：name 必填、
   spec 内唯一；process 必填、image origin 必填（image | from_build）；
   process.name 空 = 铸造时落 job.name（非空须相等——名字单一真源）；
   ttl 必填 (0, 86400s]。禁面 fail-closed（理由精确）：volumes（Task 域
   Workload 不渲染卷）、config_refs（Task 域材料面无 config 注入）、ports /
   healthcheck（一次性执行无 L1 门）、placement、replicas>1（单 Run）。
   **intake 面不在本批**：compose 扩展键 / spec_file 裸 AppSpec 面随后续
   API 扩展批（DeployRequest 注释既挂）；本批交付引擎链接线 + 校验面，
   测试以 Revision 夹具直证。
9. **事件与 errcode**：eventcode +1 `deployment.first_boot_job`（每次铸造
   即发；payload `{deployment_id, app_id, generation, job_index, job_name,
   task_id}`；三链咬合：eventcode 注册表 + schemareg + golden）。job 失败
   走既有 `deployment.failed`（error 文本点名）；job 观测面 = 既有
   `task.*`/`run.*`。errcode **零新码**（部署异步失败无 RPC 错误面；校验
   失败 E_INVALID_ARGUMENT 既有）。
10. **Provider 零改动**：job 载体 = Task 域 per-Run service 既有形态
    （F1.5）；网络挂靠是 Workload.Networks 既有面。

## 后果

- `observe_deadline` 承载三相位截止（job 等待 / L1 / L3 观察窗），消歧真源
  = `first_boot` 游标；列注释同步。
- job Task 终态后行永久留存（部署记录同款审计单位）；计入 per-Project Task
  配额（活跃期）。
- 存量部署行为零变化（无 job 的 spec 游标恒 `''`/`done` 不经过，prepare 的
  诚实拒绝移除）。
- F1.15 dogfooding 若需部署期迁移，先落 intake 面（compose 扩展键或
  spec_file）——checklist 挂账。
- observing 后置形态（job 在 L1 后跑）若未来出现真实需求（如"迁移必须在
  新代码就绪后跑"的场景），按新 ADR 扩展 JobSpec 执行点声明，不回头改本
  裁决的缺省。

## 验收锚

- [x] 串行 converge：两 job spec → 依序铸造（job 2 在 job 1 终态
  (stopped, completed) 后才出现）、全成 → materialize → 观察窗 →
  succeeded；事件序列含两条 `deployment.first_boot_job`（task_id 各异）
  （TestFirstBootSerialConverge）
- [x] 失败回滚：job Run failed → deployment.failed（error 点名 job）→
  自动回滚 materialize(from_revision)，**jobs 不重跑**（无第三次铸造）
  （TestFirstBootJobFailureRollsBack；附带修复：failDeployment 清相位
  局部 ObserveDeadline——job 等待截止不泄漏进回滚相位）
- [x] 超时：job Run stopped/ttl_expired → deployment.failed 点名超时；
  等待窗到期未终态 → failDeployment + 锚定 Task 被强停
  （TestFirstBootTTLExpiryFails / TestFirstBootWaitTimeoutForceStops）
- [x] 重启重放幂等：铸造后行游标 `i:<taskID>` 持久；重启（缓存清空）后
  不重铸、等待恢复；`done` 后 carrier 相位重放不受扰
  （TestFirstBootRestartReplayZeroRemint + 重复拍零重铸断言）
- [x] 取消/抢占收口：Cancel/supersede 命中 jobs 等待 → 锚定 Task 进入
  draining/stopped 族（审计携带操作者）
  （TestFirstBootCancelAbandonsJob / TestFirstBootSupersedeAbandonsJob）
- [x] 校验面：无 ttl / replicas>1 / volumes / config_refs / process.name
  不一致等拒绝理由精确（TestValidateJob，ValidateApp 挂钩含重名拒绝）
- [x] 网络解析：无声明 → 全部活跃项目网（铸入 spec + ProjectTask 渲染
  双断言）；`taskGroup:` 翻译；未批准跨 Project 引用 → Submit 受理预检
  拒绝（CheckPeerRefs 覆盖 job 面）（TestFirstBootSerialConverge /
  TestFirstBootNetworksDeclared）
- [x] from_build job：digest 铸时解析为完整引用；引用非 from_build 进程
  → 精确失败（TestFirstBootFromBuildDigest）
- [x] 配额有界重试：项目 Task 配额满 → 等待重试至 deadline（不立即失败）
  （TestFirstBootQuotaBoundedRetry）
- [x] 三链咬合：`deployment.first_boot_job` eventcode + schemareg +
  golden 同 commit；errcode 零新码
- [x] API/CLI：Deployment 消息暴露 first_boot_task_id（字段 + mapping
  接通 engine.FirstBootTaskID 单源公式已落）；protojson golden 已随
  ADR-0033 intake 面批补——compose 扩展键夹具驱动全链，releasing 等 job
  终态的 deployment golden 钉 first_boot_task_id 在场（done 游标下归空
  由 apitest 全链断言）
- [x] staging 真机实证：部署期迁移 job 端到端（随 F1.15 dogfooding）（F1.15 ⑦：compose `x-fleetly-first-boot-jobs` migrate→roles-sig 串行执行、失败即回滚、部署 succeeded；runbook 2026-10-02 节）
