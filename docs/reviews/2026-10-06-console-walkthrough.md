# fleetly Console 走查报告（F3.1 写面批，2026-10-06）

对象：staging 现役 Console（`161b62b-f31walk`，SSH 隧道 `127.0.0.1:19528` → manager `:9081`）。
走查口径：**HTTP 面与消费契约级**——本环境无浏览器后端（`agent.browsers.list()` 空，IAB/cdp 均不可用；browser-walkthrough skill 已加载并核实），UI 渲染层由 tsc 严格 + vitest 19 用例承载，浏览器级走查待有浏览器后端的环境补（F2.6 同款先后序：先 HTTP 对拍、后浏览器走查补档）。凭证 `console-walk-f31`（owner，走查后已吊销）。

## 换装

`826fd93-conswalk → 161b62b-f31console`（本批四 commit）→ `161b62b-f31walk`（W1 修复）。前置 Platform Backup `aadd7634` + 卷 tar（三受管库卷 + zot 卷 + 存量 torchwood-pg 旧卷）。00024 迁移随批前滚（goose → 24）。

**零扰动断言带注**：task 行数 22 服务中 19 逐位一致；3 个用户域服务滚动一次——**ADR-0048 引擎批（e2e20e6..8e8a925）首次上真机的一次性载体 spec 收敛**（P16 双别名等新渲染面 vs 旧代码物化的存量载体；mlbridge 为停服窗内应用自崩 exit 1 后自愈）。收敛后 2 分钟复测零再滚（STABLE-NO-FURTHER-ROLLS）；tw.dev 200、torchwood-pg 21 表锚不变、journal 无 goose 报错。

## 逐面判据

### HTTP 面（五项）—— 全部通过

index no-cache / hashed asset immutable / SPA fallback 200 html / 未知 `/v1/*` JSON 404 / 无凭证 401（统一信封 E_UNAUTHENTICATED）；登录验证面 whoami：好 token 200（token_name/role_name 可渲染）、坏 token 401 带建议文案。

### 蓝绿叙事（F3.1 旗舰面）—— 全部通过

走查项目 walk-f31 / app bgwalk，compose `deploy.strategy: blue-green`（whoami）：

| # | 判据 | 结果 | 证据 |
|---|---|---|---|
| ① | 第二笔部署 from_generation 落 REST 面 | ✅ | 响应 `"generation":"2"` + `"from_generation":"1"` + from_revision（首代行零值省略——不可见性同验） |
| ② | 双代窗内两代载体并存 | ✅ | observing 时点旧代 `9efc0102` 1/1 + 新代 `b6614b75` 1/1 并存；observe_deadline 在场 |
| ③ | 窗口收口旧代退役 | ✅ | succeeded 后唯一在役服务 = 新代；容器 Aliases 三形态 `web` / `web.bgwalk` / `web.g5`（{proc}/{proc}.{app}/{proc}.g{gen}） |
| ④ | Revision 策略目录 | ✅ | R1..R3 全带 `"process_strategies":[{"process":"web","strategy":"DEPLOY_STRATEGY_BLUE_GREEN"}]`（rolling 零值省略口径与契约一致） |
| ⑤ | 取消写面 | ✅ | 卡 L1 的部署（镜像 tag 不存在）`POST /v1/deployments/{id}/cancel` → cancelled；L1 卡因如实（拉取拒绝循环，旧代零扰动） |
| ⑥ | Route 端到端经蓝绿换代 | ✅ | walk.dev（http :80）200，whoami 主机名 = 收口代服务名 |

### 写面（UI 表单的同一 REST 调用）—— 全部通过

projects/apps 创建删除（项目有 App 拒 409 文案如实）/ secrets put+list+delete / configs put（版本化）/ shared-variables put（affected_apps 信封在场）/ routes create+delete / **uploads raw-tar POST**（10KiB 确定性 tar，重传 `"deduplicated":true` 内容寻址去重）/ databases create（redis pending→running 16s）+ backup trigger / tasks create+stop+delete / schedules create+trigger+delete / tokens create（secret 只显一次）+revoke / freeze set+lift / audit 过滤读面（database.* 动作可见）。错误信封渲染面：compose ports 整数形态被拒（`port entry must be a string like "8080"`——字段路径精确，UI ErrorNote 直接可用）。

## 发现（按处置）

- **W1·服务端中危（已同日修复）**：compose 声明 `ports` 不自动挂项目 default 网——image/upload 形态有 `portDeclNetworks`、compose 路径漏了；Route-facing 只兑现 404 半边（端口已知、Proxy 不可达 → 502）。修复 = ports 在场且 networks 未列 default 时补挂（`internal/spec/normalize.go`，spec_file 用户亲笔不改写）；同批单测 `TestNormalizeComposePortsAttachDefaultNetwork` 三锚（补挂/显式保留/零端口零挂网）；修复后 staging 复验 walk.dev 200。
- **O1·观察（API 面）**：`GET /v1/databases` 不带 project_id 回 **404 E_NOT_FOUND "project not found"**（而非 400 E_INVALID_ARGUMENT）——ListApps 是 400 形态，两读面口径不一致；Console 恒带 project_id 不受影响。归属 API 面小裁决，留后续批。
- **O2·观察（已知坑复证）**：manager 自打公网 IP 的 https 探针 000（manager 出站自环防火墙形态），本地 `-k` 200（LE staging CA 不受信是既知）；走查探针统一走 `--resolve 公网IP` 或 localhost+Host。
- **O3·观察（引擎批上真机）**：三个用户域服务在换装时一次性滚动（载体 spec 收敛），属 ADR-0048 引擎批首次真机部署的预期形态，但**升级零扰动断言的 e2e 矩阵覆盖不到**（夹具无旧代码物化的存量载体）——已在 runbook 记录；后续引擎批上真机应预期同类一次性收敛。

## 结束动作（已全部执行）

- walk-f31 项目级联清理（app→db→project 逐级删除 200；`fleetly-vol-walkredis` 卷残留手工清除——Database 级联删库不删卷，与 staging 残留清理先例同形态）；
- `console-walk-f31` token 吊销 + 已吊销复核；
- SSH 隧道关闭；staging 现役 = `161b62b-f31walk`（W1 修复在内）。

## 总判定

**PASS-with-notes**：判据面全绿；W1 同日修复带回归锚；O1/O3 记档归属后续批。
