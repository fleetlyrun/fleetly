// 构建台账页测试（P1-6「Builds 全无 UI」）：空态/台账渲染（状态徽章、
// driver、时长、失败 code、成功 image_ref——契约里无触发来源与 commit
// sha 字段，对应列不渲染也无从断言）/行展开（构建日志经 source=build 检
// 索、queued 行不发日志请求）/进行中构建轮询刷新（2s 周期，终态停走）/台
// 账错误信封。页面纯读面（GetBuild/ListBuilds = read）→ 无角色门可测。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppBuildsPage } from "@/pages/AppBuildsPage";
import { setToken } from "@/api/client";

function ok(body: unknown) {
  return {
    ok: true,
    status: 200,
    statusText: "",
    json: () => Promise.resolve(body),
  };
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/apps/demo/builds"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps/:name/builds" element={<AppBuildsPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

const STARTED = new Date(Date.now() - 5 * 60_000).toISOString();
const FINISHED = new Date(Date.parse(STARTED) + 42_000).toISOString();

function buildFixture(overrides: Record<string, unknown> = {}) {
  return {
    id: "bld_01",
    app: "demo",
    service: "web",
    driver: "railpack",
    status: "succeeded",
    image_ref: "registry.local/demo/web:bld_01",
    image_digest: "sha256:abc123",
    error_code: "",
    started_at: STARTED,
    finished_at: FINISHED,
    ...overrides,
  };
}

function stubBuilds(builds: unknown[]) {
  return vi.fn().mockImplementation((url: string) => {
    const u = String(url);
    if (u.includes("/logs")) {
      return Promise.resolve(ok({ entries: [] }));
    }
    return Promise.resolve(ok({ builds }));
  });
}

describe("AppBuildsPage ledger", () => {
  it("renders the empty state", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubBuilds([]));
    renderPage();

    await waitFor(() =>
      expect(screen.getByText("No builds yet.")).toBeInTheDocument(),
    );
    expect(screen.queryByTestId("build-row")).not.toBeInTheDocument();
  });

  it("renders ledger rows with badge, driver, duration, failure code and image ref", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubBuilds([
        buildFixture(),
        buildFixture({
          id: "bld_02",
          status: "failed",
          error_code: "E_BUILD_FAILED",
          image_ref: "",
          image_digest: "",
        }),
      ]),
    );
    renderPage();

    await waitFor(() =>
      expect(screen.getAllByTestId("build-row")).toHaveLength(2),
    );
    // 状态徽章复用 deployment 徽章形态（data-state 锚点）。
    const states = screen
      .getAllByTestId("state-badge")
      .map((b) => b.getAttribute("data-state"));
    expect(states).toContain("succeeded");
    expect(states).toContain("failed");
    // 失败行的注册表错误码一等渲染；成功行渲染 image_ref。
    expect(screen.getByText("E_BUILD_FAILED")).toBeInTheDocument();
    expect(
      screen.getByText("registry.local/demo/web:bld_01"),
    ).toBeInTheDocument();
    // driver 与时长（started→finished = 42s）。
    expect(screen.getAllByText("railpack").length).toBe(2);
    expect(screen.getAllByText("42s").length).toBe(2);
  });

  it("expands a terminal row and fetches the build log via the source=build history face", async () => {
    setToken("flt_test");
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      const u = String(url);
      if (u.includes("/logs")) {
        return Promise.resolve(
          ok({
            entries: [
              {
                app: "demo",
                service: "web",
                at: STARTED,
                stderr: false,
                source: "build",
                line: "#1 [internal] load build definition",
              },
              {
                app: "demo",
                service: "web",
                at: STARTED,
                stderr: true,
                source: "build",
                line: "error: buildkit solve failed",
              },
            ],
          }),
        );
      }
      return Promise.resolve(ok({ builds: [buildFixture()] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPage();

    await waitFor(() => screen.getByTestId("build-row"));
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Details" }));

    await waitFor(() =>
      expect(screen.getByTestId("build-log")).toBeInTheDocument(),
    );
    expect(
      screen.getByText("#1 [internal] load build definition"),
    ).toBeInTheDocument();
    expect(screen.getByText("error: buildkit solve failed")).toBeInTheDocument();

    // 日志请求按契约走 ListHistoryLogs(source=build)，时间窗 = 构建起止。
    const logUrl = fetchMock.mock.calls
      .map((c) => String(c[0]))
      .find((u) => u.includes("/logs"));
    expect(logUrl).toBeDefined();
    const parsed = new URL(logUrl ?? "", "http://localhost");
    expect(parsed.pathname).toBe("/v1/apps/demo/logs");
    expect(parsed.searchParams.get("source")).toBe("build");
    expect(parsed.searchParams.get("service")).toBe("web");
    expect(parsed.searchParams.get("since")).toBe(STARTED);
    expect(parsed.searchParams.get("until")).toBe(FINISHED);
  });

  it("expands a queued row without requesting logs (build has not started)", async () => {
    setToken("flt_test");
    const fetchMock = stubBuilds([
      buildFixture({
        id: "bld_q",
        status: "queued",
        started_at: "",
        finished_at: "",
        image_ref: "",
        image_digest: "",
      }),
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderPage();

    await waitFor(() => screen.getByTestId("build-row"));
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Details" }));

    await waitFor(() =>
      expect(screen.getByTestId("build-log-empty")).toHaveTextContent(
        "the build has not started",
      ),
    );
    // queued 行无 started_at——不发日志请求（诚实口径：日志尚未落产物）。
    expect(
      fetchMock.mock.calls.some((c) => String(c[0]).includes("/logs")),
    ).toBe(false);
  });
});

describe("AppBuildsPage in-flight polling", () => {
  it("keeps polling a building row until it reaches a terminal state", async () => {
    setToken("flt_test");
    let call = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        const u = String(url);
        if (u.includes("/logs")) {
          return Promise.resolve(ok({ entries: [] }));
        }
        call++;
        if (call === 1) {
          return Promise.resolve(
            ok({
              builds: [
                buildFixture({ id: "bld_run", status: "building", finished_at: "" }),
              ],
            }),
          );
        }
        return Promise.resolve(
          ok({ builds: [buildFixture({ id: "bld_run" })] }),
        );
      }),
    );
    renderPage();

    // 首屏：building 行在案（非终态 → 2s 轮询）。
    await waitFor(() =>
      expect(screen.getByTestId("build-row")).toHaveAttribute(
        "data-status",
        "building",
      ),
    );
    // 轮询到下一次拉取（2s 周期 + 余量）→ 终态行替换，轮询停走。
    await waitFor(
      () =>
        expect(screen.getByTestId("build-row")).toHaveAttribute(
          "data-status",
          "succeeded",
        ),
      { timeout: 6000 },
    );
  });
});

describe("AppBuildsPage error envelope", () => {
  it("renders the error envelope when the ledger fetch fails", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() =>
        Promise.resolve({
          ok: false,
          status: 500,
          statusText: "",
          json: () =>
            Promise.resolve({
              code: "E_INTERNAL",
              message: "ledger unavailable",
              suggestion: "check daemon logs",
            }),
        }),
      ),
    );
    renderPage();

    await waitFor(() => screen.getByTestId("error-envelope"));
    const envelope = screen.getByTestId("error-envelope");
    expect(envelope).toHaveTextContent("E_INTERNAL");
    expect(envelope).toHaveTextContent("ledger unavailable");
    expect(envelope).toHaveTextContent("check daemon logs");
  });
});
