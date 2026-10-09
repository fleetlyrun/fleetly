import type { StatusTone } from "@/components/domain/status-badge";
import type { components } from "@/api/telemetry";

type MetricSeries = components["schemas"]["v1MetricSeries"];

// Ingest 时效派生（IA v3 T6）：受管 Metrics（VictoriaMetrics）的新鲜度用
// PromQL 时间戳探针可算——`time() - max(timestamp(...))` 的 instant 值
// 即"最新样本距今秒数"。Logs（VictoriaLogs）无查询代理，三期 ticket 面
// 落地前照实 n/a。

export const METRICS_FRESHNESS_QUERY =
  'time() - max(timestamp(container_memory_working_set_bytes{job="fleetly-cadvisor"}))';

export function lastPointValue(seriesList: MetricSeries[] | undefined): number | undefined {
  for (const series of seriesList ?? []) {
    const points = series.points ?? [];
    const last = points[points.length - 1];
    const value = last?.value;
    if (value != null && Number.isFinite(value)) return value;
  }
  return undefined;
}

// 阈值贴推送链路（cadvisor 15s + engine 15s import）：5m 内健康，
// 30m 内警告，再久判定停摆。
export function formatFreshness(seconds: number | undefined): { label: string; tone: StatusTone } {
  if (seconds == null || !Number.isFinite(seconds)) return { label: "n/a", tone: "neutral" };
  if (seconds < 300) return { label: `${Math.max(1, Math.round(seconds))}s ago`, tone: "success" };
  if (seconds < 1800) return { label: `${Math.round(seconds / 60)}m ago`, tone: "warning" };
  return { label: `${Math.round((seconds / 3600) * 10) / 10}h ago`, tone: "danger" };
}
