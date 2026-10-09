# Console IA v3 原型走查记录

日期：2026-10-09；走查对象：`docs/design/2026-10-09-console-ia-v3-prototype.html`（信息架构 v3 原型，真源 [2026-10-09-console-ia-v3.md](../design/2026-10-09-console-ia-v3.md)）。

## 走查方式

真浏览器（1600×1000 视口）逐屏截图目检：暗色全量五屏 + App 详情 8 tab 中 7 个实 tab（Deployments 子视图交互另行点击验证）+ Database 详情 6 tab 中 3 个 + 亮色抽查三屏；侧栏溢出用 DOM 度量复核；DB 详情图表用 DOM 断言（SVG 生成 + 图例文本）。

## 结果：PASS（含两项已处置记录）

| 屏 | 结论 |
|---|---|
| Apps 列表（侧栏分组载体） | PASS——八组两级导航、+ New 菜单、行内动作与 CLI 对照行正常 |
| App 详情 Overview | PASS——8 tab 全在位；firing 徽标、Routes 卡（含 "managed Traefik" 注）、Recent deployments 正常 |
| App 详情 Deployments | PASS——Deployments/Builds/Revisions 子视图切换正常（Builds/Revisions 收编落位） |
| App 详情 Metrics / Logs / Terminal | PASS——双序列图表含坐标轴图例；日志 warn/error 着色；终端 process 选择器与票据 TTL 徽标正常 |
| App 详情 Configuration | PASS——Build/Processes/Environment 三卡 + "Apply changes = 新部署"提示；小瑕疵：Environment 值列 `database:orders-db` 换行，可接受 |
| App 详情 Routes | PASS——路由表 + Add route 表单（protocol/TLS seg）正常 |
| App 详情 Settings | PASS——General/Git hook（Rotate token）/红边 Danger zone 正常，双主题均验 |
| Databases 列表 | PASS——备份健康度列（ok/behind schedule/failed+错误行）、行内 Back up/Browse 动作正常 |
| Database 详情 Overview | PASS——凭证卡（Reveal once + 置灰 Rotate password 及级联 tooltip）、Used by 反查、Placement 正常 |
| Database 详情 Backups | PASS——调度/保留/后端三卡；Verify/Restore/置灰下载；failed 行带错误行 |
| Database 详情 Metrics/Logs | PASS——载体图表 SVG 与图例生成、日志面板 6 行（DOM 断言） |
| Components | PASS——四实名卡片（Traefik/zot/VictoriaLogs/VictoriaMetrics）、VL degraded 卡警示边框、ingest 时效/磁盘水位/retention 行、置灰 Restart、顶部警示横幅；双主题均验 |

## 处置记录

1. **侧栏矮视口溢出**：20 项 + 8 组标签在 1000px 视口下初始溢出 182px（Backups/Organization 完全不可见）。已收紧密度（组间距 13→9px、项高 7→5.5px），溢出降至 88px，主项全部露出，Audit/Settings 仍需滚动——与 fly.io 同为滚动侧栏形态，实施时建议加滚动位置记忆；不再追加压密度（继续压会牺牲可点面积）。
2. **组件实名露出**已按拍板落地在 Managed Providers 卡片（"managed proxy" 未出现在任何新面；页名词依 ADR-0058 采用既有词条 Managed Provider，初稿词 Components 撞 Avoid 表废弃）；Routes 卡注脚 "managed Traefik" 属设计文档 §7 允许的注记位。词汇 ADR（T9）随实施批落地。

## 变更记录（同日二轮修订，已复验）

用户反馈：App 详情原 Configuration tab 里的 Build / Processes & rollout 不该放 Configuration 下；Environment 应升级为独立 tab。已采纳（Railway 的 Variables/Settings 分法）：tab 更名 **Environment** 并收窄为 env vars + secret refs 编辑面；Build 与 Processes & rollout 并入 Settings（四卡 + Danger zone）。原型已改并在浏览器复验（Environment 表格/secret ref 徽标/Add variable/Apply changes 正常；Settings 2×2 布局正常）；设计文档 §0.1/§4.1/§6/§7/§8 已同步，Environment 词条并入 T9 词汇批次。本文其余表格描述的 Configuration tab 以修订后为准。

## 变更记录（同日五轮修订，已复验）

用户两问：Apply changes 是否应移到 Add variable 旁边；tab 名是否应随按钮叫 Variables。均采纳：**tab 终名 Variables**（与 Add variable 按钮一致，且消除 "Environment" 的 dev/staging/prod 歧义读法；与项目级 "Shared Variables" 成族，不新增冻结词条）；**Apply changes 移入工具栏右侧**，并补上脏态契约——无暂存时置灰，Add variable 对话框 Create 后点亮（浏览器复验：disabled before/after 断言 + 双态截图亲验，toast 引导语在位）。契约并入设计文档 §2.6（提交型动作与创建动作同驻工具栏、脏态启用）与 §0.1/§4.1/§5.5/§6/§7/§8 同步更名。

## 变更记录（同日四轮修订，已复验）

用户质疑：New app 对话框是否过于简单、能否承载初始化应用所需信息。评估结论：瘦对话框方向正确（create-then-configure，重配置在 8-tab 详情页），但原实现有两处硬伤——**Git hook 被误列为创建时源**（hook 是建库后 per-app 设置）且**源切换不联动字段**（compose/upload 无法承载）。已修：Source 三模式真联动（Image → 镜像引用+私有仓凭证提示；Compose → YAML 文本域；Upload → dropzone + builder 三选），Git hook 移出创建源（脚注指到 Settings → Git deploy hook），默认值语义脚注化。顺带修复表单控件漏挂 `.field/.control` 样式的问题（此前对话框输入框为浏览器原生样式）。浏览器复验：三模式字段联动截图亲验 + Upload 模式 DOM 断言通过。契约落设计文档 §5.5。

## 变更记录（同日三轮修订，已复验）

用户反馈：App Routes 的 Add route 入口应移到列表工具栏右上（与 Environment 一致），创建表单改模态对话框；其他 CRUD 列表同模式。已采纳并定为**创建交互统一契约**（设计文档 §2.6）：Routes 内联表单卡已删（入口移 toolbar 右上 + 对话框）、Environment Add variable 接对话框（Name/Type/Value）、Apps New app 与 Databases New database（pagehead 右上 + 对话框，Databases 含引擎选择与 restore-from-backup）、侧栏 + New 与 ⌘K 同链路。浏览器复验：四处对话框标题/字段数断言通过，对话框内 seg 切换与 Cancel 关闭实测正常。

## 遗留到实施批

- Deployments 详情页、Registry/Backups/Storage 等未在本原型 mock（设计文档 §5/§4 已定义，实施时按 v2 六原型解剖落）。
- Components 页为二期目标态绘制（真健康/磁盘水位接线后）；一期按文档 §5.2 以 unverified + ingest 时效 + 版本上线。
