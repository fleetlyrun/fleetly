import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { fetchSchedules, fetchTasks, tasksSearch } from "./tasks";

// 自动化域适配器单测（批1）：tasks/schedules 端点锚、protojson 行收敛、
// 空 scope 不发请求、双 tab 共享 schema 的 URL 编解码往返。fetch 面 stub
// 在 global（fetchRows 走 apiFetch 裸 fetch 管线）。

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("fetchTasks/fetchSchedules（端点 + 收敛 + 空 scope）", () => {
  it("按 project_id 整取 /v1/tasks 并展平 undefined 行（catalog rowsOf 口径：不滤缺字段行）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        tasks: [
          { id: "t1", name: "migrate", form: "one-shot", state: "active" },
          undefined,
          { name: "no-id" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchTasks({ projectId: "shop" });

    expect(rows).toEqual([
      { id: "t1", name: "migrate", form: "one-shot", state: "active" },
      { name: "no-id" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/tasks?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("schedules 打 /v1/schedules 同口径收敛", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ schedules: [{ id: "s1", name: "nightly", cron: "0 3 * * *" }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchSchedules({ projectId: "shop" });

    expect(rows).toEqual([{ id: "s1", name: "nightly", cron: "0 3 * * *" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/schedules?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（List project_id 必填契约）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchTasks({})).toEqual([]);
    expect(await fetchSchedules({ projectId: "" })).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("tasksSearch（URL 编解码契约，双 tab 共享实例）", () => {
  it("q 编码往返为并集字段集的 contains 谓词；decode 容忍缺省", () => {
    const fields = ["name", "id", "form", "state", "cron", "image"];
    const encoded = tasksSearch.encode({
      predicate: textPredicate(fields, "mig"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("mig");
    const decoded = tasksSearch.decode({ q: "mig" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "mig"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = tasksSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
