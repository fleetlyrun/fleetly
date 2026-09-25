// 自动扩缩策略卡测试（W5-S1）：策略展示（有/无策略两态）、metrics off
// 休眠态提示、编辑表单（保存 PUT 载荷）、删除、前端 admin 门。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { AppScalingCard } from "@/components/app-scaling-card";
import { setToken } from "@/api/client";

// 前端角色门固定 admin+（服务端硬门在 API 面；这里钉卡的门面）。
vi.mock("@/lib/context", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/context")>();
  return {
    ...actual,
    useTeamCapabilities: vi.fn(() => ({
      ...actual.useTeamCapabilities(),
      canAdminResources: true,
    })),
  };
});

const METRICS_ON = {
  mode: "on",
  mode_set: true,
  components: [],
  nodes_reporting: 0,
  nodes_total: 1,
  retention_days: 14,
};

const POLICY = {
  name: "demo",
  service: "web",
  min_replicas: 2,
  max_replicas: 8,
  target_cpu_pct: 60,
  target_mem_pct: 0,
  cooldown_seconds: 300,
  created_at: "2026-09-25T00:00:00Z",
  updated_at: "2026-09-25T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    statusText: String(status),
    json: () => Promise.resolve(body),
  });
}

function renderCard(app = "demo", services = ["web", "worker"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AppScalingCard app={app} services={services} />
    </QueryClientProvider>,
  );
}

describe("AppScalingCard", () => {
  it("shows the set policy summary per service", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/metrics/status")) return jsonResponse(METRICS_ON);
        if (url.endsWith("/apps/demo/scaling/web")) return jsonResponse(POLICY);
        if (url.includes("/scaling/")) {
          return jsonResponse(
            { code: "", message: "no scaling policy" },
            404,
          );
        }
        return jsonResponse({});
      }),
    );
    renderCard();
    await waitFor(() => expect(screen.getAllByTestId("scaling-row").length).toBe(2));
    // 查询解析完成后按 data-service 定位行。
    await waitFor(() => {
      const webRow = document.querySelector("[data-testid='scaling-row'][data-service='web']");
      expect(webRow?.getAttribute("data-policy")).toBe("set");
    });
    const webRow = document.querySelector("[data-testid='scaling-row'][data-service='web']");
    expect(webRow?.textContent).toContain("2–8 replicas");
    expect(webRow?.textContent).toContain("cpu 60%");
    expect(webRow?.textContent).toContain("mem off");
    expect(webRow?.textContent).toContain("cooldown 300s");
    // 无策略服务如实呈现（404 归一为 no policy，不是错误横幅）。
    const workerRow = document.querySelector("[data-testid='scaling-row'][data-service='worker']");
    expect(workerRow?.textContent).toContain("No policy");
  });

  it("shows the dormant hint and per-row dormant badges while metrics.mode is off", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/metrics/status")) return jsonResponse({ ...METRICS_ON, mode: "unset", mode_set: false });
        if (url.includes("/scaling/")) {
          return jsonResponse({ code: "", message: "no scaling policy" }, 404);
        }
        return jsonResponse({});
      }),
    );
    renderCard();
    await screen.findByTestId("scaling-dormant-hint");
    await waitFor(() => expect(screen.getAllByTestId("scaling-row-dormant").length).toBe(2));
  });

  it("saves an edited policy via PUT with the form values", async () => {
    setToken("flt_test");
    const log: { url: string; method: string; body?: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        log.push({ url, method: init?.method ?? "GET", body: init?.body });
        if (url.endsWith("/metrics/status")) return jsonResponse(METRICS_ON);
        if (url.endsWith("/apps/demo/scaling/web")) return jsonResponse(POLICY);
        if (url.includes("/scaling/")) {
          return jsonResponse({ code: "", message: "no scaling policy" }, 404);
        }
        return jsonResponse({});
      }),
    );
    renderCard();
    // 编辑既有策略。
    await waitFor(() => expect(screen.getByLabelText("Edit policy of web")).toBeInTheDocument());
    fireEvent.click(screen.getByLabelText("Edit policy of web"));
    const minInput = screen.getByTestId("scaling-input-min_replicas") as HTMLInputElement;
    fireEvent.change(minInput, { target: { value: "3" } });
    fireEvent.click(screen.getByTestId("scaling-save"));
    await waitFor(() => {
      const put = log.find((e) => e.method === "PUT" && e.url.endsWith("/apps/demo/scaling/web"));
      expect(put).toBeTruthy();
      const body = JSON.parse(String(put?.body));
      expect(body.min_replicas).toBe(3);
      expect(body.target_cpu_pct).toBe(60);
    });
  });

  it("removes a policy via DELETE", async () => {
    setToken("flt_test");
    const log: { url: string; method: string }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        log.push({ url, method: init?.method ?? "GET" });
        if (url.endsWith("/metrics/status")) return jsonResponse(METRICS_ON);
        if (url.endsWith("/apps/demo/scaling/web") && (init?.method ?? "GET") === "DELETE") {
          return jsonResponse({ name: "demo", service: "web", removed: true });
        }
        if (url.endsWith("/apps/demo/scaling/web")) return jsonResponse(POLICY);
        return jsonResponse({ code: "", message: "no scaling policy" }, 404);
      }),
    );
    renderCard();
    await waitFor(() => expect(screen.getByLabelText("Remove policy of web")).toBeInTheDocument());
    fireEvent.click(screen.getByLabelText("Remove policy of web"));
    await waitFor(() => {
      expect(log.some((e) => e.method === "DELETE" && e.url.endsWith("/apps/demo/scaling/web"))).toBe(true);
    });
  });

  it("validates that at least one target is set before submitting", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/metrics/status")) return jsonResponse(METRICS_ON);
        if (url.includes("/scaling/")) {
          return jsonResponse({ code: "", message: "no scaling policy" }, 404);
        }
        return jsonResponse({});
      }),
    );
    renderCard();
    await waitFor(() => expect(screen.getAllByText("Add policy").length).toBeGreaterThan(0));
    const addButtons = screen.getAllByText("Add policy");
    fireEvent.click(addButtons[0]);
    fireEvent.change(screen.getByTestId("scaling-input-target_cpu_pct"), { target: { value: "0" } });
    fireEvent.change(screen.getByTestId("scaling-input-target_mem_pct"), { target: { value: "0" } });
    fireEvent.click(screen.getByTestId("scaling-save"));
    expect(await screen.findByTestId("scaling-form-error")).toHaveTextContent(
      "at least one target is required",
    );
  });
});
