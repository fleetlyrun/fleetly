// 侧边栏分组测试（2026-09-25 审查 §6-4/P2-6）：四组 IA——Home 与工作区组
// （Teams/Projects）无标签，WORKLOADS/PLATFORM/ADMINISTRATION 三组带标签；
// Admin 图标与 System 去重（users 族）。user-menu.test 同款 Layout 装配
// （AuthProvider + QueryClientProvider；Me 投影驱动 isPlatformAdmin）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Layout } from "@/components/layout";
import { AuthProvider } from "@/auth";
import { setToken } from "@/api/client";

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: "",
    json: () => Promise.resolve(body),
  };
}

function stubFetch(isPlatformAdmin: boolean) {
  return vi.fn((url: unknown) => {
    const u = String(url);
    if (u.includes("/auth/me")) {
      return Promise.resolve(
        jsonResponse(200, {
          user: {
            id: "u1",
            email: "op@example.com",
            display_name: "Operator",
            is_platform_admin: isPlatformAdmin,
          },
          teams: [
            { team_id: "t1", team_slug: "acme", team_name: "Acme", role: "owner" },
          ],
        }),
      );
    }
    if (u.includes("/system/status")) {
      return Promise.resolve(jsonResponse(200, { version: "0.3.0" }));
    }
    return Promise.resolve(jsonResponse(404, { message: `unmocked ${u}` }));
  });
}

function renderLayout() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <AuthProvider>
        <QueryClientProvider client={client}>
          <Routes>
            <Route element={<Layout />}>
              <Route path="/" element={<p>home-body</p>} />
            </Route>
          </Routes>
        </QueryClientProvider>
      </AuthProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Layout sidebar grouping (2026-09-25 review P2-6)", () => {
  it("renders the labelled groups in order with Teams/Projects unlabeled after Home", async () => {
    setToken("");
    vi.stubGlobal("fetch", stubFetch(true));
    renderLayout();

    const nav = await screen.findByRole("navigation", { name: "Main" });
    // Me 投影落地（Admin 项出现 = isPlatformAdmin 生效）后再断言结构与顺序。
    await waitFor(() =>
      expect(within(nav).getByRole("link", { name: "Admin" })).toBeInTheDocument(),
    );

    // 三组带标签（复用原 Platform 标签样式）；Home 与 Teams/Projects 无标签。
    expect(screen.getByTestId("nav-group-workloads").textContent).toBe("Workloads");
    expect(screen.getByTestId("nav-group-platform").textContent).toBe("Platform");
    expect(screen.getByTestId("nav-group-administration").textContent).toBe(
      "Administration",
    );

    // 顺序：Home → Teams → Projects → Applications → Databases → Events →
    // System → Admin → Audit。
    const labels = within(nav)
      .getAllByRole("link")
      .map((l) => l.textContent);
    expect(labels).toEqual([
      "Home",
      "Teams",
      "Projects",
      "Applications",
      "Databases",
      "Events",
      "System",
      "Admin",
      "Audit",
    ]);

    // 图标可访问名去重：Admin 换 users 族图标，不再与 System 共用 server。
    const adminIcon = within(nav).getByRole("link", { name: "Admin" }).querySelector("svg");
    const systemIcon = within(nav).getByRole("link", { name: "System" }).querySelector("svg");
    expect(adminIcon?.classList.contains("lucide-users")).toBe(true);
    expect(systemIcon?.classList.contains("lucide-server")).toBe(true);
  });

  it("hides the Administration group for non-platform admins", async () => {
    setToken("");
    vi.stubGlobal("fetch", stubFetch(false));
    renderLayout();

    const nav = await screen.findByRole("navigation", { name: "Main" });
    // Me 投影落地后 Administration 组整体缺席（组标签与组内项都不渲染）。
    await waitFor(() =>
      expect(
        within(nav)
          .getAllByRole("link")
          .map((l) => l.textContent),
      ).toEqual([
        "Home",
        "Teams",
        "Projects",
        "Applications",
        "Databases",
        "Events",
        "System",
      ]),
    );
    expect(screen.queryByTestId("nav-group-administration")).not.toBeInTheDocument();
  });
});
