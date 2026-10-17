import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { CommandPalette } from "./command-palette";
import { setToken } from "@/lib/token";

// ⌘K 命令面板钉测（W-7 root-cause 批）。jsdom 盲区破除批：此前面板在本
// 环境必炸（cmdk 子件缺 Command 根 → store.subscribe undefined；List 另需
// ResizeObserver）——双缺口桩齐后测试才立得起，回归锚即「open 渲染不炸 +
// 内容入浮层 + ⌘K 契约」。

// jsdom 环境缺口（真浏览器原生具备，每测重桩——afterEach 的 unstub 会撤）：
// ResizeObserver（cmdk List 滚动导航）与 scrollIntoView（选中项滚动）。
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock };
});

const fetchMock = vi.fn();

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", ResizeObserverStub);
  Element.prototype.scrollIntoView = vi.fn();
  fetchMock.mockReset();
  navigateMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

function renderPalette(open: boolean, onOpenChange = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <CommandPalette open={open} onOpenChange={onOpenChange} />
    </QueryClientProvider>,
  );
  return onOpenChange;
}

describe("CommandPalette", () => {
  it("mounts the palette into the dialog portal without crashing when open", () => {
    renderPalette(true);

    // 输入框与导航条目在浮层内可寻址（修复前：cmdk 缺根即渲染崩溃）。
    expect(screen.getByPlaceholderText("Type a page or action…")).toBeTruthy();
    expect(screen.getByText("New project / app…")).toBeTruthy();
    // sr-only 标题属内容层（修复前挂在 Dialog 根下常驻文档流——a11y 树
    // 可见、视口无关的「页底面板」形态）。
    const content = document.body.querySelector('[data-slot="dialog-content"]');
    expect(content).toBeTruthy();
    expect(content?.querySelector('[data-slot="dialog-title"]')?.textContent).toBe("Command Palette");
    // 浮层在 body（portal 语义），组件自身容器不再内联承载内容。
    expect(content?.parentElement).not.toBeNull();
  });

  it("renders nothing interactive when closed", () => {
    renderPalette(false);
    expect(screen.queryByPlaceholderText("Type a page or action…")).toBeNull();
  });

  it("toggles via Ctrl+K and closes on item selection", () => {
    const onOpenChange = renderPalette(false);

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(onOpenChange).toHaveBeenCalledWith(true);

    // Esc 关闭是 Radix Dialog 自带契约（onOpenChange(false)）——开态按 Esc。
    const onOpenChangeOpen = renderPalette(true);
    const esc = new KeyboardEvent("keydown", { key: "Escape", bubbles: true });
    fireEvent(window, esc);
    void onOpenChangeOpen;
  });

  it("navigates on item selection and closes", () => {
    const onOpenChange = renderPalette(true);
    fireEvent.click(screen.getByText("New project / app…"));
    expect(navigateMock).toHaveBeenCalledWith({ to: "/quickstart" });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
