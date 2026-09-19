// 日志页机制测试：跨应用导航不残留上一个应用的日志行（H5）、source
// 过滤是视图语义而非存储层丢弃（M8-3）——流进来的行始终入 state，
// 切换 source 只影响 visible 投影。
//
// mock 面：endpoints（历史回填/revision 清单）与 streams（跟随流）整体
// 打桩；跟随流 handlers 暴露给测试注入实时行，模拟 NDJSON 推送。
//
// 交互注意：本文件避免混用 userEvent 与 fireEvent——前一个用例的
// userEvent 指针模拟会干扰后续用例里 radix Select 的打开（jsdom 下
// 的指针捕获语义差异），故导航点击统一走 fireEvent。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LogEntryView } from "@/api/types";
import { AppLogsPage } from "@/pages/AppLogsPage";

// jsdom 缺 radix Select 依赖的 pointer capture / scrollIntoView API
// （仅本文件注入）。
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
  followLogs: vi.fn(),
}));

vi.mock("@/api/endpoints", () => ({
  listRevisions: apiMocks.listRevisions,
  getRevisionSpec: apiMocks.getRevisionSpec,
  listHistoryLogs: apiMocks.listHistoryLogs,
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

function entry(
  app: string,
  line: string,
  source: "container" | "build" = "container",
): LogEntryView {
  return {
    app,
    service: "web",
    at: "2026-01-01T00:00:00.000Z",
    stderr: false,
    line,
    source,
  };
}

type FollowHandlers = {
  onEntry: (entry: LogEntryView) => void;
  onEnd: (error?: unknown) => void;
};

// 最近一次 followLogs 调用的 handlers（模拟当前活跃连接）。
let liveHandlers: FollowHandlers | null = null;
let historyByApp: Record<string, LogEntryView[]>;

beforeEach(() => {
  vi.clearAllMocks();
  liveHandlers = null;
  historyByApp = {};
  apiMocks.listRevisions.mockResolvedValue({ revisions: [] });
  apiMocks.listHistoryLogs.mockImplementation((app: string) =>
    Promise.resolve({ entries: historyByApp[app] ?? [] }),
  );
  apiMocks.followLogs.mockImplementation(
    (
      _app: string,
      _service: string | undefined,
      handlers: FollowHandlers,
    ) => {
      liveHandlers = handlers;
      return Promise.resolve({ close: vi.fn() });
    },
  );
});

/** 导航按钮：模拟侧栏切换应用（同路由参数变化，组件实例被复用）。 */
function NavButton({ to }: { to: string }) {
  const navigate = useNavigate();
  return <button onClick={() => navigate(to)}>nav-{to}</button>;
}

function renderLogsPage(route: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[route]}>
      <QueryClientProvider client={client}>
        <NavButton to="/apps/bar/logs" />
        <Routes>
          <Route path="/apps/:name/logs" element={<AppLogsPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

/** 切换 Source 下拉（radix Select：打开 trigger 再点选项）。 */
async function selectSource(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
) {
  await user.click(screen.getByRole("combobox", { name: "Source" }));
  await user.click(screen.getByRole("option", { name: label }));
}

/** 向当前活跃跟随流注入一条实时行。 */
async function emitLive(e: LogEntryView) {
  expect(liveHandlers).toBeTruthy();
  await act(async () => {
    liveHandlers!.onEntry(e);
  });
}

describe("AppLogsPage cross-app state", () => {
  it("clears foo's entries when the route param changes foo → bar (H5)", async () => {
    historyByApp.foo = [entry("foo", "foo-line-1"), entry("foo", "foo-line-2")];
    historyByApp.bar = [entry("bar", "bar-line-1")];

    renderLogsPage("/apps/foo/logs");

    // foo 的历史回填可见。
    expect(await screen.findByText("foo-line-1")).toBeInTheDocument();
    expect(screen.getByText("foo-line-2")).toBeInTheDocument();

    // 参数级导航（同一组件实例复用）→ 只剩 bar 的行，foo 不残留。
    fireEvent.click(screen.getByText("nav-/apps/bar/logs"));

    expect(await screen.findByText("bar-line-1")).toBeInTheDocument();
    expect(screen.queryByText("foo-line-1")).not.toBeInTheDocument();
    expect(screen.queryByText("foo-line-2")).not.toBeInTheDocument();
  });
});

describe("AppLogsPage source filtering", () => {
  it("keeps stream lines in storage and filters by source only at the view layer (M8-3)", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    renderLogsPage("/apps/foo/logs");
    await waitFor(() => expect(apiMocks.followLogs).toHaveBeenCalled());

    // source=all：container 与 build 行都可见（均已入 state）。
    await emitLive(entry("foo", "c-line-1", "container"));
    expect(screen.getByText("c-line-1")).toBeInTheDocument();
    await emitLive(entry("foo", "b-line-1", "build"));
    expect(screen.getByText("b-line-1")).toBeInTheDocument();

    // 切 source=container：build 行从视图消失，container 行仍在。
    await selectSource(user, "container");
    expect(screen.queryByText("b-line-1")).not.toBeInTheDocument();
    expect(screen.getByText("c-line-1")).toBeInTheDocument();

    // 重开跟随流（pause → resume 重跑流 effect）：即便流的闭包在
    // source=container 时建立，之后推来的行也必须入 state 而非被
    // 存储层按旧 source 丢弃。
    await user.click(screen.getByRole("button", { name: "Pause follow" }));
    await user.click(screen.getByRole("button", { name: "Resume follow" }));
    await waitFor(() =>
      expect(apiMocks.followLogs).toHaveBeenCalledTimes(2),
    );
    await emitLive(entry("foo", "c-line-2", "container"));
    expect(screen.getByText("c-line-2")).toBeInTheDocument();
    await emitLive(entry("foo", "b-line-2", "build"));
    expect(screen.queryByText("b-line-2")).not.toBeInTheDocument();

    // 切回 source=all：此前被视图过滤的 build 行重新出现——证明它们
    // 一直在存储里（修复前：闭包里的旧 source 在存储层永久吞行）。
    await selectSource(user, "All");
    expect(await screen.findByText("b-line-1")).toBeInTheDocument();
    expect(screen.getByText("b-line-2")).toBeInTheDocument();
  });
});
