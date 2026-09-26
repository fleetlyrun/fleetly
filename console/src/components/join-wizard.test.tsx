// join 向导测试（E1-8 卡片 + backlog #4-③ 轮换）：Rotate join token 两步
// 确认（对话框先行、未确认不发请求）、POST /system/nodes/join-token:rotate
// 载荷（role=worker）、成功后已生成指引自动重取显示新 token。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { JoinWizard } from "@/components/join-wizard";
import { setToken } from "@/api/client";

function jsonResponse(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    statusText: String(status),
    json: () => Promise.resolve(body),
  });
}

/** /auth/me 包装（useIsPlatformAdmin 生产接线）：isPlatformAdmin 开关 +
 * 其余请求透传（写面门测试用——join 面整体 admin scope，2026-09-25 走查）。 */
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

describe("JoinWizard rotate join token (backlog #4-③)", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    setToken("flt_test");
  });

  it("两步确认后 POST rotate（role=worker），已生成指引自动刷新新 token", async () => {
    // 首次 Generate 返回 T1；rotate 后的重取返回 T2（stub 按调用序切换）。
    let guideCalls = 0;
    const log: { url: string; method: string; body?: unknown }[] = [];
    const inner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      log.push({ url, method: init?.method ?? "GET", body: init?.body });
      if (url.includes("/system/nodes/join-guide")) {
        guideCalls += 1;
        return jsonResponse({
          guide: {
            join_command: `docker swarm join --token T${guideCalls} 10.0.0.1:2377`,
            worker_token: `T${guideCalls}`,
            manager_firewall_rules: [],
            worker_firewall_rules: [],
            dns_steps: [],
            completion_checks: [],
          },
        });
      }
      if (url.includes("/system/nodes/join-token:rotate")) {
        return jsonResponse({ role: "worker", token: "T2" });
      }
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", withMe(true, inner));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <JoinWizard />
      </QueryClientProvider>,
    );

    // 先生成指引（token T1）。（Me 投影解析后写面 UI 才渲染——先等再点。）
    fireEvent.click(
      await screen.findByRole("button", { name: "Generate join guide" }),
    );
    await waitFor(() =>
      expect(screen.getByTestId("join-wizard-token").textContent).toBe("T1"),
    );

    // 两步确认：点击 Rotate 先出对话框，未确认前不发 POST。
    fireEvent.click(screen.getByTestId("join-token-rotate"));
    expect(screen.getByTestId("join-token-rotate-dialog")).toBeInTheDocument();
    expect(
      log.some((e) => e.method === "POST" && e.url.includes("join-token:rotate")),
    ).toBe(false);

    fireEvent.click(screen.getByTestId("join-token-rotate-submit"));

    // POST 载荷（role 固定 worker）+ 指引重取显示新 token。
    await waitFor(() => {
      const post = log.find(
        (e) => e.method === "POST" && e.url.includes("join-token:rotate"),
      );
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post?.body))).toEqual({ role: "worker" });
    });
    await waitFor(() =>
      expect(screen.getByTestId("join-wizard-token").textContent).toBe("T2"),
    );
    expect(guideCalls).toBe(2);
  });

  it("rotate 失败：信封原样呈现于对话框内（不下架确认面）", async () => {
    const log: { url: string; method: string }[] = [];
    const inner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      log.push({ url, method: init?.method ?? "GET" });
      if (url.includes("/system/nodes/join-token:rotate")) {
        return Promise.resolve({
          ok: false,
          status: 403,
          statusText: "Forbidden",
          json: () =>
            Promise.resolve({
              code: "E_PLATFORM_ADMIN_REQUIRED",
              message: "this action requires a platform administrator",
            }),
        });
      }
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", withMe(true, inner));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <JoinWizard />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByTestId("join-token-rotate"));
    fireEvent.click(screen.getByTestId("join-token-rotate-submit"));
    await waitFor(() =>
      expect(screen.getByTestId("join-token-rotate-dialog").textContent).toContain(
        "E_PLATFORM_ADMIN_REQUIRED",
      ),
    );
  });

  it("viewer（写面门，2026-09-25 走查）：只读说明替代向导，无 Generate/Rotate 假按钮", async () => {
    const inner = vi.fn().mockImplementation((input: RequestInfo | URL) => {
      void input;
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", withMe(false, inner));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <JoinWizard />
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId("join-wizard-readonly-note")).toHaveTextContent(
      "Platform administrator required.",
    );
    expect(screen.queryByRole("button", { name: "Generate join guide" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("join-token-rotate")).not.toBeInTheDocument();
    // 零静默探测：只读态不应对 join 面发任何请求。
    expect(inner.mock.calls.some((c) => String(c[0]).includes("/system/nodes"))).toBe(false);
  });
});
