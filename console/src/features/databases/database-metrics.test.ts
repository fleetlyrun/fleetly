import { describe, expect, it } from "vitest";
import { DATABASE_CARRIER_PRESETS } from "./database-metrics";

describe("DATABASE_CARRIER_PRESETS", () => {
  it("lowercases ids into both runtime arms (T8 addressing)", () => {
    const memory = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "memory")!;
    const query = memory.build("01J9X4P7N2", "01PROJECT");
    expect(query).toContain('container_label_fleetly_workload_id="01j9x4p7n2"');
    expect(query).toContain('namespace="fleetly-01project"');
    expect(query).toContain('pod=~"fleetly-db-01j9x4p7n2.*"');
    expect(query).toContain(" or ");
  });

  it("cpu preset wraps each arm in rate()", () => {
    const cpu = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "cpu_cores")!;
    const query = cpu.build("db1", "p1");
    expect(query.match(/rate\(/g)).toHaveLength(2);
  });
});
