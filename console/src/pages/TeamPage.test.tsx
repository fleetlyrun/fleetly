// 团队设置页测试（v0.3 W2-S5，设计 §7/§3.1/§3.3 验收面）：成员列表与角色
// 徽章、owner 角色变更（POST members:set-role 载荷）、邀请创建（一次性链
// 接直出复制框 + POST invites 载荷角色收敛）、项目列表 + 覆写成员对话框
// （set-role 载荷 + 移除）。非 owner 渲染（admin）不见 owner 专属写按钮
// ——角色矩阵的前端投影。PatPage.test 同款 mock 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TeamPage } from "@/pages/TeamPage";
import { TeamProjectProvider } from "@/lib/context";
import { setToken } from "@/api/client";

const MEMBERS = {
  members: [
    {
      team_id: "01TEAM",
      user_id: "01U1",
      role: "owner",
      email: "founder@t.test",
      display_name: "Founder",
      created_at: "2026-09-20T10:00:00Z",
    },
    {
      team_id: "01TEAM",
      user_id: "01U2",
      role: "developer",
      email: "dev@t.test",
      display_name: "Dev",
      created_at: "2026-09-21T10:00:00Z",
    },
  ],
};

const INVITES = {
  invites: [
    {
      id: "01INV1",
      team_id: "01TEAM",
      email: "new@t.test",
      role: "viewer",
      expires_at: "2026-09-28T10:00:00Z",
      created_at: "2026-09-21T10:00:00Z",
    },
  ],
};

const PROJECTS = {
  projects: [
    { id: "01PRJ1", team_id: "01TEAM", team_slug: "acme", slug: "web", name: "Web" },
    { id: "01PRJ2", team_id: "01TEAM", team_slug: "acme", slug: "api", name: "API" },
  ],
};

const OVERRIDES = {
  members: [
    {
      project_id: "01PRJ1",
      user_id: "01U2",
      role: "viewer",
      email: "dev@t.test",
      display_name: "Dev",
    },
  ],
};

function stubFetch(overrides: {
  onCreateInvite?: (body: Record<string, unknown>) => { status: number; body: unknown };
  onSetRole?: (body: Record<string, unknown>) => { status: number; body: unknown };
} = {}) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/members:set-role") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      const result =
        overrides.onSetRole?.(body) ?? {
          status: 200,
          body: { member: { ...MEMBERS.members[1], role: body.role } },
        };
      return Promise.resolve({
        ok: result.status === 200,
        status: result.status,
        statusText: "",
        json: () => Promise.resolve(result.body),
      });
    }
    if (url.endsWith("/invites") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      const result =
        overrides.onCreateInvite?.(body) ?? {
          status: 200,
          body: {
            invite: { id: "01INV2", team_id: "01TEAM", email: body.email, role: body.role },
            token: "one-time-token-abc",
          },
        };
      return Promise.resolve({
        ok: result.status === 200,
        status: result.status,
        statusText: "",
        json: () => Promise.resolve(result.body),
      });
    }
    if (url.includes("/members/") && method === "DELETE") {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
    }
    if (url.endsWith("/invites") && method === "GET") {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(INVITES) });
    }
    // 具体路径先判：项目覆写成员读面优先于项目列表（/projects/<id>/members
    // 同样 includes("/projects")）。
    if (url.endsWith("/01PRJ1/members")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(OVERRIDES) });
    }
    if (url.includes("/projects")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(PROJECTS) });
    }
    if (url.endsWith("/members")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(MEMBERS) });
    }
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "founder@t.test", is_platform_admin: false },
            teams: [{ team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: "owner" }],
          }),
      });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
}

function renderAt(initialPath = "/teams/01TEAM") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <QueryClientProvider client={client}>
        {/* 角色能力自 Me 投影派生（context.tsx）——Provider 参与渲染。 */}
        <TeamProjectProvider>
          <Routes>
            <Route path="/teams/:teamId" element={<TeamPage />} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TeamPage members", () => {
  it("lists members with role badges (owner sees role controls)", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("team-page");

    const rows = await screen.findAllByTestId("member-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("founder@t.test")).toBeInTheDocument();

    // owner 角色徽章 + 角色变更面（owner 专属）可见。
    expect(screen.getAllByTestId("member-role-badge").map((n) => n.textContent)).toEqual(
      expect.arrayContaining(["owner", "developer"]),
    );
    const devRow = rows.find((r) => r.getAttribute("data-email") === "dev@t.test");
    expect(devRow).toBeDefined();
    await user.selectOptions(within(devRow!).getByTestId("member-role-select"), "viewer");
    await user.click(within(devRow!).getByTestId("member-role-save"));

    // POST 载荷：user_id + 新角色。
    await waitFor(() => {
      const posted = fetchMockCalls().find(
        (c) => String(c[0]).endsWith("/members:set-role") && c[1]?.method === "POST",
      );
      expect(posted).toBeDefined();
      const body = JSON.parse(String(posted?.[1]?.body)) as Record<string, unknown>;
      expect(body.user_id).toBe("01U2");
      expect(body.role).toBe("viewer");
    });
  });
});

