import { afterEach, describe, expect, it, vi } from "vitest";
import { runCommand } from "@lynx-go/uikit-core/kernel";
import {
  createRouteCommand,
  fetchRoutes,
  removeRouteCommand,
  routesResource,
  routesSearch,
} from "./routes";

// Routes 域资源适配单测（最标准单列表形态）：端点/信封收敛、空 scope 不
// 发请求、URL 编解码往返。fetch 面 stub 在 global（kit http 管线调用裸
// fetch）。

afterEach(() => {
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("fetchRoutes（收敛 + 作用域参数）", () => {
  it("整取 /v1/routes?project_id= 信封 routes（undefined 行过滤）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        routes: [
          { id: "r1", host: "shop.example", app_id: "a1", process: "web", port: 8080, tls_mode: "auto" },
          undefined,
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchRoutes({ projectId: "shop" });

    expect(rows).toEqual([
      { id: "r1", host: "shop.example", app_id: "a1", process: "web", port: 8080, tls_mode: "auto" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/routes?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchRoutes({ projectId: "" })).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("routesSearch（URL 编解码契约——host/app_id/process 三字段）", () => {
  it("q 编码往返为三字段 contains 谓词；decode 容忍缺省", () => {
    const encoded = routesSearch.encode({
      predicate: { any: [
        { field: "host", op: "contains", value: "shop" },
        { field: "app_id", op: "contains", value: "shop" },
        { field: "process", op: "contains", value: "shop" },
      ] },
      window: { size: 20 },
    });
    expect(encoded.q).toBe("shop");
    const decoded = routesSearch.decode({ q: "shop" });
    expect(decoded.predicate).toEqual({
      any: [
        { field: "host", op: "contains", value: "shop" },
        { field: "app_id", op: "contains", value: "shop" },
        { field: "process", op: "contains", value: "shop" },
      ],
    });
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = routesSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});

describe("routes 命令族（批 2 写路径命令化）", () => {
  it("create：POST /v1/routes 表单展示形在 run 内收敛（port→number、path 空串→不发送）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    const result = await runCommand(
      createRouteCommand,
      { host: "shop.example", path: "", app: "a1", process: "web", port: "8080", protocol: "http", tls: "auto" },
      { projectId: "shop" },
    );

    expect(result).toEqual({ status: "ok", payload: null });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/routes");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      project_id: "shop",
      host: "shop.example",
      app_id: "a1",
      process: "web",
      port: 8080,
      protocol: "http",
      tls_mode: "auto",
    });
  });

  it("结果两分：400→rejected 保留 apperr code、409→conflict", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_ROUTE_CONFLICT", message: "host taken" }, 409)));
    await expect(
      runCommand(
        createRouteCommand,
        { host: "h", path: "/", app: "a1", process: "web", port: "80", protocol: "http", tls: "auto" },
        { projectId: "shop" },
      ),
    ).resolves.toEqual({ status: "conflict", message: "host taken" });

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INVALID_ARGUMENT", message: "bad port" }, 400)));
    await expect(
      runCommand(
        createRouteCommand,
        { host: "h", path: "", app: "a1", process: "web", port: "x", protocol: "http", tls: "auto" },
        { projectId: "shop" },
      ),
    ).resolves.toEqual({ status: "rejected", code: "E_INVALID_ARGUMENT", message: "bad port" });
  });

  it("remove：DELETE /v1/routes/{id}（URL 编码）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await runCommand(removeRouteCommand, { routeId: "r 1" }, { projectId: "shop" });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/routes/r%201");
    expect(init.method).toBe("DELETE");
  });

  it("失效桥零读者退役（F1 回流批审计——kit selector 单源）", () => {
    expect(routesResource.legacyKeys).toBeUndefined();
  });
});
