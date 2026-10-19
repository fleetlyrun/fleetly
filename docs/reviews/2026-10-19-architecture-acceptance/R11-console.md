# R11 Console 前端 评审报告

> 评审基线：`D:\Codes\qiulin\fleetly` @ `35257f18`（工作树干净，只读评审）。
> 本机实测：`pnpm test` → **30 文件 / 129 用例全绿**（7.61s）。未运行 `console:gen/build/verify`（写仓，禁止），
> 因此 dist / src/api / routeTree.gen.ts 的"零漂移"只做静态核对（见 §7）。

## 1. 覆盖清单

### 完整读过（生产文件）
- 构建与配置：`console/package.json`、`vite.config.ts`、`tsconfig.json`、`index.html`、`src/main.tsx`
- 数据层：`src/api/client.ts`、`src/api/streams.ts`、`src/api/execstream.ts`、`src/api/gen.mjs`、`src/lib/catalog.ts`、`src/lib/token.ts`、`src/lib/project.ts`、`src/lib/api-errors.ts`、`src/lib/use-api-mutation.ts`、`src/lib/spec-deploy.ts`、`src/lib/redeploy.ts`、`src/lib/deployPayload.ts`、`src/lib/format.ts`、`src/lib/legacy-hash.ts`、`src/lib/dokploy.ts`、`src/lib/tar.ts`、`src/lib/execFrames.ts`
- 路由与壳：`src/routes/__root.tsx`、`_shell.tsx`、`login.tsx`、`index.tsx`、`_shell/overview.tsx`、`_shell/quickstart.tsx`、`_shell/events.tsx`、`_shell/settings.tsx`、`_shell/github-apps.connect.tsx`、`_shell/p.$projectId.index.tsx`、`_shell/p.$projectId.tsx`、`_shell/p.$projectId.apps.$appId.tsx`、`_shell/p.$projectId.apps.$appId.index.tsx`、`_shell/p.$projectId.apps.$appId.deployments.index.tsx`、`components/shell/login-bridge.tsx`、`components/shell/route-fallback.tsx`、`components/shell/app-sidebar.tsx`（导航段）
- 组件/模板：`components/domain/{status-badge,list-page,data-table}.tsx`、`components/ui/chart.tsx`（风格注入段）
- 特性：`features/deployments/{hooks,deployment-detail,deploy-sheet}.tsx`、`features/apps-tabs/{app-overview,app-processes,app-spec,app-variables,app-settings,exec-terminal}.tsx`、`features/projects/project-overview.tsx`、`features/spec/use-app-specs.ts`、`features/fleet/{freshness,use-system-status}.ts`、`features/metrics/{metric-presets,metric-chart}.ts(x)`、`features/logs/{use-log-stream.ts,log-viewer.tsx}`、`features/nodes/node-detail.tsx`（1-160、容器深链段）、`features/alerts/create-rule-dialog.tsx`
- 测试/守卫：`src/console.ui.antipattern.test.ts`、`components/domain/status-badge.test.tsx`、`features/apps-tabs/app-overview.test.tsx`、`lib/deployPayload.test.ts`
- embed 与门禁：`internal/interfaces/console/console.go`、`mise.toml`（console:* 任务）、`.github/workflows/ci.yml`（console job）
- 后端对照（排除误读用）：`internal/domain/delivery/deployment/deployment.go`、`internal/infrastructure/persistence/{deployment,revision,role}/repo.go`、`internal/assembly/{gateway.go,gateway_events.go}`、`proto/fleetly/delivery/v1/delivery.proto`、`proto/fleetly/telemetry/v1/telemetry.proto`、`proto/fleetly/structure/v1/structure.proto`、`proto/fleetly/identity/v1/identity.proto`

### 抽样读过
- `features/resources/panels.tsx`（1483 行，读 1-240 + 章节大纲 + secrets/configs 段）、`features/databases/databases.tsx`（1047 行，读 210-330 + 检索 secret/backup 面）、`routes/_shell/identity.tsx`（764 行，读 1-200 + 全文 API 调用清单）
- `routes/_shell/{nodes.index,templates,logs,alerts,terminal,p.$projectId.apps.index}.tsx`（调用点与深链段）
- `src/api/*.ts` 生成物（`delivery.ts` 的 `v1Deployment`/`v1ListDeploymentsResponse` 逐字段对照 proto；`structure.ts` 的 `v1ProcessSpec`）、`routeTree.gen.ts`（导入与路由表计数）
- `internal/interfaces/console/dist`（index.html、chunk 体积、入口静态导入图；**未逐行读产物**）

### 未覆盖 / 无法覆盖
- **未能再生成核对零漂移**：`console:gen` / `console:build` / `console:verify` 均写仓，禁止运行；dist 与 src/api 只做静态抽样（生成的 `v1Deployment` 字段与 proto 逐字段一致；routeTree 的 45 条 fullPath 对应 46 个路由文件，差额 1 个 pathless `_shell`，对得上）。
- **无浏览器运行时**：所有 UI 行为结论来自代码推演（键形状、状态词表、SSE 帧流），未做真机复现；`console:dev` 亦未启动。
- `components/ui/**`（33 个 shadcn 上游件）按上游原样纪律未逐行审；`components/shell/{command-palette,toolbar,topbar,user-menu,theme-toggle,project-switcher,shell-layout}` 只读导航相关段。
- `features/{tasks,registry,githubapps,fleet}/*` 页面体、`routes/_shell/{backups,audit,metrics,components}*.tsx` 只做了检索级覆盖（未逐行）。
- e2e 无 console 断言面（仓内无浏览器 e2e），故"真机走查"结论无法复验。

## 2. 模块画像与边界

**职责**：`console/**` 是一个 Vite + React 19 + TanStack Router（文件式路由，autoCodeSplitting）+ TanStack Query v5 + Radix/shadcn 的纯 SPA，只消费公共 REST/WS/SSE 面；`vite build` 产物直出 `internal/interfaces/console/dist`，由 `console.go` `go:embed` 进 fleetlyd 同端口静态服务（`internal/interfaces/console/console.go:18-36`）。

