import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { appSpecsLookup, appSpecsResource } from "./app-specs";

// App 冻结 Spec 反查适配器单测（批4；uikit 0.5.0 回流批改 restRecord
// notFound 口径）：fan-out 端点锚（apps 整取 → 逐 App GetAppSpec）、404
// 缺席语义（未部署 App = 空段非错误——经 notFound:"null" 收敛）、失败
// 透传、scope 声明与存量键桥。fetch 面 stub 在 global（authFetch→裸
// fetch 管线）；断言经资源适配器调用取数。

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

const ALL = appSpecsLookup.decode({});

describe("appSpecsResource 读面（fan-out + 收敛）", () => {
  it("apps 整取后逐 App GET /v1/apps/{id}/spec，行形态 = appId + spec 载荷", async () => {
    const fetchMock = vi.fn(async (path: string) => {
      if (path.startsWith("/v1/apps?")) {
        return jsonResponse({ apps: [{ id: "A1", name: "alpha" }, { id: "A2", name: "beta" }] });
      }
      if (path === "/v1/apps/A1/spec") {
        return jsonResponse({ spec: { processes: [{ name: "web", replicas: "2" }] } });
      }
      if (path === "/v1/apps/A2/spec") {
        return jsonResponse({ spec: { processes: [{ name: "worker", replicas: "1" }] } });
      }
      return jsonResponse({}, 404);
    });
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await appSpecsResource.collection!(ALL, { projectId: "P1" }, undefined)).rows;

    expect(rows).toEqual([
      { appId: "A1", spec: { processes: [{ name: "web", replicas: "2" }] } },
      { appId: "A2", spec: { processes: [{ name: "worker", replicas: "1" }] } },
    ]);
    const urls = fetchMock.mock.calls.map(([url]) => String(url));
    expect(urls).toContain("/v1/apps?project_id=P1&limit=200");
    expect(urls).toContain("/v1/apps/A1/spec");
    expect(urls).toContain("/v1/apps/A2/spec");
  });

  it("未部署 App（spec 404 → notFound null）= 缺席非错误；其余失败如实上抛", async () => {
    const fetchMock = vi.fn(async (path: string) => {
      if (path.startsWith("/v1/apps?")) {
        return jsonResponse({ apps: [{ id: "A1", name: "alpha" }, { id: "A2", name: "beta" }] });
      }
      if (path === "/v1/apps/A1/spec") {
        return jsonResponse({ code: "not_found", message: "no deployment" }, 404);
      }
      if (path === "/v1/apps/A2/spec") {
        return jsonResponse({ spec: { processes: [] } });
      }
      return jsonResponse({}, 404);
    });
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await appSpecsResource.collection!(ALL, { projectId: "P1" }, undefined)).rows;
    expect(rows).toEqual([{ appId: "A2", spec: { processes: [] } }]);
  });

  it("apps 整取失败如实上抛（fan-out 首段错误不吞——工厂 ApiError）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)),
    );
    await expect(appSpecsResource.collection!(ALL, { projectId: "P1" }, undefined)).rejects.toMatchObject({
      kind: "server",
      status: 503,
      message: "boom",
    });
  });

  it("scope 缺 projectId 时零请求空段", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const rows = (await appSpecsResource.collection!(ALL, {}, undefined)).rows;
    expect(rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("appSpecsResource（scope/失效桥/lookup schema）", () => {
  it("scope 声明 projectId；legacyKeys 桥零读者退役（F1 回流批审计）", () => {
    expect(appSpecsResource.scope).toEqual(["projectId"]);
    expect(appSpecsResource.legacyKeys).toBeUndefined();
  });

  it("appSpecsLookup 缺省窗口覆盖 fan-out 上界（≤50 App 行集不截断）", () => {
    expect(appSpecsLookup.decode({}).window.size).toBe(200);
  });
});
