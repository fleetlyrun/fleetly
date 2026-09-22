// 日志页检索态测试（W5-S2，E6 设计 §3.3）：两态切换（默认直播行为不变）、
// SearchLogs 调用形态（关键词/时间窗/来源 chips）、access 行结构化字段
// 展示、游标加载更多、VL 不可达/jsonl 模式的诚实错误态（引导 logs backend
// ——不冒充空结果）。
//
// mock 面：endpoints（searchLogs/历史回填/revision 清单）与 streams（跟随
// 流）整体打桩——与 AppLogsPage.test.tsx 同款骨架。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/api/errors";
import { AppLogsPage } from "@/pages/AppLogsPage";
import { setToken } from "@/api/client";

// jsdom 缺 radix Select 依赖的 pointer capture / scrollIntoView API
//（仅本文件注入）。
if (!Element.prototype.hasPointerCapture) {
  Element.prototype.hasPointerCapture = () => false;
  Element.prototype.releasePointerCapture = () => undefined;
  Element.prototype.setPointerCapture = () => undefined;
}
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => undefined;
}

const apiMocks = vi.hoisted(() => ({
  listRevisions: vi.fn(),
  getRevisionSpec: vi.fn(),
  listHistoryLogs: vi.fn(),
  searchLogs: vi.fn(),
  followLogs: vi.fn(),
}));

vi.mock("@/api/endpoints", () => ({
  listRevisions: apiMocks.listRevisions,
  getRevisionSpec: apiMocks.getRevisionSpec,
  listHistoryLogs: apiMocks.listHistoryLogs,
  searchLogs: apiMocks.searchLogs,
}));

vi.mock("@/api/streams", () => {
  class StreamError extends Error {
    status: number;
    constructor(status: number, _details: unknown) {
      super(`stream error ${status}`);
      this.status = status;
    }
  }
  return { StreamError, followLogs: apiMocks.followLogs };
});

function renderLogsPage(app = "foo") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[`/apps/${app}/logs`]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps/:name/logs" element={<AppLogsPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

/** 切到检索态（两态切换锚点）。 */
async function enterSearchMode(
  user: ReturnType<typeof userEvent.setup>,
) {
  await user.click(screen.getByTestId("logs-search-toggle"));
}

const ACCESS_ROW = {
  at: "2026-09-21T08:00:00Z",
  app: "foo",
  service: "web",
  source: "access",
  stderr: false,
  msg: "GET 200 app.example.com /api 12ms",
  fields: {
    method: "GET",
    status: "200",
    host: "app.example.com",
    route: "fleetly-foo-web-websecure@http",
    deployment_id: "dep-1",
  },
};

const CONTAINER_ROW = {
  at: "2026-09-21T08:00:01Z",
  app: "foo",
  service: "web",
  source: "container",
  stderr: false,
  msg: "boot-ok",
};

beforeEach(() => {
  vi.clearAllMocks();
  setToken("flt_test");
  apiMocks.listRevisions.mockResolvedValue({ revisions: [] });
  apiMocks.listHistoryLogs.mockResolvedValue({ entries: [] });
  apiMocks.followLogs.mockResolvedValue({ close: vi.fn() });
  apiMocks.searchLogs.mockResolvedValue({ rows: [], next_cursor: "" });
});

