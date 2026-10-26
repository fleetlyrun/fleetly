import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchUploads, fetchVolumes, storageSearch } from "./storage";

// Storage 域资源适配单测：protojson 行收敛、空 scope 不发请求、URL 编解码
// 往返。fetch 面 stub 在 global（kit http 管线调用裸 fetch）。

afterEach(() => {
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("fetchVolumes / fetchUploads（收敛 + 作用域参数）", () => {
  it("整取 /v1/volumes?project_id= 并展平信封（undefined 行过滤，缺字段行保留——catalog 同口径）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        volumes: [
          { id: "v1", name: "data", pinned_node_id: "node-a", created_at: "2026-01-01T00:00:00Z" },
          undefined,
          { name: "no-id" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchVolumes({ projectId: "shop" });

    expect(rows).toEqual([
      { id: "v1", name: "data", pinned_node_id: "node-a", created_at: "2026-01-01T00:00:00Z" },
      { name: "no-id" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/volumes?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("uploads 走 /v1/uploads?project_id= 信封 uploads（与 catalog 同端点）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ uploads: [{ id: "u1", digest: "sha256:x", size_bytes: "2048" }] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchUploads({ projectId: "shop" });

    expect(rows).toEqual([{ id: "u1", digest: "sha256:x", size_bytes: "2048" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/uploads?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（ListVolumes project_id 必填契约）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchVolumes({})).toEqual([]);
    expect(await fetchUploads({ projectId: "" })).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("storageSearch（URL 编解码契约——双 tab 共享 name/id/digest 并集）", () => {
  it("q 编码往返为三字段 contains 谓词；decode 容忍缺省", () => {
    const encoded = storageSearch.encode({
      predicate: { any: [
        { field: "name", op: "contains", value: "web" },
        { field: "id", op: "contains", value: "web" },
        { field: "digest", op: "contains", value: "web" },
      ] },
      window: { size: 20 },
    });
    expect(encoded.q).toBe("web");
    const decoded = storageSearch.decode({ q: "web" });
    expect(decoded.predicate).toEqual({
      any: [
        { field: "name", op: "contains", value: "web" },
        { field: "id", op: "contains", value: "web" },
        { field: "digest", op: "contains", value: "web" },
      ],
    });
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = storageSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
