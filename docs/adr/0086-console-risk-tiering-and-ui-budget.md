# ADR-0086：console 风险分级确认与 UI 体积预算

日期：2026-10-30 ｜ 状态：已实施（zeabur-oss 借鉴程序第 1 批 B4a/B4b，裁决记录见 `docs/proposals/2026-10-30-zeabur-oss-borrowing.md` §0/§8，设计规格 `docs/design/2026-10-30-zeabur-oss-borrowing.md` §4；同批词汇面见 ADR-0085）

## 背景

console 的破坏性操作确认此前只有 `confirm-dialog.tsx` 的 `destructive` 单布尔（红/默认钮色一档），Project 删除与受管组件 Restart 和删一个 Variable 同重；前端无任何体积预算——`manualChunks` 分包策略退化（重依赖被打进 entry）不会触线，首屏膨胀不可见。zeabur-oss 参照（引述语境）：`web/src/model/risk.ts` 风险查表驱动确认对话框形态分档；`scripts/ui-budget.sh` 以 brotli 口径量首屏、靠纪律声明「涨须理由」。fleetly 两件均自设加码：风险档位编译期执法 + 预算断言进 `console:verify` 门禁。

## 决策

1. **风险三档（B4a）**：`console/src/model/risk.ts` 纯函数分级——`RiskScope（resource|project|platform）× RiskBlast（single|cascade|platform）` → `standard | wide | type_to_confirm`。分档语义：`standard` = 单资源破坏动作；`wide` = 级联影响面（宽框 + "Consequences" 后果先行段，消费点必传 consequences 文案）；`type_to_confirm` = Project 删除与平台级动作（对话框内输入 `confirmName` 指定资源名/等价确认词，不匹配确认钮恒禁用，关窗复位）。`ConfirmRisk` 判别联合使「wide 必带 consequences、type_to_confirm 必带 confirmName」在编译期执法。
2. **confirm-dialog 契约升格**：`risk` prop 必填；`destructive` 布尔保留（钮色语义不混档位）。消费点 26 处已全量赋档（清点含两处任务清单外发现：databases 行下拉删库、task 详情页删 task）。
3. **附带修复（单源化）**：DeleteDatabase 两确认面 + 段落文案自相矛盾（「卷保留」vs "Data is destroyed"）——按真实语义（ADR-0029 决策 8：数据卷与凭证 Secret 为 Project 级材料不随删、同名重建复用卷、凭证重铸）改为单源 `DELETE_DATABASE_CONSEQUENCES` 自洽文案。
4. **反模式守卫扩条**：规则「confirm-dialog 调用点必须带 `risk={{ … tier: … }}`」（窗口扫描至 `onConfirm=` 哨兵，白名单双向保鲜、当前零条目）；**裸 AlertDialog 禁令暂缓立法**——现存 4 处带理由（lift freeze 非破坏；Redeploy/Rollback 为滚动动作且 Rollback 确认闭环归 B1 票据面〔提案 §8.2' 已裁〕；app-processes Restart 按 B1 裁决轻且可逆豁免），归零之日即立零容忍条。
5. **UI 体积预算（B4b）**：`console/scripts/check-ui-budget.mjs` 零外部依赖（node:zlib brotliCompressSync q11，浏览器下载口径），对 `internal/interfaces/console/dist` 产物执法三层——entry 层（入口 JS/CSS，自 `index.html` 解析）、懒加载 chunk 层（按 `manualChunks` 名字模式逐项，零命中即红——双向保鲜）、兜底（未覆盖资产 brotli > 50KB 即红，防新 chunk 溜进）。基线 `console/budget.json` = 实测 + 5% 向上取整（2026-10-30 稳定构建：entry JS 12.8KB / CSS 16.4KB / vendor 216.6KB / charts 68KB / xterm 60.1KB…全表见文件）。
6. **门禁接线与纪律**：`console:verify` 序列 = install → gen → build → **budget** → test → `git diff --exit-code`；**预算上涨必须同 commit 修改 budget.json 且 commit message 带理由**（纪律记于脚本头注释与本文；强于 zeabur 参照物的注释纪律自设加码）。环境变量 `FLEETLY_CONSOLE_DIST`/`FLEETLY_CONSOLE_BUDGET` 可覆盖路径（自检用）。

## 后果

- 破坏性确认的风险-形态对应成为编译期与守卫双执法契约；新增确认面必须显式赋档，漏赋即 tsc 红/守卫红。
- console 体积从此有逐项红线：分包退化、重依赖进首屏、新 chunk 无预算三种形态都会在 `console:verify` 红。
- B1 落地时（第 2 批）`wide` 档的 consequences 段将由 Teardown 预览清单升级填充；Rollback 按钮届时接入 Confirm Ticket，裸 AlertDialog 清单相应收缩。
- zeabur 引述词（plan/apply/gap/incident）进 console 文案时按 ADR-0085 豁免政策走 per-site 白名单。

## 验收锚

- [x] risk 模块 vitest 分组单测 15 例（四组 + confirmNameMatches）。
- [x] 反模式守卫扩条（risk 必填窗扫描 + 白名单双向保鲜零条目）——`console.ui.antipattern.test.ts` 全绿。
- [x] DeleteDatabase 文案单源自洽（两确认面 + 段落共用 `DELETE_DATABASE_CONSEQUENCES`）。
- [x] `check-ui-budget.mjs` 三层执法 + 红绿自检记录（条目改小红、dist 缺失友好报错、兜底腿生效）。
- [x] `console:verify` 全量绿（install/gen/build/budget/test：57 文件 373 测试；`git diff --exit-code` 的 dist 漂移随本批 commit 收口）。
- [x] 基线冻结入 `console/budget.json`（稳定可复现构建三次字节级复现后取值）。
