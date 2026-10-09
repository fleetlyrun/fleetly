import type { components } from "@/api/telemetry";

// 指标预设与序列配色（UI v2 批 3，Workbench 原型数据面）：PromQL 模板
// 单源（旧 Observability 页内联的迁移）；cadvisor 归因标签经
// sanitizeNamePart 小写化（F2.5 评估面同款归一）。

type MetricSeries = components["schemas"]["v1MetricSeries"];

export interface MetricPreset {
  key: string;
  label: string;
  unit: "percent" | "bytes";
  build: (appLabel: string) => string;
}

export const METRIC_PRESETS: MetricPreset[] = [
  {
    key: "cpu_percent",
    label: "CPU (% of node)",
    unit: "percent",
    build: (appLabel) =>
      `100 * rate(container_cpu_usage_seconds_total{container_label_fleetly_ns_app="${appLabel}"}[2m]) / on(node) machine_cpu_cores`,
  },
  {
    key: "memory",
    label: "Memory working set (bytes)",
    unit: "bytes",
    build: (appLabel) => `container_memory_working_set_bytes{container_label_fleetly_ns_app="${appLabel}"}`,
  },
  {
    key: "cpu_cores",
    label: "CPU (cores)",
    unit: "percent",
    build: (appLabel) => `rate(container_cpu_usage_seconds_total{container_label_fleetly_ns_app="${appLabel}"}[2m])`,
  },
];

// seriesKey 把序列归因为稳定标识（legend 名 + chart config 键）：容器名 →
// 节点 → 首标签族；超过 5 序列循环 chart-1..5。
export function seriesKey(series: MetricSeries, index: number): { key: string; label: string } {
  const labels = series.labels ?? {};
  const raw = labels.name ?? labels.node ?? Object.entries(labels)[0]?.join("=") ?? "series";
  const label = raw.length <= 36 ? raw : `${raw.slice(0, 18)}…${raw.slice(-12)}`;
  const key = `s${index % 5}`;
  return { key, label };
}

// formatMetricValue 是 y 轴/图例的量纲缩写（bytes 与百分制/工程计数兜底）。
export function formatMetricValue(value: number, unit: "percent" | "bytes" | "plain"): string {
  if (unit === "bytes") {
    const abs = Math.abs(value);
    if (abs >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(1)} GiB`;
    if (abs >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(0)} MiB`;
    if (abs >= 1024) return `${(value / 1024).toFixed(0)} KiB`;
    return `${value} B`;
  }
  if (unit === "percent") return `${value >= 100 ? value.toFixed(0) : value.toFixed(1)}%`;
  return Math.abs(value) >= 100 ? value.toFixed(0) : value.toFixed(2);
}
