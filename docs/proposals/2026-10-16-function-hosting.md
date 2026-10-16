# torchwood Functions 托管提案（原生托管 face，2026-10-16）

> 状态：**待裁决**（本文件即裁决载体——§0 总表逐案给裁决，开放问题逐条给答；裁决后实施项各自开 ADR 并携带守卫任务，按 2026-10-03 提案集同款纪律）
> 来源：torchwood Functions v4 重构（其设计文档 `docs/design/functions-v4.md`，2026-10-15 owner 拍板 D2「原生托管 face 实现后置、提案先行」）——torchwood 侧已完成执行面外包：函数 = endpoint 常驻服务，今天已经可以纯用 fleetly 现有面（CLI 部署 App + 注册 URL）承载，**本提案不是 torchwood 运转的前置依赖**；要裁决的是「fleetly 要不要把这类形态做成一等托管能力，以及做到哪一层」
> 词汇遵守 CONTEXT.md：`Function(s)` 仅作 **torchwood 产品实体名**按第三方豁免引用（同 GitHub Organization 先例——外部专有词汇按本义使用）；fleetly 域内**不新立 function 实体、不引入该词进域内语义**（Task 词条 Avoid 表明载）。本提案在 fleetly 侧讨论的承载形态全部用既有词汇：App / Process / Task（resident 形）/ Template / Route / Revision / Deployment / Run / Secret / Variable。同理 `InvokeJSON`、`twx_`、`X-Tw-Signature` 等 torchwood 协议专名按第三方本义引用
> 关联：torchwood 侧接口现状 `internal/domain/functions/protocol.go`（调用协议单一事实源）与双 FDK（`sdk/go/functions`、`sdk/typescript/functions`，验签 + 双轴分发参考实现）；fleetly 侧现状 `proto/fleetly/{structure,delivery,automation}/v1/`

## 0. 提案总表

| # | 提案 | 重量 | 建议批次 | 裁决（fleetly review 填写） |
|---|---|---|---|---|
| F1 | 「FDK 函数」Template 进平台模板目录（零新 proto） | 小 | 任一近批 | 待裁决 |
| F2 | resident Task 形态作为「无公网 Route、内网调用」变体的文档化与守卫 | 小 | 随 F1 | 待裁决 |
| F3 | App Process 级 warm 池 / scale-to-zero（期望温实例数 + 空闲缩容） | **大** | 独立项，触发条件见 §5 | 待裁决 |
| F4 | 函数托管的状态/日志/指标投影面（torchwood Console 透出） | 中 | F1 落地后按需 | 待裁决 |
| 前提 | 协议纪律（§2）——不是选项，是 F1–F4 全部提案的生效前提 | — | — | **确认即生效** |

## 1. 背景与动机

### 1.1 torchwood 侧现状（v4 终态）

torchwood Functions v4 把「服务端逻辑」的执行位置外包：函数 = **endpoint 常驻服务**（跑 torchwood FDK 的进程，双轴调用协议：RPC 轴 JSON-RPC 2.0 `invoke`/`http.request`，事件轴 CloudEvents 1.0 `cron.tick`/事件目录名；`X-Tw-Signature` HMAC 签名 + `X-Tw-Protocol-Version` 版本头）。torchwood 平台只保留路由与身份层（触发器调度、`twx_` 执行身份、幂等/限频/记账），**不再持有任何构建/镜像/容器编排能力**。

对 fleetly 的意义：torchwood 的函数托管需求 = 「帮我把一个跑 FDK 的镜像变成有 URL、有变量注入、有健康检查的长运行 HTTP 服务」。这在 fleetly 现有能力面上的映射是直接的：

| torchwood 需要 | fleetly 现成面 |
|---|---|
| 从源码/镜像产出可运行产物 | Source（Git/镜像引用/上传产物）→ Build → 不可变 digest |
| 长运行 HTTP 服务 + 稳定 URL | App（单 Process）+ Route（`host/path → Process 端口`，http/h2c + TLS） |
| 签名密钥/配置下发 | Secret（加密存储、注入 env、值永不回显）+ AppVariable |
| 换版/回滚/健康门 | Deployment（rolling，Revision 冻结） |
| 常驻保温/扩缩 | （缺口，见 F3）App 面现无期望副本/温实例语义 |
| 无公网、仅内网被调 | 变体：Task resident 形（稳定 DNS、无 Route）或 App + `egress:none` 网络（见 F2） |

### 1.2 为什么值得做成一等能力（fleetly 产品视角）

1. **模板目录生态位**：「跑一个标准 FDK 函数」是 Template 机制的教科书用例（变量声明 + compose 受控子集 + Route），把 torchwood 生态的开发者直接导入 fleetly 模板目录。
2. **F3 的收益外溢**：warm 池/scale-to-zero 一旦做在 App Process 级，受益的是**所有**长运行 App（成本敏感的 staging、突发型 web），函数只是第一个消费方。
3. **dogfood 驱动的真实负载**：torchwood 自有函数（微信小游戏服务端逻辑）是长期在服的真实多租户负载，比合成基准更能暴露平台短板。

