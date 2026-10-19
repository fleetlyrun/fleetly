# 安全 backlog 设计：H8 Metrics 租户域过滤 · H10 relay 节点个体凭证

| 项 | 值 |
|---|---|
| 状态 | 设计稿（design-only；本会话未修改任何代码/其它文档；按指令不更新 docs 索引） |
| 日期 | 2026-10-19 |
| 基线 | `main` @ `f312687c`（工作区干净；S6/S7 已修复 R06-M1/H11） |
| 评审锚 | `docs/reviews/2026-10-19-architecture-quality-acceptance.md` §3 H8/H10、§7 M-1/M-4、§11；`docs/reviews/2026-10-19-architecture-acceptance/R07-providers-services.md` §R07-H2；`R08-interfaces-api.md` §R08-M2；`R14-security.md` §4.2 H3、§4.8；`R06-providers-runtime.md` H3 协作点 |
| 关联 ADR | ADR-0041（Metrics/单租户存储）、ADR-0065（负载指标词汇/行级过滤挂账）、ADR-0035（行级授权）、ADR-0049（exec 反向中继/join token 鉴权）、ADR-0052/0053（swarm/k3s 双 Provider）、ADR-0075（四环分层）；实施同批做 ADR-0041/0049/0065 追记 |
| 结论摘要 | H8 选 **B：新增 scoped 查询 RPC，服务端按固定模板构造 PromQL；raw PromQL 收归平台管理员**（先以 D 止血）。H10 选 **A：节点个体 relay 凭证，锚定后按节点铸造、库存 sha256、握手绑定平台节点 ID + carrier ID；全局 rotate 与单节点 revoke 分立** |

> 配套纪律：proto 是唯一契约源（改 `proto/` 后 `buf generate` + golden/Console 生成物同 commit）；生成物手改即错；本设计不引入 PromQL 解析器、不引入外部 PKI；H8 无 DB 迁移，H10 只有空表迁移 00035（无数据回填）。

---

# 第一部分：H8 Metrics 租户域过滤

## 1. 问题与证据

### 1.1 当前行为（全链）

`QueryMetrics` 把调用方给的 PromQL 原样交给受管 VictoriaMetrics：

- handler 无任何作用域构造：`internal/interfaces/grpc/fleetlygrpc/alerting.go:55-100`，其中 `:84` 直接 `svc.s.Metrics.QuerySeries(ctx, req.GetQuery(), start, end, step)`；时间窗只校验先后（`:64-82`），`step_seconds` 原样透传（`:83`）。
- 端口签名没有作用域锚：`internal/domain/shared/capability/ports.go:394-404` 的 `QuerySeries(ctx, query string, start, end, step)`；对照同文件 `:379-389` 的 `Logging.Query(ctx, LogQuery, w)` 明确携带 `LogQuery.Namespace`。
- 请求契约只有 `query/start/end/step_seconds`，没有 `app_id/project_id`：`proto/fleetly/telemetry/v1/telemetry.proto:266-281`。
- 授权只有 `metrics:read`（`telemetry.proto:88-98`）；内建 `member` 对全部业务资源拿 `:read`，其中包含 `metrics:read`：`internal/domain/identity/roles.go:70-87`（`:80-83`）+ `internal/assembly/policy.go:45-53`（资源表含 `metrics`）。
- proto 注释自己承认执法不在服务端：“行级隔离由调用方查询构造承载（App 域标签过滤）——存储是平台单租户”：`telemetry.proto:85-87`。
- ADR 原文同样把它记为调用方责任：`docs/adr/0041-victoriametrics-metrics-provider.md:28-29`（“查询隔离由平台查询构造执法”）、`:55-56`（cadvisor 自带 `container_label_fleetly_ns_*` 标签——per-App 过滤锚）；ADR-0065 明确把“服务端命名视图 + 行级授权”挂账到后续独立批：`docs/adr/0065-workload-metric-vocabulary.md:40-41`。

### 1.2 暴露面与影响

任意 Team 的 member Token（甚至只授了 `metrics:read` 的自定义角色）可发：

```promql
container_cpu_usage_seconds_total
{__name__=~".+"}
{container_label_fleetly_ns_project="<other-project>"}
```

拿到全平台容器序列与 `container_label_fleetly_ns_project` / `container_label_fleetly_ns_app` / `container_label_fleetly_workload_id` / 容器名 / 镜像 / 节点等标签——等价跨租户遥测读（资源规模、镜像版本、上下线时刻侧信道）。这是 H 而不是 M：认证只到“能读平台观测面”，没有任何行级边界；且与 H8 同族的“单租户存储 + 平台凭证 + 透传查询”是机制缺口，不是单个 handler 的笔误（R14 §4.8 裁决）。

额外 DoS 腿（R14 已指出）：`start/end` 任意 RFC3339、`step_seconds=1`，provider 侧 `io.ReadAll(resp.Body)` 无大小上限（`internal/infrastructure/providers/victoriametrics/provider.go:314-317`），`{__name__=~".+"}[90d]` 可把控制面堆内存打满。

评审证据：验收报告 `docs/reviews/2026-10-19-architecture-quality-acceptance.md:114-119`；R07 `R07-providers-services.md:105-129`；R08 `R08-interfaces-api.md:194-210`；R14 `R14-security.md:577-614`。

### 1.3 动线现状（改造必须保住的面）

| 动线 | 位置 | 现状查询构造 |
|---|---|---|
| App Metrics tab | `console/src/features/apps-tabs/app-metrics.tsx:47-49` | `METRIC_PRESETS[].build(appLabel, process)`（词汇指标 + `app`/`process` 等值） |
| App Overview 图表 | `console/src/features/apps-tabs/app-overview.tsx:39-40` | 同上（`cpu_cores`/`memory`） |
| 全局 Metrics 工作台 | `console/src/routes/_shell/metrics.tsx:62-67` | 预设同款；`:131-142` 另有自由 PromQL 输入 |
| Database Metrics | `console/src/features/databases/database-metrics.ts:10-36` | docker 词汇臂 + k3s raw 臂的 `or` 双查询（`namespace`/`pod`） |
| 图表取数 | `console/src/lib/catalog.ts:430-447` | `GET /v1/metrics?query=...&start=&end=&step_seconds=` |
| CLI 诊断 | `cmd/fleetly/cmd/verbs_alerting.go:18-55` | `metrics query PROMQL` 透传 |
| 告警评估 | `internal/application/engine/metrics.go:146-195`（`:193-194` 评估调用） | **不经过 Metrics 端口**：评估在采集环内存样本上完成（`internal/application/engine/alerting.go:25`；端口注释 `ports.go:391-393` 亦写明“告警评估在 engine 内存样本上原生完成——不经本端口”） |

> 任务描述中的“告警评估（内部用 Metrics）”与代码不符：当前告警评估没有 `QuerySeries` 调用点（全仓仅 handler 一处调用，`grep QuerySeries\(` 可证）。因此本设计不存在“内部评估需要豁免”的问题；未来若把评估改成查询 VM，应走内部专用路径，不得复用 raw handler（见 §5.5）。

## 2. 约束

1. **动线不能失明**：上表 Console 五个消费点、CLI 诊断、告警评估必须继续工作；member 至少能看本队 App/Database 图表。
2. **不引入 PromQL 解析器**（除非能论证必须且不破坏单二进制/依赖纪律）。PromQL 是完整表达式语言，见 §3.
3. **无后端迁移**：不拆 VM、不建第二存储、不做按租户路由；现有单 VM 数据面原样。
4. **proto 唯一契约源**：新增字段/RPC 走 `buf generate`；`genproto`、Console `src/api/**`、`console/dist` 同批再生成。
5. **Console 只消费公共 REST/WS**；分层按 ADR-0075：接口层做授权，provider 不持有租户模型。
6. **pre-release 无向后兼容负担**（用户 2026-10-14 裁决）：可以改 `/v1/metrics` 语义、可以让旧 Console/CLI 在升级窗内 403；但必须在同一批把仓内消费方全部切换。
7. **双 runtime**：swarm 的词汇序列（`app`/`database` 标签，`internal/application/engine/exposition.go:44-60`）与 k3s 的 raw 序列（`namespace`/`pod`，`docs/adr/0065-workload-metric-vocabulary.md:40` 明示 k3s 词汇化挂账）都要在一张查询面里工作。
8. **结果/时间上界**：scoped 查询必须有窗口、step、序列数、响应体上限与超时；raw 查询也必须有 DoS 上界。

## 3. 方案矩阵

| 方案 | 形态 | 安全性 | 成本 | 结论 |
|---|---|---|---|---|
| A. 就地 PromQL 重写/注入 matcher | 解析用户 PromQL，向每个 selector 注入 `container_label_fleetly_ns_project="<team projects>"` 等 matcher，再原样执行 | **不 sound**：表达式语义可绕过/折叠标签；完整重写退化为 B | M-L（需 parser + 全语法覆盖 + 测试矩阵） | 放弃（理由见 3.1） |
| B. 新 scoped 查询 RPC | 请求只带 `app_id`/`database_id` + 指标白名单 + 时间窗；服务端授权后按固定模板构造 PromQL，provider 不接受用户查询文本；raw PromQL 收归平台管理员 | sound：用户无法指定表达式；指标名/标签/窗口/步长/上限全部服务端固定 | M（proto + handler + 端口 + 双 runtime 模板 + Console/CLI 切换） | **推荐** |
| C. 结果后过滤 | 先执行用户 PromQL，再按返回序列的 `container_label_*` 标签过滤 | **不成立**：`sum`/`label_replace`/`absent`/`scalar`/`or` 可折叠或伪造标签；聚合值本身已跨租户计算 | S（看起来便宜） | 放弃（理由见 3.2） |
| D. 仅收归 platform admin 止血 | raw `QueryMetrics` 只允许平台管理员；Console/CLI 暂时失明 | 立即关闭泄漏 | S | **只作为阶段 A 止血**，不是终态（member 图表失明，见 3.3） |
| E. per-tenant 指标路由 | 每租户独立 VM 账号/实例/URL，或 `-search.tenant` 多租户路由 | 理论上强 | L-XL（外部依赖 + 采集路由 + 迁移 + 运维） | 放弃（违反约束 2/3；见 3.4） |

### 3.1 为什么 A 不 sound

注入 matcher 只对 vector selector 生效，而 PromQL 允许：

- 聚合折叠：`sum(container_memory_working_set_bytes)` 抹掉标签，使任何“结果后过滤”失效；对“注入 selector”的方案而言，若重写**完整**则 `sum` 只聚合本租户数据，但完整覆盖 PromQL AST 是本方案无法保证的部分（见下）。
- 改标签：`label_replace(container_memory_working_set_bytes, "container_label_fleetly_ns_project", "victim", "", "")` 可把标签改成任意值；`label_join` 同理。
- 合成序列：`absent(container_memory_working_set_bytes{...})` / `vector(1)` / `scalar(...)` 产生无标签或指定标签的序列。
- 全表扫描：`{__name__=~".+"}` 读全平台所有指标名；即使注入租户 matcher，平台级序列（`machine_cpu_cores`、`up`、节点级 cadvisor 序列）没有 `fleetly.ns.*` 标签，注入后要么全被排除（平台诊断失明），要么保留（跨租户可见）。
- 子查询/offset/`@`/`bool`/`group_left` 等语法让“每个 selector 都注入”的实现必须覆盖完整 AST，且未来 PromQL 扩展即失效。

要做到“完整重写且不误伤平台序列”，最终必须**限制指标名/标签白名单**——那就是 B 的形态。所以 A 不是独立方案，是把 B 的复杂度藏进一个有 parser 依赖的中间层；本仓明确不引入 PromQL parser，故放弃。

### 3.2 为什么 C 不成立

结果后过滤假设“标签能在结果里保留且不可伪造”。反例：

```promql
sum(container_memory_working_set_bytes)                          # 无标签，聚合跨租户
label_replace(container_memory_working_set_bytes,
  "container_label_fleetly_ns_project", "victim", "", "")          # 伪造标签
absent(container_memory_working_set_bytes{...})                   # 合成无标签/派生标签序列
scalar(sum(container_cpu_usage_seconds_total))                    # 标量，无标签
```

即使最后返回的序列标签“看起来合法”，数值也可能由他队样本计算而来；过滤是**数据面的事后裁剪**，不是授权边界。放弃。

### 3.3 D 的代价（只做止血时）

`QueryMetrics` 收归 `RequirePlatformAdmin` 后：

- member 的 App Metrics / App Overview / Database Metrics / 全局工作台预设全部 403 → **图表失明**；
- CLI `metrics query` 对 member 403，诊断动线只剩平台管理员；
- 告警评估不受影响（不走查询面）。

这个代价在阶段 A 可接受（先关闭跨租户读），但必须在阶段 B 同批或紧随其后恢复；设计把用户可见的“失明窗口”限制在一个实施阶段内。

### 3.4 为什么 E 放弃

