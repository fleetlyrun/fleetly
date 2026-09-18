# fleetly console

Console 端 = React SPA，经 REST API（`/v1`，Bearer 鉴权）消费平台，无任何
旁路调用。T2.21 落地。

## 技术形态

| 层 | 选型 |
| --- | --- |
| 构建 | Vite 8 + TypeScript 5.9（strict） |
| UI | React 19 + Tailwind CSS v4 + shadcn/ui（组件源码入库 `src/components/ui/`）+ lucide-react |
| 路由 | react-router（BrowserRouter，basename `/ui`） |
| 服务端状态 | @tanstack/react-query |
| 流式 | fetch + ReadableStream 消费 gateway 的 NDJSON 流（日志跟随 / 事件注视） |
| 包管理 | pnpm（`pnpm-lock.yaml` 入库） |

## 目录结构

```
console/
  src/
    api/            # 数据面唯一入口
      client.ts     #   fetch + Bearer + 错误信封解析 + 401 全局处理
      errors.ts     #   ErrorEnvelope / ApiError（snake_case 七字段信封）
      endpoints.ts  #   端点封装（页面只经此消费 REST 面）
      stream.ts     #   NDJSON 解析器（断帧拼接）+ 流消费
      streams.ts    #   followLogs / watchEvents
      stream-types.ts # 流帧类型（{"entry":…} / {"event":…|{"cursor_expired":…}）
      types.ts      #   proto 投影类型（UseProtoNames → snake_case）
    pages/          # 登录 / 应用列表 / 详情（概览·部署·日志·env·域名）/ 系统 / 事件
    components/     # 壳布局 + StateBadge（状态色语义）+ EnvelopeAlert（信封渲染）+ shadcn ui
    auth.tsx        # token 登录态（localStorage 持久）
  tests → src/**/*.test.{ts,tsx}（vitest + @testing-library/react）
```

## 页面 → 端点映射

| 页面 | 端点（proto/fleetly/server/v1 派生） |
| --- | --- |
| 登录（凭据校验） | GET /v1/apps |
| 应用列表 | GET /v1/apps |
| 概览 | GET /v1/apps/{app}、GET /v1/apps/{app}/placement |
| 部署（动作+历史+回滚） | POST /v1/apps/{app}/deployments（compose bytes=base64）、GET /v1/apps/{app}/deployments、GET /v1/deployments/{id}（轮询至终态）、POST /v1/deployments/{id}/cancel、POST /v1/apps/{app}/rollbacks、GET /v1/apps/{app}/revisions |
| 日志 | GET /v1/apps/{app}/logs/stream（NDJSON 跟随）、GET /v1/apps/{app}/logs（历史：时间窗/limit/source） |
| env | GET/PUT/DELETE /v1/apps/{app}/env[/{key}]（pending 分组独立可见；GET 明文=admin 面） |
| 域名 | GET /v1/apps/{app}/domains、POST /v1/apps/{app}/domains/verify |
| 系统 | GET /v1/system/status、/v1/system/nodes、/v1/system/ingress |
| 事件 | GET /v1/events/stream（NDJSON，`since_seq` 游标续读 + cursor_expired 重同步） |

## 部署形态

- **生产**：`pnpm build` → `dist/`（base=`/ui/`）。daemon 配置
  `console.static_dir: "./console/dist"` 后由网关在 `/ui/` 前缀托管（静态
  资源不要求 token；SPA 深链回退 index.html）。数据面同源 `/v1`（全部
  Bearer 鉴权），或构建时 `VITE_API_BASE` 指向其他控制面。
- **开发**：`pnpm dev` → http://127.0.0.1:5173/ui/ 。`/v1` 经 Vite dev
  proxy 转发到 127.0.0.1:8420（网关无 CORS 头，代理是开发态跨源规避的
  唯一手段），需本机有 fleetlyd 在跑。

## 命令

```sh
pnpm install        # 安装依赖（lockfile 权威）
pnpm dev            # 开发态（含 /v1 代理）
pnpm build          # tsc --noEmit && vite build → dist/
pnpm lint           # eslint
pnpm typecheck      # tsc --noEmit
pnpm test           # vitest run（stubbed fetch，不起真服务）
```

## 语义约定

- **状态色**：running/succeeded=绿、degraded=琥珀、blocked/failed/down=红、
  中间态（queued/preparing/building/releasing/observing/blocked_waiting）=
  中性蓝/灰。见 `src/components/state-badge.tsx`。
- **错误信封**：所有 HTTP 错误与失败部署行经 `EnvelopeAlert` 渲染
  code + message + suggestion + docs（不做裸 toast）。
- **env pending**：Set/Remove 均置 pending（随下次部署生效），「待生效」
  与 effective 分组分开呈现；值恒脱敏，展开才取明文（admin）。
- **断线续读**：日志以最后时间戳回放历史后重开跟随流（FollowLogs 契约无
  游标）；事件以 seq 游标续读，`cursor_expired` 帧触发重同步。
