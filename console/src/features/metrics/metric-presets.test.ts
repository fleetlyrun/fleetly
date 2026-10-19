import { describe, expect, it } from "vitest";
import { METRIC_PRESETS, formatMetricValue } from "./metric-presets";

// H8 阶段 B：预设只描述服务端 scoped 指标白名单值（不再拼 PromQL 文本）。
// key 是 UI/URL 的稳定标识，metric 是 proto QueryScopedMetricsRequest 的
// 白名单值，unit 只驱动 y 轴量纲。
describe("METRIC_PRESETS", () => {
  it("describes the three server-side scoped templates", () => {
    expect(METRIC_PRESETS.map(({ key, unit, metric }) => ({ key, unit, metric }))).toEqual([
      { key: "cpu_percent", unit: "percent", metric: "cpu_percent" },
      { key: "memory", unit: "bytes", metric: "memory_working_set_bytes" },
      { key: "cpu_cores", unit: "plain", metric: "cpu_cores" },
    ]);
  });

  it("carries no PromQL builder (queries are server-constructed)", () => {
    for (const preset of METRIC_PRESETS) {
      expect(preset).not.toHaveProperty("build");
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
