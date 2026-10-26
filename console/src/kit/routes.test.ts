import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchRoutes, routesSearch } from "./routes";

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
