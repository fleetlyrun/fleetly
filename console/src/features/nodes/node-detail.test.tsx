import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { NodeDetailPage } from "./node-detail";
import { setToken } from "@/lib/token";

// 本文件多测试连渲：vitest 未开 globals，testing-library 的自动 cleanup
// 不生效——afterEach 显式卸载，避免跨测试 DOM 残留撞查询。

// Link 渲染脱离 Router context（详情页 crumb 回列表页用 Link）——桩成
// 纯 a 元素即可（导航行为不在本组件测试面）。
vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return {
    ...actual,
    Link: (props: { children?: ReactNode }) => <a>{props.children}</a>,
  };
});

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
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

// fetch 面桩：节点行（英雄卡数据源）+ 节点在跑容器集（四域投影字段回显）。
function stubFetch() {
  fetchMock.mockImplementation(async (path: string) => {
    if (path.startsWith("/v1/nodes/N1/containers")) {
      return new Response(
        JSON.stringify({
          containers: [
            {
              instance: "task-aaa1", workload_id: "web-1", domain: "app", team: "acme",
              project_id: "shop", app_id: "web", process: "api", image: "nginx:1.27",
            },
            { instance: "task-aaa2", workload_id: "db1", domain: "database", image: "pg:16" },
          ],
        }),
        { status: 200 },
      );
    }
    if (path === "/v1/nodes") {
      return new Response(
        JSON.stringify({
          nodes: [
            {
              platform_id: "N1", seq: "1", carrier_id: "c1", hostname: "fleetly-dev",
              role: "manager", available: true, first_seen_at: "2026-10-16T00:00:00Z",
              last_seen_at: "2026-10-16T00:00:10Z", relay_online: true, relay_version: "d60",
            },
          ],
        }),
        { status: 200 },
      );
    }
    return new Response("{}", { status: 200 });
  });
}

describe("NodeDetailPage containers", () => {
  it("renders the node hero and container rows with attribution", async () => {
    stubFetch();
    const { client, wrapper } = withClient();
    render(<NodeDetailPage nodeId="N1" />, { wrapper });
    await waitFor(() => expect(screen.getAllByText("fleetly-dev").length).toBeGreaterThan(0));

    await waitFor(() => expect(screen.getByText("task-aaa1…")).toBeTruthy());
    expect(screen.getByText("web · api")).toBeTruthy();
    expect(screen.getByText("nginx:1.27")).toBeTruthy();
    // 非应用域：workload_id 即域主体，域徽标呈现。
    expect(screen.getByText("db1")).toBeTruthy();
    expect(screen.getByText("database")).toBeTruthy();
    expect(screen.getByText("2 running")).toBeTruthy();
    client.clear();
  });

  it("shows the honest empty state when no containers run", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/nodes/N1/containers")) return new Response(JSON.stringify({ containers: [] }), { status: 200 });
      if (path === "/v1/nodes") {
        return new Response(JSON.stringify({ nodes: [{ platform_id: "N1", hostname: "fleetly-dev", role: "manager", available: true }] }), { status: 200 });
      }
      return new Response("{}", { status: 200 });
    });
    const { client, wrapper } = withClient();
    render(<NodeDetailPage nodeId="N1" />, { wrapper });
    await waitFor(() => expect(screen.getByText("No running containers on this node")).toBeTruthy());
    client.clear();
  });

  it("shows not-found when the node row is gone (stale link)", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path === "/v1/nodes") return new Response(JSON.stringify({ nodes: [] }), { status: 200 });
      return new Response("{}", { status: 200 });
    });
    const { client, wrapper } = withClient();
    render(<NodeDetailPage nodeId="N1" />, { wrapper });
    await waitFor(() => expect(screen.getByText("Node not found")).toBeTruthy());
    client.clear();
  });
});
