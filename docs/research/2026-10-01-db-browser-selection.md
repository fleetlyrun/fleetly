# 托管数据库浏览器选型报告——2026-10-01

背景：Database 模板（F1.12：postgres[含 pgvector]+redis 起步；F2.1 补 mysql/mongo）需要配套一个轻量级**数据浏览器**（表/键浏览、查询、必要时编辑），并接入 fleetly 的权限体系（Team/Role/Token + `resource:action` Scope）。本文**只做选型，不做实现**；接入 API/词汇等设计决策待用户裁定后另立 ADR。

方法：三路并行调研——①轻量单文件系（Adminer 系/pgweb/pgAdmin/SQLPad/OmniDB）；②可嵌入 web 端（DbGate/CloudBeaver/WebDB/Outerbase/Beekeeper）；③竞品接入先例 + Redis/Mongo 专用浏览器 + 认证桥接模式。全部事实经 GitHub API/release/docs 页核验（2026-10-01 快照），来源见 §9。

## 1. 一页结论

1. **接入形态先于项目选择，且形态比项目更"fleetly"**：推荐**按需实例 + Edge 门禁 + 一次性接入凭证（launcher token）+ 服务端凭据注入 + 默认只读**（§6 形态 B）。它与既有原语完全同构（Task 双形态/TTL/Owner Lease、网络组挂靠、Route、Secret 注入），不发明新机制；idle 成本为零，不破"单核轻量自宿"红线（ADR-0008 参考预算）。
2. **PostgreSQL 主选 pgweb**（MIT、Go 单二进制 ~7MB、`--sessions --lock-session --readonly --connect-backend`）：`--connect-backend` 就是为"平台持有凭据、用户持一次性 token 换会话"的场景设计的官方机制——pgweb 拿 token 回调**我们的** API 取连接串，凭据全程不出服务端。无 X-Frame-Options/CSP 头，iframe 默认可用。
3. **Redis 主选 redis-commander**（MIT、活跃、env 注入多 host、**自带只读模式**、bcrypt basic auth、子路径反代友好）。SQL 浏览器没有一家能正经覆盖 Redis，redis-commander 是该生态的事实标准。
4. **MySQL/Mongo（F2.1 时再终选）最强候选是 Adminer 6.x**：原仓库 2025-02 复活后高频发版（v6.1.1，2026-09-25），凭据注入是文档化插件面（覆盖 `credentials()` 约 20 行插件 + 6.1.0 新增 `verifyLoginToken()` 明确服务"接受外部网站登录"），iframe 有官方 `frames` 插件；Mongo 是年轻插件、Redis 驱动不可依赖。Mongo 专用备选 Mongoku（MIT、只读模式、env 注入连接）。
5. **单工具全栈路线 = DbGate（备选）**：唯一在免费版同时覆盖 PG+Redis+MySQL+Mongo 的 web 端，env 注入连接（UI 隐藏添加/编辑）+ `READONLY_<id>` + per-login 权限 + `SKIP_ALL_AUTH`（明说给反代门禁用）。三处减分：**2024 年中 MIT→GPL-3.0 改版**（fleetly 自身未定许可，仓内无 LICENSE）；镜像 109–147MB（压缩）+ Node 运行时；2026 年有已修复的认证 RCE（GHSA-wm5r-5qp3-5vxf）。组合路线不可接受时启用。
6. **观察项 Panopticum**：MIT、Go+React 单容器、PG/MySQL/Mongo/Redis/Valkey 全方言、**iframe 嵌入是文档化特性**、env 配置连接——用途与我们完全对口，但 v0.3.0（2026-02）、社区极小、bus factor 未证实。设重评触发条件（§8），不押注。
7. **竞品面结论**：Coolify/Dokploy/Easypanel/CapRover/Kamal/zane-ops **无一内置数据浏览器**，全部是"生命周期管理 UI + 一键部署 Adminer/phpMyAdmin 自输凭据"。内置 + 平台认证 + 免输密码的浏览器是**差异化**而非 table stakes（Panopticum 的立项原因正是这个缺口）。

## 2. 硬约束（从 fleetly 既有裁决推导，非新增设计）

