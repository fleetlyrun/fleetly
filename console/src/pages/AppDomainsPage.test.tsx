// 域名资源页测试（IMPL-T1-1）：写面按 deploy 门收敛（viewer 不见写控件；
// admin/developer 可见）、新增/编辑/删除走 RPC 并刷新查询；Verify 保持
// 全角色可用（read scope 服务端事实，2026-09-25 走查口径不回退）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
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
      port: "8080",
      protocol: "http",
      cert_mode: "http01",
      cert_sha256: "a".repeat(64),
      cert_not_after: "2027-01-01T00:00:00Z",
    },
  ],
};

type Call = { url: string; method: string; body: unknown };

function stubDomainsFetch(opts: { role: string } = { role: "viewer" }) {
  const calls: Call[] = [];
  const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    let body: unknown = null;
    if (typeof init?.body === "string") {
      try {
        body = JSON.parse(init.body);
      } catch {
        body = init.body;
      }
    }
    calls.push({ url, method, body });
    const json = (payload: unknown) =>
      Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(payload) });
    if (url.endsWith("/auth/me")) {
      return json({
        user: { id: "01U1", email: "f@t.test", is_platform_admin: false },
        teams: [{ team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: opts.role }],
        project_overrides: [],
      });
    }
    if (url.endsWith("/domains/verify") && method === "POST") {
      return json({
        checks: [{ domain: "demo.example.test", resolved: true, ips: ["203.0.113.9"] }],
      });
    }
    if (method === "POST" && url.endsWith("/domains")) {
      return json({ domain: { ...DOMAINS.domains[0], ...(body as object) } });
    }
    if (method === "PUT" && url.includes("/domains/")) {
      return json({ domain: { ...DOMAINS.domains[0], ...(body as object) } });
    }
    if (method === "DELETE" && url.includes("/domains/")) {
      return json({ app: "demo", domain: "demo.example.test" });
    }
    if (url.endsWith("/domains")) {
      return json(DOMAINS);
    }
    return json({});
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

describe("AppDomainsPage write face", () => {
  it("viewer：无写控件（deploy 门），Verify 保留可用 + 动作性质说明行", async () => {
    setToken("flt_test");
    const { fetchMock, calls } = stubDomainsFetch({ role: "viewer" });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await waitFor(() => expect(screen.getByText("demo.example.test")).toBeInTheDocument());
    const note = screen.getByTestId("domains-verify-note");
    expect(note).toHaveTextContent(/read-only local probe/);
    expect(note).toHaveTextContent(/available to every project role/);
    expect(screen.queryByTestId("domain-add-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("domain-edit-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("domain-remove-button")).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() => {
      expect(calls.some((c) => c.method === "POST" && c.url.endsWith("/domains/verify"))).toBe(true);
    });
    expect(
      await screen.findByText("Verification result (local probe — judgement is yours)"),
    ).toBeInTheDocument();
    expect(screen.getByText("203.0.113.9")).toBeInTheDocument();
  });

  it("admin：新增域名（POST 载荷 = 归一化字段 + 默认 http/http01）", async () => {
    setToken("flt_test");
    const { fetchMock, calls } = stubDomainsFetch({ role: "admin" });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await waitFor(() => expect(screen.getByText("demo.example.test")).toBeInTheDocument());

    const user = userEvent.setup();
    await user.click(screen.getByTestId("domain-add-button"));
    const dialog = await screen.findByTestId("domain-add-dialog");
    await user.type(within(dialog).getByTestId("domain-host-input"), "grpc.example.test");
    await user.type(within(dialog).getByTestId("domain-service-input"), "api");
    await user.type(within(dialog).getByTestId("domain-port-input"), "9090");
    await user.click(within(dialog).getByTestId("domain-submit"));

    await waitFor(() => {
      expect(
        calls.some((c) => c.method === "POST" && c.url.endsWith("/domains")),
      ).toBe(true);
    });
    const created = calls.find((c) => c.method === "POST" && c.url.endsWith("/domains"));
    expect(created?.body).toEqual({
      domain: "grpc.example.test",
      service: "api",
      port: "9090",
      protocol: "http",
      cert_mode: "http01",
    });
  });

  it("developer：编辑行（host 只读；PUT 全量字段）与删除确认", async () => {
    setToken("flt_test");
    const { fetchMock, calls } = stubDomainsFetch({ role: "developer" });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await waitFor(() => expect(screen.getByText("demo.example.test")).toBeInTheDocument());

    const user = userEvent.setup();
    await user.click(screen.getByTestId("domain-edit-button"));
    const editDialog = await screen.findByTestId("domain-edit-dialog");
    expect(within(editDialog).getByTestId("domain-host-input")).toBeDisabled();
    const portInput = within(editDialog).getByTestId("domain-port-input");
    await user.clear(portInput);
    await user.type(portInput, "9090");
    await user.click(within(editDialog).getByTestId("domain-submit"));

    await waitFor(() => {
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/domains/demo.example.test"),
        ),
      ).toBe(true);
    });
    const updated = calls.find((c) => c.method === "PUT");
    expect(updated?.body).toEqual({
      service: "web",
      port: "9090",
      protocol: "http",
      cert_mode: "http01",
    });

    await user.click(screen.getByTestId("domain-remove-button"));
    const removeDialog = await screen.findByTestId("domain-remove-dialog");
    await user.click(within(removeDialog).getByTestId("domain-remove-submit"));
    await waitFor(() => {
      expect(
        calls.some(
          (c) => c.method === "DELETE" && c.url.includes("/domains/demo.example.test"),
        ),
      ).toBe(true);
    });
  });

  it("platform admin：资源面只读说明行（P0-3 双门）", async () => {
    setToken("flt_test");
    const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      const json = (payload: unknown) =>
        Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(payload) });
      if (url.endsWith("/auth/me")) {
        return json({
          user: { id: "01U1", email: "admin@t.test", is_platform_admin: true },
          teams: [],
          project_overrides: [],
        });
      }
      if (url.endsWith("/domains")) {
        return json(DOMAINS);
      }
      return json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    await waitFor(() => expect(screen.getByText("demo.example.test")).toBeInTheDocument());
    expect(await screen.findByTestId("domains-write-note")).toHaveTextContent(/read-only/);
    expect(screen.queryByTestId("domain-add-button")).not.toBeInTheDocument();
  });
});