**分层形态**（实际，非理想）：
```
routes/**           文件式路由薄壳（router 插件改写，页体在 features/ 才可测）
features/**         页体/面板（按域分，tabs 复用一个壳）
components/domain   四态模板（ListPage/ListCard/DataTable/StatusBadge/PageFrame）
components/ui       shadcn 上游件（不改）
lib/**              查询目录（catalog）/纯函数（载荷、tar、格式、错误文案）
api/**              swagger 生成类型（12 上下文） + 手写 fetch 消费面（client/streams/execstream）
```

**依赖方向**：`features → {lib, api, components}`，`lib → api`，无反向依赖；feature 之间不互相 import（唯一"越层"形态是各 feature 自持 `useQuery`，见 H1/M 各条）。与架构文档的声明一致：Console 零私有服务端端点（`console.go:1-6` 注释）。我把 `src/**` 里所有 `/v1/*` 字面路径抽出来（模板参数归一后约 80 条业务路径，已剔测试夹具）与 `genproto/**/*.swagger.json` 的 117 条 path 全量对照：**除两条原生挂法外全部能在注解面找到**，无语义路径缺失/拼写错误。两条例外在案：`/v1/events/follow`（`internal/assembly/gateway_events.go:27`）与 `/v1/exec/stream`（`api/execstream.ts:7` 注释说明手写消费面）。

**与 ADR/文档的偏差**（详见发现清单）：
- ADR-0044 声称 Console 走"最小只读"面，实际已是全量写面（token 铸造、用户/团队/RBAC 管理、平台备份、变更冻结），凭证持久化 localStorage（`lib/token.ts:3-22` 注释明确"ADR-0044 决策 2"）——按现行实现读，ADR-0044 的"最小只读"已名不副实（文档面归 R15）。
- proto 的部署状态机是自由字符串（`delivery.proto:268` 只有一句"kebab-case 与存储一致"），Console 手维护词表 → 词表漂移（M1）。
- proto 对"双代窗进行中"给了 Console 的判式（`delivery.proto:282-286`），Console 只实现了其中一半（M8）。

## 3. 优点（带证据）

1. **错误信封与 401 全局闭环收敛在一个入口**：`apiFetch` 统一解析 apperr 信封、401 且持凭证时触发注册的 handler（清 token + 清缓存 + 回登录页带 next），登录页自身失败不误伤（无 token 不触发）。
   `src/api/client.ts:41-49`、`src/main.tsx:34-42`。
2. **查询目录把"键即失效粒度"写成了纪律**：`catalog.ts` 集中全部资源查询键，目录 60s/资源 15s/观测 10s 三档轮询，所有 per-id 查询都有 `enabled: id !== ""` 门（避免空参数打出 `/v1/apps//spec` 一类噪声）。
   `src/lib/catalog.ts:12-15,37-54,76-91`。
3. **四源部署载荷是纯函数且有真断言测试**：互斥面/空值缺席/protocol 与 port 成对/builder 仅 upload 面都在 `deployPayload.test.ts` 里逐字形钉死，Sheet 只是薄壳（`deploy-sheet.tsx:148-186`）。
   `src/lib/deployPayload.ts:46-80`、`src/lib/deployPayload.test.ts:30-100`。
4. **生成物纪律是真的有门禁**：`console:verify` = install → gen → build → test → `git diff --exit-code -- console/src/api console/src/routeTree.gen.ts internal/interfaces/console/dist`，CI console job 直接跑同一任务；gen 脚本对 openapiv2 悬空 `$ref` 与 Swagger 2.0→3 两步预处理写在码里（可解释的确定性）。
   `mise.toml:200-208`、`.github/workflows/ci.yml:57-74`、`src/api/gen.mjs:54-80`。
5. **类型纪律在模板层面是干净的**：实测 `src/**` 共 35,885 行，其中生成物（12 个 `src/api/*.ts` + `routeTree.gen.ts`）10,917 行，手写约 **2.5 万行**——手写代码里 **0 个 `any`、0 个 `@ts-ignore/@ts-expect-error`**、只有 1 处带理由的 `eslint-disable exhaustive-deps`，唯一 `as unknown as` 是 `Blob([tar])` 的 DOM 类型缝隙（`panels.tsx:1383`）；`strict: true` + `noUnused*` 在 `tsconfig.json:8-11`。
6. **敏感值一次性揭示的形态是统一且有警示的**（token / 邀请 secret / hook 的 webhook URL），列表页从不回显 secret 值（数据库连接串只显引用）；长列表侧也做了有界化（日志 5000 帧环形缓冲 + `@tanstack/react-virtual` 虚拟渲染，事件页 1000 行上限并显式披露"buffer capped"）。
   `src/routes/_shell/identity.tsx:698-711`、`src/routes/_shell/settings.tsx:222-231`、`src/features/apps-tabs/app-settings.tsx:299-311`、`src/features/databases/databases.tsx:574`、`src/features/logs/{use-log-stream.ts:10-92,log-viewer.tsx:15-35}`、`src/routes/_shell/events.tsx:26-27,184-186`。

## 4. 发现清单

