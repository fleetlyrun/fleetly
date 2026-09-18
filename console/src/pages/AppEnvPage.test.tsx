// env 管理测试：pending 变更独立分组可见（票面验收项）、effective 分组、
// 值脱敏（列表无值——全部掩码显示）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
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
});
