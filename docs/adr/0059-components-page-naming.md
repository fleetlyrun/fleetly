# ADR-0059: 平台组件页名词复裁——Components 胜出（修订 ADR-0058）

日期：2026-10-10；状态：已采纳（用户裁决，原型保真批同批落地）。

## 背景

ADR-0058 将受管组件排障页定名 Managed Providers（当时初稿 "Components" 因撞 CONTEXT.md Managed Provider 词条 _Avoid_ 表的 component 被废）。IA v3 原型（docs/design/2026-10-09-console-ia-v3-prototype.html）核对与日常使用中，"Managed Providers" 过长且与侧栏其余项（Nodes/Events/Alerts/Backups——单词短名）节奏不合；用户 2026-10-10 复裁：**页名词回归 Components**（原型的命名直觉胜出）。本 ADR 即 ADR-0007 纪律要求的显式裁决通道。

## 决策

1. **页名采用 Components**（单数语义锚仍是 Managed Provider 词条：页面是"由平台托管的 Provider 实例"的集合视图）。侧栏项、页头、路由深链文案全部跟随；route 路径保持 `/providers` 不变（避免深链/文档大面积破坏，URL 不属词汇裁决面）。
2. **CONTEXT.md Managed Provider 词条 _Avoid_ 表修订**：`component` 移出 Avoid 表（本裁决后 Components 是在册页面名词，不再是禁用同义词）；Avoid 表保留 `addon, internal service`。这是 Avoid 表的显式修订（ADR-0007 通道），非局部混用。
3. **API 结构面不动**：GetStatus 响应字段 `components` / `v1ComponentHealth` 是协议契约（结构面非 UI 词汇面），保持原名；console 类型引用随之保留。
4. **历史文档不追改**：ADR-0058、走查报告、runbook 记录中的 "Managed Providers" 是当时事实的忠实记录，保留原文。

## 不变量

- Managed Provider 词条本体定义不动（"由平台以普通 Workload 形式托管部署的 Provider 实例"）。
- 页面的排障职能、四卡实名露出（Traefik/zot/VictoriaLogs/VictoriaMetrics）、通知渠道与平台备份归 Settings 的边界全部维持 ADR-0058 原文。

## 验收锚

- [x] 侧栏 Fleet 组项、页头、文档文案显示 Components（route 仍 /providers）。
- [x] CONTEXT.md Managed Provider 词条 Avoid 表移除 component（ADR 本节为锚）。
- [x] API 字段 components/v1ComponentHealth 零改动（协议面）。
