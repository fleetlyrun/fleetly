// notifications 设置卡测试（E6 W5-S4，SystemPage Notifications 页签；W4-S3
// 通道扩展）：端点列表（含终败红态）、创建一次性 secret（弹显 + 隐藏）、
// 模式校验错误信封、台账抽屉锚点、通道类型选择（email → target 输入 + POST
// 形态）、SMTP 设置卡（指纹读面 + PUT 保存往返 + 测试邮件探针）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { NotificationsSettingsCard, SmtpSettingsCard } from "@/components/notifications-settings-card";
import { setToken } from "@/api/client";

// jsdom 缺 radix Select 依赖的 pointer capture / scrollIntoView API
//（s3-settings-card.test.tsx 同款注入）。
if (!Element.prototype.hasPointerCapture) {
  Element.prototype.hasPointerCapture = () => false;
  Element.prototype.releasePointerCapture = () => undefined;
  Element.prototype.setPointerCapture = () => undefined;
}
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => undefined;
}

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

/** /auth/me 包装（useIsPlatformAdmin 生产接线）：写面门（端点 CRUD/Test/
 * SMTP 保存 = 平台管理员专属，2026-09-25 走查）测试用；其余请求透传。 */
function withMe(
  isPlatformAdmin: boolean,
  inner: (input: RequestInfo | URL, init?: RequestInit) => Promise<unknown>,
) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/auth/me")) {
      return jsonResponse({
        user: { id: "01U1", email: "f@t.test", is_platform_admin: isPlatformAdmin },
        teams: [
          { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: "owner" },
        ],
        project_overrides: [],
      });
    }
    return inner(input, init);
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
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        log.push({ url, method: init?.method ?? "GET", body: init?.body });
        if (url.includes("/notifications/endpoints") && (init?.method ?? "GET") === "GET") {
          return jsonResponse(ENDPOINTS);
        }
        if (url.includes("/notifications/deliveries")) return jsonResponse({ deliveries: [] });
        return jsonResponse({});
      });
    vi.stubGlobal("fetch", withMe(true, stubInner));
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
        type: "webhook",
        target: "",
        event_patterns: ["deployment.*", "cron.failed"],
      });
    });
  });

  it("reveals the one-time secret after create and hides it on demand", async () => {
    setToken("flt_test");
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
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
      });
    vi.stubGlobal("fetch", withMe(true, stubInner));
    renderCard();
    await screen.findByTestId("notifications-card");
    // Me 投影解析后创建表单才渲染——先等表单出现再交互。
    await screen.findByTestId("webhook-create-form");
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
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
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
      });
    vi.stubGlobal("fetch", withMe(true, stubInner));
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

  it("creates an email endpoint through the channel select (target input, empty url)", async () => {
    setToken("flt_test");
    const log: { url: string; method: string; body?: unknown }[] = [];
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        log.push({ url, method: init?.method ?? "GET", body: init?.body });
        if (url.includes("/notifications/endpoints") && (init?.method ?? "GET") === "GET") {
          return jsonResponse(ENDPOINTS);
        }
        if (url.includes("/notifications/deliveries")) return jsonResponse({ deliveries: [] });
        return jsonResponse({});
      });
    vi.stubGlobal("fetch", withMe(true, stubInner));
    renderCard();
    await screen.findByTestId("notifications-card");
    // Me 投影解析后创建表单才渲染——先等表单出现再交互。
    await screen.findByTestId("webhook-create-form");
    // Radix Select 交互纪律（s3-settings-card.test.tsx 同款）：jsdom 缺
    // pointer capture API，用 keyDown(ArrowDown) 开启 + 点选 option。
    fireEvent.keyDown(screen.getByTestId("webhook-channel-select"), { key: "ArrowDown" });
    const option = await screen.findByRole("option", { name: /email \(SMTP via platform settings\)/ });
    fireEvent.click(option);
    await screen.findByLabelText("Recipient mailbox");
    expect(screen.queryByLabelText("Receiver URL")).toBeNull();

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "mail-ops" } });
    fireEvent.change(screen.getByLabelText("Recipient mailbox"), {
      target: { value: "ops@example.test" },
    });
    fireEvent.change(screen.getByLabelText("Event patterns"), {
      target: { value: "cron.*" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create endpoint" }));
    await waitFor(() => {
      const post = log.find((e) => e.method === "POST" && e.url.endsWith("/notifications/endpoints"));
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post?.body))).toEqual({
        name: "mail-ops",
        url: "",
        type: "email",
        target: "ops@example.test",
        event_patterns: ["cron.*"],
      });
    });
  });
});

