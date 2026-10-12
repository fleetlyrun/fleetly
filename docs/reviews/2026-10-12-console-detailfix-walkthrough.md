# Console 走查报告：详情页一致性四批 + Task 详情可达性修复（2026-10-12）

**换装**：`fb75ee2-w5fix` → `c884d29-consfix`（跳 1：16c9390 窄卡通栏 + f1fd6e7 面包屑槽位 + 6e315e3 宽度统一/Routes 模板 + c884d29 DB/Task 详情对齐）→ **`c14f854-w6fix`（现役）**（跳 2：走查 W-6 当日修）。两跳均 console-only：零迁移（goose 停 28 各窗核验）、Platform Backup 硬门（`1ee5fbdf` / `20909817`）、卷 tar 按记录·三十三 console-only 先例省略、旧二进制留底（fb75ee2-w5fix / c884d29-consfix 双 .bak）、drop-in 六件全活、doctor 10 ok / 2 warning / 0 failed、嵌入指纹两连对账（`index-BzSc7mvP.js` → `index-TWgKtbNY.js`）、路由冷窗后 tw.dev / n0.dev 双 200。

**零扰动**：两跳 task 基线 diff 唯一差异 = sec-test 周期任务历史行自然滚动 + 相对时间字符串老化，task ID 集合在用户域/受管域逐位一致——console-only 推论第四度成立。

## 修复点活体验证（本批四拍全过）

| 批 | 验证点 | 结果 |
|---|---|---|
| 16c9390 | App Settings / Database Settings 的 Danger zone 通栏 | ✅ 与同页卡片左右缘逐像素对齐 |
| f1fd6e7 | App 详情面包屑 ↔ 英雄卡节奏（28px 顶距 / 12px 下距） | ✅ 上悬下贴失衡消失 |
| 6e315e3 | App 九 tab 内容全宽一致（ Processes/Settings 等与 Overview 同宽） | ✅ 量测 hero 与内容卡 left/right 全对齐（284/1237） |
| 6e315e3 | Routes tab 归队 house 列表模板 | ✅ ListToolbar（过滤 + 2 OF 2 + Add route 主动作槽）+ DataTable 可排序表头 + 行内 copy/Delete |
| c884d29 | Database 详情英雄卡（头像/徽标/ULID/Back up now + Browse 收卡）+ 下划线 tab | ✅ 与 Apps 完全同构 |
| c884d29 | Task 详情英雄卡 + 下划线 tab（Overview/Runs/Settings） | ✅（依赖 W-6 修复后可达，见下） |

## 全页面清单（判定 PASS）

- **平台域**：Overview / Nodes（relay 回显 `online · c884d29-consfix`→`c14f854-w6fix` 两跳活体）/ Components（四组件 healthy、VM ingest "1s ago"）/ Events（Resume 后 200 条积压装填、`platform.backup_succeeded` 链与换装备份互证）/ Alerts（诚实空态）/ Backups（torchwood-pg "2h ago · ok"）/ Access / Audit（`project.delete 01M4QA9B…` 在案 = W-1 证据）/ Settings（Identity + API tokens）/ Templates（grafana + nginx）/ Metrics（7 序列、y 轴小数刻度 = W-4 在役）/ Logs 工作台。
- **项目域（torchwood）**：项目 Overview / Apps / Deployments（17 OF 17、R11→R12 seq 形态）/ Databases / Storage（5 OF 5）/ Variables（`· N REPLICAS` 分组头）/ Routes / Networks / Tasks / Registry（列表页模板族全绿）。
- **App Detail 九 tab**：Overview / Processes / Deployments / Metrics / Logs（真流帧渲染 + Instance 选择器）/ Terminal（选择器面）/ Variables / Routes / Settings。
- **Database 详情** 六 tab、**Task 详情** 三 tab（W-6 修复后）。
- **双主题**：暗默认 + 亮色切换抽查（Templates 页对照）全绿。

## Findings

- **W-6（P1，既有非本批，当日修 c14f854）**：Task 详情路由不可达——列表 `p.$projectId.tasks.tsx` 无 index 子路由，`tasks.$taskId` 点嵌套其下而列表布局（ModulePage）不渲染 Outlet：URL 切到详情、组件永不挂载（刷新亦然），h1 恒 "Tasks"。自 f6069a8（2026-10-11 对齐批 5）即存在，现役 fb75ee2 同病；c884d29 重 Skin 的详情页因不可达而未被此前走查覆盖。修 = 列表改名 `tasks.index.tsx` 对齐 databases 先例（详情成无布局兄弟路由）。修复后活体：详情页英雄卡 + 三卡全量渲染（torchwood roles-sig one-shot completed 任务）。
- **W-7（P2，挂账）**：⌘K 命令面板两症状——① 面板挂载但不可见（`Command Palette` 文本在 DOM、截图无对话框；Search 按钮与 Ctrl/Cmd-K 双路径一致）；② Events 页点 Search 触发整页错误边界 `Cannot read properties of undefined (reading 'subscribe')`（Logs 页不可复现崩溃形态）。UI v2 时代 ⌘K 曾在役，疑似 Slate 重定稿 vendored Dialog/cmdk 交换后的回归，root-cause 需专属批（jsdom 全绿盲区第 N 度）。
- **W-1（P2，挂账伞，新增症状样本）**：目录加载窗内一次性整页弹回 `/overview`（App 详情 Deployments tab 实录一次、9 秒采样不可稳复现）；记忆已删项目 `01M4QA9B…` 的 audit + events 双实锤在案。修复方向不变：启动时目录校验记忆 id、失配回退。
- **W-3（P3→P2，挂账升级）**：Suspense 全壳黑窗本日复现两次（首次进站 + App 详情切 Metrics tab），窗口秒级（~3-6s）自愈，吞没整壳含侧栏。建议壳级 spinner 维持。
- **一次性观察**：首次进站 React 零挂载（root 空、手动刷新恢复）一次——与 W-3 同族慢链窗口记录在案。

## 走查环境与残留

founder 密码会话沿用（零 token 铸造）；走查零残留（无建删操作，纯只读 + 一次日志跟随启停）。IAB 自动化怪癖增补：Playwright locator click 派发超时但动作偶发迟效（本次两处幽灵跳转）、`cua.scroll` 恒挂起（用 keypress End 替代）、坐标点击须以最新截图定位。
