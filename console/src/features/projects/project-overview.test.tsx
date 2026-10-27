import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { ProjectOverviewPage } from "@/features/projects/project-overview";
import { setToken } from "@/lib/token";

// W-1 URL 侧回退钉测：陈旧项目 id（记忆/侧栏链接源头）确认查无 → redirect
// 首个项目（replace，死地址不留历史）；目录全空才诚实 Not found；命中正常
// 渲染不导航。redirect 只在目录加载完成后判定（pending 窗不弹跳——记录·
// 四十九"整页弹回"教训）。页体住 features（router 插件改写路由文件，薄壳
// 不可单测）——mock useNavigate + useSearch（useCollection 的 URL 解码锚，
// 无路由上下文的测试环境给空 search 即 schema 缺省窗）。

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock, useSearch: () => ({}) };
});

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

function renderPage(client?: QueryClient, projectId = "gone") {
  const queryClient = client ?? new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <ProjectOverviewPage from="test" projectId={projectId} />
    </QueryClientProvider>,
  );
  return queryClient;
}

describe("project overview stale-id fallback", () => {
  it("redirects to the first project when the id is not in the catalog", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/projects")) {
        return new Response(JSON.stringify({ projects: [{ id: "live", name: "Live" }] }), { status: 200 });
      }
      return new Response("{}", { status: 200 });
    });
    renderPage();

    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/p/$projectId",
        params: { projectId: "live" },
        replace: true,
      }),
    );
    // 在途帧是 spinner 兜底，不是死页文案。
    expect(screen.queryByText(/project not found/i)).toBeNull();
  });

  it("renders the honest error when the catalog has no projects at all", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/projects")) {
        return new Response(JSON.stringify({ projects: [] }), { status: 200 });
      }
      return new Response("{}", { status: 200 });
    });
    renderPage();

    expect(await screen.findByText(/no projects yet/i)).toBeTruthy();
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it("renders the page normally on a catalog hit without navigating", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/projects")) {
        return new Response(JSON.stringify({ projects: [{ id: "gone", name: "Gone" }] }), { status: 200 });
      }
      if (path.startsWith("/v1/apps")) {
        return new Response(JSON.stringify({ apps: [] }), { status: 200 });
      }
      return new Response("{}", { status: 200 });
    });
    renderPage();

    expect(await screen.findByText("Gone")).toBeTruthy();
    expect(navigateMock).not.toHaveBeenCalled();
  });
});

// H13 跨页缓存形状回归：App 详情页的 ["deployments", appId] 是 Deployment[]
// 数组；总览磁贴必须以独立键取单条 Deployment。同键双形状会让详情页
// rows.slice 在缓存命中时抛 TypeError（整页错误边界接管）。
describe("project overview latest-deployment cache isolation", () => {
  it("keeps the app list cache array-shaped while the tile fetches its own latest deployment", async () => {
    const appId = "01JAPP0000000000000000001";
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const listKey = ["deployments", appId];
    const listRows = [{ id: "01JDEPOLD00000000000000001", state: "succeeded", updated_at: "2026-10-18T10:00:00Z" }];
    client.setQueryData(listKey, listRows);

    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/projects")) {
        return new Response(JSON.stringify({ projects: [{ id: "proj", name: "Proj" }] }), { status: 200 });
      }
      if (path.startsWith("/v1/apps")) {
        return new Response(
          JSON.stringify({ apps: [{ id: appId, project_id: "proj", name: "Web", created_at: "2026-10-01T00:00:00Z" }] }),
          { status: 200 },
        );
      }
      if (path.startsWith("/v1/deployments")) {
        return new Response(
          JSON.stringify({
            deployments: [{ id: "01JDEPNEW00000000000000002", state: "failed", updated_at: "2026-10-19T10:00:00Z" }],
          }),
          { status: 200 },
        );
      }
      return new Response("{}", { status: 200 });
    });

    renderPage(client, "proj");

    expect(await screen.findByText("Web")).toBeTruthy();
    // 磁贴从独立键取到单条对象并正常渲染（同键回归会崩在 rows.slice 语义）。
    expect(await screen.findByText("failed")).toBeTruthy();
    expect(client.getQueryData(["deployments", "latest", appId])).toMatchObject({
      id: "01JDEPNEW00000000000000002",
    });
    // 列表缓存保持数组形状（未被磁贴查询覆写）。
    expect(client.getQueryData(listKey)).toEqual(listRows);
  });
});
