# ADR-0063: wait 协议任务维度与三 verb 等待循环收编

日期：2026-10-11；状态：已采纳（架构评审第三轮候选 3，同批落地）。

## 背景

Run 终态/活跃判定与 wait 协议逐层重编码（架构评审第三轮候选 3，Strong）：

1. 三个等待 verb（WaitDeployment/WaitBuild/WaitRun）各手搓一份 send 闭包——「读行 → 状态未变不重发 → 发帧 → 终态帧后收流」的循环不变量三份并行；WaitDeployment 的终态判定是内联四值 switch，与 state 层既有 `deployment.State.Terminal()` 平行漂移。
2. `WaitRun` 只收 Run id，而 Run 随驱动环下一拍才铸出——`tasks create --wait` 被迫先轮询 ListRuns ≤30 次等首 Run 出现（客户端轮询是架构 §7 明言不自写的形态）。
3. `[]run.State{pending, running}` 字面量在 engine 六处重铸（观测终态迁移 from-集 ×3、drain 列表、stopRunRow from-集、StopTask force 列表）——与 `run.State.Active()` 语义同形却各写一份。

## 决策

1. **`waitOnRow` 泛型唯一循环**（wait.go）：各 verb 只供四个 variation points（行读+域词错误映射、状态键、行型既有 `Terminal()` 谓词、帧构造）；「状态未变不重发 + 终态帧后收流」不变量单源。WaitDeployment 换用 `deployment.State.Terminal()`，内联 switch 删除。`waitOnAggregate`（订阅核心）保留。
2. **WaitRun 任务维度**（proto only-add，ADR-0026 形状）：`WaitRunRequest.task_id = 2` 与 `id` 精确一（皆空/双填 `E_INVALID_ARGUMENT`）；REST 走 `additional_bindings { get: "/v1/tasks/{task_id}/runs/wait" }`。语义：**钉等待开始时该 Task 的最新 Run 至终态**（one-shot 即唯一 Run；resident 钉首个观测 Run，不随补足换靶——文档钉死）。无 Run 时事件驱动补获：订阅 `run` 聚合、payload `task_id` 命中即重列钉定——过滤键经 `engine.RunPayloadTaskID` 导出（runEventPayload 的 json 键单源，API 层不持键知识；订阅起点先于钉定读，waitOnAggregate 同款两步间幂等重读）。Task 已终态且无 Run → `E_CONFLICT` 诚实拒绝不挂流（终态拒绝既定形，不为边缘面开新 errcode）。授权 `authorizeTaskID`（ADR-0035 行级面）。
3. **CLI poll-30× 退役**：`tasks create --wait` 改吃 task_id 等待；`waitRunOnFrame` 的 run id 改取帧（任务维度下 CLI 预先不知道 run id，by-id 路径输出逐字节不变）；`requireRunOK` 保留——它是失败政策编码（failed 非零退出、stopped 家族正常收口），非终态集重编码。
4. **`run.ActiveStates` 单源**：Active() 的集合形态（CAS from-集 / ListByTaskStates 过滤集共用，stopping 不在列——收口态不占活槽位）；engine 六处字面量收编。共享切片只读（Transit/runStateIn 不持留不变更）。
5. **剔除裁决——proto 行消息派生布尔**（评审卡片申报项，实核后否决）：console 的 display-active 含 `stopping`、服务端 `Active()` 不含——display 语义与记账语义分裂，单一 wire 布尔必错一端或成为第三个语义；ADR-0012 冻结状态值集使 console 本地重编码漂移风险有界，tone 映射属装饰。不动契约。

## Consequences

- CLI `tasks create --wait` 的 30 秒轮询窗与 "no run appeared within the wait window" 错误面退役；等待语义从客户端猜测窗变服务端事件驱动，长任务不再受客户端窗上限。
- console WaitRun 收流视图（已排期批）落在任务维度 seam 上，无需自建轮询。
- `waitOnRow` 泛型参数面（4 closure）是新等待 verb 的接口成本；不变量与哨兵语义（errWaitDone）单源收益随 verb 数增长。
- REST 任务维度绑定（additional_bindings）随 swagger 再生成进网关面。

## 验收锚

- [x] apitest `TestWaitRunTaskID`：精确一双填/皆空 `E_INVALID_ARGUMENT`；Run 未铸出即等待 → 驱动环铸出后补获 → 钉定 Run 逐帧至终态收流；删除未铸 Run 的终态 Task → `E_CONFLICT` 拒绝不挂流。
- [x] 既有 `TestWaitRun`（by-id）/ `TestWaitDeployment` / `TestWaitBuild` 全绿——收编行为保持。
- [x] CLI golden 零漂移（runs-wait 双形态、tasks-create 全系）；cmd 包全绿。
- [x] `buf breaking` 过（only-add）；genproto/swagger/dist 同 commit（`console:gen && console:build` 对账）。
- [x] `run.ActiveStates` 收编六处字面量；engine/state 包全绿。
