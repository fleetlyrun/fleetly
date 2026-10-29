import { afterEach, describe, expect, it, vi } from "vitest";
import { runCommand } from "@lynx-go/uikit-core/kernel";
import {
  createVolumeCommand,
  fetchUploads,
  fetchVolumes,
  removeVolumeCommand,
  volumesSearch,
  uploadsSearch,
  volumesResource,
} from "./storage";

// Storage 域资源适配单测：protojson 行收敛、空 scope 不发请求、URL 编解码
// 往返；批 2 volumes 命令族锚（提交形收敛、结果两分、失效桥键清单）。
// fetch 面 stub 在 global（kit http 管线调用裸 fetch）。

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

describe("volumesSearch/uploadsSearch（URL 编解码契约——双 namespace 并集字段）", () => {
  it("q 编码往返为三字段 contains 谓词；decode 容忍缺省", () => {
    const encoded = volumesSearch.encode({
      predicate: { any: [
        { field: "name", op: "contains", value: "web" },
        { field: "id", op: "contains", value: "web" },
        { field: "digest", op: "contains", value: "web" },
      ] },
      window: { size: 20 },
    });
    // namespace 物理键（F1 回流批）：volumes.q；他命名空间键不携带
    expect(encoded["volumes.q"]).toBe("web");
    expect("q" in encoded).toBe(false);
    const decoded = volumesSearch.decode({ "volumes.q": "web" });
    expect(uploadsSearch.decode({ "volumes.q": "web" }).predicate).toBeUndefined();
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
    const patch = volumesSearch.patch({ predicate: null });
    expect("volumes.q" in patch).toBe(true);
    expect(patch["volumes.q"]).toBeUndefined();
    expect(patch["volumes.page"]).toBeUndefined();
  });
});

describe("volumes 命令族（批 2 写路径命令化）", () => {
  it("create：POST /v1/volumes 提交形收敛（pinnedNode 空串 = pinned_node_id 不发送）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    const result = await runCommand(createVolumeCommand, { name: "data", pinnedNode: "" }, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: null });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/volumes");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ project_id: "shop", name: "data" });

    await runCommand(createVolumeCommand, { name: "pinned", pinnedNode: "node-a" }, { projectId: "shop" });
    expect(JSON.parse(String((fetchMock.mock.calls[1][1] as RequestInit).body))).toEqual({
      project_id: "shop",
      name: "pinned",
      pinned_node_id: "node-a",
    });
  });

  it("结果两分：400→rejected 保留 apperr code、409→conflict（挂载中拒删）、500→throw kernel ApiError", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_VOLUME_IN_USE", message: "mounted by shop" }, 409)));
    await expect(runCommand(removeVolumeCommand, { volumeId: "v1" }, { projectId: "shop" })).resolves.toEqual({
      status: "conflict",
      message: "mounted by shop",
    });

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INVALID_ARGUMENT", message: "bad name" }, 400)));
    await expect(runCommand(createVolumeCommand, { name: "", pinnedNode: "" }, { projectId: "shop" })).resolves.toEqual({
      status: "rejected",
      code: "E_INVALID_ARGUMENT",
      message: "bad name",
    });

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INTERNAL", message: "boom" }, 500)));
    await expect(runCommand(removeVolumeCommand, { volumeId: "v1" }, { projectId: "shop" })).rejects.toEqual({
      kind: "server",
      status: 500,
      message: "E_INTERNAL: boom",
    });
  });

  it("remove：DELETE /v1/volumes/{id}（URL 编码）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await runCommand(removeVolumeCommand, { volumeId: "v 1" }, { projectId: "shop" });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/volumes/v%201");
    expect(init.method).toBe("DELETE");
  });

  it("失效桥键清单（旧 useApiMutation invalidate 等值）", () => {
    expect(volumesResource.legacyKeys?.({ projectId: "shop" })).toEqual([["resources", "volumes", "shop"]]);
  });
});