| # | 约束 | 出处 |
|---|---|---|
| C1 | 凭据平台持有（Secret 信封加密、值永不回显），用户**零密码输入**，连接串不经浏览器 | CONTEXT.md Secret 词条、领域模型 §9 |
| C2 | Console 只消费公共 REST/WS API，无私有服务端面 | 架构 §1（旧 P0-3 教训）→ 浏览器只能是独立组件/Workload，不能是 SPA 的一部分 |
| C3 | 默认捆绑面守 idle 预算（600MB 参考），受管组件单核轻量 | ADR-0008（参考默认，实测校准） |
| C4 | 权限走 `resource:action` Scope（action ∈ read/write/admin，复数资源名），读写分离，"读默认开放、写显式授权" | internal/identity/scope.go、ADR-0017 |
| C5 | Database 在 per-Project overlay 网络内，跨 Project 默认隔离 | F0.14、ADR-0013 |
| C6 | 一切写操作留痕审计 | F0.7 |
| C7 | 方言路线：PG+Redis（N1）→ +MySQL+Mongo（N2） | F1.12、F2.1 |

C2 推论：候选必须是**独立部署的服务端 web 应用**（桌面/Electron/纯浏览器端连接一律出局——后者还违背 C1）。C3 推论：常驻多实例不可取，倾向按需启动。C4 推论：浏览器本身**不得自带用户体系**，或其用户体系可被平台完全代持/绕过。C6 是最大的设计张力：SQL 控制台本质上是任意查询面，默认只读 + 写档显式授权（见 §7）。

## 3. 候选清盘（淘汰面）

| 候选 | 一句话裁决 |
|---|---|
| pgAdmin 4 | PostgreSQL Licence、极活跃，但 Python+React 重、PG-only、自有用户体系想当"前门"（LDAP/OAuth2 是给独立部署设计的）——与 C2/C3/C4 全面对撞 |
| CloudBeaver CE | Apache-2.0、唯一自带反代可信头认证（`x-remote-user`），但 Java ~417MB 镜像 + 官方 4GB RAM 建议、CE 无 Redis、SSO 在企业版——重量级出局，桥接机制值得抄 |
| AdminerEvo | **已死**（2025-01-24 归档）；其死亡催化了原 Adminer 复活 |
| SQLPad | **已死**（2025-08-23 归档，v7.5.7 终版） |
| OmniDB | 2020 起休眠（末版 3.0.3b，2020-12） |
| WebDB | AGPL-3.0、2025-06 起停滞、无 Redis；卖点"DBMS 发现+凭据猜测"需挂 docker.sock——与 C1 反向 |
| Outerbase Studio | AGPL-3.0、连接存客户端（Electron/浏览器端）、PG 属 beta 档、无 Mongo/Redis；BunnyWay 生产嵌入先例仅作参考物 |
| Beekeeper Studio | Electron 桌面端（2022 年起 GPLv3+商业双轨），形态出局 |
| RedisInsight | **SSPL v1**（≥2.0），非 OSI 开源，嵌入许可面不可接受 |
| rebrow | 官方 README 停止维护并指向 redis-commander；连接参数走 URL（泄凭据） |
| Bytebase / Metabase / Superset / NocoDB / Baserow / Teable / Directus | 品类不符（变更审批/BI/表格应用构建器/无头 CMS），非"任意注入连接的数据浏览器" |

## 4. 深评矩阵（短名单）

