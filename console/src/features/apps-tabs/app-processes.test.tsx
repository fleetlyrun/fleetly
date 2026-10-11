import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppProcessesTab } from "./app-processes";
import { setToken } from "@/lib/token";

// Apply changes 受理后的导航锚：部署成功必须落 typed 部署详情路由。
// 回归锚：旧实现 window.location.assign("../deployments/<id>") 相对
// /p/<pid>/apps/<aid>/processes 解析成 /p/<pid>/apps/deployments/<id>
// （.. 吃掉 appId 段）——路由把 "deployments" 认成 $appId → 404 页。
const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  navigateMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

// fetch 面桩：冻结 Spec（web ×1）+ 路由目录空 + Deploy 受理回 D1。
function stubFetch() {
  fetchMock.mockImplementation(async (path: string) => {
    if (path.startsWith("/v1/apps/A1/spec")) {
      return new Response(JSON.stringify({ spec: { processes: [{ name: "web", replicas: "1" }] } }), { status: 200 });
    }
    if (path.startsWith("/v1/routes")) return new Response(JSON.stringify({ routes: [] }), { status: 200 });
    if (path === "/v1/deployments") return new Response(JSON.stringify({ deployment: { id: "D1" } }), { status: 200 });
    return new Response("{}", { status: 200 });
  });
}

describe("AppProcessesTab apply changes", () => {
  it("navigates to the created deployment detail after staging replicas", async () => {
    stubFetch();
    const { client, wrapper } = withClient();
    render(<AppProcessesTab projectId="P1" appId="A1" />, { wrapper });
    await waitFor(() => expect(screen.getByText("web")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "Increase replicas of web" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply changes" }));

    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
        params: { projectId: "P1", appId: "A1", deploymentId: "D1" },
      }),
    );
    const [, init] = fetchMock.mock.calls.find(([p]) => p === "/v1/deployments") as [string, RequestInit];
    expect(JSON.parse(String(init.body)).spec_file).toContain('"replicas":"2"');
    client.clear();
  });

  it("sends the empty state to the deployments tab (deploy entry lives there)", async () => {
    // 未部署 = spec 404（W-3 空态面）；旧 ?deploy=1 整页 reload 是死参。
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/apps/A1/spec")) {
        return new Response(JSON.stringify({ code: "not_found", message: "no deployment" }), { status: 404 });
      }
      if (path.startsWith("/v1/routes")) return new Response(JSON.stringify({ routes: [] }), { status: 200 });
      return new Response("{}", { status: 200 });
    });
    const { client, wrapper } = withClient();
    render(<AppProcessesTab projectId="P1" appId="A1" />, { wrapper });
    await waitFor(() => expect(screen.getByText("No frozen spec yet")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "Deploy now" }));

    expect(navigateMock).toHaveBeenCalledWith({
      to: "/p/$projectId/apps/$appId/deployments",
      params: { projectId: "P1", appId: "A1" },
    });
    client.clear();
  });
});
