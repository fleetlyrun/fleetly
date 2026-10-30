import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppDetailLayout } from "./app-hero";
import { setToken } from "@/lib/token";

// App hero Redeploy 接线钉测（B1 W4 遗留直发面收口，2026-10-31）：裸
// AlertDialog 直发退役 → ConfirmSkipDialog 双击 skip 形态——首击回读冻结
// Spec 无票直发（428 REQUIRED）→ 「已请求确认」态（Redeploy 动词措辞）
// → 二击 confirmation.skip=true。宿主组件随本批自路由薄壳外挪（同
// Overview tab 先例），路由机制不在测试面。

const { fetchMock } = vi.hoisted(() => ({ fetchMock: vi.fn() }));

// hero 布局消费 Outlet/useLocation（tab 活动态）；useCollection 内件读
// useSearch（URL 集合参数）——裸 render 无路由树，一并桩化（与 apps-tabs
// 各 tab 测试同法）。
vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return {
    ...actual,
    Outlet: () => null,
    // useLocation 按真形应用 select（hero 只投影 pathname）。
    useLocation: ({ select }: { select?: (location: { pathname: string }) => unknown }) =>
      select ? select({ pathname: "/p/P1/apps/A1" }) : { pathname: "/p/P1/apps/A1" },
    useSearch: () => ({}),
    // ContentCrumb 的 Link 需路由树上下文——桩为裸锚点（crumb 不在断言面）。
    Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  };
}

// redeploy 链的 POST /v1/deployments 响应序（首击 428 → 二击 200）。
let deployResponses: Array<{ status: number; body: unknown }> = [];

beforeEach(() => {
  fetchMock.mockReset();
  deployResponses = [];
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

const FROZEN = { processes: [{ name: "web", replicas: "2" }] };

function stubFetch() {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path.startsWith("/v1/apps?")) {
      return new Response(JSON.stringify({ apps: [{ id: "A1", project_id: "P1", name: "web" }] }), { status: 200 });
    }
    if (path.startsWith("/v1/projects")) {
      return new Response(JSON.stringify({ projects: [{ id: "P1", name: "shop" }] }), { status: 200 });
    }
    if (path.startsWith("/v1/deployments?")) {
      // hero 状态徽标/Redeploy 门闩需要 latest 部署在册。
      return new Response(
        JSON.stringify({ deployments: [{ id: "D0", app_id: "A1", state: "DEPLOYMENT_STATE_SUCCEEDED", updated_at: "t" }] }),
        { status: 200 },
      );
    }
    if (path.startsWith("/v1/alerts")) return new Response(JSON.stringify({ states: [] }), { status: 200 });
    if (path.startsWith("/v1/apps/A1/workloads")) return new Response(JSON.stringify({}), { status: 200 });
    if (path.startsWith("/v1/apps/A1/spec")) {
      return new Response(JSON.stringify({ spec: FROZEN }), { status: 200 });
    }
    if (path === "/v1/deployments" && (init?.method ?? "GET") === "POST") {
      const next = deployResponses.shift();
      if (next != null && next.status !== 200) {
        return new Response(JSON.stringify(next.body), { status: next.status });
      }
      return new Response(JSON.stringify({ deployment: { id: "D9" } }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
}

function deployPosts(): Array<[string, RequestInit]> {
  return fetchMock.mock.calls.filter(([p, i]) => p === "/v1/deployments" && i?.method === "POST") as Array<[string, RequestInit]>;
}

function renderLayout() {
  const { client, wrapper } = withClient();
  render(<AppDetailLayout projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId" />, { wrapper });
  return { client };
}

describe("App hero Redeploy 双击 skip 形态", () => {
  it("first click resends the frozen spec without a ticket, second click sends an explicit skip", async () => {
    stubFetch();
    deployResponses = [
      { status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation: pass the ticket from a preview, or set skip" } },
    ];
    const { client } = renderLayout();

    // hero 落定（apps 目录回读后 Redeploy 门闩开）。
    const trigger = await screen.findByRole("button", { name: "Redeploy" });
    await waitFor(() => expect((trigger as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(trigger);
    expect(await screen.findByText("Redeploy web?")).toBeTruthy();

    // 首击：GET 冻结体 → POST 无 confirmation 键。
    fireEvent.click(screen.getByRole("button", { name: "Redeploy" }));
    await waitFor(() => expect(deployPosts()).toHaveLength(1));
    expect(JSON.parse(String(deployPosts()[0][1].body))).toEqual({ app_id: "A1", spec_file: JSON.stringify(FROZEN) });

    // REQUIRED → 「已请求确认」态（Redeploy 动词措辞 + skip 审计语义）。
    expect(await screen.findByText(/Redeploying requires an explicit confirmation/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Confirm redeploy" })).toBeTruthy();

    // 二击：skip=true（--yes 逃生门通道，proof=skipped 进审计）。
    fireEvent.click(screen.getByRole("button", { name: "Confirm redeploy" }));
    await waitFor(() => expect(deployPosts()).toHaveLength(2));
    expect(JSON.parse(String(deployPosts()[1][1].body))).toEqual({
      app_id: "A1",
      spec_file: JSON.stringify(FROZEN),
      confirmation: { skip: true },
    });
    client.clear();
  });
});
