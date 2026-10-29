import { afterEach, describe, expect, it, vi } from "vitest";
import { runCommand } from "@lynx-go/uikit-core/kernel";
import {
  approvePeerDescriptor,
  createNetworkCommand,
  declarePeerDescriptor,
  fetchNetworkPeers,
  fetchNetworks,
  networkPeersResource,
  networksResource,
  networksSearch,
  peersSearch,
  rebuildNetworkDescriptor,
  revokePeerDescriptor,
} from "./networks";

// Networks 域资源适配单测：双资源端点/信封收敛、空 scope 不发请求、URL
// 编解码往返；批 2 命令族锚（提交形收敛、结果两分、裸 descriptor 动词
// 面、失效桥键清单）。fetch 面 stub 在 global（kit http 管线调用裸 fetch）。

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

describe("networksSearch/peersSearch（URL 编解码契约——双 namespace）", () => {
  it("q 编码往返为双字段 contains 谓词；decode 容忍缺省", () => {
    const encoded = networksSearch.encode({
      predicate: { any: [
        { field: "name", op: "contains", value: "net" },
        { field: "id", op: "contains", value: "net" },
      ] },
      window: { size: 20 },
    });
    // namespace 物理键（F1 回流批）：networks.q；peers 键不携带
    expect(encoded["networks.q"]).toBe("net");
    const decoded = networksSearch.decode({ "networks.q": "net" });
    expect(peersSearch.decode({ "networks.q": "net" }).predicate).toBeUndefined();
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
    expect("networks.q" in patch).toBe(true);
    expect(patch["networks.q"]).toBeUndefined();
    expect(patch["networks.page"]).toBeUndefined();
  });
});

describe("networks 命令族（批 2 写路径命令化）", () => {
  it("create：POST /v1/networks 提交形收敛（egressNone 未勾选 = egress_none 不发送）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    const result = await runCommand(createNetworkCommand, { name: "net-a", egressNone: false }, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: null });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/networks");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ project_id: "shop", name: "net-a" });
  });

  it("结果两分：400→rejected 保留 apperr code、409→conflict、500→throw kernel ApiError", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INVALID_ARGUMENT", message: "bad name" }, 400)));
    await expect(
      runCommand(createNetworkCommand, { name: "", egressNone: false }, { projectId: "shop" }),
    ).resolves.toEqual({ status: "rejected", code: "E_INVALID_ARGUMENT", message: "bad name" });

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_ALREADY_EXISTS", message: "name taken" }, 409)));
    await expect(
      runCommand(createNetworkCommand, { name: "x", egressNone: false }, { projectId: "shop" }),
    ).resolves.toEqual({ status: "conflict", message: "name taken" });

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INTERNAL", message: "boom" }, 500)));
    await expect(
      runCommand(createNetworkCommand, { name: "x", egressNone: false }, { projectId: "shop" }),
    ).rejects.toEqual({ kind: "server", status: 500, message: "E_INTERNAL: boom" });
  });

  it("rebuild/approve/revoke 裸 descriptor：动词 URL 与 invalidates 目标资源", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await runCommand(rebuildNetworkDescriptor, { networkId: "n1" }, { projectId: "shop" });
    await runCommand(approvePeerDescriptor, { peerId: "p 1" }, { projectId: "shop" });
    await runCommand(revokePeerDescriptor, { peerId: "p1" }, { projectId: "shop" });

    expect(String(fetchMock.mock.calls[0][0])).toBe("/v1/networks/rebuild");
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({ network_id: "n1" });
    expect(String(fetchMock.mock.calls[1][0])).toBe("/v1/networks/peers/p%201/approve");
    expect(String(fetchMock.mock.calls[2][0])).toBe("/v1/networks/peers/p1/revoke");
    expect(rebuildNetworkDescriptor.invalidates).toEqual([{ resource: networksResource.name }]);
    expect(approvePeerDescriptor.invalidates).toEqual([{ resource: networkPeersResource.name }]);
    expect(revokePeerDescriptor.invalidates).toEqual([{ resource: networkPeersResource.name }]);
  });

  it("declare：POST /v1/networks/peers 全参（ctx 线程 project_id）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await runCommand(declarePeerDescriptor, { networkId: "n1", peerProjectId: "other" }, { projectId: "shop" });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/networks/peers");
    expect(JSON.parse(String(init.body))).toEqual({
      network_id: "n1",
      project_id: "shop",
      peer_project_id: "other",
    });
  });

  it("失效桥键清单（旧 useApiMutation invalidate 等值）", () => {
    expect(networksResource.legacyKeys?.({ projectId: "shop" })).toEqual([["resources", "networks", "shop"]]);
    expect(declarePeerDescriptor.legacyKeys?.({ projectId: "shop" })).toEqual([
      ["resources", "network-peers", "shop"],
    ]);
  });
});