### R11-H1｜同一 Query 键承载两种不兼容形状：项目总览磁贴写对象、详情/列表读数组 → 经节点深链进 App 详情必崩
- 维度：功能
- 严重度：**H**（正常导航链上必然抛渲染异常，整页被路由错误边界接管；不是边缘态）
- 位置：`console/src/features/projects/project-overview.tsx:115-124`（写）；`console/src/features/deployments/hooks.ts:25-34`（读）；崩溃点 `console/src/features/apps-tabs/app-overview.tsx:217`；可达路径 `console/src/features/nodes/node-detail.tsx:124-135`
- 证据：
  ```tsx
  // project-overview.tsx:112-124  —— 磁贴：键 ["deployments", appId]，缓存 Deployment | null
  const deployments = useQuery({
    queryKey: ["deployments", appId],
    queryFn: async () => {
      const res = await apiFetch<{ deployments?: Array<{ id?: string; state?: string; updated_at?: string }> }>(
        `/v1/deployments?app_id=${encodeURIComponent(appId)}&limit=1`,
      );
      return res.deployments?.[0] ?? null;
    },
    refetchInterval: 5_000,
  });
  ```
  ```ts
  // features/deployments/hooks.ts:25-34 —— 同键，缓存 Deployment[]
  export function useAppDeployments(appId: string) {
    return useQuery({
      queryKey: ["deployments", appId],
      ...
        const res = await apiFetch<DeploymentsResponse>(`/v1/deployments?app_id=${...}&limit=50`);
        return rowsOf(res.deployments);
  ```
  ```tsx
  // app-overview.tsx:204-222 —— 拿到对象后：rows.length undefined → 跳过空态 → rows.slice(...)
  ) : (
    <Table className="text-xs">
      ...
        {rows.slice(0, 5).map((item) => (
  ```
- 影响：TanStack Query 只按键寻址，不校验形状。**可复现链**：① 打开 `/p/{project}`（项目总览，每个 AppTile 用 `limit=1` 把 `["deployments", appId]` 写成对象）→ ② 侧栏 `Nodes`（`app-sidebar.tsx:66` 是 `<Link>`，客户端导航）→ 节点详情 → ③ 容器行的 App 深链 `<Link to="/p/$projectId/apps/$appId">`（`node-detail.tsx:127-129`，客户端导航）→ ④ App 详情挂载，`useAppDeployments` 首帧同步返回**对象**（重取是异步的），Overview tab `rows.slice` 抛 `TypeError: rows.slice is not a function` → 路由错误边界整页替换（历史同形事故见 commit `299a918b`"整页错误边界"）。反向也错：App 详情 → 面包屑 `<Link to="/p/$projectId">`（`content-crumb.tsx:19`，客户端导航）→ 总览磁贴首帧把**数组**当单条（`latest?.state` undefined），先显示 "no deployments yet" 再被重取纠正。`p.$projectId.apps.index.tsx:254` 的注释（"与磁贴/详情页同查询键共享缓存"）正是这个假设的书面化。
- 建议：磁贴/列表单元格与详情共用同一函数（都返回数组，取 `[0]`），或给"最近一条"独立键（如 `["deployments","latest",appId]`）。**机制层**：把查询键收进 `queryOptions()` 工厂（`lib/queries/*.ts`），键与 queryFn 返回类型绑定在同一处，错配即 tsc 红。成本 **S**（<1h）+ 守卫 **S**。
- 状态：已亲验（静态：键形状 + 导航链逐点核对；页面级 vitest 用例各自 `new QueryClient()`，跨页缓存不可能被现有测试覆盖）

### R11-M1｜部署状态词表与控制面不一致：`canceled` vs `cancelled`、缺 `building`/`rolling-back` → 构建期无取消按钮、终态徽章错色
- 维度：功能
- 严重度：M（用户可见的正确性缺陷 + 关键操作缺失；有 reload/CLI 兜底）
- 位置：`console/src/components/domain/status-badge.tsx:23-33,40-42`、`console/src/features/deployments/deployment-detail.tsx:35,39,43-48,182`、`console/src/routes/_shell/quickstart.tsx:117`；真源 `internal/domain/delivery/deployment/deployment.go:17-28`
- 证据：
  ```ts
  // status-badge.tsx:23-33,40-42
  const DEPLOYMENT_TONES: Record<string, StatusTone> = {
    succeeded: "success", failed: "danger", canceled: "warning", superseded: "neutral",
    queued: "neutral", preparing: "info", running: "info", observing: "info", releasing: "info",
  };
  // 活跃部署态（可 cancel 的窗口）——与 CLI/server 判式同口径。
  export function isActiveDeploymentState(state: string | undefined): boolean {
    return state === "running" || state === "observing" || state === "releasing" || state === "queued" || state === "preparing";
  }
  ```
  ```go
  // internal/domain/delivery/deployment/deployment.go:17-28（逐行原文）
  const (
  	StateQueued      State = "queued"       // admission 已受理，等待单写者拾取
  	StatePreparing   State = "preparing"    // Spec 归一化/材料装配/前置 Job
  	StateBuilding    State = "building"     // 构建中（镜像引用可跳过）
  	StateReleasing   State = "releasing"    // 投影 Workload 下发 Runtime（L1 门）
  	StateObserving   State = "observing"    // L3 观察窗 + L2 看门狗常驻
  	StateSucceeded   State = "succeeded"    // 终态
  	StateFailed      State = "failed"       // 终态（自动触发回滚）
  	StateRollingBack State = "rolling-back" // Replay 上一成功 Revision
  	StateSuperseded  State = "superseded"   // 被显式 supersede 抢占的终态
  	StateCancelled   State = "cancelled"    // 排队中/在途被取消的终态
  )
  ```
- 影响：① `building`（镜像构建，最长相位）与 `rolling-back` 是活跃态，但 `isActiveDeploymentState` 返回 false → 详情页 **Cancel 按钮消失**（`deployment-detail.tsx:182` `{active ? <Button>Cancel</Button> : null}`），而 App 部署列表根本没有取消入口（`p.$projectId.apps.$appId.deployments.index.tsx:35-86` 列里无动作列）——控制台用户在构建卡死时只能转 CLI。② `cancelled` 落到 neutral（无脉搏），而 `status-badge.test.tsx:16` 把 `"canceled"` 的错误值**钉进了测试**（测试反过来保护了漂移）。③ 时间线 `STAGES`（`deployment-detail.tsx:39`）写的是 queued/preparing/**running**/observing/succeeded——`running` 不是部署态，`building`/`releasing` 无步位，`stageIndex("building")` 返回 4 → 构建中会把末步 "succeeded" 显示为**当前**（脉冲）。
- 建议：状态词表单独成模块，并加一条跨语言守卫（读 `deployment.go` 常量与 TS 常量表比对，缺/多即红；仓内 `internal/guards` 已有同类跨面守卫范式）；`TERMINAL_STATES` 与 `isActive` 改为"非终态即活跃"（与 `State.Active()` 同构）。成本 **S**。
- 状态：已亲验

