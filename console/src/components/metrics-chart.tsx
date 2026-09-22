// uPlot 薄 React 封装（E6 W5-S3，设计 §4.2——Console 首个图表依赖：
// canvas 时序图 ≈10KB gzip，不引入 recharts 级重库）。职责只有三件：
// 挂载/销毁 uPlot 实例、ResizeObserver 宽度自适应、空态渲染。
// 数据转换（series → uPlot 的 [x, y1, …] 列式形态）就地完成，不设第二套
// 图表状态。
//
// 锚点纪律（锚点只增）：data-testid="metrics-chart"。

import { useEffect, useMemo, useRef } from "react";

import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";

export interface ChartSeries {
  /** 图例与 tooltip 用的序列名（如服务名或指标名）。 */
  label: string;
  /** 点集（t = unix 秒升序；v = 采样值）。 */
  points: { t: number; v: number }[];
}

interface MetricsChartProps {
  /** 多序列（uPlot 多 y 轴曲线）；空数组/全空点集 → 空态。 */
  series: ChartSeries[];
  /** 图表高度（px，缺省 160）。 */
  height?: number;
  /** 值格式化（tooltip/图例；缺省两位小数）。 */
  formatValue?: (v: number) => string;
  /** 无障碍名（role="img" aria-label）。 */
  ariaLabel?: string;
}

/** 缺省值格式化（资源水位两位小数即可读）。 */
const defaultFormat = (v: number) => (Number.isFinite(v) ? v.toFixed(2) : "-");

export function MetricsChart({
  series,
  height = 160,
  formatValue = defaultFormat,
  ariaLabel = "metrics chart",
}: MetricsChartProps) {
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const plotRef = useRef<uPlot | null>(null);
  // 回调快照（uPlot Options 闭包在实例生命周期内固定——格式化函数经 ref
  // 间接寻址，避免重建实例；赋值在 effect 期，不在渲染期触 ref）。
  const formatRef = useRef(formatValue);
  useEffect(() => {
    formatRef.current = formatValue;
  }, [formatValue]);

  // 列式对齐数据：以全部序列时间戳的并集升序为 x 轴，缺样点填 null
  //（uPlot 断线语义——不补零伪造连续性）。
  const aligned = useMemo<AlignedData>(() => {
    const stamps = new Set<number>();
    for (const s of series) for (const p of s.points) stamps.add(p.t);
    const xs = [...stamps].sort((a, b) => a - b);
    const out: AlignedData = [xs];
    for (const s of series) {
      const byT = new Map(s.points.map((p) => [p.t, p.v]));
      out.push(xs.map((t) => (byT.has(t) ? (byT.get(t) as number) : null)));
    }
    return out;
  }, [series]);

  const hasData = series.length > 0 && series.some((s) => s.points.length > 0);

  useEffect(() => {
    if (!wrapRef.current || !hasData) return;
    const width = wrapRef.current.clientWidth || 320;
    const opts: Options = {
      width,
      height,
      class: "metrics-chart-canvas",
      legend: { show: series.length > 1 },
      cursor: { points: { show: false } },
      axes: [
        {
          // x 轴：unix 秒 → HH:MM（资源卡粒度的可读形态）。
          values: (_u, ticks) =>
            ticks.map((v) =>
              new Date((v as number) * 1000).toISOString().slice(11, 16),
            ),
        },
        {
          values: (_u, ticks) => ticks.map((v) => formatRef.current(v as number)),
        },
      ],
      series: [
        {},
        ...series.map((s, i) => ({
          label: s.label,
          stroke: STROKE_COLORS[i % STROKE_COLORS.length],
          width: 1.5,
          value: (_u: uPlot, v: number) => formatRef.current(v),
          points: { show: s.points.length < 24 },
        })),
      ],
    };
    const plot = new uPlot(opts, aligned, wrapRef.current);
    plotRef.current = plot;

    // 容器宽度自适应（布局切换/窗口缩放——uPlot 无 auto-width，观察者
    // 驱动 setSize）。
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w && plotRef.current) {
        plotRef.current.setSize({ width: Math.floor(w), height });
      }
    });
    ro.observe(wrapRef.current);

    return () => {
      ro.disconnect();
      plot.destroy();
      plotRef.current = null;
    };
  }, [aligned, hasData, height, series]);

  if (!hasData) {
    return (
      <div className="flex h-40 items-center justify-center rounded-md border border-dashed text-xs text-muted-foreground" data-testid="metrics-chart">
        No samples in the queried window yet.
      </div>
    );
  }
  return (
    <div ref={wrapRef} className="w-full" data-testid="metrics-chart" role="img" aria-label={ariaLabel} />
  );
}

// 序列描边色板（拾色少而可辨——资源卡当前 ≤2 序列/图；深浅主题共用的
// 中饱和色）。
const STROKE_COLORS = ["#2563eb", "#d97706", "#059669", "#7c3aed", "#dc2626"];
