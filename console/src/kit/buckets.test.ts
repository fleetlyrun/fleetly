import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import {
  bucketStoresFilterSchema,
  bucketStoresLookup,
  bucketStoresResource,
  bucketStoresSearch,
} from "./buckets";

// bucket store 域适配器单测（B5 姐妹页；kit/pubsubs.test.ts 同形）：端点锚、
// protojson 行收敛（bucket_stores 信封）、空 scope 不发请求（守卫门）、快筛/
// 引擎下拉的 URL 编解码往返、写路径命令锚（create/preview/update 带
// confirmation ticket/delete/rotate——**无 backup 命令**）与 Update 确认面
// 载荷（bucket_store_id 键 + resources 在场语义）。

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

const ALL = { window: { size: 20 } } as const;

describe("bucketStoresResource 读面（端点 + 收敛 + 空 scope）", () => {
  it("按 project_id 整取 /v1/bucket-stores 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        bucket_stores: [{ id: "bs1", name: "assets", engine: "silo", status: "running" }, undefined],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await bucketStoresResource.collection!(ALL, { projectId: "shop" }, undefined)).rows;

    expect(rows).toEqual([{ id: "bs1", name: "assets", engine: "silo", status: "running" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/bucket-stores?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（ListBucketStores project_id 必填契约——守卫门）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await bucketStoresResource.collection!(ALL, {}, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("无备份命令族（silo 转储挂账——能力位裁剪，kit 不设 backup 命令）", () => {
    expect(Object.keys(bucketStoresResource.commands).sort()).toEqual(["create", "delete", "previewUpdate", "rotate", "update"]);
  });
});

describe("bucketStoresSearch（URL 编解码契约）", () => {
  it("engine 下拉过滤编解码往返（filter=engine:eq:silo 进 URL）", () => {
    const decoded = bucketStoresSearch.decode({ filter: ["engine:eq:silo"] });
    expect(decoded.predicate).toEqual({ field: "engine", op: "eq", value: "silo" });
    const encoded = bucketStoresSearch.encode({
      predicate: { field: "engine", op: "eq", value: "silo" },
      window: { size: 20 },
    });
    expect(encoded.filter).toEqual(["engine:eq:silo"]);
  });

  it("bucketStoresFilterSchema：FilterBar 同形实例，engine 值域 = 目录 bucketstore 族现值", () => {
    const schema = bucketStoresFilterSchema(["silo"]);
    expect(schema.spec.text).toEqual(bucketStoresSearch.spec.text);
    expect(schema.spec.filters?.[0]?.options).toEqual([{ value: "silo", label: "silo" }]);
    expect(schema.decode({ filter: ["engine:eq:silo"] }).predicate).toEqual(
      bucketStoresSearch.decode({ filter: ["engine:eq:silo"] }).predicate,
    );
  });

  it("bucketStoresLookup：大页 schema（行查找读面全集，不截断）", () => {
    expect(bucketStoresLookup.spec.defaultSize).toBe(200);
  });
});

describe("bucket store commands（写路径命令锚）", () => {
  it("create：POST /v1/bucket-stores，projectId 取 ctx scope；缺席面键不进载荷", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "bs_new" }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await bucketStoresResource.commands.create.run({ name: "assets", engine: "silo" }, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: { id: "bs_new" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/bucket-stores");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ project_id: "shop", name: "assets", engine: "silo" });
  });

  it("create：目录驱动面（version/resources 只携在场值；零旋钮族 settings 空集不进载荷）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "bs_new" }));
    vi.stubGlobal("fetch", fetchMock);

    await bucketStoresResource.commands.create.run(
      { name: "media", engine: "silo", version: "2026-10", settings: {}, cpuMillis: "500", memoryMb: "512" },
      { projectId: "shop" },
    );

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      project_id: "shop",
      name: "media",
      engine: "silo",
      version: "2026-10",
      resources: { cpu_millis: 500, memory_mb: 512 },
    });
  });

  it("previewUpdate：POST /v1/bucket-stores/preview-update，载荷内嵌同一 update（bucket_store_id + 在场面）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ rollout: "replace", ticket: "tkt-1" }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await bucketStoresResource.commands.previewUpdate.run(
      { id: "bs1", resources: { cpuMillis: "750", memoryMb: "" } },
      { projectId: "shop" },
    );

    expect(result).toEqual({ status: "ok", payload: { rollout: "replace", ticket: "tkt-1" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/bucket-stores/preview-update");
    expect(JSON.parse(String(init.body))).toEqual({
      update: { bucket_store_id: "bs1", resources: { cpu_millis: 750 } },
    });
  });

  it("update：PATCH /v1/bucket-stores/{id} 带 confirmation ticket；无 ticket 键缺席", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ bucket_store: { id: "bs1" }, static_applied: [] }));
    vi.stubGlobal("fetch", fetchMock);

    await bucketStoresResource.commands.update.run(
      { id: "bs1", resources: { cpuMillis: "750", memoryMb: "512" }, ticket: "tkt-1" },
      { projectId: "shop" },
    );
    await bucketStoresResource.commands.update.run({ id: "bs1", resources: { cpuMillis: "1000", memoryMb: "" } }, { projectId: "shop" });

    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "PATCH /v1/bucket-stores/bs1",
      "PATCH /v1/bucket-stores/bs1",
    ]);
    expect(JSON.parse(String(calls[0]![1].body))).toEqual({
      resources: { cpu_millis: 750, memory_mb: 512 },
      confirmation: { ticket: "tkt-1" },
    });
    expect(JSON.parse(String(calls[1]![1].body))).toEqual({ resources: { cpu_millis: 1000 } });
  });

  it("delete/rotate：动词端点锚（DELETE / POST 同形）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await bucketStoresResource.commands.delete.run({ id: "bs1" }, { projectId: "shop" });
    await bucketStoresResource.commands.rotate.run({ id: "bs1" }, { projectId: "shop" });

    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "DELETE /v1/bucket-stores/bs1",
      "POST /v1/bucket-stores/bs1/rotate-password",
    ]);
  });

  it("update 拒绝腿：E_CONFIRMATION_STALE（428）→ rejected 带原 code", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ code: "E_CONFIRMATION_STALE", message: "ticket expired" }, 428));
    vi.stubGlobal("fetch", fetchMock);

    const result = await bucketStoresResource.commands.update.run(
      { id: "bs1", resources: { cpuMillis: "1", memoryMb: "" }, ticket: "stale" },
      { projectId: "shop" },
    );

    expect(result.status).toBe("rejected");
    if (result.status === "rejected") {
      expect(result.code).toBe("E_CONFIRMATION_STALE");
    }
  });
});
