// 项目一级页测试（2026-09-24 用户裁决验收面）：跨团队聚合渲染（限定形
// team/prj）+ 生效角色（覆写行优先——升权示例：团队 developer 覆写为
// admin）+ 创建卡片门（仅 owner 团队；目标团队下拉只列我任 owner 的队）
// + 创建载荷（POST /v1/projects 带 team_id）。TeamPage.test 同款 mock
// 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { ProjectsPage } from "@/pages/ProjectsPage";
import { TeamProjectProvider } from "@/lib/context";

// 两队两项目：acme 我任 owner；beta 我任 developer，beta/site 有我的
// admin 覆写行（升权——生效角色应展示 admin 而非 developer）。
const ME = {
  user: { id: "01U1", email: "founder@t.test", is_platform_admin: false },
  teams: [
    { team_id: "01T1", team_slug: "acme", team_name: "Acme", role: "owner" },
    { team_id: "01T2", team_slug: "beta", team_name: "Beta", role: "developer" },
  ],
  project_overrides: [{ project_id: "01PRJ2", team_id: "01T2", prj_slug: "site", role: "admin" }],
};

const PROJECTS = {
  projects: [
    { id: "01PRJ1", team_id: "01T1", team_slug: "acme", slug: "web", name: "Web", description: "Frontend" },
    { id: "01PRJ2", team_id: "01T2", team_slug: "beta", slug: "site", name: "Site" },
  ],
};

interface CreateCall {
  url: string;
  body: Record<string, unknown>;
}

function stubFetch(opts: { me?: typeof ME; projects?: typeof PROJECTS } = {}) {
  const calls: CreateCall[] = [];
  const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/projects") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      calls.push({ url, body });
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ project: { id: "01PRJ3", ...body } }),
      });
    }
    if (url.endsWith("/projects")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve(opts.projects ?? PROJECTS),
      });
    }
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(opts.me ?? ME) });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
  return { fetchMock, calls };
}

function renderAt() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/projects"]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Routes>
            <Route path="/projects" element={<ProjectsPage />} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("ProjectsPage", () => {
  it("lists projects across teams with qualified names and override-aware role badges", async () => {
    vi.stubGlobal("fetch", stubFetch().fetchMock);
    renderAt();

    const rows = await screen.findAllByTestId("project-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveAttribute("data-slug", "web");
    expect(rows[1]).toHaveAttribute("data-slug", "site");
    expect(rows[0]).toHaveTextContent("acme/web");
    expect(rows[1]).toHaveTextContent("beta/site");

    // 生效角色：acme/web = 团队角色 owner；beta/site = 覆写行 admin（非
    // 团队角色 developer——覆写优先的回归锚点）。
    const badges = screen.getAllByTestId("project-role-badge");
    expect(badges[0]).toHaveTextContent("owner");
    expect(badges[1]).toHaveTextContent("admin");
  });

  it("scopes the create card to owned teams and posts the selected team_id", async () => {
    const { fetchMock, calls } = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    renderAt();
    const user = userEvent.setup();

    expect(await screen.findByTestId("project-create")).toBeInTheDocument();
    // 目标团队下拉只列 owner 团队（acme），不含 developer 的 beta。
    const select = screen.getByTestId("project-team-select") as HTMLSelectElement;
    const options = within(select).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("acme");

    await user.type(screen.getByTestId("project-slug-input"), "prod");
    await user.type(screen.getByTestId("project-name-input"), "Production");
    await user.click(screen.getByTestId("project-create-submit"));

    await waitFor(() => {
      expect(calls).toHaveLength(1);
      expect(calls[0].url).toContain("/v1/projects");
      expect(calls[0].body).toEqual({
        team_id: "01T1",
        slug: "prod",
        name: "Production",
        description: "",
      });
    });
  });

  it("hides the create card when the user owns no team", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch({
        me: {
          ...ME,
          teams: [{ team_id: "01T2", team_slug: "beta", team_name: "Beta", role: "developer" }],
          project_overrides: [],
        },
      }).fetchMock,
    );
    renderAt();

    expect(await screen.findAllByTestId("project-row")).toHaveLength(2);
    expect(screen.queryByTestId("project-create")).not.toBeInTheDocument();
  });
});
