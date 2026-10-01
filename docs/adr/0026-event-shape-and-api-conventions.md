# ADR-0026: 事件负载形状钉扎与 API 面惯例（不版本化、after_seq 游标、SSE 短时票据）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | R-2/P1-4/P1-1 裁决（批 0.5）、ADR-0006（proto 唯一契约源）、深审报告 §2.P1-1/P1-3/P1-4、§5.R-2 |

## 背景

Event payload 订户四类（Console / Agent / Skills / torchwood），一次字段改名就是静默断炊；版本号字段解决不了改名（只增原则下改名本就禁止），真正缺的是形状钉扎。浏览器 EventSource 不能设自定义头而拦截器只认 `Authorization: Bearer`（F1.2 SSE 一挂即 401）。全 proto 零分页惯例，F1.5 ListRuns（torchwood 高频创建）全量返回会爆炸；中途统一 = 全 API 面 golden 翻新 + torchwood 客户端改两次。

## 决策

1. **Event payload 不引入版本号（R-2）**：payload 字段只增（改名由只增原则禁止，N-1 CLI 兼容覆盖跨版本）。**形状钉扎**承担防断炊职责：F1.4 `fleetly schema` 反射暴露 payload JSON Schema + golden 钉形状——payload 形状漂移在 CI 层红。
2. **List 分页惯例（P1-4）**：把 ListEvents 的 `after_seq + limit` 形态定为全 API 面惯例——游标 = 各资源自家单调轴（Event=seq、Run=创建序、Build=build 序），命名统一 `after_*` + `limit`。本 ADR 后新 List 面一律从之；存量面（ListRoutes 等）随触碰批次迁移，不专项翻新。
3. **SSE 凭证 = 短时票据端点（P1-1）**：先用 Bearer 向票据端点换一次性票据（秒级 TTL、单用途、限订阅路径），SSE 以 query 参数携带。泄漏面受控：票据进日志的暴露窗口=TTL 秒级、不可重放、不可作他用。 lynx 长流超时豁免（grpcTimeout 实为优雅关停超时，注释已修正）随 F1.2 真机验证流式豁免机制。

## 后果

- outbox 断档三件套（trim / earliest-seq / 410 / 快照重同步，深审 P1-3）随 F1.2 落地，消费契约以本 ADR 的 Schema 钉扎为准。
- 票据端点 proto 定义随 F1.2；错误码（票据过期/重放）入册。
- quickstart 私有轮询收编进 `--wait`（F-15）与本 ADR 无耦合，仍按 F1.3 执行。

## 验收锚（F1.2 订阅面 2026-10-01 落地：d979463 + 阶段 2；F1.4 项随其批次）

- [ ] payload JSON Schema 经 `fleetly schema` 暴露且 golden 钉死（F1.4）
- [ ] payload 字段改名/删除在 CI 红（schema golden 漂移门）（F1.4）
- [ ] 新增 List RPC 全部带 `after_*` 游标（评审清单项；events 面已从之）
- [x] 票据：秒级 TTL、单用途、限订阅路径（过期/重放拒绝；拒绝面=原生 401 minimal body，EventStreamSource 专用）
- [x] EventSource 无自定义头完成订阅并收到首事件（httptest 级钉死 EventSource 契约形态——纯 GET+query 凭证；浏览器真机随 Console e2e 补）
- [ ] lynx 流式长流不被优雅关停超时误杀（真机验证记录；staging 项）
