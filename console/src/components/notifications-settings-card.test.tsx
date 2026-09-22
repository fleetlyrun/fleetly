// notifications 设置卡测试（E6 W5-S4，SystemPage Notifications 页签）：
// 端点列表（含终败红态）、创建一次性 secret（弹显 + 隐藏）、模式校验
// 错误信封、台账抽屉锚点。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { NotificationsSettingsCard } from "@/components/notifications-settings-card";
import { setToken } from "@/api/client";

const ENDPOINTS = {
  endpoints: [
    {
      id: "01EP",
      name: "ops",
      url: "https://ops.example.test/hook",
      event_patterns: ["deployment.*"],
      enabled: true,
      secret_fingerprint: "0123456789abcdef",
    },
    {
      id: "01DEAD",
      name: "dead-relay",
      url: "http://127.0.0.1:59991/hook",
      event_patterns: ["*"],
      enabled: true,
      secret_fingerprint: "ffffffffffffffff",
    },
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

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <NotificationsSettingsCard />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("NotificationsSettingsCard", () => {
  it("lists endpoints with fingerprint and enables creation round-trip", async () => {
    setToken("flt_test");
    const log: { url: string; method: string; body?: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        log.push({ url, method: init?.method ?? "GET", body: init?.body });
        if (url.includes("/notifications/endpoints") && (init?.method ?? "GET") === "GET") {
          return jsonResponse(ENDPOINTS);
        }
        if (url.includes("/notifications/deliveries")) return jsonResponse({ deliveries: [] });
        return jsonResponse({});
      }),
    );
    renderCard();
    await screen.findByTestId("notifications-card");
    await waitFor(() =>
      expect(screen.getByTestId("notifications-card").textContent).toContain("deployment.*"),
    );
    const text = screen.getByTestId("notifications-card").textContent ?? "";
    expect(text).toContain("ops");
    expect(text).toContain("deployment.*");
    expect(text).toContain("0123456789abcdef");
    expect(text).not.toContain("terminally failing");

    // 创建往返：POST /notifications/endpoints 带归一后的模式数组。
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "new-ops" } });
    fireEvent.change(screen.getByLabelText("Receiver URL"), {
      target: { value: "https://new.example.test/hook" },
    });
    fireEvent.change(screen.getByLabelText("Event patterns"), {
      target: { value: "deployment.*, cron.failed" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create endpoint" }));
    await waitFor(() => {
      const post = log.find((e) => e.method === "POST" && e.url.endsWith("/notifications/endpoints"));
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post?.body))).toEqual({
        name: "new-ops",
        url: "https://new.example.test/hook",
        event_patterns: ["deployment.*", "cron.failed"],
      });
    });
  });

  it("reveals the one-time secret after create and hides it on demand", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (url.includes("/notifications/endpoints") && method === "POST") {
          return jsonResponse({
            endpoint: {
              id: "01NEW",
              name: "new-ops",
              url: "https://new.example.test/hook",
              event_patterns: ["deployment.*"],
              enabled: true,
              secret_fingerprint: "abcdef0123456789",
            },
            secret: "ONCE-ONLY-SECRET-VALUE",
          });
        }
        if (url.includes("/notifications/deliveries")) return jsonResponse({ deliveries: [] });
        return jsonResponse(ENDPOINTS);
      }),
    );
    renderCard();
    await screen.findByTestId("notifications-card");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "new-ops" } });
    fireEvent.change(screen.getByLabelText("Receiver URL"), {
      target: { value: "https://new.example.test/hook" },
    });
    fireEvent.change(screen.getByLabelText("Event patterns"), {
      target: { value: "deployment.*" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create endpoint" }));
    const panel = await screen.findByTestId("webhook-secret-once");
    expect(panel.textContent).toContain("ONCE-ONLY-SECRET-VALUE");
    expect(panel.textContent).toContain("shown only once");
    // 「不再显示」：隐藏后明文从 DOM 消失（一次性语义的 UI 承诺）。
    fireEvent.click(screen.getAllByRole("button", { name: "Hide" }).at(-1)!);
    await waitFor(() =>
      expect(screen.queryByTestId("webhook-secret-once")).toBeNull(),
    );
  });

  it("marks terminally failing endpoints red and surfaces pattern validation errors", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (url.includes("/notifications/endpoints") && method === "GET") {
          return jsonResponse(ENDPOINTS);
        }
        if (url.includes("/notifications/deliveries")) {
          if (url.includes("status=failed")) {
            return jsonResponse({
              deliveries: [{ id: "01D", event_seq: 3, endpoint_id: "01DEAD", status: "failed", attempts: 3, last_error: "dial refused" }],
            });
          }
          return jsonResponse({ deliveries: [] });
        }
        if (url.endsWith("/notifications/endpoints") && method === "POST") {
          // 模式白名单违约 → E_WEBHOOK_PATTERN_INVALID 信封（422）。
          return jsonResponse(
            {
              code: "E_WEBHOOK_PATTERN_INVALID",
              message: 'webhook event pattern "BAD;drop" is invalid',
              suggestion: "Use event-name glob patterns like \"deployment.*\".",
            },
            422,
          );
        }
        return jsonResponse({});
      }),
    );
    renderCard();
    await screen.findByTestId("notifications-card");
    // 终败红态锚点（dead-relay 最近终态 = failed）。
    await waitFor(() =>
      expect(screen.getAllByTestId("webhook-endpoint-failed").length).toBeGreaterThan(0),
    );
    // 模式校验错误信封上卡。
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "bad" } });
    fireEvent.change(screen.getByLabelText("Receiver URL"), {
      target: { value: "https://x.example.test" },
    });
    fireEvent.change(screen.getByLabelText("Event patterns"), {
      target: { value: "BAD;drop" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create endpoint" }));
    await waitFor(() =>
      expect(screen.getByTestId("notifications-card").textContent).toContain(
        "E_WEBHOOK_PATTERN_INVALID",
      ),
    );
  });
});
