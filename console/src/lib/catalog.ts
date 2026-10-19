import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "../api/client";
import { listExecInstances } from "../api/execstream";
import type { components as structureSchemas } from "../api/structure";
import type { components as deliverySchemas } from "../api/delivery";
import type { components as automationSchemas } from "../api/automation";
import type { components as identitySchemas } from "../api/identity";
import type { components as telemetrySchemas } from "../api/telemetry";
import type { components as proxySchemas } from "../api/proxy";
import type { components as runtimeSchemas } from "../api/runtime";

// 目录与资源查询面（F2.6 三页选择器 + F3.1 写面全资源）：查询键是失效
// 粒度的唯一真源（写后 invalidateQueries 同键）。目录类 60s 轮询；资源
// 表 15s（写后即时失效优先，轮询是兜底）；deployment 详情页走 wait 流
// 不在目录面。全部消费公共 REST 注解面（ADR-0044 钉形）。

type Structure = structureSchemas["schemas"];
type Delivery = deliverySchemas["schemas"];
type Automation = automationSchemas["schemas"];
type Identity = identitySchemas["schemas"];
type Telemetry = telemetrySchemas["schemas"];
type Proxy = proxySchemas["schemas"];
type Runtime = runtimeSchemas["schemas"];

export interface ProjectEntry {
  id: string;
  name: string;
}

export interface AppEntry {
  id: string;
  project_id: string;
  name: string;
  created_at: string;
}

const CATALOG_MS = 60_000;
const RESOURCE_MS = 15_000;

// list 帮手：响应信封里的行数组展平（undefined 行过滤——生成类型的宽松
// 形态收敛为非空行）。
function rowsOf<T>(list: Array<T | undefined> | undefined): T[] {
  return (list ?? []).flatMap((item) => (item != null ? [item] : []));
}

// qs 组查询串（仅含非空值）。
function qs(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const encoded = search.toString();
  return encoded === "" ? "" : `?${encoded}`;
}

export function useProjects() {
  return useQuery({
    queryKey: ["catalog", "projects"],
    queryFn: async (): Promise<ProjectEntry[]> => {
      const res = await apiFetch<{ projects?: Array<Structure["v1Project"] | undefined> }>(`/v1/projects${qs({ limit: 100 })}`);
      return rowsOf(res.projects).flatMap((project) =>
        project.id && project.name ? [{ id: project.id, name: project.name }] : [],
      );
    },
    refetchInterval: CATALOG_MS,
  });
}

// 项目名反查（crumb 首段唯一文案源）：ID→名；目录未达时 undefined，
// 调用方回退通用词——裸 ID 不该出现在面包屑（W-1/W-2 走查教训）。
export function useProjectName(projectId: string): string | undefined {
  const projects = useProjects();
  return projects.data?.find((project) => project.id === projectId)?.name;
}

// useApps 拉取 App 目录。ListApps 契约 project_id 必填（structure.proto
// 校验；曾按"空 = 全部"发送，吃回 E_INVALID_ARGUMENT 噪声——2026-10-05
// 走查 F4），故空 projectId 直接禁查（React Query enabled 门）。
export function useApps(projectId: string) {
  return useQuery({
    queryKey: ["catalog", "apps", projectId],
    enabled: projectId !== "",
    queryFn: async (): Promise<AppEntry[]> => {
      const res = await apiFetch<{ apps?: Array<Structure["v1App"] | undefined> }>(`/v1/apps${qs({ project_id: projectId, limit: 200 })}`);
      return rowsOf(res.apps).flatMap((app) =>
        app.id && app.name ? [{ id: app.id, project_id: app.project_id ?? "", name: app.name, created_at: app.created_at ?? "" }] : [],
      );
    },
    refetchInterval: CATALOG_MS,
  });
}

