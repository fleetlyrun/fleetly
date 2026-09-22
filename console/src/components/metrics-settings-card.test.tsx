// metrics 设置卡测试（E6 W5-S3，SystemPage Metrics 页签）：缺省 unset
// 展示 + Enable 开关、on 态组件部署态 + Disable、跨节点诚实文案常驻。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { MetricsSettingsCard } from "@/components/metrics-settings-card";
import { setToken } from "@/api/client";

const UNSET = {
  mode: "unset",
  mode_set: false,
  components: [],
  nodes_reporting: 0,
  nodes_total: 2,
  retention_days: 14,
};

function jsonResponse(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    statusText: String(status),
    json: () => Promise.resolve(body),
  });
}

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MetricsSettingsCard />
    </QueryClientProvider>,
  );
}

describe("MetricsSettingsCard", () => {
  it("shows the unset default and enables via PUT /metrics/mode", async () => {
    setToken("flt_test");
    const log: { url: string; method: string; body?: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        log.push({ url, method: init?.method ?? "GET", body: init?.body });
        if (url.endsWith("/metrics/status")) return jsonResponse(UNSET);
        return jsonResponse({});
      }),
    );
    renderCard();
    await screen.findByTestId("metrics-status-card");
    // 查询解析前卡先挂载（mode 空投影）——等待数据到位再断言。
    await waitFor(() =>
      expect(screen.getByTestId("metrics-status-card").textContent).toContain("metrics.mode: unset"),
    );
    expect(screen.getByTestId("metrics-status-card").textContent).toContain("retention 14 days");
    // 诚实文案常驻（§6 挂账票修订：VPC/LAN 直连 + 采集面暴露口径）。
    const text = screen.getByTestId("metrics-status-card").textContent ?? "";
    expect(text).toContain("direct node addresses (VPC/LAN)");
    expect(text).toContain("blocked by the node firewall");

    fireEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => {
      const put = log.find((e) => e.method === "PUT" && e.url.endsWith("/metrics/mode"));
      expect(put).toBeTruthy();
      expect(JSON.parse(String(put?.body))).toEqual({ mode: "on" });
    });
  });

  it("lists the three components with deploy state and a working Disable when on", async () => {
    setToken("flt_test");
    const log: { url: string; method: string; body?: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        log.push({ url, method: init?.method ?? "GET", body: init?.body });
        if (url.endsWith("/metrics/status")) {
          return jsonResponse({
            mode: "on",
            mode_set: true,
            components: [
              { name: "fleetly-cadvisor", exists: true, image: "" },
              { name: "fleetly-node-exporter", exists: true, image: "" },
              { name: "fleetly-victoriametrics", exists: false, image: "" },
            ],
            nodes_reporting: 1,
            nodes_total: 2,
            retention_days: 7,
          });
        }
        return jsonResponse({});
      }),
    );
    renderCard();
    await screen.findByTestId("metrics-status-card");
    await waitFor(() =>
      expect(screen.getByTestId("metrics-status-card").textContent).toContain("metrics.mode: on"),
    );
    const text = screen.getByTestId("metrics-status-card").textContent ?? "";
    expect(text).toContain("fleetly-cadvisor");
    expect(text).toContain("fleetly-node-exporter");
    expect(text).toContain("fleetly-victoriametrics");
    expect(text).toContain("not deployed");
    expect(text).toContain("1/2 nodes reporting");

    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() => {
      const put = log.find((e) => e.method === "PUT" && e.url.endsWith("/metrics/mode"));
      expect(put).toBeTruthy();
      expect(JSON.parse(String(put?.body))).toEqual({ mode: "unset" });
    });
  });
});
