# ADR-0076: unary 面请求校验 descriptor 单源——protovalidate 执法注解 + 双面同源预检 + REST 400 投影

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-16 | ADR-0075（Clean 四环——interfaces 读径写必经 application 的校验槽位归属）、ADR-0070（buf breaking 开发期基线追认先例）、架构收敛程序 P4 批（`docs/plan/2026-10-14-architecture-convergence-plan.md`）、架构 §6（proto 是唯一契约源） |

## 背景

P4 之前，请求校验是三份手写拷贝：服务端各 RPC 的 `validateX` 函数族、CLI
旗标互斥的手拷贝块、REST gateway 的 400 面——同一条规则（required /
格式 / 互斥 / 范围）三处实现，文案与严格度各自漂移。86 处手写站点与
compose 受理批修复过的"断言失败即静默跳过"族同源（normalize.go 的
Sscanf 端口截断即先例）；CLI 手拷贝在 DeployRequest 互斥执法上有 55 行
与 proto 注解逐字平行的死拷贝。开发期无向后兼容负担，单源窗口唯一。

## 决策

1. **protovalidate 注解 = 请求形状规则的唯一契约源**。required / 格式 /
   互斥 / 范围族规则全量落 proto 的 `buf.validate` 注解（field 级规则 +
   message 级 CEL，`proto/fleetly/*/v1/*.proto`）；descriptor 经 buf
   generate 进 genproto，三面（服务端 / CLI / REST）只消费同一法源。
   手写校验族 86 站点退役（P4-1 两波 46+40，逐 commit 实数）；
   `ProcessSpec.config_refs` 随批删除（field 号/名位 reserved 防复用，
   buf breaking 对 origin/main 恰一条红 = 开发期基线追认，ADR-0070 先例）。

2. **CEL 自定义 message 逐字节保文案**。用户可见文案的契约锚从手写函数
   迁入 proto 注解原文——退役以「文案逐字节一致」为验收线（DeployRequest
   四源互斥 13 条 message 级 CEL = P3-7 不变式⑦零重钉）；空值短路纪律
   （坏输入恰产出一条违规）；隐式存在字段对零值照常求值（CEL 语义注记）。

3. **双面同源预检**。服务端 = assembly 拦截器链 validate 槽位
   （`internal/assembly/grpc.go` newValidateInterceptor）；客户端 =
   `internal/interfaces/grpc/apivalidate.Validator`，CLI 侧
   `preValidateUsage`（`cmd/fleetly/cmd/rpc.go`）以同一 descriptor 在
   发送前预检，usage 错误 exit 64。两面无第二法源；CLI 手拷贝 55 行退役。

4. **REST 400 投影契约**。gateway 面校验失败统一投影 HTTP 400 +
   E_INVALID_ARGUMENT 信封（形态随 P4-1 批 1 新钉 golden）；proto 注解
   字段注释渲染 openapi property title，消息级注释不渲染（防迁移纪要
   泄漏进 API 文档）。

5. **本律不追认的射程边界（手写保留，另议）**：动态参数化文案面——
   RFC3339/cron/PEM/JSON 语法细节、注册表枚举、`%q` got 回显族——
   descriptor 静态表达力不覆盖，手写校验在这些面继续合法；builder 族
   回显静态化利弊在案（P4-2 批 2 记录），是否再 descriptor 化另批裁决。

## 验收锚（可静态执法承诺 → 执法面）

- [x] 手写校验退役不回潮：负例矩阵 descriptor 形态 91 案 + 正例 34 案
      钉死于 `internal/interfaces/grpc/apivalidate/apivalidate_test.go`
      （执法面 = 矩阵本身：规则形状或文案漂移即红；不另设扫描器守卫，
      理由 = 形状规则的理论空间由 proto 注解承载，矩阵按退役站点逐案
      反向覆盖已闭环）。
- [x] 双面文案逐字节一致：apivalidate_test 以同一 descriptor 断言服务端
      与 CLI 预检产出（P4-1 批 1 试点族锚，随批扩展全族）。
- [x] DeployRequest 四源互斥 = 不变式⑦文案逐字节
      （`internal/testsupport/apitest/submit_spec_invariants_test.go`
      承载，P3-7 零重钉）。
- [x] REST 400 信封形态 golden 随批钉死（P4-1 批 1）。
- [x] spec 包负例：ParseSpecFile 对含 `config_refs` 键的 spec_file 受理
      面拒绝 + 读回面 DiscardUnknown 兼容（P4-1 批 1 步 4 双钉）。

## 后果与挂账

- 校验规则变更从此单点生效（改 proto 注解 → buf generate → 三面同瞬
  一致）；文案即契约，改文案 = 用户可见变更，须 golden 同批。
- 挂账（另批/另议）：runRPC/pageFlags 全量迁移（CLI 存量 dialFromEnv
  直连约 126 处，骨架与 exemplar 在 `cmd/fleetly/cmd/rpc.go`）；apitest
  builder 三件全量迁移（约 54 文件）；动态文案面 descriptor 化评估；
  JobSpec deprecated 五字段（`spec.proto:207-212`）基线追认候选，待用户
  裁决。
