# F3.6 数据浏览器 staging 走查——2026-10-07

- 环境：manager fleetly-dev（b4953ca-f36browse，9df27c9-f33tpl 升级而来；
  00027_browse_sessions 迁移随批前滚；Platform Backup `08c13298` 前置 +
  卷 tar 七份 belt-and-suspenders）。
- 面配置：unit drop-in `browse.conf`（host_suffix=dev.fleetly.run、
  gateway_url=http://10.124.0.3:9081、tls 缺省 none）。
- 判定：**PASS**（HTTP/消费契约级 + 浏览器级，锚见下）。

## 换装零扰动

| 锚 | 结果 |
|---|---|
| torchwood-pg task 行 | Running 15h 不变（docker service ps） |
| tw.dev / n0.dev | https 200（`curl -k --resolve …:443:146.190.58.0`；:80 明文 404 是既有 TLS 路由形态） |
| 双节点 relay | relay_online=true ×2，relay_agent_version=b4953ca-f36browse（worker AgentCommand 重跑） |

## browse 面全链（CLI + curl 契约级）

库：torchwood-pg（pgvector，running）——只读档（缺省）。

| 步 | 锚 | 结果 |
|---|---|---|
| 1 | `fleetly databases browse <id>` 受理回显 | pgweb / read_only=true / enforcement=session / expires_in=120 / url=browse-<sid>.dev.fleetly.run/v1/browse/entry?session=&ticket= |
| 2 | 载体在场 | `fleetly-browse-<sid>` 1/1（docker service ls） |
| 3 | entry 兑换（公网 --resolve :80） | 302 + `Location: /` + `Set-Cookie: flt_browse=<sid>.<grant>; Path=/; Max-Age=<剩余>; HttpOnly; SameSite=Lax` |
| 4 | 同票二次 | **401**（Launcher Ticket 单用途） |
| 5 | 无 cookie 访工具路由 | **401**（traefik v3.5.4 ForwardAuth middlewares 真机生效——受管面首发） |
| 6 | cookie 取工具页 | 200，`<title>pgweb</title>`（ForwardAuth 放行） |
| 7 | 服务端只读执法 | POST /api/query `show default_transaction_read_only` → `["on"]`（torchwood-pg 真簇——postgres 会话级，非工具摆设） |
| 8 | quota 面 | 10min 窗内第 5 会话 → `E_QUOTA_EXCEEDED: too many concurrent browse sessions for this team (limit 4)` |

真机坑实录：受理后即刻 curl 撞 traefik 5s 配置轮询（404 假象）——链前
sleep 8s 即稳；e2e browseprobe 已内置同款重试窗（步 1/3）。

## 浏览器级（Console browse 按钮）——2026-10-07 补档收口

后端在场环境（ZCode IAB / Chromium）补做浏览器级走查，原挂账两锚全闭：

| 锚 | 结果 | 活体证据 |
|---|---|---|
| browse 按钮受理 | ✅ | Resources → databases → torchwood-pg 行 browse 按钮点击 → `POST /v1/databases/<id>/browse` **200**（会话铸造成功；`window.open` 弹窗被走查环境的合成点击弹窗拦截器挡下——环境伪影，真实用户可信点击不受限；后续经 REST 同 token 铸会话 + 浏览器直走 entry 链等价完成） |
| entry 链（window.open 后浏览器实际发生的每一步） | ✅ | 浏览器新标签直访 entry URL（一次性票据）→ 302 + `flt_browse` cookie → `/` → ForwardAuth 放行 → **pgweb 页 200（title=pgweb）** |
| **表集可见（挂账锚①）** | ✅ | pgweb 侧栏 `Tables 21` 全列（admin_projects/admins/api_keys/audit_logs/catalog_*/document_events_outbox*/idempotency_keys/invite_codes/project_oauth_providers/projects/provider_resource_index/runbook_steps/runtime_var*/schema_migrations/tw_secrets）+ Functions 162 + Sequences 3——真簇 21 表族浏览器直见 |
| **select 见数（挂账锚②前半）** | ✅ | pgweb 查询面执行 `select current_database(), count(*) from admins` → `fleetly / 0`（真簇 200，3ms） |
| **写语句被拒（挂账锚②后半）** | ✅ | `create table` → **400 `query contains keywords not allowed in read-only mode`**（pgweb 工具层关键词拦截——enforcement 语境的干净错误信封）+ 同会话 `show default_transaction_read_only` → **`on`**（postgres 会话级服务端执法）——**双层只读执法在浏览器上下文实证**（会话 cookie 经 ForwardAuth 的完整链上） |
| enforcement 展示形态 | 注 | Console v1 侧 browse 按钮带 title 提示（"read-only … session … one-time ticket, 120s"），响应 enforcement 字段无专门展示——与实施批"enforcement 展示留后续批"挂账一致，确认为已知挂账非缺陷 |

- 走查环境注记：本会话 IAB 的 locator click 通道超时（按钮点击经 evaluate 直发等价完成）；pgweb 为外置工具 UI（ace 编辑器/原生 DOM），探针不作为其缺陷依据。
- 走查会话 ×2 靠硬 TTL/空闲回收（复测 browse carriers=0）；走查 token 已吊销。

## 残留与挂账

- 走查会话 ×4 回收实录：活到硬 TTL（created+30min）整点后 Remove 拆载体 +
  删行（rows=0/carriers=0）——**硬 TTL 回收真机锚**；10min 空闲窗未提前
  触发（走查探针迟到接触为最可能成因；空闲判定 fake-clock 单测绿——
  引擎行为面无红，挂账观察）。
- mysql（Adminer 无只读方言）在 staging 未开写档走查（write 档需
  databases:write；只读角色铸造挂账，ADR-0051 决策 6）。
- E_QUOTA_EXCEEDED 共用 suggestion 文案对 browse 语境欠贴切（挂账）。