VM 当前是单实例、单存储、平台凭证（ADR-0041:28-29），采集环（`internal/application/engine/metrics.go:146-195`）把所有节点样本导入同一个库。按租户拆 VM/账号需要：采集路由（每样本知道 team/project）、VM 多租户 URL/账号、存量数据迁移、备份/保留/告警链改造——违反“单二进制/单 SQLite/无外部依赖”和“无后端迁移”，且不能修复现有库里的历史数据。放弃。

## 4. 推荐方案

**B：新增 `QueryScopedMetrics` RPC，服务端固定模板构造 PromQL；raw `QueryMetrics` 保留但仅平台管理员可用。** 先做 D（阶段 A）止血，再做 B（阶段 B），最后做 C 阶段硬化（k3s CPU percent、truncation UI、e2e）。

决策理由：

1. **sound**：用户请求里没有 PromQL/标签表达式，无法注入；指标名、scope、标签、窗口、step、上限全部服务端常量或白名单。
2. **不动存储**：同一 VM、同一采集环、同一词汇；只换查询构造方。
3. **能保住现有动线**：App/Database/工作台预设都是固定指标模板，天然适合枚举化；数据库的双 runtime 臂由服务端统一构造，Console 反而变简单。
4. **符合分层**：授权在 handler（行级），查询构造在 capability/domain 纯函数，VM provider 只执行；不把租户模型塞进 provider。
5. **raw 诊断仍有出口**：平台管理员保留自由 PromQL；CLI 新增 scoped 动词给 member/日常诊断。

不选 A/C/E 的关键理由见 §3.1/3.2/3.4；不选“永久 D”是因为它把可用性代价转嫁给所有 member，且与 Console 产品面冲突。

## 5. 详细设计

### 5.1 proto 变更草案

`proto/fleetly/telemetry/v1/telemetry.proto`（only-add；raw 语义改注释）：

```proto
// MetricsService：指标查询面（ADR-0041）。
// - QueryMetrics 是平台管理员诊断面：原始 PromQL 直通，仅平台管理员可调用；
// - QueryScopedMetrics 是租户图表面：服务端按 scope + metric 白名单构造
//   PromQL，调用方无法指定表达式；行级授权在本 handler。
service MetricsService {
  option (grpcapi.v1.service_auth) = { default_access: ACCESS_LEVEL_SERVER };

  // QueryMetrics 原样透传 PromQL（平台管理员诊断；member 调用 E_FORBIDDEN）。
  rpc QueryMetrics(QueryMetricsRequest) returns (QueryMetricsResponse) {
    option (google.api.http) = { get: "/v1/metrics" };
    option (grpcapi.v1.method_auth) = {
      access: ACCESS_LEVEL_SERVER
      api_key_scope: { resource: "metrics" op: SCOPE_OP_READ }
    };
  }

  // QueryScopedMetrics 按 App/Database 作用域查固定指标模板。
  // 授权：metrics:read + app/database 所在 Project 的行级 Team 比对。
  rpc QueryScopedMetrics(QueryScopedMetricsRequest) returns (QueryScopedMetricsResponse) {
    option (google.api.http) = { get: "/v1/metrics/scoped" };
    option (grpcapi.v1.method_auth) = {
      access: ACCESS_LEVEL_SERVER
      api_key_scope: { resource: "metrics" op: SCOPE_OP_READ }
    };
  }
}

message QueryScopedMetricsRequest {
  option (buf.validate.message).cel = {
    id: "queryscopedmetrics.scope_exactly_one"
    message: "exactly one of app_id or database_id must be set"
    expression: "(this.app_id == '') != (this.database_id == '')"
  };
  // app_id 与 database_id 恰一非空（scope 互斥）。
  string app_id = 1;
  string database_id = 2;
  // metric 白名单：cpu_percent | cpu_cores | memory_working_set_bytes
  // （与告警规则 metric 词表同形；非法值在受理面 E_INVALID_ARGUMENT）。
  string metric = 3 [
    (buf.validate.field).cel = {
      id: "queryscopedmetrics.metric_vocab"
      message: "metric must be one of cpu_percent, cpu_cores, memory_working_set_bytes"
      expression: "this in ['cpu_percent', 'cpu_cores', 'memory_working_set_bytes']"
    }
  ];
  // process 仅 app scope 可用；小写 dns 形态（与 Process 名字符集同源）。
  string process = 4;
  // start/end 是 RFC3339；空 end = now，空 start = end 前 1h（与 raw 同）。
  string start = 5;
  string end = 6;
  // step_seconds 是请求步长；0 = 服务端按窗口自动，实际值会被钳制。
  int64 step_seconds = 7;
}

message QueryScopedMetricsResponse {
  repeated MetricSeries series = 1;
  // truncated 表示命中序列数超过服务端上限，返回的是前 N 条。
  bool truncated = 2;
  // series_total 是截断前的命中序列数（未截断 = len(series)）。
  int32 series_total = 3;
}
```

protovalidate 的 message CEL 与仓内 `delivery.proto:300-313` 同风格；字段级 CEL 在 `delivery.proto` 亦有多处先例。

同时给 `WhoAmIResponse` 加一个 only-add 字段（Console 用来显隐 raw PromQL tab）：

```proto
// platform_admin 报告调用方是否平台管理员（default Team 的 builtin-owner）——
// 与 RequirePlatformAdmin 同一判定；Console 据此显隐平台面入口（H8 raw PromQL）。
bool platform_admin = 9;
```

`WhoAmI` handler 映射 `callerIsPlatformAdmin(id)`（`internal/interfaces/grpc/fleetlygrpc/authz.go:42-44`）；`platform_admin` 的判定不在 Console 复制 role/team 条件。

### 5.2 handler 授权与请求处理

文件：`internal/interfaces/grpc/fleetlygrpc/alerting.go`。

**QueryMetrics（raw）**：

1. `RequirePlatformAdmin(ctx)` **第一行**（先于 store 检查、时间解析、任何副作用；与 H1/S2 的平台门纪律一致）。非平台管理员直接 403，不暴露 store 是否启用。**不能只把注解改成 `platform:admin`**：builtin-admin（任意 Team admin）也持有 `platform:admin`，scope 门挡不住；`RequirePlatformAdmin` 是 `default Team + builtin-owner` 的唯一判定（`authz.go:42-44,76-87`）。
2. `svc.s.Metrics == nil` → `E_METRICS_STORE_UNAVAILABLE`（现有文案保持）。
3. 解析/校验时间窗（抽出与 scoped 共用的 helper）：`start < end`，`step_seconds < 0` 拒绝；`step_seconds == 0` → 15s。
4. 调 `QuerySeries`（provider 侧加 §5.3 的体/序列上界）。
5. 错误映射：`capability.MetricsQueryError` → `E_INVALID_ARGUMENT`（现有 `mapping.go:93-98`）；新增哨兵见 §5.3/5.7。
**QueryScopedMetrics**：

```
protovalidate 已保证 scope 恰一 + metric 白名单
  ↓
switch scope:
  app_id:
    a, err := authorizeAppID(ctx, app_id)          // 行不存在 404；他队 403
    projectID = a.ProjectID
  database_id:
    db, err := Databases.Get(ctx, database_id)     // 已删/不存在 404
    projectID = db.ProjectID
    authorizeProjectID(ctx, projectID)             // 行级 Team 比对
  ↓
if Metrics == nil → E_METRICS_STORE_UNAVAILABLE     // 授权之后才暴露 store 状态
  ↓
process 校验（仅 app scope 允许；非空须匹配 `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`）
  ↓
窗口/步长钳制（§5.3）→ capability.ScopedQuery{ProjectID, AppID|DatabaseID, ...}
  ↓
svc.s.Metrics.QueryScoped(ctx, q) → QueryScopedMetricsResponse
```

要点：

- **授权用现成单源**：`authorizeAppID`（`authz.go:131-140`）与 `authorizeProjectID`（`:109-117`）已经包含“行不存在 404 / 存在但非本队 403 / 平台管理员豁免”；不复制 role/team 判定。
- `project_id` 不由客户端给：从 App/Database 行解析，避免“伪造 project + 合法 app”的组合。
- platform admin 走同一 handler：`teamAuthorized` 的 owner 豁免使其可查任意 scope；raw 面另有平台门。
- **不做**：不校验 `target team` 字段（不存在）；不接受任意 `project_id`；不接受 `metric` 之外的指标名；不接受表达式/函数/正则（k3s 模板内部的正则只由服务端按 ID 生成）。
- 错误顺序：protovalidate → 行级授权 → store 状态 → 动态参数（窗口）。这样未授权调用方无法用 503/400 差异探测资源是否存在。
- scoped 面若收到 `capability.MetricsQueryError`（VM 4xx），**不要**走 raw 的 `E_INVALID_ARGUMENT` 映射：模板是服务端固定的，4xx 是平台缺陷，映射 `E_INTERNAL` 并带 cause（`mapStateError` 会误报 400，需要 scoped handler 单独判型）。

### 5.3 provider 查询构造

#### 5.3.1 端口与纯函数

`internal/domain/shared/capability/ports.go` 的 `Metrics` 增加：

```go
// QueryScoped 查询受作用域约束的固定指标模板；调用方不提供 PromQL。
// 构造在 QueryScoped 内部完成（BuildScopedPromQL），实现方不得接受
// 用户表达式。
QueryScoped(ctx context.Context, q ScopedQuery) (SeriesPage, error)

type ScopedQuery struct {
    Metric     string // cpu_percent | cpu_cores | memory_working_set_bytes
    ProjectID  string // App/Database 行解析；k3s namespace 臂
    AppID      string // 与 DatabaseID 恰一
    DatabaseID string
    Process    string // app scope 可选
    Start, End time.Time
    Step       time.Duration
    MaxSeries  int
}

type SeriesPage struct {
    Series    []Series
    Truncated bool
    Total     int
}
```

新增 `internal/domain/shared/capability/metricsquery.go`（纯函数、零 I/O，单测钉 golden）：

```go
const (
    ScopedMetricsMaxWindow = 24 * time.Hour
    ScopedMetricsMaxPoints = 1000
    ScopedMetricsMaxSeries = 50
    ScopedMetricsRawMaxSeries = 1000
    ScopedMetricsMaxBodyBytes = 8 << 20
)

// BuildScopedPromQL 返回固定模板；参数只来自 DB 行/白名单，标签值经
// quoteMetricLabel（\" 与 \\ 转义）。
func BuildScopedPromQL(q ScopedQuery) (string, error)
func ClampScopedStep(start, end time.Time, requestedSeconds int64) (time.Duration, error)
```

指标名/标签名单源：把 `internal/application/engine/exposition.go:18-22` 的三个词汇指标名移到 capability（engine 保留同名 alias 或直接引用），并在 capability 定义：

```go
const (
    MetricLabelApp      = "app"       // 词汇（swarm docker 臂）
    MetricLabelDatabase = "database"  // 词汇（swarm docker 臂）
    MetricLabelProcess  = "process"   // 词汇
    MetricLabelProjectOnContainer = "container_label_fleetly_ns_project" // cAdvisor 投影
    MetricLabelAppOnContainer     = "container_label_fleetly_ns_app"
    MetricLabelNamespace = "namespace" // k3s raw 臂
    MetricLabelPod       = "pod"
    MetricLabelContainer = "container"
)
```

写侧标签 `fleetly.ns.project` 等仍在 provider（`swarm/translate.go:128-158`、`k3s/translate.go:159-197`）；在 swarm/k3s 的 translate 测试里加一条 drift guard：`capability.MetricLabelProjectOnContainer == "container_label_" + strings.ReplaceAll(labelProject, ".", "_")`（app/database 同），防止读侧常量与写侧标签漂移。

#### 5.3.2 模板（精确形状）

设 `app`/`db`/`project`/`process` 都是实体 ID / Process 名（`entityid.Valid` 或 process 正则），`qLabel` 为转义后的标签值。

**App scope（swarm 词汇臂）**：

```promql
fleetly_container_cpu_percent{app="<app>"[,process="<process>"]}
fleetly_container_cpu_cores{app="<app>"[,process="<process>"]}
fleetly_container_memory_working_set_bytes{app="<app>"[,process="<process>"]}
```

**App scope（k3s raw 臂）**，`podRe = fleetly-<app>[-<process>](-.*)?`（无 process 时为 `fleetly-<app>-.*`）：

```promql
container_memory_working_set_bytes{namespace="fleetly-<project>",pod=~"<podRe>",container!=""}
rate(container_cpu_usage_seconds_total{namespace="fleetly-<project>",pod=~"<podRe>",container!=""}[<rateWindow>])
100 * rate(container_cpu_usage_seconds_total{namespace="fleetly-<project>",pod=~"<podRe>",container!=""}[<rateWindow>])
      / on(node) group_left machine_cpu_cores
```

**Database scope（swarm 词汇臂）**：

```promql
fleetly_container_cpu_cores{database="<db>"}
fleetly_container_memory_working_set_bytes{database="<db>"}
```

**Database scope（k3s raw 臂）**，`podRe = fleetly-db-<db>(-.*)?`：

```promql
container_memory_working_set_bytes{namespace="fleetly-<project>",pod=~"<podRe>",container!=""}
rate(container_cpu_usage_seconds_total{namespace="fleetly-<project>",pod=~"<podRe>",container!=""}[<rateWindow>])
```