| | **pgweb** | **redis-commander** | **Adminer 6.x** | **DbGate** | **Mongoku** | **Panopticum** |
|---|---|---|---|---|---|---|
| 仓库/版本 | sosedoff/pgweb v0.17.0（2025-11-22，持续提交） | joeferner/redis-commander（活跃，2025-11 迁 ghcr.io） | vrana/adminer **v6.1.1（2026-09-25）**，2025-02 复活后 27 版 | dbgate/dbgate v7.3.1（2026-09-24，1–3 周节奏） | huggingface/Mongoku（活跃） | yurymiroshnykov/Panopticum v0.3.0（2026-02） |
| 许可 | **MIT** | **MIT** | Apache-2.0 或 GPL-2.0 双许可 | **GPL-3.0**（2024 年中由 MIT 改版） | MIT | MIT |
| 形态/重量 | Go 静态二进制 **~7MB** | Node 小容器 | PHP 单文件 ~550KB（官方镜像 php:8.4-alpine） | Node，镜像 109–147MB（压缩） | Node | Go+React 单容器 |
| 方言 | **仅 PG** | **仅 Redis** | PG/MySQL/MariaDB 核心；Mongo/Redis 为年轻插件 | **PG/MySQL/MariaDB/Mongo/Redis 全免费版** | 仅 Mongo | PG/MySQL/Mongo/Redis/Valkey/Dragonfly/ClickHouse/MSSQL… |
| 凭据服务端注入（C1） | **`--connect-backend`：一次性 token 回调平台 API 换连接串；另 `--bookmarks-only`（服务端 TOML）** | `REDIS_HOSTS` env（含密码、多 host） | 覆盖 `credentials()` 插件（`login-servers` 示范）；`verifyLoginToken()`（6.1.0）官方面向外部认证 | `CONNECTIONS`+`SERVER_/USER_/PASSWORD_/ENGINE_` env；UI 隐藏增改连接；`SHELL_CONNECTION` 默认关 | `MONGOKU_DEFAULT_HOST` env | env 配置连接 |
| 只读 | **`--readonly`** | **只读模式** | 无内建（靠 DB 授权或自写插件） | **`READONLY_<id>` + 细粒度 `PERMISSIONS`** | `MONGOKU_READONLY` | 未见 |
| 认证桥接（C4） | basic auth 单对 + 代理优先设计；connect-backend 本身即桥 | basic auth（bcrypt）+ `--trust-proxy` + 子路径 | 插件生态（`login-external`/`one-click-login`；注意 `login-reverse-proxy` 只是防爆破聚类，**不是**头认证） | **`SKIP_ALL_AUTH`**（明说给外层反代用）+ BASIC_AUTH + 多 login + JWT API + OIDC/LDAP | basic auth + OAuth2 | basic auth（默认 admin/admin 需覆盖） |
| iframe | **默认无 XFO/CSP 头，可直接嵌** | 未证实（PoC 项） | 默认 `X-Frame-Options: deny`，官方 `frames` 插件放开 | 未证实（PoC 项） | 未证实 | **文档化特性** |
| 安全史 | 无著名 CVE（未做全量扫查） | 无著名 CVE | SSRF 史（CVE-2018-7667/2021-21311）+ 2025 后持续修补（6.1.1 修 SQLite 文件写 GHSA-r9r5-j5q8-8c59）；server 参数被平台接管后 SSRF 面大减 | 认证后 RCE GHSA-wm5r-5qp3-5vxf（2026，已修，核验钉版） | — | 无记录（太年轻，双向解读） |

## 5. 认证桥接模式（证据归纳）

- **模式 a：反代可信头**。Traefik ForwardAuth 把平台会话判定委托给 fleetlyd，`authResponseHeaders` 注入并**剥离**身份头（防伪造）。但 DB 工具生态几乎无人消费可信头——唯一原生支持的是 CloudBeaver CE（被我们淘汰），故此模式只作**外层门禁**，不作身份传递。
- **模式 b：一次性 token → 预认证会话（推荐内层机制）**。pgweb `--connect-backend` 是唯一把该模式做成官方协议的（wiki 原话即"服务里带用户访问自己数据库的页面"）；Adminer 是插件生态最厚（社区 autologin 镜像 `ludekvesely/adminer` 等可参考）；DbGate/Mongoku/redis-commander 走 env 静态注入 + 实例级作用域。
- **iframe 陷阱**：现代浏览器 cookie 默认 `SameSite=Lax`，跨站 iframe 不带 cookie——Console 与浏览器实例放**同一注册域子域**（如 `db-<project>.<domain>`）即同站规避；XFO 工具各自处理（见矩阵）。

## 6. 接入形态三选项

**形态 A：常驻受管组件，挂全部活跃 Project 网络**（traefik B1 同款）。优点：零启动延迟、一处升级；缺点：所有 Project 的连接集中一容器（爆炸半径大、C5 隔离被削弱）、idle 常驻吃预算（C3，Node 系尤甚）。

