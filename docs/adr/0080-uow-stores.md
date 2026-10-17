# ADR-0080: UoW 落地——Repo 接口去执行器参数，事务作用域显式 bundle（Stores/Do）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-17 | ADR-0005（四件一拍，执行面条款被本 ADR 显式推翻）、ADR-0070（可读实体 id，seq 铸造随视图走）、ADR-0075（Clean 四环，本 ADR 是其完全实现） |

## 背景

ADR-0005 时代的执行面约定「聚合 repo 方法一律接受 state.Runner（*sql.DB 或 *sql.Tx 均满足），可独立执行也可组合进事务」在 P1b（ADR-0075）Repo 接口上移 domain 后从未按新宪章重审。2026-10-17 用户对 `internal/domain/structure/project/repo.go` 提出 Runner 作参数不符合最佳实践，经两轮社区取证与设计推演裁决：

1. **社区证据**：执行器作参数在 Clean/DDD 文献零正面样例；Three Dots Labs 2024 明确判为反模式（tx 混进业务签名、mock 被迫携带、事务纪律靠约定）。与本仓问题同构的 sqlc（DBTX = db/tx 共同满足的同三方法面）官方答案是绑定式。唯一大样本孤例 sqlboiler 还带本仓没有的全局执行器回退。
2. **仓内事实**：domain 非测试代码 246 处签名携带执行器词汇；31 个 application/interfaces 非测试文件 import `database/sql`；Anchor（13 repo）与 writes kernel 的签名说「给我 SQL 执行器」——domain 层内部分层被执行域 threading 倒置；7 个 fake 测试文件每个方法拖死参数。ADR-0075「Repo 接口层使规则零豁免可达」的立法目标下，这些是豁免走私，且 domainpurity 三红线不含 `database/sql`，属立法盲区。
3. **设计空间穷尽**（2×2：签名暴露裸执行器 vs bundle × 绑定在调用点 vs 机制内）：现状占「裸执行器×调用点」格（最差）；sqlc WithTx 占「裸执行器×机制内」但把绑定纪律摊派到每个调用点（忘一行 = 静默逃逸出事务）；ctx 藏事务隐式控制流出仓。剩「bundle×机制内」一格即显式 bundle UoW——调用点零绑定动作、闭包体整体即事务作用域、可忘项为零。
4. **被工作负载消灭的家族**：store 自管事务（Ben Johnson/TDL UpdateFn）——四件一拍真实跨聚合（task 终态拍同事务铸 run 行），非自由组合不可拆；请求级事务——SQLite 单写者 + engine 内部 beat 事务处处嵌套；四件一拍形状原语化——4 个新类型买形状执法，买贵了（用户裁决：心智负担优先）。

用户裁决（2026-10-17）：**采纳显式 bundle UoW，推翻 ADR-0005 执行面条款。**

## 决策

1. **Repo 接口去执行器参数**：domain 各聚合 `Repo` 接口方法签名只说聚合语言（`Create(ctx, p)`），`state.Runner` 从全部 domain 签名消失（`shared/state` 契约定义本身保留——`DB` 消费面仍需执行接缝）。
2. **实现构造绑定**：persistence 实现持 runner 字段，构造 `New(clock, run)`；组合根以 `db.Runner()` 构造独立执行形态（读径直调，CQRS-lite 不变），事务态由 binder 以 `*sql.Tx`（满足 `state.Runner`）重造。重绑定 = 重造实例，接口上**不设** WithTx 方法（domain 接口零执行器词汇，与 WithTx 形态的关键差异）。
3. **事务作用域 bundle（application/transact）**：`Stores` 是本事务可见的目录——全部聚合 repo（tx 绑定）+ tx 作用域 Anchor + `NextSeq`（ADR-0070 铸造随视图走）。`Binder` 是 assembly 注入的绑定闭包；`Tx.Do(ctx, fn func(Stores) error)` 是唯一事务入口（开 tx → 绑定 → fn → commit/rollback 集中一处）。调用点零绑定动作，`s` 不得逃出 `Do` 闭包（出界即 "transaction has already been committed" 响亮错），`Do` 不可嵌套（SQLite 嵌套 BEGIN 天然 fail-closed）。
4. **DB.Tx 回调收缝**：回调签名 `*sql.Tx` → `state.Runner`，application/interfaces 从此不 import `database/sql`；engine/grpc/authn 三个四件一拍组合家族代码形状保留，闭包参数换 `Stores`。
5. **契约收缩**：`state.DB` 面去 `NextSeq`（铸造唯一消费点在 Stores 与 persistence 内部）；`Runner()/Clock()/VacuumInto()` 保留。三家族组合 helper（engine `transitionFact`/grpc `writeFact`/authn 直拼）不归一——形状统一留作独立议题。
6. **静态执法**（本 ADR 可测承诺）：①domain 生产代码零 `state.Runner` 引用（`shared/state` 除外）；②`database/sql` 全仓仅存 `shared/state` 与 `infrastructure/persistence`；③逃逸面：application/interfaces 不再持有可误传的执行器实参（①②已隐含），不为理论风险加第三守卫。

## 后果

- 迁移面 ~150 文件，全机械单一规则：签名去 `run` 实参、闭包参数 `tx *sql.Tx`/`run state.Runner` → `s transact.Stores`、repo 调用取自 `s`。读路径全线减一个实参。
- fake 第二实现甩掉死参数；四件一拍行为零变化（事件流/审计行集合零漂移），staging 验收锚为行为不变。
- proto/生成物/golden/console 零涉——纯 Go 内部结构面。
- ADR-0005 四件一拍不变量本身不动（Stores/Do 是它的结构化承载，不是放松）；ADR-0075 全部条款不动。

## 验收锚

- [ ] domain 生产代码零 `state.Runner` 引用（`internal/domain/shared/state` 除外），守卫落地
- [ ] `database/sql` import 仅存 `internal/domain/shared/state` 与 `internal/infrastructure/persistence`，守卫落地
- [ ] `state.DB.Tx` 回调签名收缝为 `state.Runner`；`state.DB.NextSeq` 契约移除
- [ ] 全部聚合 repo 实现构造绑定化；Stores/Binder/Do 落地，assembly 单点装配
- [ ] `mise run test` 三 module 全绿；`mise run lint` 绿；`mise run generate:verify` 零漂移（wire 再生成）
