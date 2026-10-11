import { describe, expect, it } from "vitest";
import { buildChartRows } from "./metric-chart";
import type { components } from "@/api/telemetry";

// buildChartRows 数据底座锚：protojson 零值缺省（value 缺席 = 0，idle 应用
// 全零序列必须有行——W-B1 曾把它当缺数据跳点致图表空白）、多序列时间轴
// 对齐、脏点（time 缺席/不可解析）跳过；ADR-0065 L3：键全序列唯一（>5 序
// 列不碰撞——曾按 index%5 键合，数据互相覆写图例按 5 复读）、process 身
//份归属（同名副本容器名尾判别）、活跃优先排序 + 超上限披露。

type MetricSeries = components["schemas"]["v1MetricSeries"];

const T = Date.UTC(2026, 9, 9, 12, 0, 0);

function pointSeries(labels: Record<string, string>, points: Array<{ t: number; value?: number }>): MetricSeries {
  return {
    labels,
    points: points.map((p) => ({ time: new Date(p.t).toISOString(), value: p.value })),
  };
}

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
    // 显示序活跃优先、峰值次之——b（峰值 3）排在 a 前，但键仍按原始下标。
    expect(keys).toEqual(["s1", "s0"]);
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
      { labels: { name: "idle" }, points: Array.from({ length: 4 }, (_, i) => ({ time: new Date(T + i * 15_000).toISOString() })) },
    ];
    const { rows } = buildChartRows(series);
    expect(rows).toHaveLength(4);
    expect(rows.every((r) => r.s0 === 0)).toBe(true);
  });

  it("keeps keys unique beyond 5 series (no cross-series overwrite)", () => {
    // 键碰撞回归锚：曾用 s{index%5} 当键，>5 序列即互相覆写（行值被后写
    // 者顶掉、图例按 5 为周期复读）。
    const series: MetricSeries[] = Array.from({ length: 7 }, (_, i) =>
      pointSeries({ process: `p${i}` }, [{ t: T, value: i + 1 }]),
    );
    const { rows, keys } = buildChartRows(series);
    expect(new Set(keys).size).toBe(7);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ s0: 1, s4: 5, s5: 6, s6: 7 });
  });

  it("attributes display labels to the process name, disambiguating same-process replicas", () => {
    const series: MetricSeries[] = [
      pointSeries({ process: "web", container: "fleetly-x-web.1.rrrraaaaaa" }, [{ t: T, value: 1 }]),
      pointSeries({ process: "web", container: "fleetly-x-web.2.rrrrbbbbbb" }, [{ t: T, value: 2 }]),
      pointSeries({ process: "server", container: "fleetly-x-server.1.rrrrcccccc" }, [{ t: T, value: 3 }]),
    ];
    const { keys, labels } = buildChartRows(series);
    // 峰值序：server、web(bbbbbb)、web(aaaaaa)；单副本裸 process 名，
    // 同名多副本以容器名尾判别。
    expect(keys).toEqual(["s2", "s1", "s0"]);
    expect(labels).toEqual({
      s2: "server",
      s1: "web·bbbbbb",
      s0: "web·aaaaaa",
    });
  });

  it("recognizes the raw cadvisor process label form (workbench queries)", () => {
    const series: MetricSeries[] = [
      pointSeries({ container_label_fleetly_process: "packer", name: "fleetly-big-containername" }, [{ t: T, value: 1 }]),
    ];
    const { labels } = buildChartRows(series);
    expect(labels.s0).toBe("packer");
  });

  it("ranks recently active series first and discloses the hidden remainder beyond the cap", () => {
    // 1 条活副本（峰值低）+ 11 条死代残骸（峰值高，末样本在 1h 前）：
    // 活副本前排不被淹没；渲染面上限 10，超出计数披露。
    const stale = Array.from({ length: 11 }, (_, i) =>
      pointSeries({ process: `old${i}`, container: `c${i}` }, [{ t: T - 3_600_000, value: 100 + i }]),
    );
    const live = pointSeries({ process: "web", container: "c-live" }, [{ t: T, value: 1 }]);
    const { keys, hidden } = buildChartRows([live, ...stale]);
    expect(keys).toHaveLength(10);
    expect(keys[0]).toBe("s0"); // live 系列原始下标 0，活跃优先
    expect(hidden).toBe(2);
  });
});
