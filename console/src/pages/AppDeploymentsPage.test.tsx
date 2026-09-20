// 部署历史测试：失败行展示错误信封形态（error_code + verdict + recovery
// 可见）；中间态徽章（observing/blocked_waiting）一等渲染。跟踪轮询持续
// 失败（M9-5）：错误信封一等渲染而非永远转圈；refetchInterval 回调对空
// data 形态可选链守卫（M9-7）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppDeploymentsPage } from "@/pages/AppDeploymentsPage";
import { setToken } from "@/api/client";

function ok(body: unknown) {
  return {
    ok: true,
    status: 200,
    statusText: "",
    json: () => Promise.resolve(body),
  };
}

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

describe("AppDeploymentsPage deployment tracking (M9-5 / M9-7)", () => {
  it("renders the error envelope when the tracked deployment poll keeps failing (no infinite spinner)", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: { method?: string }) => {
        const u = String(url);
        // Deploy 提交成功入队 → 开始跟踪。
        if (init?.method === "POST" && u.includes("/apps/demo/deployments")) {
          return Promise.resolve(
            ok({ deployment_id: "dep_track", warnings: [] }),
          );
        }
        // 跟踪轮询持续 500（空 data 形态——refetchInterval 回调不得抛
        // TypeError，页面不得永远转圈）。
        if (u.includes("/deployments/dep_track")) {
          return Promise.resolve({
            ok: false,
            status: 500,
            statusText: "",
            json: () =>
              Promise.resolve({
                code: "E_INTERNAL",
                message: "tracking unavailable",
                suggestion: "check daemon logs",
              }),
          });
        }
        return Promise.resolve(ok({ deployments: [], revisions: [] }));
      }),
    );

    renderPage();
    const user = userEvent.setup();
    await user.type(
      screen.getByLabelText("Compose YAML"),
      "services:\n  web:\n    image: nginx:1.27-alpine\n",
    );
    await user.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const envelope = screen.getByTestId("error-envelope");
      expect(envelope).toHaveTextContent("E_INTERNAL");
      expect(envelope).toHaveTextContent("tracking unavailable");
      expect(envelope).toHaveTextContent("check daemon logs");
    });
    // 跟踪块仍在（部署 ID 可见），只是状态以错误信封呈现。
    expect(screen.getByTestId("deployment-tracker")).toHaveTextContent(
      "dep_track",
    );
  });
});
