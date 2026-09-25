// 团队列表页测试（2026-09-24「New team」对话框验收面）：Me 投影的团队
// 行渲染 + 建队载荷（POST /v1/teams slug+name）+ 成功关窗失效缓存 +
// 409 信封停留对话框。TeamPage.test 同款 mock 形态（vi.fn 按 URL/方法
// 分派；Me 打桩参与 TeamProjectProvider 渲染）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { TeamsPage } from "@/pages/TeamsPage";
import { TeamProjectProvider } from "@/lib/context";

const ME = {
  user: { id: "01U1", email: "founder@t.test", is_platform_admin: false },
  teams: [{ team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: "owner" }],
};

interface CreateCall {
  url: string;
  body: Record<string, unknown>;
}

function stubFetch(opts: { onCreateTeam?: (body: Record<string, unknown>) => { status: number; body: unknown } } = {}) {
  const calls: CreateCall[] = [];
  const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/teams") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      calls.push({ url, body });
      const result =
        opts.onCreateTeam?.(body) ?? { status: 200, body: { team: { id: "01T2", slug: body.slug, name: body.name } } };
      return Promise.resolve({
        ok: result.status === 200,
        status: result.status,
        statusText: "",
        json: () => Promise.resolve(result.body),
      });
    }
    if (url.endsWith("/projects")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ projects: [] }) });
    }
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(ME) });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
  return { fetchMock, calls };
}

function renderAt() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/teams"]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Routes>
            <Route path="/teams" element={<TeamsPage />} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("TeamsPage", () => {
  it("renders my teams from the Me projection with role badges", async () => {
    vi.stubGlobal("fetch", stubFetch().fetchMock);
    renderAt();
    expect(await screen.findByTestId("team-row")).toHaveAttribute("data-slug", "acme");
    expect(screen.getByTestId("team-role-badge")).toHaveTextContent("owner");
  });

  it("creates a team from the dialog (POST payload) and closes on success", async () => {
    const { fetchMock, calls } = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    renderAt();
    const user = userEvent.setup();

    await screen.findByTestId("team-row");
    await user.click(screen.getByTestId("team-create-open"));
    await user.type(screen.getByTestId("team-slug-input"), "second");
    await user.type(screen.getByTestId("team-name-input"), "Second Team");
    await user.click(screen.getByTestId("team-create-submit"));

    await waitFor(() => {
      expect(calls).toHaveLength(1);
      expect(calls[0].url).toContain("/v1/teams");
      expect(calls[0].body).toEqual({ slug: "second", name: "Second Team" });
    });
    // 成功终态：对话框关闭。
    await waitFor(() => {
      expect(screen.queryByTestId("team-create-dialog")).not.toBeInTheDocument();
    });
  });

  it("keeps the dialog open and surfaces the envelope on 409 slug collision", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch({
        onCreateTeam: () => ({
          status: 409,
          body: {
            code: "E_TEAM_SLUG_TAKEN",
            message: "team slug already exists",
            suggestion: "pick a different slug",
          },
        }),
      }).fetchMock,
    );
    renderAt();
    const user = userEvent.setup();

    await screen.findByTestId("team-row");
    await user.click(screen.getByTestId("team-create-open"));
    await user.type(screen.getByTestId("team-slug-input"), "acme");
    await user.type(screen.getByTestId("team-name-input"), "Dup");
    await user.click(screen.getByTestId("team-create-submit"));

    expect(await screen.findByRole("alert")).toHaveTextContent("team slug already exists");
    expect(screen.getByTestId("team-create-dialog")).toBeInTheDocument();
  });
});