**合成**（固定顺序，供 golden 断言）：

```promql
(<swarm 臂>) or (<k3s 臂>)
```

约束/说明：

- `project` 来自 App/Database 行的 `ProjectID`；k3s namespace 公式 `fleetly-<project>`（`k3s/translate.go:90-97`）；swarm/数据库载体名公式见 `k3s/translate.go:99-127` 与 `swarm/translate.go:128-158`。
- `cpu_percent` 只允许 app scope（Database 页目前只用 memory/cores；见 `database-metrics.ts:22-36`）；handler 对 `database_id + cpu_percent` 返回 `E_INVALID_ARGUMENT`。
- `rateWindow = max(2m, 4*step)`，上限 1h；step 已由 §5.3.3 钳制。Console 当前固定 `[2m]`（`database-metrics.ts:35`）在 24h/360s step 下窗口偏小，本设计顺手修正。
- 双 runtime 为什么可以“无 runtime 知识”：swarm 词汇序列与 k3s raw 序列在同一 VM，互不匹配对方的 label 集（词汇只在 engine 解析 docker 标签后导入，`exposition.go:24-61`；k3s 词汇化挂账见 ADR-0065:40）。`or` 的失败臂是空集，成本由 label index 吸收；这比让 VM provider 读 runtime 配置（违反 providers 互不 import）更干净。
- 所有结果标签由 VM 返回；服务端不额外 `label_replace`，保留原始序列身份给 Console 图表排序（`metric-chart.tsx:36-47`）。
- **k3s 的已知功能近似**：k3s raw 臂只有 `namespace`/`pod` 标签，没有 `fleetly.ns.app` 标签（ADR-0065:40 的 k3s 词汇化挂账）。`pod` 正则按 `<app>/<db>` ID + 分隔符构造，能防跨 Project（namespace 已收敛）和绝大部分兄弟载体；但若同一 Project 内两个 App/Database 的 ID 互为前缀（如 `web-1` 与 `web-1-2`），极端情况下会多命中同 Project 的兄弟序列。**这不是跨 Team 泄漏**（H8 的安全边界是 Team/Project），是 app 粒度展示的近似；严格收口需给 k3s cadvisor 增加标签白名单或补 k3s 词汇导出（ADR-0065 挂账），列为后续增强。验收以“跨 Team 不可见”为准，app 粒度 correctness 用 staging 夹具覆盖常见 ID。

#### 5.3.3 时间窗/step/结果上限

- 窗口：`end - start > 24h` → `E_INVALID_ARGUMENT`；`start >= end` → 同。
- step：
  - `requested < 0` → `E_INVALID_ARGUMENT`；
  - `requested == 0` → `effective = max(15s, ceil(window/240))`（与 Console `METRIC_RANGES` 的 step 口径一致，`catalog.ts:421-428`）；
  - `requested > 0` → `effective = max(requested, 15s, ceil(window/ScopedMetricsMaxPoints))`；
  - 取整到秒（VM query_range 的 step 是整数秒）。
- 序列：scoped 返回前 `ScopedMetricsMaxSeries=50` 条，`truncated=true`、`series_total=实际数`；raw 超过 `ScopedMetricsRawMaxSeries=1000` 直接 `E_METRICS_RESULT_TOO_LARGE`（管理员诊断不应静默截断）。
- 响应体：provider 对 `resp.Body` 用 `io.LimitReader(..., 8MiB+1)`；超过 → `ErrMetricsResultTooLarge`，映射 `E_METRICS_RESULT_TOO_LARGE`。raw/scoped 同用。
- 超时：保留 `queryTimeout=30s`（`provider.go:58-59`），ctx 超时归类为 `ErrMetricsQueryTimeout` → `E_METRICS_QUERY_TIMEOUT`；客户端主动取消不映射为超时（`ctx.Err()` 先判父 ctx）。
- 点上限：由 step 钳制保证每序列 ≤ ~1000 点（24h 下 step ≥ 87s）。

#### 5.3.4 provider 实现

`internal/infrastructure/providers/victoriametrics/provider.go`：

```go
func (p *Provider) QueryScoped(ctx context.Context, q capability.ScopedQuery) (capability.SeriesPage, error) {
    query, err := capability.BuildScopedPromQL(q)
    if err != nil { return capability.SeriesPage{}, err }
    max := q.MaxSeries
    if max <= 0 { max = capability.ScopedMetricsMaxSeries }
    series, err := p.queryRange(ctx, query, q.Start, q.End, q.Step, 0) // 0 = 不按序列数报错
    if err != nil { return capability.SeriesPage{}, err }
    total := len(series)
    if total > max {
        return capability.SeriesPage{Series: series[:max], Truncated: true, Total: total}, nil
    }
    return capability.SeriesPage{Series: series, Total: total}, nil
}
```

`QuerySeries`（raw）复用 `queryRange(..., ScopedMetricsRawMaxSeries)`；`queryRange` 统一做体上限、4xx 类型化（保留 `MetricsQueryError`）、timeout 类型化、series cap。

### 5.4 Console 与 CLI 切换点

#### Console

| 文件 | 变更 |
|---|---|
| `console/src/features/metrics/metric-presets.ts:12-46` | `MetricPreset.build()` 改为 `{key,label,unit,metric}` 描述（`metric` 为 §5.1 白名单值）；删除 PromQL 字符串构造与 `selector()` |
| `console/src/features/apps-tabs/app-metrics.tsx:47-49` | 用 `useScopedMetricsSeries({kind:"app", id:appId}, preset.metric, process, "1h")` |
| `console/src/features/apps-tabs/app-overview.tsx:39-40` | 同上（cpu_cores/memory 两张卡） |
| `console/src/routes/_shell/metrics.tsx:62-67` | 预设走 scoped；`:53` 的自定义 PromQL 仅 `whoami.platform_admin` 时保留，非管理员隐藏 Custom tab（URL 里 `preset=custom` 回落 CPU %） |
| `console/src/features/databases/database-metrics.ts:10-36` | 删 docker/k3s 双臂 PromQL；改为 `{metric:"memory"|"cpu_cores"}` 描述；`databases.tsx:644-645` 改传 database id |
| `console/src/lib/catalog.ts:430-447` | 新增 `useScopedMetricsSeries(scope, metric, process, range)`（GET `/v1/metrics/scoped?...`）；原 `useMetricsSeries` 改名为 `useRawMetricsSeries` 仅供管理员 custom tab |
| `console/src/lib/catalog.ts:362-384` | `WhoAmI` 增 `platformAdmin`（读 `platform_admin`） |
| `console/src/api/telemetry.ts` / `identity.ts` / `console/dist/**` | `mise run console:gen && mise run console:build` 同批再生成（生成物禁手改） |
| 图表 | `MetricChart` 增加可选 `truncated/seriesTotal` 提示（一行 “server cap reached — N of M series shown”）；不改变现有 10 条显示上限与 hidden 计数 |

#### CLI

`cmd/fleetly/cmd/verbs_alerting.go`：

- `metrics query PROMQL`：保留，synopsis 改 “platform-admin diagnostics (raw PromQL)”；member 403 由服务端返回。
- 新增 `metrics series`：

```
fleetly metrics series --app APP_ID --metric cpu_percent|cpu_cores|memory_working_set_bytes [--process P] [--start T] [--end T] [--step-seconds N]
fleetly metrics series --database DB_ID --metric memory_working_set_bytes|cpu_cores [...]
```

- `--app`/`--database` 互斥且必填恰一；`--metric` 必填；`--json` 输出 `QueryScopedMetricsResponse` protojson；人类输出每点 `series=<labels> <time> <value>`（raw 的纯 time/value 在 scoped 面会丢序列身份）。
- 命令组注册在 `cmd/fleetly/cmd/app.go:130`；golden 组输出同批更新。

#### SDK/生成物

- `genproto/**` 由 `buf generate` 生成（含 Go + swagger）；`sdk/go` 不手写新客户端，沿用生成 client。
- `console/src/api/**` 由 `openapi-typescript` 从 swagger 再生成；`console/dist` 重新构建。

### 5.5 告警评估是否豁免

**不豁免，因为不存在调用**：

- `internal/application/engine/metrics.go:146-195` 的采集环在 scrape 同拍解析 `containerSample` 并调用 `evaluateRules`（`:193`）；
- `internal/application/engine/alerting.go:25` 的评估只读内存样本；
- `internal/domain/shared/capability/ports.go:391-393` 明确“告警评估在 engine 内存样本上原生完成——不经本端口”；
- 全仓 `QuerySeries(` 调用点仅 `fleetlygrpc/alerting.go:84`（`grep -n 'QuerySeries('` 可复验）。

因此 raw 收归平台管理员不会影响告警评估。若未来需要“规则评估改查 VM”，必须新增内部专用方法/凭证，不得复用 `QueryMetrics` handler；这一点写进 ADR-0041 追记，避免下次把“内部豁免”当成泄漏口。

### 5.6 golden / apitest / 守卫影响

- `genproto/**`、`console/src/api/**`、`console/dist/**` 同批生成；`mise run generate:verify` / `console:verify` 零漂移。
- CLI golden：`cmd/fleetly/cmd/testdata/golden/metrics*.golden`（组列表、`metrics series` 用法/失败面、`--json`）；`TestGoldenAlertingVerbs` 补两轮；`whoami-json.golden` 因 `platform_admin` 字段更新（`cmd/fleetly/cmd/identity_golden_test.go:94-99`）。
- apitest：新增 `internal/testsupport/apitest/metrics_scoped_test.go`；测试夹具需要注入 fake Metrics（当前 `newHarness` 只接受 `engine.Options` mutator，`internal/testsupport/apitest/harness.go:90-126`）——新增 `apitest.NewWithMetrics(t, capability.Metrics)`（或 `Deps` mutator）测试 seam，生产装配不变。
- 端口 fake 更新：`internal/assembly/provides_test.go:138`、`internal/application/engine/metrics_test.go:48`、`internal/testsupport/apitest/alerting_test.go:157-175` 实现 `QueryScoped`。
- errcode：新增 `E_METRICS_RESULT_TOO_LARGE`（GRPC `ResourceExhausted`）与 `E_METRICS_QUERY_TIMEOUT`（GRPC `DeadlineExceeded`），带 Source/Suggestion；`go test ./internal/domain/shared/model/errcode -update` 更新 `codes.golden`。`mapping.go` 增加两分支（在 `MetricsQueryError` 分支旁）。
- 守卫：`scopeguard`/`writeclassguard` 对新 RPC 走只读扫描；`coverageguard` 需要新 RPC 的 apitest 引用；`nilfacelaw` 如把新 code 归入 501 类需核对（本设计是 4xx/5xx，不涉及）。

### 5.7 失败语义（可编程锚）

| 情形 | 错误 | HTTP/gRPC | 备注 |
|---|---|---|---|
| `metric` 非白名单 / scope 两者都给或都不给 | `E_INVALID_ARGUMENT` | 400 / InvalidArgument | protovalidate 在 handler 前 |
| `database_id + cpu_percent` 或 `database_id + process` | `E_INVALID_ARGUMENT` | 400 | Database 页没有这两个消费面；显式拒绝而非静默忽略 |
| `app_id`/`database_id` 不存在 | `E_NOT_FOUND` | 404 | 与 `authorizeProjectID` 同源 |
| 行存在但非本队 | `E_FORBIDDEN` | 403 | `authorizeAppID`/`teamAuthorized` |
| 窗口 > 24h / start ≥ end / step<0 | `E_INVALID_ARGUMENT` | 400 | 文案给上限值 |
| Metrics 面停用 | `E_METRICS_STORE_UNAVAILABLE` | 503 | 授权之后才判 |
| VM 4xx（raw 用户查询语法/语义错） | `E_INVALID_ARGUMENT` | 400 | 现有 `MetricsQueryError` 映射（`mapping.go:93-98`） |
| VM 4xx（scoped 固定模板被拒） | `E_INTERNAL` | 500 | 调用方无法修模板；是平台缺陷，必须带 cause 可排查（不要误报 400） |
| 结果体 > 8MiB / raw 序列 > 1000 | `E_METRICS_RESULT_TOO_LARGE` | 429 / ResourceExhausted | scoped 命中 >50 不报错，`truncated=true` |
| provider 30s 超时 | `E_METRICS_QUERY_TIMEOUT` | 504 / DeadlineExceeded | 客户端取消不落此码 |
| 平台管理员调用 raw | 正常 | 200 | 仍受体/序列/超时上限 |

## 6. 迁移/兼容

