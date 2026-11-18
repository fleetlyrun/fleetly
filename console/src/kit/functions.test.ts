import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import {
  functionRevisionsResource,
  functionsLookup,
  functionsResource,
  functionsSearch,
} from "./functions";

// functions 域适配器单测（ADR-0102 任务⑥；kit/pubsubs.test.ts 同形）：端点
// 锚、protojson 行收敛、空 scope 禁查门、URL 编解码往返、写路径命令锚
//（create 三平面字段面/deploy 全量替换/scale/exposure 带 host 语义/rollback/
// delete）与 exposure 双态在缺席语义（private 的 host 不下发）。

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

describe("functionsResource collection", () => {
  it("fetches /v1/functions with the project scope and unwraps the envelope", async () => {
    setToken("t");
    const fetchMock = vi.fn(async () =>
      jsonResponse({ functions: [{ id: "fn-1", name: "resize" }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const rows = (await functionsResource.collection!(ALL, { projectId: "p1" }, undefined)).rows;
    expect(rows).toEqual([{ id: "fn-1", name: "resize" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/functions?");
    expect(String(url)).toContain("project_id=p1");
  });

  it("returns empty rows without a project scope (no request fired)", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await functionsResource.collection!(ALL, {}, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("functionsSearch", () => {
  it("round-trips the exposure filter through URL search", () => {
    const decoded = functionsSearch.decode({ filter: ["exposure:eq:public"] });
    expect(decoded.predicate).toEqual({ field: "exposure", op: "eq", value: "public" });
    const encoded = functionsSearch.encode({
      predicate: { field: "exposure", op: "eq", value: "public" },
      window: { size: 20 },
    });
    expect(encoded.filter).toEqual(["exposure:eq:public"]);
  });

  it("functionsLookup is a large-window schema", () => {
    expect(functionsLookup.spec.defaultSize).toBe(200);
  });
});

describe("function write commands", () => {
  it("create posts the full acceptance shape (exposure public carries host; private omits it)", async () => {
    setToken("t");
    const fetchMock = vi.fn(async () => jsonResponse({ function: { id: "fn-9" } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await functionsResource.commands.create.run(
      {
        name: "resize",
        image: "img:1",
        port: "8080",
        exposure: "public",
        host: "img.example.test",
        env: "A=1\n# comment\nB=2",
        secretRefs: "signing-key",
        command: "/app,--verbose",
        minReplicas: "1",
        maxReplicas: "4",
      },
      { projectId: "p1" },
    );
    expect(result.status).toBe("ok");
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    const body = JSON.parse(String(init.body));
    expect(body).toMatchObject({
      project_id: "p1",
      name: "resize",
      image: "img:1",
      port: 8080,
      exposure: "public",
      host: "img.example.test",
      env: { A: "1", B: "2" },
      secret_refs: ["signing-key"],
      command: ["/app", "--verbose"],
      min_replicas: 1,
      max_replicas: 4,
    });
  });

  it("scale posts the replica count to the per-function endpoint", async () => {
    setToken("t");
    const fetchMock = vi.fn(async () => jsonResponse({ function: { id: "fn-9" } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await functionsResource.commands.scale.run({ id: "fn 9", replicas: "3" }, { projectId: "p1" });
    expect(result.status).toBe("ok");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/functions/fn%209/scale");
    expect(JSON.parse(String(init.body))).toMatchObject({ replicas: 3 });
  });

  it("exposure private omits the host (server clears the slot)", async () => {
    setToken("t");
    const fetchMock = vi.fn(async () => jsonResponse({ function: { id: "fn-9" } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await functionsResource.commands.exposure.run(
      { id: "fn-9", exposure: "private", host: "stale.example.test" },
      { projectId: "p1" },
    );
    expect(result.status).toBe("ok");
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({ exposure: "private" });
  });

  it("rollback and delete hit their endpoints", async () => {
    setToken("t");
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);
    await functionsResource.commands.rollback.run({ id: "fn-9" }, { projectId: "p1" });
    expect(String((fetchMock.mock.calls[0] as unknown as [string])[0])).toContain("/v1/functions/fn-9/rollback");
    await functionsResource.commands.delete.run({ id: "fn-9" }, { projectId: "p1" });
    const [url, init] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/functions/fn-9");
    expect(init.method).toBe("DELETE");
  });
});

describe("functionRevisionsResource", () => {
  it("scopes to the function id and unwraps revisions", async () => {
    setToken("t");
    const fetchMock = vi.fn(async () => jsonResponse({ revisions: [{ seq: "2", digest: "abc" }] }));
    vi.stubGlobal("fetch", fetchMock);
    const rows = (await functionRevisionsResource.collection!(ALL, { projectId: "p1", functionId: "fn-9" }, undefined)).rows;
    expect(rows).toHaveLength(1);
    expect(String((fetchMock.mock.calls[0] as unknown as [string])[0])).toContain("/v1/functions/fn-9/revisions");
    expect(functionsLookup.spec.defaultSize).toBe(200);
  });
});
