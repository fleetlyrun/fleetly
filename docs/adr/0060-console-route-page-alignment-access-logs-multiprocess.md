# ADR-0060: Console 路由-页名对齐、Access 定名与日志多进程面

日期：2026-10-11；状态：已采纳（用户裁决批，2026-10-11 Console UX 批次同批落地）。

## 背景

IA v3 落地后用户走查提出一组路由/导航/信息组织修正（2026-10-11 批次）：

1. Variables 页 route 仍走 `/configuration`（ADR-0059 同批重组时为避免深链破坏保留了旧路径，页名却已改 Variables）——路由与页名背离，日常使用中持续制造认知摩擦；
2. Components 页 route 仍走 `/providers`（ADR-0059 记录"route 保持 /providers 不变"）——同款背离；
3. Admin 组 "Identity" 菜单不达意（页面承载 Users / Teams / Roles / Invitations，即"谁能访问什么"）；
4. 侧栏 Overview 项在项目域任意页面恒高亮（isActive 判式对 `/p/` 前缀全量命中）——选中态失真；
5. 日志工作台 process 过滤是单值自由文本，多进程排查需要反复重启流；
6. App Detail 缺进程管理面（进程列表与实例数调整无处落）；
7. App Variables 单表在多 process 下不可筛，且"新增变量挂哪个 process"不可见。

## 决策

1. **路由对齐页名**：`/configuration` → `/variables`，`/providers` → `/components`。这推翻 ADR-0059 决策 1 的"route 保持"子项（该子项的动机是避免深链破坏；实践证明 route-页名背离的持续摩擦大于一次性深链成本）。旧路径不做兼容重定向（Console 是本仓自持 SPA，无外部深链存档面；URL 不属词汇裁决面，属 IA 裁决面）。
2. **Admin 组 Identity 更名 Access**（route 保持 `/identity`：route 面已按决策 1 对齐两处高频背离页，Identity 是低频管理页，label 对齐即可，避免第三次 URL 迁移）。member/account 是 CONTEXT.md 禁词，Access 与 User/Team/Role/Invitation 词条族全部兼容。
3. **Overview 选中态判式回归纯前缀匹配**：撤销 `/p/` 前缀全量命中特判。项目域页面由各自导航项高亮；不落侧栏项的路由（如 App 详情、Project 总览）无侧栏高亮是诚实态。
4. **日志多进程面**（proto 是唯一契约源）：
   - `StreamLogsRequest` 增 `repeated string processes = 10`（与单值 `process` 并集去重，投影公式 `engine.WorkloadID` 收敛为 Workload ID 集；database/run 轴精确拒收——载体单 Workload）；
   - `capability.LogQuery.WorkloadID string` 升格为 `WorkloadIDs []string`（空 = 域内全部）；swarm/k3s/VictoriaLogs 三 provider 集合成员判定（k3s In 选择器、VL LogsQL OR 并组）；
   - grpc-gateway 传形 = 重复键（`processes=a&processes=b`）；**逗号形不分裂**（REST 多进程测试钉此实测语义）；
   - Console 日志工作台 process 过滤升级为多选下拉（CheckboxItem 形，空选 = 全部）；URL search param 仍为 `process=` 逗号形（旧单值深链兼容）；
   - CLI `logs --process` 单值面零改动（字段保留）。
5. **App Detail 增 Processes 页**（route `/p/:projectId/apps/:appId/processes`，tab 紧随 Overview）：冻结 Spec 进程列表（Replicas/Strategy/Ports/Routes 归因）+ 实例数暂存编辑。提交面沿用 Variables tab 的暂存-部署模式（冻结 Spec 全量 + 改动组装 spec_file 走 Deploy，零 proto）。replicas=0 挂起合法（spec 校验下限"must not be negative"）。
6. **App Variables 组织复裁**：工具栏 process 筛选下拉；创建（Add variable / Add secret ref）在对话框内显式绑定 process（默认 = 当前筛选 > 第一个 process）。不做"应用到所有 process"的隐性扇出——env 本就挂 process，同名变量后续单改会分叉，隐性扇出语义不诚实。RefDialog 原实现无触发点（死 UI），同批修活并纳入绑定模式。
7. **项目 Variables 页写面按钮统一 Add 命名**（Add secret / Add config / Add variable；API 动词仍是 PUT——组件名 PutForm 承动词面，用户可见文案承 Add 面）。ADR-0007 冻结的是 CONTEXT 词条族，PUT 动词不在其列。
8. **Metrics 工作台 Metric 下拉升格为 PageTabs 四 tab**（CPU % / Memory / CPU cores / Custom PromQL），tab 即 `preset` search param，URL 契约不变；自定义 PromQL 输入移入 Custom tab。
9. **Workbench 高度链修复**：Logs/Metrics 工作台根高 `calc(100svh-3.25rem)`（顶栏 h-13）——追加日志帧只滚日志框，不再撑高页面。

## 不变量

- CONTEXT.md 词汇零改动（本 ADR 只动 IA 面：route、菜单 label、页面组织）。
- proto 既有字段零破坏（`process` 保留为单值兼容面；CLI --json golden 双形态照常）。
- 暂存-部署模式（第四源 spec_file）零 proto 的机制在 Variables/Processes 两页一致。
- Console 反模式守卫（模板化三禁单、禁词表）继续执法；新增 "Apply changes" 命中以带理由白名单条目双向保鲜。

## 验收锚

- [x] 侧栏 Variables 项指向 `/p/:id/variables`，Components 项指向 `/components`（nav.ts 单源）。
- [x] Admin 组显示 Access；页头/顶栏 crumb 同步（route 仍 /identity）。
- [x] Overview 仅在 `/overview` 高亮；项目域无伪高亮。
- [x] `StreamLogsRequest.processes` 契约落地：capability/providers 三实现 + 服务层并集去重 + 空条目精确拒绝；REST 多进程测试钉重复键形与空流语义。
- [x] Console 日志工作台 process 多选；App Logs tab 单选收窄为 `processes: [x]`；URL `process=` 逗号形深链兼容。
- [x] App Detail Processes tab 落地：列表 + 实例数暂存 + Apply changes（守卫豁免条目带理由）。
- [x] App Variables：筛选 + 创建绑定 + Add secret ref 修活。
- [x] Variables 页三处按钮 Add 命名。
- [x] Metrics 四 tab 化（URL `preset` 契约不变）。
- [x] Logs/Metrics 工作台视口定高，追加帧只滚日志框。
