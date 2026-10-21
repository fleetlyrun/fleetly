import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { NewScheduleButton } from "./panels";
import { TimezoneCombobox, detectedTimezone } from "./timezone-combobox";
import { setToken } from "@/lib/token";

// 时区可搜索下拉钉测：候选来自 Intl.supportedValuesOf（jsdom/Node full-icu
// 原生可用，不打桩的真路径）+ 降级分支（stub 掉 API）+ NewScheduleButton
// 集成（默认本地时区入提交体）。cmdk 的 jsdom 盲区（ResizeObserver/
// scrollIntoView）沿用 command-palette.test 的双桩。

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const fetchMock = vi.fn();

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", ResizeObserverStub);
  Element.prototype.scrollIntoView = vi.fn();
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

// vitest 未开 globals：testing-library 自动 cleanup 不生效，显式卸载。
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  };
}

function renderCombobox(value: string, onChange = vi.fn()) {
  const { wrapper } = withClient();
  render(<TimezoneCombobox value={value} onChange={onChange} />, { wrapper });
  return onChange;
}

function openPopover() {
  fireEvent.click(screen.getByRole("button", { name: "Timezone" }));
  return screen.getByPlaceholderText("Search timezone…");
}

describe("TimezoneCombobox", () => {
  it("shows the current value in the trigger", () => {
    renderCombobox("Asia/Shanghai");
    // accessible name 固定为 aria-label "Timezone"，当前值断 textContent。
    expect(screen.getByRole("button", { name: "Timezone" }).textContent).toContain("Asia/Shanghai");
  });

  it("filters the list as you type", () => {
    renderCombobox("UTC");
    const input = openPopover();

    fireEvent.change(input, { target: { value: "Shanghai" } });
    expect(screen.getByText("Asia/Shanghai")).toBeTruthy();
    expect(screen.queryByText("Europe/Berlin")).toBeNull();
  });

  it("selects a timezone, reports it, and closes", () => {
    const onChange = renderCombobox("UTC");
    openPopover();

    fireEvent.click(screen.getByText("Asia/Tokyo"));
    expect(onChange).toHaveBeenCalledWith("Asia/Tokyo");
    // 选择即收口：搜索框随浮层卸载。
    waitFor(() => expect(screen.queryByPlaceholderText("Search timezone…")).toBeNull());
  });

  it("pins the detected timezone in its own group", () => {
    renderCombobox("UTC");
    openPopover();

    const detected = detectedTimezone();
    const group = screen.getByText("Detected").closest("[cmdk-group]");
    expect(group?.textContent).toContain(detected);
  });

  it("falls back to a plain input when Intl.supportedValuesOf is unavailable", () => {
    // 降级面：老浏览器无 API——受控文本框（行为同文本框时代，服务端校验兜底）。
    vi.stubGlobal("Intl", { DateTimeFormat: Intl.DateTimeFormat });
    const onChange = vi.fn();
    const { wrapper } = withClient();
    render(<TimezoneCombobox value="UTC" onChange={onChange} />, { wrapper });

    const input = screen.getByLabelText("Timezone");
    fireEvent.change(input, { target: { value: "Asia/Tacloban" } });
    expect(onChange).toHaveBeenCalledWith("Asia/Tacloban");
  });
});

describe("detectedTimezone", () => {
  it("returns the browser timezone when available", () => {
    // jsdom 环境 resolvedOptions 可用——断言非空即可（不钉具体值，随测试机漂移）。
    expect(detectedTimezone()).not.toBe("");
  });

  it("falls back to UTC when detection throws or yields empty", () => {
    vi.stubGlobal("Intl", { DateTimeFormat: () => { throw new Error("boom"); } });
    expect(detectedTimezone()).toBe("UTC");

    vi.stubGlobal("Intl", { DateTimeFormat: () => ({ resolvedOptions: () => ({ timeZone: "" }) }) });
    expect(detectedTimezone()).toBe("UTC");
  });
});

describe("NewScheduleButton timezone default", () => {
  it("defaults to the detected timezone and submits it in the create body", async () => {
    fetchMock.mockImplementation(async () =>
      new Response(JSON.stringify({ schedule: { id: "sched-1", cron: "0 3 * * *", timezone: detectedTimezone() } }), { status: 200 }),
    );
    const { wrapper } = withClient();
    render(<NewScheduleButton projectId="P1" />, { wrapper });

    fireEvent.click(screen.getByRole("button", { name: /New schedule/ }));
    // 触发器承载默认值：浏览器检测的本地时区（accessible name 固定为
    // aria-label "Timezone"，值断 textContent）。
    const trigger = await screen.findByRole("button", { name: "Timezone" });
    expect(trigger.textContent).toContain(detectedTimezone());

    fireEvent.change(screen.getByLabelText("Image"), { target: { value: "ghcr.io/example/backup:latest" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/v1/schedules", expect.anything()));
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(JSON.parse(String(init.body)).timezone).toBe(detectedTimezone());
  });
});
