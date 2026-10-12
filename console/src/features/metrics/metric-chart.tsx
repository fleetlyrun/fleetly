import { useMemo } from "react";
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from "recharts";
import type { components } from "@/api/telemetry";
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart";
import { formatMetricValue } from "./metric-presets";

// MetricChart（Workbench 原型，UI v2 批 3）：shadcn Chart（Recharts 底座）
// ——坐标轴/网格/十字线 tooltip/图例含末值/序列稳定配色，旧手写无轴 SVG
// 的全面替换。多序列对齐到统一时间轴行。
//
// 序列归属与生命周期（ADR-0065 L3）：显示名优先 process 人读服务名（词汇
// 与 raw cadvisor 标签双形态识别——Processes 页同源），同名副本以容器名尾
// 判别；图表键全序列唯一（s0..sN 按原始下标——跨刷新稳定，配色随键稳定；
// 曾用 index%5 当键，>5 序列即碰撞：数据互相覆写、图例按 5 复读、React
// key 重复渲染错乱）；渲染面按「窗口尾活跃优先、峰值次之」排序，超出上限
// 的序列计数显式披露（死副本残骸不淹没活副本，也不静默丢数据）。

type MetricSeries = components["schemas"]["v1MetricSeries"];

// chartSeriesCap 是单图渲染的序列上限。
const chartSeriesCap = 10;
// seriesActiveWindowMs 是「活副本」判别窗（末样本距窗尾）——VM 即时查询
// 5 分钟 lookback 同源；15s 采集节拍下死容器序列停在其死亡时刻。
const seriesActiveWindowMs = 5 * 60_000;

