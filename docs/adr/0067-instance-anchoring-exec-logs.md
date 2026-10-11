# ADR-0067: 实例锚定——exec 受理副本锚与日志容器过滤

日期：2026-10-11；状态：已采纳（用户裁决：机制补齐，非补丁——Console App Detail 的 Terminal/Logs 两 tab 到副本粒度）。

## 背景

exec 与 logs 的粒度天花板都在 process（ADR-0049/0040 的单机语义遗产）：

1. **exec**：`CreateExecSessionRequest` 只有 app+process；多副本进程由平台代选首个在跑实例（swarm/k3s 各自 sort+first），调用方无法锚定具体副本。CLI `exec --json` 的 meta 帧回显实例——事后才知道进了谁。
2. **logs**：`StreamLogsRequest` 过滤到 Workload（= app+process），多副本日志合流不可分；响应帧 `container`（swarm task ID，ADR-0040）已带归属，但入参侧没有对应锚。
3. **观测缺口**：全栈没有"进程的在跑实例集"列表面——Console Processes 页只有 spec 期望副本数（frozen spec），不是编排器实况。

打补丁形态（Console 侧自拼 task 列表、或 engine 外轮询）都会把编排器身份知识散到消费层——机制收口在域内。

## 决策

1. **`ExecInstances` 单一枚举原语**（capability RuntimeExec 面改形）：`ExecTarget`（单数、provider 各自 sort+first）退役，换 `ExecInstances(ctx, workloadID) ([]ExecTargetInstance, error)`——全部在跑实例、ID 升序（swarm task ID 时间有序、k3s pod 名字典序，同款确定性）。**选择策略单源 engine**：`resolveExecTarget`（auto = 首行；锚定 = 匹配），provider 只枚举不选择。
2. **受理实例锚**（proto only-add）：`CreateExecSessionRequest.instance = 5`——非空锚定该在跑实例（ListExecInstances 观测行的 instance 值），不中即 `E_NOT_FOUND`（新 engine 哨兵 `ErrExecInstanceGone`，映射同 E_NOT_FOUND 信封；**不代选替身**——会话与实例绑定的既有语义延伸到受理位）；空 = auto（既有语义）。审计/事件/会话回显链零改动（instance 本就在载荷里）。
3. **`ListExecInstances` 观测面**：`GET /v1/apps/{app_id}/exec/instances?process=…`（exec 资源 READ 档——列表面是受理前置，不开写钥匙）。实时快照（直读任务/pod 列表，观测缓存不参与决策——ADR-0049 同纪律）、升序、**空集 = 观测行非错误**（诚实空，与受理面哨兵语义分立）；node 归属经平台锚定表反查（未及的实例 node 空——诚实呈现）。
4. **日志实例锚 `containers`**（三轴通用）：`StreamLogsRequest.containers = 11`（repeated，gateway 重复键传形与 processes 同款）——载体归因标识直锚（swarm task ID / k3s pod 名 = `StreamLogsResponse.container` 同源词汇，**无派生公式**，故不限 app 轴），对寻址轴收敛出的载体集做交集；条目非空精确拒绝（processes 同纪律）。三径落点：
   - **swarm 实时径**：服务标记归属校验保持（targets 循环不动），任务按 ID 精确匹配逐载体 `TaskLogs`——**tail 语义下沉到具体载体**（逐实例 TailLines，非先聚合后裁剪）；不筛任务状态（刚退出的副本尾部日志仍可读——日志面锚定身份非活性）；details 缺席的行按请求任务 ID 兜底归因（帧身份锚不丢）。
   - **k3s 实时径**：pod 名精确匹配（同"不筛相位"语义）。
   - **VL 检索径**：`fleetly_task` 流字段 OR 并组进 `{}`（与 Workload 条件交集由 LogsQL AND 自然承载）。
5. **CLI 能力面对齐**：exec/shell 加 `--instance`（旗标前置铁律），新动词 `exec-instances <app-id>/<process>`（列表 + --json 双 golden）；Console Terminal 加 Instance 下拉（auto 缺省、进程切换即重置）、Logs tab 加 Replica 下拉（all 缺省）——10s 轮询在跑实例集（观测缓存不参与**决策**，只进下拉展示）。
6. **词汇锚**：exec 域 instance = 编排器副本载体（ADR-0049 既有；CONTEXT.md 的 instance _Avoid_ 是 Run/Database 词条的避用词，exec 域不受限）；日志过滤字段 containers 与响应帧 container 同源——消费端拿响应帧的 container 值即可反向锚定。

## Consequences

- 多副本诊断闭环：副本下拉 → 锚定 exec/日志 → 帧归因可核对——Console 与 CLI 同能力面。
- `ExecTarget` 退役是 capability 面破坏性改形（仓内 3 实现点同批收编）；帧协议/会话语义零改动（ADR-0049 在线兼容保持）。
- 列表面 10s 轮询是显示面刷新（非决策输入）；受理仍实时解析——锚不中即 E_NOT_FOUND，快照漂移由诚实失败兜底。
- swarm TaskLogs 径使实例锚日志不经 ServiceLogs 聚合——跨容器交错面在该形态下消失（单副本语义，符合锚定意图）。

## 验收锚

- [x] proto only-add：`buf breaking` 过（对 origin/main）；genproto/swagger 同批再生。
- [x] engine 选择策略单源：apitest `TestExecListInstances`（升序快照/node 回显/auto 代选/锚定命中/锚不中 E_NOT_FOUND）。
- [x] swarm 实例锚径：`TestStreamLogsContainerAnchor`（服务校验保持、未命中任务不开流、不回落聚合流）+ `TestStreamLogsContainerAnchorFallbackAttribution`（details 缺席兜底归因）。
- [x] VL 检索径：`TestBuildLogSQL` task 字段单值/并组/交集形态锚。
- [x] CLI：exec/shell `--instance`、`exec-instances` 动词双形态 golden 钉死；cmd 包全绿。
- [x] Console：`console:gen && console:build` 零漂移同 commit（dist 进仓）；Terminal Instance 下拉（auto 缺省）+ Logs Replica 下拉（all 缺省）。
