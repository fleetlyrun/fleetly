// 指标预设（UI v2 批 3 引入；H8 阶段 B 起走服务端 scoped 模板）：预设只
// 描述指标白名单值（cpu_percent / cpu_cores / memory_working_set_bytes）与
// 展示量纲——PromQL 由服务端按 scope + metric 固定构造，Console 不再拼查询
// 文本（设计 docs/design/2026-10-19-security-backlog-design.md §5.4）。
// process 下钻（App Metrics 页，2026-10-12 层级定案）作为 scoped 请求的
// 可选参数传递，值域与 Process 名同源（小写 dns 形态）；缺省 = 全进程并集
//（工作台/总览原语义）。

// ScopedMetric 与服务端指标白名单同形（proto QueryScopedMetricsRequest.metric）；
// 类型单源在 kit/metrics 的查询层（批4 自 lib/catalog 迁入）。
import type { ScopedMetric } from "@/kit/metrics";

export interface MetricPreset {
  key: string;
  label: string;
  unit: "percent" | "bytes" | "plain";
  metric: ScopedMetric;
}

export const METRIC_PRESETS: MetricPreset[] = [
  {
    key: "cpu_percent",
    label: "CPU (% of node)",
    unit: "percent",
    metric: "cpu_percent",
  },
  {
    key: "memory",
    label: "Memory working set (bytes)",
    unit: "bytes",
    metric: "memory_working_set_bytes",
  },
  {
    key: "cpu_cores",
    label: "CPU (cores)",
    unit: "plain",
    metric: "cpu_cores",
  },
];

// formatMetricValue 是 y 轴/图例的量纲缩写（bytes 与百分制/工程计数兜底）。
// percent 在 <1% 量程自适应加精度（走查 W-4：0-0.1% 量程下 1 位小数把
// 全部刻度归并成重复的 0.0%/0.1%），去尾零保持刻度短。
export function formatMetricValue(value: number, unit: "percent" | "bytes" | "plain"): string {
  if (unit === "bytes") {
    const abs = Math.abs(value);
    if (abs >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(1)} GiB`;
    if (abs >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(0)} MiB`;
    if (abs >= 1024) return `${(value / 1024).toFixed(0)} KiB`;
    return `${value} B`;
  }
  if (unit === "percent") {
    const abs = Math.abs(value);
    const dp = abs >= 100 ? 0 : abs >= 1 ? 1 : abs >= 0.1 ? 2 : 3;
    return `${parseFloat(value.toFixed(dp))}%`;
  }
  return Math.abs(value) >= 100 ? value.toFixed(0) : value.toFixed(2);
}
