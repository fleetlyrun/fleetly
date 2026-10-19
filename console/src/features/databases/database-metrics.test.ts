import { describe, expect, it } from "vitest";
import { DATABASE_CARRIER_PRESETS } from "./database-metrics";

// H8 阶段 B：Database 图表面只声明指标白名单值——docker/k3s 双臂 PromQL
// 由服务端按 database id + 行解析的 Project 构造（Console 不再持有查询）。
describe("DATABASE_CARRIER_PRESETS", () => {
  it("describes the memory and cores scoped templates (no docker/k3s PromQL arms)", () => {
    expect(DATABASE_CARRIER_PRESETS.map(({ key, unit, metric }) => ({ key, unit, metric }))).toEqual([
      { key: "memory", unit: "bytes", metric: "memory_working_set_bytes" },
      { key: "cpu_cores", unit: "plain", metric: "cpu_cores" },
    ]);
  });

  it("carries no PromQL builder (queries are server-constructed)", () => {
    for (const preset of DATABASE_CARRIER_PRESETS) {
      expect(preset).not.toHaveProperty("build");
    }
  });
});
