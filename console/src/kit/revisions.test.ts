import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { fetchRevisions, revisionsResource, revisionsLookup } from "./revisions";

// Revision 域适配器单测（批4）：端点锚（app_id + limit 200）、protojson 行
// 收敛、scope 声明与存量键桥、lookup schema 的大页缺省。fetch 面 stub 在
// global（fetchRows 走 apiFetch 裸 fetch 管线）。

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

describe("fetchRevisions（端点 + 收敛）", () => {
  it("GET /v1/revisions?app_id&limit=200 并展平 undefined 行（catalog rowsOf 口径）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ revisions: [{ id: "r1", seq: "1" }, undefined, { id: "r2", seq: "2" }] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchRevisions({ projectId: "P1", appId: "A1" });

    expect(rows).toEqual([{ id: "r1", seq: "1" }, { id: "r2", seq: "2" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/revisions?app_id=A1&limit=200");
  });

  it("scope 缺 appId 时零请求空段（catalog enabled 门同语义）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const rows = await fetchRevisions({ projectId: "P1" });
    expect(rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("非 2xx 走统一错误信封（code/message 抛出）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(fetchRevisions({ projectId: "P1", appId: "A1" })).rejects.toThrowError(/boom/);
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
