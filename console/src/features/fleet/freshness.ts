import type { StatusTone } from "@/components/domain/status-badge";
import type { components } from "@/api/telemetry";

type MetricSeries = components["schemas"]["v1MetricSeries"];

// Ingest 时效派生（IA v3 T6）：受管 Metrics（VictoriaMetrics）的新鲜度用
// PromQL 时间戳探针可算——`time() - max(timestamp(...))` 的 instant 值
// 即"最新样本距今秒数"。Logs（VictoriaLogs）无查询代理，三期 ticket 面
// 落地前照实 n/a。

export const METRICS_FRESHNESS_QUERY =
  'time() - max(timestamp(container_memory_working_set_bytes{job="fleetly-cadvisor"}))';

// 节点磁盘水位（IA v3 二期⑤b，§5.2 "吃得下吗"层）：cadvisor 根容器
//（id="/"）携带宿主文件系统事实；伪设备（/dev、/dev/shm——limit 小、
// 用量噪声）显式排除，逐设备 usage/limit 后按 node 取最大 = 该节点最满
// 文件系统的水位。同标签除法（node/device/id/job 全同）——group_left
// 缺席即天然匹配（F-B1 缩放失配教训的规避形态）。staging 实证形态：
// node 标签序列，值域 0..1。
export const DISK_WATERMARK_QUERY =
  'max by (node) (container_fs_usage_bytes{job="fleetly-cadvisor",id="/",device!~"/dev|/dev/shm"} / container_fs_limit_bytes{job="fleetly-cadvisor",id="/",device!~"/dev|/dev/shm"})';

// formatWatermark 把 0..1 比值转展示面（tone 阈值：80% 警告 / 90% 危险
// ——磁盘告警族惯例；上游不可得 = n/a）。
export function formatWatermark(ratio: number | undefined): { label: string; tone: StatusTone; ratio: number } {
  if (ratio == null || !Number.isFinite(ratio)) return { label: "n/a", tone: "neutral", ratio: 0 };
  const pct = Math.round(ratio * 100);
  const tone: StatusTone = pct >= 90 ? "danger" : pct >= 80 ? "warning" : "success";
  return { label: `${pct}% used`, tone, ratio };
}

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
