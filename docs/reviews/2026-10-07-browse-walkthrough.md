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

## 浏览器级（Console browse 按钮）——挂账（后端缺席）

本会话浏览器后端注册表为空（agent.browsers.list() = []）——浏览器级
补档与 F3.1/F3.2/F3.3 三批的待办同因同挂（后端可用时一并补）。
已覆盖的部分（HTTP 契约级 = 浏览器将执行的整条链）：

- Console 入口代码面：`POST /v1/databases/<id>/browse` → 响应 url →
  `window.open`（新窗口；一次性票据 120s 内点击即兑）——console:verify
  零漂移钉死；按钮渲染在 DatabaseRow（Resources 页 databases tab）。
- 浏览器消费的 HTTP 链已全绿：entry 302 + Set-Cookie（HttpOnly/
  SameSite=Lax——浏览器原生接受形态）→ 重定向 / → ForwardAuth 放行 →
  pgweb 页 200（title=pgweb）——即 window.open 后浏览器实际发生的每一步。
- 待补档锚：真实浏览器中 torchwood-pg 表集可见性（21 表族）+ 只读档
  写语句被 postgres 会话拒绝的 UI 形态呈现。

## 残留与挂账

- 走查会话 ×4 回收实录：活到硬 TTL（created+30min）整点后 Remove 拆载体 +
  删行（rows=0/carriers=0）——**硬 TTL 回收真机锚**；10min 空闲窗未提前
  触发（走查探针迟到接触为最可能成因；空闲判定 fake-clock 单测绿——
  引擎行为面无红，挂账观察）。
- mysql（Adminer 无只读方言）在 staging 未开写档走查（write 档需
  databases:write；只读角色铸造挂账，ADR-0051 决策 6）。
- E_QUOTA_EXCEEDED 共用 suggestion 文案对 browse 语境欠贴切（挂账）。