- **proto only-add**：新增 RPC/message/字段，不删不改字段号；`buf breaking` 对 origin/main 通过。
- **无 DB 迁移**：不动 SQLite；VM 数据/保留/采集环不变。
- **生成物同批**：`mise run generate:all`（genproto + config/wire）→ `mise run console:gen` → `console:build`；CI 的 `generate:verify`/`console:verify`/golden 同 commit。
- **行为变更**：raw `QueryMetrics` 对非平台管理员从 200 变 403；scoped 新面是唯一 member 图表路径。Console/CLI 必须在同一 commit 切换，避免“服务端已 403、前端还在调 raw”的中间态。
- **ADR 追记**：ADR-0041 决策 1 的“查询隔离由调用方构造”改为“服务端 scoped 查询 + raw 平台管理员”；ADR-0065 的“行级过滤挂账”标记关闭并注明本设计；ADR-0041 决策 3 的告警评估“不经端口”保持并强调不得豁免。
- **回滚**：阶段 A 回滚 = 去掉 `RequirePlatformAdmin`（恢复泄漏，仅紧急）；阶段 B 回滚 = 回退 RPC + Console/CLI 同批回退（不留半切换）。无数据回滚面。

## 7. 验收锚（逐条可测）

- [ ] **跨队 member 查不到他队序列**：`go test ./internal/testsupport/apitest -run TestQueryScopedMetricsRejectsOtherTeam -count=1`。夹具建两队各一 Project/App；A 队 member token 用 B 队 `app_id`（及 `database_id`）调用 → gRPC `PermissionDenied`，错误含 `E_FORBIDDEN`；fake Metrics 的 `QueryScoped` 调用计数为 0（授权前拒绝，零下游）。
- [ ] **member 能查本队 App 图表**：`TestQueryScopedMetricsOwnTeamApp`：A 队 member 调本队 app 的 `cpu_percent`/`memory_working_set_bytes` → 200；fake 捕获的 `ScopedQuery` 的 `ProjectID/AppID/Process` 与夹具一致，且接口没有可传 PromQL 的字段。
- [ ] **raw PromQL 仅平台管理员**：`TestQueryMetricsPlatformAdminOnly`：member 调 `QueryMetrics` → `E_FORBIDDEN`（即使 fake Metrics 已配置）；owner → 200；匿名 → `E_UNAUTHENTICATED`（拦截器）。
- [ ] **非法指标名拒绝**：`TestQueryScopedMetricsRejectsUnknownMetric`：`metric="disk_full"`、`metric=""` → `E_INVALID_ARGUMENT`，文案含三个合法值。
- [ ] **scope 互斥**：`TestQueryScopedMetricsScopeMutex`：两个 ID 都给/都不给 → `E_INVALID_ARGUMENT`。
- [ ] **窗口/步长钳制**：`go test ./internal/domain/shared/capability -run TestClampScopedStep`：25h → error；24h + step=1 → effective ≥ 87s；1h + step=0 → 15s；step=1h → 1h；点数 ≤ 1001。
- [ ] **模板 golden**：`go test ./internal/domain/shared/capability -run TestBuildScopedPromQL`：app/database × 三种指标 × swarm/k3s 共 10 条精确字符串；断言标签值引号转义、无用户输入拼接、无 `label_replace`。
- [ ] **结果上限/超时**：`go test ./internal/infrastructure/providers/victoriametrics -run 'TestQueryScopedTruncates|TestQueryResultTooLarge|TestQueryTimeout'`：60 条 → 50 + `truncated` + `total=60`；8MiB+1 响应体 → `ErrMetricsResultTooLarge`；30s 超时 → `ErrMetricsQueryTimeout`；`go test ./internal/interfaces/grpc/fleetlygrpc -run TestMapMetricsErrors` 断言两个新 code。
- [ ] **告警评估仍工作**：`go test ./internal/application/engine -run 'TestMetricsStep|TestEvaluateRules' -count=1` 全绿；并加一条静态断言/注释：`grep -n 'QuerySeries(' internal/application/engine` 无生产调用点（评估不走端口）。
- [ ] **Console 回归**：`cd console && pnpm test`（现有 130 用例 + 新增 preset/scoped URL 用例）；`mise run console:verify`（类型再生成 + 构建零漂移）；手工检查：member 登录后 App Metrics/Overview/Database 图表有数据、Custom PromQL tab 不可见；owner 登录后可见 Custom tab 且可查 raw。
- [ ] **CLI golden**：`go test ./cmd/fleetly/cmd -run 'TestGoldenAlertingVerbs|TestGoldenIdentityVerbs'`；`metrics series` 的 usage/缺参/`--json` 双形态钉死。
- [ ] **守卫全套**：`go test ./internal/guards -count=1`（errcode golden/usage 反扫、scope、coverage）。
- [ ] **基线验证**：`mise run test`（根 + genproto + sdk）、`mise run lint`（含 buf breaking）。

## 8. 分步实施计划

> 每阶段独立可验收、独立 commit；阶段 A 是安全止血，B 恢复产品面，C 补真机/硬化。

### 阶段 A（S）：raw 收归平台管理员 + 上限（止血）

改动：

- `alerting.go` `QueryMetrics` 首行 `RequirePlatformAdmin`；抽出窗口解析 helper。
- `victoriametrics/provider.go`：`queryRange` 体上限 8MiB、raw 序列上限 1000、timeout/结果过大哨兵；`mapping.go` 两个新 code；`errcode` + golden。
- Console：**本阶段不动 Console**（custom tab 保留，owner 仍可用；member 的图表/自定义查询在阶段 A 显示 403 错误态，阶段 B 恢复）。不在阶段 A 引入依赖 `platform_admin` 的显隐（该字段随阶段 B 的 proto 一起来）。
- 测试：raw 平台门 apitest、provider 上限单测。

验收：raw member 403、owner 200；`go test ./internal/interfaces/grpc/fleetlygrpc ./internal/infrastructure/providers/victoriametrics`；Console 构建不红（图表错误态可接受，页面不崩）。

### 阶段 B（M）：scoped RPC + Console/CLI 切换

改动：

- proto（`QueryScopedMetrics` + `WhoAmI.platform_admin`）+ 生成物。
- `capability`：`QueryScoped`、`ScopedQuery`、`SeriesPage`、`BuildScopedPromQL`、`ClampScopedStep`、指标/标签常量；engine 常量 alias。
- `victoriametrics`：`QueryScoped` + `queryRange` 复用。
- `fleetlygrpc`：`QueryScopedMetrics` handler、`WhoAmI` 字段、错误映射。
- Console：presets 描述化、scoped hook、五个消费点切换、Custom tab gating、truncated 提示、`console:gen/build`。
- CLI：`metrics series` 动词 + golden。
- 测试：§7 的 scoped/授权/模板/Console/CLI 项。

验收：§7 前 9 条；`mise run console:verify`；`go test ./internal/testsupport/apitest ./cmd/fleetly/cmd`。

### 阶段 C（S）：k3s CPU % 与真机/硬化

改动：

- 在 staging/dind k3s 上验证 `100 * rate(...) / on(node) group_left machine_cpu_cores` 返回正确量纲（若失败，回退为“Database 同款 cores/memory + CPU % 明确不可用”的诚实空集，不阻塞安全修复）。
- `truncated/series_total` 的 Console 展示与 e2e 断言；`dind-two-node.sh` 的 metrics 段补一条 scoped 查询（owner 会成功，负例用 member token 需额外铸造，可放 apitest）。
- ADR-0041/0065 追记；checklist/评审状态更新。

验收：staging 手工命令（见附录 B）；`mise run e2e:twonode` 的 metrics 段仍绿。

## 9. 风险与未决

| 风险/未决 | 影响 | 处置/验证 |
|---|---|---|
| **project 级 scoped 查询是否要有** | 当前 Console 无 project 汇总图表；CLI 诊断若想按 project 查需 raw（管理员） | **已做（S18a `07128fef`，2026-10-19 裁决转为 v1 做）**：v1 用 **raw 双臂**——swarm 臂按 cAdvisor 投影标签 `container_label_fleetly_ns_project`、k3s 臂按 `namespace=fleetly-<project>`（两臂标签集互斥，`or` 不重复计数）；**不扩词汇** `fleetly_container_*`（ADR-0065 词汇无 project 维度）。project 白名单仅 `cpu_cores`/`memory_working_set_bytes`（`cpu_percent` 无明确 CPU 分母，受理面 `E_INVALID_ARGUMENT`；process 维度仅 app scope）。验证：`capability` 查询构造形状测 + apitest project scope + CLI `metrics series --project` golden。 |
| **R08-M2 余项：ListAlertRules/ListAlertStates 无 Team 过滤** | 与 H8 同源（R08-M2 被并入 H8 条目），但不在指标查询面 | **已修（S13-c3 `1dd4171d`）**：`ListAlertRules/ListAlertStates` 非平台管理员按本 Team 的 Project 集过滤（`alerting.go`）；apitest `alerting_team_filter_test.go` 钉他队规则不可见。本设计原「单列 S-M 小批」的建议已被 S13 并入身份/读面最小化批。 |
| k3s CPU % 模板真机行为 | 若 `group_left` 在 k3s raw 序列上量纲/匹配不对，member 的 k3s CPU % 图空 | 阶段 C staging 验证；失败回退为“cores/memory 可用、CPU % 空集”并显式记档（不阻塞安全修复） |
| k3s raw 臂的 app/database 归属是 pod 名近似 | 同 Project 内 ID 互为前缀的兄弟载体可能多命中（非跨 Team） | 安全边界由 `namespace=fleetly-<project>` 收敛；严格 app 粒度需 k3s 词汇化/标签白名单（ADR-0065 挂账）。验证：staging 造 `web-1`/`web-1-2` 夹具，确认 charts 只差“多出兄弟序列”且无跨 Project 序列 |
| VM 双 `or` 臂的查询成本 | 每图两个 selector；大范围可能更慢 | 24h/1000 点钳制 + 30s 超时；staging 观察 P95；必要时按 scope 缓存（不做在本批） |
| raw 管理员仍可 `{__name__=~".+"}` | 平台管理员是信任档；误用可打满内存 | 8MiB 体 + 1000 序列 + 30s 超时；不改变信任模型 |
| Console 生成物/前端回归 | 五处消费点 + 生成类型，漏一处即图表空 | `console:verify` + 手工走查 App/DB/工作台三页；preset 单测 |
| 测试 seam 新增 `NewWithMetrics` | 仅测试面，生产装配不变 | 代码评审核对 `engine.Deps` 不变 |
| 升级窗内旧 Console | pre-release 无兼容负担 | 同批发布；旧 dist 调 raw 会 403，页面显示错误态，不回滚安全修复 |

---

# 第二部分：H10 relay 节点个体凭证

## 1. 问题与证据

### 1.1 当前握手（全链）

1. `GET /v1/relay` 升级前只校验 `bearerToken` 是**活 join token**：`internal/assembly/gateway_exec.go:81-84`（`eng.ExecValidateClusterToken`）。
2. 升级后读首帧文本 `hello`，`hello.carrier_node_id` **由客户端自报**：`gateway_exec.go:102-108`；`RelayHello` 结构 `internal/domain/shared/capability/exec.go:122-127`。
3. `RelayAttach` 用 join token 复验一次，再按 `carrier_node_id` 反查平台节点并**直接替换** `relays[platformID]`：`internal/application/engine/execrelay.go:20-55`（`:24` 复验、`:27-30` 反查、`:34-46` 替换、`:53-55` 收口已附着会话）。
4. swarm 的 join token 是**集群级共享串**（worker 或 manager 均可）：`internal/infrastructure/providers/swarm/exec.go:80-92`；k3s 是**静态 node token**（server 文件）：`internal/infrastructure/providers/k3s/exec.go:63-75`。
5. `carrier_id` 对 `nodes:read` 可见：`proto/fleetly/runtime/v1/runtime.proto:79-96`。

ADR-0049 的原始裁决明确把安全承载放在 join token 上：“鉴权 = swarm join token（worker 或 manager 皆可）……`Enrollment rotate` 已轮换双 token → 旧中继失联直至重跑 RelayCommand = 泄漏处置语义”：`docs/adr/0049-exec-subface-and-reverse-relay.md:82-91`；manager 回环“每次重连从本地 SwarmInspect 取新 token”：`:88-91`。H10 要改的正是这条裁决。

### 1.2 利用链（与 H1/H2 串联）

1. 攻击者持有活 join token（H1/H2 平台面接管可取得；或 `/v1/platform/binary` 材料泄漏）+ 目标节点 carrier id（`ListNodes` 的 `carrier_id` 对 `nodes:read` 开放）。
2. 拨 `GET /v1/relay`，Bearer = join token；首帧 `{"type":"hello","carrier_node_id":"<受害节点>"}`。
3. engine 判定“该节点重连”→ `relays[platformID]` 被替换；受害节点上已附着的 exec 会话被 `relay_disconnected` 收口（可用性攻击）。
4. 之后任何发往该节点的 exec（他队用户的 `fleetly exec/shell`）都被投递给攻击者连接：收到 `ExecWorkload` 请求帧（workload/instance/argv）与 **stdin 明文**（交互式终端可能含密码），并可返回任意 stdout/stderr/exit（伪造命令输出）。
5. 审计面无法区分真假中继。

评审：`docs/reviews/2026-10-19-architecture-acceptance/R14-security.md:320-368`；验收报告 `...quality-acceptance.md:128-133`。状态：机制已确认（gateway/execrelay/provider 三处亲读），真实劫持未动态复现。

