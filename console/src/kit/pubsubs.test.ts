import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import {
  pubsubBackupListLookup,
  pubsubBackupListResource,
  pubsubsFilterSchema,
  pubsubsLookup,
  pubsubsResource,
  pubsubsSearch,
} from "./pubsubs";

// pubsub 域适配器单测（B4c 姐妹页；kit/databases.test.ts 同形）：端点锚、
// protojson 行收敛、空 scope 不发请求（守卫门）、快筛/引擎下拉的 URL 编解
// 码往返、写路径命令锚（create/preview/update 带 confirmation ticket/
// delete/backup/rotate）与 Update 确认面载荷（settings/resources 在场语
// 义——B4c 三家族对齐）。

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

describe("pubsubsResource 读面（端点 + 收敛 + 空 scope）", () => {
  it("按 project_id 整取 /v1/pubsubs 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        pubsubs: [{ id: "ps1", name: "events", engine: "nats", status: "running" }, undefined],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await pubsubsResource.collection!(ALL, { projectId: "shop" }, undefined)).rows;

    expect(rows).toEqual([{ id: "ps1", name: "events", engine: "nats", status: "running" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/pubsubs?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（ListPubSubs project_id 必填契约——守卫门）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await pubsubsResource.collection!(ALL, {}, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("pubsubBackupListResource：实例轴端点 + 空 pubsubId 不发请求", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ backups: [{ id: "bk_01" }] }));
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await pubsubBackupListResource.collection!(ALL, { projectId: "P1", pubsubId: "ps1" }, undefined)).rows;
    expect(rows).toEqual([{ id: "bk_01" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/pubsubs/ps1/backups");

    expect((await pubsubBackupListResource.collection!(ALL, { projectId: "P1" }, undefined)).rows).toEqual([]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("pubsubsSearch（URL 编解码契约）", () => {
  it("engine 下拉过滤编解码往返（filter=engine:eq:nats 进 URL）", () => {
    const decoded = pubsubsSearch.decode({ filter: ["engine:eq:nats"] });
    expect(decoded.predicate).toEqual({ field: "engine", op: "eq", value: "nats" });
    const encoded = pubsubsSearch.encode({
      predicate: { field: "engine", op: "eq", value: "nats" },
      window: { size: 20 },
    });
    expect(encoded.filter).toEqual(["engine:eq:nats"]);
  });

  it("pubsubsFilterSchema：FilterBar 同形实例，engine 值域 = 目录 pubsub 族现值", () => {
    const schema = pubsubsFilterSchema(["nats"]);
    expect(schema.spec.text).toEqual(pubsubsSearch.spec.text);
    expect(schema.spec.filters?.[0]?.options).toEqual([{ value: "nats", label: "nats" }]);
    expect(schema.decode({ filter: ["engine:eq:nats"] }).predicate).toEqual(
      pubsubsSearch.decode({ filter: ["engine:eq:nats"] }).predicate,
    );
  });

  it("pubsubsLookup：大页 schema（行查找读面全集，不截断）", () => {
    expect(pubsubsLookup.spec.defaultSize).toBe(200);
    expect(pubsubBackupListLookup.spec.defaultSize).toBe(200);
  });
});

describe("pubsubs commands（写路径命令锚）", () => {
  it("create：POST /v1/pubsubs，projectId 取 ctx scope；缺席面（version/settings/resources/restoreFrom）键不进载荷", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "ps_new" }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await pubsubsResource.commands.create.run(
      { name: "events", engine: "nats" },
      { projectId: "shop" },
    );

    expect(result).toEqual({ status: "ok", payload: { id: "ps_new" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/pubsubs");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ project_id: "shop", name: "events", engine: "nats" });
  });

  it("create：目录驱动面 + restore 源（version/settings/resources/restore_from_backup 只携在场值）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "ps_new" }));
    vi.stubGlobal("fetch", fetchMock);

    await pubsubsResource.commands.create.run(
      {
        name: "tuned",
        engine: "nats",
        version: "2.11",
        settings: { jetstream_max_file: "1gb" },
        cpuMillis: "500",
        restoreFrom: "bk_01",
      },
      { projectId: "shop" },
    );

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      project_id: "shop",
      name: "tuned",
      engine: "nats",
      version: "2.11",
      settings: { jetstream_max_file: "1gb" },
      resources: { cpu_millis: 500 },
      restore_from_backup: "bk_01",
    });
  });

  it("previewUpdate：POST /v1/pubsubs/preview-update，载荷内嵌同一 update（pubsub_id + 在场面）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ rollout: "replace", ticket: "tkt-1" }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await pubsubsResource.commands.previewUpdate.run(
      { id: "ps1", settings: { jetstream_max_memory: "256mb" } },
      { projectId: "shop" },
    );

    expect(result).toEqual({ status: "ok", payload: { rollout: "replace", ticket: "tkt-1" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/pubsubs/preview-update");
    expect(JSON.parse(String(init.body))).toEqual({
      update: { pubsub_id: "ps1", settings: { values: { jetstream_max_memory: "256mb" } } },
    });
  });

  it("update：PATCH /v1/pubsubs/{id} 带 confirmation ticket（B4c 确认面）；无 ticket 键缺席", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ pubsub: { id: "ps1" }, static_applied: ["jetstream_max_memory"] }));
    vi.stubGlobal("fetch", fetchMock);

    await pubsubsResource.commands.update.run(
      { id: "ps1", settings: { jetstream_max_memory: "256mb" }, ticket: "tkt-1" },
      { projectId: "shop" },
    );
    await pubsubsResource.commands.update.run({ id: "ps1", resources: { cpuMillis: "1000", memoryMb: "" } }, { projectId: "shop" });

    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual(["PATCH /v1/pubsubs/ps1", "PATCH /v1/pubsubs/ps1"]);
    expect(JSON.parse(String(calls[0]![1].body))).toEqual({
      settings: { values: { jetstream_max_memory: "256mb" } },
      confirmation: { ticket: "tkt-1" },
    });
    // 资源面 0 轴缺席（protojson 零值缺省同义）；无票 = confirmation 键缺席。
    expect(JSON.parse(String(calls[1]![1].body))).toEqual({ resources: { cpu_millis: 1000 } });
  });

  it("delete/backup/rotate：动词端点锚（DELETE / POST 同形）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await pubsubsResource.commands.delete.run({ id: "ps1" }, { projectId: "shop" });
    await pubsubsResource.commands.backup.run({ id: "ps1" }, { projectId: "shop" });
    await pubsubsResource.commands.rotate.run({ id: "ps1" }, { projectId: "shop" });

    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "DELETE /v1/pubsubs/ps1",
      "POST /v1/pubsubs/ps1/backups",
      "POST /v1/pubsubs/ps1/rotate-password",
    ]);
  });

  it("update 拒绝腿：E_CONFIRMATION_STALE（428）→ rejected 带原 code（分诊重预览的判别锚）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ code: "E_CONFIRMATION_STALE", message: "ticket expired" }, 428),
    );
    vi.stubGlobal("fetch", fetchMock);

    const result = await pubsubsResource.commands.update.run({ id: "ps1", settings: {}, ticket: "stale" }, { projectId: "shop" });

    expect(result.status).toBe("rejected");
    if (result.status === "rejected") {
      expect(result.code).toBe("E_CONFIRMATION_STALE");
    }
  });
});
