import { describe, expect, it } from "vitest";
import { METRIC_PRESETS } from "./metric-presets";

// per-process 下钻的 PromQL 钉死（2026-10-12 层级定案）：无过滤 = 原
// app-wide 形态（工作台/总览零改动）；有过滤 = process 标签附加，值域
// 小写化与词汇导出侧同源（engine/metrics.go 的 process 标签即小写）。
describe("METRIC_PRESETS process filter", () => {
  it("keeps the app-wide selector without a process filter", () => {
    expect(METRIC_PRESETS.find((preset) => preset.key === "cpu_percent")?.build("shop")).toBe(
      'fleetly_container_cpu_percent{app="shop"}',
    );
  });

  it("appends the process label, lowercased to match the vocabulary export", () => {
    expect(METRIC_PRESETS.find((preset) => preset.key === "cpu_percent")?.build("shop", "Web")).toBe(
      'fleetly_container_cpu_percent{app="shop",process="web"}',
    );
  });

  it("applies the process filter to every preset", () => {
    for (const preset of METRIC_PRESETS) {
      expect(preset.build("shop", "worker")).toContain(',process="worker"}');
    }
  });
});
