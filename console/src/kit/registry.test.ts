import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchRegistryRows, registrySearch } from "./registry";

// Registry 域资源适配单测：三级合并（apps→deployments→revisions）收敛、
// 子轴失败容忍、空 scope 不发请求、URL 编解码往返。fetch 面 stub 在
// global（kit http 管线调用裸 fetch）。

afterEach(() => {
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubRegistryBackend(overrides: { revisions?: () => Response; deployments?: () => Response } = {}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.startsWith("/v1/apps")) {
      return jsonResponse({
        apps: [
          { id: "a1", name: "web", project_id: "shop", created_at: "2026-01-01T00:00:00Z" },
          { id: "a2", name: "worker", project_id: "shop", created_at: "2026-01-02T00:00:00Z" },
        ],
      });
    }
    if (url.startsWith("/v1/deployments")) {
      if (overrides.deployments) return overrides.deployments();
      const appId = url.includes("app_id=a2") ? "a2" : "a1";
      return jsonResponse({
        deployments: [
          // 服务端序最新在前（latestDeploymentPerApp 取首行）
          { id: `d-${appId}-2`, app_id: appId, to_revision: appId === "a2" ? "r2" : "r1", updated_at: "2026-02-02T00:00:00Z" },
          { id: `d-${appId}-1`, app_id: appId, to_revision: "r0", updated_at: "2026-01-01T00:00:00Z" },
        ],
      });
    }
    if (url.startsWith("/v1/revisions")) {
      if (overrides.revisions) return overrides.revisions();
      const appId = url.includes("app_id=a2") ? "a2" : "a1";
      return jsonResponse({
        revisions: [{ id: appId === "a2" ? "r2" : "r1", digest: appId === "a2" ? "sha256:worker" : "sha256:web" }],
      });
    }
    return jsonResponse({});
  });
}

describe("fetchRegistryRows（三级合并收敛）", () => {
  it("apps→deployments→revisions 并轨为携带最新部署与 digest 的行（App 字段平铺）", async () => {
    const fetchMock = stubRegistryBackend();
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchRegistryRows({ projectId: "shop" });

    expect(rows).toEqual([
      {
        id: "a1",
        name: "web",
        created_at: "2026-01-01T00:00:00Z",
        deployment: { id: "d-a1-2", app_id: "a1", to_revision: "r1", updated_at: "2026-02-02T00:00:00Z" },
        digest: "sha256:web",
      },
      {
        id: "a2",
        name: "worker",
        created_at: "2026-01-02T00:00:00Z",
        deployment: { id: "d-a2-2", app_id: "a2", to_revision: "r2", updated_at: "2026-02-02T00:00:00Z" },
        digest: "sha256:worker",
      },
    ]);
    const urls = fetchMock.mock.calls.map((call) => String(call[0]));
    expect(urls.some((url) => url.includes("/v1/apps?") && url.includes("project_id=shop"))).toBe(true);
    expect(urls.some((url) => url.includes("/v1/deployments?") && url.includes("app_id=a1"))).toBe(true);
    expect(urls.some((url) => url.includes("/v1/revisions?") && url.includes("app_id=a1"))).toBe(true);
  });

  it("revisions 子轴失败收敛为 digest 缺席（列表仍可用——旧 useQueries 失败行同语义）", async () => {
    vi.stubGlobal(
      "fetch",
      stubRegistryBackend({ revisions: () => jsonResponse({ code: "E_X", message: "boom" }, 503) }),
    );

    const rows = await fetchRegistryRows({ projectId: "shop" });

    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ id: "a1", name: "web" });
    expect(rows[0].digest).toBeUndefined();
    expect(rows[0].deployment).toBeDefined();
  });

  it("apps 轴失败整列表失败（旧 apps.isError 错误态同口径）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(fetchRegistryRows({ projectId: "shop" })).rejects.toThrowError(/boom/);
  });

  it("空 projectId 不发请求", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchRegistryRows({ projectId: "" })).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("registrySearch（URL 编解码契约——app.name/app.id 平铺直达）", () => {
  it("q 编码往返为双字段 contains 谓词；decode 容忍缺省", () => {
    const encoded = registrySearch.encode({
      predicate: { any: [
        { field: "name", op: "contains", value: "web" },
        { field: "id", op: "contains", value: "web" },
      ] },
      window: { size: 20 },
    });
    expect(encoded.q).toBe("web");
    const decoded = registrySearch.decode({ q: "web" });
    expect(decoded.predicate).toEqual({
      any: [
        { field: "name", op: "contains", value: "web" },
        { field: "id", op: "contains", value: "web" },
      ],
    });
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = registrySearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
