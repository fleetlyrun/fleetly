// 告警设置卡测试（B 线 W5-S2，D-V3W5-1；SystemPage Alerts 页签）：
// metrics off 时的禁用态提示（前置门可见面）、开启链路（PUT /alerting/mode）、
// 规则清单渲染、创建流（POST /alerting/rules 的字段投影：for/severity/channels）、
// Test 即时求值、删除、规则编辑（backlog #4-④：预填 + PUT 部分更新载荷）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AlertingSettingsCard } from "@/components/alerting-settings-card";
import { setToken } from "@/api/client";

const METRICS_ON = {
  mode: "on",
  mode_set: true,
  components: [],
  nodes_reporting: 1,
  nodes_total: 1,
  retention_days: 14,
};
const METRICS_OFF = {
  mode: "unset",
  mode_set: false,
  components: [],
  nodes_reporting: 0,
  nodes_total: 1,
  retention_days: 14,
};
const ALERTS_UNSET = {
  mode: "unset",
  mode_set: false,
  vmalert_exists: false,
  vmalert_image: "",
  rule_count: 1,
  metrics_mode: "on",
};
const RULES = {
  rules: [
    {
      id: "01ALERT",
      name: "high-cpu",
      expr: "cpu_used > 90",
      for_duration_seconds: 300,
      labels: { severity: "critical" },
      channels: ["ep-1"],
      created_at: "2026-09-25T00:00:00Z",
      updated_at: "2026-09-25T00:00:00Z",
    },
  ],
};
const ENDPOINTS = {
  endpoints: [
    { id: "ep-1", name: "ops-slack", url: "", type: "slack", target: "", secret_fingerprint: "", event_patterns: ["*"], enabled: true },
  ],
};

function jsonResponse(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    statusText: String(status),
    json: () => Promise.resolve(body),
  });
}

function renderCard(metricsMode: "on" | "unset" = "on") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const log: { url: string; method: string; body?: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      log.push({ url, method: init?.method ?? "GET", body: init?.body });
      if (url.endsWith("/metrics/status")) return jsonResponse(metricsMode === "on" ? METRICS_ON : METRICS_OFF);
      if (url.endsWith("/alerting/status")) return jsonResponse(ALERTS_UNSET);
      if (url.endsWith("/alerting/rules")) {
        if (init?.method === "POST") return jsonResponse({ rule: RULES.rules[0] }, 200);
        if (init?.method === "DELETE") return jsonResponse({});
        return jsonResponse(RULES);
      }
      if (url.endsWith("/alerting/rules/01ALERT") && init?.method === "PUT") {
        return jsonResponse({ rule: { ...RULES.rules[0], expr: "up == 0" } });
      }
      if (url.endsWith("/alerting/rules:test")) {
        return jsonResponse({
          series: [{ metric: { alertname: "x", __name__: "up" }, points: [{ t: 1, v: 1 }] }],
        });
      }
      if (url.endsWith("/notifications/endpoints")) return jsonResponse(ENDPOINTS);
      return jsonResponse({});
    }),
  );
  render(
    <QueryClientProvider client={client}>
      <AlertingSettingsCard />
    </QueryClientProvider>,
  );
  return log;
}

