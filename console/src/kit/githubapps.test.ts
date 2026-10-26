import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { invalidateAfterCommand } from "@lynx-go/uikit-core";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { fetchGitHubApps, githubAppsResource, githubAppsSearch } from "./githubapps";

// GitHub Apps 域适配器单测（批1 Wave2-A）：双语境端点锚（project_id 面 /
// global 面）、protojson 行收敛、URL 编解码往返。fetch 面 stub 在 global
//（fetchRows 走 apiFetch 裸 fetch 管线）。

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

describe("fetchGitHubApps（双语境端点 + 收敛）", () => {
  it("projectId 在场打 /v1/github-apps?project_id=… 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        apps: [
          { id: "g1", project_id: "shop", account_login: "acme", app_id: "123", installation_id: "456" },
          undefined,
          { account_login: "no-id" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchGitHubApps({ projectId: "shop" });

    expect(rows).toEqual([
      { id: "g1", project_id: "shop", account_login: "acme", app_id: "123", installation_id: "456" },
      { account_login: "no-id" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/github-apps?project_id=shop");
  });

  it("projectId 缺席打 global 面（?global=true，不携带 project_id）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ apps: [{ id: "g0", account_login: "root" }] }));
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchGitHubApps({});

    expect(rows).toEqual([{ id: "g0", account_login: "root" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/github-apps?global=true");
  });

  it("非 2xx 走统一错误信封（code/message 抛出）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(fetchGitHubApps({})).rejects.toThrowError(/boom/);
  });
});

describe("githubAppsSearch（URL 编解码契约）", () => {
  it("q 编码往返为 contains 谓词（account_login/id）；decode 容忍缺省", () => {
    const fields = ["account_login", "id"];
    const encoded = githubAppsSearch.encode({
      predicate: textPredicate(fields, "acme"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("acme");
    const decoded = githubAppsSearch.decode({ q: "acme" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "acme"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = githubAppsSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});

// 命令锚（批2 写面 useCommand 化）：remove 代表落行动词族（put/
// manifestComplete 同 invalidates/legacyKeys 声明）——allScopes 双语境集合
// 失效 + ["github-apps"] 存量键桥（repos 子查询）+ 结果两分映射。
describe("命令锚：remove（映射 + 双语境失效桥）", () => {
  it("ok → global/project 两面 kit 集合键与存量 [github-apps] repos 键同批失效", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({})));
    const result = await githubAppsResource.commands.remove.run({ id: "g1" });
    expect(result.status).toBe("ok");

    const queryClient = new QueryClient();
    // collectionKey 同形：scope 段分面 {projectId:…} / {}，allScopes 命令
    // 两侧都清（project 行 shadow global 行——旧整前缀失效语义）
    const globalKey = ["collection", "github-apps", {}, null, null, { size: 20, cursor: null, offset: null }];
    const projectKey = ["collection", "github-apps", { projectId: "shop" }, null, null, { size: 20, cursor: null, offset: null }];
    const reposKey = ["github-apps", "g1", "repos"];
    queryClient.setQueryData(globalKey, []);
    queryClient.setQueryData(projectKey, []);
    queryClient.setQueryData(reposKey, []);
    invalidateAfterCommand(queryClient, githubAppsResource.commands.remove, {});
    expect(queryClient.getQueryState(globalKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(projectKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(reposKey)?.isInvalidated).toBe(true);
  });

  it("409 → conflict；remove 打 DELETE /v1/github-apps/{id}", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ code: "E_GITHUB_APP_IN_USE", message: "in use" }, 409));
    vi.stubGlobal("fetch", fetchMock);
    await expect(githubAppsResource.commands.remove.run({ id: "g1" })).resolves.toEqual({
      status: "conflict",
      message: "in use",
    });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/github-apps/g1");
    expect(init.method).toBe("DELETE");
  });

  it("manifestComplete 流态恢复失败 → rejected（非传输失败）；可解码时 project_id 取自载荷", async () => {
    await expect(
      githubAppsResource.commands.manifestComplete.run({ state: "not-a-jwt", installationId: "5" }),
    ).resolves.toEqual({
      status: "rejected",
      code: "flow_state_unrecoverable",
      message: "the flow state could not be recovered — restart the flow from the GitHub Apps page",
    });
    // base64url({"p":"shop"}) 载荷
    const state = `${btoa(JSON.stringify({ p: "shop" })).replace(/\+/g, "-").replace(/\//g, "_")}.sig`;
    const fetchMock = vi.fn(async () => jsonResponse({ app: { id: "g9", project_id: "shop" } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await githubAppsResource.commands.manifestComplete.run({ state, installationId: "5" });
    expect(result).toEqual({ status: "ok", payload: { app: { id: "g9", project_id: "shop" } } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/github-apps/manifest/complete");
    expect(JSON.parse(String(init.body))).toEqual({ state, installation_id: 5, project_id: "shop" });
  });
});
