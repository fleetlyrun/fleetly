import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppOverviewTab } from "./app-overview";
import { setToken } from "@/lib/token";

// Overview 的 per-process 摘要卡（层级定案 2026-10-12）：声明 Replicas
//（spec）× live Running（exec instances 观测）并排——App 汇总之下的
// process 行，不进实例明细（钻取点在 Processes 页）。Link 依赖 router
// 上下文（useLinkProps 读 router 实例），单测无路由树——换纯 <a> 桩
//（同文件 useNavigate 桩的先例，app-processes.test）。

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return {
    ...actual,
    Link: ({ children }: { children?: ReactNode }) => <a href="#">{children}</a>,
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

describe("AppOverviewTab processes summary", () => {
  it("lists per-process declared replicas beside the live running count", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/apps?")) {
        return new Response(JSON.stringify({ apps: [{ id: "A1", project_id: "P1", name: "alpha" }] }), { status: 200 });
      }
      if (path.startsWith("/v1/apps/A1/spec")) {
        return new Response(
          JSON.stringify({ spec: { processes: [{ name: "web", replicas: "2" }, { name: "worker", replicas: "1" }] } }),
          { status: 200 },
        );
      }
      if (path.startsWith("/v1/apps/A1/exec/instances")) {
        const process = new URLSearchParams(path.split("?")[1] ?? "").get("process") ?? "";
        const instances = process === "web" ? [{ instance: "task-w1", node_id: "n1" }] : [];
        return new Response(JSON.stringify({ instances }), { status: 200 });
      }
      if (path.startsWith("/v1/deployments")) return new Response(JSON.stringify({ deployments: [] }), { status: 200 });
      if (path.startsWith("/v1/revisions")) return new Response(JSON.stringify({ revisions: [] }), { status: 200 });
      if (path.startsWith("/v1/routes")) return new Response(JSON.stringify({ routes: [] }), { status: 200 });
      if (path.startsWith("/v1/alerts")) return new Response(JSON.stringify({ states: [] }), { status: 200 });
      return new Response(JSON.stringify({ series: [] }), { status: 200 });
    });
    const { client, wrapper } = withClient();
    render(<AppOverviewTab projectId="P1" appId="A1" />, { wrapper });

    await waitFor(() => expect(screen.getByText("Processes")).toBeTruthy());
    // 行出现 = apps 目录 → spec 扇出两轮异步已落值
    await waitFor(() => expect(screen.getByText("web")).toBeTruthy());
    await waitFor(() => expect(screen.getByText("worker")).toBeTruthy());
    const webRow = screen.getByText("web").closest("tr");
    const workerRow = screen.getByText("worker").closest("tr");
    expect(webRow).not.toBeNull();
    expect(workerRow).not.toBeNull();
    // 声明 Replicas（spec 冻结值）即时呈现
    expect(within(webRow as HTMLElement).getByText("2")).toBeTruthy();
    expect(within(workerRow as HTMLElement).getByText("1")).toBeTruthy();
    // live Running（观测查询）异步落值
    await waitFor(() => expect(within(webRow as HTMLElement).getByText("1")).toBeTruthy());
    await waitFor(() => expect(within(workerRow as HTMLElement).getByText("0")).toBeTruthy());
    client.clear();
  });
});
