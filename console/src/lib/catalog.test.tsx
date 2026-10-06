import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { useApps, useProjects } from "./catalog";
import { setToken } from "./token";

// 目录查询消费面锚（F2.6 只读面 + F3.1 扩面共用）：REST 路径/查询串、
// Bearer 头、行展平（undefined 行过滤）、ListApps 的 project_id 必填
// enabled 门（走查 F4 的回归锚——空项目不发请求）。

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

describe("useProjects", () => {
  it("fetches with the bearer header and flattens rows", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ projects: [{ id: "01M4P1", name: "shop" }, undefined, { id: "01M4P2", name: "lab" }] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useProjects(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([
      { id: "01M4P1", name: "shop" },
      { id: "01M4P2", name: "lab" },
    ]);
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/v1/projects?limit=100");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer flt_test_token");
    client.clear();
  });
});

describe("useApps", () => {
  it("does not query with an empty project id (ListApps requires project_id)", async () => {
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useApps(""), { wrapper });
    // enabled=false：挂载一个微任务拍后 fetch 仍零调用。
    await Promise.resolve();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(result.current.data).toBeUndefined();
    client.clear();
  });

  it("queries per project and drops nameless rows", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ apps: [{ id: "01M4A1", project_id: "01M4P1", name: "web" }, { id: "01M4A2" }] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useApps("01M4P1"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([{ id: "01M4A1", project_id: "01M4P1", name: "web" }]);
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe("/v1/apps?project_id=01M4P1&limit=200");
    client.clear();
  });
});
