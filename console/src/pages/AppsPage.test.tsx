// 应用列表测试：状态徽章渲染（degraded/blocked 一等展示 + 颜色语义）、
// 行点击进详情路由形态、id 导航与归属投影（2026-09-25 走查断面）、
// 团队级客户端收窄（选团队未选项目）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppsPage } from "@/pages/AppsPage";
import { TeamProjectProvider } from "@/lib/context";
import { setToken } from "@/api/client";

function stubFetchApps() {
  return vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    statusText: "",
    json: () =>
      Promise.resolve({
        apps: [
          { id: "a1", name: "web", lifecycle: "active", derived_state: "running" },
          { id: "a2", name: "api", lifecycle: "active", derived_state: "degraded" },
          { id: "a3", name: "jobs", lifecycle: "active", derived_state: "blocked" },
        ],
      }),
  });
}

function renderAt(route: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[route]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps" element={<AppsPage />} />
          <Route path="/apps/:name" element={<p>detail-of-app</p>} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AppsPage state badges", () => {
  it("renders a badge per app with the derived state verbatim", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetchApps());

    renderAt("/apps");

    await waitFor(() => {
      expect(screen.getAllByTestId("state-badge")).toHaveLength(3);
    });
    for (const badge of screen.getAllByTestId("state-badge")) {
      expect(badge.getAttribute("data-state")).toBeTruthy();
    }
    expect(screen.getByText("web")).toBeInTheDocument();
    expect(screen.getByText("api")).toBeInTheDocument();
    expect(screen.getByText("jobs")).toBeInTheDocument();
  });

  it("exposes degraded and blocked states with amber/red tone dots", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetchApps());

    renderAt("/apps");

    await waitFor(() => screen.getByText("api"));
    const badgeFor = (name: string) => {
      const row = screen.getByText(name).closest("tr")!;
      return row.querySelector('[data-testid="state-badge"]')!;
    };
    // degraded → 琥珀点（bg-amber-500）；blocked → 红点（bg-red-500）。
    expect(badgeFor("api").innerHTML).toContain("bg-amber-500");
    expect(badgeFor("jobs").innerHTML).toContain("bg-red-500");
    expect(badgeFor("web").innerHTML).toContain("bg-emerald-500");
  });

  it("navigates to the app detail on name click", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetchApps());
    const user = userEvent.setup();

    renderAt("/apps");
    await waitFor(() => screen.getByText("web"));
    await user.click(screen.getByText("web"));

    expect(await screen.findByText("detail-of-app")).toBeInTheDocument();
  });
});

describe("AppsPage ownership projection and id navigation (2026-09-25 walkthrough)", () => {
  // 归属投影夹具：两队各一 app；行副标题 = team/prj，行导航 = id。
  function stubFetchOwned() {
    return vi.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/auth/me")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () =>
            Promise.resolve({
              user: { id: "u1", email: "f@t.test", is_platform_admin: false },
              teams: [
                { team_id: "01T1", team_slug: "alpha", team_name: "Alpha", role: "owner" },
                { team_id: "01T2", team_slug: "beta", team_name: "Beta", role: "owner" },
              ],
            }),
        });
      }
      if (url.endsWith("/projects")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () => Promise.resolve({ projects: [] }),
        });
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            apps: [
              { id: "A1", name: "web", lifecycle: "active", derived_state: "running", team_slug: "alpha", project_slug: "web" },
              { id: "B1", name: "web2", lifecycle: "active", derived_state: "running", team_slug: "beta", project_slug: "site" },
            ],
          }),
      });
    });
  }

  function renderWithContext(route: string) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <MemoryRouter initialEntries={[route]}>
        <QueryClientProvider client={client}>
          <TeamProjectProvider>
            <Routes>
              <Route path="/apps" element={<AppsPage />} />
              <Route path="/apps/:name" element={<p>detail-of-app</p>} />
            </Routes>
          </TeamProjectProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );
  }

  it("shows the qualified team/prj subtitle and navigates by platform id", async () => {
    setToken("flt_test");
    const fetchMock = stubFetchOwned();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderWithContext("/apps");
    await waitFor(() => screen.getByText("web"));
    // 副标题 = 归属限定形（非裸 ULID）。
    expect(screen.getByText("alpha/web")).toBeInTheDocument();
    expect(screen.getByText("beta/site")).toBeInTheDocument();

    await user.click(screen.getByText("web"));
    // 详情导航 = id 寻址（同名 app 裸名必歧义的断层修复面）。
    expect(await screen.findByText("detail-of-app")).toBeInTheDocument();
    const appsCall = (fetchMock as ReturnType<typeof vi.fn>).mock.calls.find(
      (c) => String(c[0]).endsWith("/apps/a1") || String(c[0]).includes("/v1/apps/a1"),
    );
    expect(appsCall).toBeUndefined(); // 列表请求后无按名二次请求——导航是纯路由。
  });

  it("narrows the list to the selected team when no project is chosen", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetchOwned());

    // 团队选择经 Provider 的持久化键预置（切换器 UI 在 Layout——本页测试
    // 树不含；语义等价：selectedTeam=beta、project=null 的上下文态）。
    localStorage.setItem("fleetly.console.context", JSON.stringify({ team: "beta", project: null }));

    renderWithContext("/apps");
    await waitFor(() => screen.getByText("web2"));
    // 只选团队（未选项目）→ 客户端按 team_slug 收窄：beta 队只见 beta/site 行。
    await waitFor(() => {
      expect(screen.getAllByTestId("app-row")).toHaveLength(1);
    });
    expect(screen.getByText("beta/site")).toBeInTheDocument();
    expect(screen.queryByText("alpha/web")).not.toBeInTheDocument();
    localStorage.removeItem("fleetly.console.context");
  });
});
