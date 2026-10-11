
// 指标预设（UI v2 批 3 引入；ADR-0065 起查询走负载词汇）：单源 PromQL
// 模板。词汇序列由采集环归一导出（fleetly_container_*，身份标签
// process/workload/container + 域主体），percent/cores 已在引擎差分面
// 预计算——查询是单行等值匹配，无 rate()/group_left（F-B1 故障类消失）。
// appLabel = appId 小写（swarm sanitizeNamePart 的 label 值，词汇导出侧
// 同款归一）；cadvisor 归因标签经同款小写化（F2.5 评估面口径）。

export interface MetricPreset {
  key: string;
  label: string;
  unit: "percent" | "bytes" | "plain";
  build: (appLabel: string) => string;
}

export const METRIC_PRESETS: MetricPreset[] = [
  {
    key: "cpu_percent",
    label: "CPU (% of node)",
    unit: "percent",
    build: (appLabel) => `fleetly_container_cpu_percent{app="${appLabel}"}`,
  },
  {
    key: "memory",
    label: "Memory working set (bytes)",
    unit: "bytes",
    build: (appLabel) => `fleetly_container_memory_working_set_bytes{app="${appLabel}"}`,
  },
  {
    key: "cpu_cores",
    label: "CPU (cores)",
    unit: "plain",
    build: (appLabel) => `fleetly_container_cpu_cores{app="${appLabel}"}`,
  },
];

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
