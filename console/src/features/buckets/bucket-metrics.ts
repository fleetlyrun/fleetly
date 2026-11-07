// bucket store 载体指标预设（B5 姐妹页，pubsub-metrics.ts 同构）：Console
// 只声明指标白名单值与展示量纲，docker/k3s 双臂 PromQL 由服务端按 bucket
// store id + 行解析出的 Project 固定构造（swarm 词汇臂 bucketstore 标签 /
// k3s pod 名臂 fleetly-bkt-<name>-<id>，B5 连坐扩）。与 database/pubsub 图
// 表面同口径：无 CPU % 消费（cpu_percent 仅 app scope），保留 memory/cores
// 两项。

export interface BucketStoreCarrierMetricPreset {
  key: string;
  label: string;
  unit: "bytes" | "plain";
  metric: "memory_working_set_bytes" | "cpu_cores";
}

export const BUCKET_STORE_CARRIER_PRESETS: BucketStoreCarrierMetricPreset[] = [
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
