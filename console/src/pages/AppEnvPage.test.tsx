// env 管理测试：pending 变更独立分组可见（票面验收项）、effective 分组、
// 值脱敏（列表无值——全部掩码显示）、行删除两步确认（2026-09-25 审查
// P2-4：全站破坏性动作确认纪律）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppEnvPage } from "@/pages/AppEnvPage";
import { setToken } from "@/api/client";

function stubFetchEnv(envVars: unknown[]) {
  return vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    statusText: "",
    json: () => Promise.resolve({ env_vars: envVars }),
  });
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/apps/demo/env"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps/:name/env" element={<AppEnvPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AppEnvPage pending grouping", () => {
  it("groups pending changes under the 'takes effect on next deploy' heading", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetchEnv([
        { key: "LOG_LEVEL", source: "platform", status: "effective" },
        { key: "NEW_KEY", source: "platform", status: "pending" },
        { key: "GONE_KEY", source: "platform", status: "pending" },
      ]),
    );

    renderPage();

    await waitFor(() => {
      expect(screen.getByTestId("env-pending-heading")).toHaveTextContent(
        "Pending — takes effect on next deploy (2)",
      );
    });
    const rows = screen.getAllByTestId("env-row");
    const pendingRows = rows.filter((r) => r.getAttribute("data-status") === "pending");
    const effectiveRows = rows.filter((r) => r.getAttribute("data-status") === "effective");
    expect(pendingRows.map((r) => r.getAttribute("data-key"))).toEqual([
      "NEW_KEY",
      "GONE_KEY",
    ]);
    expect(effectiveRows).toHaveLength(1);
  });

  it("masks values in the list (no plaintext via ListEnv)", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetchEnv([
        { key: "SECRET_KEY", source: "platform", status: "effective" },
      ]),
    );

    renderPage();

    await waitFor(() => screen.getByText("SECRET_KEY"));
    // ListEnv 恒无值——列表面只有掩码形态，没有真实值。
    expect(screen.getByText("••••••••")).toBeInTheDocument();
    expect(screen.queryByText("s3cr3t-value")).not.toBeInTheDocument();
  });

  it("badges source=system rows (FLEETLY_DB_* materialized vars) distinctly", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetchEnv([
        { key: "LOG_LEVEL", source: "platform", status: "effective" },
        {
          key: "FLEETLY_DB_PG_PROD_URL",
          source: "system",
          status: "effective",
        },
      ]),
    );

    renderPage();

    await waitFor(() => screen.getByText("FLEETLY_DB_PG_PROD_URL"));
    // system 物化行（E4 库连接串）独有徽章；platform 行保持普通文本。
    const badges = screen.getAllByTestId("env-system-badge");
    expect(badges).toHaveLength(1);
    expect(badges[0].closest("tr")?.getAttribute("data-key")).toBe("FLEETLY_DB_PG_PROD_URL");
  });
});

describe("AppEnvPage remove confirmation (2026-09-25 review P2-4)", () => {
  it("opens a confirm dialog first and deletes only after submit", async () => {
    setToken("flt_test");
    const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (method === "DELETE" && url.includes("/env/LOG_LEVEL")) {
        return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            env_vars: [{ key: "LOG_LEVEL", source: "platform", status: "effective" }],
          }),
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderPage();
    await screen.findByText("LOG_LEVEL");

    // 一键不删：点击删除钮只弹确认框（对话框含后果说明 + 取消/提交）。
    await user.click(screen.getByTestId("env-remove-button"));
    expect(await screen.findByTestId("env-remove-dialog")).toBeInTheDocument();
    expect(screen.getByTestId("env-remove-dialog").textContent).toContain(
      "takes effect on the next deployment",
    );
    expect(
      (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.some(
        ([, init]) => (init as RequestInit | undefined)?.method === "DELETE",
      ),
    ).toBe(false);

    // 取消路径：对话框关闭，仍无 DELETE。
    await user.click(screen.getByTestId("env-remove-cancel"));
    await waitFor(() =>
      expect(screen.queryByTestId("env-remove-dialog")).not.toBeInTheDocument(),
    );
    expect(
      (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.some(
        ([, init]) => (init as RequestInit | undefined)?.method === "DELETE",
      ),
    ).toBe(false);

    // 确认路径：提交 → DELETE /apps/demo/env/LOG_LEVEL。
    await user.click(screen.getByTestId("env-remove-button"));
    await user.click(await screen.findByTestId("env-remove-submit"));
    await waitFor(() => {
      const del = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.find(
        ([, init]) => (init as RequestInit | undefined)?.method === "DELETE",
      );
      expect(del).toBeTruthy();
      expect(String(del?.[0])).toContain("/apps/demo/env/LOG_LEVEL");
    });
  });
});