### R11-M2｜quickstart 的 wait promise 在 `cancelled` 终态永不 resolve（页面永久 "Running…"）
- 维度：功能
- 严重度：M（向导卡死，只能刷新；不丢数据）
- 位置：`console/src/routes/_shell/quickstart.tsx:112-130`
- 证据：
  ```tsx
  const controller = new AbortController();
  const finalState = await new Promise<string>((resolve, reject) => {
    void streamDeploymentWait(deploymentId, controller.signal, (frame) => {
      const state = (frame as { state?: string }).state ?? "";
      if (["succeeded", "failed", "canceled", "superseded"].includes(state)) resolve(state);
    }).catch(reject);
  });
  ```
- 影响：把部署取消（另一标签页/CLI/别人触发）后，服务端终态帧是 `state:"cancelled"`（真源见 M1），不在列表里 → promise 永不 settle；`running` 永久为 true、按钮永久 disabled、无错误提示。另：`controller` 从不 abort，`streamDeploymentWait` 解析后读流仍挂着（服务端终态会收流，泄漏有界，属次要）。
- 建议：终态判定用"服务端终态集"（M1 的统一词表），并在 `streamDeploymentWait` 解析（流自然收口）时也兜底 `resolve`；`finally` 里 abort。成本 **S**。
- 状态：已亲验

### R11-M3｜部署详情的"断流自动重连 ×3 + 10s 轮询兜底"是死代码：首帧后断流 = 静默冻结
- 维度：可靠性
- 严重度：M（该页是部署跟踪的旗舰面；失败模式无任何提示，用户看到的是"很久不动的 building"）
- 位置：`console/src/api/streams.ts:50-56`、`console/src/features/deployments/deployment-detail.tsx:84-96,104-117,231`
- 证据：
  ```ts
  // streams.ts:50-56 —— 只有"正常 EOF"能走到 return；读流抛错（断线/abort）走 reject
  await readLines(res.body, (line) => { ... });
  return { ended: !signal.aborted };
  ```
  ```tsx
  // deployment-detail.tsx:84-96,104-107
  .then(({ ended }) => {
    if (!mounted.current) return;
    if (!ended && attempt < RECONNECT_DELAYS_MS.length) { ...重开... }
    else if (!ended) { setStreamStalled(true); }
  })
  .catch((cause) => { if (mounted.current && !controller.signal.aborted) setStreamError(cause); });
  // 轮询兜底
  const poll = useQuery({ queryKey: ["deployment-poll", deploymentId],
    enabled: streamStalled || (live == null && streamError == null), ... });
  ```
- 影响：`ended` 在"promise resolve"这条路上恒为 `true`（abort 只发生在 cleanup/重开，且那时要么 unmount 要么 `controller.signal.aborted` 挡住重连）→ `!ended` 两个分支都不可达 → `streamStalled` 永不置位 → 10s 轮询兜底永不启用。真实断流（fleetlyd 重启、代理切连）走 `catch`：若已收过帧（`live != null`），`streamError` 既不渲染（错误分支要求 `effective == null`，:136）也不触发轮询（`enabled` 里 `live == null` 为假）→ **页面永久停最后一帧**，且 `TERMINAL_STATES` 不含 `cancelled` 时也不显示 stalled 提示。即便 streamStalled 真置位，`effective = live ?? poll.data`（:117）也让轮询数据永远替换不了 live 快照——兜底即使打开也不生效。
- 建议：`streamDeploymentWait` 区分"终态收流"与"传输收口"（比对最后一帧 state 是否终态）；`effective` 改为"poll 新于 live 则用 poll"或流断即清 `live`；轮询 `enabled` 加上 `streamError != null`。成本 **S/M**。
- 状态：已亲验（静态；异常路径未真机复现）

### R11-M4｜事件页传输重连用"订阅时旧游标"续订 → 事件重复回放（行重复 + 缓冲被挤占）
- 维度：功能
- 严重度：M（每次传输层收口都会重放已收事件；1000 行上限下重复会挤掉真事件）
- 位置：`console/src/routes/_shell/events.tsx:106-120`（重连）、`:102-105`（游标其实已更新）
- 证据：
  ```tsx
  (end) => {
    setFollowing(false);
    if (!aliveRef.current) return;
    if (end.status === 410) { void resync().catch(...); return; }
    if (end.status === 401 || end.status === 403) { setError(...); return; }
    retryTimer.current = window.setTimeout(() => {
      void subscribe(afterSeq).catch((err: unknown) => setError(err));   // ← afterSeq 是本次订阅入参
    }, RECONNECT_MS);
  }
  ```
- 影响：`afterSeq` 是 `subscribe(afterSeq)` 的闭包参数（首装 = 补档后的 `status.last_seq`），流存活期间收到的所有事件都不会推进它；任何一次传输收口（fleetlyd 重启/代理空闲断连）后从旧点重放 → `appendRows` 无去重、`setCursor` 回退 → 同 seq 行重复渲染（`key={event.seq}` 直接撞 React 重复 key）。`resync` 路径用的是 `status.last_seq`，是对的，说明作者知道正确形态，只是重连分支漏了。
- 建议：用 `cursorRef`（或 `end` 里回传最后 seq）作为重连游标；`appendRows` 按 seq 去重。成本 **S**。
- 状态：已亲验

