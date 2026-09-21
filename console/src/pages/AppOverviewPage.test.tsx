// 应用概览页测试（E3-8/E5 Cron 切面）：服务清单从最近 active revision 的
// 归一化快照现读——cron 服务如实标注 scheduled（不冒充长驻态），长驻服务
// 不做状态冒充；cron 区块（台账 + 手动触发）挂载于概览页。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppOverviewPage } from "@/pages/AppOverviewPage";
import { setToken } from "@/api/client";

const COMPOSE = JSON.stringify({
  name: "demo",
  services: [
    { name: "web", image: "nginx:1.27" },
    { name: "jobber", cron: { expression: "*/5 * * * *" } },
  ],
});

const RUNS = [
  {
    id: "run_ok",
    service: "jobber",
    expression: "*/5 * * * *",
    scheduled_at: "2026-09-21T02:00:00Z",
    finished_at: "2026-09-21T02:00:10Z",
    status: "succeeded",
  },
];

function stubOverviewFetch(log: { url: string; method: string }[] = []) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    log.push({ url, method: init?.method ?? "GET" });
    if (url.endsWith("/spec")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ compose: COMPOSE }),
      });
    }
    if (url.includes("/cron-runs")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ app: "demo", runs: RUNS }),
      });
    }
    if (url.includes("/services/jobber/trigger")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            run: { id: "run_manual", service: "jobber", status: "started" },
          }),
      });
    }
    if (url.includes("/apps/demo/revisions")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({ revisions: [{ id: "rev1", status: "active" }] }),
      });
    }
    if (url.includes("/placement")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ volumes: [] }),
      });
    }
    // GET /apps/demo（GetAppResponse）
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "",
      json: () =>
        Promise.resolve({
          id: "a1",
          name: "demo",
          lifecycle: "active",
          derived_state: "running",
        }),
    });
  });
}

function renderOverview(app = "demo") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[`/apps/${app}`]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps/:name" element={<AppOverviewPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AppOverviewPage services list (E5 Cron)", () => {
  it("marks cron services scheduled (not running) and lists long-running services plainly", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubOverviewFetch());

    renderOverview();

    await screen.findByTestId("services-list");
    const badges = await screen.findAllByTestId("service-scheduled-badge");
    expect(badges).toHaveLength(1);
    expect(badges[0]?.textContent).toContain("scheduled");
    expect(badges[0]?.textContent).not.toContain("running");
    // cron 区块随 cron 服务挂载（含台账）。
    await waitFor(() =>
      expect(screen.getByTestId("cron-runs-list")).toBeInTheDocument(),
    );
    expect(screen.getByText("*/5 * * * *")).toBeInTheDocument();
  });

  it("triggers a cron service from the overview page after confirm", async () => {
    setToken("flt_test");
    const log: { url: string; method: string }[] = [];
    vi.stubGlobal("fetch", stubOverviewFetch(log));
    renderOverview();
    await waitFor(() => screen.getByTestId("cron-runs-list"));

    fireEvent.click(screen.getByRole("button", { name: "Run now" }));
    fireEvent.click(screen.getByTestId("cron-trigger-button"));

    await waitFor(() =>
      expect(screen.getByTestId("cron-trigger-result").textContent).toContain("triggered"),
    );
    const trigger = log.find(
      (e) => e.method === "POST" && e.url.includes("/services/jobber/trigger"),
    );
    expect(trigger).toBeTruthy();
  });
});
