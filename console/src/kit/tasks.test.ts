import { afterEach, describe, expect, it, vi } from "vitest";
import { collectionQueryOptions, invalidateAfterCommand } from "@lynx-go/uikit-core";
import { scoped, textPredicate, type CommandDescriptor, type CommandsRecord, type Resource, type SubsRecord } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import {
  runsLookup,
  runsResource,
  schedulesResource,
  schedulesSearch,
  stopRunCommand,
  tasksResource,
  tasksSearch,
} from "./tasks";

// 自动化域适配器单测（批1；uikit 0.5.0 回流批改 restFetchAll 口径）：
// tasks/schedules 端点锚、protojson 行收敛、空 scope 不发请求（守卫门）、
// 双 tab 共享 schema 的 URL 编解码往返。fetch 面 stub 在 global（authFetch
// →裸 fetch 管线）；断言经资源适配器调用取数。
// 批2 命令锚：defineResource commands 的结果两分映射（ok/400→rejected/
// 409→conflict/404→throw）与失效编排（kit selector + legacyKeys 桥）。

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

describe("tasksResource/schedulesResource 读面（端点 + 收敛 + 空 scope）", () => {
  it("按 project_id 整取 /v1/tasks 并展平 undefined 行（不滤缺字段行）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        tasks: [
          { id: "t1", name: "migrate", form: "one-shot", state: "active" },
          undefined,
          { name: "no-id" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await tasksResource.collection!(ALL, { projectId: "shop" }, undefined)).rows;

    expect(rows).toEqual([
      { id: "t1", name: "migrate", form: "one-shot", state: "active" },
      { name: "no-id" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/tasks?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("schedules 打 /v1/schedules 同口径收敛", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ schedules: [{ id: "s1", name: "nightly", cron: "0 3 * * *" }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await schedulesResource.collection!(ALL, { projectId: "shop" }, undefined)).rows;

    expect(rows).toEqual([{ id: "s1", name: "nightly", cron: "0 3 * * *" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/schedules?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（List project_id 必填契约——守卫门）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await tasksResource.collection!(ALL, {}, undefined)).rows).toEqual([]);
    expect((await schedulesResource.collection!(ALL, { projectId: "" }, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("tasksSearch/schedulesSearch（URL 编解码契约，双 namespace 实例）", () => {
  it("q 编码往返为并集字段集的 contains 谓词；物理键带 namespace 前缀", () => {
    const fields = ["name", "id", "form", "state", "cron", "image"];
    const encoded = tasksSearch.encode({
      predicate: textPredicate(fields, "mig"),
      window: { size: 20 },
    });
    // namespace 物理键：tasks.q（URL 键形变化是 F1 回流批的行为变更）
    expect(encoded["tasks.q"]).toBe("mig");
    expect("q" in encoded).toBe(false);
    const decoded = tasksSearch.decode({ "tasks.q": "mig" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "mig"));
    expect(decoded.window.size).toBe(20);
    // decode 只读本命名空间键——他命名空间键互不携带（M1 验收锚）
    expect(tasksSearch.decode({ "schedules.q": "mig" }).predicate).toBeUndefined();
    expect(schedulesSearch.decode({ "schedules.q": "mig" }).predicate).toEqual(
      textPredicate(fields, "mig"),
    );
  });

  it("清除谓词的补丁显式置空前缀键（函数式 search 展开不残留旧值）", () => {
    const patch = tasksSearch.patch({ predicate: null });
    expect("tasks.q" in patch).toBe(true);
    expect(patch["tasks.q"]).toBeUndefined();
    expect(patch["tasks.page"]).toBeUndefined();
  });
});

// ---- 批2 命令锚 ----

const endpointAnchors: ReadonlyArray<{
  label: string;
  descriptor: CommandDescriptor<any, any, any>;
  input: any;
  path: string;
  method: string;
  body: unknown;
}> = [
  {
    label: "task scale",
    descriptor: tasksResource.commands.scale,
    input: { id: "t1", desired_concurrency: 2 },
    path: "/v1/tasks/t1/scale",
    method: "POST",
    body: { desired_concurrency: 2 },
  },
  {
    label: "task stop（force 缺省 false）",
    descriptor: tasksResource.commands.stop,
    input: { id: "t1" },
    path: "/v1/tasks/t1/stop",
    method: "POST",
    body: { force: false },
  },
  {
    label: "task renew",
    descriptor: tasksResource.commands.renew,
    input: { id: "t1" },
    path: "/v1/tasks/t1/renew",
    method: "POST",
    body: {},
  },
  {
    label: "task delete",
    descriptor: tasksResource.commands.delete,
    input: { id: "t1" },
    path: "/v1/tasks/t1",
    method: "DELETE",
    body: {},
  },
  {
    label: "schedule trigger",
    descriptor: schedulesResource.commands.trigger,
    input: { id: "s1" },
    path: "/v1/schedules/s1/trigger",
    method: "POST",
    body: {},
  },
  {
    label: "schedule delete",
    descriptor: schedulesResource.commands.delete,
    input: { id: "s1" },
    path: "/v1/schedules/s1",
    method: "DELETE",
    body: {},
  },
  {
    label: "run stop（裸 descriptor）",
    descriptor: stopRunCommand,
    input: { id: "r1" },
    path: "/v1/runs/r1/stop",
    method: "POST",
    body: {},
  },
];

describe("defineResource commands（端点 + 请求体 + 结果两分）", () => {
  it("create 的 project_id 由命令 ctx 注入（输入面不重复携带）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ task: { id: "t9" } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await tasksResource.commands.create.run(
      { image: "busybox:1.37", form: "resident", desired_concurrency: 3 },
      { projectId: "shop" },
    );

    expect(result).toEqual({ status: "ok", payload: { task: { id: "t9" } } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/v1/tasks");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      project_id: "shop",
      image: "busybox:1.37",
      form: "resident",
      desired_concurrency: 3,
    });
  });

  it("schedule create 打 POST /v1/schedules（ctx 注入 project_id）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ schedule: { id: "s9" } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await schedulesResource.commands.create.run(
      { cron: "0 3 * * *", timezone: "Asia/Shanghai", image: "busybox:1.37" },
      { projectId: "shop" },
    );

    expect(result).toEqual({ status: "ok", payload: { schedule: { id: "s9" } } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/v1/schedules");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      project_id: "shop",
      cron: "0 3 * * *",
      timezone: "Asia/Shanghai",
      image: "busybox:1.37",
    });
  });

  for (const anchor of endpointAnchors) {
    it(`端点锚：${anchor.label} → ${anchor.method} ${anchor.path}`, async () => {
      const fetchMock = vi.fn(async () => jsonResponse({}));
      vi.stubGlobal("fetch", fetchMock);

      const result = await anchor.descriptor.run(anchor.input, {});

      expect(result).toEqual({ status: "ok", payload: {} });
      const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
      expect(url).toBe(anchor.path);
      expect(init.method).toBe(anchor.method);
      expect(JSON.parse(String(init.body))).toEqual(anchor.body);
    });
  }

  it("400 → rejected（信封 code 保留机器判别面）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INVALID_CRON", message: "bad cron" }, 400)));
    const result = await schedulesResource.commands.create.run(
      { cron: "x", image: "busybox:1.37" },
      { projectId: "shop" },
    );
    expect(result).toEqual({ status: "rejected", code: "E_INVALID_CRON", message: "bad cron" });
  });

  it("409 → conflict（如 trigger 撞上一拍未终态）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "E_CONFLICT", message: "previous run still active" }, 409)),
    );
    const result = await schedulesResource.commands.trigger.run({ id: "s1" }, {});
    expect(result).toEqual({ status: "conflict", message: "previous run still active" });
  });

  it("delete 404 保持 throw（幂等裁决对齐 CLI：服务端已删行回 200，404 = id 从未存在，不吞）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "E_NOT_FOUND", message: "no such task" }, 404)),
    );
    await expect(tasksResource.commands.delete.run({ id: "gone" }, {})).rejects.toMatchObject({
      kind: "not-found",
    });
  });
});

