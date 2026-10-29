import { afterEach, describe, expect, it, vi } from "vitest";
import { runCommand } from "@lynx-go/uikit-core/kernel";
import {
  configsResource,
  fetchConfigs,
  fetchSecrets,
  fetchSharedVariables,
  putConfigCommand,
  putSecretCommand,
  putSharedVariableCommand,
  removeSecretCommand,
  removeSharedVariableCommand,
  secretsSearch,
  configsSearch,
  secretsResource,
  sharedVariablesResource,
} from "./variables";

// Variables 域资源适配单测：三资源端点/信封收敛、空 scope 不发请求、URL
// 编解码往返；批 2 命令族锚（载荷契约、结果两分、失效桥键清单）。fetch
// 面 stub 在 global（kit http 管线调用裸 fetch）。

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

describe("secretsSearch/configsSearch/sharedVarsSearch（URL 编解码契约——三 namespace）", () => {
  it("q 编码往返为 name contains 谓词（单字段仍是 any 单叶）；decode 容忍缺省", () => {
    const encoded = secretsSearch.encode({
      predicate: { any: [{ field: "name", op: "contains", value: "api" }] },
      window: { size: 20 },
    });
    // namespace 物理键（F1 回流批）：secrets.q；他命名空间键不携带
    expect(encoded["secrets.q"]).toBe("api");
    const decoded = secretsSearch.decode({ "secrets.q": "api" });
    expect(configsSearch.decode({ "secrets.q": "api" }).predicate).toBeUndefined();
    expect(decoded.predicate).toEqual({ any: [{ field: "name", op: "contains", value: "api" }] });
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = secretsSearch.patch({ predicate: null });
    expect("secrets.q" in patch).toBe(true);
    expect(patch["secrets.q"]).toBeUndefined();
    expect(patch["secrets.page"]).toBeUndefined();
  });
});

describe("variables 命令族（批 2 写路径命令化）", () => {
  it("secrets put/remove：PUT/DELETE 端点与路径（ctx 线程 project_id，URL 编码）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await runCommand(putSecretCommand, { name: "api key", value: "s3cr3t" }, { projectId: "shop" });
    await runCommand(removeSecretCommand, { name: "api key" }, { projectId: "shop" });

    expect(String(fetchMock.mock.calls[0][0])).toBe("/v1/secrets");
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      project_id: "shop",
      name: "api key",
      value: "s3cr3t",
    });
    const [delUrl, delInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(String(delUrl)).toBe("/v1/secrets/shop/api%20key");
    expect(delInit.method).toBe("DELETE");
  });

  it("configs put：ok 载荷带 version（ConfigDetailDialog 冻结版本回读面）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ config: { version: "4" } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await runCommand(putConfigCommand, { name: "config.yaml", content: "SELECT 2;\n" }, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: { config: { version: "4" } } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/configs");
    expect(JSON.parse(String(init.body))).toEqual({ project_id: "shop", name: "config.yaml", content: "SELECT 2;\n" });
  });

  it("shared-variables put/remove：ok 载荷带 affected_apps（redeploy 提示链数据源）", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse({ affected_apps: ["a1"] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const put = await runCommand(putSharedVariableCommand, { name: "LOG_LEVEL", value: "debug" }, { projectId: "shop" });
    expect(put).toEqual({ status: "ok", payload: { affected_apps: ["a1"] } });

    const del = await runCommand(removeSharedVariableCommand, { name: "LOG_LEVEL" }, { projectId: "shop" });
    expect(del).toEqual({ status: "ok", payload: { affected_apps: ["a1"] } });
    expect(String(fetchMock.mock.calls[1][0])).toBe("/v1/shared-variables/shop/LOG_LEVEL");
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("结果两分：400→rejected 保留 apperr code、409→conflict、500→throw kernel ApiError", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INVALID_ARGUMENT", message: "empty name" }, 400)));
    await expect(
      runCommand(putSharedVariableCommand, { name: "", value: "v" }, { projectId: "shop" }),
    ).resolves.toEqual({ status: "rejected", code: "E_INVALID_ARGUMENT", message: "empty name" });

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_FROZEN", message: "immutable" }, 409)));
    await expect(
      runCommand(removeSecretCommand, { name: "x" }, { projectId: "shop" }),
    ).resolves.toEqual({ status: "conflict", message: "immutable" });

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_INTERNAL", message: "boom" }, 500)));
    await expect(runCommand(putConfigCommand, { name: "x", content: "y" }, { projectId: "shop" })).rejects.toEqual({
      kind: "server",
      status: 500,
      message: "E_INTERNAL: boom",
    });
  });

  it("失效桥键清单（旧 useApiMutation invalidate 等值；configs 含详情宽前缀）", () => {
    expect(secretsResource.legacyKeys?.({ projectId: "shop" })).toEqual([["resources", "secrets", "shop"]]);
    expect(configsResource.legacyKeys?.({ projectId: "shop" })).toEqual([
      ["resources", "configs", "shop"],
      ["resources", "config", "shop"],
    ]);
    expect(sharedVariablesResource.legacyKeys?.({ projectId: "shop" })).toEqual([
      ["resources", "shared-variables", "shop"],
    ]);
  });
});
