import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import {
  browseDatabaseCommand,
  databaseBackupListLookup,
  databaseBackupListResource,
  DATABASE_ENGINES,
  databasesResource,
  databasesSearch,
} from "./databases";

// 数据库域适配器单测（批1；uikit 0.5.0 回流批改 restFetchAll 口径）：端点
// 锚、protojson 行收敛、空 scope 不发请求（守卫门）、快筛/引擎下拉的 URL
// 编解码往返。fetch 面 stub 在 global（authFetch→裸 fetch 管线；命令面走
// apiSend → apiFetch）。批2 增写路径命令锚：实体动词挂 resource commands、
// browse 动作类裸 descriptor、结果两分（400→rejected/409→conflict）与
// legacyKeys 桥。

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

describe("databasesResource 读面（端点 + 收敛 + 空 scope）", () => {
  it("按 project_id 整取 /v1/databases 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        databases: [{ id: "db1", name: "orders", engine: "postgres", status: "running" }, undefined],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await databasesResource.collection!(ALL, { projectId: "shop" }, undefined)).rows;

    expect(rows).toEqual([{ id: "db1", name: "orders", engine: "postgres", status: "running" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/databases?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（ListDatabases project_id 必填契约——守卫门）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await databasesResource.collection!(ALL, {}, undefined)).rows).toEqual([]);
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

describe("databases commands（批2 写路径命令锚）", () => {
  it("create：POST /v1/databases，projectId 取 ctx scope；restoreFrom 空串 → 键缺席", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "db_new" }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await databasesResource.commands.create.run(
      { name: "orders-db", engine: "postgres" },
      { projectId: "shop" },
    );

    expect(result).toEqual({ status: "ok", payload: { id: "db_new" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/databases");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ project_id: "shop", name: "orders-db", engine: "postgres" });
  });

  it("create：restoreFrom 在场 → restore_from_backup 键（restore 语义共用 create）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "db_restored" }));
    vi.stubGlobal("fetch", fetchMock);

    await databasesResource.commands.create.run(
      { name: "copy", engine: "redis", restoreFrom: "bk_01" },
      { projectId: "shop" },
    );

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      project_id: "shop",
      name: "copy",
      engine: "redis",
      restore_from_backup: "bk_01",
    });
  });

  it("delete/backup/rotate：动词端点锚（DELETE / POST 同形）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await databasesResource.commands.delete.run({ id: "db1" }, { projectId: "shop" });
    await databasesResource.commands.backup.run({ id: "db1" }, { projectId: "shop" });
    await databasesResource.commands.rotate.run({ id: "db1" }, { projectId: "shop" });

    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls.map(([url, init]) => `${init.method} ${url}`)).toEqual([
      "DELETE /v1/databases/db1",
      "POST /v1/databases/db1/backups",
      "POST /v1/databases/db1/rotate-password",
    ]);
  });

  it("browse：动作类裸 descriptor，POST /v1/databases/{id}/browse 回 url", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ url: "https://browse.example/ticket" }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await browseDatabaseCommand.run({ id: "db1" }, {});

    expect(result).toEqual({ status: "ok", payload: { url: "https://browse.example/ticket" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/databases/db1/browse");
    expect(init.method).toBe("POST");
  });

  it("结果两分：400 → rejected（code 保留）；409 → conflict（结果通道非异常）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "E_INVALID_ARGUMENT", message: "name is required" }, 400)),
    );
    const rejected = await databasesResource.commands.create.run(
      { name: "", engine: "postgres" },
      { projectId: "shop" },
    );
    expect(rejected).toEqual({ status: "rejected", code: "E_INVALID_ARGUMENT", message: "name is required" });

    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "E_CONFLICT", message: "changed concurrently" }, 409)),
    );
    const conflict = await databasesResource.commands.delete.run({ id: "db1" }, { projectId: "shop" });
    expect(conflict).toEqual({ status: "conflict", message: "changed concurrently" });
  });

  it("失败出界收敛 kernel ApiError（5xx → server）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_BOOM", message: "boom" }, 503)));
    const attempt = databasesResource.commands.backup.run({ id: "db1" }, { projectId: "shop" });
    await expect(attempt).rejects.toEqual({ kind: "server", status: 503, message: "E_BOOM: boom" });
  });

  it("legacyKeys 桥零读者退役（F1 回流批审计——catalog databases 键族无读者）", () => {
    expect(databasesResource.commands.create.legacyKeys).toBeUndefined();
    expect(databasesResource.commands.delete.legacyKeys).toBeUndefined();
  });
});

// ---- per-Database 备份列表（2026-10-27 批4 读面收编：catalog
// useDatabaseBackups 退役；与 kit/backups 的跨项目台账分立） ----

describe("databaseBackupListResource 读面（端点 + 收敛）", () => {
  it("GET /v1/databases/{id}/backups 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ backups: [{ id: "B1", created_at: "t" }, undefined, { id: "B2" }] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await databaseBackupListResource.collection!(
      databaseBackupListLookup.decode({}),
      { projectId: "P1", databaseId: "D1" },
      undefined,
    )).rows;

    expect(rows).toEqual([{ id: "B1", created_at: "t" }, { id: "B2" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/databases/D1/backups");
  });

  it("scope 缺 databaseId 时零请求空段（守卫门）；非 2xx 收敛 kernel ApiError（桥零读者退役）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(
      (await databaseBackupListResource.collection!(databaseBackupListLookup.decode({}), { projectId: "P1" }, undefined))
        .rows,
    ).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(
      databaseBackupListResource.collection!(
        databaseBackupListLookup.decode({}),
        { projectId: "P1", databaseId: "D1" },
        undefined,
      ),
    ).rejects.toMatchObject({ kind: "server", status: 503, message: "boom" });
    expect(databaseBackupListResource.scope).toEqual(["projectId", "databaseId"]);
    expect(databaseBackupListResource.legacyKeys).toBeUndefined();
    expect(databaseBackupListLookup.decode({}).window.size).toBe(200);
  });
});
