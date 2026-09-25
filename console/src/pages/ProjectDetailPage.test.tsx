// 项目详情页测试（2026-09-25「项目怎么查看或编辑详情」验收面）：信息渲染
// （slug/name/description/限定形）、owner 编辑态（PATCH 载荷 name+description，
// slug 不出现在载荷——不可变）、非 owner 只读（无编辑钮）、项目内资源清单
// （apps/dbs 按 ?project= 收窄）。ProjectsPage.test 同款 mock 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { ProjectDetailPage } from "@/pages/ProjectDetailPage";
import { TeamProjectProvider } from "@/lib/context";

const PROJECT = {
  project: {
    id: "01PRJ1",
    team_id: "01T1",
    team_slug: "founder",
    slug: "staging",
    name: "Staging",
    description: "pre-prod fleet",
    created_at: "2026-09-24T13:58:59Z",
  },
};

function stubFetch(opts: { role?: string; project?: typeof PROJECT } = {}) {
  const calls: { url: string; method: string; body: Record<string, unknown> }[] = [];
  const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.includes("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "f@t.test", is_platform_admin: false },
            teams: [{ team_id: "01T1", team_slug: "founder", team_name: "Founder", role: opts.role ?? "owner" }],
          }),
      });
    }
    if (url.includes("/apps")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ apps: [{ id: "A1", name: "demo", derived_state: "running" }] }),
      });
    }
    if (url.includes("/databases")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ databases: [{ id: "D1", name: "pgshared", template: "postgres", status: "ready" }] }),
      });
    }
    if (url.endsWith("/v1/projects/01PRJ1") && method === "PATCH") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      calls.push({ url, method, body });
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ project: { ...PROJECT.project, ...body } }) });
    }
    if (url.includes("/projects/01PRJ1")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(opts.project ?? PROJECT) });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
  return { fetchMock, calls };
}

function renderAt() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/projects/01PRJ1"]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Routes>
            <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
            <Route path="/teams/:teamId" element={<p>team-settings</p>} />
            <Route path="/apps/:name" element={<p>app-detail</p>} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("ProjectDetailPage", () => {
  it("renders project info with qualified name and in-project resources", async () => {
    vi.stubGlobal("fetch", stubFetch().fetchMock);
    renderAt();

    expect(await screen.findByTestId("project-detail-name")).toHaveTextContent("Staging");
    expect(screen.getByText("founder/staging")).toBeInTheDocument();
    expect(screen.getByTestId("project-detail-description")).toHaveTextContent("pre-prod fleet");
    // 项目内资源清单（收窄断言：URL 带 project=founder/staging）。
    await waitFor(() => expect(screen.getByTestId("project-app-row")).toBeInTheDocument());
    expect(screen.getByTestId("project-db-row")).toBeInTheDocument();
    const appsCall = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.find((c) =>
      String(c[0]).includes("/v1/apps?"),
    );
    expect(String(appsCall?.[0])).toContain("project=founder%2Fstaging");
  });

  it("edits name and description as owner (PATCH without slug)", async () => {
    const { fetchMock, calls } = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    renderAt();
    const user = userEvent.setup();

    await screen.findByTestId("project-edit-open");
    await user.click(screen.getByTestId("project-edit-open"));
    await waitFor(() => expect(screen.getByTestId("project-name-input")).toHaveValue("Staging"));
    await user.clear(screen.getByTestId("project-name-input"));
    await user.type(screen.getByTestId("project-name-input"), "Staging v2");
    await user.click(screen.getByTestId("project-save"));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].method).toBe("PATCH");
    expect(calls[0].body).toEqual({ name: "Staging v2", description: "pre-prod fleet" });
    expect(calls[0].body).not.toHaveProperty("slug");
  });

  it("hides the edit affordance for non-owner members", async () => {
    vi.stubGlobal("fetch", stubFetch({ role: "developer" }).fetchMock);
    renderAt();

    expect(await screen.findByTestId("project-detail-name")).toBeInTheDocument();
    expect(screen.queryByTestId("project-edit-open")).not.toBeInTheDocument();
  });
});
