# App Detail 完整走查（13e0aca-appdetail，2026-10-12）

换装 8150725-verflag → **13e0aca-appdetail（现役）** 后的 App Detail 九 tab 全走查 + 全操作实操。走查物 = scratch 项目 walk-ad + App walkapp（compose 双进程 web/worker，nginx:1.27-alpine），毕级联清理零残留。判定：**PASS-with-notes**（三 findings，无阻断）。

## 换装实录

- console-only 批（区间仅 docs + 4ab9bbe/13e0aca 两 console commit）：零迁移（goose 停 28）、卷 tar 按记录·三十三先例省略，Platform Backup `17b64d53` 硬门照付。
- 干净 HEAD 构建（`git status` 复核在案），ldflags 版本戳 `13e0aca-appdetail`，嵌入指纹 `index-DVvpuQsH.js` 二进制↔dist 对账一致。
- **零扰动实锤**：task 基线 diff 唯一差异 = sec-test（既有 ~5min 节拍测试任务，时间戳自然推移）；SERVICES-IDENTICAL；doctor 10 ok / 2 warning / 0 failed；三路由（tw/n0/console）200；走查毕末检同绿。

## 九 tab 走查与操作面（全部实操）

| Tab | 实操 | 结果 |
|---|---|---|
| Overview | 空态→R1 部署→数据态；四指标卡/Processes 摘要卡/Routes 卡/Recent deployments | ✅ 新摘要卡（web 1/1）活体；空态文案诚实 |
| Processes | Running 列 + 行展开实例明细；web 1→2 staged→Apply changes | ✅ 跳 typed 部署详情；**扩容零替换活体**（原 task gd40wli… 保留 + 新 task wiykhdd…，Running=2，ADR-0064 纯扩路径 UI 可见） |
| Deployments | R2 详情（时间线/observe 窗/进程 DNS 面/diff 选择器/Raw 折叠）；列表四行 | ✅（列表 Revision 列见 W-2） |
| Metrics | Process 选择器 all→web | ✅ 序列 3→2（worker 消失）；scope 行增 `process web` 注记；per-instance 图例（web·7fbx2j/web·ivocey） |
| Logs | worker follow（5s 节拍流）→Stop→实例锚+text 过滤重开 | ✅ scope 行 `instance snt1dpa2yi95…`（改名生效）；43 帧全 err 行零 beat 行（VL text + 实例锚叠加正确） |
| Terminal | Connect→`echo`→**Disconnect**→**连接期切实例**→**连接期切 process**→Variables 后 `echo $FOO` | ✅ 三修全活体（见下）；变量注入全链绿 |
| Variables | Add variable FOO=bar（web）→Apply changes | ✅ R3 受理→跳详情；Terminal `foo=bar` 证实注入 |
| Routes | Add route walk-ad.dev.fleetly.run→web:80 tls auto | ✅ 行渲染 + manager 探针 200 "Welcome to nginx"；**Console Delete app 后路由 404**（级联拆除活体） |
| Settings | General/Git hook/Danger zone 走查 | ✅ 占位诚实（spec 读路径 IA v3 §8 二期注记）；Delete app 确认框文案精确 |

## 本批修复/新功能的真机锚

1. **Terminal 收口（4ab9bbe）**：Disconnect 按钮（open 态红钮/connecting 态 Cancel）→ `# disconnected` + `ws: closed` + 重开面；连接期切 instance = 断旧开新（新 session 头行锚 wiykhdd…）；连接期切 process 同构（worker 自实例 snt1dpa2…）。xterm 数据面：`echo walkad-ok-123` 回显+输出+提示符全渲染。
2. **层级钻取批（13e0aca）**：Processes Running/行展开、Metrics 选择器、Logs Instance 文案、Overview 摘要卡——四处在役（上表）。
3. **Rollback 挂账（staging-open-investigations 在案项）本轮未复现**：确认框→提交→网关日志 `DeploymentsService/Rollback` 4.5ms completed + 新部署行 g4←g3（R3 replay）succeeded。端到端全通；在案项保持开放（本轮为"通过"样本，复发再取证）。

## Findings

- **W-1（P2，暂不修——窗口期形态，复发观察）**：直进 tab 时目录加载窗内 hero/面包屑回落**裸全 ULID**（头像取首字符 "0"），数秒自愈；同窗侧栏项目切换器显 "Loading…"。appNameOf 的截断回落（`slice(0,10)…`）在此窗口未生效——疑似 Routes 页 hero 取名路径绕过 appNameOf，或窗口期 data===undefined 走了裸 ID 分支。
- **W-2（P3）**：Deployments 列表 Revision 列恒 `R…hash` 退化形态（详情页标题/Overview 都有 R seq，列表未 join revisions seq 对表）。
- **W-3（P3）**：Variables 分组头 `PROCESS WEB ×2` 的 ×2 是副本数，形似"变量条数"，可读性歧义（建议 `web · 2 replicas` 形态）。
- 环境怪癖（非产品缺陷）：IAB fullPage/clip 截图伪影（既有教训）；Radix Select 弹层跨自动化调用收起——展开+选须同调用内完成。

## 清理

Console Delete app（Danger zone 流，顺带验删除面）→ 路由 404 / 载体 0；`projects delete walk-ad` 收口。零 token 铸造（founder 密码会话）；用户域零扰动（tw.dev 200、服务基线不变）。