## 2. 协议纪律（前提，非选项）

以下四条是 F1–F4 的生效前提，fleetly 侧**确认即生效**、不随方案选择变化：

1. **调用协议唯一事实源在 torchwood**：`internal/domain/functions/protocol.go`（method 词表、签名方案 `t.<body>` HMAC-SHA256、`X-Tw-Signature: t=/v1=/v2=` 双钥轮换、±5min 窗、`X-Tw-Protocol-Version`）。fleetly **不定义第二协议、不理解载荷、不做任何协议层处理**——函数流量对 fleetly 是普通 HTTP（Route 已有的 http/h2c 语义）。
2. **调度不进 fleetly**：HTTP 触发路由已由 Route 承载；cron 与事件触发的**调度**在 torchwood 侧（出站调用函数 URL），fleetly `Schedule` 实体**不用于**函数触发——避免同一函数出现双调度源（torchwood ClaimDue 与 fleetly Schedule 各自投递）。
3. **身份/幂等/配额/记账全在 torchwood**：`twx_` 执行身份铸造与校验、客户端幂等键、限频、invocation 记账、计量，均为 torchwood 平台语义；fleetly 只提供进程、URL、变量与观测。
4. **FDK 是协议参考实现**：`sdk/go/functions` / `sdk/typescript/functions` 的 `NewServer`/`newServer`（验签 → 双轴分发）。函数镜像内的进程自携协议栈；fleetly 托管面与协议零耦合。

## 3. F1：「FDK 函数」Template（建议主选项，零新 proto）

**动机**：Template 是 fleetly 的一键部署蓝图机制（变量声明 + compose 受控子集 + 平台扩展键，Instantiate 产出 App 部署与伴生 Secret/Route）——「跑一个标准 FDK 函数」恰好是它的教科书用例，且零新 API。

**设计**：

- 平台模板目录新增 `torchwood-fdk-function`（内嵌 + 刷新快照，ADR-0050 分立条款）：
  - compose 受控子集：单 Process `fn`，镜像 = 模板消费者构建产物引用（或 placeholder 由 Instantiate 时 Source 决定），`x-fleetly-strategy: rolling`、`healthcheck.x-fleetly-http-path: /_tw/health`（FDK 可选探针，GET 不验签）；
  - 变量声明：`TW_API_BASE_URL`（torchwood 平台 API 基址——FDK 回访凭证链的另一半，见 §6）、`TW_FUNCTION_SECRETS`（或逐密钥声明）；
  - 伴生 Secret：`tw-signing-key`（`whsec_` 形态，Instantiate 时生成或消费者覆写）；
  - 平台扩展键：Route 声明（host/path → `fn:18080`）。
- 消费路径：`fleetly templates instantiate`（或 API 等价面）→ App + Route + Secret 一次成型 → 消费者把 Route 的 URL 登记进 torchwood `Function.target.url`（torchwood 侧 `RotateFunctionSecret` 与 fleetly Secret 值保持一致——分发方式见 §6）。
- **免费获得**：Deployment rolling/健康门、Revision 冻结与回滚、AppVariable 合成（ADR-0072 三级）、Drift/Converge、Build Log 脱敏——全部是 App 面既有能力，函数托管零特殊化。

**取舍**：不解决 warm/scale-to-zero（F3）；无「函数」专属投影面（F4）——状态观测走 App 既有读径。重量小的原因是全部机制现成，唯一新产物是模板资产本身。

## 4. F2：resident Task 形态变体（内网调用、无 Route）

**动机**：不是所有函数都需要公网 Route——torchwood 的 cron/event 触发走平台出站调用，若调用方（torchwood server/worker）与函数同处一张任务网，可走 fleetly 内网直连。CONTEXT 中 Task resident 形（Owner Lease、RenewTask、ScaleTask、per-Task 稳定 DNS、Task Network Group internal 变体）已是现成面。

**设计**：文档化一条受支持路径（不新增 API）：函数镜像以 resident Task 部署（`form=resident`）+ Task Network Group（internal 变体）+ torchwood server 经 `EnsureTaskNetwork` members 一次性挂靠同网 → torchwood `target.url` 填 per-Task 稳定 DNS。健康/日志走 Task/Run 既有读径。

**取舍**：Task 无 Route/Revision/回滚语义——换版 = 新 Task + 调 URL（torchwood 侧 `UpdateFunction` 改 target），无蓝绿窗；Owner Lease 需要调用方（torchwood）实现续租心跳，否则宽限即排空。**适用面**：内网专用、对换版窗口不敏感的函数；公网函数一律 F1。本提案要求的是把这条路径**写进文档并加一条 e2e 守卫**（防 resident 面演进时无声破坏），不是新开发。

## 5. F3：App Process 级 warm 池 / scale-to-zero（真正的能力增量）

**动机**：v4 后函数常驻的运行成本 = 副本数 × 常驻内存。dogfood 规模可接受，规模化后（多函数 × 低频调用）需要「保温 N 个温实例 + 空闲缩容 + 请求唤醒」语义。**该能力做在 App Process 级，受益所有长运行 App**（staging 环境、突发型 web），函数只是锚定场景。