### 1.3 关键事实

- `nodes` 表只是观测缓存，非权威：`internal/domain/runtimeops/node/node.go:1-18`；`ByCarrier` 是握手锚定面：`internal/domain/runtimeops/node/repo.go:29-32`、`internal/infrastructure/persistence/node/repo.go:160-167`。
- Enrollment 目前同时返回 join 命令与 relay 命令，relay 脚本内嵌 join token：`internal/infrastructure/providers/swarm/runtime.go:604-655`、`:665-679`；k3s relay 命令恒空（集中形态）：`internal/infrastructure/providers/k3s/runtime.go:847-865`。
- k3s 的中继在 manager 进程内集中运行（每 k8s 节点一条回环 WS），没有节点侧常驻物：`internal/infrastructure/providers/k3s/noderelay.go:1-14`、`:42-107`。
- manager 回环中继由 engine 启动，`RunNodeRelay` 不带凭证回调，provider 自己读本地 token：`internal/application/engine/lifecycle.go:62-72`、`internal/assembly/provides.go:527-533`、`swarm/noderelay.go:336-344`。
- relay 容器挂 docker.sock，本身等价节点 root：ADR-0049:140-144；因此“拿到节点脚本”与“拿到节点 root”同面，不是本设计能消除的风险（见 §8）。

## 2. 约束

1. **双 Provider**：swarm 必须继续支持 worker 节点上的 relay 容器；k3s 必须继续支持 manager 进程内集中式中继（不能要求 k3s worker 安装常驻物）。
2. **enroll 材料轮换语义**：`rotate=true` 是泄漏处置，必须作废“全部”节点材料（join token + relay 凭证），不能只作废新节点。
3. **节点重建/迁移**：同一 carrier id 重连（重启、网络恢复）必须无需人工重新 enroll；平台节点 ID 永不复用（`internal/domain/shared/model/entityid/entityid.go:1-7`、ADR-0070）。
4. **无需外部 PKI**：不引入 CA/证书分发/ACME/SPIFFE；保持单二进制、单 SQLite、明文 VPC 传输现状（ADR-0049:92-95）。
5. **pre-release 无向后兼容负担**：可以要求升级后重新 enroll；旧 relay 容器在升级窗内失联是可接受的，但 runbook 必须写清。
6. **不改变 exec 会话语义**：同节点重连顶替、在途会话 `relay_disconnected` 收口、审计/事件面保持（ADR-0049）。
7. **不扩大平台面**：EnrollNode 仍是 `platform:admin`（`runtime.proto:43-53`、`contexts.go:82-88`）。

## 3. 方案矩阵

| 方案 | 形态 | 安全性 | 成本 | 结论 |
|---|---|---|---|---|
| A. 节点个体 relay 凭证 | 节点锚定后按 platform_id 铸造随机凭证；库内存 sha256 + carrier_id；relay 握手按凭证绑定节点；全局 rotate/单节点 revoke | sound：凭证不是集群共享串，且与节点身份在库内绑定；脚本泄漏只影响单节点 | M | **推荐** |
| B. 节点私钥签名 hello | enroll 发私钥，平台存公钥；hello 用私钥签名 + 平台 nonce | 若私钥同样经脚本分发，等价于“bearer + 签名”，脚本泄漏仍可冒充；需签名/重放/nonce 协议 | M-L | 放弃（见 3.2） |
| C. mTLS / SPIFFE | 节点证书、CA、轮换、TLS 终止、证书分发 | 强，但依赖外部 PKI/证书生命周期；与单二进制/无外部依赖/明文 VPC 现状冲突 | L-XL | 放弃（见 3.3） |
| D. 首次连接 TOFU 绑定 | 共享 join token 首次连接时把 carrier 绑定到该连接，后续要求 | 仍可被“先到者”抢绑；节点离线/重建窗口不可控；需要持久化连接态 | M | 放弃（见 3.4） |
| E. 继续 join token + 只校验 carrier | 现状 + 更严的 carrier 反查/二次确认 | 不解决“声明身份”根因；join token 泄漏即可顶替 | S | 放弃（见 3.5） |
| F. 关闭 relay/exec | 拒绝所有 `/v1/relay` | 安全但 exec 产品面整体不可用 | S | 仅紧急停服手段，不是方案 |

### 3.1 为什么 A 可行

- 凭证在**节点已锚定后**铸造：platform_id 与 carrier_id 都已在 `nodes` 表，铸造时即可绑定，不存在“先发凭证、后猜节点身份”的窗口。
- 存储只落 sha256：`identity.HashToken` 同款纪律（`internal/domain/identity/token.go:3-5,53-57`），DB 泄漏不能直接重放。
- 握手只读校验：`secret_sha256 → (platform_id, carrier_id)`，再与 `hello.carrier_node_id` 反查的节点行比对；无写路径、无 TOFU 竞态。
- 轮换/吊销是删/换行：旧哈希即刻不存在；`rotate` 删全表；单节点 revoke 删一行。
- 脚本只携带该节点凭证，不再携带 join token——顺带收窄 relay 脚本的泄漏面（原脚本含 join token，等于集群成员权）。

### 3.2 为什么 B 放弃

- 私钥必须送到节点；唯一现成通道仍是 EnrollNode 的脚本/响应。私钥进脚本后，脚本泄漏 = 私钥泄漏，攻击者仍可冒充该节点；与 A 的 bearer 等价，但多出签名、nonce、时钟/重放窗口、密钥格式与轮换代码。
- 若要求私钥住 TPM/宿主 keyring，超出“节点零平台安装物”和双 Provider 的现状，且 k3s 集中式中继没有节点侧进程可持有私钥。
- 结论：在没有硬件根信任的前提下，B 不提供比 A 更强的实际边界，只增加协议面。

### 3.3 为什么 C 放弃

mTLS/SPIFFE 需要：CA/信任根、节点证书签发与轮换、gateway TLS 终止、证书分发通道、双 Provider 的节点身份映射、备份/恢复时的证书材料。这与“单二进制/单 SQLite/无外部依赖”“明文 VPC 现状（ADR-0049:92-95）”直接冲突；且当前 gateway 前的 traefik WS 透传形态未承诺客户端证书。收益（传输层身份）可以用更小的 A 拿到应用层身份；若未来平台整体上 TLS，再把 A 迁移到 mTLS 是 only-add 的升级路径。

### 3.4 为什么 D 放弃

TOFU 的窗口：攻击者只要在真实节点首次连接前用共享 join token 抢绑任意 carrier，就能长期占用该节点身份；节点离线/重装后再把凭证换回来也没有“谁先”的可靠仲裁。需要额外的“人工批准/带外核对”才能补，实际上比 A 的两步 enroll 更麻烦。放弃。

### 3.5 为什么 E 放弃

“只校验 carrier”不构成认证：carrier id 对 `nodes:read` 可见，join token 是共享串；任何持有 token 的人都能声明任意 carrier。这是 H10 的根因，不能靠更严的反查修复。

## 4. 推荐方案与详细设计

**A：节点个体 relay 凭证（按锚定节点铸造、库内 sha256、握手绑定 platform_id + carrier_id）；join token 退出 `/v1/relay` 与 `/v1/platform/binary` 鉴权。** 全局 `rotate` 作废全部；单节点 `revoke`/重铸分立。

### 4.1 生命周期

```
nodes enroll（无参数）
  → Runtime.Enrollment → join_command（仅集群加入；relay_command 空）
node 执行 join → Watch/DescribeCluster 锚定 → nodes 行（platform_id, carrier_id）
nodes relay --node <platform_id>（平台管理员）
  → node.NewRelayCredential()（fltrelay_...，32B 熵）
  → RuntimeRelayMaterial.NodeRelayCommand(base, credential)（swarm；k3s 无此面 → 501）
  → 事务：UPSERT node_relay_credentials(platform_id, carrier_id, sha256) + 审计
  → 返回 relay_command（脚本内只含该节点凭证，不含 join token）
node 执行 relay_command
  → curl /v1/platform/binary（Bearer=该凭证）
  → fleetlyd relay --manager ... --relay-credential <凭证>
  → GET /v1/relay（Bearer=该凭证）→ hello.carrier_node_id=<本机 carrier>
  → 平台：sha256 查行 → ByCarrier(hello) → 比对 platform_id/carrier_id → 附着
重连：同一凭证；同节点顶替旧连接（在途会话 relay_disconnected）
rotate（平台管理员）→ 轮换 join tokens + DELETE 全部 relay 凭证 + 断开全部 relay
revoke --node（平台管理员）→ DELETE 该行 + 断开该节点 relay
node.left（连续缺失满 LeaveGrace=30s）→ DELETE 该行 + 断开该节点 relay
```

> **S18b 宽限窗（`4cfcc7fc`）**：单拍缺行只起算，窗内节点回归即清零——不删凭证/不断连/不发事件；连续缺失满 `NodeOptions.LeaveGrace`（默认 30s）才执行离场清理。S11b 的「单拍即删」语义已被本修订取代。

### 4.2 数据模型（迁移 00035）

下一迁移号 = **00035**（现有最新 `00034_idempotency_caller_scope.sql`；`internal/infrastructure/persistence/migrations/forward_test.go:28-30` 的 `latestMigrationVersion` 同步到 35）。

```sql
-- +goose Up
-- 节点个体 relay 凭证（H10 / R14-H3）：join token 是集群级共享串，
-- hello.carrier_node_id 又是客户端自报——持有 join token 者可顶替任意节点
-- 中继。本表把“凭证 → 节点身份”绑定落到平台侧：一个节点至多一条现役
-- 凭证；重铸 = UPSERT 替换 hash（旧凭证即刻失效）。
--
-- 不加 expires_at：凭证生命周期 = join token 同面（rotate/revoke/node.left
-- 才失效）；pre-release 无兼容负担。不落明文/盐：32B 随机串的 sha256 是
-- 查询键，熵足够，盐无收益（identity/token.go 同款）。
-- 不加 nodes 外键：nodes 是观测缓存（node/node.go:1-18），节点行可能先于
-- 凭证存在/长期保留 unavailable；凭证只在“节点已锚定”后铸造，行存在性
-- 由 handler 校验。
CREATE TABLE node_relay_credentials (
  platform_id   TEXT PRIMARY KEY,
  carrier_id    TEXT NOT NULL,
  secret_sha256 TEXT NOT NULL UNIQUE,
  created_at    TEXT NOT NULL
);

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
```

语义：

- `platform_id` 主键 = 单节点单现役凭证；重铸 `INSERT ... ON CONFLICT(platform_id) DO UPDATE SET secret_sha256=excluded.secret_sha256, carrier_id=excluded.carrier_id, created_at=excluded.created_at`。旧 hash 被覆盖 → 旧凭证永远查不到。
- 无 `revoked_at`：吊销 = DELETE。审计历史在 audit 面，不在本表。
- 无 `seq`/列表 API：不需要 UI 列表；`nodes list` 的 `relay_online` 已足够。

### 4.3 凭证材质与 domain/repo 面

`internal/domain/runtimeops/node`（与 `domain/identity/token.go` 的材质纪律同款，domain 零 I/O 的例外有先例；**nodes 表仍是观测缓存，凭证住独立表**）：

```go
const RelayCredentialPrefix = "fltrelay_" // 泄露扫描识别锚

type RelayCredentialMaterial struct {
    Secret string // fltrelay_ + base64url(32B)；只在铸造响应出现一次
    SHA256 string // hex；存储与查询键
    Prefix string // 前 12 字符；审计/日志
}

func NewRelayCredential() (RelayCredentialMaterial, error) // crypto/rand
func HashRelayCredential(secret string) string             // sha256 hex

type RelayCredential struct {
    PlatformID string
    CarrierID  string
    SHA256     string
    CreatedAt  string
}
```

`node.Repo` 增加（读面不返回明文）：

```go
PutRelayCredential(ctx context.Context, c *RelayCredential) error // UPSERT（重铸即替换）
RelayCredentialBySHA256(ctx context.Context, sha string) (*RelayCredential, error) // 缺行 state.ErrNotFound
DeleteRelayCredential(ctx context.Context, platformID string) (deleted bool, err error)
DeleteAllRelayCredentials(ctx context.Context) error
```

持久化实现：`internal/infrastructure/persistence/node/repo.go`；`RelayCredentialBySHA256` 单索引查询；`Put` 用 `ON CONFLICT(platform_id) DO UPDATE`。无 `SELECT *` 明文面。

### 4.4 Enrollment 回执形态与 proto 变更

`proto/fleetly/runtime/v1/runtime.proto`（only-add；响应字段语义变化在注释写清）：

