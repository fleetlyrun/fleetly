# ADR-0036: 四面绑址可配置与端口暴露自证

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-03 | N1 独立审查"安全止血面"（docs/reviews/2026-10-03-n1-independent-review.md）、ADR-0019 附录 B（zot 受管形态）、runbook 端口暴露矩阵（docs/runbooks/staging-fleetly.md）、ADR-0035（API 面行级授权）、ADR-0021（技术栈基线） |

## 背景

runbook"端口暴露矩阵（操作者责任）"把 9080/9081/9082/5000 全部钉在"仅 VPC/内网可达"上，防线只有云防火墙——操作者漏配安全组即公网裸奔：9082（Edge config 拉取端点，traefik HTTP provider）无认证，公网可达 = 任意人可改写全量路由；5000（受管 zot）HTTP 明文 + 单一平台凭证，公网可达 = 任何持凭证者（凭证全域可读，见下）可拉推镜像。当前四面绑址的可配置面参差：gRPC（9080）/gateway（9081）已有 `server.grpc.addr`/`server.http.addr`；9082 是 assembly 硬编码常量 `:9082`；zot 的引用地址只走 env 旧通道（`FLEETLY_REGISTRY_ADDR`，install.sh 物化进 unit）。且平台对自身暴露面零自证——"防火墙配没配对"只能靠人工 curl，doctor 对此失明。

## 决策

1. **四面绑址全部可配置**（proto 是唯一契约源，缺省 = 现状，行为兼容）：
   - `server.grpc.addr` / `server.http.addr`：既有字段（缺省 `:9080`/`:9081`），本 ADR 确认其为绑面收窄的唯一正规通道并入验收；
   - `server.edge_config.addr`（**新增**）：Edge config 拉取端点监听地址，缺省 `:9082` = 现状；装配侧消费透传到 `NewEdgeConfigServer`；
   - `registry.addr`（**新增**）：受管仓库引用地址（含端口，如 `10.124.0.3:5000`），空值 = 受管仓库停用 = 现状（未设 env 时 build 源部署在 prepare 精确失败，ADR-0019 附录 B.5①）。装配侧经 ctx 注入 zot 工厂；**config 值优先**，env `FLEETLY_REGISTRY_ADDR` 保持同键旧通道兜底（env 键即 config 键大写化，`registry.addr`→`FLEETLY_REGISTRY_ADDR`，install.sh 物化零破坏）。
   - 四面均可钉内网/VPC 地址或回环；收窄是显式动作，缺省不静默改绑（升级不留暗面）。
2. **doctor 端口暴露自证**（探针接缝注入，hermetic 可测；地址来源 = doctor 旗标，env `FLEETLY_EDGE_CONFIG_ENDPOINT`/`FLEETLY_REGISTRY_ADDR` 兜底——与 daemon unit 注入的同一组 env）：
   - **exposure 检查两条**：对 edge config 端点与受管仓库的配置地址做主机分类（回环/私网 RFC1918+CGNAT+链路本地+ULA/通配/公网或域名）+ TCP 拨号探测。**公网可达：edge config → fail**（无认证端点，暴露即全量路由可改写）、**registry → warn**（明文 + 单一平台凭证）；私网/回环可达 → ok（边界声明为云防火墙）；**公网地址不可达 → ok（self-certified）**——"自证不可达"是本检查的值；未配置 → ok（面对应停用，无暴露面）；通配地址 → warn（无法自证，钉定具体地址后才有自证能力）。
   - **bind surface 汇报一条**：汇报三面（gRPC/gateway/edge config）生效绑址，`0.0.0.0`/`::`/`:port` 通配绑定显式 warn + 处置建议（钉 config 键或防火墙兜底）。缺省配置即通配绑定——doctor 如实呈报这一事实，不粉饰。
3. **per-Project 凭证 / Edge 前置认证推迟 N2**（裁决记录，非本批实现）：小微团队单租户是主形态；API 面已由 ADR-0035 行级授权保护，zot 凭证全域可读的实害窗口是多租户；zot 暴露本质是网络层问题——绑面收窄 + 云防火墙即消除公网面。N2 多租户前必须按租户隔离凭证或经 Edge 前置认证（runbook 已知边界 1 的兑现批，不静默升级）。
4. **诚实边界（记档不遮掩）**：swarm routing mesh 发布（`PortConfig`）无宿主 IP 原语——zot 5000 的宿主侧通配发布是编排器能力缺口，v1 以 config 引用地址钉定 + doctor 自证 + 云防火墙承担；宿主 IP 级发布原语挂账 Runtime 端口抽象（N4 换 Runtime 时重估）。doctor 从 CLI 侧只能探测"配置的地址"，读不到 daemon 内存态——旗标缺省值 = 文档缺省值，操作者钉绑后应把同值传给 doctor（runbook 给出操作序）。

## 后果

