// 审计页测试（v0.3 W3-S3，rbac-teams §6/§7 验收面）：台账表（actor
// email 反查解析 + error 徽章）、过滤 Apply 的查询串透传、行展开 diff 摘
// 要、分页 offset 透传与 total 投影、留存设置（未设置缺省口径 / 修改确认
// PUT 载荷）、导出指引（诚实口径——Console 只浏览）、非平台管理员 denied
// 分支。AdminPage.test 同款 mock 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AuditPage } from "@/pages/AuditPage";
import { setToken } from "@/api/client";

const AUDIT_ROWS = {
  audits: [
    {
      id: "01A1",
      at: "2026-09-23T10:00:00Z",
      actor: "user:01U1",
      action: "api.AppsService.DeleteApp",
      target: "app:01APP",
      result: "ok",
      diff_summary: "app=old-web deleted",
    },
    {
      id: "01A2",
      at: "2026-09-23T11:00:00Z",
      actor: "human",
      action: "auth.login_failed",
      target: "login:ada@example.com",
      result: "error",
      error_code: "E_AUTH_INVALID_CREDENTIALS",
      request_id: "req-42",
    },
  ],
  total: 3,
};

function stubFetch(overrides: {
  retention?: Record<string, unknown>;
  nonAdmin?: boolean;
  total?: number;
} = {}) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.includes("/audit/retention") && method === "PUT") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ days: body.days }),
      });
    }
    if (url.includes("/audit/retention")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve(overrides.retention ?? { set: true, days: 45, updated_at: "2026-09-22T00:00:00Z" }),
      });
    }
    if (/\/audit\?/.test(url)) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({ ...AUDIT_ROWS, total: overrides.total ?? AUDIT_ROWS.total }),
      });
    }
    if (url.endsWith("/users")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            users: [
              { id: "01U1", email: "ada@example.com", display_name: "Ada", created_at: "2026-09-20T00:00:00Z" },
              { id: "01U2", email: "grace@example.com", display_name: "Grace", created_at: "2026-09-20T00:00:00Z" },
            ],
          }),
      });
    }
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "ada@example.com", is_platform_admin: !overrides.nonAdmin },
            teams: [],
          }),
      });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
}

function renderAt(path = "/admin/audit") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={client}>
        <AuditPage />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AuditPage (platform admin, W3-S3)", () => {
  it("lists rows with actor email resolution, error badge and the CLI export hint", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());

    renderAt();
    await screen.findByTestId("audit-page");

    const rows = await screen.findAllByTestId("audit-row");
    expect(rows).toHaveLength(2);
    // user:01U1 反查为 email（平台管理员 /users 全量清单批反查）。
    const actorCells = screen.getAllByTestId("audit-actor-email");
    expect(actorCells[0].textContent).toBe("ada@example.com");
    // human/system 形态落原始 actor。
    expect(actorCells[1].textContent).toBe("human");
    // error 行徽章 + error code 列。
    expect(screen.getByText("E_AUTH_INVALID_CREDENTIALS")).toBeInTheDocument();
    // 导出走 CLI 的指引文案（Console 只浏览的诚实口径）。
    expect(screen.getByTestId("audit-export-hint").textContent).toContain("fleetly audit export --csv");
    // 留存设置行显式值。
    await waitFor(() => {
      expect(screen.getByTestId("audit-retention-value").textContent).toContain("45 days");
    });
  });

  it("applies filters into the /audit query string (actor + result passthrough)", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findAllByTestId("audit-row");

    await user.type(screen.getByTestId("audit-filter-actor"), "system");
    await user.selectOptions(screen.getByTestId("audit-filter-result"), "error");
    await user.click(screen.getByTestId("audit-filter-apply"));

    await waitFor(() => {
      const called = fetchMock.mock.calls.find(
        (c) => /\/audit\?/.test(String(c[0])) && String(c[0]).includes("actor=system"),
      );
      expect(called).toBeDefined();
      expect(String(called?.[0])).toContain("result=error");
      expect(String(called?.[0])).toContain("limit=50");
    });
  });

  it("expands a row to show the masked diff summary", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());
    const user = userEvent.setup();

    renderAt();
    await screen.findAllByTestId("audit-row");

    expect(screen.queryByTestId("audit-diff")).not.toBeInTheDocument();
    await user.click(screen.getAllByTestId("audit-row-expand")[0]);
    const diff = await screen.findByTestId("audit-diff");
    expect(diff.textContent).toContain("app=old-web deleted");
    await user.click(screen.getAllByTestId("audit-row-expand")[1]);
    await waitFor(() => {
      expect(screen.getByTestId("audit-diff").textContent).toContain("req-42");
    });
  });

  it("pages with offset passthrough and renders the total", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch({ total: 120 });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findAllByTestId("audit-row");
    expect(screen.getByTestId("audit-total").textContent).toBe("120");

    await user.click(screen.getByTestId("audit-page-next"));
    await waitFor(() => {
      const called = fetchMock.mock.calls.find(
        (c) => /\/audit\?/.test(String(c[0])) && String(c[0]).includes("offset=50"),
      );
      expect(called).toBeDefined();
    });
  });

  it("shows the default-window wording when retention is not explicitly set", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch({ retention: {} }));

    renderAt();
    await screen.findByTestId("audit-page");
    await waitFor(() => {
      expect(screen.getByTestId("audit-retention-value").textContent).toContain("not explicitly set");
      expect(screen.getByTestId("audit-retention-value").textContent).toContain("90");
    });
  });

  it("saves a new retention value after confirmation (PUT /audit/retention payload)", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("audit-retention-value");

    await user.type(screen.getByTestId("audit-retention-input"), "60");
    await user.click(screen.getByTestId("audit-retention-save"));
    await screen.findByTestId("audit-retention-dialog");
    await user.click(screen.getByTestId("audit-retention-confirm"));

    await waitFor(() => {
      const put = fetchMock.mock.calls.find(
        (c) => String(c[0]).includes("/audit/retention") && c[1]?.method === "PUT",
      );
      expect(put).toBeDefined();
      const body = JSON.parse(String(put?.[1]?.body)) as Record<string, unknown>;
      expect(body.days).toBe(60);
    });
  });

  it("renders the denied branch for non platform administrators", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch({ nonAdmin: true }));

    renderAt();
    await screen.findByTestId("audit-page-denied");
    expect(screen.queryByTestId("audit-page")).not.toBeInTheDocument();
    expect(screen.getByText(/Platform administrator access is required/)).toBeInTheDocument();
  });
});