```proto
message EnrollNodeRequest {
  // rotate 轮换 worker+manager join token，并作废全部节点 relay 凭证；
  // 响应只含 join_command（relay_command 空）。与 node_id/revoke 互斥。
  bool rotate = 1;
  // node_id 非空 = 针对该已锚定平台的 relay 材料动作（与 rotate 互斥）：
  //   revoke=false：铸新 relay 凭证（旧凭证即刻失效）并返回 relay_command；
  //   revoke=true ：只吊销该节点 relay 凭证，响应两命令皆空。
  string node_id = 2;
  bool revoke = 3;
}

message EnrollNodeResponse {
  // join_command 是工作节点完整加入命令（仅 swarm 形态）。
  string join_command = 1;
  // relay_command 是节点中继装载脚本（针对 node_id 铸造；含该节点个体凭证，
  // 不再含 join token）。初始 enroll / rotate 时为空：先 join，再按节点取。
  string relay_command = 2;
}
```

`capability`：

- `EnrollKit` 删除 `RelayCommand`（初始 enrollment 不再产中继材料）；保留 `Command/ManagerCommand/ExpiresAt`。
- 新增可选子面 `RuntimeRelayMaterial`：

```go
type RuntimeRelayMaterial interface {
    // NodeRelayCommand 渲染某节点的中继装载脚本；credential 是平台铸造的
    // 个体凭证明文（调用方负责落 sha256，本面不持久化、不读库）。
    NodeRelayCommand(ctx context.Context, credential string, o EnrollmentOptions) (string, error)
}
```

- `FacesOf` 增加 `RelayMaterial`；`ProviderFaces.Offered()` 增 `"relay_material"`（装配期诊断枚举面）；swarm 实现；k3s 不实现（集中形态无节点侧材料）。
- 凭证材质独立于 identity Token 体系：**不复用** `flt_`/`fltinv_` Token 表与前缀，新增 `fltrelay_` + `node_relay_credentials`。理由：节点凭证的生命周期/授权轴是“节点身份”，不是用户/团队身份；混进 identity Token 会把节点材料纳入用户 Token 的吊销/列表/审计语义，且 `tokens` 表带 team/user/role 外键，节点行没有这些轴。
- `ExecClusterToken` 从 `RuntimeExec` **删除**（join token 不再承担 relay 鉴权）；`ExecValidateClusterToken` 从 engine 删除，改 `ValidateRelayCredential`。
- `NodeRelayOptions`：`JoinToken func(ctx) (string,error)` → `Credential func(ctx, carrierNodeID string) (string,error)`（回环形态按 carrier 取内部凭证；worker 形态返回启动注入的固定凭证）。两 provider 都在确定 carrier 后调用（swarm 先 `docker info`，k3s 用 `nodeName`）。

`contexts.go` 的 `EnrollNode`：

1. `RequirePlatformAdmin` 与现有审计（`contexts.go:82-113`）。
2. 参数校验：`rotate && (node_id!="" || revoke)` → `E_INVALID_ARGUMENT`；`revoke && node_id==""` → 同。
3. `rotate=true`：`Runtime.Enrollment(ctx, true, opts)` → 事务 `DeleteAllRelayCredentials` + 审计 `node.rotate_join_tokens`（已有 action，Detail 加 `relay_credentials_revoked:true`）→ `Engine.DropAllRelays()` → 返回 join。
4. `node_id=""` 且非 rotate：`Runtime.Enrollment(ctx, false, opts)` → 返回 join（relay 空）。
5. `node_id!="" && revoke=false`：`Nodes.Get(node_id)`（404）→ `CarrierID==""` 或 `Role=="manager"`（swarm）→ `E_INVALID_ARGUMENT`（manager 的 relay 由进程内回环承载，节点侧脚本会造成双 relay 抖动；空 carrier 无法绑定）→ `RuntimeRelayMaterial` face（无 → `E_RUNTIME_FACE_UNSUPPORTED` 501）→ `NewRelayCredential` → `NodeRelayCommand` → 事务 `PutRelayCredential` + 审计 `node.relay_credential_issued`（Detail: carrier_id, prefix）→ `Engine.DropRelay(node_id)`（旧连接立即失效）→ 返回 relay。
6. `node_id!="" && revoke=true`：事务 `DeleteRelayCredential`（返回 `deleted`；无行 = 幂等成功，不写审计）+ 审计 `node.relay_credential_revoked`（仅 `deleted` 时；Detail: carrier_id, reason="manual"）→ `Engine.DropRelay(node_id)` → 返回空响应。**revoke 不需要 `RuntimeRelayMaterial` face**（k3s 无节点凭证时是幂等空删；runtime 换届后也可清理存量行），只有 `revoke=false` 的铸造需要 face。

顺序与失败语义：provider 脚本渲染在 DB 事务之前（无副作用）；事务失败不返回脚本。`rotate` 的 Runtime 轮换是外部副作用，先于 DB 事务；事务失败时 join token 已轮换、旧 relay 凭证仍在（重试 rotate 或逐节点重铸均可收敛），错误如实上抛。

### 4.5 relay 握手校验顺序

`internal/assembly/gateway_exec.go`：

```
HTTP 层（升级前）
  ValidateRelayCredential(bearer):
    - 空 → 401
    - internal token（控制面进程内中继）→ 通过
    - sha256 查 node_relay_credentials 行存在 → 通过
    - 否则 401（"credential is not an active node relay credential"）
  ↓
WebSocket 升级
  ↓
首帧 hello（15s 超时，1MiB 读限）
  - type=="hello"、carrier_node_id 非空、relay_version 有界
  ↓
RelayAttach(token, carrierID, version, writer):
  1. execFace 缺席 → ErrExecUnsupported
  2. Nodes.ByCarrier(carrierID) → 缺 → ErrExecNodeUnanchored
  3. token == internal token（constant-time）→ 信任（控制面自身）；跳到 6
  4. sha256 查行 → 缺 → ErrExecRelayUnauthorized
  5. row.PlatformID == node.PlatformID && row.CarrierID == node.CarrierID
     → 否则 ErrExecRelayUnauthorized（伪造/串号）
  6. 同节点重连：替换 relays[platformID]；已附着会话 relay_disconnected；
     未附着会话重指新连接（现有语义，execrelay.go:32-55）
```

错误呈现（区分“凭证错”与“平台存储故障”，避免把故障伪装成 401）：

- 升级前坏凭证 → HTTP 401 `bad_credential`（与现状同面）。
- DB/存储故障（`RelayCredentialBySHA256` 返回非 `state.ErrNotFound` 错误）→ HTTP 503 `relay_unavailable`；不能落成 401（否则凭证轮换/存储故障不可区分）。
- 凭证合法但 carrier 不匹配 → 升级后 WS close `StatusPolicyViolation`（1008）`attach rejected`；`slog.Warn` 只记 node/carrier/版本，不记凭证。
- 节点未锚定 → close `"node not anchored yet; retry shortly"`；节点代理退避重连（现有行为）。
- `RelayAttach` 的 DB 故障返回内部错误并 close 1011；节点代理退避。

**内部凭证（manager 回环 / k3s 集中式）**：

- engine 启动时用 `crypto/rand` 生成 32B 内部 token（`fltint_` + base64url），只存内存；0 化/丢进程即失效。
- `Engine.RelayInternalCredential(ctx, carrierID)` 提供给 `NodeRelayOptions.Credential`；swarm manager 回环与 k3s 每节点回环都从同一 engine 进程取。
- 内部 token 可在 hello 中声明任意**已锚定**节点——这是控制面进程自身的信任面（同一进程，无网络凭证泄漏面）；不写库、不出现在日志/脚本。若进程被攻破，exec 全链已失守，不构成新增边界。
- `fleetlyd relay --runtime k3s` 独立进程形态不再支持（无内部 token，也无节点侧凭证）；k3s relay 只在 manager 进程内运行（与 ADR-0053 集中形态一致）。

### 4.6 轮换/吊销语义

| 动作 | join token | relay 凭证 | 在连 relay | 在途会话 |
|---|---|---|---|---|
| `nodes enroll`（初始） | 不变，返回 join | 不变 | 不变 | 不变 |
| `nodes relay --node X`（重铸） | 不变 | 只替换 X 的 hash（旧即刻失效） | 断开 X 的旧连接（新脚本重连后替换） | X 上在途会话 `relay_disconnected` |
| `nodes revoke-relay --node X` | 不变 | 删除 X 行 | 断开 X | 同上 |
| `nodes enroll --rotate` | 轮换 worker+manager | 删除全部行 | 断开全部 relay | 全部 `relay_disconnected` |
| `node.left`（节点连续缺失满 LeaveGrace） | 不变 | 删除该节点行 | 断开该节点 | 同上 |

实现：

- `Engine.DropRelay(platformID)`：从 `relays` 摘除并 `finish` 其名下全部会话（复用 `dropRelay`，`execrelay.go:123-145`）。
- `Engine.DropAllRelays()`：遍历快照逐个 drop；在 DB 事务提交后调用（先让凭证失效，再断连接；即使断开失败，重连也会 401）。
- 即时断开是“泄漏处置”语义；与现状 rotate 只影响下次重连不同，属于有意加强（S11 已裁决即时；见 §8）。
- 重铸 X 时旧连接断开后，节点上的旧脚本容器（restart unless-stopped）会用旧凭证重连 → 401 → 退避；运营者执行新脚本（`docker rm -f fleetly-relay` + 重建）后恢复。`nodeRelayScript` 已是幂等重建脚本（`swarm/runtime.go:665-679`）。
- **重铸即断旧连接**：`nodes relay --node X` 一返回，X 的现有 relay 就会被 `DropRelay`；脚本必须在节点上立即执行（CLI/Console 文案明确“先取脚本、就地执行”，不要把重铸当作只读预览）。

### 4.7 双 Provider 差异

| 面 | swarm | k3s |
|---|---|---|
| 节点侧 relay | worker 常驻 busybox 容器（`swarm/noderelay.go`、`runtime.go:665-679`） | 无节点侧常驻物；manager 进程内每节点一条回环（`k3s/noderelay.go:42-107`） |
| 凭证来源 | `fleetlyd relay --relay-credential`（脚本注入） | `NodeRelayOptions.Credential` 返回 engine 内部 token |
| Enrollment relay 材料 | 有 `RuntimeRelayMaterial` 面；`NodeRelayCommand` 按锚定节点渲染；manager 节点拒绝定向材料（回环已覆盖） | 无（`FacesOf.RelayMaterial == nil`；定向请求 501） |
| rotate | 轮换双 join token + 删全部 relay 凭证 | 现有 rotate 已显式失败（`k3s/runtime.go:853-856`）；不涉及 relay 凭证 |
| `/v1/platform/binary` | 节点脚本下载 fleetlyd，Bearer=该节点凭证 | manager 进程不需要下载（二进制已在本机） |
| 安全边界 | 节点脚本泄漏 = 单节点 relay 身份（节点 root 可读，见 §8） | 无外部 relay 凭证可窃取；控制面进程被攻破则全失守（与现状同） |

`RuntimeRelayMaterial` 的 swarm 实现复用 manager 地址解析：

```go
func (p *Provider) NodeRelayCommand(ctx context.Context, credential string, o capability.EnrollmentOptions) (string, error) {
    if o.GatewayPort == "" { return "", fmt.Errorf("relay material needs the gateway port") }
    addr, err := p.managerAddr(ctx) // 从 NodeList 取 ManagerStatus.Addr
    if err != nil { return "", err }
    return nodeRelayScript("http://"+managerHost(addr)+":"+o.GatewayPort, credential), nil
}
```

`nodeRelayScript` 改为：

```text
t=$(mktemp) && { curl -sfL -H 'Authorization: Bearer <credential>' -o $t <base>/v1/platform/binary || wget ...; } && \
docker rm -f fleetly-relay >/dev/null 2>&1; \
c=$(docker create --name fleetly-relay --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock --entrypoint sh busybox:1.37 -c 'chmod +x /tmp/fleetlyd && exec /tmp/fleetlyd relay --manager <base> --relay-credential <credential>') && \
docker cp $t $c:/tmp/fleetlyd && docker start $c && rm -f $t
```

脚本形态约束保持（纯单引号、无空格 mktemp 路径、幂等重建）；`cmd/fleetlyd/relay.go` 旗标：

- `--manager` 不变；
- `--join-token` → `--relay-credential`（usage/synopsis/测试同批）；
- `NodeRelayOptions.Credential` 返回固定 flag 值。

### 4.8 节点离线/重装/迁移

- **重启/网络恢复**：同一 carrier id、同一凭证 → 重连成功；`RelayAttach` 顶替旧连接（现有语义）。
- **新节点首次取 relay**：join 后要等 Watch/DescribeCluster 锚定（`nodes list` 出现该 platform_id）才能 `nodes relay --node`；未锚定返回 404，重试即可（锚定延迟 = 对账节拍）。
- **节点重装（新 carrier id）**：join 后 Watch 铸**新 platform_id**（节点 ID 永不复用），旧行保留 unavailable；对新节点 `nodes relay --node <新 id>` 铸造新凭证。旧凭证在 `node.left` 或 rotate 时删除；即使残留，`ByCarrier(旧 carrier)` 仍指向旧 platform_id，而旧节点没有可路由的 workload，不能收到他队会话。
- **节点迁移（同 carrier id）**：理论上同一 swarm node 重启后 carrier 不变；凭证仍绑定 same platform_id/carrier，重连即用。
- **节点离线期凭证泄漏**：`revoke-relay` 可立即删行（无需节点在线）；节点恢复后旧凭证 401，需重铸。
- **k3s 节点**：无节点侧凭证；k8s 节点增删由集中式 sweep 处理（`k3s/noderelay.go:61-97`），`node.left` 清理其（不存在的）凭证行是 no-op。

