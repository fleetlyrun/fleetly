# fleetly 文档索引

> 更名链：**edgesets** →（2026-09-17）**edgefleet** →（2026-09-18）**fleetly**；守护进程 `fleetlyd`、CLI `fleetly`、label 命名空间 `fleetly.*`、模块 `github.com/fleetlyrun/fleetly`。当前名下的文档与代码已统一替换，历史名仅存于本注记与 git 历史。

## 设计方案（docs/design/）

| 日期 | 标题 | 状态 | 一句话说明 |
|---|---|---|---|
| 2026-09-17 | [平台架构设计](design/2026-09-17-architecture.md) | 已实现（v0.1 切面） | 自研 Go 控制面 + Swarm 底座的轻量 PaaS 总体架构（应用模型 = Compose 规范；含目标用户画像、栈边界与对外口径、2 节点 HA 边界口径、每节点入口与集中证书、执行中继 D19、基础框架 lynx+wire D20、API 面 gRPC+grpc-gateway D21）、关键决策、v0.1~v0.3 路线图 |
| 2026-09-17 | [交付流水线设计（CI/CD）](design/2026-09-17-delivery-pipeline.md) | 已实现（M1） | PR/nightly/release 三轨道、V1-V7 永久回归、引擎门禁、GitHub Actions 落地；M2（真 VPS dogfooding）后置 v0.2 E8 |
| 2026-09-17 | [发布失败与回滚语义](design/2026-09-17-release-semantics.md) | 已实现（v0.1 切面） | pause 冻结 + 快照单层重放、四层窗口、观察窗默认告警、失败场景矩阵与错误码 |
| 2026-09-17 | [stateful 放置（节点约束）](design/2026-09-17-stateful-placement.md) | 已实现（v0.1 单节点切面） | 意图/绑定/执行三层、有卷自动钉住、平台节点 ID 为锚、数据安全前哨、人工 rebind、drain 维护语义 |
| 2026-09-17 | [控制面状态模型](design/2026-09-17-state-model.md) | 已实现（v0.1 切面） | 权威/派生缓存/实时直读三层、最小 label 集、孤儿保护、备份等序与 L1/L2 恢复、一键导出 |
| 2026-09-20 | [评审遗留问题完整解决方案（S13-S20）](design/2026-09-20-remediation-complete.md) | 已实施 | [架构评审](reports/2026-09-19-architecture-review.md) 遗留项的决策完备方案：H9 路由撤销通道（noop@internal 兜底）、H15 双轨验签（openssl 兼容轨）、类 A-D 机制收口（出站出口/契约门禁/超时闭环）、S18-S20 中低严重度分波次方案与机制验收；2026-09-20 全部落地 |

## 调研报告（docs/research/）

| 日期 | 标题 | 状态 | 一句话说明 |
|---|---|---|---|
| 2026-09-17 | [竞品调研：轻量自托管 PaaS 的六个关键问题](research/2026-09-17-competitive-landscape.md) | 已完成 | 零停机/多节点/声明式/MCP/构建/差评六主题；收敛点、死亡区、借鉴与避开清单（含对架构文档的 13 条修订建议） |
| 2026-09-17 | [Swarm 作为多节点底座的可行性评估](research/2026-09-17-swarm-substrate-assessment.md) | 已完成 | 源码级验证发布语义/路由/镜像存储/故障语义；结论：建议采纳（含 7 项不可退让的 Spike 验证门）；修正前报告 Swarm 表述 |
| 2026-09-20 | [对照调研：zane-ops（Python/Django + Temporal + Swarm）](research/2026-09-20-zane-ops-comparison.md) | 已完成 | 同底座不同重量级路线对照：部署编排多处独立收敛（印证）、Celery→Temporal 迁移史、10 容器 ≈7GB 控制面实证；R1-R8 借鉴 / A1-A6 避坑清单，输入 v0.2 规划 |

## 实施规划（docs/plan/）

| 日期 | 标题 | 状态 | 一句话说明 |
|---|---|---|---|
| 2026-09-17 | [实施任务分解（Spike + v0.1）](plan/2026-09-17-task-breakdown.md) | 已完成（26/26 票，2026-09-19） | 35 张垂直切片票据（T0 骨架 5 / T1 Spike 4 / T2 v0.1 26），依赖图 + 验收锚定设计文档 + 105-146 人日估算；v0.2 已细化移至 [v0.2 规划](plan/2026-09-20-v0.2-plan.md) |
| 2026-09-17 | [v0.1 实现切面冻结清单](plan/2026-09-17-v0.1-scope-freeze.md) | 已收口（v0.1 关闭，2026-09-20） | T0.5 产出：三专项 v0.1 切面逐项裁决（FZ-1 连接串后置、FZ-2 退化信封、FZ-3 15 码 HTTP 缺省、FZ-4 cron.timed_out、FZ-5 校验信封）+ T0 完成回填 + 冻结轮确认（FZ-6~12） |
| 2026-09-20 | [v0.2 实施规划](plan/2026-09-20-v0.2-plan.md) | **范围已冻结**（裁决轮完成 2026-09-20） | 波次 W0-W5（设计立项/引擎加固 → dogfooding → 多节点 → S3+Cron → 数据库托管 → MCP/观测/终端）；裁决 V2-1~V2-8 全落（预算顶 600MB、VictoriaLogs 默认捆绑、RustFS opt-in、dogfooding 先行、v0.1.0=W1 后打、E4 平台牵线网络、通知 Webhook 首发、平台域名可选参数、挂账三项归属）；≈64-104 人日 |

## 验收报告（docs/reports/）

| 日期 | 标题 | 状态 | 一句话说明 |
|---|---|---|---|
| 2026-09-19 | [架构评审报告](reports/2026-09-19-architecture-review.md) | 归档 | 全仓对抗式评审：H 级高严重度 2 项 + 类 A-D 系统性发现，S1-S12 即时修复、S13-S20 见[整改方案](../design/2026-09-20-remediation-complete.md) |
| 2026-09-19 | [资源校准报告](reports/2026-09-19-resource-calibration.md) | 归档 | fleetlyd+dockerd idle ≈160.7MB（<200MB 达标）；Traefik/containerd 单列；容量边界实测（≤50 apps / ≤200 域名 / 并发构建 2） |
| 2026-09-19 | [v0.1 端到端验收记录](reports/2026-09-19-v0.1-acceptance.md) | 归档 | 八项能力 + 横切硬指标全绿；旅程 CRITICAL 61s（预算 20min）；v0.1 发布依据 |

状态维护：实现完成后将文档状态改为"已实现"并补 PR 链接；方案废弃时改为"已废弃"并指向替代文档。方案文档只追加关联，不删除。
