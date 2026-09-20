// 应用列表测试：状态徽章渲染（degraded/blocked 一等展示 + 颜色语义）、
// 行点击进详情路由形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppsPage } from "@/pages/AppsPage";
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
