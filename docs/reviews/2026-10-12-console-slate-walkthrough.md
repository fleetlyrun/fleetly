# Console Slate 重定稿 + 品牌 logo 换装 staging 全面走查（2026-10-12）

**判定：PASS-with-notes** —— 全路由逐页过（平台域 11 页 + 项目域 12 页 + App Detail 九 tab + 登录页 + 双主题抽查），Slate 视觉与新 F logo 全面上屏；走查咬出 5 findings，其中 3 项当日修复换装收口（W-2/W-4/W-5），2 项挂账（W-1/W-3）。走查零残留（纯只读 + 既有 founder 密码会话，零 token 铸造、零项目/载体创建）。

## 换装链（fa382dd-wfix → 现役 fb75ee2-w5fix，三跳）

| 跳 | 版本戳 | 内容 | 前置 Platform Backup |
|---|---|---|---|
| 1 | `9f27dd3-slate` | Slate 视觉重定稿（token/字号阶梯/密度）+ 品牌 F logo 换装（f6e949f + 9f27dd3） | `3ab58967` |
| 2 | `513e1c9-w2fix` | W-2/W-4 修复（513e1c9） | `92cfb5bc` |
| 3 | `fb75ee2-w5fix` | W-5 修补（fb75ee2，无层级层 CSS） | `cdd8d6b6` |

三跳均：干净 HEAD 构建 + 版本戳 ldflags 单引号内联 + 嵌入指纹对账（`index-Dt57Vf4x.js` → `index-DWhz4SwQ.js` → `index-CSShMEFx.js`）+ drop-in 六件全活 + doctor 10 ok / 2 warning / 0 failed（既有基线）+ goose 停 28 零迁移 + **零扰动实锤**（三跳基线 diff 仅 sec-test 既有节拍与相对时间字符串老化，task ID 全同——console-only 批零滚动推论再度成立）+ 路由冷窗后 tw.dev/n0.dev 200 + journal 零 error。

## 覆盖面与活体锚

- **登录/品牌**：新 F 叶形 logo 登录页 + 侧栏在役（暗底无白晕）；`/logo.png` `/favicon.png` 200。
- **App Detail 九 tab 全通**：Overview（大数字统计卡/g6←g4 代链/Processes Running 列）；Processes（stepper/Apply changes）；Deployments（Revision 列 R1→R2 seq 形态，W-2 记录修复在役）；Metrics（双图例 + per-instance 尾值）；**Logs 真流 646 帧**（双实例交错/Stop 红钮）；**Terminal 全链**（session 锚实例 + `echo slate-ok` 回显 + 真实容器主机名）；Variables（分组/暂存）；Routes（诚实空态）；Settings（General/hook 表单/Danger zone）。
- **项目域**：总览磁贴/Apps 列表（大写微标签表头 + CLI 行）/Deployments（项目级 R…hash 诚实退化）/Tasks/Registry（PageTabs + never deployed 诚实行）/Storage/Variables/Routes/Networks + torchwood Databases（pgvector running · 备份 18h ok · endpoint）。
- **平台域**：Overview（Nodes 1/4 历史观测口径既有）/Nodes（**relay 版本回显 `9f27dd3-slate` 新二进制活体** + 现役置顶历史置灰）/Components（四组件 healthy + VM ingest 1s ago）/Events/Alerts/Backups/Access/Audit（**exec.session 审计活体**——终端走查同窗落表）/Settings（password session · admin）。
- **双主题**：暗色全量；亮色抽 Overview/Databases 无白字白底。
- **门禁**：vitest 108 绿（+3 W-4 精度锚）/ console:build 全绿 / console:verify 零漂移（提交后）。

## Findings 与处置

| # | 级 | 症状 | 根因 | 处置 |
|---|---|---|---|---|
| W-1 | P2 | localStorage `fleetly_console_project` 存已删项目 id → 整个 /p/ 域导航 "project not found in catalog"，切换器仍显示旧名（名与 id 失同步）；恢复 = 切换器重选 | 审计证实 `project.delete project/01M4QA9B…`（当日 14:02Z）后本地记忆不失效；切换器名不重校验（记录·三十一观察⑤的姊妹面） | **挂账**（行为修复需裁决：启动时对目录校验记忆 id，失配回退首项目并同步名） |
| W-2 | P2 | Databases 页整壳横向溢出（scrollWidth 1536 > 1440，顶栏右半被裁） | vendored SidebarInset 无 `min-w-0`，flex min-content 下限把 main 钉在内容固有宽（该页工具栏 min-content 最宽才踩中） | **当日修**（513e1c9）：shell-layout 消费侧 `<SidebarInset className="min-w-0">`；活体 overflow=false |
| W-3 | P3 | 懒加载路由 chunk 慢链窗口整壳全黑（Tasks 首访 2s+，自愈） | React.lazy Suspense fallback 空窗（壳与页面同边界） | **挂账**（建议壳级 spinner fallback；慢链才可见，快链不可达） |
| W-4 | P3 | Metrics y 轴刻度在 <1% 量程重复（0.0%/0.1% 打满） | formatMetricValue percent 固定 1 位小数 | **当日修**（513e1c9）：<1% 自适应 2-3 位 + 去尾零；+3 测试锚；活体 0%/0.045%/0.09%/0.14%/0.18% 全唯一 |
| W-5 | P4 | 固定宽 Select 触发器裁字无省略号（"all (processes comb"、"auto (first runnin"） | vendored 值槽 `flex` 工具类压过 line-clamp 的 display；base 层规则再被 utility 层压 | **当日修**（fb75ee2）：无层级层规则（block + ellipsis + min-w-0）压过全部 utility；活体 "all (processes co…" |

**观察（非缺陷）**：n0reg/web 遗留变量 `WALK_NOTE=staged-by-walkthrough`（上轮走查残留，删除会 stage 新部署，留在验证项目可接受）；项目域 Monitor 组 Logs/Metrics 指全局工作台（带项目上下文），`/p/:id/logs` 非路由（深链 404 页诚实承接）；平台 /terminal 与 App Terminal 同组件面未重复走查。

## 教训

1. **CSS 层级执法**：Tailwind v4 中 base 层规则会被 utilities 层同属性覆盖（与特异性无关）——对 vendored 组件行为修补要用**无层级规则**（压过全部 layer）；本案 line-clamp-1 的 display:-webkit-box 被 `flex` 工具类覆盖后才暴露。
2. **flex min-content 陷阱**：`flex-1 w-full` 无 `min-w-0` 时内容固有宽就是收缩下限——宽表页（横向内容最宽者）会把整壳撑溢出；vendored 布局组件的类组合缺口在消费侧用 className 补（不触上游）。
3. **走查即验收的反面**：console:verify/vitest 全绿 ≠ 慢链路/真实数据形态不咬人（W-2/W-3 均为 jsdom/本地盲区，与 W1/W1' 同族——真实浏览器走查不可省）。