### R11-M5｜反模式守卫的扫描面 ≠ 声称的"全量 src/**"，且根目录缺失时静默通过
- 维度：测试与守卫
- 严重度：M（守卫是 Console 一致性契约的唯一机械执法面，盲区会静默扩大）
- 位置：`console/src/console.ui.antipattern.test.ts:23,26-52,63-81`；被放行的实例 `console/src/features/metrics/metric-chart.tsx:170`
- 证据：
  ```ts
  // 文件头声明："执法范围：全量 src/**。components/ui/** 按上游原样纪律天然不在扫描面。"
  const SCANNED_ROOTS = ["src/components/domain", "src/lib", "src/features", "src/hooks", "src/routes", "src/pages"];
  ...
  it(`scans ${root}`, () => {
    if (!existsSync(root)) return; // 尚未落地的目录（features/routes 随批次创建）
  ```
- 影响：① `src/components/shell/**`（11 文件，含 sidebar/topbar/command-palette 等壳件）、`src/main.tsx`、`src/api/**` 不在扫描面，而文件头只豁免了 `components/ui/**` 与 `backups-view.tsx`——声明与行为不符（现存文件当前无违规，属"下一处违规拦不住"）。② 五个 `it` 各自 `existsSync(root) || return`：目录改名/移动即**全绿空跑**，无"扫到了多少文件"的下界断言（同一测试文件里已有 `walk`，加一个文件计数最容易）。③ 规则按字面 API 名钉死：禁 `.toLocaleString(` 但放行 `.toLocaleTimeString(`——`metric-chart.tsx:170` 就用了后者做时间呈现，正是"时间格式化单源"契约要禁的散写形态。
- 建议：单次 `walk("src")` + 显式例外表（双向保鲜，仓内已有范式）+ `expect(scannedFiles).toBeGreaterThan(100)`；时间规则改 `/\.toLocale(Time|Date)?String\s*\(/`。成本 **S**。
- 状态：已亲验（并对被排除目录跑了同一组 7 条规则：当前无违规）

### R11-M6｜首屏无条件预载 xterm（293KB）+ 全站无压缩：首屏 1.22MB，与"xterm 随终端页、首屏不背"的分包声明相反
- 维度：性能
- 严重度：M（每次进 Console 都付；移动/弱网显著；修复面小）
- 位置：`internal/interfaces/console/dist/index.html:19-23`、`internal/interfaces/console/dist/assets/index-F0D3ejK-.js`（入口第 3 条静态导入）、声明处 `console/vite.config.ts:40-42`
- 证据：
  ```html
  <!-- dist/index.html:18-23（committed 生成物，即实际发布形态） -->
  <script type="module" crossorigin src="/assets/index-F0D3ejK-.js"></script>
  <link rel="modulepreload" crossorigin href="/assets/vendor-DK7SFLhG.js">
  <link rel="modulepreload" crossorigin href="/assets/forms-BOryEL0M.js">
  <link rel="modulepreload" crossorigin href="/assets/xterm-C9W4tV-y.js">
  <link rel="stylesheet" crossorigin href="/assets/xterm-xj0gfbX3.css">
  <link rel="stylesheet" crossorigin href="/assets/index-AiN3EYIt.css">
  ```
  ```
  // dist/assets/index-F0D3ejK-.js 首部
  import{...}from"./vendor-DK7SFLhG.js";import{...}from"./forms-BOryEL0M.js";import"./xterm-C9W4tV-y.js";
  ```
- 依据实测：`xterm-*.js` **290,588 B** + `xterm-*.css` 2,502 B；首屏合计 **1,219,336 B**（index 27,695 + vendor 662,792 + forms 85,028 + xterm 293,090 + CSS 153,233），且 `console.go` 只设 `Cache-Control`，**无 gzip/br 中间件、无预压缩产物**（Go 侧检索 `gzip` 只命中 exec/config 等无关处）。根因：`routes/_shell/terminal.tsx` / `p.$projectId.apps.$appId.terminal.tsx` 在路由模块顶层 import `ExecTerminal`，而 `exec-terminal.tsx` 有一条纯副作用 CSS 导入（`import "@xterm/xterm/css/xterm.css"`）——即使组件被 autoCodeSplitting 拆走，路由模块仍被 `routeTree.gen.ts` 静态导入，CSS 副作用把 xterm chunk 拉进入口图。
- 建议：终端页改为路由内动态 import（或把 CSS 导入挪进懒加载边界），构建后加一条"入口 modulepreload 不得含 xterm/charts"的产物断言（可在 `console:verify` 里 grep dist/index.html，S）；压缩交给 fleetlyd（`gziphandler` 或预压 `.gz`，成本 S）。
- 状态：已亲验（对 committed dist 直接度量；未重跑 build）

### R11-M7｜"New token" 默认角色 = admin（最小权限反转）
- 维度：安全
- 严重度：M（有 tokens:write 的人随手建 token 即得全资源 admin；服务端 `Token=min(声明,creator)` 限制了不能越权到 creator 之上，故非提权，但把默认值钉在最特权档）
- 位置：`console/src/routes/_shell/settings.tsx:196-200,238-249`；服务端顺序 `internal/infrastructure/persistence/role/repo.go:92`、角色语义 `internal/domain/identity/roles.go:63-92`
- 证据：
  ```tsx
  const create = useMutation({
    mutationFn: () => apiSend<{ token?: { id?: string }; secret?: string }>("/v1/tokens", "POST", {
      name,
      role_id: roleId || roles[0]?.id,        // ← 未选即第 0 条
    }),
  ...
  <Select value={roleId || roles[0]?.id || ""} onValueChange={setRoleId}>
  ```
  ```sql
  -- persistence/role/repo.go:92
  FROM roles ORDER BY builtin DESC, id
  -- id 序：builtin-admin → builtin-member → builtin-owner ⇒ roles[0] = admin
  ```
- 影响：admin = 全资源 `:admin`（`roles.go:74-79`）。用户填个名字直接 Create → 铸出**全资源管理** token（且未传 `team_id`，`identity.proto:466` 对该字段无必填约束，Team 归属取服务端默认），最小权限原则失效；权限更高的 owner 反而在列表第三位。若 `roles` 尚未加载/加载失败，`role_id` 缺席，服务端 `string.required` 拒绝（`identity.proto:467-473`）——按钮却是可点的，出现"必填错误"式失败。
- 建议：默认不选（placeholder = "pick a role"）+ Create 按钮在 `roleId === ""` 时禁用；或默认 `builtin-member`。成本 **S**。
- 状态：已亲验

