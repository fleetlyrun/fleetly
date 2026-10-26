import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchNetworkPeers, fetchNetworks, networksSearch } from "./networks";

// Networks 域资源适配单测：双资源端点/信封收敛、空 scope 不发请求、URL
// 编解码往返。fetch 面 stub 在 global（kit http 管线调用裸 fetch）。

afterEach(() => {
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("fetchNetworks / fetchNetworkPeers（收敛 + 作用域参数）", () => {
  it("networks 整取 /v1/networks?project_id= 信封 networks（undefined 行过滤）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        networks: [
          { id: "n1", name: "default", egress_none: false, created_at: "2026-01-01T00:00:00Z" },
          undefined,
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchNetworks({ projectId: "shop" });

    expect(rows).toEqual([{ id: "n1", name: "default", egress_none: false, created_at: "2026-01-01T00:00:00Z" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/networks?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("peers 走 /v1/networks/peers?project_id= 信封 peers（与 catalog 同端点）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        peers: [{ id: "p1", network_id: "n1", peer_project_id: "other", state: "pending" }],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchNetworkPeers({ projectId: "shop" });

    expect(rows).toEqual([{ id: "p1", network_id: "n1", peer_project_id: "other", state: "pending" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/networks/peers?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchNetworks({})).toEqual([]);
    expect(await fetchNetworkPeers({ projectId: "" })).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("networksSearch（URL 编解码契约——双 tab 共享 name/id）", () => {
  it("q 编码往返为双字段 contains 谓词；decode 容忍缺省", () => {
    const encoded = networksSearch.encode({
      predicate: { any: [
        { field: "name", op: "contains", value: "net" },
        { field: "id", op: "contains", value: "net" },
      ] },
      window: { size: 20 },
    });
    expect(encoded.q).toBe("net");
    const decoded = networksSearch.decode({ q: "net" });
    expect(decoded.predicate).toEqual({
      any: [
        { field: "name", op: "contains", value: "net" },
        { field: "id", op: "contains", value: "net" },
      ],
    });
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = networksSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
