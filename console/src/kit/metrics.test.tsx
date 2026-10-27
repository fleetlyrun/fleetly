import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { METRIC_RANGES, useRawMetricsSeries, useScopedMetricsSeries } from "./metrics";
import { setToken } from "@/lib/token";

// 指标读面迁移锚（批4 自 lib/catalog.ts 迁入）：空 query/空 scope 不发请求、
// raw 窗口参数（query/start/end/step_seconds）、scoped 的 app/database 分形
// （process 仅 app scope 传递）与截断披露透传。

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

describe("METRIC_RANGES", () => {
  it("keeps the four chart windows in milliseconds", () => {
    expect(METRIC_RANGES).toEqual({
      "30m": 1_800_000,
      "1h": 3_600_000,
      "6h": 21_600_000,
      "24h": 86_400_000,
    });
  });
});

describe("useRawMetricsSeries", () => {
  it("does not query with an empty promql", async () => {
    const { client, wrapper } = withClient();
    renderHook(() => useRawMetricsSeries("", "1h"), { wrapper });
    expect(fetchMock).not.toHaveBeenCalled();
    client.clear();
  });

  it("queries with the explicit window and flattens multi-series rows", async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ series: [{ labels: { __name__: "up" }, values: [] }, undefined] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useRawMetricsSeries("up", "1h"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toHaveLength(1);
    const [path] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(path)).toContain("/v1/metrics?");
    expect(String(path)).toContain("query=up");
    expect(String(path)).toContain("start=");
    expect(String(path)).toContain("end=");
    expect(String(path)).toContain("step_seconds=");
    client.clear();
  });
});

describe("useScopedMetricsSeries", () => {
  it("queries app scope with metric and process, preserving truncation metadata", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({ series: [{ values: [] }], truncated: true, series_total: 12 }),
        { status: 200 },
      ),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(
      () => useScopedMetricsSeries({ kind: "app", id: "a1" }, "cpu_percent", "WEB", "1h"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toMatchObject({ truncated: true, seriesTotal: 12 });
    const [path] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(path)).toContain("/v1/metrics/scoped?");
    expect(String(path)).toContain("app_id=a1");
    expect(String(path)).toContain("metric=cpu_percent");
    expect(String(path)).toContain("process=web");
    client.clear();
  });

  it("drops process on database scope and defaults seriesTotal when the server omits it", async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ series: [{ values: [] }, { values: [] }] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(
      () => useScopedMetricsSeries({ kind: "database", id: "db1" }, "memory_working_set_bytes", "web", "1h"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.seriesTotal).toBe(2);
    const [path] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(path)).toContain("database_id=db1");
    expect(String(path)).not.toContain("process=");
    expect(String(path)).not.toContain("app_id=");
    client.clear();
  });

  it("does not query with an empty scope id", async () => {
    const { client, wrapper } = withClient();
    renderHook(() => useScopedMetricsSeries({ kind: "app", id: "" }, "cpu_percent", undefined, "1h"), { wrapper });
    expect(fetchMock).not.toHaveBeenCalled();
    client.clear();
  });
});