### R11-M8｜蓝绿双代窗卡片忽略 proto 判式的"活跃态"半边：部署成功后仍宣称"两代并存"
- 维度：功能
- 严重度：M（校准偏轻：仅呈现/叙事错误，无数据后果；但它是 proto 明文写给 Console 的判式）
- 位置：`console/src/features/deployments/deployment-detail.tsx:389-403,405-425`；契约 `proto/fleetly/delivery/v1/delivery.proto:282-286`
- 证据：
  ```tsx
  function blueGreenWindow(deployment: Deployment, toRevision: Revision | undefined): GenerationWindow | null {
    const from = Number(deployment.from_generation ?? "0");
    const to = Number(deployment.generation ?? "0");
    if (from === 0 || to <= from) return null;
    const strategies = toRevision?.process_strategies ?? [];
    if (!strategies.some((entry) => entry?.strategy === "DEPLOY_STRATEGY_BLUE_GREEN")) return null;
    return { fromGen: String(from), toGen: String(to), observing: deployment.state === "observing", ... };
  }
  ```
  ```proto
  // delivery.proto:284-285
  // Console 判"双代窗进行中"= 活跃部署 state ∈ releasing/observing 且 from_generation < generation。
  ```
- 影响：succeeded/failed 的历史部署（from_generation<generation 且目标是 BG 策略）也会渲染 "blue-green window" 卡，正文断言 "Both generations ... run side by side until the new one is fully healthy"——此时旧代早已收口。排障/复盘时这是错误事实。
- 建议：判式补 `state ∈ {releasing, observing}`（与 proto 注释一致）；成本 **S**。
- 状态：已亲验

### R11-L1｜App 冻结 Spec 的 50 路客户端扇出在多页复用且 `staleTime` 默认 0，切页即重打 50 个请求
- 维度：性能
- 严重度：L
- 位置：`console/src/features/spec/use-app-specs.ts:9-36`；消费方 `features/apps-tabs/app-overview.tsx:32`、`features/databases/databases.tsx:444`、`features/resources/panels.tsx:617,1356`
- 证据：
  ```ts
  const appIds = apps.map((app) => app.id).slice(0, FAN_OUT_LIMIT);   // 50
  const queries = useQueries({ queries: appIds.map((appId) => ({
    queryKey: ["resources", "app-spec", projectId, appId],
    ...
    refetchInterval: 60_000,
  }))});
  ```
- 影响：单页最多 50 个并发 `GetAppSpec`；`staleTime` 未设 → 任一消费页 remount 立即全量重取（Overview、Databases、Storage/Uploads 之间来回切即 50×3 次突发）。同理 `useProjectDeployments` 15s 扇出 50。属于"客户端聚合"的既有裁决（ADR-0057），但缺一个 `staleTime` 兜底，成本极低。
- 建议：给扇出查询加 `staleTime: 30_000`；或按需 `enabled`（只有当前 tab 可见时才 fan-out）。成本 **S**。
- 状态：已亲验

### R11-L2｜生成的 `src/api` 只用于实体形状：请求/响应信封全手写并 `as T`，契约改名不会被 tsc 抓到
- 维度：可维护性
- 严重度：L
- 位置：`console/src/features/deployments/hooks.ts:17-23`、`src/features/apps-tabs/app-spec.tsx:42,49`、`src/lib/catalog.ts:369-384`；生成的对照物 `src/api/delivery.ts:693-695`
- 证据：
  ```ts
  // 手写信封（生成物里已有 components["schemas"]["v1ListDeploymentsResponse"]）
  interface DeploymentsResponse { deployments?: Array<Deployment | undefined>; }
  function rowsOf(list: Array<Deployment | undefined> | undefined): Deployment[] { ... }
  ```
  ```ts
  // 生成物
  v1ListDeploymentsResponse: { deployments?: components["schemas"]["v1Deployment"][]; };
  ```
- 影响：`apiFetch` 是 `as T` 强转，生成物提供的 **operation/path 与信封类型完全没参与校验**；服务端把 `deployments` 改名或换成 `items` 时控制台只会"静默空列表"。另 `useWhoami` 用 `Record<string, unknown>` 手读 snake_case（`catalog.ts:374-379`），而 identity 上下文其实已生成 `v1WhoAmIResponse`（`src/api/identity.ts:482`）。注释也有漂移：`catalog.ts:342-344` 称 "system 服务不在 console 生成上下文（gen.mjs CONTEXTS 刻意不含）"，而 `gen.mjs:24` 已含 `system`、`src/api/system.ts` 存在。
- 建议：调用点改用 `components["schemas"]["v1*Response"]`，并把 `client.ts` 的泛型约束到生成的 paths；顺手修注释。成本 **M**。
- 状态：已亲验

### R11-L3｜embed 静态面对未带内容哈希的 public 资产也发 365d immutable
- 维度：可靠性
- 严重度：L
- 位置：`internal/interfaces/console/console.go:52-64`；资产 `console/public/{logo.png,favicon.png}`
- 证据：
  ```go
  if stat, err := file.Stat(); err == nil && !stat.IsDir() && upath != "index.html" {
      ...
      w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", int64(assetMaxAge/time.Second)))
  ```
- 影响：设计前提是"文件名带内容哈希"（`console.go:21-23` 注释），但 `logo.png`/`favicon.png` 是稳定名；换品牌图后老客户端最长一年不更新。（`/assets/*` 带 hash，正确。）
- 建议：只对 `dist/assets/` 前缀发 immutable，其余 `no-cache` 或加 short max-age。成本 **S**。
- 状态：已亲验

