import { describe, expect, it } from "vitest";
import { METRIC_PRESETS, formatMetricValue } from "./metric-presets";

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

// y 轴刻度格式化（走查 W-4 回归锚）：<1% 量程自适应精度——0-0.1% 量程下
// 1 位小数曾把全部刻度归并成重复的 0.0%/0.1%；去尾零保持刻度短。
describe("formatMetricValue percent precision", () => {
  it("keeps coarse precision at and above 1%", () => {
    expect(formatMetricValue(24.36, "percent")).toBe("24.4%");
    expect(formatMetricValue(100, "percent")).toBe("100%");
    expect(formatMetricValue(0.5, "percent")).toBe("0.5%");
  });

  it("adds precision below 1% so ticks stay distinct", () => {
    expect(formatMetricValue(0.025, "percent")).toBe("0.025%");
    expect(formatMetricValue(0.05, "percent")).toBe("0.05%");
    expect(formatMetricValue(0.075, "percent")).toBe("0.075%");
    expect(formatMetricValue(0.1, "percent")).toBe("0.1%");
  });

  it("trims trailing zeros and keeps zero honest", () => {
    expect(formatMetricValue(0, "percent")).toBe("0%");
    expect(formatMetricValue(1, "percent")).toBe("1%");
    expect(formatMetricValue(2.5, "percent")).toBe("2.5%");
  });
});
