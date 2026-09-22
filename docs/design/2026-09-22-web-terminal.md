# E7 Web 终端（fleetly-exec 执行中继）+ 控制面 TLS 专项设计

| 状态 | 日期 | 说明 |
|---|---|---|
| **已实现+真机验证（2026-09-22，W5 收官）**：S5 TLS 基座+S6 终端全落；staging 真机演练全过（[runbook §11](../runbooks/vps-dogfooding.md)）——真 PTY 回显/事件/内容零泄漏/relay 自动 wss 化（TLS platform 真证书三端点 200、明文双面拒、off-host token 加密）。**真机修复一件（W5 最重要收获）**：gateway 对 gRPC 面的回拨（torchwood 式 FromEndpoint 反代）在 TLS 形态下仍用明文凭据 → 全量 REST /v1 断——CLI 直连与 native 端点的 e2e 都测过也拦不住这一条；修复=回拨凭据跟随 TLS 形态（回环自拨+进程信任锚 InsecureSkipVerify，外部面 TLS 由监听器强制）+ 单测正负向 + e2e TLS-9 断言（教训：**代理形态的每一跳都要有 TLS 形态下的断言**）。S6 落地注记：①exec 反向通道拨 **8420（HTTP 面）** 非 §3.3 字面 8421——native WS 端点在 gateway 面；②注册帧成员发现=「节点 hostname 反查为主 + 容器 ID 前缀匹配补充」（swarm 任务容器 hostname=节点 hostname 非容器 ID，dind 实证）；③exec 镜像已钉 digest（CI 首推 run 35754500342，台账 #18，豁免摘除）；④terminal.enabled 缺省 true；manual/off TLS 形态 relay 降级 ws:// 明文（诚实标注），platform 模式拨 IP 按 ctrl.\<base\> SAN 校验无 insecure skip | 2026-09-22 | 裁决输入：[架构 D19](2026-09-17-architecture.md)（执行中继原始裁决，本票 **D-W5-3 修订其通道机制字面**、安全面条款全部保留）、V2-8（CLI TLS 与 E7 同批，规划 §3）、R1 细则（zane-ops webshell 调研 §7：PTY+exec+白名单照抄，补齐空闲超时/审计/连接上限）、[v0.2 规划 §2 W5 行](../plan/2026-09-20-v0.2-plan.md)。**D-W5-3**（反向常连）= 用户授权按设计合理性定夺（2026-09-22，用户并认领解决 W3-F2 UDP 放行）；D-W5-1（MCP 暂缓）与 D-W5-2/D-W5-4 见 [observability 专项](2026-09-22-observability.md) |

## 1. 现状与问题

1. **D19 字面机制不可实现**：原文「控制面经 `tasks.fleetly-exec` DNS 正向拨入」——fleetlyd 是宿主进程，overlay 网络对宿主不路由（E3-5 实证：宿主不可达 `http://rustfs:9000`，同源结构性限制），swarm DNS 也不对宿主解析。正向方案无论 UDP 是否放行，都必须在 manager 上加一个**常驻代理容器**代拨——多一个组件、多一个故障点、多一个监听面。
2. **跨节点正向通道同时被 W3-F2 卡**：overlay 数据面（VXLAN/UDP）被云防火墙过滤——控制路径若建在 overlay 健康上，则终端可用性与网络环境强耦合。
3. **控制面明文**：8421（gRPC）/8420（HTTP）无 TLS——staging 上 CLI token 与 Console 登录走公网明文，违反用户既定红线（敏感通道不走公网明文，8423 讨论时裁）。V2-8 定为与 E7 同批。
4. **平台无 WebSocket 面**：coder/websocket 已在依赖复核轮落定选型但未引入（同 minio-go 当年「已裁未引」状态）。

## 2. 执行中继 fleetly-exec（D19 修订版）

### 2.1 通道机制（D-W5-3：反向常连）

