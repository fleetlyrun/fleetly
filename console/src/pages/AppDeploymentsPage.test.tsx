// 部署历史测试：失败行展示错误信封形态（error_code + verdict + recovery
// 可见）；中间态徽章（observing/blocked_waiting）一等渲染。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppDeploymentsPage } from "@/pages/AppDeploymentsPage";
import { setToken } from "@/api/client";

function stubFetch(deployments: unknown[]) {
  return vi.fn().mockImplementation((url: string) => {
    if (String(url).includes("/deployments")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ deployments }),
      });
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "",
      json: () => Promise.resolve({ revisions: [] }),
    });
  });
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/apps/demo/deployments"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps/:name/deployments" element={<AppDeploymentsPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AppDeploymentsPage failure envelope", () => {
  it("renders code + message + suggestion for a failed deployment row", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetch([
        {
          id: "dep_01",
          app: "demo",
          kind: "deploy",
          status: "failed",
          phase: "",
          revision_id: "rev_1",
          error_code: "E_BUILD_FAILED",
          verdict: "buildkit solve failed at step 4/6",
          recovery: "fix Dockerfile and redeploy; build log in the artifacts dir",
          created_at: "2026-09-18T10:00:00Z",
        },
      ]),
    );

    renderPage();

    await waitFor(() => screen.getByRole("alert"));
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("E_BUILD_FAILED");
    expect(alert).toHaveTextContent("buildkit solve failed at step 4/6");
    expect(alert).toHaveTextContent(
      "fix Dockerfile and redeploy; build log in the artifacts dir",
    );
  });

  it("renders intermediate states (observing / blocked_waiting) as first-class badges", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetch([
        {
          id: "dep_obs",
          app: "demo",
          kind: "deploy",
          status: "observing",
          phase: "",
          revision_id: "rev_2",
          error_code: "",
          verdict: "",
          recovery: "",
        },
        {
          id: "dep_bw",
          app: "demo",
          kind: "deploy",
          status: "releasing",
          phase: "blocked_waiting",
          revision_id: "rev_3",
          error_code: "",
          verdict: "",
          recovery: "",
        },
      ]),
    );

    renderPage();

    await waitFor(() => screen.getByText("dep_bw"));
    const rows = screen.getAllByTestId("deployment-row");
    expect(rows).toHaveLength(2);
    const badges = screen.getAllByTestId("state-badge");
    const states = badges.map((b) => b.getAttribute("data-state"));
    expect(states).toContain("observing");
    expect(states).toContain("blocked_waiting");
  });
});
