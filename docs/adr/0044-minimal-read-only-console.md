# ADR-0044: 最小只读 Console——消费面、生成链与 embed 静态形态

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-05 | 架构文档 §总览/§7/§12（Console 钉形三条）、技术栈 Console 行、ADR-0026（SSE 票据 + after_* 惯例）、ADR-0040（日志双径）、F2.6（checklist）、F3.1/F3.5（后续写面） |

## 背景

F2.6 落地最小只读 Console（部署状态/日志/事件流三页）。架构文档已钉三条：React SPA 构建产物
embed 进 `fleetlyd`、**只消费公共 REST/WS API、无私有服务端面**（旧 P0-3 教训）；proto 单源
生成 TS 客户端类型 + schema 漂移门禁；技术栈钉 React 19 + Vite + TS + Tailwind 4 + Radix +
TanStack Query + xterm（node 24 / pnpm，openapi-typescript 生成链）。本 ADR 裁决落地留白：
消费面盘点结论、刷新形态（轮询 vs 流）、路由/凭证形态、embed 服务挂法、生成链的悬空 $ref
处置与漂移门禁口径。

## 决策

1. **三页消费面盘点 = 零 proto 改动**（以现网注解面为准，逐一核实）：
   - **部署状态页**：`GET /v1/projects` + `GET /v1/apps?project_id=` + `GET
     /v1/deployments?app_id=&limit=`（全部既有注解面；**部署列表是 per-App 轴**——
     app_id 服务端必填（行级授权锚），页面 App 必选、目录到达自动选首个，不提供
     跨 App 聚合视图——聚合面若将来需要是 API 面的事，不是 Console 私有服务端面）。
     刷新形态 = TanStack Query 轮询 5s。不选 `GET /v1/deployments/{id}/wait` 流：
     列表页并发观察多个部署，每行一条流的管理成本不抵收益；wait 流留给写面前批
     （F3.1）做单部署跟踪。
   - **日志页**：`GET /v1/logs`（StreamLogs）REST 形态**核实存在**——grpc-gateway 对
     server-streaming 注解面走 ForwardResponseStream：chunked 响应、每帧一个 protojson
     （snake_case）对象 + 换行分隔（NDJSON）。EventSource 不能带 Authorization 头，但本
     面用 `fetch` ReadableStream 消费（可带 Bearer 头），**无缺口、不补注解面**。查询面
     = app_id + process + tail_lines + follow + text（ADR-0040 双径：无 text 实时路径 /
     有 text 检索路径，页面上同一过滤器透传）。
   - **事件流页**：补档 `GET /v1/events?after_seq=&limit=` + 订阅 `POST /v1/events/ticket`
     换票 → EventSource `GET /v1/events/follow?ticket=&after_seq=`（ADR-0026 钉面：
     EventSource 无自定义头，凭证经秒级单用途票据）。断档 410 → 以 `GET /v1/events/status`
     重同步游标后重订阅（页内自动，无需人工干预）。
2. **形态**：React 19 + Vite + TS + Tailwind 4 + TanStack Query。**Radix 与 xterm 本批不进**
   （三只读页无交互组件/终端需求，依赖面为零更好；F3.1 写面与终端页时引入）。**路由 = 手写
   hash 路由**（`#/deployments` `#/logs` `#/events` 三路由约 30 行，不引路由库——最小面
   原则；N3 全功能若需 URL 参数/嵌套路由再升格）。**凭证 = 页头 Token 输入框**（Bearer，
   localStorage 持久化，401 时各页错误态呈现；无登录页——账号交互面是 F3.1 的事）。
   protojson 口径遵循全仓约定：snake_case 字段名、int64 为字符串（运行时 `Number()` 归一）。
3. **embed 静态面**：vite 构建产物输出 `internal/console/dist`（**提交进仓**，纪律同
   genproto：生成物 committed + 漂移门禁），`internal/console` 包 `go:embed all:dist`
   提供纯静态 handler，assembly 挂在 root mux：非 `/v1/*` 且非既有原生挂载路径 → 静态
   文件；未命中的非 `/v1/*` 路径 → `index.html`（SPA fallback）。hashed asset
   `Cache-Control: immutable`、`index.html` `no-cache`。**零私有服务端面**：console 无
   任何专属端点/CGI/注入，静态字节与公共 REST 完全解耦（架构钉形）。`/v1/*` 行为零变化
   （既有 REST 冒烟回归锚）。开发形态：`pnpm dev` 的 vite dev server 代理 `/v1` →
   `http://localhost:9081`，fleetlyd 不感知开发面。