- **形态**：swarm **global service** `fleetly-exec`，每节点一任务；仅挂内部网络 `fleetly-exec-net`（**不发布任何 host 端口、不挂应用网络**），挂载本节点 `/var/run/docker.sock`。
- **反向常连**：任务启动即持集群 token（Swarm secret `fleetly-exec-token`，平台 duty 生成/轮换/分发）**出站**拨控制面 `wss://<control_addr>/internal/exec-relay`（TLS 面见 §4；TLS off 的单节点明文形态为 `ws://` + token——诚实降级说明）；断线指数退避重连（1s 起、30s 封顶）。
- **注册**：连接建立即发自报帧（任务 container hostname）→ 控制面经 Docker task 反查 NodeID（**成员发现零自研**，D19 原文保留）；15s ping keepalive。
- **会话路由**：控制面维护 node→连接表；终端会话按目标容器所在节点路由到对应连接。
- **裁断理由（设计合理性，三点）**：①正向方案甩不掉 manager 常驻代理容器（宿主不可路由 overlay 是结构性的），反向**零新增常驻组件**；②反向复用平台既有惯例——8423 provider 端点就是「受管组件持 token 主动拨控制面 + 平台证书 TLS」的同型先例（D-MN-6）；③控制面自己的控制路径不依赖 overlay 数据面健康（W3-F2 类环境下终端仍可用——出站走本节点 gwbridge→VPC TCP）。
- **D19 不变项（全部保留）**：API 面收窄 `healthz`/`exec`；只对带 `fleetly.app` label 的容器执行（其余 403）；集群 token 经 Swarm secret 下发；会话空闲 10 分钟/硬上限 30 分钟；`terminal` 独立 scope（默认仅 admin）；起止入审计；MCP 工具面不暴露终端（D10 前瞻条款，E2 暂缓后随 v0.3 生效）。

### 2.2 镜像（第二个第一方平台镜像）

- `ghcr.io/fleetlyrun/fleetly-exec:<ver>`：`deploy/Dockerfile.exec` 纯 COPY 静态二进制（dbtools 同款：无 RUN 多架构免 QEMU），CI `exec.yml` workflow（dispatch + release 可调用 + cosign 签名），digest 钉 Go 常量。relay 逻辑 = 独立小 main（复用 SDK/共享包拨号），不进 fleetlyd 主二进制。

### 2.3 会话协议与 PTY

- WS 二进制帧（长度前缀 + 类型）：`register` / `session.open{container, shell}` / `stdin` / `stdout` / `stderr` / `resize{cols,rows}` / `session.close` / `ping`。
- relay 侧 exec：Docker API `ContainerExecCreate{Tty, AttachStdin/Out/Err, Cmd=[shell]}` + attach + `ExecResize`；**shell 白名单 = `/bin/bash`、`/bin/sh`**（按容器内存在性探测择一，R1 标准做法照抄；用户自定命令 v0.2 不开——白名单就是命令面）。
- 目标选择：app 的指定 service → 控制面列 running tasks → 任选（多副本时选最新）；用户在 Console 选 service，不选 task（副本细节对操作员透明）。

### 2.4 限额、安全与审计

- **时限**：空闲 10min（无数据帧）断开 + 硬上限 30min（连接侧与 relay 侧双保险）；**并发上限**：per-token 2、全局 8（对齐 WatchEvents per-token 5 的 A5 精神，终端更重故更紧）。
- **scope**：`methodScopes` 新增 ExecService.* → `terminal`；`admin` 蕴含 `terminal`；独立 token 需显式 `--scopes terminal`（read/deploy **不**蕴含——默认仅 admin 的 D19 原文）。
- **审计**：`terminal.opened` / `terminal.closed`（actor、token、app/service/container、node、时长）双落审计面与事件注册表（只增）。
- **relay 攻击面**：无 label 容器 403（relay 本地强制，不依赖控制面）；集群 token 错误→控制面拒连；healthz 无鉴权（零信息面）。

### 2.5 API 与 Console