### R11-L4｜凭证存 localStorage（ADR 声明）叠加全站无 CSP/安全头：缺第二道防线
- 维度：安全
- 严重度：L（当前无已知注入面：全仓只有 1 处 `dangerouslySetInnerHTML`，是 shadcn 图表注入 code 常量的颜色；无第三方脚本）
- 位置：`console/src/lib/token.ts:3-22`、`console/index.html:1-19`、`internal/interfaces/console/console.go:66-69`（只设 Content-Type/Cache-Control）
- 证据：
  ```ts
  const STORAGE_KEY = "fleetly_console_token";
  let current = window.localStorage.getItem(STORAGE_KEY) ?? "";
  export function setToken(next: string) {
    current = next.trim();
    if (current === "") window.localStorage.removeItem(STORAGE_KEY);
    else window.localStorage.setItem(STORAGE_KEY, current);
  ```
- 影响：平台 token（登录铸造、长期有效）XSS 可读；服务端不发 `Content-Security-Policy`/`X-Content-Type-Options`/`X-Frame-Options`（全仓 grep 无命中）。CSRF 面为无（Bearer 头、无 cookie），iframe 面为无（browse 用 `window.open(..., "noopener")`，`databases.tsx:261,460`）。
- 建议：console.go 侧加 CSP（`default-src 'self'; connect-src 'self'` + 允许的 style/字体）与 nosniff/frame-ancestors；token 存储形态若不变，至少在 ADR 里把"无 CSP"列为已知残余风险。成本 **S**（头）/ **M**（改 httpOnly cookie 会话）。
- 状态：已亲验（未做渗透）

### R11-L5｜巨型文件：单文件承载多域面板
- 维度：可维护性
- 严重度：L
- 位置：`console/src/features/resources/panels.tsx`（1483 行，8 个面板：networks/peers/routes/volumes/secrets/configs/variables/uploads）、`features/databases/databases.tsx`（1047 行）、`routes/_shell/identity.tsx`（764 行）
- 证据：`panels.tsx` 的章节锚 `// ---- networks ----` / `// ---- routes ----` / `// ---- volumes ----` / `// ---- secrets / configs / shared variables ----` / `// ---- uploads ----` 全在一文件（panels.tsx:84,384,606,827,1347）。
- 影响：模板已收敛（ListCard/DataTable 吸收四态），但域边界仍靠注释分区；review/冲突面偏大，测试只能整文件挂。
- 建议：按域拆 `features/{networks,routes,volumes,secrets,configs,uploads}/*.tsx`（纯搬迁，S/M）；成本 **M**。
- 状态：已亲验（行数实测）

### R11-L6｜5 个已声明依赖实际零引用（其中 `radix-ui` 是整套原语聚合包）
- 维度：可维护性
- 严重度：L
- 位置：`console/package.json:36-51`
- 证据：`clsx`、`tailwind-merge`（`cn` 包自称其 drop-in 替代）、`radix-ui`（聚合包；代码全走 `@radix-ui/react-*` 单体）、`react-hook-form`、`@hookform/resolvers` 在 `src/**` 中 import 数均为 **0**（逐名正则统计）；`vite.config.ts:51` 的 `forms` chunk 判式仍按 `react-hook-form/@hookform` 分组，实际该 chunk 只有 zod。
- 影响：安装面/锁文件/升级审计面虚增；`radix-ui` 聚合包与单体包并存有"装了两套原语"的误解成本（构建无影响，未被 import）。
- 建议：删 5 项并 `pnpm import` 更新锁文件；`forms` chunk 判式改为 `zod`。成本 **S**。
- 状态：已亲验

### R11-L7｜`useRevisions` 固定 `limit=200` 且服务端升序返回：修订数 >200 的 App 会把"最新冻结"看错
- 维度：功能
- 严重度：L（仅长寿 App；>200 次内容变更才会命中）
- 位置：`console/src/lib/catalog.ts:249-259`、消费方 `features/apps-tabs/app-spec.tsx:55-56`（`revisions.data[length-1]` 当最新）；服务端 `internal/infrastructure/persistence/revision/repo.go:91-96`（升序 + max 200）
- 证据：
  ```ts
  const res = await apiFetch<{ revisions?: ... }>(`/v1/revisions${qs({ app_id: appId, limit: 200 })}`);
  ...
  // app-spec.tsx:55
  const latestFrozen = revisions.data?.[(revisions.data ?? []).length - 1];
  ```
- 影响：截断在**最旧** 200 条（升序 + 无游标翻页）→ `latestFrozen` 实为 R200，`app-spec` 的"最新冻结 ≠ 当前生效"提示条可能永不出现/误判，Revision 下拉缺新条目。
- 建议：用 `after_revision_seq` 取尾页，或加一个 `latest=true`/降序查询面。成本 **S/M**（需接口侧配合）。
- 状态：已亲验（升序与钳位读自 repo 实现）

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **proto 缺状态枚举是跨模块根因**（R08/R01）：`Deployment.state` 是 `string`（`proto/fleetly/delivery/v1/delivery.proto:268`），终端集/活跃集只活在 Go 常量（`internal/domain/delivery/deployment/deployment.go:17-28`）与各消费方手抄里；CLI（R10）与 Console（R11）都各自手维护，M1 是其中一处。建议 R08 评估把 state 升为枚举（或加一个"状态目录"只读 RPC），并在 R12 加跨语言词表守卫。顺带：`delivery.proto:511` 说"终态集见 Deployment.state 注释"，但该注释并未列出终态集（文档缺口）。
2. **HTTP 压缩与安全响应头缺失**（R08/R09）：`console.go` 只设 `Cache-Control`/`Content-Type`，全仓无 gzip/brotli、无 CSP/nosniff/X-Frame-Options。Console 首屏 1.22MB 未压缩（M6）由此放大；建议在 gateway 外壳统一加压缩 + 安全头，而不是只改前端分包。
3. **`/v1/events/follow` 与 `/v1/exec/stream` 是原生挂法**（R08）：Console 手写消费面（`api/streams.ts`、`api/execstream.ts`），协议形状靠 apitest 双端钉（`internal/testsupport/apitest/events_sse_test.go`）。R08 若改 SSE 帧形态（如 error 帧 payload 从 JSON 字符串变对象），Console 的 `JSON.parse(frame.data) as string`（`streams.ts:119`）会静默拿错类型——目前 `sseErrorPayload` 只发 `"CODE"` 字符串，两侧一致。
4. **服务端一次 401 即全局登出**（R14/R08）：`apiFetch` 收 401 就清 token（`client.ts:44-46`）。若后端某条路径因权限/票据设计偶发 401（例如 scope 不足误映射 401 而非 403），用户会被莫名踢出登录态。建议 R08/R14 核对 401/403 的映射边界。
5. **Console 铸 token 不带 `team_id`**（R14）：`settings.tsx:197-200` 只发 name+role_id，平台级 token 形态。若预期是"Team 内 token"，这是契约缺口（`identity.proto:466` 允许空 team_id）。
6. **本地实测基线**：`pnpm test` 30 文件 129 用例全绿与 PROFILE 记载一致；页面级用例各自新建 `QueryClient`，H1 这类跨页缓存缺陷天然测不到。

