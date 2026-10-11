// 库载体指标寻址（IA v3 T8 已验证；ADR-0065 起 docker 臂走负载词汇）：
// docker 臂查采集环归一导出的 fleetly_container_* 词汇（身份标签
// workload = fleetly.workload.id 小写归一）；k3s 载体词汇化挂账——保留
// cadvisor/k8s 形态原文臂（namespace/pod 匹配），双 runtime 以 or 臂
// 各自恰一命中。label/pod 值均经小写归一（F2.5 同款），matcher 取
// databaseID.toLowerCase()。
// 词汇 percent/cores 在引擎差分面预计算——docker 臂无 rate()；k3s 臂仍
// 自带 rate()（raw counter 形态，双臂量纲一致：cores）。

const dockerWorkload = (databaseId: string) => `workload="${databaseId.toLowerCase()}"`;

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
      `fleetly_container_memory_working_set_bytes{${dockerWorkload(databaseId)}} or container_memory_working_set_bytes{${k3sCarrier(databaseId, projectId)}}`,
  },
  {
    key: "cpu_cores",
    label: "CPU (cores)",
    unit: "plain",
    build: (databaseId, projectId) =>
      `fleetly_container_cpu_cores{${dockerWorkload(databaseId)}} or rate(container_cpu_usage_seconds_total{${k3sCarrier(databaseId, projectId)}}[2m])`,
  },
];
