import { describe, expect, it } from "vitest";
import { buildChartRows } from "./metric-chart";
import type { components } from "@/api/telemetry";

// buildChartRows 数据底座锚：protojson 零值缺省（value 缺席 = 0，idle 应用
// 全零序列必须有行——W-B1 曾把它当缺数据跳点致图表空白）、多序列时间轴
// 对齐、脏点（time 缺席/不可解析）跳过。

type MetricSeries = components["schemas"]["v1MetricSeries"];

describe("buildChartRows", () => {
  it("treats an absent value as zero (protojson omits proto3 scalar zero)", () => {
    // REST 信封对 value=0 的点只发 time 字段——全零序列是合法数据。
    const series: MetricSeries[] = [
      {
        labels: { name: "app.1" },
        points: [
          { time: "2026-10-09T12:00:00Z" },
          { time: "2026-10-09T12:00:15Z" },
        ],
      },
    ];
    const { rows, keys } = buildChartRows(series);
    expect(rows).toHaveLength(2);
    expect(rows.map((r) => r.s0)).toEqual([0, 0]);
    expect(keys).toEqual(["s0"]);
  });

  it("aligns multiple series onto the shared time axis", () => {
    const series: MetricSeries[] = [
      {
        labels: { name: "a" },
        points: [
          { time: "2026-10-09T12:00:00Z", value: 1 },
          { time: "2026-10-09T12:00:15Z", value: 2 },
        ],
      },
      {
        labels: { name: "b" },
        points: [{ time: "2026-10-09T12:00:15Z", value: 3 }],
      },
    ];
    const { rows, keys } = buildChartRows(series);
    expect(keys).toEqual(["s0", "s1"]);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ s0: 1 });
    expect(rows[1]).toMatchObject({ s0: 2, s1: 3 });
  });

  it("skips points with a missing or unparsable time", () => {
    const series: MetricSeries[] = [
      {
        labels: { name: "a" },
        points: [
          { time: "not-a-timestamp", value: 9 },
          { value: 8 },
          { time: "2026-10-09T12:00:00Z", value: 7 },
        ],
      },
    ];
    const { rows } = buildChartRows(series);
    expect(rows).toHaveLength(1);
    expect(rows[0].s0).toBe(7);
  });

  it("produces chartable rows for an all-zero series (idle app CPU %)", () => {
    // 走查 W-B1 回归锚：全零序列不得产出空 rows（空 rows = 图表空白
    // 无线无轴）。
    const series: MetricSeries[] = [
      { labels: { name: "idle" }, points: Array.from({ length: 4 }, (_, i) => ({ time: new Date(Date.UTC(2026, 9, 9, 12, 0, i * 15)).toISOString() })) },
    ];
    const { rows } = buildChartRows(series);
    expect(rows).toHaveLength(4);
    expect(rows.every((r) => r.s0 === 0)).toBe(true);
  });
});
