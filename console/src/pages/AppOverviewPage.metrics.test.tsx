// 应用概览页 replicas 水位列（E6 W5-S3 挂账收敛）+ Resources 卡 opt-in
// 引导挂载（存量 AppOverviewPage.test 的只增补充——锚点 service-replicas /
// app-metrics-card 不破坏存量断言）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppOverviewPage } from "@/pages/AppOverviewPage";
import { setToken } from "@/api/client";

const COMPOSE = JSON.stringify({
  name: "demo",
  services: [
    { name: "web", deploy: { replicas: 3 } },
    { name: "worker", deploy: { mode: "global" } },
    { name: "jobber", cron: { expression: "*/5 * * * *" } },
  ],
});

describe("AppOverviewPage replicas column + metrics card anchor", () => {
  it("shows declared replicas per service (compose default 1, global, cron excluded) and mounts the opt-in metrics card", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/spec")) {
          return Promise.resolve({
            ok: true,
            status: 200,
            statusText: "",
            json: () => Promise.resolve({ compose: COMPOSE }),
          });
        }
        if (url.includes("/metrics/status")) {
          return Promise.resolve({
            ok: true,
            status: 200,
            statusText: "",
            json: () =>
              Promise.resolve({
                mode: "unset",
                mode_set: false,
                components: [],
                nodes_reporting: 0,
                nodes_total: 1,
                retention_days: 14,
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
        // GET /apps/demo 与 cron-runs 等回空（本测试不涉）。
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
      }),
    );

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MemoryRouter initialEntries={["/apps/demo"]}>
        <QueryClientProvider client={client}>
          <Routes>
            <Route path="/apps/:name" element={<AppOverviewPage />} />
          </Routes>
        </QueryClientProvider>
      </MemoryRouter>,
    );

    await screen.findByTestId("services-list");
    // 行内容随 spec 查询解析异步填充——等待副本位渲染再断言。
    const replicaCells = await screen.findAllByTestId("service-replicas");
    // cron 服务不参与副本水位语义（—）。
    expect(replicaCells).toHaveLength(2);
    expect(replicaCells[0]?.textContent).toBe("3");
    expect(replicaCells[1]?.textContent).toBe("global (per node)");

    // Resources 卡以 opt-in 引导态挂载（mode=unset）。
    await screen.findByTestId("app-metrics-card");
    expect(screen.getByTestId("app-metrics-card").textContent).toContain("opt-in");
  });
});
