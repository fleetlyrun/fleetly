// Cron 区块测试（E5 Cron，架构 §4.3）：运行台账渲染（状态徽章色语义、
// skipped 的 skip_reason、failed 的 error）、cron 服务 expression/timezone/
// timeout 展示、手动触发按钮（confirm 后触发；skipped 结果行内提示）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { CronSection } from "@/components/cron-section";
import { setToken } from "@/api/client";

const COMPOSE_WITH_CRON = JSON.stringify({
  name: "demo",
  services: [
    { name: "web", image: "nginx:1.27" },
    {
      name: "jobber",
      cron: { expression: "*/5 * * * *", timezone: "", timeout: "" },
    },
  ],
});

const RUNS = [
  {
    id: "run_ok",
    service: "jobber",
    expression: "*/5 * * * *",
    scheduled_at: "2026-09-21T02:00:00Z",
    started_at: "2026-09-21T02:00:01Z",
    finished_at: "2026-09-21T02:00:10Z",
    status: "succeeded",
  },
  {
    id: "run_skip",
    service: "jobber",
    expression: "*/5 * * * *",
    scheduled_at: "2026-09-21T02:05:00Z",
    status: "skipped",
    skip_reason: "overlap",
  },
  {
    id: "run_fail",
    service: "jobber",
    expression: "*/5 * * * *",
    scheduled_at: "2026-09-21T02:10:00Z",
    started_at: "2026-09-21T02:10:01Z",
    finished_at: "2026-09-21T02:10:05Z",
    status: "failed",
    error: "container exited 1",
  },
];

function stubCronFetch(opts?: {
  compose?: string;
  runs?: Record<string, unknown>[];
}) {
  const log: { url: string; method: string }[] = [];
  const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    log.push({ url, method: init?.method ?? "GET" });

    if (url.includes("/cron-runs")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ app: "demo", runs: opts?.runs ?? RUNS }),
      });
    }
    if (url.endsWith("/spec")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ compose: opts?.compose ?? COMPOSE_WITH_CRON }),
      });
    }
    if (url.includes("/trigger")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            run: {
              id: "run_manual",
              service: "jobber",
              status: "skipped",
              skip_reason: "node_unavailable",
            },
          }),
      });
    }
    if (url.includes("/apps/demo/revisions")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ revisions: [{ id: "rev1", status: "active" }] }),
      });
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "",
      json: () => Promise.resolve({}),
    });
  });
  return { fetchMock, log };
}

function renderSection(app = "demo") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <CronSection app={app} />
    </QueryClientProvider>,
  );
}

describe("CronSection (E5 Cron)", () => {
  it("renders cron service declaration and the runs list with badge color semantics", async () => {
    setToken("flt_test");
    const { fetchMock } = stubCronFetch();
    vi.stubGlobal("fetch", fetchMock);

    renderSection();

    // cron 服务区块：expression/timezone/timeout 如实呈现。
    await waitFor(() => expect(screen.getByText("*/5 * * * *")).toBeInTheDocument());
    const row = screen.getByTestId("cron-service-row");
    expect(row.getAttribute("data-service")).toBe("jobber");
    expect(row.textContent).toContain("UTC");
    expect(row.textContent).toContain("default");

    // 台账：状态徽章按色板语义（succeeded 绿 / failed 红 / skipped 灰）。
    await waitFor(() =>
      expect(screen.getByTestId("cron-runs-list")).toBeInTheDocument(),
    );
    const badges = screen.getAllByTestId("cron-run-status");
    expect(badges).toHaveLength(3);
    expect(badges.find((b) => b.getAttribute("data-status") === "succeeded")?.innerHTML).toContain("bg-emerald-500");
    expect(badges.find((b) => b.getAttribute("data-status") === "failed")?.innerHTML).toContain("bg-red-500");
    expect(badges.find((b) => b.getAttribute("data-status") === "timeout") === undefined).toBe(true);
    const skipped = badges.find((b) => b.getAttribute("data-status") === "skipped");
    expect(skipped?.innerHTML).toContain("bg-zinc-400");
    // skipped 行带 skip_reason；failed 行带 error 摘要。
    expect(screen.getByText("skipped: overlap")).toBeInTheDocument();
    expect(screen.getByText("container exited 1")).toBeInTheDocument();
  });

  it("triggers a manual run after confirm and surfaces the skip reason inline", async () => {
    setToken("flt_test");
    const { fetchMock, log } = stubCronFetch();
    vi.stubGlobal("fetch", fetchMock);

    renderSection();
    await waitFor(() => screen.getByTestId("cron-runs-list"));

    // confirm 门：先确认，后触发。
    fireEvent.click(screen.getByRole("button", { name: "Run now" }));
    expect(screen.getByTestId("cron-trigger-button")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("cron-trigger-button"));

    await waitFor(() =>
      expect(screen.getByTestId("cron-trigger-result").textContent).toContain(
        "skipped (node_unavailable)",
      ),
    );
    const trigger = log.find((e) => e.url.includes("/apps/demo/services/jobber/trigger"));
    expect(trigger?.method).toBe("POST");
  });

  it("renders nothing for an app without cron services", async () => {
    setToken("flt_test");
    const { fetchMock } = stubCronFetch({
      compose: JSON.stringify({ name: "demo", services: [{ name: "web" }] }),
      runs: [],
    });
    vi.stubGlobal("fetch", fetchMock);

    const { container } = renderSection();
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith("/spec"))).toBe(true),
    );
    await new Promise((resolve) => setTimeout(resolve, 50));
    // 无 cron 服务 → 不渲染区块（也不冒充空态）；台账查询根本不发出。
    expect(screen.queryByTestId("cron-runs-list")).not.toBeInTheDocument();
    expect(container).toBeEmptyDOMElement();
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/cron-runs"))).toBe(false);
  });
});