- **ticket 流（浏览器无自定义 WS 头的诚实解）**：`POST /v1/terminal/tickets`（Bearer，terminal scope，body 指定 app/service）→ 一次性 60s ticket（绑 token+app+service）→ `GET /v1/terminal?ticket=...`（native 端点 WS 升级，coder/websocket）。长效 token 不进 URL。
- **Console**：AppDetail 终端入口（新前端依赖 `@xterm/xterm` + `@xterm/addon-fit`，MIT）；面板常驻显示：目标容器/节点、剩余时限、断线原因（超时/上限/服务重启）。
- CLI 终端：**不做**（本地场景 `docker exec` 直达；Web 终端是 UI 场景，D19 定位）。

## 3. 控制面 TLS（V2-8 同批）

### 3.1 配置面（config.yaml，静态项）

- `control_plane.tls.mode` ∈ {`off`（缺省）, `platform`, `manual`}；`manual` = `control_plane.tls.cert_file` / `key_file` 路径；`platform` = **复用平台证书**（需 `base_domain` 非空；多 SAN 单证书 ctrl/registry/console[+s3]，LE 签发与续期/轮换由平台证书 duty 既有机制免费承接——D-MN-6）。
- 生效面：**8421（gRPC）与 8420（HTTP：gateway + native 端点 + /ui）双面同证书**。off = 今日行为零变化（单节点 localhost 形态不受影响——默认绑定 127.0.0.1 本就无公网面）。
- 证书重载：平台证书续期后热重载（平台证书 duty 落盘回调）；manual 模式 SIGHUP/重启生效（文档明示）。

### 3.2 CLI / SDK

- CLI：`--tls`（校验，ServerName 用所拨主机名）/ `--tls-insecure`（跳过校验，显式旗标不默认）+ env `FLEETLY_TLS=true|insecure`；缺省明文（兼容存量）。
- SDK：`WithTLS(*tls.Config)` / `WithTLSInsecure()`；`PerRPCCredentials.RequireTransportSecurity` 相应修正为跟随配置（今日恒 false 是明文形态的产物）。
- Console 访问口径：TLS on 后经 `https://<SAN 主机>:8420` 访问（如 `console.<base>:8420`，需 DNS A 记录指到 manager）；IP 直连会有证书名不匹配（浏览器例外/换 SAN 主机名，文档诚实指引）。

### 3.3 exec 反向通道 TLS

- relay 拨 `wss://<advertise_addr>:8421`；`tls.Config.ServerName` = 平台证书 SAN 主机名（`ctrl.<base>`，经服务 env `FLEETLY_CONTROL_TLS_NAME` 下发）——**拨 IP 但按 SAN 名校验**，无 insecure skip；`platform` 模式外（manual/off）相应退化并诚实标注。

### 3.4 e2e

TLS 模式 dind 冒烟（manual 模式自签证书最简）：双面证书生效 → CLI `--tls` 校验通过/`--tls-insecure` 显式旁路 → 明文拒绝 → gateway/native/UI 全路径可用。

## 4. 验收锚定

| 面 | 验收 |
|---|---|
| 安全面套件（R1/D19 验收原文「中继安全面测试套件」） | 无 label 容器 403 / 集群 token 错误拒 / 空闲超时掐断 / 硬上限掐断 / per-token 并发上限 / `terminal` scope 缺失拒（read token 拿不到终端）/ 起止审计行 + 事件 / ticket 一次性（重放拒）与 60s 过期 |
| 功能 | 双节点容器终端（staging；worker 面依赖 UDP 放行——若未放行则 manager 面全验证 + worker 面挂账诚实记录）/ resize / 白名单 shell 探测 / 断线重连后新会话可建 |
| TLS | §3.4 全项 + exec 通道 wss + ServerName 校验路径 |
| 预算 | exec 任务 idle 计入全栈台账（global 每节点一份） |
| 契约 | 事件码只增（`terminal.*`）；proto 加法过 breaking 门禁；Console 锚点清单只增 |
