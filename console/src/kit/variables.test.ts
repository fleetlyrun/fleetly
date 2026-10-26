import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchConfigs, fetchSecrets, fetchSharedVariables, variablesSearch } from "./variables";

// Variables 域资源适配单测：三资源端点/信封收敛、空 scope 不发请求、URL
// 编解码往返。fetch 面 stub 在 global（kit http 管线调用裸 fetch）。

afterEach(() => {
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("三资源 fetch（端点 + 信封 + 收敛）", () => {
  it("secrets 走 /v1/secrets 信封 secrets；configs 走 /v1/configs 信封 configs", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith("/v1/secrets")) {
        return jsonResponse({ secrets: [{ id: "s1", name: "api-key", updated_at: "2026-01-01T00:00:00Z" }, undefined] });
      }
      if (url.startsWith("/v1/configs")) {
        return jsonResponse({ configs: [{ id: "c1", name: "nginx.conf", version: "3" }] });
      }
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    expect(await fetchSecrets({ projectId: "shop" })).toEqual([
      { id: "s1", name: "api-key", updated_at: "2026-01-01T00:00:00Z" },
    ]);
    expect(await fetchConfigs({ projectId: "shop" })).toEqual([{ id: "c1", name: "nginx.conf", version: "3" }]);
    const urls = fetchMock.mock.calls.map((call) => String(call[0]));
    expect(urls[0]).toContain("/v1/secrets?");
    expect(urls[0]).toContain("project_id=shop");
    expect(urls[1]).toContain("/v1/configs?");
    expect(urls[1]).toContain("project_id=shop");
  });

  it("shared-variables 走 /v1/shared-variables 信封 variables", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ variables: [{ id: "v1", name: "LOG_LEVEL", value: "debug", updated_at: "2026-01-02T00:00:00Z" }] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    expect(await fetchSharedVariables({ projectId: "shop" })).toEqual([
      { id: "v1", name: "LOG_LEVEL", value: "debug", updated_at: "2026-01-02T00:00:00Z" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/shared-variables?");
    expect(String(url)).toContain("project_id=shop");
  });

  it("空 projectId 不发请求（三资源同契约）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchSecrets({})).toEqual([]);
    expect(await fetchConfigs({ projectId: "" })).toEqual([]);
    expect(await fetchSharedVariables({ projectId: undefined })).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("variablesSearch（URL 编解码契约——三 tab 共享 name 单字段）", () => {
  it("q 编码往返为 name contains 谓词（单字段仍是 any 单叶）；decode 容忍缺省", () => {
    const encoded = variablesSearch.encode({
      predicate: { any: [{ field: "name", op: "contains", value: "api" }] },
      window: { size: 20 },
    });
    expect(encoded.q).toBe("api");
    const decoded = variablesSearch.decode({ q: "api" });
    expect(decoded.predicate).toEqual({ any: [{ field: "name", op: "contains", value: "api" }] });
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = variablesSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
