# ADR-0028: Team 轴 F1.8 前接实与项目名唯一性口径

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | R-7 裁决（批 0.5）、深审 P1-10/Q-16、ADR-0013（网络成员模型）、架构 §2 依赖规则 |

## 背景

`projects.team_id` 有槽位但默认 `'default'`（migrations/00001）；引擎侧 `appTeam` 已立为团队解析单一真源（N0.1 P2-11），但 `managed.go` 的 `resolveBackend` / `activeProjectNetworks` 两处仍硬编码 `"default"`——局部应用了规则、漏了同族调用点。`idx_projects_name` 全局唯一不含 team_id——Team 1─* Project 的领域关系在 schema 层未闭合，多团队时项目名跨团队互斥。连带 Q-16：CreateToken / CreateUser 不校验 role 与 team 归属一致。

## 决策

1. **F1.8 前接实**：随跨 Project 互通批（要判 Team 边界）把全部域解析（`appTeam` 同族三处）从 Project 行实取 team；装配/引擎零 `"default"` 字面量。
2. **索引改口径**：`idx_projects_name` 改 `(team_id, name)`——加法迁移建新索引、后删旧索引（注意存量唯一索引迁移顺序：先建后删，中间窗口靠事务保证）。同名项目在不同 Team 可并存；跨 Team 撞名被拒。
3. **Q-16 同批收口**：CreateToken / CreateUser 校验 role 与 team 归属一致，不一致 409。
4. **user repo FK 归一**（既有挂账）：DeleteUser 的 FK RESTRICT 归一为 ErrConflict 映射（对齐 role repo 先例，消除 E_INTERNAL 误导面）。

## 后果

- 载体域（swarm 命名公式）从 default 切到真实 team——存量集群受管组件的 NamespaceRef 随首启 reconcile 自愈（Ensure 幂等重放），无需人工迁移。
- v1 执法面维持"读默认开放、写显式授权"；多团队治理（per-Team 配额等）不在本 ADR 范围。

## 验收锚

- [ ] 引擎/装配零 `"default"` 团队字面量（守卫或 grep 锚在册）
- [ ] 同名项目在不同 Team 并存；跨 Team 撞名 409
- [ ] CreateToken / CreateUser 的 role/team 不一致被拒
- [ ] 存量库迁移：新索引生效、旧索引有序退场、迁移可回滚安全
- [ ] user repo FK 违例映射 E_CONFLICT（不再 E_INTERNAL）
