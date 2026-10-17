import { afterEach, describe, expect, it } from "vitest";
import { Suspense } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { RouteFallback } from "./route-fallback";

// W-3 壳级 fallback：懒加载路由切片的慢链窗口不再整块全黑——Spinner
// 自带 role/aria（读屏可达）；Suspense 集成形态钉壳层接线语义。

afterEach(() => cleanup());

describe("RouteFallback", () => {
  it("renders an accessible loading state", () => {
    render(<RouteFallback />);
    expect(screen.getByRole("status")).toBeTruthy();
  });

  it("shows the fallback while a lazy route chunk suspends", () => {
    // 永不 resolve 的 thenable：模拟慢 chunk 拉取窗（不落 resolve 时序断言）。
    function NeverResolves(): null {
      throw new Promise(() => {});
    }
    render(
      <Suspense fallback={<RouteFallback />}>
        <NeverResolves />
      </Suspense>,
    );
    expect(screen.getByRole("status")).toBeTruthy();
  });
});
