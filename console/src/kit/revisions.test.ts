import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { revisionsLookup, revisionsResource } from "./revisions";

// Revision 域适配器单测（批4；uikit 0.5.0 回流批改 restFetchAll 口径）：
// 端点锚（app_id + limit 200）、protojson 行收敛、scope 声明与存量键桥、
// lookup schema 的大页缺省。fetch 面 stub 在 global（authFetch→裸 fetch
// 管线）；断言经资源适配器调用取数。

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

const ALL = { window: { size: 200 } } as const;

describe("revisionsResource 读面（端点 + 收敛）", () => {
  it("GET /v1/revisions?app_id&limit=200 并展平 undefined 行（restFetchAll 信封过滤）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ revisions: [{ id: "r1", seq: "1" }, undefined, { id: "r2", seq: "2" }] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await revisionsResource.collection!(ALL, { projectId: "P1", appId: "A1" }, undefined)).rows;

    expect(rows).toEqual([{ id: "r1", seq: "1" }, { id: "r2", seq: "2" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/revisions?app_id=A1&limit=200");
  });

  it("scope 缺 appId 时零请求空段（守卫门——catalog enabled 门同语义）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const rows = (await revisionsResource.collection!(ALL, { projectId: "P1" }, undefined)).rows;
    expect(rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("非 2xx 收敛 kernel ApiError（工厂 Response→ApiError 单源）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(revisionsResource.collection!(ALL, { projectId: "P1", appId: "A1" }, undefined)).rejects.toMatchObject(
      { kind: "server", status: 503, message: "boom" },
    );
  });
});

describe("revisionsResource（scope/失效桥/lookup schema）", () => {
  it("scope 声明 projectId+appId；legacyKeys 桥零读者退役（F1 回流批审计）", () => {
    expect(revisionsResource.scope).toEqual(["projectId", "appId"]);
    expect(revisionsResource.legacyKeys).toBeUndefined();
  });

  it("revisionsLookup 缺省窗口 = 整取上限（join 读面窗口不截断行集）", () => {
    expect(revisionsLookup.decode({}).window.size).toBe(200);
  });
});
