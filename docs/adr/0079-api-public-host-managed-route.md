# ADR-0079: 平台 API（gRPC 控制面）公网受管发布（api 主机路由 + 登录 method 限速 + CLI/SDK grpcs://）

日期：2026-10-16；状态：已采纳（用户裁定 CLI 公网直连需求——「CLI 连接的端口也需要对公网开放，不然 fleetly CLI 无法从公网连接」；AGENTS.md CLI 本地优先裁定后 SSH 隧道 recipe 是唯一公网通道，本批退役该 workaround）。

## 背景

- AGENTS.md 2026-10-16 裁定：CLI 一律本地执行，`FLEETLY_ADDR`/`FLEETLY_TOKEN` 指向目标控制面——用户一般使用场景不假设有服务器 SSH 权限。但 9080（fleetlyd gRPC）仅 VPC/回环可达，公网用户只能 SSH LocalForward 跳板，与裁定精神相悖。
- CLI 面已核实为**纯 gRPC**：uploads（`Builds.UploadSource` 流式）/exec（gRPC 流）全在 9080，无 websocket、无从 addr 派生的 HTTP 面——一条 Host 路由即整面。
- ADR-0061 已立「平台自身公网发布 = engine 合成受管路由（非 Route 表行）」先例（console → REST gateway 9081）；traefik 渲染层 h2c scheme 与 path 白名单现成，staging 的 tw-grpc/ml-grpc 用户路由已实证 gRPC-over-traefik（TLS termination + h2c 后端）。
- SDK 侧 `fleetly.Dial` 硬编码 insecure，注释已预留本批形态：「TLS 形态随证书体系批次提供 WithDialOptions 注入（grpc:// 与 grpcs:// scheme，架构文档 §1）」。`FLEETLY_ADDR` 带任何 scheme 今天直接不可用（无注册 resolver → DNS 解析失败）。
- 既有安全事实：CLI `login` 支持密码形态走 gRPC `fleetly.identity.v1.UsersService/Login`——公网 gRPC 面若无限速，即是绕过 console REST 登录限速（ADR-0061 决策 3）的爆破通道。

## 决策

1. **config 新增 `api` 发布面三键**（internal/config/config.proto；env `FLEETLY_API_HOST` / `FLEETLY_API_GRPC_URL` / `FLEETLY_API_TLS`）：`host` 是 API 公网主机名（如 api.dev.fleetly.run），`grpc_url` 是 traefik 容器可达的 fleetlyd gRPC 基址（`console.gateway_url` 同文化——traefik 容器能拉到的地址即正确地址），`tls` 是路由 TLS 模式（空值随渲染层视为 auto）。**host 空 = 整面停用**（registry.addr/browse.host_suffix/console.host 同文化：收窄是显式动作，升级零扰动）。
2. **engine 合成双路由**（`apiCapabilityRoutes`，publishRoutes 合并面，console 同款）：
   - **主路由 Host-only + h2c** → `grpc_url` 的 host:port——CLI/SDK 从公网经 TLS 直连的整面通道；
   - **登录路由 `Host && PathPrefix(/fleetly.identity.v1.UsersService/Login)`** 带 rateLimit（gRPC method 路径含点号，path 白名单纵深面天然可过）——限速值**复用 ADR-0061 决策 3 的常量**（每源 IP 窗恒 1 分钟、均值 10、突发 20）：同一后端方法的两个公网入口（REST /v1/auth/login 与 gRPC method）必须同姿态，否则 gRPC 面是限速绕道。
   - 路由指向控制面自身而非租户载体（Target/Process/Port 零值）；进程生命周期内恒定，不参与发布签名（重启即重发）。
   - `APILoginMethodPath` 常量与 identity.proto UsersService.Login 的 gRPC method 路径同源，apitest 漂移守卫锚到生成码 `UsersService_ServiceDesc`（proto 改包名/服务名/方法名即红）。
3. **SDK 地址 scheme 感知**（`fleetly.Dial`，架构文档 §1 预留约定兑现）：`grpcs://host:port` 剥前缀 + `credentials.NewTLS(nil)`（系统根；公网证书由受管 traefik ACME 签发）；`grpc://host:port` 剥前缀显式明文；**无 scheme = 现状明文缺省**（本机/内网直连，一切既有消费零变化）；其余形态原样透传 grpc 原生解析（保留 unix:/// 等）。CLI 零代码改动（`dialClient` 接缝全收口），`--addr` 帮助文案补 scheme 说明；凭据文件 addr 键继续仅记录不参与拨号（防不可信文件重定向 endpoint 的既有裁决不动）。
4. **公开暴露边界（诚实陈述，显式改判 ADR-0061 决策 4 的「9080 暴露面不变」）**：api 主机前置的是**全量 gRPC 控制面**——非 PUBLIC 动词仍需 Token（authn 拦截链语义零变化）；Token 猜测面在 128-bit 量级不可行；**除 Login method 外无限速**（与 9081 非 login 路径同姿态——CLI 单命令即可多次 RPC，整面限速会误伤正常使用）。9080 绑址不动（仍 VPC-only，公网只经 traefik TLS termination）；9082 暴露面不变；端口矩阵唯一公网入口仍是受管 traefik 80/443。
5. **Provider 侧零改动**：traefik 渲染层 h2c scheme、rateLimit 受管中间件、path 白名单（含点号）全部现成；golden 增 h2c+rateLimit 组合路由钉死该形态（gRPC method 路径顺带锚纵深面）。
6. **DNS 零动作**：`api.dev.fleetly.run` 落在既有 `*.dev.fleetly.run` 泛解析内。

## 不变量

- CONTEXT.md 词汇零改动；proto/fleetly 公共 API 零改动（只动 internal/config 配置段与 SDK）。
- 未配置 host 的部署零发布零行为差（升级零扰动）；VPC/回环直连形态（现役一切内部消费）零变化。
- 9080 绑址与 9082 暴露面零变化；限速只挂 Login method——其余 method 不设窗。
- 凭据文件 addr 不参与拨号的既有安全裁决不变。

## 验收锚

- [x] `config.api` 三键 + env 桥 + engine `ApiConfig` 装配直通。
- [x] engine 合成双路由 + 三态测试（装配/未配置/畸形 grpc_url）+ publishRoutes 合并与签名短路不回归。
- [x] `APILoginMethodPath` 漂移守卫（常量字面值 + 生成码 ServiceDesc 双锚）。
- [x] traefik Provider golden 增 h2c+rateLimit 组合路由（gRPC method 路径形态）。
- [x] SDK scheme 行为测试：grpcs:// 对自签证书服务器以 x509 校验失败（TLS 传输确在验证书）/ grpc:// 剥前缀明文往返 / 裸 addr 现状不变。
- [x] staging 换装：drop-in `api.conf` → api.dev.fleetly.run 证书签发 → 本机 `FLEETLY_ADDR=grpcs://api.dev.fleetly.run` 全动词直连 → 登录限速活体（错密码连打超限 429 形态）。
- [x] runbook 端口矩阵 9080 行增注与 dated 记录。