describe("TeamPage invites", () => {
  it("creates an invite and shows the one-time link with the posted role", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("team-page");
    // 等待 Me 投影落定（角色能力派生源）后 Invites tab 才渲染。
    await screen.findByRole("tab", { name: "Invites" });
    await user.click(screen.getByRole("tab", { name: "Invites" }));

    await user.type(screen.getByTestId("invite-email-input"), "new2@t.test");
    await user.selectOptions(screen.getByTestId("invite-role-select"), "viewer");
    await user.click(screen.getByTestId("invite-create-submit"));

    // 链接直出复制框：一次性 token 拼进邀请链接（/ui/auth/invite?token=…）。
    const link = await screen.findByTestId("invite-link");
    expect(link.textContent).toContain("/auth/invite?token=one-time-token-abc");
    expect(screen.getByTestId("invite-link-copy")).toBeInTheDocument();

    const posted = fetchMock
      .mock.calls.find(
        (c) => String(c[0]).endsWith("/invites") && c[1]?.method === "POST",
      );
    const body = JSON.parse(String(posted?.[1]?.body)) as Record<string, unknown>;
    expect(body.email).toBe("new2@t.test");
    expect(body.role).toBe("viewer");

    // 邀请列表渲染（pending 行 + 吊销钮）。
    expect(screen.getByTestId("invite-row")).toBeInTheDocument();
  });
});

describe("TeamPage projects + overrides", () => {
  it("lists projects in qualified form and manages role overrides", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("team-page");
    await user.click(screen.getByRole("tab", { name: "Projects" }));

    // 限定形展示（D-W0-9）。
    const rows = await screen.findAllByTestId("project-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("acme/web")).toBeInTheDocument();

    // 打开覆写成员对话框：现有覆写行（viewer 降权）+ 设置新覆写。
    // 等待 Me 投影落定（canInvite 能力门）后 Members 按钮才渲染。
    const openButton = await within(rows[0]!).findByTestId("project-members-open");
    await user.click(openButton);
    await screen.findByTestId("project-members-dialog");
    expect(await screen.findByTestId("override-row")).toHaveAttribute("data-email", "dev@t.test");

    await user.selectOptions(screen.getByTestId("override-user-select"), "01U2");
    await user.selectOptions(screen.getByTestId("override-role-select"), "developer");
    await user.click(screen.getByTestId("override-set-submit"));

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) => String(c[0]).endsWith("/members:set-role") && c[1]?.method === "POST",
      );
      expect(posted).toBeDefined();
      const body = JSON.parse(String(posted?.[1]?.body)) as Record<string, unknown>;
      expect(body.user_id).toBe("01U2");
      expect(body.role).toBe("developer");
    });

    // 摘除覆写（DELETE /projects/01PRJ1/members/01U2）。
    await user.click(screen.getByTestId("override-remove"));
    await waitFor(() => {
      const deleted = fetchMock.mock.calls.find(
        (c) => String(c[0]).endsWith("/members/01U2") && c[1]?.method === "DELETE",
      );
      expect(deleted).toBeDefined();
    });
  });
});

// ── 页签进 URL（2026-09-25 审查 P2-2：SystemPage 同款 ?tab= 深链）────────

describe("TeamPage tabs in URL", () => {
  it("deep-links to the projects tab via ?tab=projects", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());
    renderAt("/teams/01TEAM?tab=projects");

    await screen.findByTestId("team-page");
    expect(await screen.findByTestId("projects-tab")).toBeInTheDocument();
    expect(screen.queryByTestId("members-card")).not.toBeInTheDocument();
  });

  it("deep-links to the invites tab when the capability gate allows it", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());
    renderAt("/teams/01TEAM?tab=invites");

    await screen.findByTestId("team-page");
    expect(await screen.findByTestId("invites-tab")).toBeInTheDocument();
    expect(screen.queryByTestId("members-card")).not.toBeInTheDocument();
  });

  it("falls back to the members tab on an unknown tab value", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());
    renderAt("/teams/01TEAM?tab=bogus");

    await screen.findByTestId("team-page");
    expect(await screen.findByTestId("members-card")).toBeInTheDocument();
    expect(screen.queryByTestId("projects-tab")).not.toBeInTheDocument();
  });

  it("links each project row to its detail page (id-addressed)", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());
    renderAt("/teams/01TEAM?tab=projects");

    const links = await screen.findAllByTestId("project-row-link");
    expect(links.map((l) => l.getAttribute("href"))).toEqual([
      "/projects/01PRJ1",
      "/projects/01PRJ2",
    ]);
  });
});

function fetchMockCalls() {
  return (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls;
}