export function useNetworks(projectId: string) {
  return useQuery({
    queryKey: ["resources", "networks", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ networks?: Array<Structure["v1Network"] | undefined> }>(`/v1/networks${qs({ project_id: projectId })}`);
      return rowsOf(res.networks);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useNetworkPeers(projectId: string) {
  return useQuery({
    queryKey: ["resources", "network-peers", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ peers?: Array<Structure["v1NetworkPeer"] | undefined> }>(`/v1/networks/peers${qs({ project_id: projectId })}`);
      return rowsOf(res.peers);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useRoutes(projectId: string) {
  return useQuery({
    queryKey: ["resources", "routes", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ routes?: Array<Proxy["v1Route"] | undefined> }>(`/v1/routes${qs({ project_id: projectId })}`);
      return rowsOf(res.routes);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useVolumes(projectId: string) {
  return useQuery({
    queryKey: ["resources", "volumes", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ volumes?: Array<Structure["v1Volume"] | undefined> }>(`/v1/volumes${qs({ project_id: projectId })}`);
      return rowsOf(res.volumes);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useSecrets(projectId: string) {
  return useQuery({
    queryKey: ["resources", "secrets", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ secrets?: Array<Structure["v1Secret"] | undefined> }>(`/v1/secrets${qs({ project_id: projectId })}`);
      return rowsOf(res.secrets);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useConfigs(projectId: string) {
  return useQuery({
    queryKey: ["resources", "configs", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ configs?: Array<Structure["v1Config"] | undefined> }>(`/v1/configs${qs({ project_id: projectId })}`);
      return rowsOf(res.configs);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useSharedVariables(projectId: string) {
  return useQuery({
    queryKey: ["resources", "shared-variables", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ variables?: Array<Structure["v1SharedVariable"] | undefined> }>(`/v1/shared-variables${qs({ project_id: projectId })}`);
      return rowsOf(res.variables);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useDatabases(projectId: string) {
  return useQuery({
    queryKey: ["resources", "databases", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ databases?: Array<Structure["v1Database"] | undefined> }>(`/v1/databases${qs({ project_id: projectId })}`);
      return rowsOf(res.databases);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useDatabaseBackups(databaseId: string) {
  return useQuery({
    queryKey: ["resources", "database-backups", databaseId],
    enabled: databaseId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ backups?: Array<Structure["v1Backup"] | undefined> }>(`/v1/databases/${encodeURIComponent(databaseId)}/backups`);
      return rowsOf(res.backups);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useUploads(projectId: string) {
  return useQuery({
    queryKey: ["resources", "uploads", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ uploads?: Array<Delivery["v1Upload"] | undefined> }>(`/v1/uploads${qs({ project_id: projectId })}`);
      return rowsOf(res.uploads);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useTasks(projectId: string) {
  return useQuery({
    queryKey: ["resources", "tasks", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ tasks?: Array<Automation["v1Task"] | undefined> }>(`/v1/tasks${qs({ project_id: projectId })}`);
      return rowsOf(res.tasks);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useRuns(taskId: string) {
  return useQuery({
    queryKey: ["resources", "runs", taskId],
    enabled: taskId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ runs?: Array<Automation["v1Run"] | undefined> }>(`/v1/runs${qs({ task_id: taskId })}`);
      return rowsOf(res.runs);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useSchedules(projectId: string) {
  return useQuery({
    queryKey: ["resources", "schedules", projectId],
    enabled: projectId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ schedules?: Array<Automation["v1Schedule"] | undefined> }>(`/v1/schedules${qs({ project_id: projectId })}`);
      return rowsOf(res.schedules);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useRevisions(appId: string) {
  return useQuery({
    queryKey: ["resources", "revisions", appId],
    enabled: appId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ revisions?: Array<Delivery["v1Revision"] | undefined> }>(`/v1/revisions${qs({ app_id: appId, limit: 200 })}`);
      return rowsOf(res.revisions);
    },
    refetchInterval: RESOURCE_MS,
  });
}

// useProcessInstances 是进程在跑实例集的观测查询（ADR-0067 快照语义：
// 直读编排器任务/pod 列表，空集 = 观测行非错误）。查询键与 Terminal/Logs
// 下拉同源——副本下拉、Processes 页 Running 列、Overview per-process
// 摘要共享缓存（App→Process→Instance 钻取面，2026-10-12 层级定案）。
export function useProcessInstances(appId: string, process: string) {
  return useQuery({
    queryKey: ["exec-instances", appId, process],
    enabled: appId !== "" && process !== "",
    queryFn: () => listExecInstances(appId, process),
    refetchInterval: 10_000,
  });
}

export function useAudit(filters: { source?: string; action?: string; actor?: string; resource?: string; limit?: number }) {
  return useQuery({
    queryKey: ["audit", filters],
    queryFn: async () => {
      const res = await apiFetch<{ entries?: Array<Identity["v1AuditEntry"] | undefined> }>(`/v1/audit${qs(filters)}`);
      return rowsOf(res.entries);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useTokens() {
  return useQuery({
    queryKey: ["settings", "tokens"],
    queryFn: async () => {
      const res = await apiFetch<{ tokens?: Array<Identity["v1Token"] | undefined> }>("/v1/tokens");
      return rowsOf(res.tokens);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useRoles() {
  return useQuery({
    queryKey: ["settings", "roles"],
    queryFn: async () => {
      const res = await apiFetch<{ roles?: Array<Identity["v1Role"] | undefined> }>("/v1/roles");
      return rowsOf(res.roles);
    },
    refetchInterval: CATALOG_MS,
  });
}

// 身份管理面（C2 治理批）：users/teams/invitations——RBAC 的 UI 消费面
// （roles 上面已有）。动词面对齐 CLI（users/teams/invitations 组）。
export function useUsers() {
  return useQuery({
    queryKey: ["identity", "users"],
    queryFn: async () => {
      const res = await apiFetch<{ users?: Array<Identity["v1User"] | undefined> }>("/v1/users");
      return rowsOf(res.users);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useTeams() {
  return useQuery({
    queryKey: ["identity", "teams"],
    queryFn: async () => {
      const res = await apiFetch<{ teams?: Array<Identity["v1Team"] | undefined> }>("/v1/teams");
      return rowsOf(res.teams);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useInvitations() {
  return useQuery({
    queryKey: ["identity", "invitations"],
    queryFn: async () => {
      const res = await apiFetch<{ invitations?: Array<Identity["v1Invitation"] | undefined> }>("/v1/invitations");
      return rowsOf(res.invitations);
    },
    refetchInterval: RESOURCE_MS,
  });
}

// Platform backups 列表（C2：Settings 触发面补齐台账面）。system 服务
// 不在 console 生成上下文（gen.mjs CONTEXTS 刻意不含）——类型内联手写，
// 与 freeze 面同法。PlatformSnapshot = { id, time, hostname }。
export interface PlatformSnapshot {
  id: string;
  time: string;
  hostname: string;
}

export function usePlatformBackups() {
  return useQuery({
    queryKey: ["settings", "platform-backups"],
    queryFn: async (): Promise<PlatformSnapshot[]> => {
      const res = await apiFetch<{ snapshots?: Array<PlatformSnapshot | undefined> }>("/v1/platform/backups");
      return rowsOf(res.snapshots);
    },
    refetchInterval: 60_000,
  });
}

// WhoAmIResponse 的行形态（identity swagger 的信封名以生成类型为准）。
// platformAdmin 读服务端 platform_admin（H8 raw PromQL 入口显隐）——Console
// 不复制 role/team 判定（设计 §5.1/§5.4）。
export interface WhoAmI {
  tokenName: string;
  roleName: string;
  userName: string;
  platformAdmin: boolean;
}

export function useWhoami(enabled: boolean) {
  return useQuery({
    queryKey: ["settings", "whoami"],
    enabled,
    queryFn: async (): Promise<WhoAmI> => {
      const res = await apiFetch<Record<string, unknown>>("/v1/whoami");
      return {
        tokenName: typeof res.token_name === "string" ? res.token_name : "",
        roleName: typeof res.role_name === "string" ? res.role_name : "",
        userName: typeof res.user_name === "string" ? res.user_name : "",
        platformAdmin: res.platform_admin === true,
      };
    },
    retry: false,
    staleTime: CATALOG_MS,
  });
}

export function useAlertingChannels() {
  return useQuery({
    queryKey: ["settings", "channels"],
    queryFn: async () => {
      const res = await apiFetch<{ channels?: Array<Telemetry["v1NotificationChannel"] | undefined> }>("/v1/channels");
      return rowsOf(res.channels);
    },
    refetchInterval: RESOURCE_MS,
  });
}

// 告警规则与现行评估态（C1 可观测批）：states 单独拉是为了 observed_value
// 与内置 system 行（规则表行内联 state 已有，states 是 firing 总览面）。
export function useAlertRules() {
  return useQuery({
    queryKey: ["alerts", "rules"],
    queryFn: async () => {
      const res = await apiFetch<{ rules?: Array<Telemetry["v1AlertRule"] | undefined> }>("/v1/alerts/rules");
      return rowsOf(res.rules);
    },
    refetchInterval: RESOURCE_MS,
  });
}

export function useAlertStates() {
  return useQuery({
    queryKey: ["alerts", "states"],
    queryFn: async () => {
      const res = await apiFetch<{ states?: Array<Telemetry["v1AlertState"] | undefined> }>("/v1/alerts");
      return rowsOf(res.states);
    },
    refetchInterval: RESOURCE_MS,
  });
}

// METRIC_RANGES 是图表时间窗值域（ms）；step 取窗/240（下限 15s——
// 与服务端缺省步长同源）。
export const METRIC_RANGES: Record<string, number> = {
  "30m": 30 * 60_000,
  "1h": 60 * 60_000,
  "6h": 6 * 60 * 60_000,
  "24h": 24 * 60 * 60_000,
};

// ScopedMetric 是服务端 scoped 指标白名单（proto QueryScopedMetricsRequest
// .metric 同形）；Console 只在这三者中选择，不构造 PromQL。
export type ScopedMetric = "cpu_percent" | "cpu_cores" | "memory_working_set_bytes";

// MetricsScope 是 scoped 指标查询作用域（App 与 Database 恰一；project 由
// 服务端从已授权行解析，客户端不传）。
export interface MetricsScope {
  kind: "app" | "database";
  id: string;
}

// ScopedMetricsResult 保留服务端截断披露（truncated/series_total）——图表
// 据此提示命中序列被服务端上限截断，而不是静默丢数据。
export interface ScopedMetricsResult {
  series: Telemetry["v1MetricSeries"][];
  truncated: boolean;
  seriesTotal: number;
}

// metricWindow 是 raw/scoped 共用的时间窗计算（end=now、窗宽取
// METRIC_RANGES、step=max(15s, 窗/240)——与服务端钳制口径一致）。
function metricWindow(rangeKey: string): { start: string; end: string; stepSeconds: number } {
  const windowMs = METRIC_RANGES[rangeKey] ?? METRIC_RANGES["1h"];
  const end = new Date();
  const start = new Date(end.getTime() - windowMs);
  return {
    start: start.toISOString(),
    end: end.toISOString(),
    stepSeconds: Math.max(15, Math.round(windowMs / 1000 / 240)),
  };
}

// useRawMetricsSeries 拉原始 PromQL 时序（GET /v1/metrics；平台管理员诊断
// 面——member 由服务端 403）。空 query 不发请求（表单未就绪态）。
export function useRawMetricsSeries(query: string, rangeKey: string) {
  return useQuery({
    queryKey: ["metrics", query, rangeKey],
    enabled: query !== "",
    queryFn: async (): Promise<Telemetry["v1MetricSeries"][]> => {
      const { start, end, stepSeconds } = metricWindow(rangeKey);
      const res = await apiFetch<{ series?: Array<Telemetry["v1MetricSeries"] | undefined> }>(
        `/v1/metrics${qs({ query, start, end, step_seconds: stepSeconds })}`,
      );
      return rowsOf(res.series);
    },
    refetchInterval: 30_000,
  });
}

// useScopedMetricsSeries 拉服务端 scoped 固定模板时序（GET
// /v1/metrics/scoped；H8 阶段 B member 图表唯一路径）：请求只带 scope +
// 白名单 metric + 时间窗，PromQL 由服务端构造。scope 与 metric 进 queryKey
//（切 App/Database/指标即换缓存键）；process 仅 app scope 传递；空 scope id
// 不发请求；enabled 供 custom raw 模式关掉后台预设请求。
export function useScopedMetricsSeries(
  scope: MetricsScope,
  metric: ScopedMetric,
  process: string | undefined,
  rangeKey: string,
  enabled = true,
) {
  // process 值域与词汇导出一致（小写 dns）；仅 app scope 传递，database
  // scope 忽略（服务端会拒绝 database+process）。
  const processFilter = scope.kind === "app" && process != null && process !== "" ? process.toLowerCase() : undefined;
  return useQuery({
    queryKey: ["metrics", "scoped", scope.kind, scope.id, metric, processFilter ?? "", rangeKey],
    enabled: enabled && scope.id !== "",
    queryFn: async (): Promise<ScopedMetricsResult> => {
      const { start, end, stepSeconds } = metricWindow(rangeKey);
      const params =
        scope.kind === "app"
          ? { app_id: scope.id, metric, process: processFilter, start, end, step_seconds: stepSeconds }
          : { database_id: scope.id, metric, start, end, step_seconds: stepSeconds };
      const res = await apiFetch<Telemetry["v1QueryScopedMetricsResponse"]>(`/v1/metrics/scoped${qs(params)}`);
      const series = rowsOf(res.series);
      return {
        series,
        truncated: res.truncated === true,
        seriesTotal: res.series_total ?? series.length,
      };
    },
    refetchInterval: 30_000,
  });
}

// appNameOf 从目录里解析 App 显示名（缺失回退短 id）。
export function appNameOf(apps: AppEntry[] | undefined, appId: string | undefined): string {
  if (!appId) return "—";
  const hit = apps?.find((app) => app.id === appId);
  return hit ? hit.name : `${appId.slice(0, 10)}…`;
}

// 节点面（C4）：RuntimeAdmin 的 UI 消费面——list + enroll 材料 + drain/
// cordon/uncordon 三动词（动词面对齐 CLI nodes 组）。enroll 材料等价集群
// 成员权（platform:admin 档），响应只在铸造/轮换时可见。
// useNodes 节点清单（平台级读面：服务端门 R08-M1——非平台管理员调用即
// 403，故 enabled 由调用方按 whoami.platformAdmin 传入，fail-closed）。
export function useNodes(enabled = true) {
  return useQuery({
    queryKey: ["nodes", "list"],
    enabled,
    queryFn: async () => {
      const res = await apiFetch<{ nodes?: Array<Runtime["v1Node"] | undefined> }>("/v1/nodes");
      return rowsOf(res.nodes);
    },
    refetchInterval: RESOURCE_MS,
  });
}

// useNodeContainers 是节点在跑容器集的观测查询（ListNodeContainers 快照
// 语义：直读编排器任务/pod 列表；空集 = 观测行非错误）。节点详情页 10s
// 轮询（useProcessInstances 同拍）。
export function useNodeContainers(nodeId: string) {
  return useQuery({
    queryKey: ["node-containers", nodeId],
    enabled: nodeId !== "",
    queryFn: async () => {
      const res = await apiFetch<{ containers?: Array<Runtime["v1NodeContainer"] | undefined> }>(
        `/v1/nodes/${encodeURIComponent(nodeId)}/containers`,
      );
      return rowsOf(res.containers);
    },
    refetchInterval: 10_000,
  });
}
