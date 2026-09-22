// metrics-chart 单测（E6 W5-S3）：uPlot 以模块 mock 承载（jsdom 无 canvas
// 2D 上下文，真库构造期即抛）——断言组件对 uPlot 的消费契约：列式对齐
// 数据、缺样断线（null）、实例生命周期（resize/destroy）、空态渲染。

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { MetricsChart } from "@/components/metrics-chart";

// uplot 假件：记录构造参数与实例（消费契约断言面）。
const instances: {
  opts: { width: number; height: number; series: { label: string }[] };
  data: (number[] | (number | null)[])[];
  sizes: { width: number; height: number }[];
  destroyed: boolean;
  resizeObserver: ResizeObserver | null;
}[] = [];

// ResizeObserver 由全局 setup 桩承载（尺寸驱动断言经由实例的 sizes 账面，
// 不直接驱动观察者回调）。

vi.mock("uplot", () => ({
  default: class FakeUPlot {
    opts: { width: number; height: number; series: { label: string }[] };
    data: (number[] | (number | null)[])[];
    sizes: { width: number; height: number }[] = [];
    destroyed = false;
    constructor(
      opts: { width: number; height: number; series: { label: string }[] },
      data: (number[] | (number | null)[])[],
      _el: HTMLElement,
    ) {
      this.opts = opts;
      this.data = data;
      instances.push(this as never);
    }
    setSize(size: { width: number; height: number }) {
      this.sizes.push(size);
    }
    destroy() {
      this.destroyed = true;
    }
  },
}));

// jsdom 无 ResizeObserver：注入捕获式假件。
class FakeResizeObserver {
  observe(_el: Element) {}
  disconnect() {}
  unobserve(_el: Element) {}
  static callback: ((entries: { contentRect: { width: number } }[]) => void) | null = null;
  constructor(cb: (entries: { contentRect: { width: number } }[]) => void) {
    FakeResizeObserver.callback = cb;
  }
}
vi.stubGlobal("ResizeObserver", FakeResizeObserver);

describe("MetricsChart", () => {
  it("renders the empty state (same anchor) and never constructs uPlot without samples", () => {
    instances.length = 0;
    render(<MetricsChart series={[]} />);
    expect(screen.getByTestId("metrics-chart").textContent).toContain("No samples");
    expect(instances).toHaveLength(0);
  });

  it("passes column-aligned data with null gaps to uPlot and renders the anchor", () => {
    instances.length = 0;
    render(
      <MetricsChart
        ariaLabel="cpu chart"
        series={[
          { label: "web", points: [{ t: 100, v: 1.5 }, { t: 300, v: 2.5 }] },
          { label: "api", points: [{ t: 200, v: 9 }] },
        ]}
        height={120}
      />,
    );
    expect(screen.getByTestId("metrics-chart")).toBeInTheDocument();
    expect(instances).toHaveLength(1);
    const inst = instances[0]!;
    // 构造参数：宽度来自容器（jsdom 0 → 回落 320）、高度透传、序列名。
    expect(inst.opts.width).toBeGreaterThan(0);
    expect(inst.opts.height).toBe(120);
    expect(inst.opts.series.slice(1).map((s) => s.label)).toEqual(["web", "api"]);
    // x 轴 = 时间戳并集升序；web 在 t=200 缺样 → null（断线，不补零）。
    expect(inst.data[0]).toEqual([100, 200, 300]);
    expect(inst.data[1]).toEqual([1.5, null, 2.5]);
    expect(inst.data[2]).toEqual([null, 9, null]);
  });

  it("formats values through the injected formatter", () => {
    instances.length = 0;
    render(
      <MetricsChart
        series={[{ label: "m", points: [{ t: 1, v: 1048576 }] }]}
        formatValue={(v) => `${(v / 1048576).toFixed(0)} MiB`}
      />,
    );
    const inst = instances[0]!;
    // 序列 value 格式化经 opts.series[i].value（uPlot 惯例回调）。
    const valueFn = (inst.opts.series as unknown as { value: (u: unknown, v: number) => string }[])[1]!
      .value;
    expect(valueFn(null, 1048576)).toBe("1 MiB");
  });

  it("destroys the instance on unmount", () => {
    instances.length = 0;
    const { unmount } = render(
      <MetricsChart series={[{ label: "m", points: [{ t: 1, v: 1 }] }]} />,
    );
    expect(instances).toHaveLength(1);
    unmount();
    expect((instances[0] as unknown as { destroyed: boolean }).destroyed).toBe(true);
  });
});
