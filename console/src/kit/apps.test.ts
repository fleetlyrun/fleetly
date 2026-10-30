import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { appsResource, appsSearch } from "./apps";

// uikit 适配器族单测（试点切片；uikit 0.5.0 回流批改 restFetchAll 口径）：
// protojson 行收敛、作用域参数与鉴权头、空 scope 不发请求、URL 编解码
// 往返。fetch 面 stub 在 global（authFetch→裸 fetch 管线）；断言经资源
// 适配器调用取数（fetchApps 仍是 fan-out 消费面出口，锚在资源面钉死）。

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

describe("appsResource 读面（protojson 收敛 + 作用域参数 + 鉴权头）", () => {
  it("整取 /v1/apps 并把宽松生成类型收敛为非空 AppEntry", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        apps: [
          { id: "a1", name: "web", project_id: "shop", created_at: "2026-01-01T00:00:00Z" },
          undefined,
          { id: "", name: "no-id" },
          { name: "no-id-2" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await appsResource.collection!(ALL, { projectId: "shop" }, undefined)).rows;

    expect(rows).toEqual([
      { id: "a1", project_id: "shop", name: "web", created_at: "2026-01-01T00:00:00Z" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/apps?");
    expect(String(url)).toContain("project_id=shop");
    expect(String(url)).toContain("limit=200");
  });

  it("空 projectId 不发请求（ListApps project_id 必填契约——守卫门）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await appsResource.collection!(ALL, {}, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("持有凭证时带 Bearer 头", async () => {
    setToken("tok-1");
    const fetchMock = vi.fn(async () => jsonResponse({ apps: [] }));
    vi.stubGlobal("fetch", fetchMock);

    await appsResource.collection!(ALL, { projectId: "shop" }, undefined);

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer tok-1");
  });

  it("非 2xx 收敛 kernel ApiError（工厂 Response→ApiError 单源）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(appsResource.collection!(ALL, { projectId: "shop" }, undefined)).rejects.toMatchObject({
      kind: "server",
      status: 503,
      message: "boom",
    });
  });
});

describe("appsSearch（URL 编解码契约）", () => {
  it("q 编码往返为 contains 谓词；decode 容忍缺省", () => {
    const encoded = appsSearch.encode({
      predicate: { any: [
        { field: "name", op: "contains", value: "web" },
        { field: "id", op: "contains", value: "web" },
      ] },
      window: { size: 20 },
    });
    expect(encoded.q).toBe("web");
    const decoded = appsSearch.decode({ q: "web" });
    expect(decoded.predicate).toEqual({
      any: [
        { field: "name", op: "contains", value: "web" },
        { field: "id", op: "contains", value: "web" },
      ],
    });
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = appsSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});

describe("apps commands（批2 写路径命令锚）", () => {
  it("create：POST /v1/apps，projectId 取 ctx scope，name trim 后发送", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ app: { id: "a_new" } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await appsResource.commands.create.run({ name: "  web  " }, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: { appId: "a_new" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/apps");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ project_id: "shop", name: "web" });
  });

  it("create：400 → rejected（结果通道，code 保留）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "E_ALREADY_EXISTS", message: "app name already used" }, 400)),
    );
    const result = await appsResource.commands.create.run({ name: "web" }, { projectId: "shop" });
    expect(result).toEqual({ status: "rejected", code: "E_ALREADY_EXISTS", message: "app name already used" });
  });
});
