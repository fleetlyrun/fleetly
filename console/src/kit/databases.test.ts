import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { DATABASE_ENGINES, databasesSearch, fetchDatabases } from "./databases";

// 数据库域适配器单测（批1）：端点锚、protojson 行收敛、空 scope 不发请求、
// 快筛/引擎下拉的 URL 编解码往返。fetch 面 stub 在 global（fetchRows 走
// apiFetch 裸 fetch 管线）。

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

describe("fetchDatabases（端点 + 收敛 + 空 scope）", () => {
  it("按 project_id 整取 /v1/databases 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        databases: [{ id: "db1", name: "orders", engine: "postgres", status: "running" }, undefined],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchDatabases({ projectId: "shop" });

    expect(rows).toEqual([{ id: "db1", name: "orders", engine: "postgres", status: "running" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/databases?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（ListDatabases project_id 必填契约）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchDatabases({})).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("databasesSearch（URL 编解码契约）", () => {
  it("engine 下拉过滤编解码往返（filter=engine:eq:postgres 进 URL）", () => {
    const decoded = databasesSearch.decode({ filter: ["engine:eq:postgres"] });
    expect(decoded.predicate).toEqual({ field: "engine", op: "eq", value: "postgres" });
    const encoded = databasesSearch.encode({
      predicate: { field: "engine", op: "eq", value: "postgres" },
      window: { size: 20 },
    });
    expect(encoded.filter).toEqual(["engine:eq:postgres"]);
  });

  it("schema filters 值域 = 平台引擎枚举（FilterBar 下拉单源）", () => {
    const filter = databasesSearch.spec.filters?.[0];
    expect(filter?.field).toBe("engine");
    expect(filter?.options?.map((option) => option.value)).toEqual([...DATABASE_ENGINES]);
  });
});
