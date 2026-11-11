# ADR-0100: 核心资源图完整性——外键在 SQL，状态枚举留在 domain

日期：2026-11-11 ｜ 状态：Accepted

## 背景

迁移 00047 为核心资源图补了数据库级外键（26 表重建、31 条 `ON DELETE
RESTRICT`）。同批需要裁决一个边界：`state/status` 等枚举值域是否也要以 SQL
`CHECK` 约束下沉到表定义。

现状是：枚举的合法值、迁移规则与终态语义由 domain 状态机拥有（`state.*`、
各聚合的 `State` 类型）；SQLite 只存文本。若在迁移里手写 `CHECK (state IN
(...))`，同一值域会出现两份真源——domain 增删状态时 SQL 不会同步，迁移与
领域状态机将静默分叉。

## 决策

1. **外键承载引用完整性**：核心资源图的父子引用由数据库外键执法，全部
   `ON DELETE RESTRICT`；不级联。软删（`deleted_at` tombstone）仍是应用层
   语义，外键只阻止硬删仍被引用的父行。
2. **状态枚举不下沉 SQL**：不在迁移中为 `state/status` 手写 `CHECK`。枚举
   值域、迁移规则与终态语义保持 domain 单源；SQL 只做存储与引用约束。
3. **迁移 fail-closed**：00047 末尾执行 `pragma_foreign_key_check`；存在孤儿
   行即回滚，不静默丢行、不伪造父行。夹具必须先建父行。
4. **schema 不变量测试**：`internal/infrastructure/persistence` 用
   `pragma_foreign_key_list` 逐表断言外键集合与 `ON DELETE` 行为，防止迁移
   漂移；这是标准 `go test`，不是 AST 守卫。
5. **未来若确需 SQL CHECK**：先建立枚举的单一生成/导出机制（例如从 domain
   注册表生成迁移片段），再由生成物落 SQL；禁止手写第二份值域。

## 后果

- 硬删被引用父行会大声失败；应用层软删路径不受影响。
- 状态值域只在 domain 变更，迁移不需要跟随枚举演进。
- 孤儿行在迁移期即暴露，不会进入运行期。
- SQL 层无法独立拒绝非法状态文本；该职责明确归 domain 受理/迁移校验，API
  与引擎的既有状态机测试继续承载行为契约。

## 验收锚

- [x] `TestMigrateForwardCoreGraphForeignKeys`：v46→v47 父/子行存活、孤儿插入拒绝、`foreign_key_check` 干净。
- [x] `TestMigrateForwardCoreGraphForeignKeysRejectsOrphans`：孤儿行使迁移失败。
- [x] `TestForeignKeysAreDeclared`：全库 `pragma_foreign_key_list` 期望集与 `ON DELETE RESTRICT`。
- [x] 迁移与 schema 测试同批落地（commit `3584323b`）。
