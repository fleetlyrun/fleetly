import { useMemo } from "react";
import type { components } from "@/api/telemetry";
import {
  type MetricSeries,
  MetricChart as FleetMetricChart,
} from "@/components/kit/values/metric-chart";
import { formatMetricValue } from "./metric-presets";

// MetricChart（metrics 直迁批，2026-10-24）：渲染面整页换 fleet-ui 块
// （components/fleet/values/metric-chart——序列治理四件套内置），本文件只剩
// **页面投影**：v1MetricSeries → MetricSeries（seriesIdentity 是业务词汇，
// 留 console 侧——design §6.1 裁决"base/replica 归一不进块"）。三个调用点
// （工作台 / app metrics tab / databases metrics）props 不变。
//
// 序列归属与生命周期（ADR-0065 L3）：显示名优先 process 人读服务名（词汇
// 与 raw cadvisor 标签双形态识别），同名副本以容器名尾判别；键 = s{原始下标}
// 跨刷新稳定（配色随键稳定；曾用 index%5 当键，>5 序列碰撞覆写的教训成文于
// fleet-ui §6.1）。

type V1MetricSeries = components["schemas"]["v1MetricSeries"];

// seriesIdentity 归一序列身份：base 是显示名主干（process 人读名优先，
// 容器名/node/首标签兜底——工作台 raw 查询的机器级序列落 node），replica
// 是同 base 多副本时的判别尾（容器名——swarm 任务随机尾唯一）。
function seriesIdentity(series: V1MetricSeries): { base: string; replica: string } {
  const labels = series.labels ?? {};
  const base =
    labels.process ||
    labels.container_label_fleetly_process ||
    labels.name ||
    labels.node ||
    Object.entries(labels)[0]?.join("=") ||
    "series";
  const replica = labels.container || labels.name || labels.node || "";
  return { base, replica };
}

/** v1MetricSeries → 块的 MetricSeries（页面投影，纯函数）：
 *  key = s{原始下标}（跨刷新稳定）；label 按**全集**频次判别同名副本（被渲染
 *  上限淘汰的兄弟副本不引起显示名抖动）；points 的 v 缺席原样下传——块按
 *  protojson 零值方言解释为 0（idle 应用的全零 CPU% 是合法序列不是缺数据）。 */
export function toMetricSeries(seriesList: ReadonlyArray<V1MetricSeries>): MetricSeries[] {
  const freq = new Map<string, number>();
  for (const series of seriesList) {
    const base = seriesIdentity(series).base;
    freq.set(base, (freq.get(base) ?? 0) + 1);
  }
  return seriesList.map((series, index) => {
    const { base, replica } = seriesIdentity(series);
    const label =
      (freq.get(base) ?? 0) > 1
        ? `${base}·${replica ? replica.slice(-6) : String(index)}`
        : base;
    return {
      key: `s${index}`,
      label,
      points: (series.points ?? []).map((point) => ({
        t: point.time != null ? Date.parse(point.time) : Number.NaN,
        v: point.value,
      })),
    };
  });
}

/** 服务端截断披露的 props 映射（纯函数，可单测）：仅服务端明确截断且带
 *  命中总数时披露（H8 §5.3.3）；不改变图面渲染上限与 +N more 计数。 */
export function serverCapOf(
  truncated: boolean | undefined,
  seriesTotal: number | undefined,
  shown: number,
): { shown: number; total: number } | undefined {
  if (!truncated || seriesTotal == null || seriesTotal <= shown) return undefined;
  return { shown, total: seriesTotal };
}

export function MetricChart({
  seriesList,
  unit,
  truncated,
  seriesTotal,
}: {
  seriesList: V1MetricSeries[];
  unit: "percent" | "bytes" | "plain";
  // truncated/seriesTotal 是服务端 scoped 面的截断披露（H8 §5.3.3）：命中
  // 序列超过服务端上限时只返回前 N 条，图面必须显式说明而不是静默丢序列。
  truncated?: boolean;
  seriesTotal?: number;
}) {
  const series = useMemo(() => toMetricSeries(seriesList), [seriesList]);
  return (
    <FleetMetricChart
      series={series}
      formatValue={(value) => formatMetricValue(value, unit)}
      serverCap={serverCapOf(truncated, seriesTotal, seriesList.length)}
    />
  );
}
