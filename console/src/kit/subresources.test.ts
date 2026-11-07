import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { streamsResource, subBucketsResource, topicsResource } from "./subresources";

// 子资源适配器单测（B8，ADR-0096 决策 10 / design §4.7；kit/buckets.test.ts
// 同形）：端点锚（/v1/pubsubs/{id}/topics|streams、/v1/bucket-stores/{id}/
// buckets）、protojson 行收敛、空父 scope 不发请求（守卫门）、写路径命令锚
// （create 载荷 / preview 无路径参数端点 + update 带票 / delete 的 query 票
// ——DELETE 无 body，gateway 只从 query 装配 confirmation）。

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

describe("topicsResource（kafka 协议族子资源）", () => {
  it("按父实例整取 /v1/pubsubs/{id}/topics 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ topics: [{ id: "t1", name: "orders", partitions: 3, status: "ready" }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const rows = (await topicsResource.collection!(ALL, { projectId: "shop", pubsubId: "p1" }, undefined)).rows;
    expect(rows).toEqual([{ id: "t1", name: "orders", partitions: 3, status: "ready" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/pubsubs/p1/topics");
  });

  it("空 pubsubId 不发请求（父实例锚必填——守卫门）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await topicsResource.collection!(ALL, { projectId: "shop" }, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("create：POST /v1/pubsubs/{id}/topics（retention 缺省键缺席）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ topic: { id: "t1" } }));
    vi.stubGlobal("fetch", fetchMock);
    await topicsResource.commands.create.run({ pubsubId: "p1", name: "orders", partitions: 3 }, { projectId: "shop", pubsubId: "p1" });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(`${init.method} ${url}`).toBe("POST /v1/pubsubs/p1/topics");
    expect(JSON.parse(String(init.body))).toEqual({ name: "orders", partitions: 3 });
  });

  it("previewUpdate 走无路径参数端点、update PATCH 带 confirmation ticket", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ changes: [], ticket: "tkt-1" }));
    vi.stubGlobal("fetch", fetchMock);
    await topicsResource.commands.previewUpdate.run(
      { pubsubId: "p1", id: "t1", partitions: 5 },
      { projectId: "shop", pubsubId: "p1" },
    );
    await topicsResource.commands.update.run(
      { pubsubId: "p1", id: "t1", partitions: 5, ticket: "tkt-1" },
      { projectId: "shop", pubsubId: "p1" },
    );
    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "POST /v1/pubsubs/topics/preview-update",
      "PATCH /v1/pubsubs/p1/topics/t1",
    ]);
    expect(JSON.parse(String(calls[0]![1].body))).toEqual({ update: { pubsub_id: "p1", topic_id: "t1", partitions: 5 } });
    expect(JSON.parse(String(calls[1]![1].body))).toEqual({ partitions: 5, confirmation: { ticket: "tkt-1" } });
  });

  it("previewDelete 披露行 + delete 以 query 携票（DELETE 无 body）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ disclosure: "unrecoverable", ticket: "tkt-d" }));
    vi.stubGlobal("fetch", fetchMock);
    const preview = await topicsResource.commands.previewDelete.run(
      { pubsubId: "p1", id: "t1" },
      { projectId: "shop", pubsubId: "p1" },
    );
    expect(preview).toEqual({ status: "ok", payload: { disclosure: "unrecoverable", ticket: "tkt-d" } });
    await topicsResource.commands.delete.run({ pubsubId: "p1", id: "t1", ticket: "tkt-d" }, { projectId: "shop", pubsubId: "p1" });
    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "POST /v1/pubsubs/topics/preview-delete",
      "DELETE /v1/pubsubs/p1/topics/t1?confirmation.ticket=tkt-d",
    ]);
    expect(JSON.parse(String(calls[0]![1].body))).toEqual({ pubsub_id: "p1", topic_id: "t1" });
    expect(calls[1]![1].body).toBeUndefined();
  });
});

describe("streamsResource（nats 子资源）", () => {
  it("create：subjects 逗号串拆分、轴值直传", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ stream: { id: "s1" } }));
    vi.stubGlobal("fetch", fetchMock);
    await streamsResource.commands.create.run(
      { pubsubId: "p1", name: "ORDERS", subjects: "orders.>, bills.>", retention: "limits", storage: "file" },
      { projectId: "shop", pubsubId: "p1" },
    );
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(`${init.method} ${url}`).toBe("POST /v1/pubsubs/p1/streams");
    expect(JSON.parse(String(init.body))).toEqual({
      name: "ORDERS",
      subjects: ["orders.>", "bills.>"],
      retention: "limits",
      storage: "file",
    });
  });

  it("update：subjects 全量替换（空串 = 清空集）+ max 撤上限为 0", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ stream: { id: "s1" } }));
    vi.stubGlobal("fetch", fetchMock);
    await streamsResource.commands.update.run(
      { pubsubId: "p1", id: "s1", subjects: "", maxStreamSize: null, ticket: "tkt" },
      { projectId: "shop", pubsubId: "p1" },
    );
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(`${init.method} ${url}`).toBe("PATCH /v1/pubsubs/p1/streams/s1");
    expect(JSON.parse(String(init.body))).toEqual({ subjects: [], max_stream_size: 0, confirmation: { ticket: "tkt" } });
  });

  it("delete 无票 = 无 query（skip/票面由调用面决定）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);
    await streamsResource.commands.delete.run({ pubsubId: "p1", id: "s1" }, { projectId: "shop", pubsubId: "p1" });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(`${init.method} ${url}`).toBe("DELETE /v1/pubsubs/p1/streams/s1");
  });
});

describe("subBucketsResource（bucket store 子资源）", () => {
  it("按父实例整取 /v1/bucket-stores/{id}/buckets；空父 scope 不发请求", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ buckets: [{ id: "b1", name: "media", quota_bytes: "1024" }] }));
    vi.stubGlobal("fetch", fetchMock);
    const rows = (await subBucketsResource.collection!(ALL, { projectId: "shop", bucketStoreId: "bs1" }, undefined)).rows;
    expect(rows).toEqual([{ id: "b1", name: "media", quota_bytes: "1024" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/bucket-stores/bs1/buckets");
    fetchMock.mockClear();
    expect((await subBucketsResource.collection!(ALL, { projectId: "shop" }, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("命令集齐形（create/previewUpdate/update/previewDelete/delete）", () => {
    expect(Object.keys(subBucketsResource.commands).sort()).toEqual([
      "create",
      "delete",
      "previewDelete",
      "previewUpdate",
      "update",
    ]);
  });

  it("previewDelete + delete 带票（query 形态）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ disclosure: "objects", ticket: "tkt-b" }));
    vi.stubGlobal("fetch", fetchMock);
    await subBucketsResource.commands.previewDelete.run({ bucketStoreId: "bs1", id: "b1" }, { projectId: "shop", bucketStoreId: "bs1" });
    await subBucketsResource.commands.delete.run(
      { bucketStoreId: "bs1", id: "b1", ticket: "tkt-b" },
      { projectId: "shop", bucketStoreId: "bs1" },
    );
    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "POST /v1/bucket-stores/buckets/preview-delete",
      "DELETE /v1/bucket-stores/bs1/buckets/b1?confirmation.ticket=tkt-b",
    ]);
  });
});