describe("AppLogsPage search mode (W5-S2)", () => {
  it("defaults to the live view and switches to search only on demand", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    renderLogsPage();

    // 默认直播：直播流在、检索面板不在——现状行为不变。
    expect(screen.getByTestId("log-stream")).toBeInTheDocument();
    expect(screen.queryByTestId("logs-search-input")).not.toBeInTheDocument();

    await user.click(screen.getByTestId("logs-search-toggle"));

    // 检索态：面板锚点齐备，直播流让位。
    expect(screen.getByTestId("logs-search-input")).toBeInTheDocument();
    expect(screen.getByTestId("logs-search-source-access")).toBeInTheDocument();
    expect(screen.getByTestId("logs-search-source-container")).toBeInTheDocument();
    expect(screen.getByTestId("logs-search-source-build")).toBeInTheDocument();
    expect(screen.getByTestId("logs-search-results")).toBeInTheDocument();
    expect(screen.queryByTestId("log-stream")).not.toBeInTheDocument();

    // 切回直播：行为如初。
    await user.click(screen.getByRole("button", { name: "Live" }));
    expect(screen.getByTestId("log-stream")).toBeInTheDocument();
    expect(screen.queryByTestId("logs-search-input")).not.toBeInTheDocument();
  });

  it("runs a SearchLogs query from keyword, window and source chips and renders access fields", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    apiMocks.searchLogs.mockResolvedValue({
      rows: [ACCESS_ROW, CONTAINER_ROW],
      next_cursor: "",
    });
    renderLogsPage();
    await enterSearchMode(user);

    await user.type(screen.getByTestId("logs-search-input"), "GET");
    // 来源 chips 多选：点 access 与 container（access 锚点即词表新面）。
    await user.click(screen.getByTestId("logs-search-source-access"));
    await user.click(screen.getByTestId("logs-search-source-container"));
    await user.click(screen.getByTestId("logs-search-submit"));

    await waitFor(() => expect(apiMocks.searchLogs).toHaveBeenCalled());
    const [app, opts] = apiMocks.searchLogs.mock.calls[0] as [string, {
      keyword?: string;
      sources?: string[];
      since?: string;
      limit?: number;
    }];
    expect(app).toBe("foo");
    expect(opts.keyword).toBe("GET");
    expect(opts.sources).toEqual(["access", "container"]);
    expect(opts.since).toBeTruthy();
    expect(Number.isFinite(Date.parse(opts.since ?? ""))).toBe(true);
    expect(opts.limit).toBe(200);

    // 结果渲染：时间倒序原文 + 服务/来源徽标 + access 行结构化字段。
    expect(await screen.findByTestId("logs-search-results")).toBeInTheDocument();
    expect(screen.getByText("GET 200 app.example.com /api 12ms")).toBeInTheDocument();
    expect(screen.getByText(/deployment_id=dep-1/)).toBeInTheDocument();
    expect(screen.getByText(/\[access\]/)).toBeInTheDocument();
    expect(screen.getByText("boot-ok")).toBeInTheDocument();
  });

  it("appends the next page when Load more is clicked with the server cursor", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    apiMocks.searchLogs
      .mockImplementationOnce(() =>
        Promise.resolve({
          rows: [ACCESS_ROW],
          next_cursor: "c2",
        }),
      )
      .mockImplementationOnce(() =>
        Promise.resolve({
          rows: [CONTAINER_ROW],
          next_cursor: "",
        }),
      );
    renderLogsPage();
    await enterSearchMode(user);
    await user.click(screen.getByTestId("logs-search-submit"));

    await waitFor(() =>
      expect(screen.getByText("GET 200 app.example.com /api 12ms")).toBeInTheDocument(),
    );
    await user.click(screen.getByTestId("logs-search-load-more"));

    await waitFor(() =>
      expect(apiMocks.searchLogs).toHaveBeenCalledTimes(2),
    );
    const [, secondOpts] = apiMocks.searchLogs.mock.calls[1] as [
      string,
      { cursor?: string },
    ];
    expect(secondOpts.cursor).toBe("c2");
    // 第二页追加（非替换）：两行同时在册。
    expect(await screen.findByText("boot-ok")).toBeInTheDocument();
    expect(screen.getByText("GET 200 app.example.com /api 12ms")).toBeInTheDocument();
    // 无游标 → Load more 收起。
    expect(screen.queryByTestId("logs-search-load-more")).not.toBeInTheDocument();
  });

  it("renders the honest backend-unavailable error instead of pretending an empty result", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    apiMocks.searchLogs.mockRejectedValue(
      new ApiError(503, {
        code: "E_LOGS_BACKEND_UNAVAILABLE",
        message: "log search is unavailable: the VictoriaLogs backend did not answer",
      }),
    );
    renderLogsPage();
    await enterSearchMode(user);
    await user.click(screen.getByTestId("logs-search-submit"));

    const errBox = await screen.findByTestId("logs-search-error");
    expect(errBox.textContent).toContain("E_LOGS_BACKEND_UNAVAILABLE");
    // 引导 `fleetly logs backend` 的诚实文案（设计 §3.1/§3.3：不冒充空结果）。
    expect(errBox.textContent).toContain("fleetly logs backend");
    // 错误态下结果区不渲染任何行（不把错误伪装成空命中）。
    expect(screen.queryByText("GET 200 app.example.com /api 12ms")).not.toBeInTheDocument();
  });
});
