import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { ProjectOverviewPage } from "@/features/projects/project-overview";
import { setToken } from "@/lib/token";

// W-1 URL 侧回退钉测：陈旧项目 id（记忆/侧栏链接源头）确认查无 → redirect
// 首个项目（replace，死地址不留历史）；目录全空才诚实 Not found；命中正常
// 渲染不导航。redirect 只在目录加载完成后判定（pending 窗不弹跳——记录·
// 四十九"整页弹回"教训）。页体住 features（router 插件改写路由文件，薄壳
// 不可单测）——mock 只需 useNavigate。

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock };
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

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <ProjectOverviewPage projectId="gone" />
    </QueryClientProvider>,
  );
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
