# ADR-0057: Console UI v2——设计系统、信息架构与依赖口径升格

日期：2026-10-09；状态：定稿（批 0-4 + staging 走查落地；批 5 reskin 与守卫扩全量见验收锚未勾项）

## 背景

Console 经 F2.6 只读面→F3.1 写面→C1-C6 对齐批的逐期演进，能力面已与 CLI 对齐（14 路由走查 PASS），但 UI 形态停留在"工程可用"层：14 个平铺导航 tab 无分组、项目选择器在 8+ 页各自为政（切页即丢上下文）、Tasks 页手贴 project ID、图表无坐标轴、错误文案万金油、`window.confirm` 破坏性确认。用户判定为"玩具，完全不能用"，要求全面重构并参考精品 PaaS。原型（docs/design/2026-10-09-console-redesign-prototype.html）经双主题九屏浏览器走查定稿。

本 ADR 显式升格 ADR-0044 的两条口径：**最小依赖纪律**（决策：组件手写、Radix 留给未来）升格为**主流库优先**——不为不引而不引，各领域取事实标准；**手写 hash 路由**（决策 2）由 TanStack Router 取代。ADR-0044 的其余决策（零私有 BFF、embed 静态、openapi 生成链、词汇冻结执法面）全部延续。

## 决策

1. **组件与样式：shadcn/ui 官方体系**（style=radix-nova，Radix 基座 + Lucide 图标 + Tailwind v4 CSS variables）。组件源码进 `console/src/components/ui/`，`components.json` 指向官方 registry；`src/components/ui/**` 按"上游原样"纪律不自改（升级 diff 干净），本仓定制只走 token 层与领域件包装。
2. **双主题**：暗色默认（`html.dark` 首挂防闪），亮色可切，localStorage 键 `fleetly_console_theme`。全部颜色收进 oklch token（`styles.css`），品牌渐变仅品牌时刻（磁贴 avatar/hero）。
3. **路由：TanStack Router**（文件式 + browser history 干净 URL + 类型化 search params）。服务端 SPA fallback 既有测试锚天然兼容；旧 `#/...` 深链由 main.tsx 迁移 shim 重写；`routeTree.gen.ts` 是提交进仓生成物，纳入 console:verify 零漂移断言。
4. **一致性来自原型**：六种页面原型（Dashboard/List/Detail/Settings/Flow/Workbench）钉死解剖结构，页面只填内容；交互契约（四态反馈、键盘、RelativeTime、CopyButton、交叉链接、401 闭环）全站一致。
5. **数据面**：TanStack Query 延续（查询键契约不变）；表格 @tanstack/react-table（9.x legacy 层，v8 形态，迁移隔离在 DataTable 单文件）；表单 react-hook-form + zod；toast sonner；⌘K cmdk；图表 Recharts（经 shadcn Chart 包装，路由级 lazy）；字体 Inter Variable + JetBrains Mono 本地打包（@fontsource，无 CDN）。
6. **一致性由守卫执法**：`console.ui.antipattern.test.ts` 禁 window.confirm/原生 dialog/时间散写/hash 直写/万金油错误文案；批 0 执法新世界目录（components/domain、lib、features、hooks、routes），批 6 扩全量。范围例外带理由注记、双向保鲜。

## 不变量（沿 ADR-0044/架构 §7）

零私有 BFF；NDJSON/SSE 票据/WS 帧流协议只换壳不换协议；deployments 服务端 per-App 轴不动（项目级聚合走客户端 fan-out）；`fleetly_console_token` 键、`<title>fleetly console</title>`、SPA fallback、缓存头语义不变；词汇冻结执法面覆盖全部新 UI 文案；W1/W3 教训（form 只做业务容器）延续并按 Radix 机制重写守卫测试。

## 验收锚

- [x] 依赖钉版进 pnpm-lock：@tanstack/react-router、@tanstack/react-table、@tanstack/react-virtual、react-hook-form、zod、@hookform/resolvers、recharts、sonner、cmdk、lucide-react、radix-ui、cn、date-fns、@fontsource-variable/inter、@fontsource/jetbrains-mono、next-themes、tw-animate-css
- [x] `console/components.json` 存在且 style=radix-nova；`src/components/ui/**` 与 `src/hooks/use-mobile.ts` 由 registry 内容落位
- [x] `src/styles.css` 含双主题 oklch token（:root/.dark + @theme inline 映射）；index.html 含主题防闪内联脚本；`<title>fleetly console</title>` 不变
- [x] 领域件单源：StatusBadge（deployment/node/alert 值域映射）、RelativeTime、CopyButton、EmptyState、DataTable、PageHeader、ProjectAvatar 各一文件，组件测试覆盖
- [x] `console.ui.antipattern.test.ts` 五条规则执法新世界目录，范围例外带理由
- [x] 既有 42 用例不回退；全套 vitest 绿
- [x] routeTree.gen.ts 纳入 console:verify 断言路径（批 1，c3c266b）
- [~] 六原型全页面落地：Dashboard/List/Detail/Flow(DeploySheet)/Workbench(Logs/Metrics) 已落地并过 staging 真机走查（docs/reviews/2026-10-09-console-ui-v2-walkthrough.md PASS）；Identity/Settings/Nodes/Templates/Quickstart/Terminal/Events/Login 仍旧页挂新壳（功能与 mutation 语义原样），reskin 留下一批
- [~] staging 真机走查 PASS 报告已进 docs/reviews（双主题抽查；全路由×双主题矩阵待批 5 落齐后补全）