// ---- 失效编排锚（invalidateAfterCommand 单源：kit selector + legacyKeys 桥） ----

type InvalidateCall = { predicate?: (query: { queryKey: readonly unknown[] }) => boolean; queryKey?: readonly unknown[] };

function fakeQueryClient() {
  return { invalidateQueries: vi.fn() };
}

// kit collection 键经 collectionQueryOptions 单源取（0.4.0 键形含 scopeKeys
// 段，手拼会漏——宽容降级只保旧形键，不保新形手拼）。
function collectionKey<T, ID, C extends CommandsRecord<ID>, S extends SubsRecord, K extends readonly string[]>(
  resource: Resource<T, ID, C, S, K>,
  projectId: string,
): readonly unknown[] {
  return collectionQueryOptions(scoped(resource, { projectId }), { window: { size: 20 } }).queryKey;
}

function callsOf(client: { invalidateQueries: ReturnType<typeof vi.fn> }): Array<[InvalidateCall]> {
  return client.invalidateQueries.mock.calls as Array<[InvalidateCall]>;
}

function predicateOf(client: { invalidateQueries: ReturnType<typeof vi.fn> }) {
  const call = callsOf(client).find(([c]) => c.predicate != null);
  expect(call).toBeDefined();
  return call![0].predicate!;
}

