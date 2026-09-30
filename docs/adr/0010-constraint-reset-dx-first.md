# 约束面重置：仅定位、dogfooding 与 DX 优先三条为硬约束

2026-09-30 用户直裁：fleetly 的硬约束只有三条——①产品定位：轻量 PaaS，人类与 AI Agent 一等公民（Agent 支持走 CLI + 仓内 Skills，不做 MCP）；②torchwood / messageloop 是第一期 dogfooding 项目；③**开发者体验（DX）优先级极高**，是一切功能与设计取舍的第一权重。归档项目的全部其他约定（Dokploy 地板、状态语义细节、webhook 单轨、600MB idle 预算、HA 口径、不抽象三连、Compose 受控子集范围等）自本日起降级为**参考默认值**，可在重设计中逐项推翻。

## Consequences

- 全部"继承自归档"的条款按参考默认对待；竞品深调报告（`docs/research/`）以 DX 为第一透镜逐项重估，重估结论以新 ADR 或设计文档修订记录。
- 推翻参考默认不需要特殊程序，但须留 ADR 记录，保持决策可追溯。
- 三条硬约束本身不再被后续调研或重构推翻，除非用户显式变更。