**形态 B（推荐）：按需实例**。用户在 Console 发起浏览 → fleetlyd 校验 Scope → 铸造短 TTL 一次性 launcher token → 在目标 Project 网络上起浏览器实例（凭据从 Secret 解封注入 env，C1）→ Route + Edge ForwardAuth 门禁对外 → 会话结束/超时回收。与 Task（one-shot/resident、TTL、Owner Lease、网络组挂靠）完全同构，**不新增平台机制**；实例只持有单个 Project 的连接（C5、爆炸半径最小）；idle 零成本（C3）。代价：秒级冷启动（可预热池缓解）与实例生命周期管理。pgweb/redis-commander/Adminer 均可作按需实例；DbGate 因重量只适合此形态而非常驻。

**形态 C：库级嵌入 fleetlyd——否决**。无候选具备可嵌入库形态（pgweb 是独立二进制、其余 PHP/Node）；且必然制造"Console 私有服务端面"违 C2。

## 7. 权限与安全设计要求（留给后续 ADR 的输入，本节不裁决）

1. **Scope 映射**：`databases:read` 门禁进入浏览器（默认只读档：工具只读开关 + 平台侧铸造只读 DB 角色双保险）；`databases:write` 才解锁写档（SQL 控制台天然是任意查询面——"读默认开放、写显式授权"在此必须是双层执法，工具开关只是 UX，真隔离靠 DB 授权）。
2. **审计缺口**：浏览器内的查询不属于平台写 RPC，C6 覆盖不到。至少记录"谁在何时开了哪个 Database 的浏览器会话"；查询文本是否留痕（审计价值 vs 内容隐私）是开放问题。
3. **SSRF 面**：连接目标一律由平台下发（Adminer 历史 SSRF 的攻击面即用户自填 server 参数），实例只挂 Project 网络进一步收窄。
4. **launcher token**：短 TTL、单次、绑定 User/Project/Database 三元组，经 Edge 校验后才允许建立工具会话。
5. **钉版**：所有浏览器镜像走 digest 钉定（与 F2.7 dbtemplate 同纪律）；DbGate 必须钉在 RCE 修复版之上。

## 8. 风险与开放问题

- **DbGate GPL-3.0**：不改代码、独立容器分发属标准合规形态；但"永不打补丁"是工程纪律约束（任何胶水需求必须用 env/反代消化）。fleetly 自身许可未定（仓内无 LICENSE），若最终闭源分发，GPL 组件的聚合分发边界需一次法务确认。
- **组合路线的隐性成本**：pgweb+redis-commander（+后续 Adminer/Mongoku）= 2–4 个组件各自升级/钉版/桥接，UX 不统一。这是"单工具全栈 DbGate"依然保留为备选的原因；**触发条件：组合路线两组件落地后 UX/维护实测不可接受，或法务确认 GPL 无碍且 DbGate iframe PoC 通过**。
- **Panopticum 重评触发**：进入 1.0、或贡献者/社区规模显著增长、或我们 N2 前需要"单工具全方言"而 DbGate 被否——任一满足即重评。
- **iframe 行为未证实项**：redis-commander/DbGate/Mongoku 的 XFO/CSP 需 PoC 实测（pgweb/Adminer 已证实）。
- **落地批次建议**：N1（F1.12 Database 最小集）不带浏览器（CLI 面为主）；浏览器随 N2b/N3 Console 面落地，PoC（iframe 实测 + pgweb connect-backend 与 fleetlyd 对接验证）可在 N2 前插入。本文不改动功能清单。

## 8.1 终选（2026-10-04，F2.1 批——MySQL/Mongo 通道）

F2.1 把 mysql/mongo 收进模板矩阵（dbtemplate 值域五引擎），浏览器通道随之终选：

- **MySQL → Adminer 6.1.1**：核心方言（非插件）+ PHP 单文件 ~550KB（按需
  实例冷启动最轻）+ credentials 插件与 `verifyLoginToken()`（6.1.0+）官方面
  向外部认证——launcher token 桥接的模式 b 生态最厚。SSRF 史的攻击面
  （用户自填 server 参数）由平台接管连接目标结构性消除（§7.3）。
