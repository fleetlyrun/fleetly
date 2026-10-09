# Console UI 完整浏览器走查报告（2026-10-09）

**环境**：staging 单机 fleetly-dev，现役 09083ea-walk（走查中两次同批上线：761f516-w1p → 09083ea-walk）；ZCode IAB（1280×720）+ SSH 隧道 127.0.0.1:19527；凭据 = founder 密码会话（password session · admin）。

**判定：PASS（三修随批落地）**——14 路由全量走查，写面抽查零事故，C1/C5 关键活体锚全部复验；发现 W1'（P0，当轮修复）+ W2/F1/F2（当轮修复）。

## 覆盖矩阵（14/14 路由）

| 路由 | 判定 | 关键证据 / 发现 |
|---|---|---|
| Login | W1'→✅ | **W1'（P0）：嵌套 form 致密码登录零动作**（详见下节）；修复后密码登录全链绿（截图在案） |
| Deployments | ✅ | torchwood 带数据态：succeeded / gen 13 / revision 引用列全渲染 |
| Apps | ✅ | 项目下拉/项目卡/删除守卫；**hook 模态**首配表单（E_NOT_FOUND 路径）正常 |
| Resources | ✅ | networks+peers 表；**databases tab：备份展开 + verify 活体绿**（ok — digest 2abe86703250dc4b…，与 CLI 锚同值）；F1：Size 裸小数（已修 toFixed(2)） |
| Tasks | ✅ | tasks/schedules 双 tab 骨架；项目输入 paste-id 形态（record·十四 W2 维持记档） |
| Observability | ✅ | **图表活体**：torchwood CPU 多序列 SVG（peak 0.01 cores，一热多温形态真实）；F2：图例超长容器名（已修中段截断）；alerts tab 空 relay 态正常 |
| Identity | ✅ | users（founder 行 + password… 动作）/ roles（builtin scope 全表）/ teams（default 行）/ invitations 空态 |
| Nodes | W2→✅ | **W2：历史行混淆面**（详见下节）；修复后现役行置顶复验绿 |
| Logs | ✅ | 过滤面（project/app/process/tail/text/follow）完整 |
| Events | ✅ | **SSE 活体**：Resume → `● following`，cursor 117967、200 行积压投递 |
| Audit | ✅ | source/action/actor/resource 过滤面 |
| Settings | ✅ | whoami/tokens/冻结/**平台备份台账**（当日 5 快照） |
| Quickstart | ✅ | 一表全链表单（预填复用语义） |
| Templates | ✅ | builtin 目录（grafana/nginx 系）show/instantiate |
| Terminal | ✅（注） | 表单/会话面完整；xterm 落屏受本 IAB rAF 挂起限制不渲染（走查环境 quirk，record·十四 口径；数据面 WS 通道不受影响） |

## 发现项与处置

- **W1'（P0，当轮修复 761f516）**：密码登录零动作——C6 重写 LoginPage 时外层布局容器误用 `<form>`，内层业务表单成嵌套；真实浏览器内层 submit 归属外层无 handler form，默认 GET 提交整页刷新（URL 带 `?`）。jsdom 冒泡行为不同 → 组件测试全绿漏网（W1 同机制换页面复发）。修：外层 div；守卫从 Modal 扩到页面级（登录页单 form 无 form 祖先锚）。
- **W2（Nodes 历史行混淆面，当轮修复 09083ea）**：历史注册行（node2 旧注册、manager 旧平台 ID）与现役行混排——现役 fleetly-dev 沉底第三行，两行同名 hostname，全部带 uncordon/drain 按钮，误操作面真实存在。修：现役（available=true）先行排序 + 历史行置灰 + 措辞 "unschedulable"→"unavailable"（平台无 retired 字段，不臆断死活）+ hostname 悬浮 platform_id 锚。复验：现役行置顶。诚实边界：cordoned 现役行与历史行在 API 形态上不可区分，按钮保留（对历史行操作会得到平台诚实报错）。
- **F1（已修）**：备份 Size 列裸双精度小数（0.052725791931152344 MiB）→ toFixed(2)。
- **F2（已修）**：指标图例渲染 swarm 容器全名（60+ 字符）→ 中段截断保尾段（task id）。
- **观察项（不修，记档）**：Tasks 项目输入 paste-id 形态（既有 W2 记档维持）；Events 初始 paused 态（票据按需消费，设计如此）；Terminal xterm 在本 IAB 不落屏（环境 quirk）。

## 活体锚复验（本轮走查附带）

- 密码登录全链（founder → password session · admin Shell）
- 指标图表多序列活体（torchwood CPU，1h 窗）
- 备份 verify（digest 与 2026-10-09 记录·十九 CLI 锚同值）
- Events SSE（Resume → following + 200 行积压）
- 节点中继版本回显（online · 09083ea-walk——relay 版本随 daemon 换装实时更新）

## 走查环境事实

19527 隧道为易逝品（会话结束即断，重走查先重建）；IAB 对本机回环可达；locator click 偶发超时但动作实际生效（重读状态即可，勿盲目重试）。
