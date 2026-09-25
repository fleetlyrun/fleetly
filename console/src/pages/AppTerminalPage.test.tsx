// Web 终端页单测（E7 W5-S6）：mock WS（connectTerminal 注入假连接——不测
// 真实 PTY）+ mock xterm 视图（jsdom 无渲染面）。覆盖：开屏面板（服务选
// 择集/开屏状态行/平台状态）、回显与断开原因呈现（服务端 close 帧的
// reason 原文进状态行）、ticket 失败信封呈现（403/上限文案）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/client";
import { AppTerminalPage } from "@/pages/AppTerminalPage";

// xterm 视图整体 mock（jsdom 无 canvas 渲染面——验收分工：真实 PTY 行为
// 由 e2e 承载，此处只钉页面装配与状态机）。最新 props 落 harness——测试
// 经 onResize 驱动「视图行列落定 → conn.resize」的转发接线。
const viewHarness = vi.hoisted(() => ({
  props: null as null | { phase: string; onResize: (cols: number, rows: number) => void },
}));
vi.mock("@/terminal/app-terminal-view", () => ({
  AppTerminalView: (props: { phase: string; onResize: (cols: number, rows: number) => void }) => {
    viewHarness.props = props;
    return <div data-testid="terminal-session-view" data-phase={props.phase} />;
  },
}));

const COMPOSE = JSON.stringify({
  name: "demo",
  services: [
    { name: "web", image: "nginx:1.27" },
    { name: "jobber", cron: { expression: "*/5 * * * *" } },
  ],
});

// 假连接（connectTerminal 的替身）：测试经 handlers 驱动 onData/onClose。
type FakeHandlers = {
  onData: (data: Uint8Array, stderr: boolean) => void;
  onClose: (frame: { id: string; code: number; reason: string }) => void;
  onDisconnect: (reason: string) => void;
};
let fakeHandlers: FakeHandlers | null = null;
const connectCalls: { path: string }[] = [];
const resizeCalls: Array<[number, number]> = [];

vi.mock("@/api/terminal-ws", async () => {
  const actual = await vi.importActual<typeof import("@/api/terminal-ws")>("@/api/terminal-ws");
  return {
    ...actual,
    connectTerminal: (path: string, handlers: FakeHandlers) => {
      connectCalls.push({ path });
      fakeHandlers = handlers;
      return {
        write: () => {},
        resize: (cols: number, rows: number) => {
          resizeCalls.push([cols, rows]);
        },
        close: () => {},
      };
    },
  };
});

function stubFetch() {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/spec")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ compose: COMPOSE }),
      });
    }
    if (url.includes("/revisions")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ revisions: [{ id: "rev1", status: "active" }] }),
      });
    }
    if (url.includes("/terminal/status")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            enabled: true,
            relay_deployed: true,
            nodes_connected: 1,
            active_sessions: 0,
          }),
      });
    }
    if (url.endsWith("/terminal/tickets") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"));
      if (body.service === "web") {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () =>
            Promise.resolve({
              ticket: "tkt_abc",
              expires_at: "2026-09-22T00:01:00Z",
              websocket_path: "/v1/terminal?ticket=tkt_abc",
              expires_in_seconds: 60,
            }),
        });
      }
      // 非 web 服务（不可达/被拒的通用负路径）：退化信封 403。
      return Promise.resolve({
        ok: false,
        status: 403,
        statusText: "Forbidden",
        json: () => Promise.resolve({ message: "token scope insufficient (requires terminal)" }),
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
    if (url.includes("/apps/demo")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            id: "01APP",
            name: "demo",
            derived_state: "running",
            lifecycle: "active",
            created_at: "2026-09-21T00:00:00Z",
            updated_at: "2026-09-21T00:00:00Z",
          }),
      });
    }
    return Promise.resolve({
      ok: false,
      status: 404,
      statusText: "Not Found",
      json: () => Promise.resolve({ message: "not found" }),
    });
  });
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/apps/demo/terminal"]}>
        <Routes>
          <Route path="/apps/:name/terminal" element={<AppTerminalPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("AppTerminalPage", () => {
  beforeEach(() => {
    setToken("tok_test");
    fakeHandlers = null;
    connectCalls.length = 0;
    resizeCalls.length = 0;
    viewHarness.props = null;
    vi.stubGlobal("fetch", stubFetch());
  });

  it("renders the panel with long-running services only, the status line and the platform state", async () => {
    renderPage();
    const panel = await screen.findByTestId("terminal-panel");
    expect(panel).toBeInTheDocument();
    // cron 服务（jobber）不进选择集——只有 web。
    const select = await screen.findByTestId("terminal-service-select");
    expect(select.textContent).not.toContain("jobber");
    expect(screen.getByTestId("terminal-platform-state").textContent).toContain("1 node(s) connected");
    // protojson 省略零值字段——active_sessions=0 不在响应里也须显示 0
    //（回归：曾把 undefined 直接内插成 "undefined session(s)"）。
    expect(screen.getByTestId("terminal-platform-state").textContent).toContain("0 session(s)");
    expect(screen.getByTestId("terminal-status-line").textContent).toContain("idle");
  });

  it("opens a session: ticket → WS connect → close frame reason lands in the status line", async () => {
    renderPage();
    // 服务选择集就绪（useEffect 回填首项）后开终端按钮才可用。
    const open = await screen.findByTestId("terminal-open-button");
    await waitFor(() => expect(open).not.toBeDisabled());
    fireEvent.click(open);
    await waitFor(() => expect(connectCalls).toHaveLength(1));
    expect(connectCalls[0]!.path).toBe("/v1/terminal?ticket=tkt_abc");
    expect(screen.getByTestId("terminal-status-line").textContent).toContain("connected to web");
    // 视图行列落定 → conn.resize（PTY 侧行列同步接线；先等 active 渲染
    // 提交——conn 尚为 null 的旧闭包里 resize 是 no-op）。
    viewHarness.props!.onResize(120, 30);
    await waitFor(() => expect(resizeCalls).toContainEqual([120, 30]));
    // 服务端 close 帧：断线原因原文进状态行（不发明第二套文案）。
    fakeHandlers!.onClose({ id: "s1", code: 429, reason: "terminal session limit reached (2 per token, 8 platform-wide)" });
    await waitFor(() =>
      expect(screen.getByTestId("terminal-status-line").textContent).toContain(
        "terminal session limit reached",
      ),
    );
  });

  it("surfaces the ticket-failure envelope (terminal scope missing) in the status line", async () => {
    renderPage();
    const select = await screen.findByTestId("terminal-service-select");
    // 选择集首项是 web（正路径）；切到不存在 ticket 的形态需另一服务——
    // 这里只剩 cron 被过滤，故直接以负路径 fetch 分支驱动：改用键盘事件
    // 不可行时，退而断言「无第二服务可选」不成立时文案正确。取捷径：
    // 直接以 data 拒绝形态触发——POST 失败分支已按 service!=web 铺设，
    // 此处用 select 值改写（人工注入 option 后触发 change）。
    const option = document.createElement("option");
    option.value = "db";
    option.textContent = "db";
    select.appendChild(option);
    fireEvent.change(select, { target: { value: "db" } });
    fireEvent.click(screen.getByTestId("terminal-open-button"));
    await waitFor(() =>
      expect(screen.getByTestId("terminal-status-line").textContent).toContain(
        "token scope insufficient",
      ),
    );
    expect(connectCalls).toHaveLength(0);
  });
});
