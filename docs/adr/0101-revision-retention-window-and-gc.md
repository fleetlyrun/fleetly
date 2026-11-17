# ADR-0101: Revision 保留窗与全域清理——审计链归 Deployment 时间线

日期：2026-11-17 ｜ 状态：Accepted（决策已裁；实施任务见验收锚，随下一代码批落地）

关联：ADR-0023（App 删除语义——本 ADR 修订其保留后果半句）、ADR-0100（外键执法——本 ADR 开一条显式例外）、ADR-0048（双代窗——活跃集判定）、ADR-0064（内容寻址——digest 互不牵连）、docs/design/2026-10-16-functions-api.md（v3 §4.3 同律落地）

## 背景

ADR-0023 后果节裁定「Revision/Deployment 行永不删（审计单位）——App 删除不级联清历史」。两个现实压翻了该保留：

1. **存储形态**：Revision 是 blob 落 SQLite（spec protojson + SourceText 同行，2026-09-30 存储裁决）——「Platform Backup = 单 SQLite 文件 + 密钥即全部」的前提。无限保留最终撑爆单文件备份语义；清理不是可选项，是该存储裁决的必然配套。
2. **行业对照（2026-11-16 竞品调研）**：Cloudflare Workers 交互面仅呈现最近 100 个 version；AWS Web Functions 每函数 50 个 revision 自动清理（在流量的不可删）；Cloud Run 每服务 1000 个自动删最旧；Knative 48h/15h/最少 5。「永不删」是行业离群值。

同时 `deployments.to_revision` 外键 `ON DELETE RESTRICT`（ADR-0100 执法）使历史 deployment 行引用的 revision 在数据库层面不可删——App 常态每个成功部署都有 revision+deployment 对，清理若不解开该耦合即空转。

用户裁决（2026-11-17）：全面支持 Revision 清理，默认保留 100，含 App 与其他服务（仓内持 revision 的聚合 = App 与 Function 两处——数据服务轨明确不取 Revision，managed-data 设计「三轨一基」）；条款 =「在流量的不可删 + 上限触发清理」；deployment 耦合采软引用案（deployment 行保留、`to_revision` 降软引用、悬空读诚实降级）；App 删除即时全清 revisions。

## 决策

1. **统一保留律**：每聚合保留最近 **100** 个 revision（App `revisions`、Function `function_revisions` 同律；常量入引擎设置面，v1 不设 per-project 旋钮）。**在流量的不可删**，活跃集定义：
   - App：全部**非终态** deployment 的 `to_revision` 与 `from_revision` ∪ 最近 succeeded deployment 的 `to_revision`（覆盖 ADR-0048 双代窗旧代）；
   - Function：`current_revision_id` 指向行（Phase 2 若加候选/权重指针则一并保护）。
   活跃集本身超 100 时全保留（软上限，结构化日志明示，绝不删在流量行）。
2. **上限触发清理**：新 revision 落账的**同一事务内**删除最旧非活跃行至 ≤100。App 侧在部署受理事务；Function 侧在 Create/Deploy 事务。单事务原子：无后台环、无竞态窗、重启无残留状态。
3. **审计链归 Deployment 时间线**：Deployment 行继续永不删（审计单位 = 时间线）；`deployments.to_revision` 外键取消，降为软引用（`FromRevision` 无外键先例）。读到悬空 id 时诚实降级，用户可见英文文案 `revision cleaned by retention policy`，不静默隐藏。此为 ADR-0100「外键承载引用完整性」的**显式例外**并记录于其例外清单：该边的存在价值是防悬空引用，而保留窗之后悬空是设计内终态。
4. **聚合终局即时全清**：App 删除落账事务内清全部 revisions（翻 ADR-0023「App 删除不级联清历史」的 revision 半句）；deployment 审计行与 ADR-0023 决策 6「部署/审计历史永续」维持不变。Function 删除同律（设计 v3 §4.3）。
5. **清理是引擎自发动作、非用户动词**：**不发事件**（eventcode 零新码，防事件风暴）；结构化日志留痕（聚合、删除行号区间）；audit 面不动。
6. **digest 内容寻址互不牵连**：删除某 revision 行不影响共享同 digest 的其他行（function_revisions 内容寻址去重语义不受清理影响）。
7. **回滚窗 = 保留窗**：回滚/指回窗外 revision 返回 E_NOT_FOUND + suggestion（Cloudflare「只能回滚最近 100 个」同款语义）。
8. **CONTEXT.md Revision 词条微调**（与本 ADR 同 commit）：「回滚与审计的单位」→「回滚的单位；行按保留窗清理（ADR-0101），审计链由 Deployment 时间线承载」。

## 理由

- **审计两问分离**：「发生了什么」（deployment 时间线：何时、从何到何、成败、CommitSHA——永不删）与「当时的确切 Spec」（revision 冻结体——窗内可查、窗外已清）。行业四家同款分离，无一以 revision 行承载审计主链。
- **单文件备份是地基**：2026-09-30 存储裁决把全部状态收进单 SQLite 文件，保留律是该裁决的守门条款而非新债。
- **软引用而非 FK SET NULL**：悬空 id 保留展示价值（部署时间线仍可读「部署到 r-42（已按保留策略清理）」）；SET NULL 会把 id 抹掉，审计可读性反而更差；且 `FromRevision` 已确立无外键形态，不是新先例。

## 后果

- 迁移 00048（占号，实施时复核）：drop `deployments.to_revision` 外键；ADR-0100 schema 不变量测试 `TestForeignKeysAreDeclared` 期望集同批更新（移除该边并在 ADR-0100 例外清单登记本 ADR）。
- App 侧行为变更：既有部署序列开始触发清理——契约测试与 e2e 锚见下；API 面零变化（窗外回滚 = 既有 E_NOT_FOUND 语义 + suggestion）。
- Function `function_revisions` 建表即带保留律（ADR-0102 携带），全仓一套清理逻辑，无第二份拷贝。
- 存储 reclaimed 仅限 SQLite 行（spec blob 与 SourceText）；镜像层存储（zot）另有自己的生命周期，本 ADR 不涉。

## 验收锚

- [ ] 迁移 00048 落地 + `TestForeignKeysAreDeclared` 期望集更新 + ADR-0100 例外清单登记
- [ ] GC 契约测试三件：在流量保护（非终态/双代窗/current 指向不删）；超限恰清最旧非活跃至 100；活跃集超限全保留 + 日志明示
- [ ] 窗外回滚 E_NOT_FOUND + suggestion
- [ ] App 删除级联断言：revisions 全清、deployment 行全留、悬空 `to_revision` 读降级文案
- [ ] 悬空降级断言（部署时间线读径）
- [ ] e2e：连续 100+ 部署序列后 ListRevisions 恰 100 且在流量者在列
- [ ] 零事件面新增（eventcode 注册表零新码）；结构化日志字段评审
