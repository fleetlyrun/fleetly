// 团队/项目上下文与切换器测试（v0.3 W2-S5）：切换器选项（限定形 team/prj
// ——D-W0-9）、选择持久化 localStorage（fleetly.console.context 键，仓内
// fleetly.console.* 前缀惯例）、团队切换失效项目选择、AppsPage 携带
// ?project=team/prj 收窄请求（服务端过滤语义的资源页接引）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TeamProjectProvider, useProjectContext } from "@/lib/context";
import { TeamProjectSwitcher } from "@/components/team-project-switcher";
import { setToken } from "@/api/client";

const ME = {
  user: { id: "01U1", email: "founder@t.test", is_platform_admin: false },
  teams: [
    { team_id: "01TEAM1", team_slug: "acme", team_name: "Acme", role: "owner" },
    { team_id: "01TEAM2", team_slug: "beta", team_name: "Beta", role: "developer" },
  ],
};

const PROJECTS = {
  projects: [
    { id: "01PRJ1", team_id: "01TEAM1", team_slug: "acme", slug: "default", name: "Default" },
    { id: "01PRJ2", team_id: "01TEAM1", team_slug: "acme", slug: "web", name: "Web" },
    { id: "01PRJ3", team_id: "01TEAM2", team_slug: "beta", slug: "default", name: "Default" },
  ],
};

function stubIdentityFetch() {
  return vi.fn().mockImplementation((input: RequestInfo | URL, _init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/auth/me")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(ME) });
    }
    if (url.includes("/projects")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(PROJECTS) });
    }
    if (url.includes("/apps")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ apps: [{ id: "01A", name: "web", derived_state: "running" }] }),
      });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
}

/** 上下文读出探针（断言 projectRef/qualifyApp 的派生语义）。 */
function ContextProbe() {
  const ctx = useProjectContext();
  return (
    <div
      data-testid="context-probe"
      data-team={ctx.selectedTeamSlug ?? ""}
      data-project={ctx.selectedProjectSlug ?? ""}
      data-ref={ctx.projectRef}
      data-qualified={ctx.qualifyApp("web")}
    />
  );
}

function renderWith(children: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>{children}</TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TeamProjectSwitcher", () => {
  it("renders team and qualified project options and persists the selection", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubIdentityFetch());
    const user = userEvent.setup();

    renderWith(
      <>
        <TeamProjectSwitcher />
        <ContextProbe />
      </>,
    );

    const teamSelect = await screen.findByTestId("team-switcher");
    await user.selectOptions(teamSelect, "acme");
    const projectSelect = screen.getByTestId("project-switcher");

    // 项目选项收敛到所选团队（beta/default 不在 acme 的下拉里）。
    const options = Array.from(projectSelect.querySelectorAll("option")).map((o) => o.textContent);
    expect(options).toContain("acme/default");
    expect(options).toContain("acme/web");
    expect(options).not.toContain("beta/default");

    await user.selectOptions(projectSelect, "web");

    // 限定形派生（probe）：team/prj 与 team/prj/app。
    await waitFor(() => {
      expect(screen.getByTestId("context-probe")).toHaveAttribute("data-ref", "acme/web");
    });
    expect(screen.getByTestId("context-probe")).toHaveAttribute("data-qualified", "acme/web/web");

    // localStorage 持久化（键名 fleetly.console.context）。
    expect(JSON.parse(localStorage.getItem("fleetly.console.context") ?? "{}")).toEqual({
      team: "acme",
      project: "web",
    });
  });

  it("resets the project selection when the team changes", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubIdentityFetch());
    const user = userEvent.setup();

    localStorage.setItem("fleetly.console.context", JSON.stringify({ team: "acme", project: "web" }));
    renderWith(
      <>
        <TeamProjectSwitcher />
        <ContextProbe />
      </>,
    );

    // 启动即恢复持久化选择。
    await waitFor(() => {
      expect(screen.getByTestId("context-probe")).toHaveAttribute("data-ref", "acme/web");
    });

    await user.selectOptions(screen.getByTestId("team-switcher"), "beta");
    await waitFor(() => {
      expect(screen.getByTestId("context-probe")).toHaveAttribute("data-project", "");
    });
    // 项目选择失效，团队选择保留。
    expect(JSON.parse(localStorage.getItem("fleetly.console.context") ?? "{}")).toEqual({
      team: "beta",
      project: null,
    });
  });
});

describe("resource pages project narrowing", () => {
  it("AppsPage requests carry ?project=team/prj when a project is selected", async () => {
    setToken("flt_test");
    const fetchMock = stubIdentityFetch();
    vi.stubGlobal("fetch", fetchMock);

    localStorage.setItem("fleetly.console.context", JSON.stringify({ team: "acme", project: "web" }));
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const { AppsPage } = await import("@/pages/AppsPage");
    render(
      <MemoryRouter initialEntries={["/apps"]}>
        <QueryClientProvider client={client}>
          <TeamProjectProvider>
            <AppsPage />
          </TeamProjectProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );

    // 首帧 projectRef 尚未就绪（projects 查询在途）→ 先发全量请求；上下文
    // 落定后重查带收窄参数。等待收窄调用出现再断言。
    await waitFor(() => {
      const narrowed = fetchMock.mock.calls.find((c) =>
        String(c[0]).includes("/apps?project=acme%2Fweb"),
      );
      expect(narrowed).toBeDefined();
    });
  });
});