- **Mongo → Mongoku**（huggingface，MIT，活跃）：Adminer 的 Mongo 支持
  是年轻插件（矩阵 §4"方言"行），不把数据库通道押在插件成熟度上；
  Mongoku 单方言专注 + env 注入连接（C1 可行）。iframe XFO 行为未证实
  → 随浏览器落地批 PoC（§8 既有项，与 redis-commander/DbGate 同批）。
- **组合路线定型**：pgweb（PG）+ redis-commander（Redis）+ Adminer
  （MySQL）+ Mongoku（Mongo）四件按需实例；DbGate 仍为"单工具全栈"
  备选，触发条件不变（§8）。全部走 digest 钉定（F2.7 同纪律）。

本文其余结论（形态 B 按需实例、launcher token、默认只读双保险）不变。

## 9. 来源

- pgweb：repo/README/wiki（[sosedoff/pgweb](https://github.com/sosedoff/pgweb)、[Connect-Backend wiki](https://github.com/sosedoff/pgweb/wiki/Connect-Backend)、`pkg/command/options.go`）、v0.17.0 release 资产（~7MB）
- Adminer：[vrana/adminer](https://github.com/vrana/adminer)（releases、`adminer/include/adminer.inc.php` 插件 API、`design.inc.php` 头部）、[plugins 目录](https://www.adminer.org/en/plugins/)、[security 页](https://www.adminer.org/en/security/)、CVE-2018-7667/CVE-2021-21311、GHSA-r9r5-j5q8-8c59；AdminerEvo 归档（discussion #233）；AdminNeo（adminneo-org/adminneo，v5.8.0 2026-09-16，备胎）
- DbGate：[dbgate/dbgate](https://github.com/dbgate/dbgate)（LICENSE/GPLv3 与 LICENSE-OLD/MIT 改版证据）、[env-variables 文档](https://docs.dbgate.io/dbgate/customization/env-variables/index.html)、[web-app-config 样例](https://docs.dbgate.io/dbgate/customization/web-app-config/index.html)、Docker Hub tags（7.3.1=147MB/alpine 109MB）、GHSA-wm5r-5qp3-5vxf
- CloudBeaver：[dbeaver/cloudbeaver](https://github.com/dbeaver/cloudbeaver)（25.3.5 drivers 目录、反代头认证 wiki、issue #3344 iframe cookie）、Docker tags（~417MB）、EE 4GB RAM 建议
- Redis/Mongo：[redis-commander](https://github.com/joeferner/redis-commander)、[RedisInsight SSPL](https://redis.io/legal/licenses/)、[rebrow](https://github.com/marians/rebrow)、[mongo-express](https://github.com/mongo-express/mongo-express)、[Mongoku](https://github.com/huggingface/Mongoku)、[Panopticum](https://github.com/yurymiroshnykov/Panopticum)
- 桥接模式：[Traefik ForwardAuth](https://doc.traefik.io/traefik/middlewares/http/forwardauth/)、[Authelia Traefik 集成](https://www.authelia.com/integration/proxies/traefik/)、[CloudBeaver 反代头认证 wiki](https://github.com/dbeaver/cloudbeaver/wiki/Reverse-proxy-header-authentication)、Adminer autologin 镜像（ludekvesely/adminer、mvandrew/adminer-autologin）
- 竞品先例：Coolify（databases 文档 + 一键 pgAdmin）、Dokploy（databases/connection 文档）、Easypanel（Adminer 模板）、CapRover（one-click-apps）、Kamal（accessories 文档）、zane-ops（opensourcedrop 评测）、dbeverywhere/coolify-adminer（缺口佐证）
- 淘汰面：pgAdmin4（config.py `X_FRAME_OPTIONS`/`AUTHENTICATION_SOURCES`）、SQLPad/OmniDB 仓库状态、WebDB（WebDB-App/app）、Outerbase（outerbase/studio + BunnyWay fork）、Beekeeper（ultimate-and-gpl 博文）、Bytebase/Metabase/Superset/NocoDB/Baserow/Teable/Directus 各官网/仓库
