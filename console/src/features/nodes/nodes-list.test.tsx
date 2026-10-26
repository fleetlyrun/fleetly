import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { NodesPage } from "./nodes-list";
import { nodesSearch } from "@/kit/nodes";
import { setToken } from "@/lib/token";

// 本文件多测试连渲：vitest 未开 globals，testing-library 的自动 cleanup
// 不生效——afterEach 显式卸载，避免跨测试 DOM 残留撞查询。
//（批1 Wave2-A useCollection 化：useCollection 需要 Router/Query 两上下文，
// 裸 render 不再成立——内联最小路由（resources/panels.test 同款）。）

// NODES_ROUTE_ID：测试路由自持的 id——useSearch({ from }) 按此锚定活跃
// match（真实路由 id 是 /_shell/nodes/，同形参数化在此等价）。
const NODES_ROUTE_ID = "/nodes";

function renderNodesPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ validateSearch: nodesSearch.toSearchSchema() });
  const pageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: NODES_ROUTE_ID,
    component: () => (
      <QueryClientProvider client={queryClient}>
        <NodesPage from={NODES_ROUTE_ID} />
      </QueryClientProvider>
    ),
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([pageRoute]),
    history: createMemoryHistory({ initialEntries: [NODES_ROUTE_ID] }),
  });
  return { queryClient, ...render(<RouterProvider router={router} />) };
}

interface FetchCall {
  path: string;
  body: unknown;
}

const fetchMock = vi.fn();
const calls: FetchCall[] = [];

// fetch 面桩：whoami 的 platform_admin 是动作显隐唯一依据；nodes 列表含
// manager（不可取定向材料）+ worker（可铸/吊销）。
function stubFetch(platformAdmin: boolean) {
  calls.length = 0;
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    calls.push({ path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (path === "/v1/whoami") {
      return new Response(
        JSON.stringify({ token_name: "console", role_name: "builtin-owner", user_name: "root", platform_admin: platformAdmin }),
        { status: 200 },
      );
    }
    if (path === "/v1/nodes") {
      return new Response(
        JSON.stringify({
          nodes: [
            { platform_id: "N1", carrier_id: "c1", hostname: "manager-1", role: "manager", available: true, relay_online: true },
            { platform_id: "W1", carrier_id: "c2", hostname: "worker-1", role: "worker", available: true, relay_online: false },
          ],
        }),
        { status: 200 },
      );
    }
    if (path === "/v1/nodes/enroll") {
      return new Response(JSON.stringify({ relay_command: "docker run fake-relay-script" }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
}

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

describe("NodesPage relay actions", () => {
  it("shows the two relay actions on worker rows only (platform admin)", async () => {
    stubFetch(true);
    const { queryClient } = renderNodesPage();
    await waitFor(() => expect(screen.getByText("worker-1")).toBeTruthy());

    const workerRow = screen.getByText("worker-1").closest("tr");
    const managerRow = screen.getByText("manager-1").closest("tr");
    expect(workerRow).not.toBeNull();
    expect(managerRow).not.toBeNull();
    expect(within(workerRow as HTMLElement).getByText("Relay command")).toBeTruthy();
    expect(within(workerRow as HTMLElement).getByText("Revoke relay")).toBeTruthy();
    expect(within(managerRow as HTMLElement).queryByText("Relay command")).toBeNull();
    queryClient.clear();
  });

  it("issues the relay command for a node and reveals the sensitive script", async () => {
    stubFetch(true);
    const { queryClient } = renderNodesPage();
    await waitFor(() => expect(screen.getByText("worker-1")).toBeTruthy());

    fireEvent.click(screen.getByText("Relay command"));
    await waitFor(() => expect(screen.getByText(/Relay command for W1/)).toBeTruthy());
    expect(screen.getByText(/treat this script as node-root material/i)).toBeTruthy();
    expect(screen.getByText("docker run fake-relay-script")).toBeTruthy();

    const enrollCall = calls.find((call) => call.path === "/v1/nodes/enroll");
    expect(enrollCall?.body).toEqual({ node_id: "W1" });
    queryClient.clear();
  });

  it("revokes a node relay credential after confirmation", async () => {
    stubFetch(true);
    const { queryClient } = renderNodesPage();
    await waitFor(() => expect(screen.getByText("worker-1")).toBeTruthy());

    fireEvent.click(screen.getByText("Revoke relay"));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/Revoke relay credential for worker-1/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke relay" }));

    await waitFor(() => {
      const enrollCall = calls.find((call) => call.path === "/v1/nodes/enroll");
      expect(enrollCall?.body).toEqual({ node_id: "W1", revoke: true });
    });
    queryClient.clear();
  });

  it("hides the whole node surface when platform_admin is unknown/false (fail-closed)", async () => {
    stubFetch(false);
    const { queryClient } = renderNodesPage();
    await waitFor(() => expect(screen.getByText("Platform admin only")).toBeTruthy());
    expect(screen.queryByText("worker-1")).toBeNull();
    expect(calls.some((call) => call.path === "/v1/nodes")).toBe(false);
    queryClient.clear();
  });
});
