// 库载体指标寻址（IA v3 T8 已验证）：载体是普通容器，cadvisor 全量采集且
// QueryMetrics 是 PromQL 透传——按 docker label（swarm 实证）或 k3s
// namespace/pod 双臂 or 匹配，双 runtime 通用。label 值经 sanitizeNamePart
// 小写（F2.5 同款归一），故 matcher 取 databaseID.toLowerCase()。
// 注意：group_left 除法（cpu percent of node）不在库预设里——载体指标一期
// 只出绝对量（cores/bytes），避免多臂 or 后的除法序列归因复杂化。

const dockerWorkload = (databaseId: string) => `container_label_fleetly_workload_id="${databaseId.toLowerCase()}"`;

const k3sCarrier = (databaseId: string, projectId: string) =>
  `namespace="fleetly-${projectId.toLowerCase()}",pod=~"fleetly-db-${databaseId.toLowerCase()}.*"`;

export interface CarrierMetricPreset {
  key: string;
  label: string;
  unit: "bytes" | "plain";
  build: (databaseId: string, projectId: string) => string;
}

export const DATABASE_CARRIER_PRESETS: CarrierMetricPreset[] = [
  {
    key: "memory",
    label: "Memory working set (bytes)",
    unit: "bytes",
    build: (databaseId, projectId) =>
      `container_memory_working_set_bytes{${dockerWorkload(databaseId)}} or container_memory_working_set_bytes{${k3sCarrier(databaseId, projectId)}}`,
  },
  {
    key: "cpu_cores",
    label: "CPU (cores)",
    unit: "plain",
    build: (databaseId, projectId) =>
      `rate(container_cpu_usage_seconds_total{${dockerWorkload(databaseId)}}[2m]) or rate(container_cpu_usage_seconds_total{${k3sCarrier(databaseId, projectId)}}[2m])`,
  },
];