describe("AlertingSettingsCard", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    setToken("flt_test");
  });

  it("disables the mode toggle with a gate hint while metrics.mode is off", async () => {
    renderCard("unset");
    const card = await screen.findByTestId("alerting-status-card");
    await waitFor(() =>
      expect(card.textContent).toContain("alerts.mode: unset"),
    );
    // metrics.mode=off：Enable 禁用 + 指引文案（前置门可见面）。
    const enable = screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement;
    expect(enable.disabled).toBe(true);
    expect(screen.getByTestId("alerts-metrics-gate-note").textContent).toContain(
      "Enable the Metrics stack first",
    );
  });

  it("enables alerts mode via PUT /alerting/mode when metrics is on", async () => {
    const log = renderCard("on");
    const card = await screen.findByTestId("alerting-status-card");
    await waitFor(() => expect(screen.getAllByTestId("alerting-rule-row").length).toBe(1));
    const enable = screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement;
    expect(enable.disabled).toBe(false);
    expect(card.textContent).toContain("metrics.mode: on");
    fireEvent.click(enable);
    await waitFor(() => {
      const put = log.find((e) => e.method === "PUT" && e.url.endsWith("/alerting/mode"));
      expect(put).toBeTruthy();
      expect(JSON.parse(String(put?.body))).toEqual({ mode: "on" });
    });
  });

  it("lists rules with severity/for/channels projections", async () => {
    renderCard("on");
    const card = await screen.findByTestId("alerting-status-card");
    await waitFor(() => expect(screen.getAllByTestId("alerting-rule-row").length).toBe(1));
    const text = card.textContent ?? "";
    expect(text).toContain("high-cpu");
    expect(text).toContain("cpu_used > 90");
    expect(text).toContain("for 300s");
    expect(text).toContain("severity: critical");
    expect(text).toContain("channels: ep-1");
  });

  it("creates a rule carrying for/severity/channels and evaluates the expr with Test", async () => {
    const log = renderCard("on");
    await screen.findByTestId("alerting-status-card");
    fireEvent.click(screen.getByRole("button", { name: "New rule" }));
    const form = await screen.findByTestId("alerting-rule-form");

    fireEvent.change(screen.getByTestId("alert-rule-name"), { target: { value: "vm-down" } });
    fireEvent.change(screen.getByTestId("alert-rule-expr"), { target: { value: "up == 0" } });
    fireEvent.change(screen.getByTestId("alert-rule-for"), { target: { value: "120" } });
    fireEvent.change(screen.getByTestId("alert-rule-severity"), { target: { value: "critical" } });
    // Test 即时求值（规则编写校验面）。
    fireEvent.click(screen.getByTestId("alerting-test-button"));
    await waitFor(() =>
      expect(screen.getByTestId("alerting-test-result").textContent).toContain("1 series"),
    );
    // 通道勾选（notifications 端点清单 → channels 投影）。
    fireEvent.click(screen.getByLabelText("ops-slack"));
    const testReq = log.find((e) => e.url.endsWith("/alerting/rules:test"));
    expect(testReq).toBeTruthy();
    expect(JSON.parse(String(testReq?.body))).toEqual({ expr: "up == 0" });

    fireEvent.click(screen.getByTestId("alert-rule-submit"));
    await waitFor(() => {
      const post = log.find((e) => e.method === "POST" && e.url.endsWith("/alerting/rules"));
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post?.body))).toEqual({
        name: "vm-down",
        expr: "up == 0",
        for_duration_seconds: 120,
        labels: { severity: "critical" },
        channels: ["ep-1"],
      });
    });
    void form;
  });

  it("removes a rule via DELETE /alerting/rules/{id}", async () => {
    const log = renderCard("on");
    await screen.findByTestId("alerting-status-card");
    await waitFor(() => expect(screen.getAllByTestId("alerting-rule-row").length).toBe(1));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => {
      const del = log.find((e) => e.method === "DELETE" && e.url.includes("/alerting/rules/"));
      expect(del).toBeTruthy();
      expect(del?.url).toContain("01ALERT");
    });
  });

  it("edits a rule: form prefilled from the row, PUT carries the fields (backlog #4-④)", async () => {
    const log = renderCard("on");
    await screen.findByTestId("alerting-status-card");
    await waitFor(() => expect(screen.getAllByTestId("alerting-rule-row").length).toBe(1));

    fireEvent.click(screen.getByTestId("alert-rule-edit"));
    await screen.findByTestId("alerting-rule-form");

    // 预填：name/expr/for/severity/通道勾选全部来自规则行投影。
    expect((screen.getByTestId("alert-rule-name") as HTMLInputElement).value).toBe("high-cpu");
    expect(
      (screen.getByTestId("alert-rule-expr") as HTMLTextAreaElement).value,
    ).toBe("cpu_used > 90");
    expect((screen.getByTestId("alert-rule-for") as HTMLInputElement).value).toBe("300");
    expect((screen.getByTestId("alert-rule-severity") as HTMLInputElement).value).toBe("critical");
    // 通道勾选预填（端点清单为异步查询——等勾选行渲染后断言勾选态）。
    await waitFor(() => expect(screen.getByLabelText("ops-slack")).toBeChecked());

    // 修改 expr 后保存 → PUT /alerting/rules/{id}（字段与创建载荷对齐）。
    fireEvent.change(screen.getByTestId("alert-rule-expr"), {
      target: { value: "up == 0" },
    });
    fireEvent.click(screen.getByTestId("alert-rule-submit"));
    await waitFor(() => {
      const put = log.find(
        (e) => e.method === "PUT" && e.url.endsWith("/alerting/rules/01ALERT"),
      );
      expect(put).toBeTruthy();
      expect(JSON.parse(String(put?.body))).toEqual({
        name: "high-cpu",
        expr: "up == 0",
        for_duration_seconds: 300,
        labels: { severity: "critical" },
        channels: ["ep-1"],
      });
    });
  });
});