describe("失效编排（kit selector 单源；catalog 桥零读者退役）", () => {
  it("task 命令缺省失效：同资源 kit 键投影匹配", () => {
    const client = fakeQueryClient();
    invalidateAfterCommand(
      client as unknown as Parameters<typeof invalidateAfterCommand>[0],
      tasksResource.commands.create,
      { projectId: "shop" },
    );
    const predicate = predicateOf(client);
    expect(predicate({ queryKey: collectionKey(tasksResource, "shop") })).toBe(true);
    expect(predicate({ queryKey: collectionKey(tasksResource, "other") })).toBe(false);
    expect(predicate({ queryKey: collectionKey(schedulesResource, "shop") })).toBe(false);
  });

  it("scale/stop 同资源缺省失效（runs 面投影不相等——精确失效归 stopRun）", () => {
    for (const descriptor of [tasksResource.commands.scale, tasksResource.commands.stop]) {
      const client = fakeQueryClient();
      invalidateAfterCommand(
        client as unknown as Parameters<typeof invalidateAfterCommand>[0],
        descriptor,
        { projectId: "shop" },
      );
      const predicate = predicateOf(client);
      expect(predicate({ queryKey: collectionKey(tasksResource, "shop") })).toBe(true);
      // runs 声明键 [projectId, taskId]：命令 scope {projectId} 的投影缺
      // taskId——不相等（如实的 scope 边界，非缺陷）。
      expect(
        predicate({ queryKey: ["collection", "runs", { projectId: "shop", taskId: "T1" }, ["projectId", "taskId"], null, null, { size: 200, cursor: null, offset: null }] }),
      ).toBe(false);
    }
  });

  it("trigger 失效跨 tasks + schedules 双面（selector 声明）", () => {
    const client = fakeQueryClient();
    invalidateAfterCommand(
      client as unknown as Parameters<typeof invalidateAfterCommand>[0],
      schedulesResource.commands.trigger,
      { projectId: "shop" },
    );
    const predicate = predicateOf(client);
    expect(predicate({ queryKey: collectionKey(tasksResource, "shop") })).toBe(true);
    expect(predicate({ queryKey: collectionKey(schedulesResource, "shop") })).toBe(true);
    expect(predicate({ queryKey: collectionKey(schedulesResource, "other") })).toBe(false);
  });

  it("stopRun 裸 descriptor：runs selector 投影匹配（scope 同源喂匹配）", () => {
    const client = fakeQueryClient();
    invalidateAfterCommand(
      client as unknown as Parameters<typeof invalidateAfterCommand>[0],
      stopRunCommand,
      { projectId: "shop", taskId: "T1" },
    );
    const predicate = predicateOf(client);
    expect(predicate({ queryKey: ["collection", "runs", { projectId: "shop", taskId: "T1" }, ["projectId", "taskId"], null, null, { size: 200, cursor: null, offset: null }] })).toBe(true);
    expect(predicate({ queryKey: ["collection", "runs", { projectId: "shop", taskId: "T2" }, ["projectId", "taskId"], null, null, { size: 200, cursor: null, offset: null }] })).toBe(false);
  });
});

// ---- runs 资源（2026-10-27 批4 读面收编：catalog useRuns 退役） ----

describe("runsResource 读面（端点 + 收敛）", () => {
  it("GET /v1/runs?task_id 并展平 undefined 行（restFetchAll 信封过滤）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ runs: [{ id: "R1", state: "running" }, undefined, { id: "R2", state: "failed" }] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await runsResource.collection!(runsLookup.decode({}), { projectId: "P1", taskId: "T1" }, undefined)).rows;

    expect(rows).toEqual([{ id: "R1", state: "running" }, { id: "R2", state: "failed" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/runs?task_id=T1");
  });

  it("scope 缺 taskId 时零请求空段（守卫门——catalog enabled 门同语义）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await runsResource.collection!(runsLookup.decode({}), { projectId: "P1" }, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("非 2xx 收敛 kernel ApiError；scope 声明（存量键桥零读者退役）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(
      runsResource.collection!(runsLookup.decode({}), { projectId: "P1", taskId: "T1" }, undefined),
    ).rejects.toMatchObject({ kind: "server", status: 503, message: "boom" });
    expect(runsResource.scope).toEqual(["projectId", "taskId"]);
    expect(runsResource.legacyKeys).toBeUndefined();
    expect(runsLookup.decode({}).window.size).toBe(200);
  });
});