### 4.9 审计/事件/日志

| 动作 | audit action | 面 | Detail |
|---|---|---|---|
| 初始 enroll | `node.enroll`（已有） | LedgerOnly（已有理由） | 可加 `{"relay_material":"none"}`（可选） |
| rotate | `node.rotate_join_tokens`（已有） | LedgerOnly（已有理由） | `{"relay_credentials_revoked":true}` |
| 定向重铸 | **`node.relay_credential_issued`**（新） | LedgerOnly（理由：auth 材料铸造不是聚合状态迁移，无 outbox 孪生事件） | `{carrier_id, prefix}` |
| 定向吊销 | **`node.relay_credential_revoked`**（新） | LedgerOnly（理由同前；仅实际删除行时发） | `{carrier_id, reason:"manual"}` |
| node.left 清理 | `node.relay_credential_revoked` | LedgerOnly | `{carrier_id, reason:"node_left"}` |
| 同节点二次附着 | **`node.relay_replaced`**（新） | LedgerOnly（理由：连接活体事件不是聚合迁移；仅 `old != nil` 时发，人工可见告警） | `{version}` |

- 绝不把明文/完整 sha256 写 audit：只写 `prefix`（前 12 字符）与 carrier/platform ID。
- `node.relay_replaced` 与 `slog.Warn("node relay replaced while previous connection live", "node", ...)` 同发；正常断线重连（old 已被 drop）不触发。
- factdual 守卫：三个 `LedgerOnly` 声明处必须带偏离理由注释（`internal/guards/factdual_test.go` 的纪律）；本设计已写明理由。

### 4.10 CLI / Console / runbook 触点

**CLI**（`cmd/fleetly/cmd/verbs_delivery.go` 的 nodes 组）：

- `nodes enroll [--rotate]`：人类输出仍只打 join 命令；JSON 的 `relay_command` 为空（protojson 省略默认值）。
- 新增 `nodes relay --node ID`：调 `EnrollNode{node_id}`，人类输出 relay 脚本（敏感；仅平台管理员可取；stdout 提示“treat this script as node-root material”），`--json` 输出 protojson。
- 新增 `nodes revoke-relay --node ID`：调 `EnrollNode{node_id, revoke:true}`，输出 `relay credential revoked for node <id>`。
- 组列表 golden（`nodes.golden`/`nodes-json.golden`）更新；`cmd/fleetly/cmd/identity_golden_test.go:14` 的 `secretRe` 加 `fltrelay_` 前缀（防未来 golden 误把节点凭证打进出货面）。golden 夹具的现有唯一节点 `node-1` 是 `Role=manager`（`internal/testsupport/apitest/harness.go:598-600`），`nodes relay` 步骤前需在 `Services.Nodes` 种一行 `Role=worker` 的节点；否则会按 manager 拒绝（这同时覆盖该拒绝分支的负例）。

**Console**（`console/src/routes/_shell/nodes.index.tsx`）：

- 初始 Enrollment 对话框：只展示 join 命令 + 提示“节点锚定后，在节点行取 relay 命令”。
- 节点行新增两个动作（可见性沿用现状：非平台管理员调用 403 toast）：
  - `Relay command`：`POST /v1/nodes/enroll {node_id}` → 展示脚本；
  - `Revoke relay`：`POST /v1/nodes/enroll {node_id, revoke:true}` → 确认后吊销。
- Rotate 确认文案改为：“Rotate join tokens and revoke all node relay credentials; connected relays drop. Reissue relay commands per node.”
- `console/src/api/runtime.ts` 由 `console:gen` 更新（`node_id`/`revoke` 字段）。

**Runbook**（`docs/runbooks/staging-fleetly.md`）：

- `:383-390` 段：把“RelayCommand 原样执行”改为两步：join → `nodes relay --node <platform_id>` → 在节点执行输出的脚本；记录“旧 relay 容器/旧 join token 脚本在升级后 401”。
- 所有升级序里“worker 重跑 RelayCommand”改为“逐 worker 重铸并重跑 relay 命令”。
- rotate 段（`:806` Console 说明）更新为“join token + relay 凭证全量作废、连接即时断开、逐节点重铸”。
- 新增故障排查：`relay_online=false` 先看 `nodes revoke-relay`/`nodes relay` 的审计与节点容器日志；`401 bad_credential` 表示凭证已轮换或节点行被删。

## 5. 迁移/兼容

- **schema 迁移号**：`00035_node_relay_credentials.sql`（下一号；`00034` 为当前最新）。`forward_test.go` 的 `latestMigrationVersion` 改 35；无 backfill（表为空起步）。
- **proto only-add**：`EnrollNodeRequest.node_id/revoke`；`EnrollNodeResponse` 字段号不变、语义注释更新；`Node` 消息不变（`carrier_id` 仍可读——H10 修复后它不是秘密）。
- **老节点过渡**：pre-release 直接要求重新 enroll，**不设兼容窗口**（用户 2026-10-14 裁决）。控制面升级后：
  1. 旧 worker relay 容器继续用 join token → `/v1/relay` 401，`relay_online=false`；
  2. 对每个 worker：`nodes relay --node <platform_id>` → 在节点执行新脚本；
  3. manager 回环 / k3s 集中式自动用内部 token，无需动作。
- **不迁移旧 relay 凭证**：旧材料（join token）不再被 relay 接受，迁移无对象。
- **runbook/e2e 同批**：`e2e/dind-two-node.sh:238-284` 的 exec 腿必须改成新流程，否则 CI 红；这是刻意的“文档-代码-测试同批”纪律。
- **回滚**：回退 migration 不提供 Down（goose 只前滚）；回退代码后旧 join token relay 恢复可用（安全回退面存在）。若已执行 rotate，join token 也换了，回退后旧 relay 仍不可用，需要重新 enroll——这是 rotate 的既有语义。

## 6. 验收锚（逐条可测）

- [ ] **伪造 carrier id 拒**：`go test ./internal/application/engine -run TestRelayAttachRejectsOtherCarrier -count=1`。节点 A 凭证 + hello `carrier=B` → `ErrExecRelayUnauthorized`（或 `ErrExecNodeUnanchored`，若 B 未锚定另测）；`e.relays` 中 B 无项；已有 A 连接不受影响。
- [ ] **join token 不再能附着**：`TestRelayAttachRejectsJoinToken`：用 swarm join token 字符串（或 FakeRuntime 的 `TESTTOKEN`）调 `RelayAttach` → `ErrExecRelayUnauthorized`；`ValidateRelayCredential` 对 join token 返回错。e2e：`curl -H 'Authorization: Bearer <join token>' /v1/platform/binary` → 401。
- [ ] **合法节点重连顶替**：`TestRelayAttachReplacesSameNode`：凭证/节点 A 首次 attach；再 attach 同凭证同 carrier → 旧连接被替换、旧连接上已附着会话收到一次 `relay_disconnected`、新连接收到后续帧。
- [ ] **未锚定节点拒**：`TestRelayAttachRejectsUnanchoredCarrier`：凭证行存在但 `ByCarrier` 无行 → `ErrExecNodeUnanchored`。
- [ ] **rotate 全量作废**：apitest `TestEnrollNodeRotateRevokesAllRelayCredentials`：两节点各一凭证；owner `EnrollNode{rotate:true}` → 两凭证 `RelayAttach` 均拒、`e.relays` 清空、响应 `JoinCommand` 非空且 `RelayCommand` 空；旧 join token 也不再被 relay 接受。
- [ ] **目标节点轮换只影响目标**：`TestEnrollNodeRelayMaterialTargetsNode`：A/B 各一凭证；`EnrollNode{node_id:A}` → A 旧凭证拒、A 新凭证可用、B 旧凭证仍可用；审计 `node.relay_credential_issued` 的 Detail 含 A 的 carrier/prefix，不含明文。
- [ ] **单节点吊销**：`TestEnrollNodeRevokeRelayCredential`：`EnrollNode{node_id:A, revoke:true}` → A 行删除、A 旧凭证拒、B 不受影响；响应两命令空；审计 `node.relay_credential_revoked`。
- [ ] **回执形态**：`TestEnrollNodeInitialReturnsJoinOnly`：初始 enroll `RelayCommand==""`；`TestEnrollNodeRelayMaterialRequiresAnchoredNode`：未知 node_id → `E_NOT_FOUND`；`TestEnrollNodeRelayMaterialRejectsCombos`：rotate+node_id / revoke 无 node_id → `E_INVALID_ARGUMENT`；`TestEnrollNodeRelayMaterialK3sUnsupported` → `E_RUNTIME_FACE_UNSUPPORTED`（501）；`TestEnrollNodeRelayMaterialRejectsManagerNode` → `E_INVALID_ARGUMENT`。
- [ ] **凭证不落明文**：`TestRelayCredentialHashAtRest`：`PutRelayCredential` 后 `RelayCredentialBySHA256(secret)` 查不到；`RelayCredentialBySHA256(HashRelayCredential(secret))` 命中；表行 `secret_sha256` 为 64 hex，不等于明文；`SELECT` 全行不含 `fltrelay_`。
- [ ] **双 Provider 单测**：`go test ./internal/infrastructure/providers/swarm -run 'TestNodeRelayScript|TestNodeRelayCommand'`：脚本含 `--relay-credential fltrelay_...`、不含 `SWMTKN`；`go test ./internal/infrastructure/providers/k3s -run TestEnrollment`：join 命令仍用 node token、`FacesOf.RelayMaterial == nil`、`RunNodeRelay` 使用 `NodeRelayOptions.Credential`（fake callback 捕获 nodeName）。
- [ ] **CLI golden**：`go test ./cmd/fleetly/cmd -run TestGoldenNodes`：`nodes relay --node`/`nodes revoke-relay --node` 的 usage、缺参 64、`--json` 形态、组列表；`go test ./cmd/fleetlyd`：`--relay-credential` 必填、缺参 2、帮助含新旗标。
- [ ] **e2e 双节点**：`mise run e2e:twonode`（或 `sh e2e/dind-two-node.sh`）：join 后按 `nodes relay --node <worker platform id>` 取脚本并执行 → 双节点 `relay_online=true`；脚本不含 `SWMTKN`；用 join token 打 `/v1/platform/binary` → 401；exec 全链仍通过；`nodes enroll --rotate` 后旧 relay 失联、按节点重铸后恢复（若本阶段 e2e 不含 rotate，至少手工/apitest 覆盖）。
- [ ] **迁移前进**：`go test ./internal/infrastructure/persistence/migrations -run TestMigrateForwardFromEra28ToLatest`：00035 在场、era 行无损。
- [ ] **守卫**：`go test ./internal/guards -count=1`（coverage 需覆盖新 runtime face；factdual 三个 LedgerOnly 理由注释；writeclass/scopeguard 不变）。
- [ ] **真机/staging**：按 runbook 升级一台 worker，`fleetly nodes list --json` relay_online 恢复；`fleetly exec` 双节点各一条；旧 join token curl 401；日志中 `node relay replaced` 仅在重复连接时出现。

## 7. 分步实施计划

> 两阶段；阶段 1 关闭攻击链，阶段 2 补齐运维/真机面。

### 阶段 1（M）：凭证面 + 握手 + 定向材料

改动：

- `migrations/00035_node_relay_credentials.sql` + `forward_test.go` 版本号。
- `domain/runtimeops/node`：材质生成/hash、`RelayCredential`、Repo 四方法；persistence 实现 + 测试。
- `capability`：删 `ExecClusterToken`；`EnrollKit` 删 `RelayCommand`；新增 `RuntimeRelayMaterial` + `FacesOf`；`NodeRelayOptions.Credential` 签名。
- providers：swarm/k3s 删除 `ExecClusterToken`/`readNodeToken` 的 relay 用途；`Credential` 回调；swarm `NodeRelayCommand`；`nodeRelayScript` 改凭证；`Enrollment` 仅 join。
- engine：内部 token + `ValidateRelayCredential` + `RelayAttach` 凭证绑定校验 + `DropRelay/DropAllRelays`（rotate/重铸/吊销即时断连；本阶段完成）；删除 `ExecValidateClusterToken`；生命周期回环回调。
- gateway：`/v1/relay` 与 `/v1/platform/binary` 换鉴权；hello 后 attach。
- handler `EnrollNode`：rotate/node_id/revoke 三分支；审计；脚本渲染；事务。
- CLI：`nodes relay`、`nodes revoke-relay`、`fleetlyd --relay-credential`；golden。
- Console：初始 enroll 材料对话框改为“仅 join + 提示按节点取 relay”；`console:gen`（runtime `node_id`/`revoke` 字段）。节点行两个动作留阶段 2。
- 测试：§6 的 engine/repo/apitest/provider/CLI/migration 项。