describe("SmtpSettingsCard", () => {
  it("shows the stored fingerprint (never plaintext), saves via PUT and probes a test mail", async () => {
    setToken("flt_test");
    const log: { url: string; method: string; body?: unknown }[] = [];
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      log.push({ url, method: init?.method ?? "GET", body: init?.body });
      if (url.endsWith("/notifications/smtp") && (init?.method ?? "GET") === "GET") {
        return jsonResponse({
          settings: {
            host: "smtp.example.test",
            port: 587,
            username: "relay-user",
            password_fingerprint: "0123456789abcdef",
            from: "fleetly@example.test",
          },
        });
      }
      if (url.endsWith("/notifications/smtp/test")) {
        return jsonResponse({ ok: true, status_code: 250 });
      }
      return jsonResponse({});
      });
    vi.stubGlobal("fetch", withMe(true, stubInner));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <SmtpSettingsCard />
      </QueryClientProvider>,
    );
    const card = await screen.findByTestId("notifications-smtp-card");
    // 表单以已存设置预填 + 指纹展示（明文永不回读）。
    await waitFor(() => {
      expect((screen.getByLabelText("Relay host") as HTMLInputElement).value).toBe("smtp.example.test");
    });
    expect(card.textContent).toContain("0123456789abcdef");

    // PUT 保存往返：改 host + 提供新密码（空口令 = 清除的语义在服务端面）。
    fireEvent.change(screen.getByLabelText("Relay host"), { target: { value: "smtp2.example.test" } });
    fireEvent.change(screen.getByLabelText("Password (write-only)"), {
      target: { value: "brand-new-pw" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save SMTP settings" }));
    await waitFor(() => {
      const put = log.find((e) => e.method === "PUT" && e.url.endsWith("/notifications/smtp"));
      expect(put).toBeTruthy();
      expect(JSON.parse(String(put?.body))).toEqual({
        host: "smtp2.example.test",
        port: 587,
        username: "relay-user",
        password: "brand-new-pw",
        from: "fleetly@example.test",
      });
    });

    // 测试邮件探针：POST /notifications/smtp/test 带 to。
    fireEvent.change(screen.getByPlaceholderText("you@example.test"), {
      target: { value: "me@example.test" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    await waitFor(() => {
      const post = log.find((e) => e.method === "POST" && e.url.endsWith("/notifications/smtp/test"));
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post?.body))).toEqual({ to: "me@example.test" });
    });
    await waitFor(() =>
      expect(screen.getByTestId("smtp-test-result").textContent).toContain("accepted by the relay"),
    );
  });

  it("hides the saved timestamp when updated_at is the epoch zero value (2026-09-25 walkthrough)", async () => {
    setToken("flt_test");
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/notifications/smtp") && (init?.method ?? "GET") === "GET") {
        // proto 零值 Timestamp 的 JSON 形态（epoch）——未保存过设置时的实况。
        return jsonResponse({
          settings: { host: "", port: 0, updated_at: "1970-01-01T00:00:00Z" },
        });
      }
      return jsonResponse({});
      });
    vi.stubGlobal("fetch", withMe(true, stubInner));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <SmtpSettingsCard />
      </QueryClientProvider>,
    );
    // Me 投影解析后表单卡才渲染（此前只读说明短暂替代——先等表单出现）。
    await screen.findByLabelText("Relay host");
    // 零值守卫（对齐 ACME 卡同类修复）：epoch 不渲染 "saved 1970/…"。
    const desc = screen.getByText(/one platform-wide configuration/);
    expect(desc.textContent).not.toContain("saved");
    expect(desc.textContent).not.toContain("1970");
  });

  it("viewer（写面门，2026-09-25 走查）：SMTP 卡整卡替换为只读说明，不发 GET /notifications/smtp", async () => {
    setToken("flt_test");
    const log: { url: string; method: string }[] = [];
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      log.push({ url, method: init?.method ?? "GET" });
      return jsonResponse({});
      });
    vi.stubGlobal("fetch", withMe(false, stubInner));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <SmtpSettingsCard />
      </QueryClientProvider>,
    );
    const note = await screen.findByTestId("smtp-settings-readonly-note");
    expect(note).toHaveTextContent("Platform administrator required.");
    // 整卡替换：表单/保存/探针一概不渲染。
    expect(screen.queryByLabelText("Relay host")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save SMTP settings" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Test" })).not.toBeInTheDocument();
    // 读面即 admin scope：不发 GET /notifications/smtp（enabled:false）。
    expect(log.some((e) => e.url.includes("/notifications/smtp"))).toBe(false);
  });
});

describe("NotificationsSettingsCard viewer write-face gate (2026-09-25 walkthrough)", () => {
  it("viewer：端点清单与台账照常，创建表单换只读说明，行写钮隐藏、Deliveries 保留", async () => {
    setToken("flt_test");
    const log: { url: string; method: string }[] = [];
    const stubInner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      log.push({ url, method: init?.method ?? "GET" });
      if (url.includes("/notifications/endpoints") && (init?.method ?? "GET") === "GET") {
        return jsonResponse(ENDPOINTS);
      }
      if (url.includes("/notifications/deliveries")) return jsonResponse({ deliveries: [] });
      return jsonResponse({});
      });
    vi.stubGlobal("fetch", withMe(false, stubInner));

    renderCard();

    const note = await screen.findByTestId("webhook-create-readonly-note");
    expect(note).toHaveTextContent("Platform administrator required.");
    // 读面保留：清单行照常（异步到达）；Deliveries（read scope）照常（两行各一个）。
    await screen.findByText("ops");
    expect(screen.getAllByRole("button", { name: "Deliveries" }).length).toBe(2);

    // 写面收口：行写钮（Enable/Test/Rotate/Delete）隐藏。
    expect(screen.queryByTestId("webhook-create-form")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Enable" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disable" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Test" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rotate secret" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
    // 零写请求面。
    expect(log.some((e) => e.method !== "GET" && e.url.includes("/notifications"))).toBe(false);
  });
});
