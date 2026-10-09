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
import { formatMetricValue, seriesKey } from "./metric-presets";

// MetricChart（Workbench 原型，UI v2 批 3）：shadcn Chart（Recharts 底座）
// ——坐标轴/网格/十字线 tooltip/图例含末值/序列稳定配色，旧手写无轴 SVG
// 的全面替换。多序列对齐到统一时间轴行。

type MetricSeries = components["schemas"]["v1MetricSeries"];

// buildChartRows 把多序列点流对齐到统一时间轴行（图表的数据底座）。
// protojson 对 proto3 标量零值缺省序列化——value 缺席即 0（idle 应用的
// CPU % 是合法全零序列，不是缺数据；W-B1 走查咬出曾把它当缺数据跳点，
// 全零序列图表空白无线无轴）。time 缺席或不可解析才是脏点，跳过。
export function buildChartRows(seriesList: MetricSeries[]): {
  rows: Array<Record<string, number | string> & { t: number }>;
  keys: string[];
  labels: Record<string, string>;
} {
  const keys: string[] = [];
  const labels: Record<string, string> = {};
  const timeMap = new Map<number, Record<string, number | string> & { t: number }>();

  seriesList.forEach((series, index) => {
    const { key, label } = seriesKey(series, index);
    keys.push(key);
    labels[key] = label;
    for (const point of series.points ?? []) {
      if (point.time == null) continue;
      const t = Date.parse(point.time);
      if (Number.isNaN(t)) continue;
      const value = point.value ?? 0;
      const row: (Record<string, number | string> & { t: number }) | undefined = timeMap.get(t) ?? { t };
      row[key] = value;
      timeMap.set(t, row);
    }
  });

  const rows = [...timeMap.entries()]
    .sort(([a], [b]) => a - b)
    .map(([timestamp, row]) => ({ ...row, t: timestamp }));

  return { rows, keys, labels };
}

export function MetricChart({
  seriesList,
  unit,
}: {
  seriesList: MetricSeries[];
  unit: "percent" | "bytes" | "plain";
}) {
  const { rows, config, keys } = useMemo(() => {
    const config: ChartConfig = {};
    const { rows, keys, labels } = buildChartRows(seriesList);
    for (const key of keys) {
      config[key] = { label: labels[key], color: `var(--chart-${(Number(key.slice(1)) % 5) + 1})` };
    }
    return { rows, config, keys };
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
          <YAxis tickFormatter={(value: number) => formatMetricValue(value, unit)} tickLine={false} axisLine={false} width={56} />
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
    </div>
  );
}