验收：§6 前 9 条 + CLI/migration；`go test ./internal/application/engine ./internal/infrastructure/providers/... ./internal/testsupport/apitest ./cmd/fleetly/... ./cmd/fleetlyd`。

### 阶段 2（S）：轮换/生命周期 + 真机/运维面

改动：

- rotate/revoke 的即时断连已在阶段 1 落地；本阶段不重复实现，只验证行为。
- `reconcileNodes`：`node.left` 删除凭证 + `DropRelay` + 审计（`internal/application/engine/nodes.go`；凭证清理独立于 `node.left` 事件发射成功与否——先清安全材料，事件失败只记日志，不把清理挂在事件 `continue` 之后）。**S18b 起**：节点连续缺失满 `NodeOptions.LeaveGrace`（默认 30s）才执行清理，单拍缺行只起算（窗内回归不处置）。
- `RelayAttach` 的 `old != nil` 二次附着 `slog.Warn` + `node.relay_replaced` 审计。
- `e2e/dind-two-node.sh` exec 腿改新流程 + join-token 401 负例 + rotate 恢复腿。
- `docs/runbooks/staging-fleetly.md` 全量更新（enroll 两步、升级重跑、rotate、故障排查）。
- ADR-0049 追记（鉴权模型改写）、ADR-0052/0053 补充 Provider 差异；checklist/评审状态更新。

验收：§6 全部；`mise run e2e:twonode`；staging 双节点。

## 8. 风险与未决

| 风险/未决 | 影响 | 处置/验证 |
|---|---|---|
| **两步 enroll 的运营成本**（join → 等锚定 → 按节点取 relay） | 每加一个 worker 多一次命令；升级序多一步 | **已裁决接受（S11 落地）**：这是消除 TOFU/抢绑的唯一干净形态；Console 节点行动作把第二次变成一次点击；runbook 升级序已列「逐 worker 重铸」。 |
| **rotate/重铸是否即时断开在连 relay** | 现状 rotate 只影响“下次重连”；即时断开会让在途会话立即失败 | **已裁决即时（S11 落地）**：泄漏处置立刻失效；会话收到 `relay_disconnected` 诚实告知。 |
| 脚本内凭证可被节点 root 读取 | 节点 root 本就有 docker.sock/容器 root，等于已控节点；无法在不引入硬件根信任下消除 | 接受（ADR-0049:140-144 同款边界）；控制面只保证“脚本泄漏 ≠ 集群 join token 泄漏” |
| join token 仍可让恶意节点加入 swarm | H10 只关闭 relay 顶替腿，不关闭“集群成员权”本身（C3） | 保留为独立议题；H1/H2 平台门禁已修（S2），join token 分发仍按平台管理员面管理 |
| 内部 token 可声明任意已锚定节点 | 控制面进程自身信任面；若进程被攻破 exec 已全失守 | 接受；内存 only、无日志/无脚本；不写库 |
| k3s 无节点侧 relay 材料 | 定向 `node_id` 在 k3s 返回 501；运营者不能给 k3s 节点发 relay 脚本 | 符合 ADR-0053 集中形态；文档写清“k3s 无需此动作”；若未来 k3s 节点侧代理化，再补实现 |
| `node.left` 抖动误删凭证 | **已修（S18b `4cfcc7fc`）**：单拍缺行只起算，连续缺失满 `NodeOptions.LeaveGrace`（默认 30s）才删凭证/断连；窗内回归不处置（不删/不断/不发事件） | 宽限窗钉测在 `internal/application/engine/nodes_test.go`；runbook「node.left 抖动风险」同步改口 |
| 升级窗内 worker exec 不可用 | 旧 relay 容器 401，直到逐节点重跑新脚本 | pre-release 接受；runbook 把“逐 worker 重铸”列为升级硬步骤；manager 回环不受影响 |
| 旧 relay 脚本被缓存/重放 | 旧 join token 脚本在升级后 401；旧单节点凭证脚本在重铸后 401 | 重铸/rotate 后旧脚本自然失效；e2e/审计可验证 |
| Console 动作对非管理员 403 | 与现有 enroll/rotate 行为一致 | **已做（S11b `f756ed70`）**：`nodes-list.tsx` 按 `whoami.platform_admin` fail-closed 显隐平台动作；非管理员沿用现有 403 toast。 |

## S18 追记（2026-10-19，`07128fef` + `4cfcc7fc`）

- **project 级 scoped 指标（S18a）**：v1 做，走 raw 双臂而非词汇扩展（详见 H8 §9 行与附录 C）；白名单 `cpu_cores`/`memory_working_set_bytes`。
- **Login 会话 Token TTL（S18a，R14-M1 另一半）**：`auth.login_token_ttl` 默认 `720h`，`0` = 不过期（逃生门）；**只作用于 Login 铸造的密码会话 Token**——bootstrap 与 `tokens create`/CLI/SDK 铸造的 Agent/CI Token 永不过期。authn 逐请求查表判定（过期即 `E_UNAUTHENTICATED`，suggestion 指向重新登录）。过期行当前**无清扫**：每次登录新增一行、不构成新增泄漏面，列为后续清理项（触发：token 表容量观测/清理批）。
- **node.left 宽限窗（S18b）**：`NodeOptions.LeaveGrace` 默认 30s；单拍缺行只起算，满窗才删凭证/断连（H10 §4.1/§4.6/§8 已同批改口）。

---

# 附录 A：改动面清单（供 handoff）

> 按阶段列；`*` 表示生成物禁止手改（走 mise 任务）。

## H8

**阶段 A**

- `internal/interfaces/grpc/fleetlygrpc/alerting.go`（raw 平台门 + 窗口 helper）
- `internal/infrastructure/providers/victoriametrics/provider.go`（`queryRange` 体/序列/超时上限）
- `internal/interfaces/grpc/fleetlygrpc/mapping.go`（两个新 code）
- `internal/domain/shared/model/errcode/codes.go` + `testdata/codes.golden`
- `internal/interfaces/grpc/fleetlygrpc/alerting_test.go` / apitest 新用例

**阶段 B**

- `proto/fleetly/telemetry/v1/telemetry.proto`（RPC/messages）、`proto/fleetly/identity/v1/identity.proto`（`platform_admin`）
- `genproto/** *`、`console/src/api/telemetry.ts *`、`console/src/api/identity.ts *`、`console/dist/** *`
- `internal/domain/shared/capability/ports.go`、新增 `metricsquery.go`（builder/limits/常量）
- `internal/application/engine/exposition.go`（指标名 alias/引用）
- `internal/infrastructure/providers/victoriametrics/provider.go` + `provider_test.go`
- `internal/interfaces/grpc/fleetlygrpc/alerting.go`（scoped handler）、`identity.go`（WhoAmI 字段）、`mapping.go`
- `internal/testsupport/apitest/harness.go`（NewWithMetrics seam）、新增 `metrics_scoped_test.go`；三个 fake Metrics
- `cmd/fleetly/cmd/verbs_alerting.go`、`app.go`、`alerting_golden_test.go`、`testdata/golden/metrics*`、`identity*`
- Console：`features/metrics/metric-presets.ts`、`metric-chart.tsx`（truncated）、`features/apps-tabs/app-metrics.tsx`、`app-overview.tsx`、`routes/_shell/metrics.tsx`、`features/databases/database-metrics.ts`、`databases.tsx`、`lib/catalog.ts`、tests
- `docs/adr/0041-*`、`docs/adr/0065-*`（追记；实施时）

**阶段 C**

- `e2e/dind-two-node.sh`（metrics scoped 段）
- staging 验证记录（不是代码）
- Console truncated 提示打磨（若阶段 B 未做）

## H10

**阶段 1**

- `internal/infrastructure/persistence/migrations/00035_node_relay_credentials.sql`、`forward_test.go`
- `internal/domain/runtimeops/node/node.go`、`repo.go`（材质/类型/接口）
- `internal/infrastructure/persistence/node/repo.go` + test
- `internal/domain/shared/capability/exec.go`（删 `ExecClusterToken`、`NodeRelayOptions.Credential`）、`runtime.go`（`EnrollKit`、`RuntimeRelayMaterial`、`EnrollmentOptions` 注释）、`faces.go`
- `internal/infrastructure/providers/swarm/{exec.go,noderelay.go,runtime.go}` + tests（含 `var _ capability.RuntimeRelayMaterial = (*Provider)(nil)`）
- `internal/infrastructure/providers/k3s/{exec.go,noderelay.go,runtime.go}` + tests（`exec_test.go` 的 `TestExecClusterToken` 删除/改为 `Enrollment` join 形态 + `FacesOf.RelayMaterial==nil`）
- `internal/application/engine/{exec.go,execrelay.go,lifecycle.go,nodes.go}`（内部 token/校验/attach/drop/清理）
- `internal/assembly/gateway_exec.go`（两入口鉴权 + attach）
- `internal/testsupport/apitest/{harness.go,execrelay.go}`（FakeRuntime 去 `ExecClusterToken`/`RelayCommand`、加 `NodeRelayCommand`；`AttachFakeRelay` 先种 `node_relay_credentials` 行再用 `TESTTOKEN`）；`internal/application/engine/exec_test.go` fakeExecFace 同步
- `proto/fleetly/runtime/v1/runtime.proto`、`genproto/** *`、`console/src/api/runtime.ts *`
- `internal/interfaces/grpc/fleetlygrpc/contexts.go`（EnrollNode 三分支 + 审计）
- `cmd/fleetlyd/relay.go`、`main_test.go`；`cmd/fleetly/cmd/verbs_delivery.go`、golden
- `console/src/routes/_shell/nodes.index.tsx`（初始 material 文案 + `console:gen` 类型）
- `e2e/dind-two-node.sh`（阶段 2 完整；阶段 1 至少脚本形态/401）
- `docs/runbooks/staging-fleetly.md`
- `docs/adr/0049-*`（追记）

**阶段 2**

- `internal/application/engine/nodes.go`（node.left revoke/drop/audit + LeaveGrace 宽限窗，S18b）
- `internal/application/engine/execrelay.go`（二次附着 warn/audit）
- `console/src/routes/_shell/nodes.index.tsx`（节点行 Relay/Revoke 动作 + rotate 文案）
- `e2e/dind-two-node.sh`（rotate 恢复腿、负例）
- `docs/runbooks/staging-fleetly.md`（全量）
- ADR-0052/0053 补充、checklist

# 附录 B：验证命令速查

```sh
# 生成物 / 契约
mise run generate:all
mise run generate:verify
mise run console:gen && mise run console:build
mise run console:verify

# H8
go test ./internal/domain/shared/capability -run 'TestScopedMetrics|TestBuildScopedPromQL|TestClampScopedStep' -count=1
go test ./internal/infrastructure/providers/victoriametrics -run 'TestQueryScoped|TestQuerySeries' -count=1
go test ./internal/testsupport/apitest -run 'TestQueryScopedMetrics|TestQueryMetricsPlatformAdmin' -count=1
go test ./internal/interfaces/grpc/fleetlygrpc -run 'TestMapMetricsErrors|TestWhoAmI' -count=1
go test ./cmd/fleetly/cmd -run 'TestGoldenAlertingVerbs|TestGoldenIdentityVerbs' -count=1
cd console && pnpm test

# H10
go test ./internal/application/engine -run 'TestRelay' -count=1
go test ./internal/infrastructure/persistence/node -run 'TestRelayCredential' -count=1
go test ./internal/infrastructure/providers/swarm ./internal/infrastructure/providers/k3s -count=1
go test ./internal/testsupport/apitest -run 'TestEnrollNode' -count=1
go test ./internal/infrastructure/persistence/migrations -run TestMigrateForwardFromEra28ToLatest -count=1
go test ./cmd/fleetly/cmd -run TestGoldenNodes -count=1
go test ./cmd/fleetlyd -count=1
sh -n e2e/dind-two-node.sh
mise run e2e:twonode

# 全套门禁（两批各自收尾）
go test ./internal/guards -count=1
mise run test
mise run lint
```

# 附录 C：本设计的明确不做（Non-goals）

H8：

- 不做**词汇级** project 标签（S18a `07128fef` 已用 raw 双臂承载 project scope：cAdvisor 投影标签 + k3s namespace；ADR-0065 词汇保持无 project 维度）。
- 不新增 PromQL 解析器、不做 PromQL 标签重写。
- 不拆/迁移 VictoriaMetrics，不做 per-tenant 存储路由。
- 不在本批修 `ListAlertRules`/`ListAlertStates` 的 Team 过滤（R08-M2 余项，单列小批；见 §9）。
- 不改告警评估语义（继续内存样本评估）。

H10：

- 不引入 mTLS/SPIFFE/外部 PKI，不改明文 VPC 传输口径。
- 不做 TOFU 首次连接绑定，不设兼容窗口。
- 不改变 exec 会话语义、限额、票据、审计主体（会话面零改动）。
- 不把 `carrier_id` 从 `ListNodes` 移除（修复后它不是秘密；节点观测面保留）。
- 不给 k3s 增加节点侧常驻代理（保持集中式）。
- 不把 relay 凭证做成可回显的列表接口（只写/只铸造；DB 只存 hash）。
