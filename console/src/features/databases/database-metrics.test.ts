import { describe, expect, it } from "vitest";
import { DATABASE_CARRIER_PRESETS } from "./database-metrics";

describe("DATABASE_CARRIER_PRESETS", () => {
  it("addresses the vocabulary workload label on the docker arm and raw k8s labels on the k3s arm (T8 / ADR-0065)", () => {
    const memory = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "memory")!;
    const query = memory.build("01J9X4P7N2", "01PROJECT");
    expect(query).toContain('fleetly_container_memory_working_set_bytes{workload="01j9x4p7n2"}');
    expect(query).toContain('namespace="fleetly-01project"');
    expect(query).toContain('pod=~"fleetly-db-01j9x4p7n2.*"');
    expect(query).toContain(" or ");
  });

  it("cpu preset keeps cores dimension: vocabulary gauge on the docker arm, rate() only on the raw k3s arm", () => {
    const cpu = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "cpu_cores")!;
    const query = cpu.build("db1", "p1");
    expect(query).toContain('fleetly_container_cpu_cores{workload="db1"}');
    // 词汇 cores 已在引擎差分面预计算——rate() 只属于 raw k3s 臂。
    expect(query.match(/rate\(/g)).toHaveLength(1);
  });
});
