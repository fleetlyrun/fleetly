import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppProcessesTab } from "./app-processes";
import { setToken } from "@/lib/token";

// 本文件多测试连渲：vitest 未开 globals，testing-library 的自动 cleanup
// 不生效——afterEach 显式卸载，避免跨测试 DOM 残留撞查询。

// Apply changes 受理后的导航锚：部署成功必须落 typed 部署详情路由。
// 回归锚：旧实现 window.location.assign("../deployments/<id>") 相对
// /p/<pid>/apps/<aid>/processes 解析成 /p/<pid>/apps/deployments/<id>
// （.. 吃掉 appId 段）——路由把 "deployments" 认成 $appId → 404 页。
const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

// 批4 useCollection 化：绑定层读 useSearch（路由归因集合）——裸 render 无
// 路由树，桩化为恒空（lookup schema 走缺省窗口）。
vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock, useSearch: () => ({}) };
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
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

// fetch 面桩：冻结 Spec（web ×1）+ 路由目录空 + Deploy 受理回 D1 + 在跑
// 实例集观测（Running 列/行展开的数据源，ADR-0067）。
function stubFetch() {
  fetchMock.mockImplementation(async (path: string) => {
    if (path.startsWith("/v1/apps/A1/spec")) {
      return new Response(JSON.stringify({ spec: { processes: [{ name: "web", replicas: "1" }] } }), { status: 200 });
    }
    if (path.startsWith("/v1/apps/A1/exec/instances")) {
      return new Response(
        JSON.stringify({ instances: [{ instance: "task-aaa1", node_id: "n1" }, { instance: "task-aaa2", node_id: "n2" }] }),
        { status: 200 },
      );
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
    render(<AppProcessesTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/processes" />, { wrapper });
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

  it("opens the deploy sheet directly from the empty state (no detour through the deployments list)", async () => {
    // 未部署 = spec 404（W-3 空态面）；走查反馈（2026-10-27）：旧动线跳
    // Deployments 列表再点一次 Deploy… 绕一跳——Deploy now 直开表单。
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/apps/A1/spec")) {
        return new Response(JSON.stringify({ code: "not_found", message: "no deployment" }), { status: 404 });
      }
      if (path.startsWith("/v1/routes")) return new Response(JSON.stringify({ routes: [] }), { status: 200 });
      if (path.startsWith("/v1/apps?")) {
        return new Response(JSON.stringify({ apps: [{ id: "A1", project_id: "P1", name: "alpha" }] }), { status: 200 });
      }
      if (path.startsWith("/v1/apps/A1/hook")) return new Response("not found", { status: 404 });
      if (path.startsWith("/v1/storage/uploads")) return new Response(JSON.stringify({ uploads: [] }), { status: 200 });
      return new Response("{}", { status: 200 });
    });
    const { client, wrapper } = withClient();
    render(<AppProcessesTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/processes" />, { wrapper });
    await waitFor(() => expect(screen.getByText("No frozen spec yet")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "Deploy now" }));

    // DeploySheet 直开：非 hook + 无冻结体 → 五源表单（缺省 image）。
    expect(await screen.findByText("Create a deployment — the server queues and supersedes by generation.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Deploy" })).toBeTruthy();
    expect(navigateMock).not.toHaveBeenCalled();
    client.clear();
  });
});

// 实例钻取面（2026-10-12 层级定案）：Running 列对照声明 Replicas，行展开
// 见实例明细（编排器载体标识全值 + 节点）——App→Process→Instance 的唯一
// 钻取点。
describe("AppProcessesTab instance drill-down", () => {
  it("shows the live running count and expands to instance rows", async () => {
    stubFetch();
    const { client, wrapper } = withClient();
    render(<AppProcessesTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/processes" />, { wrapper });
    await waitFor(() => expect(screen.getByText("web")).toBeTruthy());
    await waitFor(() => expect(screen.getByText("2")).toBeTruthy()); // Running 列：2 在跑

    fireEvent.click(screen.getByRole("button", { name: "Toggle instances of web" }));

    await waitFor(() => expect(screen.getByText("task-aaa1")).toBeTruthy());
    expect(screen.getByText("task-aaa2")).toBeTruthy();
    expect(screen.getByText("n1")).toBeTruthy();
    client.clear();
  });
});

// 行级 Restart（ADR-0069）：确认后 POST RestartProcess 端点——零 lineage
// 排障动线（不动 Spec、不建部署），路径按进程名分域。
describe("AppProcessesTab process restart", () => {
  it("confirms and posts to the process restart endpoint", async () => {
    stubFetch();
    const { client, wrapper } = withClient();
    render(<AppProcessesTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/processes" />, { wrapper });
    await waitFor(() => expect(screen.getByText("web")).toBeTruthy());

    // 触发钮与确认钮同名（触发 → 确认框 portal 渲染在末尾，取末个）。
    fireEvent.click(screen.getByRole("button", { name: "Restart" }));
    // 确认框披露抖动语义（滚动重排、零 lineage）。
    expect(await screen.findByText("Restart web?")).toBeTruthy();
    const confirm = screen.getAllByRole("button", { name: "Restart" }).at(-1)!;
    fireEvent.click(confirm);

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([p]) => p === "/v1/apps/A1/processes/web/restart"),
      ).toBe(true),
    );
    const [, init] = fetchMock.mock.calls.find(([p]) => p === "/v1/apps/A1/processes/web/restart") as [
      string,
      RequestInit,
    ];
    expect(JSON.parse(String(init.body))).toEqual({});
    client.clear();
  });
});
