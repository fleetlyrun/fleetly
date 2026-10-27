import { useQuery } from "@tanstack/react-query";
import type { components as telemetrySchemas } from "@/api/telemetry";
import { apiFetch } from "@/api/client";
import { qs, rowsOf } from "./http";

// 指标域读面（2026-10-27 批4 自 lib/catalog.ts 迁入；REST 端点、时间窗与
// 步长口径逐值照抄源实现）：raw/scoped 时序是图表观测读而非列表语义，保持
// useQuery 直连（不走 collection 资源形）。30s 轮询同工作台口径。

type Telemetry = telemetrySchemas["schemas"];

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
// 面——member 由服务端 403）。空 query 不发请求（表单未就绪态）；enabled
// 由调用方按 whoami.platformAdmin 传入（缺省 true——诊断页表单形态；组件
// 页对非管理员禁发，省一次恒 403 的请求噪音，useScopedMetricsSeries 同款
// 纪律）。
export function useRawMetricsSeries(query: string, rangeKey: string, enabled = true) {
  return useQuery({
    queryKey: ["metrics", query, rangeKey],
    enabled: enabled && query !== "",
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
