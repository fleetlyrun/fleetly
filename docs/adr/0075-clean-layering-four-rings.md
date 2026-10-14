# ADR-0075: Clean 四环分层立法——domain/application/infrastructure/interfaces + Repo 接口层路线与三条显式例外

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-15 | ADR-0007（词汇冻结——Audit 词条入册）、ADR-0024（受理位与读写分离律）、ADR-0035（audit 操作者轴）、ADR-0066（writes kernel 与 A 化路线）、ADR-0001/0004（冻结面不动）、架构收敛程序（docs/plan/2026-10-14-architecture-convergence-plan.md、docs/design/2026-10-14-architecture-convergence-report.html） |

## 背景

第一阶段收官时 internal/ 已长出 30 个顶层包，层级归属只活在架构文档 §2 与守卫注释里（13 个树外包）；结构对人与 Agent 不可自证，且 `XxxDomain`/`XxxKernel` 式命名把方法论焊进类型名。2026-10-14 架构收敛程序四轮审查定稿：既有依赖 DAG 本已符合 Clean 依赖规则（engine 消费 capability 端口而非 providers 实现、SDK 圈禁、叶子纯度全部实证），缺的是把规则写进目录。用户终裁：采用 Clean(DDD) 四环分包 + Repo 必须有 interface 层 + 去方法论命名；开发期无向后兼容负担。

## 决策

1. **四环树**（P1a-1 已落位，47da28b）：`internal/domain`（shared：spec/model/apperr/capability；identity 上下文种子；services：anchor/material/dbtemplate/dbbrowser/writes）、`internal/application`（engine/authn/idem/governance/composeintake/apptemplate/schema）、`internal/infrastructure`（persistence/providers/github/upload/keyring）、`internal/interfaces`（grpc/admin/console）；assembly/config/buildinfo 为组合根，guards/testsupport 为元层。

2. **依赖规则五行**（dep_matrix 守卫执法，现状登记版已落位 0390dd9）：

   ```
   domain      → （零外向；三红线：无 I/O、无 genproto、无 RPC 框架）
   application → domain
   infrastructure → domain（实现接口）
   interfaces  → application + domain（读径；写必经 application——读写分离律）
   assembly    → 全部（组合根）
   ```

   现状登记豁免（P1b Repo 接口抽取后清零）：application→persistence、interfaces→persistence（读径）、anchor/writes→persistence、github/upload 客户端注入（fleetlygrpc 与 application 两处）。

3. **Repo 接口层路线（P1b）**：`domain/<限界上下文>/<聚合>/` 持行类型 + `Repo` 接口（包名限定裸名，如 `project.Repo`）；`infrastructure/persistence/<聚合>/` 持不导出实现 + `New`，wire.Bind 绑定；技术聚合（outbox/audit/idempotency/logcursor）同律。两缝由此坐实（真 SQLite 与未来 fake 互为第二实现）。

4. **命名法则**：导出面零方法论后缀——协调类型禁止 `XxxDomain`/`XxxKernel`/`XxxManager` 形态（engine 的 `Engine` 是冻结词条例外）；engine 九域内核**不导出**（复数普通名词 struct + 同包白盒测试）；消费侧接口不导出（accept interfaces 惯用法）；`XxxService` 仅存在于 proto 生成契约面。方法论文汇只存在于四个目录名。

5. **三条显式例外**（不伪装成合规，守卫与注释锚定）：
   - **贫血聚合 + 状态机集中**：状态迁移不变量集中在 writes kernel 与 engine（单写者收敛是第一设计决策，ADR-0066），不分散进 37 个聚合的行为方法；
   - **admin 离线写绕过 kernel**：控制面已停的离线维护语义；
   - **读写分离律**：interfaces 可直读 domain.Repo（CQRS-lite 读径），一切写必经 application（acceptanceguard 承载）。

6. **词汇面**：Audit 词条入册（操作者轴/只增/与 Event 分界）；Event 词条注记资源轴 team_id（P0-2 起）；exec 会话 ID 随机化待 ADR-0070 适用范围修订（内存活体 ≠ 持久化实体）随 P3-1 落地。

7. **冻结面（重构全程不动）**：loop 唯一环骨架、transition 唯写序列、ensureMemo、词汇冻结（ADR-0007）、capability 核心 6 方法窄面、level-triggered 收敛语义。

## 理由

四环目录让依赖规则对人与 Agent 自证（此前 30 行守卫矩阵不可读、结构不可导航）；Repo 接口层消灭 application/interfaces→persistence 两条违规边使规则零豁免可达；激进形态经 design-it-twice 否决两案（facts 单表合流三硬伤、Engine 服务组装必搬冻结面）——本 ADR 只落有证据的形态。

## 后果

- P1b 完成前存在登记豁免（带收紧锚）；persistence 目录与 state 包名的暂不一致随 statekit 批定稿。
- 既有守卫路径面全部随批修缮（P1a-1 已完成 22 文件）；文档路径以本 ADR 与架构 §2 现树为准。
- 新增 internal 顶层包必须先入本 ADR 的树（守卫枚举目录执法）。

## 验收锚

- [x] 四环树落位，golden 全集零漂移（P1a-1，47da28b）
- [x] domain 三红线 / capability 纯度 / dep_matrix 现状登记版守卫落地且红灯实验自证（P1a-3，a59715f、0390dd9）
- [x] apperr 去 genproto、material 拆 keyring（a59715f）
- [x] 本 ADR + 架构 §2 重写 + CONTEXT.md Audit 词条 + AGENTS.md 四环化同批
- [ ] P1b：Repo 接口全量抽取，dep_matrix 豁免清零，application/interfaces→persistence 边收紧
- [ ] state 包名/目录一致性与 statekit 共同定稿（P5）
- [ ] e2e Go helper 进守卫扫面（P1a-4，随本批）
