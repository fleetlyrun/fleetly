# ADR-0024: 幂等键 header 形态与受理写路径（受理位 + 统一写原语）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | R-4/D-3/C1 裁决（批 0.5）、ADR-0016（admission 去重双层窗口）、ADR-0007（Acceptance 词条随本 ADR 入册）、深审报告 §1.D-3/§5.R-4、架构评审（29 处内联 Tx 实证） |

## 背景

两条现状合流：

1. **幂等键无通用执法结构**：现状只是 `deployments.idempotency_key` 列 + 活跃态部分唯一索引；`FindActiveByIdempotencyKey` 只查活跃、同键命中返回既有不比请求体；`admission_idem_test` 钉死"终态后同键→受理为新部署"的 N0 口径——与领域模型场景 6 目标语义（同键同体重放返回同一结果、同键异体 409、24h 保留）方向相反。形态裂缝：body 字段（delivery.proto 现状）与架构 §7 措辞 `Idempotency-Key` 头两套真源并存，冲突规则无答案。
2. **写路径无组合原语**：api 层是事实应用层（117 处 repo 直调 vs 9 处 engine 调用），四件一拍（写 + 事件 + 审计）在 api 手写 29 处、engine 另有 2 份拷贝（`transit`/`transitBuild`，R-8 已排队）；受理守卫（父资源存活 / 配额 / 删除守卫 / FK 归属）7 族散落在 `*Services` 上，无枚举面，配额读在事务外（PutConfig TOCTOU）。

## 决策

1. **幂等键 header 为主**：`Idempotency-Key` 头是唯一通用形态；拦截器级一份实现覆盖全部创建型 RPC。`DeployRequest.idempotency_key` 降级为部署专锚（commit 去重锚）。**双源冲突规则**：同键 body 与头不一致 → 拒绝（专用错误码入册）。
2. **D-3 结构**：单表（key → RPC 方法 → 请求体指纹 → 响应引用 → expires_at=24h）+ janitor 清理。同键同体重放返回同一结果；同键异体 409。聚合内活跃唯一索引**保留**为 ADR-0016 admission 去重——两层窗口语义：admission 活跃窗口（部署域去重/合并/抢占）+ 通用 24h 幂等窗口（跨终态结果重放），互不吞并。A1 测试显式改口径；webhook 去重锚随此收口（去重与效果同事务，或以幂等键承担）。
3. **受理位（Acceptance）**：api 层受理判定归一为可枚举 module——父资源存活（`requireActiveProject` 族）、配额、删除守卫、FK/归属校验同住；受理读全部移入受理事务（修配额 TOCTOU）。与部署 Admission 分立：受理位答"收不收"，Admission 答"怎么排"。
4. **统一写原语**：四件一拍一个 Tx 作用域原语，engine transit 与 api 写路径共用。R-8 的 transit helper 由此从"engine 状态机试点"扩为全写路径适用；R-8 原保险条款保留——deployment/build 先迁移验证表达力，各线特例超三成即回退为约定 + 守卫反扫。
5. **Acceptance 词条**随本 ADR 进 CONTEXT.md（ADR-0007 显式流程）。

## 后果

- F1.1 落地序：幂等表迁移 + 拦截器 → webhook 去重收口 → 受理位 module 抽取（29 处内联逐步迁移，新写面必须走原语）。
- `E_IDEMPOTENCY_KEY_CONFLICT` 入册随 F1.1（含双源冲突码）。
- **同批守卫任务**（ADR 含可执法承诺，同批开守卫）：①受理面反扫（枚举全部创建型/删除型 RPC × 受理检查在册，未覆盖面显式豁免带理由——guard C 同款）；②幂等覆盖反扫（新创建型 RPC 无幂等执法即红）。随实现批次（F1.1）落地，非本 ADR 批。

## 验收锚（F1.1 三段落地 2026-10-01：c5c7159 + 89cab2f + 阶段 3，证据=测试在树）

- [x] 同键同体重放返回同一结果；同键异体 409；body/头双源不一致被拒（idem 单测 + apitest 全链）
- [x] 终态后同键同体仍返回原结果（通用 24h 窗；admission 活跃窗口语义不变——两层窗口并存）
- [x] 24h 过期后同键作新请求受理；janitor 清理可观测（TestRetentionExpiry / TestSweepRemovesExpired）
- [x] 全部创建型 RPC 经拦截器幂等执法（反扫守卫 TestIdempotencyCoversCreateVerbs，红灯实验过）
- [x] webhook 重投不产生重复部署（去重与效果同事务：gateway delivery 派生键重放 + 台账随事实落）
- [x] 配额检查与写同事务（configQuota 受理检查；并发风暴下不超限）
- [x] 受理面反扫守卫在册全绿（TestAcceptanceWritePathsGoThroughCommit）；api 内联 `.Tx(ctx` 写编排归零（TestNoHandRolledTxChoreography，唯一豁免点=原语本体 acceptance.go）
