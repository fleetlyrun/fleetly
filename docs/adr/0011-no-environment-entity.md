# 无 Environment 实体：环境即项目，promote 后置为跨项目原语

2026-09-30 用户终裁（用户定位=小微团队）：不设 Project 内 Environment 层。理由：实体成本由全部用户支付（模型/CLI/Console/权限/测试矩阵全链路多一维），收益只归于实际跑 staging 的少数小微团队；竞品口碑痛点清单中无环境相关项；torchwood dogfooding 亦无此需求。环境即项目：`shop` / `shop-staging` 命名约定 + per-Project 网络隔离（staging 结构上摸不到 prod）。变量两级：SharedVariable（Project）+ Variable（App），Project 层在下、App 层覆盖。

promote 晋升保留为后置的差异化押注，且**不依赖本实体**：实现为跨项目按 Revision/digest 部署 + 变量映射（`fleetly promote <src> <dst>`），血缘进审计。预览环境是 Environment 唯一可能挣得席位的场景，出现真实需求前用"临时 Project + 标签 + janitor"形态（结构上全新变量集，无继承泄密面——dokploy 预览泄密的反面）。

## Consequences

- 对冲：Spec/Revision 从第一天起目标无关——变量按名引用、部署时对目标 Project 的绑定解析；将来引入 Environment（若有）是有界迁移而非重写。
- 重开触发条件（其一即重开 ADR）：① dogfooding 或早期用户反复提出"共享定义的多环境隔离"需求；② 预览环境立项。
- 竞品深调报告中"保留并升格"的反转建议随本裁决作废，证据保留作重开时参考。
