// vendored from fleet-ui registry/values/metric-chart.tsx @0.1.0（2026-10-24 metrics 直迁）
// 拷贝适配（design.md §12.1 token 映射表）：text-muted→text-muted-foreground、
// border-line→border-border、text-fg→text-foreground、text-[11px]→text-2xs；
// 其余与上游一致（本块零 kit import——recharts + shadcn chart 基元 + cn）。
import { useMemo } from "react";
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from "recharts";
import {
  type ChartConfig,
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart";
import { cn } from "@/lib/utils";

/**
 * MetricChart（values 块）——多序列时序折线，序列治理四件套内置（design.md §6.1 裁决）：
 *
 * 1. 活跃窗排序：末样本距窗尾 ≤ activeWindowMs（缺省 5min）的序列优先，峰值次之，
 *    声明序兜底——停更的旧序列不淹没在写序列，也不静默丢数据；
 * 2. 渲染上限 + N more：maxSeries（缺省 10）之外的序列计数披露；
 * 3. 服务端截断披露：serverCap { shown, total }（命中超过服务端上限只回前 N 条时）；
 * 4. 点对齐统一时间轴行 + protojson 零值缺省：v 缺席按 missingValue 解释（缺省 0
 *    ——proto3 标量零值省略序列化，全零是合法序列不是缺数据；"gap" 显式断线）。
 *
 * 序列身份 = key（跨刷新稳定；配色按输入序循环 var(--chart-N)）。数值时间轴单形态
 * （t = ms epoch）；动画/圆点全局关闭（轮询刷新不闪烁）。行字段内部用 s{原始下标}
 * ——key 里出现点/空格等字符不会落进 CSS 变量名。
 */

export interface MetricPoint {
  /** ms epoch（数值时间轴） */
  t: number;
  /** 缺席/空 = 无值，按 missingValue 解释（缺省 0 = protojson 零值缺省方言） */
  v?: number | null;
}

export interface MetricSeries {
  /** 全图唯一身份（跨刷新稳定——配色与 React key 都随它） */
  key: string;
  /** 图例文案（实体词汇由页面给） */
  label: string;
  points: ReadonlyArray<MetricPoint>;
}

export interface BuildMetricRowsOptions {
  maxSeries?: number;
  activeWindowMs?: number;
  missingValue?: number | "gap";
}

export interface BuiltMetricRows {
  /** 对齐到统一时间轴的行（字段 s{原始下标}） */
  rows: ReadonlyArray<Record<string, number | string | null> & { t: number }>;
  /** 渲染面（排序+截断后）的行字段名，即 s{原始下标} */
  fields: ReadonlyArray<string>;
  /** 行字段 → 图例文案 */
  labels: Readonly<Record<string, string>>;
  /** 行字段 → 图例末值（最后一个非空样本） */
  lastValues: Readonly<Record<string, number | undefined>>;
  /** 渲染上限外被截下的序列数 */
  hidden: number;
}

/** 序列→行折叠（纯函数，导出可单测；块内经 useMemo 消费）。 */
export function buildMetricRows(
  series: ReadonlyArray<MetricSeries>,
  options: BuildMetricRowsOptions = {},
): BuiltMetricRows {
  const maxSeries = options.maxSeries ?? 10;
  const activeWindowMs = options.activeWindowMs ?? 5 * 60_000;
  const missingValue = options.missingValue ?? 0;

  const scored = series.map((s, index) => {
    let lastT = Number.NEGATIVE_INFINITY;
    let peak = Number.NEGATIVE_INFINITY;
    for (const point of s.points) {
      if (!Number.isFinite(point.t)) continue;
      if (point.t > lastT) lastT = point.t;
      const v = point.v ?? (missingValue === "gap" ? null : missingValue);
      if (v !== null && v > peak) peak = v;
    }
    return { index, lastT, peak };
  });
  const windowEnd = scored.reduce((acc, s) => Math.max(acc, s.lastT), Number.NEGATIVE_INFINITY);
  const ranked = scored
    .filter((s) => s.lastT > Number.NEGATIVE_INFINITY) // 全脏点序列不进渲染面
    .sort((a, b) => {
      const aActive = windowEnd - a.lastT <= activeWindowMs;
      const bActive = windowEnd - b.lastT <= activeWindowMs;
      if (aActive !== bActive) return aActive ? -1 : 1;
      if (a.peak !== b.peak) return b.peak - a.peak;
      return a.index - b.index;
    });
  const displayed = ranked.slice(0, maxSeries);
  const hidden = series.length - displayed.length;

  const fields: string[] = [];
  const labels: Record<string, string> = {};
  for (const s of displayed) {
    const field = `s${s.index}`;
    fields.push(field);
    labels[field] = series[s.index]!.label;
  }

  // 对齐到统一时间轴行：缺席序列在该时刻无字段（recharts 断线）；v 缺席按方言解释
  const timeMap = new Map<number, Record<string, number | string | null> & { t: number }>();
  for (const s of displayed) {
    const field = `s${s.index}`;
    for (const point of series[s.index]!.points) {
      if (!Number.isFinite(point.t)) continue;
      const row = timeMap.get(point.t) ?? { t: point.t };
      if (point.v === undefined || point.v === null) {
        row[field] = missingValue === "gap" ? null : missingValue;
      } else {
        row[field] = point.v;
      }
      timeMap.set(point.t, row);
    }
  }
  const rows = [...timeMap.values()].sort((a, b) => a.t - b.t);

  const lastValues: Record<string, number | undefined> = {};
  for (const field of fields) {
    for (let i = rows.length - 1; i >= 0; i -= 1) {
      const value = rows[i]![field];
      if (typeof value === "number") {
        lastValues[field] = value;
        break;
      }
    }
  }

  return { rows, fields, labels, lastValues, hidden };
}

export interface MetricChartProps {
  series: ReadonlyArray<MetricSeries>;
  /** y 轴/tooltip/图例末值的量纲格式化（bytes/percent 等单位词表由页面注入） */
  formatValue?: (value: number) => string;
  /** x 轴刻度格式化（缺省 HH:MM） */
  formatTick?: (t: number) => string;
  maxSeries?: number;
  activeWindowMs?: number;
  missingValue?: number | "gap";
  /** 服务端截断披露（命中超过服务端上限只回前 N 条） */
  serverCap?: { shown: number; total: number };
  /** 图高 px（缺省 224） */
  height?: number;
  className?: string;
}

export function MetricChart({
  series,
  formatValue,
  formatTick,
  maxSeries,
  activeWindowMs,
  missingValue,
  serverCap,
  height = 224,
  className,
}: MetricChartProps) {
  const built = useMemo(
    () => buildMetricRows(series, { maxSeries, activeWindowMs, missingValue }),
    [series, maxSeries, activeWindowMs, missingValue],
  );
  const fmt = formatValue ?? ((value: number) => String(value));
  const fmtTick = formatTick ?? ((t: number) => new Date(t).toTimeString().slice(0, 5));

  const config = useMemo(() => {
    const out: ChartConfig = {};
    // 配色按原始下标循环——序列在输入数组中的次序稳定则跨刷新配色稳定
    for (const field of built.fields) {
      const index = Number(field.slice(1));
      out[field] = {
        label: built.labels[field],
        color: `var(--chart-${(index % 5) + 1})`,
      };
    }
    return out;
  }, [built]);

  const capNote =
    serverCap && serverCap.total > serverCap.shown
      ? `server cap reached — ${serverCap.shown} of ${serverCap.total} series shown`
      : null;

  return (
    <div className={cn("flex flex-col", className)} data-testid="metric-chart">
      {built.rows.length === 0 ? (
        <div
          className="flex items-center justify-center rounded-md border border-border text-xs text-muted-foreground"
          style={{ height }}
          data-testid="metric-chart-empty"
        >
          No samples in window
        </div>
      ) : (
        <ChartContainer config={config} className="w-full" style={{ height }}>
          <LineChart data={built.rows} margin={{ left: 4, right: 12, top: 8, bottom: 0 }}>
            <CartesianGrid vertical={false} strokeDasharray="3 3" />
            <XAxis
              dataKey="t"
              type="number"
              scale="time"
              domain={["dataMin", "dataMax"]}
              tickFormatter={fmtTick}
              tickLine={false}
              axisLine={false}
              tickMargin={8}
              minTickGap={48}
            />
            <YAxis tickFormatter={fmt} tickLine={false} axisLine={false} width={64} />
            <ChartTooltip
              content={
                <ChartTooltipContent
                  indicator="line"
                  labelFormatter={(_, payload) => {
                    const t = (payload?.[0]?.payload as { t?: number } | undefined)?.t;
                    return t != null ? new Date(t).toLocaleTimeString() : "";
                  }}
                />
              }
            />
            {built.fields.map((field) => (
              <Line
                key={field}
                dataKey={field}
                name={field}
                stroke={`var(--color-${field})`}
                strokeWidth={1.5}
                dot={false}
                isAnimationActive={false}
              />
            ))}
          </LineChart>
        </ChartContainer>
      )}
      <div className="flex flex-wrap gap-x-4 gap-y-1 px-1 py-1.5 text-xs text-muted-foreground">
        {built.fields.map((field) => (
          <span key={field} className="inline-flex items-center gap-1.5">
            <span
              className="inline-block h-0.5 w-3 rounded-sm"
              style={{ background: `var(--color-${field})` }}
            />
            {built.labels[field]}
            {built.lastValues[field] !== undefined ? (
              <b className="text-foreground">{fmt(built.lastValues[field]!)}</b>
            ) : null}
          </span>
        ))}
      </div>
      {built.hidden > 0 ? (
        <p className="px-1 pb-1 text-2xs text-muted-foreground" data-testid="metric-chart-more">
          +{built.hidden} more series not shown — ranking keeps active series first
        </p>
      ) : null}
      {capNote ? (
        <p className="px-1 pb-1 text-2xs text-muted-foreground" data-testid="metric-chart-cap">
          {capNote}
        </p>
      ) : null}
    </div>
  );
}
