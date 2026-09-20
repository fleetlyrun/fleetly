// 部署字段级 diff 视图测试（T0-V2.4）：行内 What changed 展开后——
// 改字段态（image/replicas 逐字段 old → new）、空 diff 态（同 revision
// 重部署 → 诚实提示）、404 态（上一版快照滑出保留窗）。数据面全走 stub
// fetch：/deployments 列表 + /revisions/<id>/spec 两版快照正文。

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

function notFound() {
  return {
    ok: false,
    status: 404,
    statusText: "Not Found",
    json: () =>
      Promise.resolve({ code: "E_NOT_FOUND", message: "revision not found" }),
  };
}

function specJSON(services: unknown[]): string {
  return JSON.stringify({ name: "demo", services, spec_hash: "h" });
}

function stubFetch(
  deployments: unknown[],
  specs: Record<string, unknown>,
): ReturnType<typeof vi.fn> {
  return vi.fn().mockImplementation((url: unknown) => {
    const u = String(url);
    if (u.includes("/deployments")) {
      return Promise.resolve(ok({ deployments }));
    }
    const m = u.match(/\/revisions\/([^/]+)\/spec/);
    if (m && m[1]) {
      const body = specs[m[1]];
      return Promise.resolve(body ? ok(body) : notFound());
    }
    return Promise.resolve(ok({ revisions: [] }));
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

function succeededDeployment(id: string, revisionId: string) {
  return {
    id,
    app: "demo",
    kind: "deploy",
    status: "succeeded",
    phase: "",
    revision_id: revisionId,
    error_code: "",
    verdict: "",
    recovery: "",
  };
}

describe("AppDeploymentsPage field-level diff (T0-V2.4)", () => {
  it("expands a row and shows per-field old → new values from two real snapshots", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetch(
        [
          succeededDeployment("dep_new", "rev_2"),
          succeededDeployment("dep_old", "rev_1"),
        ],
        {
          rev_1: {
            revision_id: "rev_1",
            seq: "1",
            compose: specJSON([
              { name: "web", image: "nginx:1.26", deploy: { replicas: 2 } },
            ]),
          },
          rev_2: {
            revision_id: "rev_2",
            seq: "2",
            compose: specJSON([
              { name: "web", image: "nginx:1.27", deploy: { replicas: 3 } },
            ]),
          },
        },
      ),
    );

    renderPage();
    const user = userEvent.setup();
    await waitFor(() => screen.getAllByTestId("deployment-row"));
    await user.click(
      screen.getAllByRole("button", { name: "What changed" })[0],
    );

    const panel = await waitFor(() => screen.getByTestId("deployment-diff"));
    expect(panel).toHaveTextContent("services.web.image");
    expect(panel).toHaveTextContent("nginx:1.26");
    expect(panel).toHaveTextContent("nginx:1.27");
    expect(panel).toHaveTextContent("services.web.deploy.replicas");
    expect(panel).toHaveTextContent("Changed");
  });

  it("shows an honest empty-diff note when both deployments share one revision (redeploy)", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch(
      [
        succeededDeployment("dep_a", "rev_same"),
        succeededDeployment("dep_b", "rev_same"),
      ],
      {},
    );
    vi.stubGlobal("fetch", fetchMock);

    renderPage();
    const user = userEvent.setup();
    await waitFor(() => screen.getAllByTestId("deployment-row"));
    await user.click(
      screen.getAllByRole("button", { name: "What changed" })[0],
    );

    const panel = await waitFor(() => screen.getByTestId("deployment-diff"));
    expect(panel).toHaveTextContent("No changes vs previous revision.");
    // 同 revision 无需拉快照：零 spec 请求。
    const specCalls = fetchMock.mock.calls.filter((c) =>
      String(c[0]).includes("/spec"),
    );
    expect(specCalls).toHaveLength(0);
  });

  it("explains the retention window when the previous snapshot is gone (404)", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetch(
        [
          succeededDeployment("dep_new", "rev_9"),
          succeededDeployment("dep_old", "rev_gone"),
        ],
        {
          // rev_gone 无正文 → 404（superseded 滑出保留窗）。
          rev_9: {
            revision_id: "rev_9",
            seq: "9",
            compose: specJSON([{ name: "web", image: "nginx:1.27" }]),
          },
        },
      ),
    );

    renderPage();
    const user = userEvent.setup();
    await waitFor(() => screen.getAllByTestId("deployment-row"));
    await user.click(
      screen.getAllByRole("button", { name: "What changed" })[0],
    );

    const panel = await waitFor(() => screen.getByTestId("deployment-diff"));
    expect(panel).toHaveTextContent(
      "outside the retention window (last 5 successful deployments)",
    );
  });
});