## 6. 测试与守卫质量评估

**覆盖强度**：30 文件 129 用例，7.61s，全绿（本机实测）。分布合理：纯函数（deployPayload/tar/dokploy/format/legacy-hash/metric-presets/execFrames/backup-health/freshness/registry-model）逐条断言；页面级（app-overview/app-processes/app-spec/app-variables/app-settings/panels/project-overview/node-detail/list-toolbar/command-palette/route-fallback）用 jsdom + 真 `QueryClient` + `fetch` 桩渲染真组件，断言落在可见文案/交互结果上（例：`app-overview.test.tsx:76-80` 同时断言声明 replicas 与 live running；`app-variables.test.tsx` 断言 PUT+DELETE 的调用形态与导航）。

**断言强度**：偏好"行为断言"而非快照，未见 `toMatchSnapshot` 形式的噪声钉死；`deployPayload.test.ts` 用 `toEqual` 钉整载荷 + `"key" in payload` 钉缺席语义（空值不发送），属于高信息量断言。

**主要盲区**（按影响排序）：
1. **跨路由/跨页的缓存契约无测试**：每个用例独立 QueryClient，`["deployments", appId]` 双形状（H1）与"重连游标"（M4）这类跨组件不变量完全在测试面之外。建议加一条"两页共享键形状"的用例（同一 client 先渲染磁贴再渲染详情）。
2. **负面/异常路径少**：SSE 断流/410 resync、wait 流中断、401 闭环、`cancelled` 终态都无测试（M2/M3/M4 因此长期存活）。`event` 流的 410 分支靠人工走查。
3. **反模式守卫自身**（M5）：扫描面声明与实现不符、目录缺失静默通过、规则按 API 字面名钉死。这是"守卫的守卫"缺口——仓内在 `internal/guards` 有"守卫有效性"文化，Console 侧还没有等价物。
4. **词表类测试反向固化漂移**：`status-badge.test.tsx:16` 钉 `canceled`（错值），说明"测试通过"不能证明语义正确，缺跨语言真源对照（M1）。

**假阴性风险**：`fetch` 桩按路径前缀分派（`app-overview.test.tsx:43-63`），返回手写 JSON——与后端真实 protojson 形状（int64 字符串、零值省略）只在部分用例被显式模拟（如 replicas 用字符串）。一旦后端字段改名/类型变，这些桩会继续绿。

## 7. 未决与置信度

**最不确定的 5 点**
1. **M3 的"断流真的会发生"**：我把"clean EOF 只应发生在终态"当作错误前提，但客观上 `streamDeploymentWait` 的 `ended` 恒 true 是确定的；真实断流走 `catch` 后被静默，这条是确定的。不确定的只是"生产环境断流频率"（fleetlyd 重启/代理超时）。置信度高（代码路径确定），影响频次中。
2. **H1 的可达性依赖导航链**：我已逐点核对（AppTile 是 `<a>` 全量刷新，所以只有"项目总览 → 侧栏 Nodes/Apps → 行内深链"这类客户端导航会撞上），且 5 分钟 `gcTime` 内必中。若 TanStack Router 在某处强制整页导航，或 Query 在挂载时同步清空旧形状，则降级为"显示错误不崩"。置信度中高。
3. **dist 零漂移**：只核对了 dist 的入口/预载/chunk 与 src 的对应关系、routeTree 路由数（45 全路径 vs 46 路由文件，含 1 个 pathless）、`src/api/delivery.ts` 的 Deployment 字段与 proto 逐字段一致；**未运行 build**，不能声称"再生成零漂移"。chunk hash 与源码同批（commit `5b97134f` 声明 dist 同批换装）只能作为间接证据。
4. **M7 的实际权限面**：我依据 `BuiltinRoles` 与 `ORDER BY builtin DESC, id` 判定默认 = builtin-admin；未核实 `Token=min(声明,creator)` 的**运行时**是否对空 team_id 的 token 有额外收敛（该逻辑归 R14）。若 min() 之外还有一层 team 轴收敛，M7 的实际风险下调。
5. **L7 的阈值现实性**：>200 次内容变更才触发；若平台常态每个 App 修订数 <50，可降级为文档注记。

**整体置信度：中高**。
- H：静态证据链完整（键、形状、导航、崩溃点），未做浏览器复现（禁止 build/run）。
- M1/M2/M4/M7/M8：代码与真源双侧对照，确定性高。
- M3/M5/M6：M6 是对 committed 产物的直接度量（确定）；M3/M5 是代码/测试面结构性结论（确定）。
- 数值类（dist 体积、行数、依赖零引用）均为本机实测。

**禁止/未做**：`console:gen`、`console:build`、`console:verify`、`pnpm install`、任何浏览器/e2e 运行；未改动仓库任何文件（只写了本报告）。
