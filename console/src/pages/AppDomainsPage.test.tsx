// 域名页测试（2026-09-25 走查：Verify 按钮的角色可达性以 scope 事实为准
// ——VerifyAppDomains 服务端登记 read scope（scope.go DomainsService）+
// handler 读层角色门（internal/api/read.go requireAppAccess），探测不写
// 平台状态 → 全角色可用是服务端事实，按钮保留 + 卡内性质说明行）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppDomainsPage } from "@/pages/AppDomainsPage";
import { TeamProjectProvider } from "@/lib/context";
import { setToken } from "@/api/client";

const DOMAINS = {
  domains: [
    {
      service: "web",
      domain: "demo.example.test",
      port: 443,
      cert_sha256: "a".repeat(64),
      cert_not_after: "2027-01-01T00:00:00Z",
    },
  ],
};

function stubDomainsFetch(opts: { role: string } = { role: "viewer" }) {
  const calls: Array<{ url: string; method: string }> = [];
  const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ url, method });
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "f@t.test", is_platform_admin: false },
            teams: [
              { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: opts.role },
            ],
            project_overrides: [],
          }),
      });
    }
    if (url.endsWith("/domains/verify") && method === "POST") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () =>
          Promise.resolve({
            checks: [
              { domain: "demo.example.test", resolved: true, ips: ["203.0.113.9"] },
            ],
          }),
      });
    }
    if (url.endsWith("/domains")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(DOMAINS) });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
  return { fetchMock, calls };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/apps/demo/domains"]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Routes>
            <Route path="/apps/:name/domains" element={<AppDomainsPage />} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AppDomainsPage verify face", () => {
  it("viewer：Verify 保留可用（read scope 服务端事实）+ 动作性质说明行", async () => {
    setToken("flt_test");
    const { fetchMock, calls } = stubDomainsFetch({ role: "viewer" });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await waitFor(() => expect(screen.getByText("demo.example.test")).toBeInTheDocument());
    const note = screen.getByTestId("domains-verify-note");
    expect(note).toHaveTextContent(/read-only local probe/);
    expect(note).toHaveTextContent(/available to every project role/);

    // viewer 点 Verify：POST 正常发出（服务端读层角色门放行），结果表渲染。
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() => {
      expect(calls.some((c) => c.method === "POST" && c.url.endsWith("/domains/verify"))).toBe(
        true,
      );
    });
    expect(await screen.findByText("Verification result (local probe — judgement is yours)")).toBeInTheDocument();
    expect(screen.getByText("203.0.113.9")).toBeInTheDocument();
  });

  it("admin：同一读面与说明（按钮不因角色收紧——scope 登记为 read）", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubDomainsFetch({ role: "admin" }).fetchMock);

    renderPage();

    await waitFor(() => expect(screen.getByText("demo.example.test")).toBeInTheDocument());
    expect(screen.getByTestId("domains-verify-note")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Verify" })).toBeEnabled();
  });
});
