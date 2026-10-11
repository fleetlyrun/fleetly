# ADR-0062: Task 行铸造单源（Mint seam）

日期：2026-10-11；状态：已采纳（架构评审第三轮候选 1，同批落地）。

## 背景

Task 行装配知识曾两处手搓：API 受理位（`automation.go` CreateTask 内联行构造）与 engine 铸造原语（`minttask.go` mintOneShotTask）。漂移已实际发生：Form 推导分叉出两种写法（`spec.TaskForm(s)` vs `task.FormOneShot` 字面量）；protojson 冻结选项两处各写一份；one-shot 缺省并发 1 两处手补（壳硬编码 1、受理位归一化补 1）。

架构评审第三轮（improve-codebase-architecture 流程，2026-10-11）将「Task 行铸造 seam」列为候选 1（Strong）：行形状的两个生产者（受理位 + schedule/firstBoot 域铸造调用点）实存，deletion test 通过——删掉单源，知识弹回各调用点（配额拷贝与"注释承诺审计、代码无审计"两起漂移先例都在同族缝上）。

## 决策

1. **engine 包导出纯函数 `MintTaskRow(projectID, name, ownerTokenID string, spec *specv1.TaskSpec) (*task.Task, error)`** 作为 Task 行装配唯一所在：身份注入（TaskRef + 属主 Token，原地变更调用方 spec）→ 行缺省（Form 推导单源 `spec.TaskForm`、one-shot 缺省并发 1）→ protojson 冻结（snake_case / 不带未填充字段，与 AppSpec `marshalSpec` 同口径钉死）→ 行装配（`TaskDNSName` 铸名、`StateActive`）。无 receiver：四步是纯计算，不假借 Engine 状态。
2. **spec 验证不住铸造**（实施中被 cmd golden 实证打回的初版裁决）：铸造输入的效性契约不统一——firstBoot 铸造 spec 是平台投影，携带 `taskGroup:`/`project:` 跨挂 networks，而 `ValidateTask` 的"Task 网络附件只有 network_group 一条轨"禁用（ADR-0012）只治用户提交面；且 `ValidateTask` 要求 TaskRef 在场，而身份锚恰由铸造注入。验证归各入口受理面：API = `normalizeCreateTask`（`ValidateTaskTemplate` ref 缺席面，规则与 `ValidateTask` 逐条相同，错误文案逐字不变）、schedule = 模板冻结时 `ValidateTaskTemplate`、firstBoot = AppSpec 受理位 `CheckFirstBootNetworks`。铸造只保证注入构造出的 ref 非空。
3. **两个薄 adapter**：API 受理位（CreateTask：authz → `resolveOwnerToken` → `normalizeCreateTask` 请求形状面 → `MintTaskRow` → 受理 commit，ADR-0024 守卫形状不变）与域铸造壳（`mintOneShotTask`：`MintTaskRow` → `commitWrite` 四件 + `domainSteps` + `taskLoop.Kick`）。受理位 `normalizeCreateTask` 收窄为请求形状（E_INVALID_ARGUMENT）+ spec 组装，不再碰 taskID/TaskRef/冻结。
4. **静态守卫**：`TestTaskRowMintingSingleSite` 反扫非测试代码的 `state/task.Task` 复合字面（go/ast 精判 + 按文件真实 import 别名解析，`map[string][]*task.Task{}` 之类元素类型字面不误报），唯一合法点 `minttask.go`；豁免带理由双向保鲜。
5. **词汇**：CONTEXT.md 增补 **Task Mint（铸造）** 词条；既有词条零改动（ADR-0007 冻结不破）。

## Consequences

- 行形状新事实（新列派生、新缺省规则）只改铸造一处；受理位与三个域铸造调用点（schedule / firstBoot / F2.2 backup）自动跟随——locality 收口。
- 冻结体字节形态不变（选项钉死）：存量行读回零漂移，无数据迁移；golden 双形态零漂移已实证。
- one-shot 缺省并发 1 收编入铸造且只缺省行值、不改写冻结体：firstBoot（spec 不带 Desired）与 schedule 模板（可声明 0）场景行值保持 1，配额统计口径不变。
- 初版"验证入铸造"的深裁决被 3 处 cmd golden（deploy_compose_jobs / deployments_list_first_boot / deploy_supersede）当场证伪——验收门禁拦住设计错误，验证归位后全绿。裁决 2 记录的是修正后形状。

## 验收锚

- [x] `TestTaskRowMintingSingleSite` 绿：唯一合法点 `minttask.go`；变异探针（受理位重植 `task.Task{` 字面）必红，还原复绿。
- [x] tasks-create 系列双形态 golden 零漂移；deploy_compose_jobs / first_boot / supersede 金科玉律复绿（验证归位后）。
- [x] 受理位与 schedule/firstBoot 铸造均经 `MintTaskRow`（域壳不再自带行形状）。
- [x] one-shot 缺省并发 1 收编入铸造；firstBoot spec 不带 Desired 场景行值保持 1。
- [x] CONTEXT.md 增补 Task Mint 词条，既有词条零改动。
