import { act, render, renderHook, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ListToolbar, useClientPage } from "./list-toolbar";

// 列表页规范件锚（对齐批 5）：客户端分页的切片/钳制语义 + 工具栏主动作
// 槽的渲染位（用户复裁：创建钮钉工具栏行右侧，页头钮位退役）。

describe("useClientPage", () => {
  it("slices rows by page size and reports the page count", () => {
    const rows = Array.from({ length: 45 }, (_, index) => index);
    const { result } = renderHook(() => useClientPage(rows, 20));
    expect(result.current.pageCount).toBe(3);
    expect(result.current.page).toBe(0);
    expect(result.current.pageRows).toEqual(Array.from({ length: 20 }, (_, index) => index));
  });

  it("advances pages and clamps when the list shrinks under the cursor", () => {
    const rows = Array.from({ length: 45 }, (_, index) => index);
    const { result, rerender } = renderHook(({ list }: { list: number[] }) => useClientPage(list, 20), {
      initialProps: { list: rows },
    });
    act(() => result.current.setPage(2));
    expect(result.current.pageRows).toEqual([40, 41, 42, 43, 44]);
    rerender({ list: rows.slice(0, 25) });
    expect(result.current.page).toBe(1);
    expect(result.current.pageRows).toEqual([20, 21, 22, 23, 24]);
  });

  it("keeps one empty page for an empty list", () => {
    const { result } = renderHook(() => useClientPage([], 20));
    expect(result.current.pageCount).toBe(1);
    expect(result.current.pageRows).toEqual([]);
  });
});

describe("ListToolbar", () => {
  it("renders the counter and the actions slot", () => {
    render(
      <ListToolbar
        label="apps"
        value=""
        onChange={() => undefined}
        placeholder="Filter apps..."
        total={2}
        shown={1}
        actions={<button type="button">New app</button>}
      />,
    );
    expect(screen.getByText("1 of 2")).toBeTruthy();
    expect(screen.getByRole("button", { name: "New app" })).toBeTruthy();
    expect(screen.getByLabelText("Filter apps")).toBeTruthy();
  });
});