**设计方向（供 review，非定稿）**：

- Process 级新语义：`min_warm`（期望温实例下限，缺省 = 现状恒 ≥1）、`idle_scale_to_zero`（bool + idle 窗）。与现有 Process 副本/滚动语义的关系是**本提案最大的开放问题**：swarm 原生 replicas 只表达静态期望数，「0→1 请求唤醒」需要引擎外的请求观测（Proxy 侧连接计数？）与唤醒编排（收到请求时 Ensure 副本 1 并等待健康）——落在 Proxy Capability 与 engine 的职责边界上，正是 fleetly「池语义留给调用方」（tasks.proto 时代注释）vs 平台收编的分界重划。**建议**：若裁决做，开工 ADR 必须先回答「唤醒观测点在 Proxy 还是 Runtime Provider」与「唤醒窗口内请求排队/快速失败的语义」两问。
- torchwood 侧配合：SLA 口径已声明冷启动不在 P99 承诺内（`functions-execution-identity-and-triggers.md` §SLA），scale-to-zero 属「可接受的慢」，无平台侧改造。
- **触发条件建议**（参考 P12 记录不排期先例）：dogfood 函数数 × 常驻成本出现真实压力，或出现首个「想要 scale-to-zero 的非函数 App」需求方。

## 6. 交接面清单（两仓各自职责）

| 面 | torchwood | fleetly |
|---|---|---|
| 调用协议 | 定义 + 演进（`X-Tw-Protocol-Version`） | 零感知（普通 HTTP 流量） |
| 签名密钥分发 | `CreateFunction`/`RotateFunctionSecret` 生成与记账 | Secret 实体持有值 + env 注入（FDK 读 env 传 `NewServer(mux, secret)`）——**两仓各持一半：fleetly 管注入，torchwood 管值的一致性（轮换双钥窗 v1/v2 并存，两侧都要配）** |
| 回访凭证 | `twx_` token 铸造/校验 + `TW_API_BASE_URL` 语义 | env 注入通道（`TW_API_BASE_URL` 由部署侧下发，值 = torchwood server API 基址） |
| 触发 | http/cron/event 三型调度 + 出站调用 | Route（公网入口）/ Task Network Group（内网） |
| 观测 | invocation 记账、业务指标 | App/Process 状态、Build Log、Metrics（PromQL）、Alert Rule |
| 函数元数据登记 | `Function.target.url` 指向 fleetly Route/DNS | 无 torchwood 概念（URI 即耦合点，唯一） |

## 7. 验收锚（实施完成后逐条核）

1. 一个 dogfood 函数（torchwood 真实小游戏服务端逻辑）经 F1 Template 实例化部署，`torchwood functions invoke` 全链路 200（签名验签、`twx_` 回访、invocation 记账三段都在）。
2. 调用路径与「任意 endpoint target」**逐字节一致**——fleetly 托管对 torchwood 不可见（除 URL 值本身）。
3. cron/event 触发经 torchwood 调度出站到 fleetly Route/DNS，投递成功（CloudEvents 信封 + 验签全链）。
4. （若 F4 裁决做）torchwood Console 可见函数托管状态/指标投影。
5. （若 F3 裁决做）scale-to-zero：空闲窗后副本归零、下一请求唤醒成功且唤醒窗内请求行为符合 ADR 定义。

## 8. 开放问题（请 fleetly review 逐条给答）

1. **F1 模板资产归属**：`torchwood-fdk-function` 模板进平台内嵌目录（fleetly 仓维护）还是仅作外部模板快照分发？内嵌即 fleetly 认领了对 torchwood FDK 版本的追随义务（FDK 大版本升级时模板要跟）。
2. **F2 的守卫级别**：文档化 + e2e 守卫是否足够？resident Task 面后续演进（如 Owner Lease 语义收紧）是否需要一条显式「函数托管形态」兼容测试进 CI 常态？
3. **F3 的职责边界**：唤醒观测点放 Proxy（流量侧计数）还是 Runtime Provider（replicas 语义扩展）？这决定它是 Proxy Capability 还是 engine 的增量——建议裁决「做不做」时一并定「归谁」。
4. **F4 投影的最小面**：torchwood Console 要的状态/指标，走「引导消费 fleetly 既有 API」（torchwood 后端聚合 App 状态/PromQL 查询，零 fleetly 新面）还是 fleetly 出专门投影 RPC？前者无 fleetly 工作量但把聚合逻辑放在 torchwood；后者面窄但要定义投影契约。
5. **配额与滥用面**：函数 = 长运行 App，fleetly 现有 per-Project 配额（如有）是否覆盖「大量常驻小进程」形态？是否需要 per-Project App 数/AppVariable 数之外的常驻成本维度？
6. **命名留痕**：本提案在 fleetly 域内未立任何新实体名（F1 是 Template 资产、F2 是 Task 既有形、F3 是 Process 语义增强）——确认无需走 ADR-0007 词汇增补；若 review 中倾向立专门实体（不推荐，见 §4 取舍），命名须避开 Task 词条 Avoid 表（function/job/run/agent/one-off 全禁）。
