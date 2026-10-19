// 库载体指标预设（IA v3 T8；H8 阶段 B 起走服务端 scoped 模板）：
// Console 只声明指标白名单值与展示量纲，docker/k3s 双臂 PromQL 由服务端
// 按 database id + 行解析出的 Project 固定构造（设计
// docs/design/2026-10-19-security-backlog-design.md §5.3、§5.4）。Database
// 图表面没有 CPU % 消费（cpu_percent 仅 app scope），保留 memory/cores 两项。

export interface CarrierMetricPreset {
  key: string;
  label: string;
  unit: "bytes" | "plain";
  metric: "memory_working_set_bytes" | "cpu_cores";
}

export const DATABASE_CARRIER_PRESETS: CarrierMetricPreset[] = [
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
