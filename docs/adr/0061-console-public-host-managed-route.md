# ADR-0061: Console 面板公网受管发布（console 主机路由 + 登录限速 + ACME CA 可配置）

日期：2026-10-11；状态：已采纳（用户拍板 C 批——访问改进评估三解中的产品解，2026-10-10 评估在案）。

## 背景

Console 访问长期依赖手工 SSH 隧道（`ssh -N -L` 转发 9081）——隧道易逝、每会话重建、单机可用。三层解法评估（稳态隧道 / WireGuard 私网 / 产品批）后用户拍板产品解：面板作为受管路由发布到公网。

- Console 与 REST gateway 同进程同端口服务（`internal/console.Mount`：非 `/v1` 路径出 SPA、`/v1/*` 交 REST gateway）——**一条 Host 路由即整面**，零新端口零新服务。
- `*.dev.fleetly.run` 泛解析已指向 staging 受管 traefik 入口（runbook 拓扑节），`console.dev.fleetly.run` 零 DNS 动作。
- browse 会话（ADR-0051）已立「engine 合成 ephemeral 受管路由」先例：entry 免门禁路由走 `Host && PathPrefix`（规则更长优先级更高），后端即 gateway 地址。
- 既有诚实边界：Login 等 PUBLIC 端点无限速（identity.go 记档——「控制面 9080/9081 均 VPC-only，端口暴露矩阵承载边界」）。公网可达后该边界不能再靠绑址承载，限速从挂账变落地。
- 受管 ACME 缺省 LE staging CA（2026-09-30 裁决，防误触发生产配额；生产 CA 留安装引导批切换）——staging 证书浏览器告警是访问痛点的组成部分。

## 决策

1. **config 新增 `console` 发布面三键**（internal/config/config.proto；env `FLEETLY_CONSOLE_HOST` / `FLEETLY_CONSOLE_GATEWAY_URL` / `FLEETLY_CONSOLE_TLS`）：`host` 是面板公网主机名，`gateway_url` 是 traefik 容器可达的 REST gateway 基址（`browse.gateway_url` 同文化——traefik 容器能拉到的地址即正确地址），`tls` 是路由 TLS 模式（空值随渲染层口径视为 auto）。**host 空 = 整面停用**（不发布任何路由；registry.addr/browse.host_suffix 同文化：收窄是显式动作，升级零扰动）。
2. **engine 合成双路由**（`consoleCapabilityRoutes`，publishRoutes 合并面）：
   - **主路由 Host-only** → `gateway_url` 的 host:port——Console SPA 与 `/v1` REST API 整面公网可达（同源服务是 Mount 的既有事实，路由只是把既有同源性暴露出来）；
   - **登录路由 `Host && PathPrefix(/v1/auth/login)`** 带 rateLimit——规则更长优先级更高的既有先例（browse entry 同款）；限速缺省**每源 IP 窗恒 1 分钟、均值 10、突发 20**（人类登录充裕，bcrypt 在线爆破被压到不可行量级）。
   - 路由指向控制面自身而非租户载体（Target/Process/Port 零值）；进程生命周期内恒定，不参与发布签名（重启即重发）。
   - `ConsoleLoginPath` 常量与 identity.proto Login REST 注解同源，apitest 漂移守卫钉死（常量字面值 + gateway 实服务该路径 + 相近路径不误配）——注解改路径而路由未同批改 = 限速指空路径或主路由吞掉登录，守卫把这类静默化变红。
3. **`capability.Route` 增 `RateLimit` 声明**（`RouteRateLimit{Average, Burst}`；Burst < 均值校验拒绝，fail-closed）；traefik 渲染层受管中间件子集从仅 forwardAuth 扩为**第二种受管形态 rateLimit**（窗恒 1m；P9 预检与 golden 同批钉死 console 双路由发布形态）。其余中间件形态仍不入受管面。
4. **公开暴露边界（诚实陈述，非新增认证面）**：console 主机前置的是**全量 gateway**——非 PUBLIC 动词仍需 Token（授权语义零变化）；PUBLIC 面暴露 = 登录端点（限速）+ 只读目录端点等既有 PUBLIC 集合。9080（gRPC）与 9082（proxy config）暴露面不变；gateway 绑址不动（仍 VPC-only，公网只经 traefik）。identity.go 的多租户前诚实边界记档由本决策兑现限速半边，其余半边（逐 IP 限速细调/锁定策略）挂账多租户批。
5. **ACME CA 目录可配置**：`FLEETLY_PROXY_ACME_CA_SERVER` env 覆盖受管 traefik 的 `--certificatesresolvers.le.acme.caserver`；**空 = 缺省 staging CA（字节不变）**——生产 CA 缺省化仍留安装引导批（2026-09-30 裁决不改），staging 现网经 env 显式切生产 CA 收浏览器告警（放开是显式动作）。存量 staging CA 证书到续期窗自然换发；如需立即换发，runbook 记清 ACME 卷操作序。
6. **DNS 零动作**：`console.dev.fleetly.run` 落在既有 `*.dev.fleetly.run` 泛解析内。

## 不变量

- CONTEXT.md 词汇零改动；proto/fleetly 公共 API 零改动（只动 internal/config 配置段）。
- Console 静态面与 REST 行为零变化（`internal/console.Mount` 不动）；未配置 host 的部署零发布零行为差（升级零扰动）。
- 9080/9082 暴露面与绑址面零变化；端口矩阵的唯一公网入口仍是受管 traefik 80/443。
- 限速只挂登录路由——SPA 静态面与工作台轮询路径不设窗（避免误伤轮询工作台）。

## 验收锚

- [x] `config.console` 三键 + env 桥 + engine `ConsoleConfig` 装配直通（7fe3034）。
- [x] `capability.RouteRateLimit` + `ValidateRouteRateLimit` + traefik `rateLimit` 受管中间件 + golden 钉死 console 双路由形态 + 预检语料补限速案例（67ecf44）。
- [x] engine 合成双路由 + 三态测试（装配/未配置/畸形 gateway_url）+ publishRoutes 合并与签名短路不回归（c790b9a）。
- [x] `ConsoleLoginPath` 漂移守卫三锚（常量字面值 / gateway 实服务该路径 / 相近路径 404）（c790b9a）。
- [x] ACME CA env 覆盖 + 缺省字节不变双案测试（01a5d38）。
- [ ] staging 换装：drop-in `console.conf` + `FLEETLY_PROXY_ACME_CA_SERVER` 切生产 CA → `https://console.dev.fleetly.run` 走查（生产 CA 证书 + 主路由整面 + 登录限速活体）。
- [ ] runbook 端口矩阵增补（80/443 行 + 9081 行注）与 dated 记录。
