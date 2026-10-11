# ADR-0066: 写序列 kernel 收编（internal/writes）

日期：2026-10-11；状态：已采纳（架构评审第三轮候选 2，同批落地）。

## 背景

「四件一拍」的段序不变量（检查 → 聚合写 → 事件 → 审计，任一段失败整单回滚）曾有三处实现：API 受理位 `commit`（acceptance.go，ADR-0024 原语）与 engine 驱动域两个 kernel——`commitTransition`（状态迁移，2026-10-03 已收编单源）与 `commitWrite`（非迁移写）。评审实核收窄：与 API commit 平行的是 **commitWrite 的序列**（transition 族已是 engine 内部单源、只被内部门户消费）；两份序列并行各自演化正是 2026-10-03 漂移史（task 域拷贝"注释承诺审计、代码无审计"）的温床，仍可能再次分叉。

## 决策

1. **新叶子包 `internal/writes` 拥有写序列**：`Sequence(ctx, tx, outbox, audit, Fact)`——checks → write → events → audits，任一段失败即返回、后续段零执行；单回滚由调用方事务承载。`Fact` 收归一化形态：`Events []func() Event` 与 `Audits []func(ctx) []*audit.Entry` 均 lazy（负载/审计字段可依赖事务内写的返回行）；Audits 组形一次求值可返 0/N 条。sink 直收具体 repo（两侧同形 `*outbox.Repo`/`*audit.Repo`，不设接口抽象）。
2. **两个薄 adapter，~37 调用点零改动**：API `commit` normalize 现有 `writeFact`（事件闭包捕获可变 `eventFact` 追加时读取——`withPayload` 就地填充语义逐字节保持；audits/auditsFrom 并为审计组）；engine `commitWrite` normalize 后委托，**steps 逐段以空 Checks 形重放同一序列**（编排留 engine，域差异段不入 kernel）。`commitTransition` 不动（CAS + 原地迁移抑制事件是 engine 自有迁移语义）。
3. **方案取舍（B 而非 A）**：A（全量 Fact 单形统一，~37 调用点换形）的额外收益——eventFact/auditFact 类型单源、withPayload 模式退役、审计铸型单源——偏外观且机械面大；B 以零调用点风险绝根文档在案的失败模式（序列漂移）。A 化作为后续按域增量批挂账（见 Consequences）。
4. **无新跨仓守卫承诺**：ADR-0024 双守卫（TestAcceptanceWritePathsGoThroughCommit / TestNoHandRolledTxChoreography）钉的调用形状不变——handler 照旧调 `svc.s.commit`，`DB.Tx` 直调仍只在 acceptance.go——自然过。

## Consequences

- 两份序列循环归一：新段序规则（如未来加第五段）只改 kernel 一处，API 与 engine 自动跟随；漂移失败模式绝根。
- `internal/writes` 成为后续任何新写面的共享底座：A 化（Fact 单形、审计铸型单源、withPayload 退役）可按域增量推进，每域一批、守卫随批。
- engine `auditFact`（声明形）与 API 直建 `audit.Entry` 双形保留——差异是真实的（engine 延迟到 ctx 求值、API 在 handler 持 actor），kernel 组形同时容纳两者。
- kernel 段序以 `internal/writes` 包测钉死（LastSeq 探针 + 失败隔离记录器）；变异验证=事件段提到写前必红。

## 验收锚

- [x] `internal/writes` 包测：段序（检查=write 前 / 事件=write 后 / 审计=事件后）与失败隔离（check/write 注入失败后续段零执行 + 失败事务 LastSeq 不变实锤回滚）。
- [x] 变异验证：kernel 事件段提到写前 → 段序断言红，还原复绿。
- [x] 双侧行为保持：engine transition 测试、apitest 全栈、双守卫全绿；golden 零漂移。
- [x] 调用点零改动：`s.commit`/`commitWrite` 签名不变，~37 处调用点未触。