- 不配置任何新字段 = 现行为逐位一致（9082 仍 `:9082`，zot 仍纯 env 驱动）；staging 现网零漂移。
- 操作者可获得"平台可配置 + doctor 可自证"的绑面收窄：钉私网地址后 doctor 给出 ok 证据链，防火墙失配从"人工核对"降为"一条命令"。
- doctor 缺省输出新增 bind surface warn（缺省即通配绑定的如实呈报）——warn 不改退出码，golden 双形态随批再生成。
- zot 工厂签名不变（ctx 注入），capability 包新增两个 ctx 助手（叶子层，无新依赖方向）。
- 多租户前 per-Project 凭证仍是敞口（已知边界 1 维持挂账），N2 兑现批不得跳过。

## 验收锚

- [x] `server.edge_config.addr` 缺省 `:9082` = 现状；显式值透传到 Edge 拉取端点监听（config 解析测试钉死缺省与透传）（N1 收尾批 0c8b9ce）
- [x] `registry.addr` 缺省空 = 受管仓库停用（现状）；显式值经装配 ctx 注入 zot 工厂且优先于 env `FLEETLY_REGISTRY_ADDR`；env 旧通道单独可用（zot 工厂测试钉死三态）（0c8b9ce）
- [x] doctor exposure 检查：公网可达 edge config → fail、registry → warn；私网/回环可达 → ok；公网不可达 → ok（self-certified）；未配置 → ok（not configured）；通配 → warn（unable to self-certify）（0c8b9ce：分类与探测分支纯函数 + 注入探针单测）
- [x] doctor bind surface：`0.0.0.0`/`::`/`:port` 通配绑定显式 warn + 处置建议；全部钉定 → ok（pinned）（0c8b9ce）
- [x] doctor 探测 hermetic：地址分类与探测分支为纯函数 + 注入探针，单测不拨真网（Windows 本机确定性）（0c8b9ce）
- [x] golden 双形态（doctor/doctor-json）全量再生成，仅新增检查行；runbook 端口矩阵同步"绑面配置化"说明与配置示例（0c8b9ce + runbook 端口矩阵节）
- [x] mise run test + mise run lint 全绿（0c8b9ce 随批全门禁绿）

## N2 兑现（2026-10-04，F2.1 批）

决策 3 的两项推迟承诺在本批部分兑现，形态裁决如下：

1. **Edge 前置认证 = 共享令牌头（已落地）**：`server.edge_config.auth_token`
   （config 新字段，空 = 无认证现状）。非空时拉取端点要求
   `X-Fleetly-Edge-Token` 头常量时间比对（traefik http provider 原生
   `--providers.http.headers` 通道——proto 旧注释"traefik 不支持凭证形态"
   系误记，已勘正）；受管 traefik 命令随 token 增同名头。**9082 Unix
   socket 形态否决**：traefik http provider 无 unix scheme 支持，容器内
   挂 socket 还要额外卷面——令牌头是原生能力零新机制。缺省关闭（收窄是
   显式动作、升级零扰动）；**多租户启用前置项 = 置值**（doctor bind
   surface 面提示随 Console 批）。
2. **per-Project registry 凭证（设计定稿，实现排 N2b 初）**：凭证域隔离
   的完整形态 = 镜像仓布局带 Project 前缀（`<addr>/<projectID>/<appID>`，
   LocalImageRef 唯一真源改公式）+ zot accessControl per-Project 用户
   （`<projectID>/**` 仓库门禁）+ 凭证按 Project 铸造分发（capability
   Registry 增可选 per-Project 端点面；zot htpasswd/config 材料随活跃
   Project 集再生成 = 项目创建/删除一次受管滚动，60s stop-grace 数据面
   无扰）。存量迁移：扁平仓旧 digest 引用保持全用户可读（存量暴露不
   扩大不收缩），新内容全走前缀布局。排期理由：涉及 staging 在役 zot
   滚动与 Revision 冻结引用兼容面，与 F2.4/F2.5 同批风险叠加过大；
   多租户启用（Invitation 面开放）前完成即满足决策 3 的"不得跳过"。

验收锚（N2 兑现部分）：
- [x] auth_token 空：端点无认证、traefik 命令逐位不变（升级零扰动断言）
- [x] auth_token 非空：缺失/错值头 → 401（响应体零配置字节）；正确值 → 200 + 快照（常量时间比对）
- [x] config 访问器缺省/显式/nil 链三态钉死；wire 装配透传（config→ctx 唯一契约源 + env 同键兜底）
- [x] per-Project 凭证（N2b 初：仓布局前缀 + accessControl + per-Project 端点面 + staging 迁移实录）
  ——2026-10-04 落地（c8c8360..c7de917 五 commit）：capability 双子面
  （ProjectEndpoints/ProjectScopedMaterials，FacesOf 协商）+ zot per-Project
  凭证（keys/registry-projects/，E28 按面）+ accessControl 门禁
  （`<projectID>/**` 用户门禁、`*` 扁平可读、无 `**` catch-all——
  doublestar 最长匹配实证）+ builds.repo 列存量回退 + reconciler 喂活跃
  集（读失败整组不下发）。staging 迁移实录与验收锚见 runbook
  2026-10-04 记录·二（含真机咬出的首构建竞速：API Kick + 推送 401
  有界退避双修）。
