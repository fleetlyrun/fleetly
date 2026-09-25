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

// 单团队用户（P1-3 对照组：个人队 owner——未选团队时既有回落语义成立）。
const ME_SINGLE = {
  user: { id: "01U2", email: "solo@t.test", is_platform_admin: false },
  teams: [{ team_id: "01TEAM1", team_slug: "acme", team_name: "Acme", role: "owner" }],
};

const PROJECTS = {
  projects: [
    { id: "01PRJ1", team_id: "01TEAM1", team_slug: "acme", slug: "default", name: "Default" },
    { id: "01PRJ2", team_id: "01TEAM1", team_slug: "acme", slug: "web", name: "Web" },
    { id: "01PRJ3", team_id: "01TEAM2", team_slug: "beta", slug: "default", name: "Default" },
  ],
};

function stubIdentityFetch(me: unknown = ME) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, _init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/auth/me")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(me) });
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

describe("TeamProjectSwitcher team-context hint (review P1-3)", () => {
  it("shows the unlock hint for a multi-team user with no team selected", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubIdentityFetch());
    const user = userEvent.setup();

    renderWith(<TeamProjectSwitcher />);

    // 多团队 + 未选团队：能力解析不出角色、写钮静默消失——显式提示补因果。
    expect(await screen.findByTestId("team-context-hint")).toHaveTextContent(
      "Select a team to unlock deploy and write actions.",
    );

    // 选中团队后提示随之消失（能力已解析）。
    await user.selectOptions(screen.getByTestId("team-switcher"), "acme");
    expect(screen.queryByTestId("team-context-hint")).not.toBeInTheDocument();
  });

  it("renders no hint for a single-team user (existing fallback semantics)", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubIdentityFetch(ME_SINGLE));

    renderWith(<TeamProjectSwitcher />);

    // 单团队回落：唯一团队即上下文，无需引导。
    await screen.findByTestId("team-switcher");
    expect(screen.queryByTestId("team-context-hint")).not.toBeInTheDocument();
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