// seriesIdentity 归一序列身份：base 是显示名主干（process 人读名优先，
// 容器名/node/首标签兜底——工作台 raw 查询的机器级序列落 node），replica
// 是同 base 多副本时的判别尾（容器名——swarm 任务随机尾唯一）。
function seriesIdentity(series: MetricSeries): { base: string; replica: string } {
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

export function buildChartRows(seriesList: MetricSeries[]): {
  rows: Array<Record<string, number | string> & { t: number }>;
  keys: string[];
  labels: Record<string, string>;
  hidden: number;
} {
  // 评分排序（显示序）：末样本时刻定活跃、峰值定显著、原始下标兜底稳定。
  // 全脏点（time 缺席/不可解析）序列不进渲染面。
  const scored = seriesList.map((series, index) => {
    let lastT = Number.NEGATIVE_INFINITY;
    let peak = Number.NEGATIVE_INFINITY;
    for (const point of series.points ?? []) {
      if (point.time == null) continue;
      const t = Date.parse(point.time);
      if (Number.isNaN(t)) continue;
      if (t > lastT) lastT = t;
      const value = point.value ?? 0;
      if (value > peak) peak = value;
    }
    return { index, lastT, peak };
  });
  const windowEnd = scored.reduce((acc, s) => Math.max(acc, s.lastT), Number.NEGATIVE_INFINITY);
  const ranked = scored
    .filter((s) => s.lastT > Number.NEGATIVE_INFINITY)
    .sort((a, b) => {
      const aActive = windowEnd - a.lastT <= seriesActiveWindowMs;
      const bActive = windowEnd - b.lastT <= seriesActiveWindowMs;
      if (aActive !== bActive) return aActive ? -1 : 1;
      if (a.peak !== b.peak) return b.peak - a.peak;
      return a.index - b.index;
    });
  const displayed = ranked.slice(0, chartSeriesCap);
  const hidden = seriesList.length - displayed.length;

  // 显示名判别频次按全集统计——被上限淘汰的兄弟副本不引起显示名抖动。
  const freq = new Map<string, number>();
  for (const series of seriesList) {
    const base = seriesIdentity(series).base;
    freq.set(base, (freq.get(base) ?? 0) + 1);
  }
  const keys: string[] = [];
  const labels: Record<string, string> = {};
  for (const s of displayed) {
    const key = `s${s.index}`;
    const { base, replica } = seriesIdentity(seriesList[s.index]);
    keys.push(key);
    labels[key] =
      (freq.get(base) ?? 0) > 1 ? `${base}·${replica ? replica.slice(-6) : String(s.index)}` : base;
  }

  // rows：渲染面序列对齐到统一时间轴行。protojson 对 proto3 标量零值缺省
  // 序列化——value 缺席即 0（idle 应用的 CPU % 是合法全零序列，不是缺
  // 数据；W-B1 走查咬出曾把它当缺数据跳点，全零序列图表空白无线无轴）。
  const timeMap = new Map<number, Record<string, number | string> & { t: number }>();
  for (const s of displayed) {
    const key = `s${s.index}`;
    for (const point of seriesList[s.index].points ?? []) {
      if (point.time == null) continue;
      const t = Date.parse(point.time);
      if (Number.isNaN(t)) continue;
      const row = timeMap.get(t) ?? { t };
      row[key] = point.value ?? 0;
      timeMap.set(t, row);
    }
  }
  const rows = [...timeMap.entries()]
    .sort(([a], [b]) => a - b)
    .map(([timestamp, row]) => ({ ...row, t: timestamp }));

  return { rows, keys, labels, hidden };
}

export function MetricChart({
  seriesList,
  unit,
}: {
  seriesList: MetricSeries[];
  unit: "percent" | "bytes" | "plain";
}) {
  const { rows, config, keys, hidden } = useMemo(() => {
    const config: ChartConfig = {};
    const built = buildChartRows(seriesList);
    for (const key of built.keys) {
      const index = Number(key.slice(1));
      config[key] = { label: built.labels[key], color: `var(--chart-${(index % 5) + 1})` };
    }
    return { rows: built.rows, config, keys: built.keys, hidden: built.hidden };
  }, [seriesList]);

  const lastValues = useMemo(() => {
    const out: Record<string, number | undefined> = {};
    for (const key of keys) {
      const lastRow = [...rows].reverse().find((row) => row[key] != null);
      out[key] = lastRow?.[key] as number | undefined;
    }
    return out;
  }, [rows, keys]);

  return (
    <div className="flex flex-col">
      <ChartContainer config={config} className="h-56 w-full">
        <LineChart data={rows} margin={{ left: 4, right: 12, top: 8, bottom: 0 }}>
          <CartesianGrid vertical={false} strokeDasharray="3 3" />
          <XAxis
            dataKey="t"
            type="number"
            scale="time"
            domain={["dataMin", "dataMax"]}
            tickFormatter={(value: number) => new Date(value).toTimeString().slice(0, 5)}
            tickLine={false}
            axisLine={false}
            tickMargin={8}
            minTickGap={48}
          />
          <YAxis tickFormatter={(value: number) => formatMetricValue(value, unit)} tickLine={false} axisLine={false} width={64} />
          <ChartTooltip
            content={
              <ChartTooltipContent
                indicator="line"
                labelFormatter={(_, payload) => {
                  const t = (payload?.[0]?.payload as { t?: number } | undefined)?.t;
                  return t != null ? new Date(t).toLocaleTimeString?.() ?? "" : "";
                }}
              />
            }
          />
          <ChartLegend content={<ChartLegendContent />} />
          {keys.map((key) => (
            <Line
              key={key}
              dataKey={key}
              name={key}
              stroke={`var(--color-${key})`}
              strokeWidth={1.5}
              dot={false}
              isAnimationActive={false}
            />
          ))}
        </LineChart>
      </ChartContainer>
      <div className="flex flex-wrap gap-x-4 gap-y-1 px-1 py-1.5 text-xs text-muted-foreground">
        {keys.map((key) => (
          <span key={key} className="inline-flex items-center gap-1.5">
            <span className="inline-block h-0.5 w-3 rounded-sm" style={{ background: `var(--color-${key})` }} />
            {config[key]?.label as string}
            {lastValues[key] != null ? <b className="text-foreground">{formatMetricValue(lastValues[key]!, unit)}</b> : null}
          </span>
        ))}
      </div>
      {hidden > 0 ? (
        <p className="px-1 pb-1 text-2xs text-muted-foreground">
          +{hidden} more series not shown — stopped replicas from earlier generations stay queryable in the workbench.
        </p>
      ) : null}
    </div>
  );
}