4. **TS 类型生成链 + 漂移门禁**：`openapi-typescript` 从 `genproto/fleetly/**/**.swagger.json`
   生成 `console/src/api/<context>.ts`（逐文件镜像，消费哪些上下文就生成哪些——本批
   structure/delivery/telemetry 三件）。**悬空 $ref 处置**：openapiv2 插件把
   `responses.default` 渲染为外部形态 `$ref: ".fleetly.shared.v1.ErrorResponse"`（definitions
   为空、仓内无该 target 文件），生成脚本先做确定性预处理——把该 ref 改写为本文件内
   definitions 注入的 ErrorResponse 形态（手写最小信封：code/message），proto 与 swagger
   生成物不动。**漂移门禁 = CI console job**：`pnpm install --frozen-lockfile` → `pnpm
   gen`（类型再生成）→ `tsc` → `vite build` → `git diff --exit-code`（src/api 类型 +
   internal/console/dist 产物双钉）。门禁口径与 backend job 的 codegen 漂移本地门禁互补
   （后端生成物漂移在本地 generate:verify，前端两面在 CI——node 工具链进 CI 后冷环境
   可复现）。
5. **工具链钉版**：node 24.21.0 / pnpm 12.9.1 入 `mise.toml [tools]`（CI mise-action 同
   源安装，本地 mise install 同版本——三面一致，torchwood 同款模式）。CI 从 6 job 变
   7 job（+console），收官注记写明。
6. **词汇面**：前端源码进禁词扫描面（`console/src/**` 的 .ts/.tsx/.html——用户可见英文
   同为词汇冻结契约面；`console/dist`、`node_modules`、`pnpm-lock.yaml` 是生成物/锁文件
   不扫）。注释中文、用户可见文本英文，前端同律。

## 后果

- staging 换装后 Console 随 fleetlyd 同端口（:9081）可直接访问；无独立部署面、无新端口。
- dist 提交进仓使 review diff 含构建产物（噪音换确定性——与 genproto 同款取舍）；再生成
  必须 `pnpm gen && pnpm build` 后与源变更同 commit（生成物纪律）。
- F3.1 写面引入 Radix/路由库/登录页时，本 ADR 的最小形态裁决不构成阻碍（手写 hash 路由
  可整体替换）；TS 类型生成链与漂移门禁直接沿用。
- vite 构建确定性是 dist 漂移门禁的前提（同输入同版本同输出）；若上游引入非确定性再收窄
  门禁面（届时以注记收口）。

## 验收锚

- [x] fleetlyd 静态服务 Console：`GET /` 返回 index.html（200、text/html）、SPA fallback
  （未知非 /v1 路径回 index.html）、hashed asset 带 immutable 缓存头（httptest 级钉死）
  （internal/console/console_test.go 六件：TestRootServesIndexHTML/TestSPAFallbackServesIndexHTML/
  TestHashedAssetImmutableCache/TestIndexHTMLDirectIsNoCache/TestMountPassesV1Through/
  TestNonGetIs405）
- [x] `/v1/*` 行为零变化：既有 REST 冒烟（identity 面/错误信封/未带凭证 401）不红；未知
  `/v1/*` 路径仍是 gateway 404 而非 SPA fallback（internal/apitest/console_static_test.go：
  未知 /v1/* JSON 404 + 非 html 断言 + whoami 照常；既有 rest_gateway_test 全绿同批）
- [x] 三页只消费公共 API：代码面零 console 专属服务端逻辑（embed 包纯静态、无任何 fetch
  处理器）；事件订阅走票据（无自定义头面——console fetch 解帧消费，服务端契约对
  EventSource 兼容性由既有 events_sse_test 钉死）（/v1/logs NDJSON 帧契约进
  internal/apitest/rest_logs_test.go——含未带凭证 401）
- [x] `pnpm gen` 再生成零漂移：openapi-typescript 从 genproto swagger 再生成与提交的
  `console/src/api/*.ts` 逐字节一致（CI console job 断言；本地连跑两次 md5 一致实证）
- [x] dist 再构建零漂移：`pnpm build` 产物与提交的 `internal/console/dist` 一致（CI
  console job 断言；连续两次本地构建一致实证后才进门禁）
- [x] node/pnpm 与 CI 同版本（mise.toml 单源，CI 经 mise-action 安装；mise run
  console:verify 本地与 CI 同口径）
